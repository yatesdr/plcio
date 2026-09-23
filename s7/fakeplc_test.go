package s7

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// fakePLC is a minimal in-process S7comm server (TPKT/COTP/S7) used to test
// the client end to end over TCP without any real device. It models memory
// per area/DB, answers read items the way a real CPU does (4-byte error
// items, fill bytes after odd items, BIT/REAL byte lengths) and rejects
// responses that would exceed the negotiated PDU.
type fakePLC struct {
	t   *testing.T
	ln  net.Listener
	pdu uint16

	mu       sync.Mutex
	dbs      map[int][]byte  // DB number -> contents
	areas    map[byte][]byte // I/Q/M area code -> contents
	reads    [][]byte        // S7ANY items of every read request, per request
	writes   []fakeWrite
	conns    map[net.Conn]bool
	crSeen   chan struct{} // signalled on every COTP CR (non-blocking)
	holdCR   chan struct{} // when non-nil, COTP CC waits until closed
	stallCR  bool          // when true, COTP CR is never answered
	accepted int

	// tamper, when set, may rewrite each S7 response before it is sent.
	tamper func(req, resp []byte) []byte
	// szlRefuse makes read-SZL requests fail with error 0xD401 (SZL not available).
	szlRefuse bool
	userData  int // number of UserData requests seen
}

// fakeWrite records one write item as seen on the wire.
type fakeWrite struct {
	transport byte // S7ANY transport size
	count     int  // S7ANY element count
	area      byte
	db        int
	bitAddr   int
	dataTS    byte   // data-section transport size
	dataLen   int    // data-section length field
	data      []byte // payload bytes
}

func newFakePLC(t *testing.T) *fakePLC {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakePLC{
		t:      t,
		ln:     ln,
		pdu:    480,
		dbs:    map[int][]byte{},
		areas:  map[byte][]byte{},
		conns:  map[net.Conn]bool{},
		crSeen: make(chan struct{}, 64),
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.conns[conn] = true
			f.accepted++
			f.mu.Unlock()
			go f.serve(conn)
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		f.mu.Lock()
		for c := range f.conns {
			c.Close()
		}
		f.mu.Unlock()
	})
	return f
}

func (f *fakePLC) addr() string { return f.ln.Addr().String() }

// connect dials the fake PLC as an S7-1200 (rack 0, slot 0).
func (f *fakePLC) connect(opts ...Option) *Client {
	f.t.Helper()
	c, err := Connect(f.addr(), append([]Option{WithRackSlot(0, 0), WithTimeout(2 * time.Second)}, opts...)...)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(c.Close)
	return c
}

func (f *fakePLC) setDB(n int, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dbs[n] = data
}

func (f *fakePLC) db(n int) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.dbs[n]...)
}

func (f *fakePLC) openConns() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.conns)
}

func (f *fakePLC) readRequests() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.reads...)
}

func (f *fakePLC) writeLog() []fakeWrite {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeWrite(nil), f.writes...)
}

// waitOpenConns polls until the server sees exactly n open connections.
func (f *fakePLC) waitOpenConns(n int) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if f.openConns() == n {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func (f *fakePLC) serve(conn net.Conn) {
	defer func() {
		conn.Close()
		f.mu.Lock()
		delete(f.conns, conn)
		f.mu.Unlock()
	}()
	for {
		header := make([]byte, 4)
		if _, err := io.ReadFull(conn, header); err != nil {
			return
		}
		payload := make([]byte, int(binary.BigEndian.Uint16(header[2:]))-4)
		if _, err := io.ReadFull(conn, payload); err != nil {
			return
		}
		if len(payload) > 1 && payload[1] == cotpCR {
			select {
			case f.crSeen <- struct{}{}:
			default:
			}
			f.mu.Lock()
			stall, hold := f.stallCR, f.holdCR
			f.mu.Unlock()
			if stall {
				io.Copy(io.Discard, conn) // never answer; wait for the client to give up
				return
			}
			if hold != nil {
				<-hold
			}
			fakeTPKT(conn, []byte{6, cotpCC, 0, 1, 0, 1, 0})
			continue
		}
		req := payload[3:]
		resp := f.handle(req)
		if resp == nil {
			return
		}
		f.mu.Lock()
		tamper := f.tamper
		f.mu.Unlock()
		if tamper != nil {
			resp = tamper(req, append([]byte(nil), resp...))
		}
		fakeTPKT(conn, append([]byte{2, cotpDT, 0x80}, resp...))
	}
}

func fakeTPKT(conn net.Conn, data []byte) {
	out := []byte{3, 0, 0, 0}
	binary.BigEndian.PutUint16(out[2:], uint16(4+len(data)))
	conn.Write(append(out, data...))
}

// ackData builds an S7 AckData PDU answering req.
func ackData(req []byte, errClass byte, params, data []byte) []byte {
	h := []byte{s7ProtocolID, s7MsgAckData, 0, 0, req[4], req[5], 0, 0, 0, 0, errClass, 0}
	binary.BigEndian.PutUint16(h[6:], uint16(len(params)))
	binary.BigEndian.PutUint16(h[8:], uint16(len(data)))
	return append(append(h, params...), data...)
}

// memFor returns the backing memory for an S7ANY area/DB (nil if absent).
// Must be called with f.mu held.
func (f *fakePLC) memFor(area byte, db int) []byte {
	if area == s7AreaDB {
		return f.dbs[db]
	}
	return f.areas[area]
}

func tsElemSize(ts byte) int {
	switch ts {
	case tsWORD, tsINT:
		return 2
	case tsDWORD, tsDINT, tsREAL:
		return 4
	case tsTIMER, tsCOUNTER:
		return 2
	default:
		return 1
	}
}

// fakeSZL0424 is a plausible SZL 0x0424 record (CPU in RUN).
var fakeSZL0424 = []byte{0x51, 0x44, 0xFF, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}

// handleUserData answers a read-SZL request like a CPU does. Must be called
// with f.mu held.
func (f *fakePLC) handleUserData(req []byte) []byte {
	f.userData++
	d := req[18:]
	id, index := d[4:6], d[6:8]
	params := []byte{0x00, 0x01, 0x12, 0x08, 0x12, 0x84, 0x01, 0x01, 0x00, 0x00, 0x00, 0x00}
	var data []byte
	if f.szlRefuse {
		params[10], params[11] = 0xD4, 0x01
		data = []byte{dataItemNotExist, 0x00, 0x00, 0x00}
	} else {
		body := append(append(append([]byte{}, id...), index...), 0x00, 0x14, 0x00, 0x01)
		body = append(body, fakeSZL0424...)
		data = append([]byte{dataItemSuccess, 0x09, 0, 0}, body...)
		binary.BigEndian.PutUint16(data[2:], uint16(len(body)))
	}
	h := []byte{s7ProtocolID, s7MsgUserData, 0, 0, req[4], req[5], 0, 0, 0, 0}
	binary.BigEndian.PutUint16(h[6:], uint16(len(params)))
	binary.BigEndian.PutUint16(h[8:], uint16(len(data)))
	return append(append(h, params...), data...)
}

// tcOffset maps an S7ANY item to a byte offset: timers/counters are
// addressed by number (2 bytes each), everything else by bit address.
func tcOffset(ts byte, bitAddr int) int {
	if ts == tsTIMER || ts == tsCOUNTER {
		return bitAddr * 2
	}
	return bitAddr >> 3
}

func (f *fakePLC) handle(req []byte) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req[1] == s7MsgUserData {
		return f.handleUserData(req)
	}
	paramLen := int(binary.BigEndian.Uint16(req[6:]))
	params := req[10 : 10+paramLen]
	switch params[0] {
	case s7FuncSetupComm:
		p := []byte{s7FuncSetupComm, 0, 0, 1, 0, 1, 0, 0}
		binary.BigEndian.PutUint16(p[6:], f.pdu)
		return ackData(req, 0, p, nil)
	case s7FuncRead:
		if len(req) > int(f.pdu) {
			return ackData(req, errClassNoResource, nil, nil)
		}
		n := int(params[1])
		f.reads = append(f.reads, append([]byte(nil), params[2:]...))
		var data []byte
		for i := 0; i < n; i++ {
			item := params[2+12*i : 14+12*i]
			ts := item[3]
			count := int(binary.BigEndian.Uint16(item[4:]))
			db := int(binary.BigEndian.Uint16(item[6:]))
			bitAddr := int(item[9])<<16 | int(item[10])<<8 | int(item[11])
			mem := f.memFor(item[8], db)
			var out []byte
			switch {
			case mem == nil:
				out = []byte{dataItemNotExist, 0, 0, 0}
			case ts == tsBIT:
				if count != 1 || bitAddr>>3 >= len(mem) {
					out = []byte{dataItemAddressError, 0, 0, 0}
					break
				}
				// BIT response: transport 0x03, length in bytes, value 0/1
				out = []byte{dataItemSuccess, 0x03, 0, 1, (mem[bitAddr>>3] >> (bitAddr & 7)) & 1}
			default:
				size := count * tsElemSize(ts)
				off := tcOffset(ts, bitAddr)
				if off+size > len(mem) {
					out = []byte{dataItemAddressError, 0, 0, 0}
					break
				}
				out = []byte{dataItemSuccess, 0x04, 0, 0}
				binary.BigEndian.PutUint16(out[2:], uint16(size*8))
				if ts == tsTIMER || ts == tsCOUNTER {
					out[1] = 0x09 // OCTET STRING: length in bytes
					binary.BigEndian.PutUint16(out[2:], uint16(size))
				}
				if ts == tsREAL {
					out[1] = 0x07 // REAL: length in bytes
					binary.BigEndian.PutUint16(out[2:], uint16(size))
				}
				out = append(out, mem[off:off+size]...)
			}
			if i < n-1 && len(out)%2 == 1 {
				out = append(out, 0) // fill byte
			}
			data = append(data, out...)
		}
		if 14+len(data) > int(f.pdu) {
			return ackData(req, errClassNoResource, nil, nil)
		}
		return ackData(req, 0, []byte{s7FuncRead, byte(n)}, data)
	case s7FuncWrite:
		item := params[2:14]
		d := req[10+paramLen:]
		w := fakeWrite{
			transport: item[3],
			count:     int(binary.BigEndian.Uint16(item[4:])),
			db:        int(binary.BigEndian.Uint16(item[6:])),
			area:      item[8],
			bitAddr:   int(item[9])<<16 | int(item[10])<<8 | int(item[11]),
			dataTS:    d[1],
			dataLen:   int(binary.BigEndian.Uint16(d[2:])),
		}
		n := itemByteLenForTest(w.dataTS, w.dataLen)
		w.data = append([]byte(nil), d[4:4+n]...)
		f.writes = append(f.writes, w)
		code := byte(dataItemSuccess)
		mem := f.memFor(w.area, w.db)
		off := tcOffset(w.transport, w.bitAddr)
		switch {
		case mem == nil:
			code = dataItemNotExist
		case w.transport == tsBIT:
			if w.count != 1 || n != 1 {
				code = dataItemTypeInconsistent
			} else if w.data[0] != 0 {
				mem[off] |= 1 << (w.bitAddr & 7)
			} else {
				mem[off] &^= 1 << (w.bitAddr & 7)
			}
		case w.count*tsElemSize(w.transport) != n:
			// A real CPU rejects items whose declared size differs from the data
			code = dataItemTypeInconsistent
		case off+n > len(mem):
			code = dataItemAddressError
		default:
			copy(mem[off:], w.data)
		}
		return ackData(req, 0, []byte{s7FuncWrite, 1}, []byte{code})
	}
	f.t.Errorf("fake PLC: unexpected function 0x%02X", params[0])
	return nil
}

// itemByteLenForTest decodes a data-section length independently of the
// client code under test: BIT/REAL/OCTET lengths are bytes, others bits.
func itemByteLenForTest(ts byte, n int) int {
	switch ts {
	case 0x03, 0x07, 0x09:
		return n
	}
	return (n + 7) / 8
}
