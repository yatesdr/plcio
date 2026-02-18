package pccc

import (
	"fmt"
	"sync/atomic"

	"github.com/yatesdr/plcio/eip"
	"github.com/yatesdr/plcio/logging"
)

// PLC provides low-level PCCC communication with SLC500, PLC-5, and MicroLogix processors.
// It wraps an EIP client and handles PCCC command framing and CIP encapsulation.
type PLC struct {
	IpAddress  string
	Connection *eip.EipClient

	// Routing controls how CIP requests are sent:
	// - nil or empty: send directly (SLC 5/05, MicroLogix with built-in Ethernet)
	// - non-empty: route via Connection Manager (e.g., through 1756-DHRIO gateway)
	RoutePath []byte

	// PLCType selects command format details (SLC500, PLC5, MicroLogix).
	PLCType PLCType

	// PCCC requester ID fields (embedded in CIP Execute PCCC requests)
	vendorID  uint16
	serialNum uint32

	// Transaction counter for PCCC TNS field
	tns uint32
}

// Tag holds raw data read from a PCCC data table address.
type Tag struct {
	Address  string // Original address string (e.g., "N7:0")
	FileType byte   // PCCC file type code
	Bytes    []byte // Raw value bytes (little-endian)
}

// nextTNS returns the next transaction number, wrapping at 16 bits.
func (p *PLC) nextTNS() uint16 {
	return uint16(atomic.AddUint32(&p.tns, 1))
}

// ReadAddress reads a single data table address and returns the raw bytes.
func (p *PLC) ReadAddress(addr *FileAddress) (*Tag, error) {
	if p == nil || p.Connection == nil {
		return nil, fmt.Errorf("ReadAddress: nil PLC or connection")
	}
	if addr == nil {
		return nil, fmt.Errorf("ReadAddress: nil address")
	}

	debugLog("ReadAddress %s: file=%d type=0x%02X elem=%d sub=%d readSize=%d",
		addr.RawAddress, addr.FileNumber, addr.FileType, addr.Element, addr.SubElement, addr.ReadSize())

	// Build the PCCC read request wrapped in CIP
	tns := p.nextTNS()
	cipReq, err := buildReadRequest(addr, tns, p.vendorID, p.serialNum)
	if err != nil {
		return nil, fmt.Errorf("ReadAddress: %w", err)
	}

	// Send via EIP
	cipResp, err := p.sendCipRequest(cipReq)
	if err != nil {
		return nil, fmt.Errorf("ReadAddress %s: %w", addr.RawAddress, err)
	}

	// Parse the CIP response to extract PCCC payload
	pcccResp, err := parseCipExecutePCCCResponse(cipResp)
	if err != nil {
		return nil, fmt.Errorf("ReadAddress %s: %w", addr.RawAddress, err)
	}

	// Parse the PCCC response
	data, err := parsePCCCReadResponse(pcccResp)
	if err != nil {
		return nil, fmt.Errorf("ReadAddress %s: %w", addr.RawAddress, err)
	}

	debugLog("ReadAddress %s: got %d bytes", addr.RawAddress, len(data))

	return &Tag{
		Address:  addr.RawAddress,
		FileType: addr.FileType,
		Bytes:    data,
	}, nil
}

// ReadAddressN reads count contiguous elements starting at addr.Element.
// The returned Tag.Bytes contains up to count * ElementSize(addr.FileType) bytes.
// This is used for batch reads: a single PCCC round-trip retrieves multiple
// consecutive data table elements.
func (p *PLC) ReadAddressN(addr *FileAddress, count int) (*Tag, error) {
	if p == nil || p.Connection == nil {
		return nil, fmt.Errorf("ReadAddressN: nil PLC or connection")
	}
	if addr == nil {
		return nil, fmt.Errorf("ReadAddressN: nil address")
	}
	if count <= 0 {
		return nil, fmt.Errorf("ReadAddressN: count must be > 0")
	}

	elemSize := ElementSize(addr.FileType)
	byteCount := count * elemSize

	debugLog("ReadAddressN %s: count=%d elemSize=%d byteCount=%d",
		addr.RawAddress, count, elemSize, byteCount)

	tns := p.nextTNS()
	cipReq, err := buildReadRequestN(addr, byteCount, tns, p.vendorID, p.serialNum)
	if err != nil {
		return nil, fmt.Errorf("ReadAddressN: %w", err)
	}

	cipResp, err := p.sendCipRequest(cipReq)
	if err != nil {
		return nil, fmt.Errorf("ReadAddressN %s: %w", addr.RawAddress, err)
	}

	pcccResp, err := parseCipExecutePCCCResponse(cipResp)
	if err != nil {
		return nil, fmt.Errorf("ReadAddressN %s: %w", addr.RawAddress, err)
	}

	data, err := parsePCCCReadResponse(pcccResp)
	if err != nil {
		return nil, fmt.Errorf("ReadAddressN %s: %w", addr.RawAddress, err)
	}

	debugLog("ReadAddressN %s: got %d bytes (expected %d)", addr.RawAddress, len(data), byteCount)

	return &Tag{
		Address:  addr.RawAddress,
		FileType: addr.FileType,
		Bytes:    data,
	}, nil
}

// WriteAddress writes raw bytes to a data table address.
func (p *PLC) WriteAddress(addr *FileAddress, data []byte) error {
	if p == nil || p.Connection == nil {
		return fmt.Errorf("WriteAddress: nil PLC or connection")
	}
	if addr == nil {
		return fmt.Errorf("WriteAddress: nil address")
	}

	debugLog("WriteAddress %s: file=%d type=0x%02X elem=%d sub=%d data=%X",
		addr.RawAddress, addr.FileNumber, addr.FileType, addr.Element, addr.SubElement, data)

	// Build the PCCC write request wrapped in CIP
	tns := p.nextTNS()
	cipReq, err := buildWriteRequest(addr, data, tns, p.vendorID, p.serialNum)
	if err != nil {
		return fmt.Errorf("WriteAddress: %w", err)
	}

	// Send via EIP
	cipResp, err := p.sendCipRequest(cipReq)
	if err != nil {
		return fmt.Errorf("WriteAddress %s: %w", addr.RawAddress, err)
	}

	// Parse the CIP response
	pcccResp, err := parseCipExecutePCCCResponse(cipResp)
	if err != nil {
		return fmt.Errorf("WriteAddress %s: %w", addr.RawAddress, err)
	}

	// Parse the PCCC write response
	if err := parsePCCCWriteResponse(pcccResp); err != nil {
		return fmt.Errorf("WriteAddress %s: %w", addr.RawAddress, err)
	}

	debugLog("WriteAddress %s: success", addr.RawAddress)
	return nil
}

// Close disconnects from the PLC.
func (p *PLC) Close() {
	if p == nil || p.Connection == nil {
		return
	}
	_ = p.Connection.Disconnect()
}

// IsConnected returns true if the EIP session is active.
func (p *PLC) IsConnected() bool {
	return p != nil && p.Connection != nil && p.Connection.IsConnected()
}

// Keepalive sends a NOP to keep the TCP connection alive.
func (p *PLC) Keepalive() error {
	if p == nil || p.Connection == nil {
		return nil
	}
	return p.Connection.SendNop()
}

// sendCipRequest sends a CIP request using the appropriate messaging mode:
// - Routed unconnected messaging if RoutePath is set
// - Direct unconnected messaging otherwise
//
// PCCC does not use CIP connected messaging (Forward Open), so we always
// use SendRRData (EIP command 0x6F).
func (p *PLC) sendCipRequest(reqData []byte) ([]byte, error) {
	if len(reqData) == 0 {
		return nil, fmt.Errorf("sendCipRequest: empty request data")
	}
	debugLog("sendCipRequest: %d bytes, svc=0x%02X", len(reqData), reqData[0])

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

	debugLog("sendCipRequest: response %d bytes", len(resp.Items[1].Data))
	return resp.Items[1].Data, nil
}

// debugLog logs a message via the global debug logger.
func debugLog(format string, args ...interface{}) {
	logging.DebugLog("pccc", format, args...)
}
