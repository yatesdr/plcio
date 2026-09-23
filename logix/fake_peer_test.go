package logix

import (
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/yatesdr/plcio/eip"
)

// fakePeer is a minimal in-process EtherNet/IP target for tests. It answers
// RegisterSession/UnRegisterSession, SendRRData, and SendUnitData, unwraps
// Unconnected Send and connected sequence counts, and hands each bare CIP
// request to handle. Returning nil from handle drops the TCP link.
type fakePeer struct {
	t        *testing.T
	listener net.Listener
	handle   func(req []byte) []byte

	mu       sync.Mutex
	conns    []net.Conn
	requests [][]byte
	wg       sync.WaitGroup

	// mangle, when set, may rewrite the address item data and the
	// sequence-prefixed transport data of connected replies.
	mangle func(address, data []byte) ([]byte, []byte)
}

func newFakePeer(t *testing.T, handle func(req []byte) []byte) *fakePeer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &fakePeer{t: t, listener: listener, handle: handle}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			p.conns = append(p.conns, conn)
			p.mu.Unlock()
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				defer conn.Close()
				p.serve(conn)
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		p.mu.Lock()
		for _, conn := range p.conns {
			conn.Close()
		}
		p.mu.Unlock()
		done := make(chan struct{})
		go func() { p.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("fake peer did not stop")
		}
	})
	return p
}

// requestLog returns a copy of every bare CIP request received so far.
func (p *fakePeer) requestLog() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]byte(nil), p.requests...)
}

func (p *fakePeer) serve(conn net.Conn) {
	conn.SetDeadline(time.Now().Add(20 * time.Second))
	var toConnID []byte // T->O connection ID from the last Forward Open
	for {
		frame, err := eip.ReadFrame(conn)
		if err != nil {
			return
		}
		var reply *eip.Frame
		switch frame.Command {
		case 0x65:
			reply = frame.Reply(0, []byte{1, 0, 0, 0})
			reply.SessionHandle = 0x1234
		case 0x66:
			return
		case 0x6f, 0x70:
			body, err := eip.ParseRRData(frame.Data)
			if err != nil {
				p.t.Error(err)
				return
			}
			packet, err := eip.ParseEipCommonPacket(body)
			if err != nil || len(packet.Items) != 2 {
				p.t.Errorf("CPF %v", err)
				return
			}
			req := packet.Items[1].Data
			var seq []byte
			if frame.Command == 0x70 {
				seq, req = req[:2], req[2:]
			}
			// Unconnected Send (0x52) to the Connection Manager, as opposed
			// to Read Tag Fragmented (also 0x52) addressed to a symbol.
			routed := len(req) > 3 && req[0] == 0x52 && req[2] == 0x20 && req[3] == 0x06
			if routed {
				start := 2 + int(req[1])*2
				n := int(binary.LittleEndian.Uint16(req[start+2:]))
				req = req[start+4 : start+4+n]
			}
			if !routed && seq == nil && len(req) >= 16 && (req[0] == 0x54 || req[0] == 0x5b) {
				toConnID = append([]byte(nil), req[12:16]...)
			}
			p.mu.Lock()
			p.requests = append(p.requests, append([]byte(nil), req...))
			p.mu.Unlock()
			response := p.handle(req)
			if response == nil {
				return
			}
			if routed {
				response = append([]byte{0xd2, 0, 0, 0}, response...)
			}
			address, transport := uint16(0), uint16(0xb2)
			var addressData []byte
			if seq != nil {
				// Connected replies carry the T->O connection ID.
				address, transport = 0xa1, 0xb1
				addressData = append(make([]byte, 0, 4), toConnID...)
				if len(addressData) != 4 {
					addressData = make([]byte, 4)
				}
				response = append(append([]byte(nil), seq...), response...)
				p.mu.Lock()
				mangle := p.mangle
				p.mu.Unlock()
				if mangle != nil {
					addressData, response = mangle(addressData, response)
				}
			}
			cpf := &eip.EipCommonPacket{Items: []eip.EipCommonPacketItem{
				{TypeId: address, Length: uint16(len(addressData)), Data: addressData},
				{TypeId: transport, Length: uint16(len(response)), Data: response},
			}}
			reply = frame.Reply(0, eip.BuildRRData(cpf.Bytes()))
		default:
			p.t.Errorf("unexpected encapsulation command 0x%x", frame.Command)
			return
		}
		if _, err := conn.Write(reply.Bytes()); err != nil {
			return
		}
	}
}

// newClient dials the peer and optionally performs the Forward Open.
func (p *fakePeer) newClient(t *testing.T, open bool) *Client {
	t.Helper()
	host, port, _ := net.SplitHostPort(p.listener.Addr().String())
	n, _ := strconv.Atoi(port)
	transport := eip.NewEipClientWithPort(host, uint16(n))
	transport.SetTimeout(2 * time.Second)
	if err := transport.Connect(); err != nil {
		t.Fatal(err)
	}
	c := &Client{plc: &PLC{IpAddress: host, Connection: transport}}
	if open {
		if err := c.plc.OpenConnection(); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(c.Close)
	return c
}

// fakeForwardOpenReply answers a Forward Open (0x54/0x5B) successfully,
// echoing the connection triple as a real target does.
func fakeForwardOpenReply(req []byte) []byte {
	out := []byte{req[0] | 0x80, 0, 0, 0}
	out = binary.LittleEndian.AppendUint32(out, 0x11223344) // O->T ID chosen by target
	out = append(out, req[12:16]...)                        // T->O ID
	out = append(out, req[16:24]...)                        // serial, vendor, originator serial
	out = binary.LittleEndian.AppendUint32(out, 0x00201234)
	out = binary.LittleEndian.AppendUint32(out, 0x00204001)
	return append(out, 0, 0)
}

// fakeSymbol decodes the symbolic/element path of a Read/Write request.
func fakeSymbol(req []byte) (name string, rest []byte) {
	start := 2 + int(req[1])*2
	path := req[2:start]
	for len(path) > 0 {
		switch path[0] {
		case 0x91:
			n := int(path[1])
			if name != "" {
				name += "."
			}
			name += string(path[2 : 2+n])
			path = path[2+n+n%2:]
		case 0x28:
			name += fmt.Sprintf("[%d]", path[1])
			path = path[2:]
		case 0x29:
			name += fmt.Sprintf("[%d]", binary.LittleEndian.Uint16(path[2:]))
			path = path[4:]
		default:
			return fmt.Sprintf("?%x", path), req[start:]
		}
	}
	return name, req[start:]
}

// fakeMSP answers a Multiple Service Packet by delegating each embedded
// request to each and reporting 0x1E if any embedded service failed.
func fakeMSP(req []byte, each func([]byte) []byte) []byte {
	start := 2 + int(req[1])*2
	body := req[start:]
	n := int(binary.LittleEndian.Uint16(body))
	out := make([]byte, 2+2*n)
	binary.LittleEndian.PutUint16(out, uint16(n))
	failed := false
	for i := 0; i < n; i++ {
		begin := int(binary.LittleEndian.Uint16(body[2+2*i:]))
		end := len(body)
		if i+1 < n {
			end = int(binary.LittleEndian.Uint16(body[4+2*i:]))
		}
		binary.LittleEndian.PutUint16(out[2+2*i:], uint16(len(out)))
		reply := each(body[begin:end])
		failed = failed || reply[2] != 0
		out = append(out, reply...)
	}
	status := byte(0)
	if failed {
		status = 0x1e
	}
	return append([]byte{0x8a, 0, status, 0}, out...)
}

// fakeFragment serves one Read Tag (0x4C) or Read Tag Fragmented (0x52)
// reply for value data, at most max bytes per reply. Structure replies carry
// the 0x02A0 type and the handle in every fragment, as Logix controllers do.
func fakeFragment(req []byte, rest []byte, dataType uint16, handle uint16, data []byte, max int) []byte {
	offset := 0
	if req[0] == 0x52 {
		offset = int(binary.LittleEndian.Uint32(rest[2:]))
	}
	end, status := offset+max, byte(0x06)
	if end >= len(data) {
		end, status = len(data), 0
	}
	out := []byte{req[0] | 0x80, 0, status, 0}
	out = binary.LittleEndian.AppendUint16(out, dataType)
	if dataType == CIPStructType {
		out = binary.LittleEndian.AppendUint16(out, handle)
	}
	return append(out, data[offset:end]...)
}
