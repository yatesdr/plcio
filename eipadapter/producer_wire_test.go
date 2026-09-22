package eipadapter

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/yatesdr/plcio/eip"
)

func TestProducerRawCPF(t *testing.T) {
	want := []byte{2, 0, 2, 128, 8, 0, 4, 3, 2, 1, 8, 7, 6, 5, 177, 0, 4, 0, 10, 9, 170, 187}
	got := buildProducerPacket(0x01020304, 0x05060708, 0x090a, []byte{0xaa, 0xbb})
	if !bytes.Equal(got, want) {
		t.Fatalf("wire packet: %x; want %x", got, want)
	}
}

func TestTimeoutDoesNotOverflow(t *testing.T) {
	if got := connectionTimeout(10_000_000, 7); got != 5120*time.Second {
		t.Fatal(got)
	}
}

func TestConsumerFormatsAndValidation(t *testing.T) {
	for _, tt := range []struct {
		name                         string
		data                         []byte
		wrapped, wrongPeer, accepted bool
	}{
		{"raw", []byte{1, 2, 3, 4}, false, false, true},
		{"run idle", []byte{1, 0, 0, 0, 1, 2, 3, 4}, false, false, true},
		{"legacy wrapped", []byte{1, 2, 3, 4}, true, false, true},
		{"short", []byte{1, 2, 3}, false, false, false},
		{"long", []byte{1, 2, 3, 4, 5}, false, false, false},
		{"wrong peer", []byte{1, 2, 3, 4}, false, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Now()
			a := &Adapter{cfg: Config{Now: func() time.Time { return now }}}
			a.connMgr = NewConnectionManager(a)
			assembly := NewAssembly(102, AssemblyOutput, 4)
			c := &Connection{OTConnID: 42, Consume: assembly, peerAddr: peerAddr{ip: [4]byte{127, 0, 0, 1}, port: 2222}}
			a.connMgr.byOT[c.OTConnID] = c
			packet := buildO2TPacket(42, 1, tt.data)
			if tt.wrapped {
				packet = (&eip.Frame{Command: eip.SendUnitData, Data: eip.BuildRRData(packet)}).Bytes()
			}
			peer := net.IPv4(127, 0, 0, 1)
			if tt.wrongPeer {
				peer = net.IPv4(127, 0, 0, 2)
			}
			a.handleIOPacket(packet, &net.UDPAddr{IP: peer, Port: 12345})
			if tt.accepted {
				if !bytes.Equal(assembly.Bytes(), []byte{1, 2, 3, 4}) || !c.lastInboundAt.Equal(now) {
					t.Fatalf("packet not accepted: %x", assembly.Bytes())
				}
			} else if !bytes.Equal(assembly.Bytes(), make([]byte, 4)) || !c.lastInboundAt.IsZero() {
				t.Fatal("invalid packet changed data or watchdog")
			}
			if c.peerAddr.port != 2222 {
				t.Fatal("source port replaced negotiated destination")
			}
		})
	}
}

func TestReceiveWatchdogUsesOTInterval(t *testing.T) {
	for _, initial := range []bool{false, true} {
		name := "after traffic"
		if initial {
			name = "no initial traffic"
		}
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			a := &Adapter{cfg: Config{Now: func() time.Time { return now }}, stopCh: make(chan struct{})}
			a.connMgr = NewConnectionManager(a)
			c := &Connection{OTConnID: 1, TOConnID: 2, OTRPI: 5000, TORPI: 100000, createdAt: now.Add(-time.Second), Produce: NewAssembly(101, AssemblyInput, 4)}
			if !initial {
				c.lastInboundAt = now.Add(-30 * time.Millisecond)
			}
			a.connMgr.byOT[1] = c
			a.connMgr.byTO[2] = c
			done := make(chan struct{})
			go func() { defer close(done); a.runProducer(c) }()
			defer close(a.stopCh)
			select {
			case <-done:
			case <-time.After(250 * time.Millisecond):
				t.Fatal("O->T receive watchdog did not expire")
			}
			if a.connMgr.lookupByOT(1) != nil {
				t.Fatal("expired connection retained")
			}
		})
	}
}
