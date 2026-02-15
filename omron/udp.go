package omron

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yatesdr/plcio/logging"
)

const (
	defaultFINSPort = 9600
)

// udpTransport implements FINS over UDP.
type udpTransport struct {
	mu        sync.Mutex
	conn      *net.UDPConn
	plcAddr   *net.UDPAddr
	localNode byte
	plcNode   byte
	network   byte
	unit      byte
	sid       uint32
	timeout   time.Duration
	debug     bool
	connected bool
}

// newUDPTransport creates a new UDP transport.
func newUDPTransport() *udpTransport {
	return &udpTransport{
		timeout: 2 * time.Second,
	}
}

// connect establishes the UDP connection.
func (t *udpTransport) connect(address string, port int, network, node, unit, srcNode byte) error {
	if port <= 0 {
		port = defaultFINSPort
	}

	addr := fmt.Sprintf("%s:%d", address, port)
	logging.DebugConnect("FINS/UDP", addr)
	logging.DebugLog("FINS/UDP", "Connection params: network=%d, node=%d, unit=%d, srcNode=%d", network, node, unit, srcNode)

	// Resolve PLC address
	plcAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		logging.DebugConnectError("FINS/UDP", addr, err)
		return fmt.Errorf("failed to resolve address: %w", err)
	}

	// Create local address (bind to any available port)
	localAddr, err := net.ResolveUDPAddr("udp", ":0")
	if err != nil {
		logging.DebugError("FINS/UDP", "resolve local address", err)
		return fmt.Errorf("failed to resolve local address: %w", err)
	}

	// Create UDP connection
	conn, err := net.ListenUDP("udp", localAddr)
	if err != nil {
		logging.DebugError("FINS/UDP", "create UDP socket", err)
		return fmt.Errorf("failed to create UDP socket: %w", err)
	}

	// Auto-detect source node from local IP if not specified
	if srcNode == 0 {
		srcNode = t.detectLocalNode(address)
		logging.DebugLog("FINS/UDP", "Auto-detected source node: %d", srcNode)
	}

	t.conn = conn
	t.plcAddr = plcAddr
	t.network = network
	t.plcNode = node
	t.unit = unit
	t.localNode = srcNode
	t.connected = true

	logging.DebugConnectSuccess("FINS/UDP", addr, fmt.Sprintf("localNode=%d, plcNode=%d", srcNode, node))
	return nil
}

// detectLocalNode attempts to determine the local node number.
func (t *udpTransport) detectLocalNode(plcAddress string) byte {
	conn, err := net.Dial("udp", plcAddress+":9600")
	if err != nil {
		return 0
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	ip := localAddr.IP.To4()
	if ip == nil {
		return 0
	}
	return ip[3]
}

// close closes the UDP connection.
func (t *udpTransport) close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.plcAddr != nil {
		logging.DebugDisconnect("FINS/UDP", t.plcAddr.String(), "close requested")
	}

	if t.conn != nil {
		err := t.conn.Close()
		t.conn = nil
		t.connected = false
		return err
	}
	return nil
}

// isConnected returns true if connected.
func (t *udpTransport) isConnected() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.connected && t.conn != nil
}

// setDisconnected marks the transport as disconnected.
func (t *udpTransport) setDisconnected() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.connected = false
}

// nextSID returns the next service ID.
func (t *udpTransport) nextSID() byte {
	return byte(atomic.AddUint32(&t.sid, 1) & 0xFF)
}

// sendCommand sends a FINS command and returns the response.
// FINS/UDP frame format: Just the FINS frame (no TCP wrapper)
func (t *udpTransport) sendCommand(command uint16, data []byte) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.connected || t.conn == nil {
		logging.DebugLog("FINS/UDP", "sendCommand called but not connected")
		return nil, fmt.Errorf("not connected")
	}

	sid := t.nextSID()

	// Build FINS frame
	// ICF=0x80: use gateway, command, response required
	header := FINSHeader{
		ICF: 0x80, // Command, response required
		RSV: 0x00,
		GCT: 0x02, // Max 2 gateways
		DNA: t.network,
		DA1: t.plcNode,
		DA2: t.unit,
		SNA: t.network,
		SA1: t.localNode,
		SA2: 0x00,
		SID: sid,
	}

	logging.DebugLog("FINS/UDP", "Command 0x%04X: SID=%d DNA=%d DA1=%d DA2=%d SNA=%d SA1=%d dataLen=%d",
		command, sid, t.network, t.plcNode, t.unit, t.network, t.localNode, len(data))

	frame := FINSFrame{
		Header:  header,
		Command: command,
		Data:    data,
	}

	frameBytes := frame.Bytes()
	logging.DebugTX("FINS/UDP", frameBytes)

	// Set deadline
	if t.timeout > 0 {
		t.conn.SetDeadline(time.Now().Add(t.timeout))
	}

	// Send
	if _, err := t.conn.WriteToUDP(frameBytes, t.plcAddr); err != nil {
		t.connected = false
		logging.DebugDisconnect("FINS/UDP", t.plcAddr.String(), fmt.Sprintf("send failed: %v", err))
		return nil, fmt.Errorf("failed to send: %w", err)
	}

	// Receive response
	buf := make([]byte, 2048)
	n, _, err := t.conn.ReadFromUDP(buf)
	if err != nil {
		t.connected = false
		logging.DebugDisconnect("FINS/UDP", t.plcAddr.String(), fmt.Sprintf("recv failed: %v", err))
		return nil, fmt.Errorf("failed to receive: %w", err)
	}

	logging.DebugRX("FINS/UDP", buf[:n])

	// Parse response
	resp, err := ParseFINSResponse(buf[:n])
	if err != nil {
		logging.DebugError("FINS/UDP", "parse response", err)
		return nil, err
	}

	// Check end code
	if resp.EndCode != FINSEndOK {
		logging.DebugLog("FINS/UDP", "FINS end code error: 0x%04X", resp.EndCode)
		return nil, FINSEndCodeError(resp.EndCode)
	}

	return resp.Data, nil
}

// readWords reads words from a memory area.
func (t *udpTransport) readWords(area byte, address uint16, count uint16) ([]uint16, error) {
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
func (t *udpTransport) writeWords(area byte, address uint16, words []uint16) error {
	values := make([]byte, len(words)*2)
	for i, w := range words {
		binary.BigEndian.PutUint16(values[i*2:i*2+2], w)
	}

	data := BuildMemoryWriteRequest(area, address, 0, values)
	_, err := t.sendCommand(FINSCmdMemoryWrite, data)
	return err
}

// readBits reads bits from a memory area.
func (t *udpTransport) readBits(area byte, address uint16, bitOffset byte, count uint16) ([]bool, error) {
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
func (t *udpTransport) writeBits(area byte, address uint16, bitOffset byte, bits []bool) error {
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
func (t *udpTransport) readCPUStatus() (*CPUStatus, error) {
	resp, err := t.sendCommand(FINSCmdCPUStatus, nil)
	if err != nil {
		return nil, err
	}
	return ParseCPUStatus(resp)
}

// readCycleTime reads the cycle time.
func (t *udpTransport) readCycleTime() (*CycleTime, error) {
	resp, err := t.sendCommand(FINSCmdCycleTime, []byte{0x00})
	if err != nil {
		return nil, err
	}
	return ParseCycleTime(resp)
}

// connectionMode returns a description of the connection.
func (t *udpTransport) connectionMode(address string, port int) string {
	return fmt.Sprintf("FINS/UDP %s:%d (NET:%d NODE:%d UNIT:%d, LOCAL:%d)",
		address, port, t.network, t.plcNode, t.unit, t.localNode)
}

// getSourceNode returns the local node number.
func (t *udpTransport) getSourceNode() byte {
	return t.localNode
}

// setDebug enables/disables debug mode.
func (t *udpTransport) setDebug(enabled bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.debug = enabled
}
