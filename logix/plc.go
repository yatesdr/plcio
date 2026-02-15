package logix

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/yatesdr/plcio/cip"
	"github.com/yatesdr/plcio/eip"
	"github.com/yatesdr/plcio/logging"
)

// PLC is a thin Logix-specific wrapper over the generic eip client.
// Keep CIP/Logix logic here; eip remains transport + session + CPF.
type PLC struct {
	IpAddress  string
	Slot       byte           // CPU slot for ControlLogix (default 0)
	Connection *eip.EipClient

	// Routing controls how CIP requests are sent:
	// - nil or empty: send directly (for CompactLogix or direct CPU connection)
	// - non-empty: route via Connection Manager through specified path
	RoutePath []byte

	// CIP connection state (for connected messaging)
	cipConn  *cip.Connection // Active CIP connection (nil if not connected)
	connPath []byte          // Connection path used for Forward Open/Close
	connSize uint16          // Negotiated connection size
}

// Tag holds the raw data read from a PLC tag.
// Decoding is deferred - the caller interprets Bytes according to DataType.
type Tag struct {
	Name     string // Tag name as requested
	DataType uint16 // CIP data type code (e.g., 0xC1=BOOL, 0xC3=DINT, 0xCA=REAL)
	Bytes    []byte // Raw tag value bytes (little-endian)
}

// NewPLC creates the PLC wrapper (does not connect).
// If timeout > 0, it overrides the EIP client's default timeout.
func NewPLC(ipaddr string, timeout time.Duration) (PLC, error) {
	if ipaddr == "" {
		return PLC{}, fmt.Errorf("NewPLC: empty ipaddr")
	}
	if timeout > 0 {
		debugLog("NewPLC %s: timeout=%v", ipaddr, timeout)
	} else {
		debugLog("NewPLC %s: timeout=default (5s)", ipaddr)
	}
	c := eip.NewEipClient(ipaddr)
	if timeout > 0 {
		c.SetTimeout(timeout)
	}
	err := c.Connect()
	if err != nil {
		debugLog("NewPLC %s: connect failed: %v", ipaddr, err)
		return PLC{}, fmt.Errorf("NewPLC: failed to connect. %w", err)
	}
	debugLog("NewPLC %s: connected, session=0x%08X", ipaddr, c.GetSession())
	return PLC{IpAddress: ipaddr, Slot: 0, Connection: c}, nil
}

// Connect connects and registers a session (delegates to EIP client).
func (p *PLC) Connect() error {
	if p == nil || p.Connection == nil {
		return fmt.Errorf("PLC.Connect: nil plc/client")
	}
	return p.Connection.Connect()
}

// Close disconnects best-effort (no error).
func (p *PLC) Close() {
	if p == nil || p.Connection == nil {
		return
	}
	// Close CIP connection if open
	if p.cipConn != nil {
		_ = p.CloseConnection()
	}
	_ = p.Connection.Disconnect()
}

// SetRoutePath configures explicit routing for the PLC.
// Use this when connecting through a gateway or communication module.
// Pass nil or empty slice to disable routing (for direct connections).
func (p *PLC) SetRoutePath(path []byte) {
	if p == nil {
		return
	}
	p.RoutePath = path
}

// SetSlotRouting configures routing through backplane port 1 to a specific slot.
// Use this for ControlLogix when connecting via an Ethernet module (e.g., 1756-EN2T)
// and need to route to the CPU in a specific slot.
func (p *PLC) SetSlotRouting(slot byte) {
	if p == nil {
		return
	}
	p.RoutePath = []byte{0x01, slot} // Port 1 (backplane), link address = slot
}

// ReadTag reads a single tag by symbolic name and returns raw bytes + data type.
// The element count defaults to 1; use ReadTagCount for arrays.
func (p *PLC) ReadTag(tagName string) (*Tag, error) {
	return p.ReadTagCount(tagName, 1)
}

// ReadTagCount reads multiple elements of a tag (for arrays).
// For large arrays that exceed packet size, automatically reads in chunks using array indexing.
func (p *PLC) ReadTagCount(tagName string, count uint16) (*Tag, error) {
	return p.ReadTagCountWithInstance(tagName, count, 0)
}

// ReadTagCountWithInstance reads a tag using symbolic addressing.
// The instanceID parameter is currently unused but preserved for future compatibility.
// For large arrays that exceed packet size, automatically reads in chunks using array indexing.
func (p *PLC) ReadTagCountWithInstance(tagName string, count uint16, instanceID uint32) (*Tag, error) {
	if p == nil || p.Connection == nil {
		return nil, fmt.Errorf("ReadTag: nil plc or connection")
	}
	if tagName == "" {
		return nil, fmt.Errorf("ReadTag: empty tag name")
	}

	debugLogVerbose("ReadTag %q: count=%d", tagName, count)

	// Use symbolic addressing
	tag, partialTransfer, err := p.readTagCountInternal(tagName, count)
	if err != nil {
		debugLog("ReadTag %q: error: %v", tagName, err)
		return nil, err
	}

	// If we got all data, return it
	if !partialTransfer {
		debugLogVerbose("ReadTag %q: got %d bytes, type=0x%04X", tagName, len(tag.Bytes), tag.DataType)
		return tag, nil
	}

	// Partial transfer - need to read in chunks using array indexing
	// This works better for structures than byte-offset fragmented reads
	debugLogVerbose("ReadTag %q: partial transfer (%d bytes), chunking", tagName, len(tag.Bytes))
	return p.readTagChunked(tagName, count, tag)
}

// readTagCountInternal performs a single read request and returns partial transfer flag.
func (p *PLC) readTagCountInternal(tagName string, count uint16) (*Tag, bool, error) {
	path, err := cip.EPath().Symbol(tagName).Build()
	if err != nil {
		return nil, false, fmt.Errorf("ReadTag: failed to build path: %w", err)
	}

	reqData := make([]byte, 0, 2+len(path)+2)
	reqData = append(reqData, SvcReadTag)
	reqData = append(reqData, path.WordLen())
	reqData = append(reqData, path...)
	reqData = binary.LittleEndian.AppendUint16(reqData, count)

	cipResp, err := p.sendCipRequest(reqData)
	if err != nil {
		return nil, false, fmt.Errorf("ReadTag: %w", err)
	}

	return parseReadTagResponseEx(cipResp, tagName)
}

// readTagChunked reads a large array in chunks using array index syntax.
// This is more reliable for structure arrays than byte-offset fragmented reads.
func (p *PLC) readTagChunked(tagName string, totalCount uint16, initialTag *Tag) (*Tag, error) {
	if initialTag == nil || len(initialTag.Bytes) == 0 {
		return nil, fmt.Errorf("readTagChunked: no initial data to determine element size")
	}

	// Calculate element size from initial read
	// The initial read gave us partial data - figure out how many elements fit
	initialBytes := len(initialTag.Bytes)

	// Estimate elements per chunk based on connection size
	// Leave room for CIP overhead (header, type code, etc.)
	maxPayload := 480 // Conservative default for unconnected messaging
	if p.connSize > 0 {
		maxPayload = int(p.connSize) - 100 // Leave room for protocol overhead
	}

	// We need to figure out how many complete elements we got
	// For the first chunk, we'll use the partial data and continue from there
	allBytes := make([]byte, 0, initialBytes*int(totalCount)/10+initialBytes)
	allBytes = append(allBytes, initialTag.Bytes...)

	// Calculate elements read so far
	// We need to determine element size - try reading a single element to get the size
	singleTag, _, err := p.readTagCountInternal(tagName+"[0]", 1)
	if err != nil {
		// Can't determine element size, return what we have
		return &Tag{
			Name:     tagName,
			DataType: initialTag.DataType,
			Bytes:    allBytes,
		}, nil
	}

	elemSize := len(singleTag.Bytes)
	if elemSize == 0 {
		return &Tag{
			Name:     tagName,
			DataType: initialTag.DataType,
			Bytes:    allBytes,
		}, nil
	}

	// Calculate how many elements we already have
	elementsRead := initialBytes / elemSize

	// Calculate optimal chunk size
	elemsPerChunk := maxPayload / elemSize
	if elemsPerChunk < 1 {
		elemsPerChunk = 1
	}
	if elemsPerChunk > 100 {
		elemsPerChunk = 100 // Cap chunk size to avoid huge requests
	}

	// Read remaining elements in chunks
	for elementsRead < int(totalCount) {
		remaining := int(totalCount) - elementsRead
		chunkSize := elemsPerChunk
		if chunkSize > remaining {
			chunkSize = remaining
		}

		// Read chunk starting at current index
		chunkTagName := fmt.Sprintf("%s[%d]", tagName, elementsRead)
		chunkTag, partial, err := p.readTagCountInternal(chunkTagName, uint16(chunkSize))
		if err != nil {
			// Return what we have so far
			break
		}

		allBytes = append(allBytes, chunkTag.Bytes...)
		elementsRead += len(chunkTag.Bytes) / elemSize

		// If no partial transfer, we got all requested elements
		if !partial {
			// Move to next chunk
			continue
		}

		// Partial transfer within chunk - add what we got and continue
		actualElems := len(chunkTag.Bytes) / elemSize
		if actualElems == 0 {
			break // No progress, stop to avoid infinite loop
		}
	}

	return &Tag{
		Name:     tagName,
		DataType: initialTag.DataType,
		Bytes:    allBytes,
	}, nil
}

// ReadTagFragmented reads a tag using the Read Tag Fragmented service (0x52).
// This is used for large structures that exceed the packet size.
// It reads in chunks using byte offsets and reassembles the full data.
func (p *PLC) ReadTagFragmented(tagName string, expectedSize uint32) (*Tag, error) {
	if p == nil || p.Connection == nil {
		return nil, fmt.Errorf("ReadTagFragmented: nil plc or connection")
	}

	// Build the symbolic EPath for the tag name
	path, err := cip.EPath().Symbol(tagName).Build()
	if err != nil {
		return nil, fmt.Errorf("ReadTagFragmented: failed to build path: %w", err)
	}

	// Calculate max chunk size based on connection
	maxChunk := uint32(480) // Conservative default for unconnected messaging
	if p.connSize > 0 {
		maxChunk = uint32(p.connSize) - 100 // Leave room for protocol overhead
	}

	var allBytes []byte
	var dataType uint16
	offset := uint32(0)

	for offset < expectedSize {
		// Request remaining bytes, capped at max chunk
		remaining := expectedSize - offset
		chunkSize := remaining
		if chunkSize > maxChunk {
			chunkSize = maxChunk
		}

		// Build Read Tag Fragmented request:
		// [Service 0x52] [PathSize] [Path] [ElementCount:2] [Offset:4]
		reqData := make([]byte, 0, 2+len(path)+6)
		reqData = append(reqData, SvcReadTagFragmented)
		reqData = append(reqData, path.WordLen())
		reqData = append(reqData, path...)
		reqData = binary.LittleEndian.AppendUint16(reqData, 1) // Element count = 1
		reqData = binary.LittleEndian.AppendUint32(reqData, offset)

		cipResp, err := p.sendCipRequest(reqData)
		if err != nil {
			if len(allBytes) > 0 {
				// Return what we have
				break
			}
			return nil, fmt.Errorf("ReadTagFragmented: %w", err)
		}

		tag, partial, err := parseReadTagFragmentedResponse(cipResp, tagName)
		if err != nil {
			if len(allBytes) > 0 {
				break
			}
			return nil, fmt.Errorf("ReadTagFragmented: %w", err)
		}

		// First chunk gives us the data type
		if offset == 0 {
			dataType = tag.DataType
		}

		allBytes = append(allBytes, tag.Bytes...)
		offset += uint32(len(tag.Bytes))

		// If no partial transfer, we're done
		if !partial {
			break
		}
	}

	return &Tag{
		Name:     tagName,
		DataType: dataType,
		Bytes:    allBytes,
	}, nil
}

// WriteTag writes raw bytes to a tag. The dataType must match the tag's type in the PLC.
func (p *PLC) WriteTag(tagName string, dataType uint16, value []byte) error {
	return p.WriteTagCount(tagName, dataType, value, 1)
}

// WriteTagCount writes multiple elements to a tag (for arrays).
func (p *PLC) WriteTagCount(tagName string, dataType uint16, value []byte, count uint16) error {
	if p == nil || p.Connection == nil {
		return fmt.Errorf("WriteTag: nil plc or connection")
	}
	if tagName == "" {
		return fmt.Errorf("WriteTag: empty tag name")
	}

	logging.DebugLog("logix", "WriteTagCount %s: dataType=0x%04X (%s), count=%d, data=%X",
		tagName, dataType, TypeName(dataType), count, value)

	// Build the symbolic EPath for the tag name.
	path, err := cip.EPath().Symbol(tagName).Build()
	if err != nil {
		return fmt.Errorf("WriteTag: failed to build path: %w", err)
	}

	// Build the CIP request:
	// [Service 1 byte] [PathSize 1 byte] [Path n bytes] [DataType 2 bytes] [Count 2 bytes] [Data n bytes]
	reqData := make([]byte, 0, 2+len(path)+4+len(value))
	reqData = append(reqData, SvcWriteTag)                        // Service code
	reqData = append(reqData, path.WordLen())                     // Path size in words
	reqData = append(reqData, path...)                            // Path bytes
	reqData = binary.LittleEndian.AppendUint16(reqData, dataType) // Data type
	reqData = binary.LittleEndian.AppendUint16(reqData, count)    // Element count
	reqData = append(reqData, value...)                           // Tag data

	logging.DebugLog("logix", "WriteTagCount %s: CIP request=%X", tagName, reqData)

	// Send request and get response
	cipResp, err := p.sendCipRequest(reqData)
	if err != nil {
		logging.DebugLog("logix", "WriteTagCount %s: sendCipRequest error: %v", tagName, err)
		return fmt.Errorf("WriteTag: %w", err)
	}

	logging.DebugLog("logix", "WriteTagCount %s: CIP response=%X", tagName, cipResp)

	if err := parseWriteTagResponse(cipResp); err != nil {
		logging.DebugLog("logix", "WriteTagCount %s: response parse error: %v", tagName, err)
		return fmt.Errorf("WriteTag: %w", err)
	}

	return nil
}

// buildRoutedCpf wraps a CIP request in a CPF packet with routing via Connection Manager.
// The routePath specifies how to reach the target (e.g., {0x01, 0x00} for backplane port 1, slot 0).
func buildRoutedCpf(cipRequest []byte, routePath []byte) *eip.EipCommonPacket {
	// Unconnected Send service wraps the CIP request for routing.
	// Structure:
	// [Priority/Tick: 1] [Timeout Ticks: 1] [Message Size: 2] [Message: n]
	// [Pad: 1 if message size is odd] [Route Path Size: 1] [Reserved: 1] [Route Path: n]
	ucmm := make([]byte, 0, 4+len(cipRequest)+1+2+len(routePath))
	ucmm = append(ucmm, 0x0A) // Priority/time tick (10 = 160ms tick)
	ucmm = append(ucmm, 0x05) // Timeout ticks (5 ticks = 800ms)
	ucmm = binary.LittleEndian.AppendUint16(ucmm, uint16(len(cipRequest)))
	ucmm = append(ucmm, cipRequest...)
	// Pad to word boundary if message size is odd
	if len(cipRequest)%2 != 0 {
		ucmm = append(ucmm, 0x00)
	}
	ucmm = append(ucmm, byte(len(routePath)/2)) // Route path size in words
	ucmm = append(ucmm, 0x00)                   // Reserved
	ucmm = append(ucmm, routePath...)           // Route path

	// Build the full UCMM request: route to Connection Manager (class 0x06, instance 1)
	// Service 0x52 = Unconnected_Send
	cmPath, _ := cip.EPath().Class(0x06).Instance(1).Build()
	fullReq := make([]byte, 0, 2+len(cmPath)+len(ucmm))
	fullReq = append(fullReq, 0x52)             // Unconnected_Send service
	fullReq = append(fullReq, cmPath.WordLen()) // Path size in words
	fullReq = append(fullReq, cmPath...)        // Connection Manager path
	fullReq = append(fullReq, ucmm...)          // UCMM payload

	return &eip.EipCommonPacket{
		Items: []eip.EipCommonPacketItem{
			{TypeId: eip.CpfAddressNullId, Length: 0, Data: nil},
			{TypeId: eip.CpfUnconnectedMessageId, Length: uint16(len(fullReq)), Data: fullReq},
		},
	}
}

// buildDirectCpf wraps a CIP request in a CPF packet for direct messaging (no routing).
// Use this for CompactLogix or when connected directly to the target device.
func buildDirectCpf(cipRequest []byte) *eip.EipCommonPacket {
	return &eip.EipCommonPacket{
		Items: []eip.EipCommonPacketItem{
			{TypeId: eip.CpfAddressNullId, Length: 0, Data: nil},
			{TypeId: eip.CpfUnconnectedMessageId, Length: uint16(len(cipRequest)), Data: cipRequest},
		},
	}
}

// sendCipRequest sends a CIP request using the appropriate messaging mode:
// - Connected messaging if a CIP connection is established (most efficient)
// - Routed unconnected messaging if RoutePath is set (for ControlLogix via Ethernet module)
// - Direct unconnected messaging otherwise (for CompactLogix or direct CPU connection)
func (p *PLC) sendCipRequest(reqData []byte) ([]byte, error) {
	debugLogVerbose("sendCipRequest: svc=0x%02X, %d bytes", reqData[0], len(reqData))

	if p.cipConn != nil {
		// Use connected messaging
		connData := p.cipConn.WrapConnected(reqData)
		cpf := p.buildConnectedCpf(connData)

		resp, err := p.Connection.SendUnitDataTransaction(*cpf)
		if err != nil {
			debugLog("sendCipRequest: SendUnitDataTransaction error: %v", err)
			return nil, fmt.Errorf("SendUnitDataTransaction: %w", err)
		}

		if len(resp.Items) < 2 {
			return nil, fmt.Errorf("expected 2 CPF items, got %d", len(resp.Items))
		}

		_, cipResp, err := p.cipConn.UnwrapConnected(resp.Items[1].Data)
		if err != nil {
			return nil, fmt.Errorf("UnwrapConnected: %w", err)
		}

		debugLogVerbose("sendCipRequest: response %d bytes", len(cipResp))
		return cipResp, nil
	}

	// Use unconnected messaging
	var cpf *eip.EipCommonPacket
	if len(p.RoutePath) > 0 {
		cpf = buildRoutedCpf(reqData, p.RoutePath)
	} else {
		cpf = buildDirectCpf(reqData)
	}

	resp, err := p.Connection.SendRRData(*cpf)
	if err != nil {
		debugLog("sendCipRequest: SendRRData error: %v", err)
		return nil, fmt.Errorf("SendRRData: %w", err)
	}

	if len(resp.Items) < 2 {
		return nil, fmt.Errorf("expected 2 CPF items, got %d", len(resp.Items))
	}

	cipResp := resp.Items[1].Data

	// Unwrap UCMM response if routed
	if len(p.RoutePath) > 0 {
		cipResp, err = unwrapUCMMResponse(cipResp)
		if err != nil {
			return nil, err
		}
	}

	debugLogVerbose("sendCipRequest: response %d bytes", len(cipResp))

	return cipResp, nil
}

// unwrapUCMMResponse unwraps an Unconnected_Send response to get the embedded response.
// UCMM response format: [ReplyService 1] [Reserved 1] [Status 1] [AddlStatusSize 1] [AddlStatus n] [EmbeddedResponse n]
func unwrapUCMMResponse(data []byte) ([]byte, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("UCMM response too short: %d bytes", len(data))
	}

	replyService := data[0]
	// reserved := data[1]
	status := data[2]
	addlStatusSize := data[3] // Size in words

	// Verify it's an Unconnected_Send reply (0x52 | 0x80 = 0xD2)
	if replyService != 0xD2 {
		// Not a UCMM response, return as-is (might be direct response)
		return data, nil
	}

	// Check UCMM status
	if status != StatusSuccess {
		return nil, parseCipError(status, addlStatusSize, data[4:])
	}

	// Extract the embedded response (skip 4-byte header + additional status)
	embeddedStart := 4 + int(addlStatusSize)*2
	if embeddedStart >= len(data) {
		return nil, fmt.Errorf("UCMM response has no embedded data")
	}

	return data[embeddedStart:], nil
}

// parseReadTagResponse parses the CIP response for a Read Tag request.
// Response format: [ReplyService 1] [Reserved 1] [Status 1] [AddlStatusSize 1] [AddlStatus n] [DataType 2] [Data n]
func parseReadTagResponse(data []byte, tagName string) (*Tag, error) {
	tag, _, err := parseReadTagResponseEx(data, tagName)
	return tag, err
}

// parseReadTagResponseEx parses Read Tag response and returns partial transfer flag.
func parseReadTagResponseEx(data []byte, tagName string) (*Tag, bool, error) {
	if len(data) < 4 {
		return nil, false, fmt.Errorf("response too short: %d bytes", len(data))
	}

	replyService := data[0]
	// reserved := data[1]
	status := data[2]
	addlStatusSize := data[3] // Size in words (2-byte units)

	// Verify it's a reply (bit 7 set) to Read Tag
	if replyService != (SvcReadTag | 0x80) {
		return nil, false, fmt.Errorf("unexpected reply service: 0x%02X", replyService)
	}

	// Check status - partial transfer (0x06) is OK, we'll read more
	partialTransfer := (status == StatusPartialTransfer)
	if status != StatusSuccess && status != StatusPartialTransfer {
		return nil, false, parseCipError(status, addlStatusSize, data[4:])
	}

	// Skip additional status words
	dataStart := 4 + int(addlStatusSize)*2
	if len(data) < dataStart+2 {
		return nil, false, fmt.Errorf("response missing data type field")
	}

	dataType := binary.LittleEndian.Uint16(data[dataStart : dataStart+2])
	tagData := data[dataStart+2:]

	return &Tag{
		Name:     tagName,
		DataType: dataType,
		Bytes:    tagData,
	}, partialTransfer, nil
}

// parseReadTagFragmentedResponse parses the response for Read Tag Fragmented service.
// Response format: [ReplyService 1] [Reserved 1] [Status 1] [AddlStatusSize 1] [AddlStatus n] [DataType 2] [Data n]
func parseReadTagFragmentedResponse(data []byte, tagName string) (*Tag, bool, error) {
	if len(data) < 4 {
		return nil, false, fmt.Errorf("response too short: %d bytes", len(data))
	}

	replyService := data[0]
	status := data[2]
	addlStatusSize := data[3]

	// Verify it's a reply to Read Tag Fragmented (0x52 | 0x80 = 0xD2)
	if replyService != (SvcReadTagFragmented | 0x80) {
		return nil, false, fmt.Errorf("unexpected reply service: 0x%02X", replyService)
	}

	// Check status - partial transfer means more data available
	partialTransfer := (status == StatusPartialTransfer)
	if status != StatusSuccess && status != StatusPartialTransfer {
		return nil, false, parseCipError(status, addlStatusSize, data[4:])
	}

	dataStart := 4 + int(addlStatusSize)*2
	if len(data) < dataStart+2 {
		return nil, false, fmt.Errorf("response missing data type field")
	}

	dataType := binary.LittleEndian.Uint16(data[dataStart : dataStart+2])
	tagData := data[dataStart+2:]

	return &Tag{
		Name:     tagName,
		DataType: dataType,
		Bytes:    tagData,
	}, partialTransfer, nil
}

// parseWriteTagResponse parses the CIP response for a Write Tag request.
// Response format: [ReplyService 1] [Reserved 1] [Status 1] [AddlStatusSize 1] [AddlStatus n]
func parseWriteTagResponse(data []byte) error {
	if len(data) < 4 {
		return fmt.Errorf("response too short: %d bytes", len(data))
	}

	replyService := data[0]
	// reserved := data[1]
	status := data[2]
	addlStatusSize := data[3]

	// Verify it's a reply to Write Tag
	if replyService != (SvcWriteTag | 0x80) {
		return fmt.Errorf("unexpected reply service: 0x%02X", replyService)
	}

	if status != StatusSuccess {
		return parseCipError(status, addlStatusSize, data[4:])
	}

	return nil
}

// parseCipError constructs an error from CIP status codes.
func parseCipError(status byte, addlSize byte, addlData []byte) error {
	statusName := cipStatusName(status)

	// Check for extended status (not just for General Error)
	if addlSize >= 1 && len(addlData) >= 2 {
		extStatus := binary.LittleEndian.Uint16(addlData[:2])
		if extStatus != 0 {
			extName := cipExtStatusName(extStatus)
			return fmt.Errorf("CIP error: %s (0x%02X), extended: %s (0x%04X)", statusName, status, extName, extStatus)
		}
	}

	return fmt.Errorf("CIP error: %s (0x%02X)", statusName, status)
}

func cipStatusName(status byte) string {
	switch status {
	case StatusSuccess:
		return "Success"
	case 0x01:
		return "Connection Failure"
	case 0x02:
		return "Resource Unavailable"
	case 0x03:
		return "Invalid Parameter"
	case StatusPathSegmentError:
		return "Path Segment Error"
	case StatusPathUnknown:
		return "Path Unknown"
	case StatusPartialTransfer:
		return "Partial Transfer"
	case 0x07:
		return "Connection Lost"
	case StatusServiceNotSupport:
		return "Service Not Supported"
	case 0x09:
		return "Invalid Attribute Value"
	case StatusObjectNotExist:
		return "Object Does Not Exist"
	case 0x0D:
		return "Object Already Exists"
	case 0x0E:
		return "Attribute Not Settable"
	case 0x0F:
		return "Privilege Violation"
	case 0x10:
		return "Device State Conflict"
	case 0x11:
		return "Reply Data Too Large"
	case 0x13:
		return "Not Enough Data"
	case 0x14:
		return "Attribute Not Supported"
	case 0x15:
		return "Too Much Data"
	case 0x1C:
		return "Not Enough Data Received"
	case 0x1E:
		return "Invalid Symbolic"
	case 0x20:
		return "Invalid Parameter Type"
	case 0x26:
		return "Invalid Path"
	case StatusGeneralError:
		return "General Error"
	default:
		return fmt.Sprintf("Status 0x%02X", status)
	}
}

func cipExtStatusName(extStatus uint16) string {
	switch extStatus {
	case ExtStatusTagNotFound:
		return "Tag Not Found"
	case ExtStatusIllegalType:
		return "Illegal Data Type"
	case ExtStatusTagReadOnly:
		return "Tag Read Only"
	case ExtStatusSizeTooSmall:
		return "Size Too Small"
	case ExtStatusSizeTooLarge:
		return "Size Too Large"
	case ExtStatusOffsetError:
		return "Offset Out of Range"
	case 0x0100:
		return "Connection In Use"
	case 0x0103:
		return "Transport Class Not Supported"
	case 0x0106:
		return "Ownership Conflict"
	case 0x0107:
		return "Connection Not Found"
	case 0x0108:
		return "Invalid Connection Type"
	case 0x0109:
		return "Invalid Connection Size"
	case 0x0110:
		return "Module Not Found"
	case 0x0111:
		return "Connection Request Refused"
	case 0x0203:
		return "Connection Timed Out"
	case 0x0204:
		return "Unconnected Send Timed Out"
	case 0x0205:
		return "Parameter Error"
	case 0x0311:
		return "Connection Request Failed"
	case 0x0312:
		return "Connection Request Rejected"
	case 0xFF00:
		return "Extended Link Error"
	default:
		return fmt.Sprintf("Extended Status 0x%04X", extStatus)
	}
}
