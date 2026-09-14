package driver

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yatesdr/plcio/ads"
	"github.com/yatesdr/plcio/eip"
	"github.com/yatesdr/plcio/logix"
	"github.com/yatesdr/plcio/omron"
	"github.com/yatesdr/plcio/pccc"
	"github.com/yatesdr/plcio/s7"
)

// Each peer speaks only the small protocol subset needed by the boundary
// fixture. Replies and expected value bytes are independent wire literals.
func adapterPeer(t *testing.T, serve func(net.Conn)) string {
	return adapterPeerAt(t, "127.0.0.1:0", serve)
}
func adapterPeerAt(t *testing.T, address string, serve func(net.Conn)) string {
	t.Helper()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var active net.Conn
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		mu.Lock()
		active = conn
		mu.Unlock()
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		serve(conn)
	}()
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		if active != nil {
			active.Close()
		}
		mu.Unlock()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("peer did not stop")
		}
	})
	return listener.Addr().String()
}

func adsPeerRequest(conn net.Conn) ([]byte, error) {
	header := make([]byte, 6)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	size := binary.LittleEndian.Uint32(header[2:])
	if size < 32 || size > 1<<20 {
		return nil, errors.New("bad ADS request")
	}
	request := make([]byte, 6+int(size))
	copy(request, header)
	_, err := io.ReadFull(conn, request[6:])
	return request, err
}
func adsPeerReply(conn net.Conn, request, body []byte) {
	reply := make([]byte, 38+len(body))
	binary.LittleEndian.PutUint32(reply[2:6], uint32(32+len(body)))
	copy(reply[6:14], request[14:22])
	copy(reply[14:22], request[6:14])
	copy(reply[22:24], request[22:24])
	reply[24] = 5
	binary.LittleEndian.PutUint32(reply[26:30], uint32(len(body)))
	copy(reply[34:38], request[34:38])
	copy(reply[38:], body)
	conn.Write(reply)
}
func adsPeerData(data []byte) []byte {
	out := make([]byte, 8+len(data))
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(data)))
	copy(out[8:], data)
	return out
}
func TestADSAdapterPreservesPartialRead(t *testing.T) {
	var reads atomic.Int32
	endpoint := adapterPeer(t, func(conn net.Conn) {
		for {
			req, err := adsPeerRequest(conn)
			if err != nil {
				return
			}
			cmd := binary.LittleEndian.Uint16(req[22:])
			var body []byte
			if cmd == 1 {
				body = make([]byte, 24)
				body[4] = 3
				copy(body[8:], "Fixture")
			} else {
				group := binary.LittleEndian.Uint32(req[38:])
				switch group {
				case 0xf008, 0xf00f, 0xf00c:
					body = []byte{1, 7, 0, 0}
				case 0xf009:
					name := string(bytes.TrimRight(req[54:], "\x00"))
					if name == "bad" {
						body = []byte{0x10, 7, 0, 0, 0, 0, 0, 0}
						break
					}
					entry := make([]byte, 30+len(name)+1+5+1)
					binary.LittleEndian.PutUint32(entry, uint32(len(entry)))
					binary.LittleEndian.PutUint32(entry[4:], 0x4040)
					binary.LittleEndian.PutUint32(entry[12:], 4)
					binary.LittleEndian.PutUint32(entry[16:], 3)
					binary.LittleEndian.PutUint16(entry[24:], uint16(len(name)))
					binary.LittleEndian.PutUint16(entry[26:], 4)
					copy(entry[30:], name)
					copy(entry[31+len(name):], "DINT")
					body = adsPeerData(entry)
				case 0xf003:
					body = adsPeerData([]byte{42, 0, 0, 0})
				case 0xf005:
					if reads.Add(1) > 1 {
						return
					}
					body = adsPeerData([]byte{25, 0, 0, 0})
				case 0xf006:
					body = []byte{0, 0, 0, 0}
				default:
					t.Errorf("ADS group %x", group)
					return
				}
			}
			adsPeerReply(conn, req, body)
		}
	})
	a, err := NewADSAdapterWithOptions(&PLCConfig{Address: endpoint, Family: FamilyBeckhoff, Timeout: time.Second}, ads.WithMaxBatchItems(1))
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Connect(); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	values, err := a.Read([]TagRequest{{Name: "good"}, {Name: "bad"}, {Name: "lost"}, {Name: "later"}})
	if !errors.Is(err, ads.ErrConnectionLost) || !errors.Is(err, io.EOF) || len(values) != 4 {
		t.Fatalf("%v %v", values, err)
	}
	if values[0].Value != int64(25) || values[0].StableValue != int64(25) || !bytes.Equal(values[0].Bytes, []byte{25, 0, 0, 0}) || values[0].Error != nil {
		t.Fatal(values[0])
	}
	var device *ads.AdsError
	if !errors.As(values[1].Error, &device) || device.Code != 0x710 {
		t.Fatal(values[1])
	}
	if values[2].Error == nil || values[3].Error == nil || a.IsConnected() {
		t.Fatal("lost partial error or connection state")
	}
}

func eipPeer(t *testing.T, handle func([]byte) []byte) string {
	endpoint := adapterPeerAt(t, "127.0.0.1:44818", func(conn net.Conn) {
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
				cpf, err := eip.ParseRRData(frame.Data)
				if err != nil {
					t.Error(err)
					return
				}
				packet, err := eip.ParseEipCommonPacket(cpf)
				if err != nil || len(packet.Items) != 2 {
					t.Errorf("CPF %v", err)
					return
				}
				request := packet.Items[1].Data
				routed := len(request) > 1 && request[0] == 0x52
				if routed {
					start := 2 + int(request[1])*2
					if len(request) < start+4 {
						t.Error("short UCMM")
						return
					}
					size := int(binary.LittleEndian.Uint16(request[start+2:]))
					request = request[start+4 : start+4+size]
				}
				response := handle(request)
				if response == nil {
					return
				}
				if routed {
					response = append([]byte{0xd2, 0, 0, 0}, response...)
				}
				data := []byte{0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 0, 0, 0xb2, 0, 0, 0}
				binary.LittleEndian.PutUint16(data[14:16], uint16(len(response)))
				data = append(data, response...)
				reply = frame.Reply(0, data)
			default:
				t.Errorf("encap %x", frame.Command)
				return
			}
			if _, err = conn.Write(reply.Bytes()); err != nil {
				return
			}
		}
	})
	host, _, _ := net.SplitHostPort(endpoint)
	return host
}
func TestLogixAdapterReadWriteAndPartialError(t *testing.T) {
	var reads, writes atomic.Int32
	endpoint := eipPeer(t, func(req []byte) []byte {
		switch req[0] {
		case 0x5b, 0x54:
			return []byte{req[0] | 0x80, 0, 1, 0}
		case 0x55:
			return []byte{0xd5, 0, 1, 0}
		case 0x4c:
			if reads.Add(1) > 3 {
				return nil
			}
			return []byte{0xcc, 0, 0, 0, 0xc7, 0, 255, 255}
		case 0x4d:
			start := 2 + int(req[1])*2
			if !bytes.Equal(req[start:], []byte{0xc7, 0, 1, 0, 255, 255}) {
				t.Errorf("Logix write %x", req)
			}
			writes.Add(1)
			return []byte{0xcd, 0, 0, 0}
		default:
			t.Errorf("CIP %x", req)
			return nil
		}
	})
	a, _ := NewLogixAdapter(&PLCConfig{Address: endpoint, Family: FamilyMicro800, Timeout: time.Second})
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	values, err := a.Read([]TagRequest{{Name: "n"}})
	if err != nil || len(values) != 1 || values[0].Value != uint64(65535) {
		t.Fatalf("%v %v", values, err)
	}
	if err = a.Write("n", values[0].Value); err != nil {
		t.Fatal(err)
	}
	if writes.Load() != 1 {
		t.Fatal("missing write")
	}
	values, err = a.Read([]TagRequest{{Name: "n"}, {Name: "later"}})
	if !errors.Is(err, logix.ErrConnectionLost) || len(values) != 2 || values[0].Value != uint64(65535) || values[1].Error == nil || a.IsConnected() {
		t.Fatalf("%v %v", values, err)
	}
}
func TestPCCCAdapterReadWriteAndPartialError(t *testing.T) {
	var reads, writes atomic.Int32
	endpoint := eipPeer(t, func(req []byte) []byte {
		if req[0] != 0x4b {
			t.Errorf("PCCC %x", req)
			return nil
		}
		start := 2 + int(req[1])*2
		payload := req[start:]
		cmd := payload[7:]
		reply := append([]byte{0xcb, 0, 0, 0}, payload[:7]...)
		reply = append(reply, 0x4f, 0, cmd[2], cmd[3])
		switch cmd[4] {
		case 0xa2:
			if reads.Add(1) > 2 {
				return nil
			}
			return append(reply, 0, 128)
		case 0xaa:
			if !bytes.Equal(cmd[len(cmd)-2:], []byte{0, 128}) {
				t.Errorf("PCCC write %x", cmd)
			}
			writes.Add(1)
			return reply
		default:
			t.Errorf("PCCC function %x", cmd)
			return nil
		}
	})
	a, _ := NewPCCCAdapter(&PLCConfig{Address: endpoint, Family: FamilySLC500, Timeout: time.Second})
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	values, err := a.Read([]TagRequest{{Name: "N7:0"}})
	if err != nil || len(values) != 1 || values[0].Value != int64(-32768) {
		t.Fatalf("%v %v", values, err)
	}
	if err = a.Write("N7:0", values[0].Value); err != nil {
		t.Fatal(err)
	}
	if writes.Load() != 1 {
		t.Fatal("missing write")
	}
	values, err = a.Read([]TagRequest{{Name: "N7:0"}, {Name: "N7:10"}, {Name: "invalid"}})
	if !errors.Is(err, pccc.ErrConnectionLost) || len(values) != 3 || values[0].Value != int64(-32768) || values[1].Error == nil || values[2].Error == nil || a.IsConnected() {
		t.Fatalf("%v %v", values, err)
	}
}

func tpktRead(conn net.Conn) ([]byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	size := int(binary.BigEndian.Uint16(header[2:]))
	if size < 4 || size > 4096 {
		return nil, errors.New("bad TPKT")
	}
	data := make([]byte, size-4)
	_, err := io.ReadFull(conn, data)
	return data, err
}
func tpktReply(conn net.Conn, data []byte) {
	header := []byte{3, 0, 0, 0}
	binary.BigEndian.PutUint16(header[2:], uint16(4+len(data)))
	conn.Write(append(header, data...))
}
func TestS7AdapterReadWriteAndPartialError(t *testing.T) {
	var reads, writes atomic.Int32
	endpoint := adapterPeer(t, func(conn net.Conn) {
		for {
			req, err := tpktRead(conn)
			if err != nil {
				return
			}
			if req[1] == 0xe0 {
				tpktReply(conn, []byte{6, 0xd0, 0, 1, 0, 1, 0})
				continue
			}
			s := req[3:]
			params := s[10 : 10+int(binary.BigEndian.Uint16(s[6:]))]
			header := []byte{0x32, 3, 0, 0, s[4], s[5], 0, 2, 0, 0, 0, 0}
			var data []byte
			switch params[0] {
			case 0xf0:
				header[7] = 8
				data = []byte{0xf0, 0, 0, 1, 0, 1, 1, 0xe0}
			case 4:
				if reads.Add(1) > 2 {
					return
				}
				data = []byte{4, params[1]}
				for i := 0; i < int(params[1]); i++ {
					data = append(data, 255, 4, 0, 16, 128, 0)
				}
				binary.BigEndian.PutUint16(header[8:], uint16(6*int(params[1])))
			case 5:
				tail := s[10+len(params):]
				if !bytes.Equal(tail[len(tail)-2:], []byte{128, 0}) {
					t.Errorf("S7 write %x", s)
				}
				writes.Add(1)
				data = []byte{5, 1, 255}
				header[9] = 1
			default:
				t.Errorf("S7 function %x", params)
				return
			}
			tpktReply(conn, append([]byte{2, 0xf0, 128}, append(header, data...)...))
		}
	})
	a, _ := NewS7Adapter(&PLCConfig{Address: endpoint, Family: FamilyS7, Timeout: time.Second, Tags: []TagSelection{{Name: "DB1.0", DataType: "INT"}}})
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	values, err := a.Read([]TagRequest{{Name: "DB1.0", TypeHint: "INT"}})
	if err != nil || len(values) != 1 || values[0].Value != int64(-32768) {
		t.Fatalf("%v %v", values, err)
	}
	if err = a.Write("DB1.0", values[0].Value); err != nil {
		t.Fatal(err)
	}
	if writes.Load() != 1 {
		t.Fatal("missing write")
	}
	requests := make([]TagRequest, 20)
	for i := range requests {
		requests[i] = TagRequest{Name: "DB1." + strconv.Itoa(2*i), TypeHint: "INT"}
	}
	values, err = a.Read(requests)
	if !errors.Is(err, s7.ErrConnectionLost) || len(values) != 20 || values[0].Value != int64(-32768) || values[19].Error == nil || a.IsConnected() {
		t.Fatalf("%v %v", values, err)
	}
}
func finsRead(conn net.Conn) ([]byte, error) {
	header := make([]byte, 16)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	size := int(binary.BigEndian.Uint32(header[4:]))
	if size < 8 || size > 4096 {
		return nil, errors.New("bad FINS")
	}
	body := make([]byte, size-8)
	_, err := io.ReadFull(conn, body)
	return append(header, body...), err
}
func finsReply(conn net.Conn, command uint32, body []byte) {
	out := make([]byte, 16)
	copy(out, "FINS")
	binary.BigEndian.PutUint32(out[4:], uint32(8+len(body)))
	binary.BigEndian.PutUint32(out[8:], command)
	conn.Write(append(out, body...))
}
func TestOmronAdapterReadWriteAndPartialError(t *testing.T) {
	var reads, writes atomic.Int32
	endpoint := adapterPeer(t, func(conn net.Conn) {
		for {
			req, err := finsRead(conn)
			if err != nil {
				return
			}
			if binary.BigEndian.Uint32(req[8:]) == 0 {
				finsReply(conn, 1, []byte{0, 0, 0, 1, 0, 0, 0, 2})
				continue
			}
			frame := req[16:]
			body := append([]byte(nil), frame[:12]...)
			body[0] = 0xc0
			body = append(body, 0, 0)
			switch binary.BigEndian.Uint16(frame[10:]) {
			case 0x0101:
				if reads.Add(1) > 2 {
					return
				}
				count := int(binary.BigEndian.Uint16(frame[16:]))
				for range count {
					body = append(body, 255, 255)
				}
			case 0x0102:
				if !bytes.Equal(frame[18:], []byte{255, 255}) {
					t.Errorf("FINS write %x", frame)
				}
				writes.Add(1)
			default:
				t.Errorf("FINS command %x", frame)
				return
			}
			finsReply(conn, 2, body)
		}
	})
	host, port, _ := net.SplitHostPort(endpoint)
	number, _ := strconv.Atoi(port)
	a, _ := NewOmronAdapter(&PLCConfig{Address: host, FinsPort: number, Family: FamilyOmron, Protocol: "fins", Timeout: time.Second})
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	values, err := a.Read([]TagRequest{{Name: "DM0"}})
	if err != nil || len(values) != 1 || values[0].Value != uint64(65535) {
		t.Fatalf("%v %v", values, err)
	}
	if err = a.Write("DM0", values[0].Value); err != nil {
		t.Fatal(err)
	}
	if writes.Load() != 1 {
		t.Fatal("missing write")
	}
	values, err = a.Read([]TagRequest{{Name: "DM0"}, {Name: "DM10"}})
	if !errors.Is(err, omron.ErrConnectionLost) || len(values) != 2 || values[0].Value != uint64(65535) || values[1].Error == nil || a.IsConnected() {
		t.Fatalf("%v %v", values, err)
	}
	if !reflect.DeepEqual(values[0].Value, values[0].StableValue) {
		t.Fatal("stable value differs")
	}
}
