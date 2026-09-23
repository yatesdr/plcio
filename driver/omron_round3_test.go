package driver

import (
	"bytes"
	"encoding/binary"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOmronAdapterProtocolValidation(t *testing.T) {
	for _, tc := range []struct {
		protocol, want string
		discovery      bool
	}{
		{"", "fins", false},
		{"fins", "fins", false},
		{" FINS ", "fins", false},
		{"Fins-TCP", "fins-tcp", false},
		{"fins-udp\t", "fins-udp", false},
		{"eip", "eip", true},
		{"  EIP ", "eip", true},
	} {
		a, err := NewOmronAdapter(&PLCConfig{Family: FamilyOmron, Protocol: tc.protocol})
		if err != nil || a.protocol != tc.want || a.SupportsDiscovery() != tc.discovery {
			t.Errorf("%q: adapter=%+v err=%v", tc.protocol, a, err)
		}
	}
	for _, p := range []string{"tcp", "udp", "fins/tcp", "cip", "ethernet/ip", "modbus"} {
		if a, err := NewOmronAdapter(&PLCConfig{Family: FamilyOmron, Protocol: p}); err == nil || !strings.Contains(err.Error(), "unsupported protocol") {
			t.Errorf("%q accepted: %+v %v", p, a, err)
		}
	}
}

func TestOmronAdapterForcedFINSTransport(t *testing.T) {
	port := omronFixFINSServer(t) // TCP only
	a, err := NewOmronAdapter(&PLCConfig{Address: "127.0.0.1", FinsPort: port, Family: FamilyOmron, Protocol: "fins-tcp", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	if mode := a.ConnectionMode(); !strings.HasPrefix(mode, "FINS/TCP") {
		t.Fatalf("mode %q", mode)
	}
	a.Close()

	// fins-udp must not fall back to TCP: nothing answers UDP on that port.
	u, _ := NewOmronAdapter(&PLCConfig{Address: "127.0.0.1", FinsPort: port, Family: FamilyOmron, Protocol: "fins-udp", Timeout: 300 * time.Millisecond})
	if err := u.Connect(); err == nil {
		u.Close()
		t.Fatal("fins-udp connected without a UDP responder")
	}
}

// ULINT values above math.MaxInt64 reach the wire unchanged, and NJ TIME /
// DATE_AND_TIME values round-trip through the adapter.
func TestOmronEIPUnsignedAndTimeValues(t *testing.T) {
	dt := time.Date(2024, 3, 15, 13, 45, 30, 123456789, time.UTC)
	var mu sync.Mutex
	var writes [][]byte
	host := eipPeer(t, func(req []byte) []byte {
		start := 2 + int(req[1])*2
		path := req[:start]
		switch req[0] {
		case 0x5b, 0x54:
			return []byte{req[0] | 0x80, 0, 1, 0}
		case 0x4c:
			switch {
			case bytes.Contains(path, []byte("big")):
				return []byte{0xcc, 0, 0, 0, 0xc9, 0, 0, 0, 0, 0, 0, 0, 0, 0}
			case bytes.Contains(path, []byte("dur")):
				return append([]byte{0xcc, 0, 0, 0, 0xdb, 0}, binary.LittleEndian.AppendUint64(nil, uint64(1500*time.Millisecond))...)
			default:
				return append([]byte{0xcc, 0, 0, 0, 0x0a, 0}, binary.LittleEndian.AppendUint64(nil, uint64(dt.UnixNano()))...)
			}
		case 0x4d:
			mu.Lock()
			writes = append(writes, append([]byte(nil), req[start:]...))
			mu.Unlock()
			return []byte{0xcd, 0, 0, 0}
		}
		return []byte{req[0] | 0x80, 0, 8, 0}
	})
	a, _ := NewOmronAdapter(&PLCConfig{Address: host, Family: FamilyOmron, Protocol: "eip", Timeout: time.Second})
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	values, err := a.Read([]TagRequest{{Name: "dur"}, {Name: "stamp"}})
	if err != nil || len(values) != 2 || values[0].Value != int64(1500*time.Millisecond) {
		t.Fatalf("TIME read %v %v", values, err)
	}
	if got, ok := values[1].Value.(time.Time); !ok || !got.Equal(dt) || values[1].DataType != 0x010A {
		t.Fatalf("DATE_AND_TIME read %#v (type %#x)", values[1].Value, values[1].DataType)
	}

	if err := a.Write("big", []uint64{math.MaxUint64, 1}); err != nil {
		t.Fatal(err)
	}
	if err := a.Write("dur", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := a.Write("stamp", dt); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := [][]byte{
		append(append([]byte{0xc9, 0, 2, 0}, bytes.Repeat([]byte{0xff}, 8)...), 1, 0, 0, 0, 0, 0, 0, 0),
		append([]byte{0xdb, 0, 1, 0}, binary.LittleEndian.AppendUint64(nil, uint64(2*time.Second))...),
		append([]byte{0x0a, 0, 1, 0}, binary.LittleEndian.AppendUint64(nil, uint64(dt.UnixNano()))...),
	}
	if len(writes) != len(want) {
		t.Fatalf("writes %x", writes)
	}
	for i := range want {
		if !bytes.Equal(writes[i], want[i]) {
			t.Errorf("write %d = %x, want %x", i, writes[i], want[i])
		}
	}
}

// An unknown FINS type hint fails the write instead of writing a WORD.
func TestOmronFINSUnknownTypeHintRejected(t *testing.T) {
	port := omronFixFINSServer(t)
	a, _ := NewOmronAdapter(&PLCConfig{Address: "127.0.0.1", FinsPort: port, Family: FamilyOmron, Protocol: "fins-tcp", Timeout: time.Second,
		Tags: []TagSelection{{Name: "D100", DataType: "FLOAT32"}}})
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.Write("D100", int64(1)); err == nil || !strings.Contains(err.Error(), "unsupported FINS type") {
		t.Fatalf("write with unknown hint: %v", err)
	}
}
