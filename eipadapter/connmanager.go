package eipadapter

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/yatesdr/plcio/cip"
	"github.com/yatesdr/plcio/logging"
)

// ForwardOpenContext is passed to a Config.OnForwardOpen callback.
type ForwardOpenContext struct {
	Request *cip.ForwardOpenRequest
	// ConfigInstance is the instance ID extracted from the connection path
	// for the configuration assembly. 0 if absent or 0x80 ("no config").
	ConfigInstance uint32
	// ConsumeInstance is the O->T (scanner→adapter) assembly instance.
	ConsumeInstance uint32
	// ProduceInstance is the T->O (adapter→scanner) assembly instance.
	ProduceInstance uint32
}

// Connection is one active CIP Class 1 connection between this adapter and a
// scanner. Field accessors are concurrent-safe; mutation paths take the mu.
type Connection struct {
	// Connection IDs, as returned in the Forward_Open reply (see
	// assignConnIDsLocked). OTConnID tags the O->T data we consume;
	// TOConnID tags the T->O data we produce.
	OTConnID uint32
	TOConnID uint32

	OTRPI uint32 // µs
	TORPI uint32

	ConnectionSerial uint16
	VendorID         uint16
	OriginatorSerial uint32
	TimeoutMultiplier byte

	Consume *Assembly // O->T input to adapter
	Produce *Assembly // T->O output from adapter

	// peerAddr is the originator's I/O address, fixed at Forward_Open from
	// the TCP peer that opened the connection: T->O data is sent there and
	// O->T data is only accepted from its IP.
	peerAddr peerAddr

	seqOut uint32 // 32-bit cyclic packet counter for T->O
	seqMu  sync.Mutex

	lastInboundAt time.Time
	createdAt     time.Time

	// otFormat is the O->T real-time format implied by the Forward_Open
	// connection size (otFormatAuto = detect per packet).
	otFormat byte

	// lastSeq is the last accepted O->T 32-bit sequence number.
	lastSeq  uint32
	seqValid bool

	// run is the O->T Run/Idle header's Run bit (valid when runKnown).
	run      bool
	runKnown bool

	done chan struct{} // closed with the connection; nil for bare fixtures

	mu     sync.RWMutex
	closed bool
}

// O->T real-time formats (CIP Vol 1, 3-6.1). The format is not signalled in
// the Forward_Open, so it is inferred from the O->T connection size.
const (
	otFormatAuto     byte = iota // unknown: strip a header if the length says so
	otFormatModeless             // sequence count + data
	otFormatRunIdle              // sequence count + 32-bit Run/Idle header + data
)

// markClosed flags the connection closed and wakes its goroutine. It
// reports whether this call did the closing.
func (c *Connection) markClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	c.closed = true
	if c.done != nil {
		close(c.done)
	}
	return true
}

// ConnectionEventType identifies what happened to a Class 1 connection.
type ConnectionEventType int

const (
	// ConnectionOpened: a Forward_Open was accepted.
	ConnectionOpened ConnectionEventType = iota + 1
	// ConnectionClosed: Forward_Close received, or the adapter shut down.
	ConnectionClosed
	// ConnectionTimedOut: no O->T packet arrived within the connection
	// timeout (RPI x timeout multiplier).
	ConnectionTimedOut
	// ConnectionRun: the O->T Run/Idle header reported Run (first header
	// seen, or a change from Idle).
	ConnectionRun
	// ConnectionIdle: the O->T Run/Idle header reported Idle (first header
	// seen, or a change from Run).
	ConnectionIdle
)

func (t ConnectionEventType) String() string {
	switch t {
	case ConnectionOpened:
		return "opened"
	case ConnectionClosed:
		return "closed"
	case ConnectionTimedOut:
		return "timed out"
	case ConnectionRun:
		return "run"
	case ConnectionIdle:
		return "idle"
	}
	return fmt.Sprintf("ConnectionEventType(%d)", int(t))
}

// ConnectionEvent is passed to Config.OnConnectionEvent.
type ConnectionEvent struct {
	Type     ConnectionEventType
	OTConnID uint32
	TOConnID uint32
	// ConsumeInstance / ProduceInstance are the connection's output (O->T)
	// and input (T->O) assembly instances; 0 if absent.
	ConsumeInstance uint32
	ProduceInstance uint32
	// Originator is the scanner's IP address.
	Originator net.IP
}

// emitConnEvent invokes Config.OnConnectionEvent, if set.
func (a *Adapter) emitConnEvent(c *Connection, t ConnectionEventType) {
	fn := a.cfg.OnConnectionEvent
	if fn == nil {
		return
	}
	c.mu.RLock()
	ip := net.IPv4(c.peerAddr.ip[0], c.peerAddr.ip[1], c.peerAddr.ip[2], c.peerAddr.ip[3]).To4()
	c.mu.RUnlock()
	ev := ConnectionEvent{Type: t, OTConnID: c.OTConnID, TOConnID: c.TOConnID, Originator: ip}
	if c.Consume != nil {
		ev.ConsumeInstance = c.Consume.InstanceID
	}
	if c.Produce != nil {
		ev.ProduceInstance = c.Produce.InstanceID
	}
	fn(ev)
}

type peerAddr struct {
	ip   [4]byte
	port uint16
}

func (c *Connection) markInbound(now time.Time) {
	c.mu.Lock()
	c.lastInboundAt = now
	c.mu.Unlock()
}

func (c *Connection) nextSeq() uint32 {
	c.seqMu.Lock()
	defer c.seqMu.Unlock()
	c.seqOut++
	return c.seqOut
}

// ConnectionManager implements CIP Class 0x06 Instance 1: Forward_Open and
// Forward_Close handling, plus tracking of all active Class 1 connections.
type ConnectionManager struct {
	adapter *Adapter

	mu   sync.RWMutex
	byOT map[uint32]*Connection // index by O->T connection ID (unique: we assign it)
	// byTO indexes only T->O IDs the adapter allocated itself. A
	// point-to-point T->O ID is chosen by the originator, so two scanners
	// may legitimately pick the same one; those are not indexed.
	byTO   map[uint32]*Connection
	closed bool // set by closeAll; no new connections afterwards
}

func NewConnectionManager(a *Adapter) *ConnectionManager {
	return &ConnectionManager{
		adapter: a,
		byOT:    make(map[uint32]*Connection),
		byTO:    make(map[uint32]*Connection),
	}
}

func (m *ConnectionManager) Class() uint32    { return 0x06 }
func (m *ConnectionManager) Instance() uint32 { return 1 }

const (
	cmForwardOpen      byte = 0x54
	cmForwardOpenLarge byte = 0x5B
	cmForwardClose     byte = 0x4E
	cmUnconnectedSend  byte = 0x52
)

func (m *ConnectionManager) Handle(req *ObjectRequest) ObjectResponse {
	switch req.Service {
	case cmForwardOpen:
		return m.handleForwardOpen(req.Data, false, req.origin)
	case cmForwardOpenLarge:
		return m.handleForwardOpen(req.Data, true, req.origin)
	case cmForwardClose:
		return m.handleForwardClose(req.Data, req.origin)
	case cmUnconnectedSend:
		return m.handleUnconnectedSend(req.Data, req.origin)
	default:
		return ObjectResponse{Status: cip.StatusServiceNotSupported}
	}
}

// Connection Manager extended status codes (general status 0x01) not
// defined by the cip package. CIP Vol 1, Table 3-5.33 "Connection Manager
// Service Request Error Codes". Newer spec editions add more specific codes
// (0x011C-0x012F, e.g. 0x0124 "invalid T->O connection type", 0x012A/0x012B
// "invalid consuming/producing application path"); the original generic
// codes are used here because every scanner decodes them.
const (
	extInvalidNetConnParam uint16 = 0x0108 // Invalid network connection parameter
	extOutOfConnections    uint16 = 0x0113 // Out of connections
	extInvalidAppPath      uint16 = 0x0117 // Invalid produced or consumed application path
)

// minRPI is the smallest RPI (µs) the adapter can honour. Smaller requests
// are rejected with 0x0111 "RPI not supported" rather than silently served
// slower: the API reported in the reply must be what we actually do, and
// producing slower than the requested RPI would break the originator's
// timing contract.
const minRPI = 1000

// Network connection parameter connection types (CIP Vol 1, 3-5.5.1.1).
const (
	connTypeNull      = 0
	connTypeMulticast = 1
	connTypeP2P       = 2
)

// netConnParams decodes a Forward_Open network connection parameter word:
// 16-bit (bits 14-13 type, bit 9 variable, bits 8-0 size) or 32-bit Large
// Forward_Open (bits 30-29 type, bit 25 variable, bits 15-0 size).
func netConnParams(p uint32, large bool) (connType byte, variable bool, size int) {
	if large {
		return byte(p>>29) & 0x3, p&(1<<25) != 0, int(p & 0xFFFF)
	}
	return byte(p>>13) & 0x3, p&(1<<9) != 0, int(p & 0x1FF)
}

// forwardOpenReject builds a Forward_Open failure with general status 0x01
// and, if ext != 0, one extended status word. The body is the unsuccessful
// Forward_Open response (CIP Vol 1 3-5.5.2): connection serial, vendor ID,
// originator serial, remaining path size, reserved.
func forwardOpenReject(r *cip.ForwardOpenRequest, ext uint16) ObjectResponse {
	return cmReject(cip.StatusConnectionFailure, r.ConnectionSerial, r.VendorID, r.OriginatorSerial, ext)
}

// cmReject builds an unsuccessful Forward_Open / Forward_Close response:
// the connection triad, remaining path size (0) and reserved byte (the
// unsuccessful Forward_Open and Forward_Close response bodies are
// identical), with general status status and, if ext != 0, one extended
// status word.
func cmReject(status byte, serial, vendor uint16, origSerial uint32, ext uint16) ObjectResponse {
	body := make([]byte, 0, 10)
	body = binary.LittleEndian.AppendUint16(body, serial)
	body = binary.LittleEndian.AppendUint16(body, vendor)
	body = binary.LittleEndian.AppendUint32(body, origSerial)
	body = append(body, 0x00, 0x00)
	resp := ObjectResponse{Status: status, Data: body}
	if ext != 0 {
		resp.ExtData = []uint16{ext}
	}
	return resp
}

func (m *ConnectionManager) handleForwardOpen(data []byte, large bool, origin *peerAddr) ObjectResponse {
	r, err := cip.ParseForwardOpenRequest(data, large)
	if err != nil {
		return ObjectResponse{Status: cip.StatusInvalidParameter}
	}

	// Only Class 1 I/O is implemented (transport class = low nibble of the
	// transport type/trigger byte).
	if r.TransportTrigger&0x0F != 1 {
		logging.DebugLog("eipadapter", "Forward_Open rejected: transport class %d", r.TransportTrigger&0x0F)
		return forwardOpenReject(r, cip.ExtTransportClassUnsupp)
	}

	cfgInst, consume, produce, err := parseIOConnectionPath(r.ConnectionPath)
	if err != nil {
		logging.DebugLog("eipadapter", "Forward_Open path parse: %v", err)
		// Build error with ext status 0x0315 (Invalid Connection Path).
		return forwardOpenReject(r, cip.ExtInvalidConnPath)
	}

	var consumeAsm, produceAsm *Assembly
	if consume != 0 {
		consumeAsm = m.adapter.Assembly(consume)
		if consumeAsm == nil {
			return forwardOpenReject(r, cip.ExtInvalidConnPath)
		}
		// The scanner writes the consumed (O->T) assembly: it must be an
		// output assembly.
		if consumeAsm.Direction != AssemblyOutput {
			logging.DebugLog("eipadapter", "Forward_Open rejected: consume assembly %d is not an output assembly", consume)
			return forwardOpenReject(r, extInvalidAppPath)
		}
	}
	if produce != 0 {
		produceAsm = m.adapter.Assembly(produce)
		if produceAsm == nil {
			return forwardOpenReject(r, cip.ExtInvalidConnPath)
		}
		if produceAsm.Direction != AssemblyInput {
			logging.DebugLog("eipadapter", "Forward_Open rejected: produce assembly %d is not an input assembly", produce)
			return forwardOpenReject(r, extInvalidAppPath)
		}
	}

	// O->T must be point-to-point (it carries the heartbeat that feeds the
	// connection watchdog). T->O must not be null when something is
	// produced. A multicast T->O request is accepted and served unicast to
	// the originator, as in earlier releases, so existing scanner
	// configurations keep working.
	otType, otVariable, otSize := netConnParams(r.OTParams, large)
	toType, toVariable, toSize := netConnParams(r.TOParams, large)
	if otType != connTypeP2P || (produceAsm != nil && toType == connTypeNull) {
		logging.DebugLog("eipadapter", "Forward_Open rejected: connection types O->T=%d T->O=%d", otType, toType)
		return forwardOpenReject(r, extInvalidNetConnParam)
	}
	if toType == connTypeMulticast {
		logging.DebugLog("eipadapter", "Forward_Open: multicast T->O requested; producing unicast to the originator")
	}

	// Connection sizes include the 2-byte sequence count; O->T data may also
	// carry the 32-bit Run/Idle header. T->O is sent modeless (no header).
	// A variable-size connection's size is a maximum.
	if produceAsm != nil {
		need := produceAsm.Size + 2
		if (!toVariable && toSize != need) || (toVariable && toSize < need) {
			logging.DebugLog("eipadapter", "Forward_Open rejected: T->O size %d, assembly %d needs %d", toSize, produce, need)
			return forwardOpenReject(r, cip.ExtInvalidConnSize)
		}
	}
	otFormat := otFormatAuto
	if consumeAsm != nil {
		modeless, runIdle := consumeAsm.Size+2, consumeAsm.Size+6
		switch {
		case !otVariable && otSize == modeless:
			otFormat = otFormatModeless
		case !otVariable && otSize == runIdle:
			otFormat = otFormatRunIdle
		case otVariable && otSize >= modeless:
		default:
			logging.DebugLog("eipadapter", "Forward_Open rejected: O->T size %d, assembly %d needs %d or %d", otSize, consume, modeless, runIdle)
			return forwardOpenReject(r, cip.ExtInvalidConnSize)
		}
	}

	if r.OTRPI < minRPI || (produceAsm != nil && r.TORPI < minRPI) {
		logging.DebugLog("eipadapter", "Forward_Open rejected: RPI O->T=%dus T->O=%dus below minimum %dus", r.OTRPI, r.TORPI, minRPI)
		return forwardOpenReject(r, cip.ExtRPIOutOfRange)
	}

	if m.adapter.cfg.OnForwardOpen != nil {
		if err := m.adapter.cfg.OnForwardOpen(&ForwardOpenContext{
			Request:         r,
			ConfigInstance:  cfgInst,
			ConsumeInstance: consume,
			ProduceInstance: produce,
		}); err != nil {
			logging.DebugLog("eipadapter", "Forward_Open rejected by callback: %v", err)
			return forwardOpenReject(r, 0)
		}
	}

	c := &Connection{
		OTRPI:             r.OTRPI,
		TORPI:             r.TORPI,
		ConnectionSerial:  r.ConnectionSerial,
		VendorID:          r.VendorID,
		OriginatorSerial:  r.OriginatorSerial,
		TimeoutMultiplier: r.TimeoutMultiplier,
		Consume:           consumeAsm,
		Produce:           produceAsm,
		createdAt:         m.adapter.cfg.Now(),
		otFormat:          otFormat,
		done:              make(chan struct{}),
	}
	if origin != nil {
		c.peerAddr = *origin
	}

	// Admission checks and registration happen under one lock so that
	// concurrent Forward_Opens cannot both pass the limit/duplicate checks.
	m.mu.Lock()
	var ext uint16
	switch {
	case m.closed:
		ext = extOutOfConnections
	case len(m.byOT) >= m.adapter.cfg.MaxConnections:
		ext = extOutOfConnections
	case m.findTriadLocked(r.ConnectionSerial, r.VendorID, r.OriginatorSerial) != nil:
		// Same connection triad as an open connection: duplicate
		// Forward_Open (CIP Vol 1 3-5.5.2.1).
		ext = cip.ExtConnectionInUse
	case consumeAsm != nil && !consumeAsm.claimOwner(c):
		// Output assembly already owned by another exclusive-owner
		// connection.
		ext = cip.ExtOwnershipConflict
	}
	if ext != 0 {
		m.mu.Unlock()
		logging.DebugLog("eipadapter", "Forward_Open rejected: ext status 0x%04X (O->T=0x%08X serial=0x%04X)", ext, r.OTConnectionID, r.ConnectionSerial)
		return forwardOpenReject(r, ext)
	}
	m.assignConnIDsLocked(c, r, toType)
	m.mu.Unlock()

	logging.DebugLog("eipadapter", "Forward_Open accepted: O->T=0x%08X T->O=0x%08X consume=%d produce=%d OTRPI=%dus TORPI=%dus",
		c.OTConnID, c.TOConnID, consume, produce, r.OTRPI, r.TORPI)

	// Every connection gets a goroutine: it runs the O->T watchdog from now
	// (a connection that never receives traffic times out) and, if there is
	// a produce assembly, the T->O producer. Deferred so it starts after the
	// Opened event, even if the application callback panics. The RPIs were
	// validated above, so the APIs reported below are the intervals used.
	defer m.adapter.startConnection(c)
	m.adapter.emitConnEvent(c, ConnectionOpened)

	body := cip.BuildForwardOpenSuccess(cip.ForwardOpenSuccess{
		OTConnectionID:   c.OTConnID,
		TOConnectionID:   c.TOConnID,
		ConnectionSerial: c.ConnectionSerial,
		VendorID:         c.VendorID,
		OriginatorSerial: c.OriginatorSerial,
		OTAPI:            r.OTRPI,
		TOAPI:            r.TORPI,
	})
	return ObjectResponse{Status: cip.StatusSuccess, Data: body}
}

// assignConnIDsLocked sets c's connection IDs and registers c. m.mu must
// be held for writing.
//
// CIP Vol 1 3-5.4.1 (Network Connection IDs): for a point-to-point
// connection the consumer chooses the ID, for multicast the producer does;
// the target returns both IDs in the Forward_Open reply and the originator
// uses the reply's values. OpENer does the same
// (cipconnectionobject.c, ConnectionObjectGeneralConfiguration). So:
//
//   - O->T (always point-to-point here; we consume it): the target chooses.
//     The originator's proposed ID is kept when it is free — the behaviour
//     of earlier releases, so the O->T ID on the wire is unchanged in the
//     normal case — and a fresh ID is chosen only if another connection
//     already uses it, instead of refusing the Forward_Open.
//   - T->O point-to-point (the originator consumes it): the originator's
//     ID is used, so it matches whatever the scanner expects.
//   - T->O multicast (accepted but produced unicast), or a zero proposal:
//     the target allocates the ID.
func (m *ConnectionManager) assignConnIDsLocked(c *Connection, r *cip.ForwardOpenRequest, toType byte) {
	c.OTConnID = r.OTConnectionID
	if c.OTConnID == 0 || m.byOT[c.OTConnID] != nil {
		c.OTConnID = m.allocateConnIDLocked()
	}
	m.byOT[c.OTConnID] = c
	if toType != connTypeMulticast && r.TOConnectionID != 0 {
		c.TOConnID = r.TOConnectionID
		return
	}
	c.TOConnID = m.allocateConnIDLocked()
	m.byTO[c.TOConnID] = c
}

// handleForwardClose closes the connection with the request's connection
// triad. CIP Vol 1 3-5.5.3: if no such connection exists the reply is
// general status 0x01, extended status 0x0107 (target connection not
// found), with the unsuccessful Forward_Close body. A different
// originator has a different triad (vendor ID + originator serial), so it
// can never close someone else's connection. As in OpENer (ForwardClose in
// cipconnectionmanager.c), a request with a matching triad that comes from
// a different IP than the one that opened the connection is refused with
// general status 0x0F (privilege violation). origin is nil when the peer
// is unknown; the IP check is then skipped.
func (m *ConnectionManager) handleForwardClose(data []byte, origin *peerAddr) ObjectResponse {
	r, err := cip.ParseForwardCloseRequest(data)
	if err != nil {
		return ObjectResponse{Status: cip.StatusInvalidParameter}
	}

	m.mu.RLock()
	found := m.findTriadLocked(r.ConnectionSerial, r.VendorID, r.OriginatorSerial)
	m.mu.RUnlock()

	if found == nil {
		logging.DebugLog("eipadapter", "Forward_Close: no connection serial=0x%04X vendor=0x%04X orig=0x%08X", r.ConnectionSerial, r.VendorID, r.OriginatorSerial)
		return cmReject(cip.StatusConnectionFailure, r.ConnectionSerial, r.VendorID, r.OriginatorSerial, cip.ExtTargetConnNotFound)
	}
	if origin != nil {
		found.mu.RLock()
		owner := found.peerAddr
		found.mu.RUnlock()
		if owner.port != 0 && owner.ip != origin.ip {
			logging.DebugLog("eipadapter", "Forward_Close for O->T=0x%08X refused: from %v, opened by %v", found.OTConnID, net.IP(origin.ip[:]), net.IP(owner.ip[:]))
			return cmReject(statusPrivilegeViolation, r.ConnectionSerial, r.VendorID, r.OriginatorSerial, 0)
		}
	}

	logging.DebugLog("eipadapter", "Forward_Close O->T=0x%08X T->O=0x%08X", found.OTConnID, found.TOConnID)
	m.remove(found, ConnectionClosed)

	body := cip.BuildForwardCloseSuccess(r.ConnectionSerial, r.VendorID, r.OriginatorSerial)
	return ObjectResponse{Status: cip.StatusSuccess, Data: body}
}

// statusPrivilegeViolation is CIP general status 0x0F (CIP Vol 1,
// Appendix B).
const statusPrivilegeViolation byte = 0x0F

// handleUnconnectedSend unwraps the embedded CIP request and dispatches it.
// This is how scanners often send Get_Attribute_Single without first opening
// a connection — they wrap the real request inside an Unconnected_Send to the
// Connection Manager.
func (m *ConnectionManager) handleUnconnectedSend(data []byte, origin *peerAddr) ObjectResponse {
	// Format: PriorityTickTime(1) TimeoutTicks(1) MessageRequestSize(2)
	// MessageRequest(...) PathSize(1) Reserved(1) RoutePath(...)
	if len(data) < 4 {
		return ObjectResponse{Status: cip.StatusInvalidParameter}
	}
	msgSize := int(binary.LittleEndian.Uint16(data[2:4]))
	if 4+msgSize > len(data) {
		return ObjectResponse{Status: cip.StatusInvalidParameter}
	}
	embedded := data[4 : 4+msgSize]
	respBytes := m.adapter.dispatchFrom(embedded, 0, 0, origin)
	// Strip the response header we don't need — return raw response data
	// from the embedded service, including the reply service byte.
	if len(respBytes) < 4 {
		return ObjectResponse{Status: cip.StatusInvalidParameter}
	}
	// We've already wrapped — strip the outer status because the caller
	// (dispatch in command.go) will add it back. Return the inner data only,
	// signalled by Status=success and Data=respBytes-without-header.
	//
	// Simpler approach: return the entire reply bytes, including header,
	// as raw Data, and let the outer dispatch skip its wrapping. But our
	// flow always wraps. So we have to peel one layer.
	//
	// Reply format: [reply_svc, 0x00, status, addl_sz, addl_words..., data...]
	addlWords := int(respBytes[3])
	bodyStart := 4 + addlWords*2
	if bodyStart > len(respBytes) {
		return ObjectResponse{Status: cip.StatusInvalidParameter}
	}
	innerStatus := respBytes[2]
	innerData := respBytes[bodyStart:]
	if innerStatus != cip.StatusSuccess {
		// Keep the extended status and error body (e.g. a rejected
		// Forward_Open's 0x0113) rather than dropping them.
		ext := make([]uint16, 0, addlWords)
		for i := 0; i < addlWords; i++ {
			ext = append(ext, binary.LittleEndian.Uint16(respBytes[4+2*i:]))
		}
		return ObjectResponse{Status: innerStatus, ExtData: ext, Data: innerData}
	}
	return ObjectResponse{Status: cip.StatusSuccess, Data: innerData}
}

// allocateConnIDLocked returns a fresh connection ID not used as an O->T
// ID or an adapter-allocated T->O ID. We avoid 0 and the well-known
// 0x20000002 reserved value. m.mu must be held for writing so the ID can be
// registered atomically.
func (m *ConnectionManager) allocateConnIDLocked() uint32 {
	for {
		id := rand.Uint32()
		if id == 0 || id == 0x20000002 {
			continue
		}
		if m.byOT[id] != nil || m.byTO[id] != nil {
			continue
		}
		return id
	}
}

func (m *ConnectionManager) lookupByOT(otID uint32) *Connection {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.byOT[otID]
}

func (m *ConnectionManager) lookupByTO(toID uint32) *Connection {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.byTO[toID]
}

// findTriadLocked returns the open connection with the given connection
// triad (connection serial, originator vendor ID, originator serial), or
// nil. m.mu must be held.
func (m *ConnectionManager) findTriadLocked(serial, vendor uint16, origSerial uint32) *Connection {
	for _, c := range m.byOT {
		if c.ConnectionSerial == serial && c.VendorID == vendor && c.OriginatorSerial == origSerial {
			return c
		}
	}
	return nil
}

// closeAll closes every connection and refuses new ones (adapter shutdown).
func (m *ConnectionManager) closeAll() {
	m.mu.Lock()
	m.closed = true
	all := make([]*Connection, 0, len(m.byOT))
	for _, c := range m.byOT {
		all = append(all, c)
	}
	m.byOT = make(map[uint32]*Connection)
	m.byTO = make(map[uint32]*Connection)
	m.mu.Unlock()
	for _, c := range all {
		m.finish(c, ConnectionClosed)
	}
}

// remove unregisters c — only if the map entries still refer to this very
// connection, so a stale caller can never remove a newer connection that
// reused the ID — then closes it and reports ev (0 = no event).
func (m *ConnectionManager) remove(c *Connection, ev ConnectionEventType) {
	m.mu.Lock()
	if m.byOT[c.OTConnID] == c {
		delete(m.byOT, c.OTConnID)
	}
	if m.byTO[c.TOConnID] == c {
		delete(m.byTO, c.TOConnID)
	}
	m.mu.Unlock()
	m.finish(c, ev)
}

// finish closes c (stopping its goroutine), releases its output assembly
// and reports ev, once. Output assembly data is deliberately left as is.
func (m *ConnectionManager) finish(c *Connection, ev ConnectionEventType) {
	if !c.markClosed() {
		return
	}
	if c.Consume != nil {
		c.Consume.releaseOwner(c)
	}
	if ev != 0 {
		m.adapter.emitConnEvent(c, ev)
	}
}

// parseIOConnectionPath decodes the connection path in a Forward_Open request
// to extract config/consume/produce assembly instances. Recognises:
//
//	0x20 0x04                          Class = Assembly (Class 0x04)
//	0x24 NN  | 0x25 0x00 NN NN         Config Instance (8 or 16-bit)
//	0x2C NN  | 0x2D 0x00 NN NN         Connection Point — first = consume (O->T),
//	                                                       second = produce (T->O)
//	(0x2D form for 16-bit instances follows logical-segment padding rules)
//
// Port segments before the class are silently skipped.
func parseIOConnectionPath(path []byte) (cfg, consume, produce uint32, err error) {
	i := 0
	var classSet bool
	var connPoints []uint32
	var instSet bool
	for i < len(path) {
		seg := path[i]
		segType := (seg >> 5) & 0b111
		switch segType {
		case 0b000: // Port segment — skip
			port := seg & 0x0F
			extended := seg&0x10 != 0
			_ = port
			if extended {
				if i+1 >= len(path) {
					return 0, 0, 0, fmt.Errorf("truncated port segment")
				}
				ln := int(path[i+1])
				adv := 2 + ln
				if adv%2 != 0 {
					adv++
				}
				if i+adv > len(path) {
					return 0, 0, 0, fmt.Errorf("truncated extended port segment")
				}
				i += adv
			} else {
				if i+1 >= len(path) {
					return 0, 0, 0, fmt.Errorf("truncated port segment")
				}
				i += 2
			}
		case 0b001: // Logical segment
			logType := (seg >> 2) & 0b111
			logFmt := seg & 0b11
			i++
			var val uint32
			switch logFmt {
			case 0b00:
				if i+1 > len(path) {
					return 0, 0, 0, fmt.Errorf("truncated 8-bit segment")
				}
				val = uint32(path[i])
				i++
			case 0b01:
				if i+3 > len(path) {
					return 0, 0, 0, fmt.Errorf("truncated 16-bit segment")
				}
				val = uint32(binary.LittleEndian.Uint16(path[i+1 : i+3]))
				i += 3
			case 0b10:
				if i+5 > len(path) {
					return 0, 0, 0, fmt.Errorf("truncated 32-bit segment")
				}
				val = binary.LittleEndian.Uint32(path[i+1 : i+5])
				i += 5
			default:
				return 0, 0, 0, fmt.Errorf("reserved logical format")
			}
			switch logType {
			case 0b000: // class
				if val != 0x04 {
					return 0, 0, 0, fmt.Errorf("connection target must be Assembly class (0x04), got 0x%02X", val)
				}
				classSet = true
			case 0b001: // instance — config
				if !classSet {
					return 0, 0, 0, fmt.Errorf("instance before class")
				}
				if instSet {
					// Some scanners use a sequence of instances rather than
					// connection points. Treat 2nd/3rd instances as
					// consume/produce.
					connPoints = append(connPoints, val)
				} else {
					if val == 0x80 || val == 0 {
						cfg = 0
					} else {
						cfg = val
					}
					instSet = true
				}
			case 0b011: // connection point
				connPoints = append(connPoints, val)
			default:
				return 0, 0, 0, fmt.Errorf("unsupported logical type 0b%03b in connection path", logType)
			}
		default:
			return 0, 0, 0, fmt.Errorf("unsupported segment type 0b%03b in connection path", segType)
		}
	}
	if !classSet {
		return 0, 0, 0, fmt.Errorf("connection path missing class segment")
	}
	switch len(connPoints) {
	case 0:
		// Some adapters allow just a config instance and direction-bits in
		// the network parameters; not supported here.
		return 0, 0, 0, fmt.Errorf("connection path missing connection-point segments")
	case 1:
		// Single connection point typically means "produce only" for an
		// input-only adapter. Treat as produce.
		produce = connPoints[0]
	case 2:
		consume = connPoints[0]
		produce = connPoints[1]
	default:
		return 0, 0, 0, fmt.Errorf("too many connection-point segments: %d", len(connPoints))
	}
	return cfg, consume, produce, nil
}
