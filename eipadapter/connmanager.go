package eipadapter

import (
	"encoding/binary"
	"fmt"
	"math/rand"
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
	OTConnID uint32 // O->T connection ID (scanner-chosen, used by O->T data we receive)
	TOConnID uint32 // T->O connection ID (we chose, scanner uses it to receive)

	OTRPI uint32 // µs
	TORPI uint32

	ConnectionSerial uint16
	VendorID         uint16
	OriginatorSerial uint32
	TimeoutMultiplier byte

	Consume *Assembly // O->T input to adapter
	Produce *Assembly // T->O output from adapter

	// peerAddr is filled in when we receive the first O->T packet (or
	// proactively from the Forward_Open path when supplied).
	peerAddr peerAddr

	seqOut uint32 // 32-bit cyclic packet counter for T->O
	seqMu  sync.Mutex

	lastInboundAt time.Time
	createdAt     time.Time

	mu     sync.RWMutex
	closed bool
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

	mu       sync.RWMutex
	byOT     map[uint32]*Connection // index by O->T connection ID
	byTO     map[uint32]*Connection
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
		return m.handleForwardOpen(req.Data, false)
	case cmForwardOpenLarge:
		return m.handleForwardOpen(req.Data, true)
	case cmForwardClose:
		return m.handleForwardClose(req.Data)
	case cmUnconnectedSend:
		return m.handleUnconnectedSend(req.Data)
	default:
		return ObjectResponse{Status: cip.StatusServiceNotSupported}
	}
}

func (m *ConnectionManager) handleForwardOpen(data []byte, large bool) ObjectResponse {
	r, err := cip.ParseForwardOpenRequest(data, large)
	if err != nil {
		return ObjectResponse{Status: cip.StatusInvalidParameter}
	}

	cfgInst, consume, produce, err := parseIOConnectionPath(r.ConnectionPath)
	if err != nil {
		logging.DebugLog("eipadapter", "Forward_Open path parse: %v", err)
		// Build error with ext status 0x0315 (Invalid Connection Path).
		return ObjectResponse{
			Status:  cip.StatusConnectionFailure,
			ExtData: []uint16{cip.ExtInvalidConnPath},
		}
	}

	var consumeAsm, produceAsm *Assembly
	if consume != 0 {
		consumeAsm = m.adapter.Assembly(consume)
		if consumeAsm == nil {
			return ObjectResponse{Status: cip.StatusConnectionFailure, ExtData: []uint16{cip.ExtInvalidConnPath}}
		}
	}
	if produce != 0 {
		produceAsm = m.adapter.Assembly(produce)
		if produceAsm == nil {
			return ObjectResponse{Status: cip.StatusConnectionFailure, ExtData: []uint16{cip.ExtInvalidConnPath}}
		}
	}

	if m.adapter.cfg.OnForwardOpen != nil {
		if err := m.adapter.cfg.OnForwardOpen(&ForwardOpenContext{
			Request:         r,
			ConfigInstance:  cfgInst,
			ConsumeInstance: consume,
			ProduceInstance: produce,
		}); err != nil {
			logging.DebugLog("eipadapter", "Forward_Open rejected by callback: %v", err)
			return ObjectResponse{Status: cip.StatusConnectionFailure}
		}
	}

	// Allocate our T->O connection ID — scanner uses this to identify
	// incoming I/O packets from us.
	toConnID := m.allocateConnID()

	c := &Connection{
		OTConnID:          r.OTConnectionID,
		TOConnID:          toConnID,
		OTRPI:             r.OTRPI,
		TORPI:             r.TORPI,
		ConnectionSerial:  r.ConnectionSerial,
		VendorID:          r.VendorID,
		OriginatorSerial:  r.OriginatorSerial,
		TimeoutMultiplier: r.TimeoutMultiplier,
		Consume:           consumeAsm,
		Produce:           produceAsm,
		createdAt:         m.adapter.cfg.Now(),
	}

	m.mu.Lock()
	m.byOT[c.OTConnID] = c
	m.byTO[c.TOConnID] = c
	m.mu.Unlock()

	if produceAsm != nil {
		go m.adapter.runProducer(c)
	}

	logging.DebugLog("eipadapter", "Forward_Open accepted: O->T=0x%08X T->O=0x%08X consume=%d produce=%d OTRPI=%dus TORPI=%dus",
		c.OTConnID, c.TOConnID, consume, produce, r.OTRPI, r.TORPI)

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

func (m *ConnectionManager) handleForwardClose(data []byte) ObjectResponse {
	r, err := cip.ParseForwardCloseRequest(data)
	if err != nil {
		return ObjectResponse{Status: cip.StatusInvalidParameter}
	}

	var found *Connection
	m.mu.Lock()
	for _, c := range m.byOT {
		if c.ConnectionSerial == r.ConnectionSerial &&
			c.VendorID == r.VendorID &&
			c.OriginatorSerial == r.OriginatorSerial {
			found = c
			delete(m.byOT, c.OTConnID)
			delete(m.byTO, c.TOConnID)
			break
		}
	}
	m.mu.Unlock()

	if found != nil {
		found.mu.Lock()
		found.closed = true
		found.mu.Unlock()
		logging.DebugLog("eipadapter", "Forward_Close O->T=0x%08X T->O=0x%08X", found.OTConnID, found.TOConnID)
	}

	body := cip.BuildForwardCloseSuccess(r.ConnectionSerial, r.VendorID, r.OriginatorSerial)
	return ObjectResponse{Status: cip.StatusSuccess, Data: body}
}

// handleUnconnectedSend unwraps the embedded CIP request and dispatches it.
// This is how scanners often send Get_Attribute_Single without first opening
// a connection — they wrap the real request inside an Unconnected_Send to the
// Connection Manager.
func (m *ConnectionManager) handleUnconnectedSend(data []byte) ObjectResponse {
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
	respBytes := m.adapter.dispatch(embedded, 0, 0)
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
		return ObjectResponse{Status: innerStatus}
	}
	return ObjectResponse{Status: cip.StatusSuccess, Data: innerData}
}

// allocateConnID returns a fresh T->O connection ID that isn't currently
// in use. We avoid 0 and the well-known 0x20000002 reserved value.
func (m *ConnectionManager) allocateConnID() uint32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for {
		id := rand.Uint32()
		if id == 0 || id == 0x20000002 {
			continue
		}
		if _, dup := m.byTO[id]; dup {
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

func (m *ConnectionManager) closeAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.byOT {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
	}
	m.byOT = make(map[uint32]*Connection)
	m.byTO = make(map[uint32]*Connection)
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
