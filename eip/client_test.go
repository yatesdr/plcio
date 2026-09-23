package eip

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// newPipeClient returns a client wired to an in-memory fake peer (net.Pipe).
func newPipeClient(t *testing.T, session uint32, timeout time.Duration) (*EipClient, net.Conn) {
	t.Helper()
	cliConn, peer := net.Pipe()
	t.Cleanup(func() {
		_ = peer.Close()
		_ = cliConn.Close()
	})
	return &EipClient{ipAddr: "pipe", port: 44818, conn: cliConn, session: session, timeout: timeout}, peer
}

// readRequest consumes one encapsulated request from the fake peer side.
func readRequest(peer net.Conn) (*Frame, error) {
	return ReadFrame(peer)
}

func encapHeader(cmd, length uint16, session, status uint32) []byte {
	h := binary.LittleEndian.AppendUint16(nil, cmd)
	h = binary.LittleEndian.AppendUint16(h, length)
	h = binary.LittleEndian.AppendUint32(h, session)
	h = binary.LittleEndian.AppendUint32(h, status)
	h = append(h, make([]byte, 8)...)
	h = binary.LittleEndian.AppendUint32(h, 0)
	return h
}

func testCpf() EipCommonPacket {
	req := []byte{0x4C, 0x02, 0x91, 0x02, 'A', 'B', 0x01, 0x00}
	return EipCommonPacket{Items: []EipCommonPacketItem{
		{TypeId: CpfAddressNullId, Length: 0},
		{TypeId: CpfUnconnectedMessageId, Length: uint16(len(req)), Data: req},
	}}
}

// On a framing error (bad session handle / oversize length) the payload is
// left unread in the TCP stream. The connection must be torn down, exactly as
// on an I/O error, so the next transaction cannot parse stale payload bytes as
// a header, and so IsConnected() reports the loss to callers.
func TestRecvEncapFramingErrorClosesConn(t *testing.T) {
	cases := []struct {
		name    string
		length  uint16
		session uint32
	}{
		{"session mismatch", 10, 0x22222222},
		{"oversize length", 0xFFFF, 0x11111111},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, peer := newPipeClient(t, 0x11111111, 2*time.Second)
			go func() {
				if _, err := readRequest(peer); err != nil {
					return
				}
				// Header plus (part of) a payload. The write blocks until the
				// client reads it or closes the pipe.
				_, _ = peer.Write(append(encapHeader(SendRRData, tc.length, tc.session, 0), make([]byte, 10)...))
			}()

			_, err := c.SendRRData(testCpf())
			if err == nil {
				t.Fatal("expected error")
			}
			if c.IsConnected() {
				t.Fatalf("connection still marked up after framing error: %v", err)
			}
		})
	}
}

// A well-formed response still works and leaves the connection up.
func TestSendRRDataFakePeerOK(t *testing.T) {
	c, peer := newPipeClient(t, 0x11111111, 2*time.Second)
	cipResp := []byte{0xCC, 0x00, 0x00, 0x00, 0xC4, 0x00, 0x2A, 0x00, 0x00, 0x00}
	go func() {
		if _, err := readRequest(peer); err != nil {
			return
		}
		cpf := EipCommonPacket{Items: []EipCommonPacketItem{
			{TypeId: CpfAddressNullId, Length: 0},
			{TypeId: CpfUnconnectedMessageId, Length: uint16(len(cipResp)), Data: cipResp},
		}}
		payload := BuildRRData(cpf.Bytes())
		_, _ = peer.Write(append(encapHeader(SendRRData, uint16(len(payload)), 0x11111111, 0), payload...))
	}()
	cp, err := c.SendRRData(testCpf())
	if err != nil {
		t.Fatal(err)
	}
	if len(cp.Items) != 2 || string(cp.Items[1].Data) != string(cipResp) {
		t.Fatalf("unexpected response: %+v", cp)
	}
	if !c.IsConnected() {
		t.Fatal("connection dropped after good response")
	}
}

// ListIdentityTCP holds the client mutex; with no deadline an unresponsive
// peer blocked every other operation indefinitely. It must honour the
// client's configured timeout.
func TestListIdentityTCPTimeout(t *testing.T) {
	c, peer := newPipeClient(t, 0x11111111, 150*time.Millisecond)
	go func() {
		// Consume the request, then never answer.
		_, _ = readRequest(peer)
		_, _ = io.Copy(io.Discard, peer)
	}()

	done := make(chan error, 1)
	go func() {
		_, err := c.ListIdentityTCP()
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected timeout error")
		}
	case <-time.After(3 * time.Second):
		_ = peer.Close() // unblock the stuck call so the test can exit
		t.Fatal("ListIdentityTCP did not honour the client timeout")
	}
}

func TestListIdentityTCPFakePeerOK(t *testing.T) {
	c, peer := newPipeClient(t, 0x11111111, 2*time.Second)

	name := "1756-L83E/B"
	item := binary.LittleEndian.AppendUint16(nil, 1) // encap version
	sock := make([]byte, 16)
	binary.BigEndian.PutUint16(sock[0:2], 2)
	binary.BigEndian.PutUint16(sock[2:4], 44818)
	copy(sock[4:8], []byte{10, 0, 0, 5})
	item = append(item, sock...)
	item = binary.LittleEndian.AppendUint16(item, 1)      // vendor
	item = binary.LittleEndian.AppendUint16(item, 0x0E)   // device type
	item = binary.LittleEndian.AppendUint16(item, 0x00A6) // product code
	item = append(item, 33, 11)
	item = binary.LittleEndian.AppendUint16(item, 0x3060)
	item = binary.LittleEndian.AppendUint32(item, 0xC0FFEE01)
	item = append(item, byte(len(name)))
	item = append(item, name...)
	item = append(item, 0x03)
	payload := binary.LittleEndian.AppendUint16(nil, 1)
	payload = append(payload, cpfItem(CpfTypeListIdentityResponseId, uint16(len(item)), item)...)

	go func() {
		req, err := readRequest(peer)
		if err != nil || req.Command != 0x63 {
			return
		}
		_, _ = peer.Write(append(encapHeader(0x63, uint16(len(payload)), 0, 0), payload...))
	}()
	ids, err := c.ListIdentityTCP()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0].ProductName != name || ids[0].SerialNumber != 0xC0FFEE01 || ids[0].Port != 44818 {
		t.Fatalf("unexpected identities: %+v", ids)
	}
	if !c.IsConnected() {
		t.Fatal("connection dropped after good ListIdentity response")
	}
}
