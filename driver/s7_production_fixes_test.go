package driver

import (
	"encoding/binary"
	"math"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"
)

// s7MultiPeer is a multi-connection S7comm peer backed by one DB1 image. It
// records every write payload and tracks open connections so adapter
// lifecycle tests can detect leaked clients.
type s7MultiPeer struct {
	ln     net.Listener
	mu     sync.Mutex
	db1    []byte
	writes [][]byte
	conns  map[net.Conn]bool
	crSeen chan struct{}
	hold   chan struct{}
}

func newS7MultiPeer(t *testing.T) *s7MultiPeer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &s7MultiPeer{ln: ln, db1: make([]byte, 64), conns: map[net.Conn]bool{}, crSeen: make(chan struct{}, 64)}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			p.conns[conn] = true
			p.mu.Unlock()
			go p.serve(conn)
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		p.mu.Lock()
		for c := range p.conns {
			c.Close()
		}
		p.mu.Unlock()
	})
	return p
}

func (p *s7MultiPeer) open() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.conns)
}

func (p *s7MultiPeer) waitOpen(n int) bool {
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if p.open() == n {
			return true
		}
	}
	return false
}

func (p *s7MultiPeer) serve(conn net.Conn) {
	defer func() {
		conn.Close()
		p.mu.Lock()
		delete(p.conns, conn)
		p.mu.Unlock()
	}()
	for {
		req, err := tpktRead(conn)
		if err != nil {
			return
		}
		if req[1] == 0xe0 {
			select {
			case p.crSeen <- struct{}{}:
			default:
			}
			p.mu.Lock()
			hold := p.hold
			p.mu.Unlock()
			if hold != nil {
				<-hold
			}
			tpktReply(conn, []byte{6, 0xd0, 0, 1, 0, 1, 0})
			continue
		}
		s := req[3:]
		if s[1] == 0x07 { // UserData read-SZL (keepalive): one SZL 0x0424 record
			resp := []byte{0x32, 7, 0, 0, s[4], s[5], 0, 12, 0, 32,
				0, 1, 0x12, 8, 0x12, 0x84, 1, 1, 0, 0, 0, 0,
				0xff, 9, 0, 28, 4, 0x24, 0, 0, 0, 20, 0, 1}
			resp = append(resp, make([]byte, 20)...)
			tpktReply(conn, append([]byte{2, 0xf0, 128}, resp...))
			continue
		}
		paramLen := int(binary.BigEndian.Uint16(s[6:]))
		params := s[10 : 10+paramLen]
		header := []byte{0x32, 3, 0, 0, s[4], s[5], 0, 2, 0, 0, 0, 0}
		var data []byte
		p.mu.Lock()
		switch params[0] {
		case 0xf0:
			header[7] = 8
			data = []byte{0xf0, 0, 0, 1, 0, 1, 1, 0xe0}
		case 4: // one BYTE/WORD/DWORD item per request is enough here
			n := int(binary.BigEndian.Uint16(params[6:]))
			if params[5] == 0x04 {
				n *= 2
			} else if params[5] == 0x06 || params[5] == 0x08 {
				n *= 4
			}
			off := (int(params[11])<<16 | int(params[12])<<8 | int(params[13])) >> 3
			data = []byte{4, 1, 255, 4, 0, 0}
			binary.BigEndian.PutUint16(data[4:], uint16(n*8))
			data = append(data, p.db1[off:off+n]...)
			binary.BigEndian.PutUint16(header[8:], uint16(len(data)-2))
		case 5:
			d := s[10+paramLen:]
			// Like a real CPU: BIT, REAL and OCTET STRING data lengths are in
			// bytes, all other transport sizes in bits.
			n := int(binary.BigEndian.Uint16(d[2:]))
			if d[1] != 0x03 && d[1] != 0x07 && d[1] != 0x09 {
				n /= 8
			}
			payload := append([]byte(nil), d[4:4+n]...)
			off := (int(params[11])<<16 | int(params[12])<<8 | int(params[13])) >> 3
			copy(p.db1[off:], payload)
			p.writes = append(p.writes, payload)
			data = []byte{5, 1, 255}
			header[9] = 1
		}
		p.mu.Unlock()
		tpktReply(conn, append([]byte{2, 0xf0, 128}, append(header, data...)...))
	}
}

func TestS7AdapterUntypedWriteNeedsSizeOrType(t *testing.T) {
	p := newS7MultiPeer(t)
	a, _ := NewS7Adapter(&PLCConfig{Address: p.ln.Addr().String(), Family: FamilyS7, Timeout: time.Second,
		Tags: []TagSelection{{Name: "DB1.4", DataType: "REAL"}, {Name: "DB1.20", DataType: "FLOAT"}}})
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	realBytes := func(v float32) []byte {
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, math.Float32bits(v))
		return b
	}

	// Documented example with a configured REAL: 4 bytes, not 8.
	if err := a.Write("DB1.4", 3.14); err != nil {
		t.Fatal(err)
	}
	// Unconfigured size-less address: refuse instead of writing LREAL/LINT.
	for _, v := range []any{3.14, int64(5)} {
		if err := a.Write("DB1.12", v); err == nil {
			t.Fatalf("untyped DB1.12 <- %T accepted", v)
		}
	}
	// Unknown configured type.
	if err := a.Write("DB1.20", 1.5); err == nil {
		t.Fatal("unknown configured type accepted")
	}
	// Sized addresses use the address width.
	if err := a.Write("DB1.DBD8", 3.14); err == nil {
		t.Fatal("fractional float to untyped DBD accepted")
	}
	if err := a.Write("DB1.DBD8", float64(3)); err != nil {
		t.Fatal(err)
	}
	if err := a.Write("DB1.DBW30", int64(7)); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	writes := append([][]byte(nil), p.writes...)
	p.mu.Unlock()
	want := [][]byte{realBytes(3.14), {0, 0, 0, 3}, {0, 7}}
	if !reflect.DeepEqual(writes, want) {
		t.Fatalf("writes %x, want %x", writes, want)
	}
}

func TestS7AdapterConnectClosesPreviousClient(t *testing.T) {
	p := newS7MultiPeer(t)
	a, _ := NewS7Adapter(&PLCConfig{Address: p.ln.Addr().String(), Family: FamilyS7, Timeout: time.Second})
	for i := 0; i < 3; i++ {
		if err := a.Connect(); err != nil {
			t.Fatal(err)
		}
	}
	if !p.waitOpen(1) {
		t.Fatalf("%d connections open after repeated Connect, want 1", p.open())
	}
	a.Close()
	if !p.waitOpen(0) {
		t.Fatalf("%d connections open after Close", p.open())
	}
}

func TestS7AdapterCloseDuringConnect(t *testing.T) {
	p := newS7MultiPeer(t)
	hold := make(chan struct{})
	p.mu.Lock()
	p.hold = hold
	p.mu.Unlock()
	a, _ := NewS7Adapter(&PLCConfig{Address: p.ln.Addr().String(), Family: FamilyS7, Timeout: time.Second})
	done := make(chan error, 1)
	go func() { done <- a.Connect() }()
	<-p.crSeen
	a.Close()
	close(hold)
	if err := <-done; err == nil {
		t.Error("Connect succeeded after Close")
	}
	if a.IsConnected() || a.Client() != nil {
		t.Fatal("Close during Connect left a client installed")
	}
	if !p.waitOpen(0) {
		t.Fatalf("%d connections leaked", p.open())
	}
}

func TestS7AdapterConcurrentLifecycleIsRaceFree(t *testing.T) {
	p := newS7MultiPeer(t)
	a, _ := NewS7Adapter(&PLCConfig{Address: p.ln.Addr().String(), Family: FamilyS7, Timeout: time.Second,
		Tags: []TagSelection{{Name: "DB1.0", DataType: "INT"}}})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				a.Connect()
				if j%3 == 0 {
					a.Close()
				}
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				a.Read([]TagRequest{{Name: "DB1.0", TypeHint: "INT"}})
				a.Write("DB1.0", int64(j))
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				a.IsConnected()
				a.ConnectionMode()
				a.GetDeviceInfo()
				a.Client()
			}
		}()
	}
	wg.Wait()
	a.Close()
	if !p.waitOpen(0) {
		t.Fatalf("%d connections leaked", p.open())
	}
}
