package driver

import (
	"encoding/binary"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/yatesdr/plcio/eip"
)

// logixMultiPeer serves unconnected EtherNet/IP on 127.0.0.1:44818 for any
// number of TCP sessions, so reconnects can be observed. Forward Open is
// rejected, leaving clients on unconnected messaging.
func logixMultiPeer(t *testing.T, handle func([]byte) []byte) (string, func() int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:44818")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	open := 0
	var wg sync.WaitGroup
	serve := func(conn net.Conn) {
		defer func() {
			conn.Close()
			mu.Lock()
			open--
			mu.Unlock()
		}()
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		for {
			frame, err := eip.ReadFrame(conn)
			if err != nil {
				return
			}
			var reply *eip.Frame
			switch frame.Command {
			case 0x65:
				reply = frame.Reply(0, []byte{1, 0, 0, 0})
				reply.SessionHandle = 1234
			case 0x66:
				return
			case 0x6f:
				body, err := eip.ParseRRData(frame.Data)
				if err != nil {
					t.Error(err)
					return
				}
				packet, err := eip.ParseEipCommonPacket(body)
				if err != nil || len(packet.Items) != 2 {
					t.Errorf("CPF %v", err)
					return
				}
				req := packet.Items[1].Data
				var response []byte
				if req[0] == 0x54 || req[0] == 0x5b {
					response = []byte{req[0] | 0x80, 0, 0x01, 0x01, 0x00, 0x01}
				} else if response = handle(req); response == nil {
					return
				}
				cpf := &eip.EipCommonPacket{Items: []eip.EipCommonPacketItem{
					{TypeId: 0},
					{TypeId: 0xb2, Length: uint16(len(response)), Data: response},
				}}
				reply = frame.Reply(0, eip.BuildRRData(cpf.Bytes()))
			default:
				t.Errorf("encap %x", frame.Command)
				return
			}
			if _, err := conn.Write(reply.Bytes()); err != nil {
				return
			}
		}
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			open++
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				serve(conn)
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		for _, conn := range conns {
			conn.Close()
		}
		mu.Unlock()
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("peer did not stop")
		}
	})
	return "127.0.0.1", func() int {
		mu.Lock()
		defer mu.Unlock()
		return open
	}
}

func logixTestSymbol(req []byte) string {
	start := 2 + int(req[1])*2
	path := req[2:start]
	name := ""
	for len(path) > 1 && path[0] == 0x91 {
		n := int(path[1])
		if name != "" {
			name += "."
		}
		name += string(path[2 : 2+n])
		path = path[2+n+n%2:]
	}
	return name
}

// logixPairPeer serves scalars A=1, B=2, and a structure S {X DINT, Y REAL}
// whose whole-structure reads are refused, forcing member expansion.
func logixPairPeer(t *testing.T) (string, func() int) {
	definition := append([]byte{0, 0, 0xc4, 0, 0, 0, 0, 0, 0, 0, 0xca, 0, 4, 0, 0, 0},
		[]byte("Pair;n\x00X\x00Y\x00")...)
	scalar := func(req []byte) []byte {
		switch logixTestSymbol(req) {
		case "A":
			return []byte{0xcc, 0, 0, 0, 0xc4, 0, 1, 0, 0, 0}
		case "B":
			return []byte{0xcc, 0, 0, 0, 0xc4, 0, 2, 0, 0, 0}
		case "S.X":
			return []byte{0xcc, 0, 0, 0, 0xc4, 0, 7, 0, 0, 0}
		case "S.Y":
			return []byte{0xcc, 0, 0, 0, 0xca, 0, 0, 0, 0xc0, 0x3f}
		}
		return []byte{req[0] | 0x80, 0, 0x0f, 0} // privilege violation
	}
	return logixMultiPeer(t, func(req []byte) []byte {
		start := 2 + int(req[1])*2
		if len(req) > 5 && req[2] == 0x20 && req[3] == 0x6c {
			switch req[0] {
			case 0x03:
				body := req[start:]
				n := int(binary.LittleEndian.Uint16(body))
				out := binary.LittleEndian.AppendUint16([]byte{0x83, 0, 0, 0}, uint16(n))
				for i := 0; i < n; i++ {
					id := binary.LittleEndian.Uint16(body[2+2*i:])
					out = append(binary.LittleEndian.AppendUint16(out, id), 0, 0)
					switch id {
					case 5:
						out = binary.LittleEndian.AppendUint32(out, 8)
					case 4:
						out = binary.LittleEndian.AppendUint32(out, uint32((len(definition)+23+3)/4))
					case 3, 2:
						out = binary.LittleEndian.AppendUint16(out, uint16(map[uint16]int{3: 8, 2: 2}[id]))
					case 1:
						out = binary.LittleEndian.AppendUint16(out, 0xbeef)
					}
				}
				return out
			case 0x4c:
				return append([]byte{0xcc, 0, 0, 0}, definition...)
			}
		}
		switch req[0] {
		case 0x0a:
			body := req[start:]
			n := int(binary.LittleEndian.Uint16(body))
			out := make([]byte, 2+2*n)
			binary.LittleEndian.PutUint16(out, uint16(n))
			for i := 0; i < n; i++ {
				begin := int(binary.LittleEndian.Uint16(body[2+2*i:]))
				end := len(body)
				if i+1 < n {
					end = int(binary.LittleEndian.Uint16(body[4+2*i:]))
				}
				binary.LittleEndian.PutUint16(out[2+2*i:], uint16(len(out)))
				out = append(out, scalar(body[begin:end])...)
			}
			return append([]byte{0x8a, 0, 0, 0}, out...)
		case 0x4c, 0x52:
			return scalar(req)
		case 0x4d:
			return []byte{0xcd, 0, 0, 0}
		case 0x55: // symbol browse after a reconnect: empty table
			return []byte{0xd5, 0, 0, 0}
		}
		t.Errorf("unexpected CIP %x", req)
		return nil
	})
}

var logixPairTags = []TagInfo{
	{Name: "A", TypeCode: 0xc4, Instance: 1},
	{Name: "B", TypeCode: 0xc4, Instance: 2},
	{Name: "S", TypeCode: 0x8001, Instance: 3},
}

// logix.Client.Read returns structures before batched scalars and expands an
// unreadable structure into members; the driver must still answer 1:1 in
// request order.
func TestLogixAdapterReadAlignsResultsWithRequests(t *testing.T) {
	host, _ := logixPairPeer(t)
	a, _ := NewLogixAdapter(&PLCConfig{Family: FamilyLogix, Address: host, Timeout: time.Second})
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.SetTags(logixPairTags)
	values, err := a.Read([]TagRequest{{Name: "A"}, {Name: "S"}, {Name: "B"}, {Name: "A"}})
	if err != nil || len(values) != 4 {
		t.Fatalf("%d results: %v", len(values), err)
	}
	for i, want := range []struct {
		name  string
		value any
	}{
		{"A", int64(1)}, {"S", map[string]any{"X": int64(7), "Y": float64(1.5)}}, {"B", int64(2)}, {"A", int64(1)},
	} {
		v := values[i]
		if v.Name != want.name || v.Error != nil || !reflect.DeepEqual(v.Value, want.value) {
			t.Fatalf("result %d: %s=%#v err=%v, want %s=%#v", i, v.Name, v.Value, v.Error, want.name, want.value)
		}
	}
	if values[1].DataType != 0x8001 {
		t.Fatalf("folded structure type %#x", values[1].DataType)
	}
}

// Connect must release the previous session, and Close/Connect must be safe
// against concurrent Read/Write/Keepalive (run with -race).
func TestLogixAdapterReconnectAndConcurrentClose(t *testing.T) {
	host, open := logixPairPeer(t)
	a, _ := NewLogixAdapter(&PLCConfig{Family: FamilyLogix, Address: host, Timeout: time.Second})
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	first := a.Client()
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	if a.Client() == first || first.IsConnected() {
		t.Fatal("reconnect leaked the previous client")
	}
	deadline := time.Now().Add(time.Second)
	for open() != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := open(); n != 1 {
		t.Fatalf("%d sessions open after reconnect", n)
	}

	a.SetTags(logixPairTags)
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				switch (g + i) % 4 {
				case 0:
					_, _ = a.Read([]TagRequest{{Name: "A"}, {Name: "B"}})
				case 1:
					_ = a.Write("A", int64(i))
				case 2:
					_ = a.Keepalive()
					_ = a.IsConnected()
					_ = a.ConnectionMode()
				case 3:
					if i%5 == 0 {
						_ = a.Close()
						_ = a.Connect()
					}
				}
			}
		}(g)
	}
	wg.Wait()
	_ = a.Close()
	if a.Client() != nil || a.IsConnected() {
		t.Fatal("adapter still connected after Close")
	}
}
