package eip

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"plcio/logging"
)

const (
	NOP               uint16 = 0x00
	RegisterSession   uint16 = 0x65
	UnRegisterSession uint16 = 0x66
	SendRRData        uint16 = 0x6F
	SendUnitData      uint16 = 0x70
)

// Client type includes IP Address, the net connection, and a session identifier.
type EipClient struct {
	ipAddr  string
	port    uint16
	conn    net.Conn
	session uint32
	timeout time.Duration
	mu      sync.Mutex
}

func (e *EipClient) GetAddr() string {
	if e == nil {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ipAddr
}

func (e *EipClient) GetTimeout() time.Duration {
	if e == nil {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.timeout
}

func (e *EipClient) GetSession() uint32 {
	if e == nil {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.session
}

func (e *EipClient) SetTimeout(dur time.Duration) error {
	if e == nil {
		return fmt.Errorf("SetTimeout: nil client")
	}
	e.mu.Lock()
	e.timeout = dur
	e.mu.Unlock()
	return nil
}

func (e *EipClient) IsConnected() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.conn != nil
}

// Typically EIP uses the default port of 44818.
func NewEipClient(ipaddr string) *EipClient {
	return &EipClient{
		ipAddr:  ipaddr,
		port:    44818,
		conn:    nil,
		session: 0,
		timeout: time.Second * 5, // 5 seconds matches pylogix default
	}
}

// Allow for custom ports if needed.
func NewEipClientWithPort(ipaddr string, port uint16) *EipClient {
	return &EipClient{
		ipAddr:  ipaddr,
		port:    port,
		conn:    nil,
		session: 0,
		timeout: time.Second * 5, // 5 seconds matches pylogix default
	}
}

// Connect over EIP and register a session.
func (e *EipClient) Connect() error {
	if e == nil {
		return fmt.Errorf("Connect: Received nil client.")
	}

	e.mu.Lock()
	// Build the connection string.
	connString := e.ipAddr + ":" + strconv.Itoa(int(e.port))
	timeout := e.timeout
	e.mu.Unlock()

	logging.DebugConnect("EIP", connString)

	// Dial with timeout.
	d := net.Dialer{Timeout: timeout}
	conn, err := d.Dial("tcp", connString)
	if err != nil {
		logging.DebugConnectError("EIP", connString, err)
		return fmt.Errorf("Failed in Connect: %w", err)
	}

	logging.DebugLog("EIP", "TCP connection established to %s", connString)

	// Set up a keep-alive.
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(30 * time.Second)
	}

	var oldConn net.Conn

	e.mu.Lock()
	oldConn = e.conn
	oldSession := e.session

	e.conn = conn
	e.session = 0

	session, err := e.registerSession()
	if err != nil {
		e.conn = oldConn
		e.session = oldSession
		e.mu.Unlock()
		_ = conn.Close()
		logging.DebugError("EIP", "RegisterSession", err)
		return fmt.Errorf("Connect: failed to register session. %w", err)
	}

	e.session = session
	e.mu.Unlock()

	logging.DebugConnectSuccess("EIP", connString, fmt.Sprintf("session=0x%08X", session))

	if oldConn != nil {
		_ = oldConn.Close()
	}
	return nil
}

// Disconnect cleanly.
func (e *EipClient) Disconnect() error {

	// Treat nil client as a no-operation (no error).
	if e == nil {
		return nil
	}

	// Lock the socket
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.conn == nil {
		e.session = 0
		return nil
	}

	logging.DebugDisconnect("EIP", e.ipAddr, "client disconnect requested")

	// Best-effort to unregister existing session.
	if e.session != 0 {
		e.unRegisterSession()
		return nil
	}

	err := e.conn.Close()
	e.conn = nil
	e.session = 0

	return err
}

// Register a session with the controller
func (e *EipClient) registerSession() (uint32, error) {

	if e == nil || e.conn == nil {
		return 0, fmt.Errorf("RegisterSession: not connected.")
	}

	msg := EipEncap{
		command:       RegisterSession,
		length:        4,
		sessionHandle: 0,
		status:        0,
		context:       [8]byte{},
		options:       0,
		data:          []byte{1, 0, 0, 0},
	}

	// Session is registered in EIP by sending command 0x65.
	resp, err := e.transactEncap(msg)
	if err != nil {
		return 0, fmt.Errorf("RegisterSession: failed transaction: %w", err)
	}

	// The PLC may throw a response error, check to make sure it's set to 0.
	if resp.status != 0 {
		return 0, fmt.Errorf("Failed at RegisterSession(): Encap returned status not 0: 0x%08x", resp.status)
	}

	// If we didn't get a session for some reason, this failed.
	if resp.sessionHandle == 0 {
		return 0, fmt.Errorf("Failed at RegisterSession(): Got session_handle=0")
	}

	return resp.sessionHandle, nil
}

// De-Register a session with the controller
// Returns true if a session was found and successfully disconnected.
func (e *EipClient) unRegisterSession() (err error) {

	// Guard against nil pointer, treat at no-operation if received.
	if e == nil || e.conn == nil {
		return nil
	}

	// If a session isn't set, no-operation.
	if e.session == 0 {
		return nil
	}

	// EthernetIP De-Register session message.
	msg := EipEncap{
		command:       0x66,
		length:        0,
		sessionHandle: e.session,
		status:        0,
		context:       [8]byte{},
		options:       0,
		data:          []byte{},
	}

	// Prevent hanging forever on a bad connection.
	_ = e.conn.SetWriteDeadline(time.Now().Add(e.timeout))
	defer e.conn.SetWriteDeadline(time.Time{})

	// Send the message, ignore any errors since the client may be closing already.
	err = e.sendEncap(msg)

	e.session = 0
	e.conn.Close()
	e.conn = nil

	// Success
	return err
}

// Atomic transaction
func (e *EipClient) transactEncap(msg EipEncap) (*EipEncap, error) {
	if e == nil {
		return nil, fmt.Errorf("transactionEncap: received nil client.")
	}

	if e.conn == nil {
		return nil, fmt.Errorf("transactEncap: not connected.")
	}

	// Avoid hanging forever on write.
	_ = e.conn.SetWriteDeadline(time.Now().Add(e.timeout))
	defer e.conn.SetWriteDeadline(time.Time{})
	err := e.sendEncap(msg)
	if err != nil {
		return nil, fmt.Errorf("transactEncap: failed to send message.  %w", err)
	}

	// Avoid hanging forever on read.
	_ = e.conn.SetReadDeadline(time.Now().Add(e.timeout))
	defer e.conn.SetReadDeadline(time.Time{})
	resp, err := e.recvEncap()
	if err != nil {
		return nil, fmt.Errorf("transactEncap: failed to read response.  %w", err)
	}

	return resp, nil
}

// Send an EIP Encapsulated message.   Should be used inside BeginTxn() / EndTxn() block to assure atomicity.
func (e *EipClient) sendEncap(msg EipEncap) error {
	if e == nil || e.conn == nil {
		return fmt.Errorf("sendEncap: not connected")
	}
	data := msg.Bytes()
	logging.DebugTX("EIP", data)
	_, err := e.conn.Write(data)
	if err != nil {
		logging.DebugError("EIP", "sendEncap write", err)
		// Mark connection as dead so IsConnected() returns false
		e.conn.Close()
		e.conn = nil
	}
	return err
}

// Receives an EIP Encapsulated message. Should be used inside BeginTxn() / EndTxn() block to assure atomicity.
func (e *EipClient) recvEncap() (*EipEncap, error) {
	if e == nil || e.conn == nil {
		return nil, fmt.Errorf("recvEncap: not connected")
	}
	// Read the response encapsulation header.
	header := make([]byte, 24)
	_, err := io.ReadFull(e.conn, header)
	if err != nil {
		logging.DebugError("EIP", "recvEncap read header", err)
		// Mark connection as dead so IsConnected() returns false
		e.conn.Close()
		e.conn = nil
		return nil, fmt.Errorf("SendRecv: Error reading Encap header. %w", err)
	}

	// Get the payload length, session handle.
	payload_length := binary.LittleEndian.Uint16(header[2:4])
	sessionHandle := binary.LittleEndian.Uint32(header[4:8])

	// Sanity checks before proceeding.
	if payload_length > 65511 {
		logging.DebugLog("EIP", "RX excessive payload length: %d", payload_length)
		return nil, fmt.Errorf("SendRecv: Payload excessive.  Payload Length: %d", payload_length)
	}
	// Session handle validation:
	// - Session 0 in response is always valid (used by ListIdentity, etc.)
	// - Otherwise, response session must match our session
	if sessionHandle != 0 && e.session != 0 && sessionHandle != e.session {
		logging.DebugLog("EIP", "RX session mismatch: expected 0x%08X, got 0x%08X", e.session, sessionHandle)
		return nil, fmt.Errorf("SendRecv: Session mismatch in response.  Need %d, Got %d", e.session, sessionHandle)
	}

	// Read the encap data payload.
	payload := make([]byte, payload_length)
	_, err = io.ReadFull(e.conn, payload)
	if err != nil {
		logging.DebugError("EIP", "recvEncap read payload", err)
		// Mark connection as dead so IsConnected() returns false
		e.conn.Close()
		e.conn = nil
		return nil, fmt.Errorf("SendRecv: Failed to read payload. %w", err)
	}

	// Log the complete received packet
	fullPacket := append(header, payload...)
	logging.DebugRX("EIP", fullPacket)

	// Copy the context bytes to load into Encap.
	var ctx [8]byte
	copy(ctx[:], header[12:20])

	return &EipEncap{
		command:       binary.LittleEndian.Uint16(header[:2]),
		length:        binary.LittleEndian.Uint16(header[2:4]),
		sessionHandle: binary.LittleEndian.Uint32(header[4:8]),
		status:        binary.LittleEndian.Uint32(header[8:12]),
		context:       ctx,
		options:       binary.LittleEndian.Uint32(header[20:24]),
		data:          payload,
	}, nil
}

// Send an unconnected explicit message over TCP.
// Requires an TCP connection and a non-zero session handle (RegisterSession).
func (e *EipClient) SendRRData(packet EipCommonPacket) (*EipCommonPacket, error) {
	if e == nil {
		return nil, fmt.Errorf("SendRRData: Received nil client.")
	}

	// Force atomic transaction
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.conn == nil {
		return nil, fmt.Errorf("SendRRData: not connected.  Did you call Connect()?")
	}
	if e.session == 0 {
		return nil, fmt.Errorf("SendRRData: session_handle is 0 (did you call RegisterSession?)")
	}

	// Get the byte slice for the CommonPacket.
	packet_bytes := packet.Bytes()
	if len(packet_bytes) == 0 {
		return nil, fmt.Errorf("SendRRData: Conversion to bytes resulted in empty CIP request")
	}

	// Wrap in RRData
	rrdata := EipCommandData{
		interfaceHandle: 0,
		timeout:         0,
		packet:          packet_bytes,
	}

	rrdata_bytes := rrdata.Bytes()

	// Wrap in Ethernet/IP Encapsulation
	req := EipEncap{
		command:       SendRRData,
		length:        uint16(len(rrdata_bytes)),
		sessionHandle: e.session,
		status:        0,
		context:       [8]byte{},
		options:       0,
		data:          rrdata_bytes,
	}

	// Transmit the Ethernet/Ip frame using SendRecv()
	resp, err := e.transactEncap(req)
	if err != nil {
		return nil, fmt.Errorf("SendRRData(): Failed to transact packet. %w", err)
	}
	if resp.status != 0 {
		return nil, fmt.Errorf("SendRRData(): encapsulation status=0x%08x", resp.status)
	}

	// Parse the response into CommandData.
	cdata, err := ParseEipCommandData(resp.data)
	if err != nil {
		return nil, fmt.Errorf("SendRRData(): ParseCommandData failed.  %w", err)
	}

	// Parse the packet into a CommonPacket format.
	cpacket, err := ParseEipCommonPacket(cdata.packet)
	if err != nil {
		return nil, fmt.Errorf("SendRRData: ParseCommonPacket failed.  %w", err)
	}

	// Return the raw response bytes to be parsed in the CIP library.
	return cpacket, nil

}

// Send an connected explicit message over TCP.
// Requires an TCP connection and a non-zero session handle (RegisterSession).
func (e *EipClient) SendUnitData(packet EipCommonPacket) error {
	if e == nil {
		return fmt.Errorf("SendUnitData: Received nil client.")
	}

	// Force atomic transaction
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.conn == nil {
		return fmt.Errorf("SendUnitData: not connected.  Did you call Connect()?")
	}
	if e.session == 0 {
		return fmt.Errorf("SendUnitData: session_handle is 0 (did you call RegisterSession?)")
	}

	// Get the byte slice for the CommonPacket.
	packet_bytes := packet.Bytes()
	if len(packet_bytes) == 0 {
		return fmt.Errorf("SendUnitData: Conversion to bytes resulted in empty CIP request")
	}

	// Wrap in RRData
	cmd := EipCommandData{
		interfaceHandle: 0,
		timeout:         0,
		packet:          packet_bytes,
	}

	cmd_bytes := cmd.Bytes()

	// Wrap in Ethernet/IP Encapsulation
	req := EipEncap{
		command:       SendUnitData,
		length:        uint16(len(cmd_bytes)),
		sessionHandle: e.session,
		status:        0,
		context:       [8]byte{},
		options:       0,
		data:          cmd_bytes,
	}

	// Prevent hanging forever.
	_ = e.conn.SetWriteDeadline(time.Now().Add(e.timeout))
	defer e.conn.SetWriteDeadline(time.Time{})

	err := e.sendEncap(req)
	if err != nil {
		return fmt.Errorf("SendUnitData: Failed to transmit packet. %w", err)
	}
	return nil
}

// SendUnitDataTransaction sends a connected explicit message and waits for response.
// This is the connected messaging equivalent of SendRRData.
func (e *EipClient) SendUnitDataTransaction(packet EipCommonPacket) (*EipCommonPacket, error) {
	if e == nil {
		return nil, fmt.Errorf("SendUnitDataTransaction: nil client")
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.conn == nil {
		return nil, fmt.Errorf("SendUnitDataTransaction: not connected")
	}
	if e.session == 0 {
		return nil, fmt.Errorf("SendUnitDataTransaction: no session")
	}

	packet_bytes := packet.Bytes()
	if len(packet_bytes) == 0 {
		return nil, fmt.Errorf("SendUnitDataTransaction: empty packet")
	}

	cmd := EipCommandData{
		interfaceHandle: 0,
		timeout:         0,
		packet:          packet_bytes,
	}
	cmd_bytes := cmd.Bytes()

	req := EipEncap{
		command:       SendUnitData,
		length:        uint16(len(cmd_bytes)),
		sessionHandle: e.session,
		status:        0,
		context:       [8]byte{},
		options:       0,
		data:          cmd_bytes,
	}

	resp, err := e.transactEncap(req)
	if err != nil {
		return nil, fmt.Errorf("SendUnitDataTransaction: %w", err)
	}
	if resp.status != 0 {
		return nil, fmt.Errorf("SendUnitDataTransaction: status=0x%08x", resp.status)
	}

	cdata, err := ParseEipCommandData(resp.data)
	if err != nil {
		return nil, fmt.Errorf("SendUnitDataTransaction: %w", err)
	}

	cpacket, err := ParseEipCommonPacket(cdata.packet)
	if err != nil {
		return nil, fmt.Errorf("SendUnitDataTransaction: %w", err)
	}

	return cpacket, nil
}

// Implements the EIP No-Op command (0x00)
// Can be used to validate the connection is still open.
// Returns true if response is received and valid, otherwise false.

func (e *EipClient) SendNop() error {
	if e == nil {
		return fmt.Errorf("Nop: Received nil EipClient.")
	}

	// Force atomic transaction
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.conn == nil {
		return fmt.Errorf("Nop: Connection is nil.   Did you call Connect()?")
	}

	msg := EipEncap{
		command:       NOP,
		length:        0,
		sessionHandle: e.session,
		status:        0,
		context:       [8]byte{},
		options:       0,
		data:          nil,
	}

	// Prevent hanging forever.
	_ = e.conn.SetWriteDeadline(time.Now().Add(e.timeout))
	defer e.conn.SetWriteDeadline(time.Time{})

	err := e.sendEncap(msg)
	if err != nil {
		return fmt.Errorf("SendNop: failed to transmit message.  %w", err)
	}

	return nil
}
