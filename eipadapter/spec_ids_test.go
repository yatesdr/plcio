package eipadapter

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/yatesdr/plcio/cip"
	"github.com/yatesdr/plcio/eip"
)

// Connection ID ownership (CIP Vol 1 3-5.4.1; OpENer
// ConnectionObjectGeneralConfiguration): point-to-point T->O IDs come from
// the originator, multicast T->O IDs from the target; the O->T proposal is
// kept when free.
func TestForwardOpenConnectionIDOwnership(t *testing.T) {
	const multicastTO = 0x2000 // network connection parameter: type 1 (multicast)
	for _, tt := range []struct {
		name      string
		toType    uint32
		toID      uint32
		wantEchoT bool
	}{
		{"p2p T->O keeps originator ID", 0x4000, 0xFEED0001, true},
		{"p2p T->O zero proposal allocated", 0x4000, 0, false},
		{"multicast T->O allocated by target", multicastTO, 0xFEED0002, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in, out := ioAssemblies()
			a := newUnitAdapter(t, nil, in, out)
			sink := udpSink(t)
			s := newFO(1, 0xAB01, 10, 6)
			s.toParams = tt.toType | 6
			s.toID = tt.toID
			r := a.openFO(s, loopbackOrigin(uint16(sink.LocalAddr().(*net.UDPAddr).Port)))
			if r.Status != cip.StatusSuccess {
				t.Fatalf("FO status 0x%02X ext 0x%04X", r.Status, extOf(r))
			}
			fo, err := cip.ParseForwardOpenResponse(r.Data)
			if err != nil {
				t.Fatal(err)
			}
			if fo.OTConnectionID != 0xAB01 {
				t.Errorf("O->T ID 0x%08X, want proposal 0xAB01", fo.OTConnectionID)
			}
			if got := fo.TOConnectionID == tt.toID; got != tt.wantEchoT || fo.TOConnectionID == 0 {
				t.Errorf("T->O ID 0x%08X (proposal 0x%08X), want echo=%v", fo.TOConnectionID, tt.toID, tt.wantEchoT)
			}

			// Produced packets carry the T->O ID from the reply.
			_ = sink.SetReadDeadline(time.Now().Add(2 * time.Second))
			buf := make([]byte, 1500)
			n, _, err := sink.ReadFromUDP(buf)
			if err != nil {
				t.Fatal(err)
			}
			pkt, err := eip.ParseEipCommonPacket(buf[:n])
			if err != nil || pkt.Items[0].TypeId != 0x8002 {
				t.Fatalf("producer packet: %v %x", err, buf[:n])
			}
			if got := binary.LittleEndian.Uint32(pkt.Items[0].Data); got != fo.TOConnectionID {
				t.Errorf("producer conn ID 0x%08X, want reply T->O 0x%08X", got, fo.TOConnectionID)
			}
		})
	}
}

// Two originators may pick the same point-to-point T->O ID; both
// connections must be accepted and each produces with its own ID.
func TestSameP2PTOIDFromTwoOriginators(t *testing.T) {
	in := NewAssembly(101, AssemblyInput, 4)
	a := newUnitAdapter(t, nil, in)
	for i, serial := range []uint16{1, 2} {
		s := newFO(serial, 0x300+uint32(i), 2, 6)
		s.path = []byte{0x20, 0x04, 0x24, 0x80, 0x2C, 0x65}
		s.toID = 0xCAFE0001
		r := a.openFO(s, loopbackOrigin(2222))
		if r.Status != cip.StatusSuccess {
			t.Fatalf("FO %d: 0x%02X ext 0x%04X", serial, r.Status, extOf(r))
		}
		fo, _ := cip.ParseForwardOpenResponse(r.Data)
		if fo.TOConnectionID != 0xCAFE0001 {
			t.Fatalf("FO %d: T->O 0x%08X", serial, fo.TOConnectionID)
		}
	}
	// Closing one leaves the other intact.
	a.connMgr.handleForwardClose(forwardCloseRequest(1)[6:], loopbackOrigin(2222))
	if a.connMgr.lookupByOT(0x301) == nil {
		t.Fatal("closing one connection removed the other")
	}
}

func TestForwardCloseRejections(t *testing.T) {
	in, out := ioAssemblies()
	a := newUnitAdapter(t, nil, in, out)
	if r := a.openFO(newFO(7, 0x700, 10, 6), loopbackOrigin(2222)); r.Status != cip.StatusSuccess {
		t.Fatalf("FO: 0x%02X ext 0x%04X", r.Status, extOf(r))
	}
	fc := func(serial, vendor uint16, orig uint32) []byte {
		b := []byte{0x0A, 0x0E}
		b = binary.LittleEndian.AppendUint16(b, serial)
		b = binary.LittleEndian.AppendUint16(b, vendor)
		b = binary.LittleEndian.AppendUint32(b, orig)
		return append(b, 0x02, 0x00, 0x20, 0x04, 0x24, 0x80)
	}
	for _, tt := range []struct {
		name   string
		req    []byte
		origin *peerAddr
		status byte
		ext    uint16
	}{
		{"unknown serial", fc(8, 0x1234, 0x5678ABCD), loopbackOrigin(2222), cip.StatusConnectionFailure, cip.ExtTargetConnNotFound},
		{"other vendor", fc(7, 0x4321, 0x5678ABCD), loopbackOrigin(2222), cip.StatusConnectionFailure, cip.ExtTargetConnNotFound},
		{"other originator serial", fc(7, 0x1234, 0x11111111), loopbackOrigin(2222), cip.StatusConnectionFailure, cip.ExtTargetConnNotFound},
		{"same triad, other IP", fc(7, 0x1234, 0x5678ABCD), &peerAddr{ip: [4]byte{10, 0, 0, 9}, port: 2222}, statusPrivilegeViolation, 0},
	} {
		r := a.connMgr.handleForwardClose(tt.req, tt.origin)
		if r.Status != tt.status || extOf(r) != tt.ext {
			t.Errorf("%s: status 0x%02X ext 0x%04X, want 0x%02X/0x%04X", tt.name, r.Status, extOf(r), tt.status, tt.ext)
		}
		// Unsuccessful Forward_Close body echoes the request triad.
		if !bytes.Equal(r.Data[:8], tt.req[2:10]) || len(r.Data) != 10 {
			t.Errorf("%s: body %x", tt.name, r.Data)
		}
		if a.connMgr.lookupByOT(0x700) == nil {
			t.Fatalf("%s: connection closed by a rejected Forward_Close", tt.name)
		}
	}
	if r := a.connMgr.handleForwardClose(fc(7, 0x1234, 0x5678ABCD), loopbackOrigin(2222)); r.Status != cip.StatusSuccess {
		t.Fatalf("owner Forward_Close: 0x%02X", r.Status)
	}
	if a.connMgr.lookupByOT(0x700) != nil {
		t.Fatal("connection still open after Forward_Close")
	}
	// Closing it again: not found.
	if r := a.connMgr.handleForwardClose(fc(7, 0x1234, 0x5678ABCD), loopbackOrigin(2222)); r.Status != cip.StatusConnectionFailure || extOf(r) != cip.ExtTargetConnNotFound {
		t.Fatalf("second Forward_Close: 0x%02X ext 0x%04X", r.Status, extOf(r))
	}
}

// The not-found status reaches the wire as general status 0x01 with one
// extended status word 0x0107, followed by the Forward_Close error body.
func TestForwardCloseNotFoundOnWire(t *testing.T) {
	a := startTestAdapter(t, nil)
	c := dialRaw(t, a.Adapter)
	c.register()
	resp := c.rr(forwardCloseRequest(99))
	want := []byte{cmForwardClose | 0x80, 0, cip.StatusConnectionFailure, 1, 0x07, 0x01, 99, 0, 0x34, 0x12, 0xCD, 0xAB, 0x78, 0x56, 0, 0}
	if !bytes.Equal(resp, want) {
		t.Fatalf("Forward_Close reply %x, want %x", resp, want)
	}
}

func TestTCPIPStringEncoding(t *testing.T) {
	for _, tt := range []struct {
		in   string
		max  int
		want []byte
	}{
		{"", 48, []byte{0, 0}},
		{"ab", 64, []byte{2, 0, 'a', 'b'}},
		{"abc", 64, []byte{3, 0, 'a', 'b', 'c', 0}},
		{"abcdef", 5, []byte{5, 0, 'a', 'b', 'c', 'd', 'e', 0}},
	} {
		if got := cipString(tt.in, tt.max); !bytes.Equal(got, tt.want) {
			t.Errorf("cipString(%q, %d) = %x, want %x", tt.in, tt.max, got, tt.want)
		}
	}
	got := ifaceConfig(net.IPv4(192, 168, 1, 10), net.CIDRMask(24, 32))
	want := []byte{
		10, 1, 168, 192, // IP as UDINT 0xC0A8010A
		0, 255, 255, 255, // mask 0xFFFFFF00
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, // gateway, name servers
		0, 0, // domain name: empty STRING
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("ifaceConfig = %x, want %x", got, want)
	}
}
