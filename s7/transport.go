package s7

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"plcio/logging"
)

const (
	defaultS7Port = 102

	// TPKT constants (RFC 1006)
	tpktVersion    = 0x03
	tpktHeaderSize = 4

	// COTP PDU Types (ISO 8073)
	cotpCR = 0xE0 // Connection Request
	cotpCC = 0xD0 // Connection Confirm
	cotpDT = 0xF0 // Data Transfer

	// COTP parameter codes
	cotpParamSrcTSAP  = 0xC1
	cotpParamDstTSAP  = 0xC2
	cotpParamTPDUSize = 0xC0

	// Default PDU sizes
	defaultPDUSize   = 480
	maxPDUSize       = 960
	cotpTPDUSize1024 = 0x0A // 2^10 = 1024 bytes
)

// transport handles the ISO-on-TCP and COTP layer for S7 communication.
type transport struct {
	mu        sync.Mutex
	conn      net.Conn
	address   string
	rack      int
	slot      int
	timeout   time.Duration
	pduSize   uint16
	connected bool
}

// newTransport creates a new transport instance.
func newTransport() *transport {
	return &transport{
		timeout: 10 * time.Second,
		pduSize: defaultPDUSize,
	}
}

// connect establishes connection to an S7 PLC.
func (t *transport) connect(address string, rack, slot int) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Add default port if not specified
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		// No port specified, use default
		address = fmt.Sprintf("%s:%d", address, defaultS7Port)
	} else if port == "" {
		address = fmt.Sprintf("%s:%d", host, defaultS7Port)
	}

	t.address = address
	t.rack = rack
	t.slot = slot

	logging.DebugConnect("S7", address)
	logging.DebugLog("S7", "Connection params: rack=%d, slot=%d", rack, slot)

	// TCP connect
	conn, err := net.DialTimeout("tcp", address, t.timeout)
	if err != nil {
		logging.DebugConnectError("S7", address, err)
		return fmt.Errorf("TCP connect failed: %w", err)
	}
	t.conn = conn

	logging.DebugLog("S7", "TCP connection established to %s", address)

	// Set read/write deadlines
	if err := t.conn.SetDeadline(time.Now().Add(t.timeout)); err != nil {
		t.conn.Close()
		logging.DebugError("S7", "set deadline", err)
		return fmt.Errorf("failed to set deadline: %w", err)
	}

	// COTP connection
	if err := t.cotpConnect(); err != nil {
		t.conn.Close()
		logging.DebugError("S7", "COTP connect", err)
		return fmt.Errorf("COTP connect failed: %w", err)
	}

	logging.DebugLog("S7", "COTP connection established")

	// S7 setup communication
	pduSize, err := t.s7SetupComm()
	if err != nil {
		t.conn.Close()
		logging.DebugError("S7", "S7 setup communication", err)
		return fmt.Errorf("S7 setup failed: %w", err)
	}
	t.pduSize = pduSize

	t.connected = true

	logging.DebugConnectSuccess("S7", address, fmt.Sprintf("rack=%d, slot=%d, PDU=%d", rack, slot, pduSize))

	// Clear deadline for ongoing operations
	t.conn.SetDeadline(time.Time{})

	return nil
}

// close closes the transport connection.
func (t *transport) close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	logging.DebugDisconnect("S7", t.address, "close requested")

	t.connected = false
	if t.conn != nil {
		err := t.conn.Close()
		t.conn = nil
		return err
	}
	return nil
}

// isConnected returns whether the transport is connected.
func (t *transport) isConnected() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.connected
}

// getPDUSize returns the negotiated PDU size.
func (t *transport) getPDUSize() uint16 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pduSize
}

// sendReceive sends an S7 request and receives the response.
func (t *transport) sendReceive(s7Request []byte) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.connected || t.conn == nil {
		logging.DebugLog("S7", "sendReceive called but not connected")
		return nil, fmt.Errorf("not connected")
	}

	// Set deadline for this operation
	if err := t.conn.SetDeadline(time.Now().Add(t.timeout)); err != nil {
		t.connected = false
		logging.DebugError("S7", "sendReceive set deadline", err)
		return nil, fmt.Errorf("failed to set deadline: %w", err)
	}

	// Build COTP DT + S7 payload
	cotpDTHeader := []byte{0x02, cotpDT, 0x80} // 3-byte COTP DT header
	payload := append(cotpDTHeader, s7Request...)

	// Send with TPKT framing
	if err := t.sendTPKT(payload); err != nil {
		t.connected = false
		logging.DebugDisconnect("S7", t.address, fmt.Sprintf("send failed: %v", err))
		return nil, err
	}

	// Receive response
	response, err := t.recvTPKT()
	if err != nil {
		t.connected = false
		logging.DebugDisconnect("S7", t.address, fmt.Sprintf("recv failed: %v", err))
		return nil, err
	}

	// Skip COTP DT header (3 bytes)
	if len(response) < 3 {
		logging.DebugLog("S7", "Response too short: %d bytes", len(response))
		return nil, fmt.Errorf("response too short")
	}
	if response[1] != cotpDT {
		logging.DebugLog("S7", "Expected COTP DT (0x%02X), got 0x%02X", cotpDT, response[1])
		return nil, fmt.Errorf("expected COTP DT, got 0x%02X", response[1])
	}

	return response[3:], nil
}

// sendTPKT sends data with TPKT framing.
func (t *transport) sendTPKT(data []byte) error {
	length := len(data) + tpktHeaderSize
	header := []byte{
		tpktVersion,
		0x00,
		byte(length >> 8),
		byte(length),
	}

	packet := append(header, data...)
	logging.DebugTX("S7", packet)
	_, err := t.conn.Write(packet)
	if err != nil {
		logging.DebugError("S7", "sendTPKT write", err)
	}
	return err
}

// recvTPKT receives a TPKT-framed packet.
func (t *transport) recvTPKT() ([]byte, error) {
	// Read TPKT header
	header := make([]byte, tpktHeaderSize)
	if _, err := io.ReadFull(t.conn, header); err != nil {
		logging.DebugError("S7", "recvTPKT read header", err)
		return nil, fmt.Errorf("failed to read TPKT header: %w", err)
	}

	if header[0] != tpktVersion {
		logging.DebugLog("S7", "RX invalid TPKT version: %d", header[0])
		return nil, fmt.Errorf("invalid TPKT version: %d", header[0])
	}

	length := int(binary.BigEndian.Uint16(header[2:4]))
	if length < tpktHeaderSize {
		logging.DebugLog("S7", "RX invalid TPKT length: %d", length)
		return nil, fmt.Errorf("invalid TPKT length: %d", length)
	}

	// Read payload
	payload := make([]byte, length-tpktHeaderSize)
	if _, err := io.ReadFull(t.conn, payload); err != nil {
		logging.DebugError("S7", "recvTPKT read payload", err)
		return nil, fmt.Errorf("failed to read TPKT payload: %w", err)
	}

	// Log complete received packet
	fullPacket := append(header, payload...)
	logging.DebugRX("S7", fullPacket)

	return payload, nil
}

// cotpConnect performs COTP connection request/confirm exchange.
func (t *transport) cotpConnect() error {
	// Build COTP Connection Request
	// TSAP format: local = 01 00, remote = 01 (rack<<5 | slot)
	srcTSAP := []byte{0x01, 0x00}
	dstTSAP := []byte{0x01, byte(t.rack<<5 | t.slot)}

	// COTP CR PDU
	cr := []byte{
		0x00,       // Length (filled later)
		cotpCR,     // PDU type
		0x00, 0x00, // Destination reference
		0x00, 0x01, // Source reference
		0x00, // Class 0
	}

	// Add parameters
	cr = append(cr, cotpParamSrcTSAP, byte(len(srcTSAP)))
	cr = append(cr, srcTSAP...)
	cr = append(cr, cotpParamDstTSAP, byte(len(dstTSAP)))
	cr = append(cr, dstTSAP...)
	cr = append(cr, cotpParamTPDUSize, 0x01, cotpTPDUSize1024)

	// Set length (excluding length byte itself)
	cr[0] = byte(len(cr) - 1)

	// Send CR
	if err := t.sendTPKT(cr); err != nil {
		return fmt.Errorf("failed to send COTP CR: %w", err)
	}

	// Receive CC
	cc, err := t.recvTPKT()
	if err != nil {
		return fmt.Errorf("failed to receive COTP CC: %w", err)
	}

	if len(cc) < 2 {
		return fmt.Errorf("COTP CC too short")
	}

	// Check PDU type (second byte after length)
	if cc[1] != cotpCC {
		return fmt.Errorf("expected COTP CC (0x%02X), got 0x%02X", cotpCC, cc[1])
	}

	return nil
}

// s7SetupComm performs S7 communication setup and returns negotiated PDU size.
func (t *transport) s7SetupComm() (uint16, error) {
	// Build S7 Setup Communication request
	request := buildSetupCommRequest(maxPDUSize)

	// COTP DT header
	cotpDTHeader := []byte{0x02, cotpDT, 0x80}
	payload := append(cotpDTHeader, request...)

	// Send
	if err := t.sendTPKT(payload); err != nil {
		return 0, fmt.Errorf("failed to send S7 setup: %w", err)
	}

	// Receive
	response, err := t.recvTPKT()
	if err != nil {
		return 0, fmt.Errorf("failed to receive S7 setup response: %w", err)
	}

	// Skip COTP DT header
	if len(response) < 3 {
		return 0, fmt.Errorf("S7 setup response too short")
	}
	if response[1] != cotpDT {
		return 0, fmt.Errorf("expected COTP DT in response")
	}

	s7Response := response[3:]
	return parseSetupCommResponse(s7Response)
}
