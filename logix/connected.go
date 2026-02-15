package logix

import (
	"encoding/binary"
	"fmt"

	"github.com/yatesdr/plcio/cip"
	"github.com/yatesdr/plcio/eip"
	"github.com/yatesdr/plcio/logging"
)

var verboseLogging bool // Controls detailed template/parsing logs

// SetVerboseLogging enables or disables detailed template/parsing logs.
func SetVerboseLogging(verbose bool) {
	verboseLogging = verbose
}

// debugLog logs a message if debug logging is enabled.
func debugLog(format string, args ...interface{}) {
	logging.DebugLog("Logix", format, args...)
}

// debugLogVerbose logs detailed messages only when verbose logging is enabled.
func debugLogVerbose(format string, args ...interface{}) {
	if verboseLogging {
		logging.DebugLog("Logix", format, args...)
	}
}

// Connection size options
const (
	ConnectionSizeLarge = 4002 // Large Forward Open max size
	ConnectionSizeSmall = 504  // Standard Forward Open size
)

// OpenConnection establishes a CIP connection using Forward Open.
// This enables more efficient connected messaging for repeated operations.
// Attempts large connection size first, falls back to smaller size if rejected.
func (p *PLC) OpenConnection() error {
	if p == nil || p.Connection == nil {
		return fmt.Errorf("OpenConnection: nil plc or connection")
	}
	if p.cipConn != nil {
		return fmt.Errorf("OpenConnection: connection already open")
	}

	debugLog("OpenConnection %s: attempting Forward Open", p.IpAddress)

	// Try large connection size first, then fall back to small
	sizes := []uint16{ConnectionSizeLarge, ConnectionSizeSmall}

	var lastErr error
	for _, size := range sizes {
		err := p.tryForwardOpen(size)
		if err == nil {
			debugLog("OpenConnection %s: connected (size=%d bytes)", p.IpAddress, size)
			return nil // Success
		}
		debugLog("OpenConnection %s: Forward Open (size=%d) failed: %v", p.IpAddress, size, err)
		lastErr = err
	}

	return fmt.Errorf("OpenConnection: all connection sizes failed: %w", lastErr)
}

// tryForwardOpen attempts Forward Open with the specified connection size.
func (p *PLC) tryForwardOpen(connectionSize uint16) error {
	// Build connection path to the target
	connPath := p.buildConnectionPath()

	// Create Forward Open config
	cfg := cip.DefaultForwardOpenConfig()
	cfg.ConnectionPath = connPath
	cfg.OTConnectionSize = connectionSize
	cfg.TOConnectionSize = connectionSize

	// Use standard Forward Open (0x54) for sizes ≤511, Large (0x5B) for >511
	var reqData []byte
	var connSerial uint16
	var err error
	if connectionSize <= 511 {
		reqData, connSerial, err = cip.BuildForwardOpenRequestSmall(cfg)
	} else {
		reqData, connSerial, err = cip.BuildForwardOpenRequest(cfg)
	}
	if err != nil {
		return fmt.Errorf("tryForwardOpen: %w", err)
	}

	// Forward Open is sent directly (not via UCMM routing).
	// The connection path inside Forward Open contains the route.
	cpf := &eip.EipCommonPacket{
		Items: []eip.EipCommonPacketItem{
			{TypeId: eip.CpfAddressNullId, Length: 0, Data: nil},
			{TypeId: eip.CpfUnconnectedMessageId, Length: uint16(len(reqData)), Data: reqData},
		},
	}

	// Send via SendRRData
	resp, err := p.Connection.SendRRData(*cpf)
	if err != nil {
		return fmt.Errorf("tryForwardOpen: SendRRData failed: %w", err)
	}

	if len(resp.Items) < 2 {
		return fmt.Errorf("tryForwardOpen: expected 2 CPF items, got %d", len(resp.Items))
	}

	cipResp := resp.Items[1].Data

	if len(cipResp) < 4 {
		return fmt.Errorf("tryForwardOpen: response too short")
	}

	// Check CIP response status
	replyService := cipResp[0]
	status := cipResp[2]
	addlStatusSize := cipResp[3]

	// Accept reply for either Standard (0x54) or Large (0x5B) Forward Open
	if replyService != (cip.SvcForwardOpen|0x80) && replyService != (cip.SvcForwardOpenLarge|0x80) {
		return fmt.Errorf("tryForwardOpen: unexpected reply service: 0x%02X", replyService)
	}

	if status != 0x00 {
		// Extract extended status for more detail
		extStatus := uint16(0)
		if addlStatusSize >= 1 && len(cipResp) >= 6 {
			extStatus = binary.LittleEndian.Uint16(cipResp[4:6])
		}
		return fmt.Errorf("tryForwardOpen (size=%d): Forward Open failed - status=0x%02X, extStatus=0x%04X, path=% X",
			connectionSize, status, extStatus, connPath)
	}

	// Parse Forward Open response
	dataStart := 4 + int(addlStatusSize)*2
	if dataStart >= len(cipResp) {
		return fmt.Errorf("tryForwardOpen: response missing data")
	}

	foResp, err := cip.ParseForwardOpenResponse(cipResp[dataStart:])
	if err != nil {
		return fmt.Errorf("tryForwardOpen: %w", err)
	}

	// Create and store the connection
	p.cipConn = &cip.Connection{
		OTConnID:     foResp.OTConnectionID,
		TOConnID:     foResp.TOConnectionID,
		SerialNumber: connSerial,
		VendorID:     cfg.VendorID,
		OrigSerial:   cfg.OriginatorSerial,
	}
	p.connPath = connPath
	p.connSize = connectionSize

	return nil
}

// CloseConnection tears down the CIP connection using Forward Close.
func (p *PLC) CloseConnection() error {
	if p == nil || p.Connection == nil {
		return nil
	}
	if p.cipConn == nil {
		return nil // Not connected
	}

	// Build Forward Close request
	reqData, err := cip.BuildForwardCloseRequest(p.cipConn, p.connPath)
	if err != nil {
		p.cipConn = nil
		return fmt.Errorf("CloseConnection: %w", err)
	}

	// Wrap in CPF for unconnected messaging
	cpf := &eip.EipCommonPacket{
		Items: []eip.EipCommonPacketItem{
			{TypeId: eip.CpfAddressNullId, Length: 0, Data: nil},
			{TypeId: eip.CpfUnconnectedMessageId, Length: uint16(len(reqData)), Data: reqData},
		},
	}

	// Send and ignore errors (best-effort close)
	_, _ = p.Connection.SendRRData(*cpf)

	p.cipConn = nil
	p.connPath = nil
	return nil
}

// IsConnected returns true if the PLC connection is active.
// For connected messaging, this checks the CIP connection.
// For unconnected messaging, this checks the underlying EIP/TCP connection.
func (p *PLC) IsConnected() bool {
	if p == nil {
		return false
	}
	// For connected messaging, check CIP connection
	if p.cipConn != nil {
		return true
	}
	// For unconnected messaging, check TCP connection
	return p.Connection != nil && p.Connection.IsConnected()
}

// ReadTagConnected reads a tag using connected messaging.
// Requires an open connection (call OpenConnection first).
func (p *PLC) ReadTagConnected(tagName string) (*Tag, error) {
	return p.ReadTagCountConnected(tagName, 1)
}

// ReadTagCountConnected reads multiple elements using connected messaging.
func (p *PLC) ReadTagCountConnected(tagName string, count uint16) (*Tag, error) {
	if p.cipConn == nil {
		return nil, fmt.Errorf("ReadTagConnected: no connection (call OpenConnection first)")
	}

	// Build the CIP request (same as unconnected)
	path, err := cip.EPath().Symbol(tagName).Build()
	if err != nil {
		return nil, fmt.Errorf("ReadTagConnected: %w", err)
	}

	reqData := make([]byte, 0, 2+len(path)+2)
	reqData = append(reqData, SvcReadTag)
	reqData = append(reqData, path.WordLen())
	reqData = append(reqData, path...)
	reqData = binary.LittleEndian.AppendUint16(reqData, count)

	// Wrap with sequence number for connected messaging
	connData := p.cipConn.WrapConnected(reqData)

	// Build CPF for connected messaging
	cpf := p.buildConnectedCpf(connData)

	// Send via SendUnitDataTransaction
	resp, err := p.Connection.SendUnitDataTransaction(*cpf)
	if err != nil {
		return nil, fmt.Errorf("ReadTagConnected: %w", err)
	}

	if len(resp.Items) < 2 {
		return nil, fmt.Errorf("ReadTagConnected: expected 2 CPF items")
	}

	// Unwrap connected response
	_, cipResp, err := p.cipConn.UnwrapConnected(resp.Items[1].Data)
	if err != nil {
		return nil, fmt.Errorf("ReadTagConnected: %w", err)
	}

	// Parse the Read Tag response
	tag, err := parseReadTagResponse(cipResp, tagName)
	if err != nil {
		return nil, fmt.Errorf("ReadTagConnected: %w", err)
	}

	return tag, nil
}

// ReadMultiple reads multiple tags in a single request using Multiple Service Packet.
// This is more efficient than reading tags one at a time.
// Requires an open connection for best performance, but works without one too.
func (p *PLC) ReadMultiple(tagNames []string) ([]*Tag, error) {
	if len(tagNames) == 0 {
		return nil, nil
	}

	// Build individual read requests
	requests := make([]cip.MultiServiceRequest, len(tagNames))
	for i, tagName := range tagNames {
		path, err := cip.EPath().Symbol(tagName).Build()
		if err != nil {
			return nil, fmt.Errorf("ReadMultiple: tag %q: %w", tagName, err)
		}

		requests[i] = cip.MultiServiceRequest{
			Service: SvcReadTag,
			Path:    path,
			Data:    []byte{0x01, 0x00}, // Element count = 1
		}
	}

	// Build Multiple Service Packet
	msData, err := cip.BuildMultipleServiceRequest(requests)
	if err != nil {
		return nil, fmt.Errorf("ReadMultiple: %w", err)
	}

	// Build the complete CIP request with MSP service and path
	msPath, _ := cip.EPath().Class(0x02).Instance(1).Build() // Message Router
	reqData := make([]byte, 0, 2+len(msPath)+len(msData))
	reqData = append(reqData, cip.SvcMultipleServicePacket)
	reqData = append(reqData, msPath.WordLen())
	reqData = append(reqData, msPath...)
	reqData = append(reqData, msData...)

	var cipResp []byte

	if p.cipConn != nil {
		// Use connected messaging
		connData := p.cipConn.WrapConnected(reqData)
		cpf := p.buildConnectedCpf(connData)

		resp, err := p.Connection.SendUnitDataTransaction(*cpf)
		if err != nil {
			return nil, fmt.Errorf("ReadMultiple: %w", err)
		}

		if len(resp.Items) < 2 {
			return nil, fmt.Errorf("ReadMultiple: expected 2 CPF items")
		}

		_, cipResp, err = p.cipConn.UnwrapConnected(resp.Items[1].Data)
		if err != nil {
			return nil, fmt.Errorf("ReadMultiple: %w", err)
		}
	} else {
		// Use unconnected messaging
		var cpf *eip.EipCommonPacket
		if len(p.RoutePath) > 0 {
			cpf = buildRoutedCpf(reqData, p.RoutePath)
		} else {
			cpf = buildDirectCpf(reqData)
		}

		resp, err := p.Connection.SendRRData(*cpf)
		if err != nil {
			return nil, fmt.Errorf("ReadMultiple: %w", err)
		}

		if len(resp.Items) < 2 {
			return nil, fmt.Errorf("ReadMultiple: expected 2 CPF items")
		}

		cipResp = resp.Items[1].Data

		// Unwrap UCMM if routed
		if len(p.RoutePath) > 0 {
			cipResp, err = unwrapUCMMResponse(cipResp)
			if err != nil {
				return nil, fmt.Errorf("ReadMultiple: %w", err)
			}
		}
	}

	// Parse Multiple Service Packet response header
	if len(cipResp) < 4 {
		return nil, fmt.Errorf("ReadMultiple: response too short")
	}

	replyService := cipResp[0]
	status := cipResp[2]
	addlStatusSize := cipResp[3]

	if replyService != (cip.SvcMultipleServicePacket | 0x80) {
		return nil, fmt.Errorf("ReadMultiple: unexpected reply service: 0x%02X", replyService)
	}

	// Status 0x1E = "Embedded service error" means MSP succeeded but some services failed
	// We still parse individual responses to return what we can
	if status != 0x00 && status != 0x1E {
		return nil, fmt.Errorf("ReadMultiple: MSP failed with status 0x%02X", status)
	}

	// Parse individual responses
	dataStart := 4 + int(addlStatusSize)*2
	responses, err := cip.ParseMultipleServiceResponse(cipResp[dataStart:])
	if err != nil {
		return nil, fmt.Errorf("ReadMultiple: %w", err)
	}

	if len(responses) != len(tagNames) {
		return nil, fmt.Errorf("ReadMultiple: expected %d responses, got %d", len(tagNames), len(responses))
	}

	// Convert responses to Tags
	tags := make([]*Tag, len(tagNames))
	for i, resp := range responses {
		// Status 0x00 = success, 0x06 = partial transfer (OK for reads)
		if resp.Status != 0x00 && resp.Status != 0x06 {
			// Tag read failed - log the error for debugging
			var extStatus uint16
			if len(resp.ExtStatus) >= 2 {
				extStatus = binary.LittleEndian.Uint16(resp.ExtStatus)
			}
			debugLogVerbose("ReadMultiple: tag %q failed with status 0x%02X (%s), extStatus 0x%04X",
				tagNames[i], resp.Status, cipStatusName(resp.Status), extStatus)
			tags[i] = nil
			continue
		}

		if len(resp.Data) < 2 {
			tags[i] = nil
			continue
		}

		dataType := binary.LittleEndian.Uint16(resp.Data[0:2])
		tags[i] = &Tag{
			Name:     tagNames[i],
			DataType: dataType,
			Bytes:    resp.Data[2:],
		}
	}

	return tags, nil
}

// buildConnectionPath builds the connection path for Forward Open.
// Matches pylogix's _connected_path() exactly:
// route (port segment) + [0x20, 0x02, 0x24, 0x01] (Message Router class 2, instance 1)
func (p *PLC) buildConnectionPath() []byte {
	// Build the path: [port, slot] + [0x20, 0x02, 0x24, 0x01]
	path := make([]byte, 0, 6)

	if len(p.RoutePath) > 0 {
		path = append(path, p.RoutePath...)
	} else {
		path = append(path, 0x01, p.Slot)
	}

	// Add Message Router (class 2, instance 1) - pylogix always adds this
	path = append(path, 0x20, 0x02, 0x24, 0x01)

	return path
}

// Keepalive sends a NOP (No Operation) via connected messaging to keep
// the CIP ForwardOpen connection alive. This should be called periodically
// when no other connected operations are being performed.
// Returns nil if not connected (unconnected messaging mode).
func (p *PLC) Keepalive() error {
	if p.cipConn == nil {
		// Not using connected messaging, nothing to keep alive
		return nil
	}

	// Build NOP request targeting Identity object (class 1, instance 1)
	// Format: service, path_size, path (class segment, instance segment)
	reqData := []byte{
		SvcNop, // Service code 0x17
		0x02,   // Path size (2 words)
		0x20, 0x01, // Class segment: class 1 (Identity)
		0x24, 0x01, // Instance segment: instance 1
	}

	// Wrap with sequence number for connected messaging
	connData := p.cipConn.WrapConnected(reqData)

	// Build CPF for connected messaging
	cpf := p.buildConnectedCpf(connData)

	// Send via SendUnitDataTransaction
	resp, err := p.Connection.SendUnitDataTransaction(*cpf)
	if err != nil {
		return fmt.Errorf("Keepalive: %w", err)
	}

	if len(resp.Items) < 2 {
		return fmt.Errorf("Keepalive: expected 2 CPF items, got %d", len(resp.Items))
	}

	// Unwrap connected response to check status
	_, cipResp, err := p.cipConn.UnwrapConnected(resp.Items[1].Data)
	if err != nil {
		return fmt.Errorf("Keepalive: %w", err)
	}

	// Check CIP status - NOP should return success (0x00)
	// But we accept any non-error response as success for keepalive purposes
	if len(cipResp) >= 2 {
		status := cipResp[1]
		// 0x00 = success, 0x08 = service not supported (still a valid response)
		if status != 0x00 && status != StatusServiceNotSupport {
			return fmt.Errorf("Keepalive: CIP status 0x%02X", status)
		}
	}

	return nil
}

// buildConnectedCpf builds a CPF packet for connected messaging.
func (p *PLC) buildConnectedCpf(data []byte) *eip.EipCommonPacket {
	return &eip.EipCommonPacket{
		Items: []eip.EipCommonPacketItem{
			// Connected Address Item with O->T connection ID (per pylogix)
			{
				TypeId: eip.CpfAddressConnectionId,
				Length: 4,
				Data:   binary.LittleEndian.AppendUint32(nil, p.cipConn.OTConnID),
			},
			// Connected Data Item
			{
				TypeId: eip.CpfConnectedTransportPacketId,
				Length: uint16(len(data)),
				Data:   data,
			},
		},
	}
}
