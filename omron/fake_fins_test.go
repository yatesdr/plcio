package omron

// Reusable in-process fakes for FINS/TCP, FINS/UDP and EtherNet/IP used by the
// omron tests. They listen on 127.0.0.1 only; no real device is ever contacted.

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/yatesdr/plcio/eip"
)

// finsReq is a decoded FINS command received by a fake.
type finsReq struct {
	Header  FINSHeader
	Command uint16
	Data    []byte
}

func parseFINSReq(b []byte) (finsReq, bool) {
	if len(b) < 12 {
		return finsReq{}, false
	}
	h, _ := ParseFINSHeader(b[:10])
	return finsReq{Header: *h, Command: binary.BigEndian.Uint16(b[10:12]), Data: append([]byte(nil), b[12:]...)}, true
}

// finsResponseFrame builds a FINS response to req the way a PLC does: response
// ICF, source/destination swapped, SID and command echoed.
func finsResponseFrame(req finsReq, endCode uint16, data []byte) []byte {
	h := req.Header
	out := []byte{0xC0, 0, 2, h.SNA, h.SA1, h.SA2, h.DNA, h.DA1, h.DA2, h.SID}
	out = binary.BigEndian.AppendUint16(out, req.Command)
	out = binary.BigEndian.AppendUint16(out, endCode)
	return append(out, data...)
}

// finsTCPFrame wraps payload in a FINS/TCP header.
func finsTCPFrame(cmd, errCode uint32, payload []byte) []byte {
	out := []byte("FINS")
	out = binary.BigEndian.AppendUint32(out, uint32(8+len(payload)))
	out = binary.BigEndian.AppendUint32(out, cmd)
	out = binary.BigEndian.AppendUint32(out, errCode)
	return append(out, payload...)
}

// fakeFINS models PLC memory (word areas, big-endian words) and answers
// 0x0101/0x0102/0x0104/0x0501.
type fakeFINS struct {
	mu      sync.Mutex
	mem     map[byte]map[uint16]uint16
	reqs    []finsReq
	flags   uint16 // OR-ed into every end code (status flag tests)
	tcpHook func(req finsReq) []byte
	udpHook func(req finsReq) [][]byte
}

func newFakeFINS() *fakeFINS {
	return &fakeFINS{mem: map[byte]map[uint16]uint16{}}
}

func (f *fakeFINS) set(area byte, addr uint16, words ...uint16) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mem[area] == nil {
		f.mem[area] = map[uint16]uint16{}
	}
	for i, w := range words {
		f.mem[area][addr+uint16(i)] = w
	}
}

// setTCPHook / setUDPHook install a hook that may override the reply to a
// request (return nil to use the memory model).
func (f *fakeFINS) setTCPHook(h func(req finsReq) []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tcpHook = h
}

func (f *fakeFINS) setUDPHook(h func(req finsReq) [][]byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.udpHook = h
}

func (f *fakeFINS) hooks() (func(req finsReq) []byte, func(req finsReq) [][]byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tcpHook, f.udpHook
}

func (f *fakeFINS) get(area byte, addr uint16) uint16 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mem[area][addr]
}

func (f *fakeFINS) requests() []finsReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]finsReq(nil), f.reqs...)
}

func (f *fakeFINS) commands(cmd uint16) []finsReq {
	var out []finsReq
	for _, r := range f.requests() {
		if r.Command == cmd {
			out = append(out, r)
		}
	}
	return out
}

func wordAreaOfBitArea(area byte) byte {
	switch area {
	case AreaCIOBit:
		return AreaCIOWord
	case AreaWRBit:
		return AreaWRWord
	case AreaHRBit:
		return AreaHRWord
	case AreaARBit:
		return AreaARWord
	case AreaDMBit:
		return AreaDMWord
	}
	return area
}

// handle executes a FINS command against the memory model.
func (f *fakeFINS) handle(req finsReq) (uint16, []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	word := func(area byte, addr uint16) uint16 { return f.mem[area][addr] }
	put := func(area byte, addr uint16, v uint16) {
		if f.mem[area] == nil {
			f.mem[area] = map[uint16]uint16{}
		}
		f.mem[area][addr] = v
	}
	d := req.Data
	switch req.Command {
	case FINSCmdMemoryRead:
		if len(d) != 6 {
			return 0x1002, nil
		}
		area, addr, bit, count := d[0], binary.BigEndian.Uint16(d[1:3]), d[3], binary.BigEndian.Uint16(d[4:6])
		if count > FINSMaxWordsPerRead {
			return 0x1101, nil // over the per-command limit
		}
		var out []byte
		for i := 0; i < int(count); i++ {
			if IsBitArea(area) {
				pos := int(bit) + i
				w := word(wordAreaOfBitArea(area), addr+uint16(pos/16))
				out = append(out, byte(w>>(pos%16)&1))
			} else {
				out = binary.BigEndian.AppendUint16(out, word(area, addr+uint16(i)))
			}
		}
		return f.flags, out
	case FINSCmdMemoryWrite:
		if len(d) < 6 {
			return 0x1002, nil
		}
		area, addr, bit, count := d[0], binary.BigEndian.Uint16(d[1:3]), d[3], binary.BigEndian.Uint16(d[4:6])
		payload := d[6:]
		for i := 0; i < int(count); i++ {
			if IsBitArea(area) {
				pos := int(bit) + i
				wa, wAddr := wordAreaOfBitArea(area), addr+uint16(pos/16)
				w := word(wa, wAddr) &^ (1 << (pos % 16))
				if payload[i] != 0 {
					w |= 1 << (pos % 16)
				}
				put(wa, wAddr, w)
			} else {
				put(area, addr+uint16(i), binary.BigEndian.Uint16(payload[i*2:]))
			}
		}
		return f.flags, nil
	case FINSCmdMultiMemoryRead:
		if len(d) == 0 || len(d)%4 != 0 {
			return 0x1003, nil
		}
		var out []byte
		for i := 0; i < len(d); i += 4 {
			area, addr := d[i], binary.BigEndian.Uint16(d[i+1:i+3])
			if area != AreaDMWord && area != AreaCIOWord && area != AreaHRWord && area != AreaWRWord && area != AreaARWord {
				return 0x1101, nil // area classification missing
			}
			out = append(out, area)
			out = binary.BigEndian.AppendUint16(out, word(area, addr))
		}
		return f.flags, out
	case FINSCmdCPURead:
		return f.flags, append([]byte("CJ2M-CPU31          "), make([]byte, 20)...)
	}
	return 0x0401, nil
}

// startFINSTCP serves FINS/TCP on 127.0.0.1 and returns the port.
func startFINSTCP(t *testing.T, f *fakeFINS) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var conns []net.Conn
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				f.serveTCP(conn)
			}()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return ln.Addr().(*net.TCPAddr).Port
}

func (f *fakeFINS) serveTCP(conn net.Conn) {
	for {
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		hdr := make([]byte, 16)
		if _, err := io.ReadFull(conn, hdr); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(hdr[4:8])
		if string(hdr[:4]) != "FINS" || n < 8 || n > 4096 {
			return
		}
		body := make([]byte, n-8)
		if _, err := io.ReadFull(conn, body); err != nil {
			return
		}
		switch binary.BigEndian.Uint32(hdr[8:12]) {
		case cmdNodeAddressRequest:
			payload := binary.BigEndian.AppendUint32(nil, 10)   // client node assigned
			payload = binary.BigEndian.AppendUint32(payload, 2) // server node
			conn.Write(finsTCPFrame(cmdNodeAddressResponse, 0, payload))
		case cmdFINSFrameSend:
			req, ok := parseFINSReq(body)
			if !ok {
				return
			}
			if hook, _ := f.hooks(); hook != nil {
				if raw := hook(req); raw != nil {
					if _, err := conn.Write(raw); err != nil {
						return
					}
					continue
				}
			}
			code, data := f.handle(req)
			if _, err := conn.Write(finsTCPFrame(cmdFINSFrameSend, 0, finsResponseFrame(req, code, data))); err != nil {
				return
			}
		default:
			return
		}
	}
}

// startFINSUDP serves FINS/UDP on 127.0.0.1 and returns the port.
func startFINSUDP(t *testing.T, f *fakeFINS) int {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			req, ok := parseFINSReq(buf[:n])
			if !ok {
				continue
			}
			if _, hook := f.hooks(); hook != nil {
				if out := hook(req); out != nil {
					for _, d := range out {
						conn.WriteToUDP(d, from)
					}
					continue
				}
			}
			code, data := f.handle(req)
			conn.WriteToUDP(finsResponseFrame(req, code, data), from)
		}
	}()
	t.Cleanup(func() {
		conn.Close()
		<-done
	})
	return conn.LocalAddr().(*net.UDPAddr).Port
}

// connectFINSTCPTest connects a Client to a fake FINS/TCP server.
func connectFINSTCPTest(t *testing.T, f *fakeFINS) *Client {
	t.Helper()
	port := startFINSTCP(t, f)
	c, err := Connect("127.0.0.1", WithTransport(TransportFINSTCP), WithPort(port), WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// startEIP serves a minimal EtherNet/IP encapsulation endpoint on 127.0.0.1.
// handle receives the raw CIP request (unconnected or connected) and returns
// the CIP reply; returning nil drops the connection.
func startEIP(t *testing.T, handle func(req []byte) []byte) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var conns []net.Conn
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				serveEIP(conn, handle)
			}()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return ln.Addr().(*net.TCPAddr).Port
}

func serveEIP(conn net.Conn, handle func(req []byte) []byte) {
	for {
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		frame, err := eip.ReadFrame(conn)
		if err != nil {
			return
		}
		var reply *eip.Frame
		switch frame.Command {
		case 0x65: // RegisterSession
			reply = frame.Reply(0, []byte{1, 0, 0, 0})
			reply.SessionHandle = 1234
		case 0x66: // UnRegisterSession
			return
		case 0x6f, 0x70: // SendRRData / SendUnitData
			cpf, err := eip.ParseRRData(frame.Data)
			if err != nil {
				return
			}
			packet, err := eip.ParseEipCommonPacket(cpf)
			if err != nil || len(packet.Items) != 2 {
				return
			}
			request := packet.Items[1].Data
			var seq []byte
			if frame.Command == 0x70 {
				seq, request = request[:2], request[2:]
			}
			response := handle(request)
			if response == nil {
				return
			}
			data := []byte{0, 0, 0, 0, 0, 0, 2, 0}
			if frame.Command == 0x70 {
				data = append(data, 0xa1, 0, 4, 0, 1, 0, 0, 0, 0xb1, 0)
				data = binary.LittleEndian.AppendUint16(data, uint16(2+len(response)))
				data = append(data, seq...)
			} else {
				data = append(data, 0, 0, 0, 0, 0xb2, 0)
				data = binary.LittleEndian.AppendUint16(data, uint16(len(response)))
			}
			data = append(data, response...)
			reply = frame.Reply(0, data)
		default:
			return
		}
		if _, err := conn.Write(reply.Bytes()); err != nil {
			return
		}
	}
}

// connectEIPTest connects a Client to a fake EIP endpoint. Forward Open is
// refused so unconnected messaging is used.
func connectEIPTest(t *testing.T, handle func(req []byte) []byte) *Client {
	t.Helper()
	port := startEIP(t, func(req []byte) []byte {
		if len(req) > 0 && (req[0] == 0x54 || req[0] == 0x5b) {
			return []byte{req[0] | 0x80, 0, 0x01, 0} // Forward Open refused
		}
		return handle(req)
	})
	c, err := Connect("127.0.0.1", WithTransport(TransportEIP), WithPort(port), WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
