package omron

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yatesdr/plcio/logging"
)

// FINS/TCP frame constants.
const (
	finsTCPMagic           = "FINS"
	tcpHeaderSize          = 16
	cmdNodeAddressRequest  = 0x00000000
	cmdNodeAddressResponse = 0x00000001
	cmdFINSFrameSend       = 0x00000002
	cmdFINSFrameSendError  = 0x00000003
)

// tcpTransport implements FINS over TCP.
type tcpTransport struct {
	mu         sync.Mutex
	conn       net.Conn
	address    string
	port       int
	network    byte
	plcNode    byte
	unit       byte
	localNode  byte
	serverNode byte // Assigned by PLC
	sid        uint32
	timeout    time.Duration
	debug      bool
	connected  bool
}

// newTCPTransport creates a new TCP transport.
func newTCPTransport() *tcpTransport {
	return &tcpTransport{
		timeout: 5 * time.Second,
	}
}

// connect establishes the TCP connection.
func (t *tcpTransport) connect(address string, port int, network, node, unit, srcNode byte) error {
	if port <= 0 {
		port = defaultFINSPort
	}

	t.address = address
	t.port = port
	t.network = network
	t.plcNode = node
	t.unit = unit
	t.localNode = srcNode

	// Connect TCP
	addr := fmt.Sprintf("%s:%d", address, port)
	logging.DebugConnect("FINS/TCP", addr)
	logging.DebugLog("FINS/TCP", "Connection params: network=%d, node=%d, unit=%d, srcNode=%d", network, node, unit, srcNode)

	conn, err := net.DialTimeout("tcp", addr, t.timeout)
	if err != nil {
		logging.DebugConnectError("FINS/TCP", addr, err)
		return fmt.Errorf("failed to connect: %w", err)
	}
	t.conn = conn

	logging.DebugLog("FINS/TCP", "TCP connection established to %s", addr)

	// Perform node address negotiation
	if err := t.negotiateNodeAddress(); err != nil {
		conn.Close()
		logging.DebugError("FINS/TCP", "node address negotiation", err)
		return fmt.Errorf("node address negotiation failed: %w", err)
	}

	t.connected = true
	logging.DebugConnectSuccess("FINS/TCP", addr, fmt.Sprintf("localNode=%d, serverNode=%d, plcNode=%d", t.localNode, t.serverNode, t.plcNode))
	return nil
}

// negotiateNodeAddress performs FINS/TCP node address exchange.
// Per Omron W421 FINS/TCP specification:
// - Request: FINS(4) + Length(4) + Command(4) + ErrorCode(4) + ClientNode(4) = 20 bytes
// - Response: FINS(4) + Length(4) + Command(4) + ErrorCode(4) + ClientNode(4) + ServerNode(4) = 24 bytes
func (t *tcpTransport) negotiateNodeAddress() error {
	// Build Node Address Data Send request
	req := make([]byte, tcpHeaderSize+4)

	// FINS magic
	copy(req[0:4], finsTCPMagic)

	// Length: 12 bytes (command + error code + client node)
	// Length field indicates bytes after "FINS"(4) + "Length"(4) = 8 bytes
	binary.BigEndian.PutUint32(req[4:8], 12)

	// Command: Node Address Data Send
	binary.BigEndian.PutUint32(req[8:12], cmdNodeAddressRequest)

	// Error code: 0
	binary.BigEndian.PutUint32(req[12:16], 0)

	// Client node: Always request 0 (auto-assign) for FINS/TCP
	// The PLC will assign a valid node within its supported range.
	// Unlike UDP where we use IP-based node detection, TCP negotiation
	// should let the PLC assign the node to avoid "node out of range" errors
	// (error 0x00000002) on PLCs with limited node ranges like CP1L-E.
	requestedNode := uint32(0)
	binary.BigEndian.PutUint32(req[16:20], requestedNode)

	logging.DebugLog("FINS/TCP", "Node address request: clientNode=%d (0=auto-assign)", requestedNode)
	logging.DebugTX("FINS/TCP", req)

	// Set deadline
	if t.timeout > 0 {
		t.conn.SetDeadline(time.Now().Add(t.timeout))
	}

	// Send request
	if _, err := t.conn.Write(req); err != nil {
		logging.DebugError("FINS/TCP", "send node address request", err)
		return fmt.Errorf("failed to send node address request: %w", err)
	}

	// Read response
	resp := make([]byte, tcpHeaderSize+8)
	if _, err := io.ReadFull(t.conn, resp); err != nil {
		logging.DebugError("FINS/TCP", "read node address response", err)
		return fmt.Errorf("failed to read node address response: %w", err)
	}

	logging.DebugRX("FINS/TCP", resp)

	// Verify magic
	if string(resp[0:4]) != finsTCPMagic {
		logging.DebugLog("FINS/TCP", "Invalid magic: got %q, expected %q", string(resp[0:4]), finsTCPMagic)
		return fmt.Errorf("invalid FINS response magic: got %q", string(resp[0:4]))
	}

	// Check length field
	respLen := binary.BigEndian.Uint32(resp[4:8])
	logging.DebugLog("FINS/TCP", "Response length field: %d (expected 12 for node address response)", respLen)

	// Check command
	cmd := binary.BigEndian.Uint32(resp[8:12])
	logging.DebugLog("FINS/TCP", "Response command: 0x%08X (expected 0x%08X for node address response)", cmd, cmdNodeAddressResponse)
	if cmd != cmdNodeAddressResponse {
		return fmt.Errorf("unexpected command: 0x%08X (expected 0x%08X)", cmd, cmdNodeAddressResponse)
	}

	// Check error code
	errCode := binary.BigEndian.Uint32(resp[12:16])
	if errCode != 0 {
		logging.DebugLog("FINS/TCP", "Node address error code: 0x%08X", errCode)
		errMsg := finsNodeAddressErrorMsg(errCode)
		return fmt.Errorf("node address error: 0x%08X (%s)", errCode, errMsg)
	}

	// Extract assigned addresses
	t.localNode = byte(binary.BigEndian.Uint32(resp[16:20]))
	t.serverNode = byte(binary.BigEndian.Uint32(resp[20:24]))

	logging.DebugLog("FINS/TCP", "Node address negotiation success: localNode=%d (assigned), serverNode=%d", t.localNode, t.serverNode)

	// Use server node as destination if not explicitly set
	if t.plcNode == 0 {
		t.plcNode = t.serverNode
		logging.DebugLog("FINS/TCP", "Using serverNode %d as destination (plcNode was 0)", t.plcNode)
	}

	return nil
}

// finsNodeAddressErrorMsg returns a human-readable message for FINS/TCP node address error codes.
func finsNodeAddressErrorMsg(errCode uint32) string {
	switch errCode {
	case 0x00000000:
		return "normal"
	case 0x00000001:
		return "client node address already used"
	case 0x00000002:
		return "client node address out of range"
	case 0x00000003:
		return "server node address already used"
	default:
		return "unknown error"
	}
}

// close closes the TCP connection.
func (t *tcpTransport) close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	logging.DebugDisconnect("FINS/TCP", t.address, "close requested")

	if t.conn != nil {
		err := t.conn.Close()
		t.conn = nil
		t.connected = false
		return err
	}
	return nil
}

// isConnected returns true if connected.
func (t *tcpTransport) isConnected() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.connected && t.conn != nil
}

// setDisconnected marks the transport as disconnected.
func (t *tcpTransport) setDisconnected() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.connected = false
}

// nextSID returns the next service ID.
func (t *tcpTransport) nextSID() byte {
	return byte(atomic.AddUint32(&t.sid, 1) & 0xFF)
}

// sendCommand sends a FINS command and returns the response.
// FINS frame format (per W227 FINS Commands Reference):
// - Header (10 bytes): ICF, RSV, GCT, DNA, DA1, DA2, SNA, SA1, SA2, SID
// - Command (2 bytes): Command code (e.g., 0x0101 for Memory Read)
// - Data (variable): Command-specific data
func (t *tcpTransport) sendCommand(command uint16, data []byte) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.connected || t.conn == nil {
		logging.DebugLog("FINS/TCP", "sendCommand called but not connected")
		return nil, fmt.Errorf("not connected")
	}

	sid := t.nextSID()

	// Build FINS frame
	// ICF bits: 7=Gateway(0=use), 6=Type(0=cmd,1=resp), 5=RespReq(0=no,1=yes), 4-0=Reserved
	// ICF=0x80 means: use gateway, command, response required
	header := FINSHeader{
		ICF: 0x80, // Command, response required
		RSV: 0x00, // Reserved
		GCT: 0x02, // Gateway count (max 2 hops)
		DNA: t.network,
		DA1: t.plcNode,
		DA2: t.unit,
		SNA: t.network,
		SA1: t.localNode,
		SA2: 0x00,
		SID: sid,
	}

	logging.DebugLog("FINS/TCP", "Command 0x%04X: SID=%d DNA=%d DA1=%d DA2=%d SNA=%d SA1=%d dataLen=%d",
		command, sid, t.network, t.plcNode, t.unit, t.network, t.localNode, len(data))

	finsFrame := FINSFrame{
		Header:  header,
		Command: command,
		Data:    data,
	}
	finsBytes := finsFrame.Bytes()

	// Build TCP frame
	tcpFrame := make([]byte, tcpHeaderSize+len(finsBytes))
	copy(tcpFrame[0:4], finsTCPMagic)
	binary.BigEndian.PutUint32(tcpFrame[4:8], uint32(8+len(finsBytes)))
	binary.BigEndian.PutUint32(tcpFrame[8:12], cmdFINSFrameSend)
	binary.BigEndian.PutUint32(tcpFrame[12:16], 0)
	copy(tcpFrame[tcpHeaderSize:], finsBytes)

	logging.DebugTX("FINS/TCP", tcpFrame)

	// Set deadline
	if t.timeout > 0 {
		t.conn.SetDeadline(time.Now().Add(t.timeout))
	}

	// Send
	if _, err := t.conn.Write(tcpFrame); err != nil {
		t.connected = false
		logging.DebugDisconnect("FINS/TCP", t.address, fmt.Sprintf("send failed: %v", err))
		return nil, fmt.Errorf("failed to send: %w", err)
	}

	// Read response header
	respHeader := make([]byte, tcpHeaderSize)
	if _, err := io.ReadFull(t.conn, respHeader); err != nil {
		t.connected = false
		logging.DebugDisconnect("FINS/TCP", t.address, fmt.Sprintf("recv header failed: %v", err))
		return nil, fmt.Errorf("failed to read response header: %w", err)
	}

	// Verify magic
	if string(respHeader[0:4]) != finsTCPMagic {
		logging.DebugLog("FINS/TCP", "Invalid FINS response magic: %s", string(respHeader[0:4]))
		return nil, fmt.Errorf("invalid FINS response magic")
	}

	// Get length
	respLen := binary.BigEndian.Uint32(respHeader[4:8])
	if respLen < 8 {
		logging.DebugLog("FINS/TCP", "Invalid response length: %d", respLen)
		return nil, fmt.Errorf("invalid response length: %d", respLen)
	}

	// Check command
	cmd := binary.BigEndian.Uint32(respHeader[8:12])
	if cmd == cmdFINSFrameSendError {
		errCode := binary.BigEndian.Uint32(respHeader[12:16])
		logging.DebugLog("FINS/TCP", "FINS frame error: 0x%08X", errCode)
		return nil, fmt.Errorf("FINS frame error: 0x%08X", errCode)
	}
	if cmd != cmdFINSFrameSend {
		logging.DebugLog("FINS/TCP", "Unexpected response command: 0x%08X", cmd)
		return nil, fmt.Errorf("unexpected response command: 0x%08X", cmd)
	}

	// Read FINS frame
	finsLen := int(respLen) - 8
	if finsLen <= 0 {
		logging.DebugLog("FINS/TCP", "Empty FINS response")
		return nil, fmt.Errorf("empty FINS response")
	}

	respFrame := make([]byte, finsLen)
	if _, err := io.ReadFull(t.conn, respFrame); err != nil {
		t.connected = false
		logging.DebugDisconnect("FINS/TCP", t.address, fmt.Sprintf("recv frame failed: %v", err))
		return nil, fmt.Errorf("failed to read FINS response: %w", err)
	}

	// Log complete received packet
	fullResp := append(respHeader, respFrame...)
	logging.DebugRX("FINS/TCP", fullResp)

	// Parse FINS response
	resp, err := ParseFINSResponse(respFrame)
	if err != nil {
		logging.DebugError("FINS/TCP", "parse response", err)
		return nil, err
	}

	// Check end code
	if resp.EndCode != FINSEndOK {
		logging.DebugLog("FINS/TCP", "FINS end code error: 0x%04X", resp.EndCode)
		return nil, FINSEndCodeError(resp.EndCode)
	}

	return resp.Data, nil
}

// readWords reads words from a memory area.
func (t *tcpTransport) readWords(area byte, address uint16, count uint16) ([]uint16, error) {
	data := BuildMemoryReadRequest(area, address, 0, count)

	resp, err := t.sendCommand(FINSCmdMemoryRead, data)
	if err != nil {
		return nil, err
	}

	if len(resp) < int(count)*2 {
		return nil, fmt.Errorf("response too short: got %d bytes, expected %d", len(resp), count*2)
	}

	words := make([]uint16, count)
	for i := uint16(0); i < count; i++ {
		words[i] = binary.BigEndian.Uint16(resp[i*2 : i*2+2])
	}

	return words, nil
}

// writeWords writes words to a memory area.
func (t *tcpTransport) writeWords(area byte, address uint16, words []uint16) error {
	values := make([]byte, len(words)*2)
	for i, w := range words {
		binary.BigEndian.PutUint16(values[i*2:i*2+2], w)
	}

	data := BuildMemoryWriteRequest(area, address, 0, values)
	_, err := t.sendCommand(FINSCmdMemoryWrite, data)
	return err
}

// readBits reads bits from a memory area.
func (t *tcpTransport) readBits(area byte, address uint16, bitOffset byte, count uint16) ([]bool, error) {
	data := BuildMemoryReadRequest(area, address, bitOffset, count)

	resp, err := t.sendCommand(FINSCmdMemoryRead, data)
	if err != nil {
		return nil, err
	}

	if len(resp) < int(count) {
		return nil, fmt.Errorf("response too short: got %d bytes, expected %d", len(resp), count)
	}

	bits := make([]bool, count)
	for i := uint16(0); i < count; i++ {
		bits[i] = resp[i] != 0
	}

	return bits, nil
}

// writeBits writes bits to a memory area.
func (t *tcpTransport) writeBits(area byte, address uint16, bitOffset byte, bits []bool) error {
	values := make([]byte, len(bits))
	for i, b := range bits {
		if b {
			values[i] = 1
		}
	}

	data := BuildMemoryWriteRequest(area, address, bitOffset, values)
	_, err := t.sendCommand(FINSCmdMemoryWrite, data)
	return err
}

// readCPUStatus reads the CPU status.
func (t *tcpTransport) readCPUStatus() (*CPUStatus, error) {
	resp, err := t.sendCommand(FINSCmdCPUStatus, nil)
	if err != nil {
		return nil, err
	}
	return ParseCPUStatus(resp)
}

// readCycleTime reads the cycle time.
func (t *tcpTransport) readCycleTime() (*CycleTime, error) {
	resp, err := t.sendCommand(FINSCmdCycleTime, []byte{0x00})
	if err != nil {
		return nil, err
	}
	return ParseCycleTime(resp)
}

// connectionMode returns a description of the connection.
func (t *tcpTransport) connectionMode(address string, port int) string {
	return fmt.Sprintf("FINS/TCP %s:%d (NET:%d NODE:%d UNIT:%d, LOCAL:%d)",
		address, port, t.network, t.plcNode, t.unit, t.localNode)
}

// getSourceNode returns the local node number.
func (t *tcpTransport) getSourceNode() byte {
	return t.localNode
}

// setDebug enables/disables debug mode.
func (t *tcpTransport) setDebug(enabled bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.debug = enabled
}
