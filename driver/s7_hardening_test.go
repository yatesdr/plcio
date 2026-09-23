package driver

import (
	"encoding/binary"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/yatesdr/plcio/s7"
)

// Reads of an address without a request hint use the configured DataType
// (same case-insensitive lookup as Write); an explicit hint still wins.
func TestS7AdapterReadUsesConfiguredType(t *testing.T) {
	p := newS7MultiPeer(t)
	p.mu.Lock()
	copy(p.db1[4:], []byte{0xFF, 0xF6, 0x00, 0x00})               // DB1.4 INT -10, then padding
	binary.BigEndian.PutUint32(p.db1[16:], math.Float32bits(1.5)) // DB1.DBD16 REAL
	binary.BigEndian.PutUint32(p.db1[8:], uint32(0xFFFFFF9C))     // DB1.8 TIME -100 ms
	p.mu.Unlock()
	a, _ := NewS7Adapter(&PLCConfig{Address: p.ln.Addr().String(), Family: FamilyS7, Timeout: time.Second,
		Tags: []TagSelection{{Name: "db1.4", DataType: "INT"}, {Name: "DB1.DBD16", DataType: "real"}, {Name: "DB1.8", DataType: "TIME"}}})
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	// One tag per Read: the test peer answers single-item requests only.
	reqs := []TagRequest{{Name: "DB1.4"}, {Name: "DB1.DBD16"}, {Name: "DB1.8"}, {Name: "DB1.4", TypeHint: "DINT"}}
	want := []any{int64(-10), 1.5, int64(-100), int64(int32(-655360))}
	vals := make([]*TagValue, len(reqs))
	for i, req := range reqs {
		got, err := a.Read([]TagRequest{req})
		if err != nil {
			t.Fatal(err)
		}
		v := got[0]
		vals[i] = v
		if v.Error != nil || v.Value != want[i] {
			t.Errorf("%s = %#v (%v), want %#v", v.Name, v.Value, v.Error, want[i])
		}
	}
	if vals[0].DataType != s7.TypeInt || vals[1].DataType != s7.TypeReal {
		t.Errorf("types %v %v", vals[0].DataType, vals[1].DataType)
	}

	// The configured type also drives writes to the sized address and TIME.
	if err := a.Write("DB1.DBD16", 2.25); err != nil {
		t.Fatal(err)
	}
	if err := a.Write("DB1.8", 250*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := a.Write("DB1.8", int64(-1)); err != nil {
		t.Fatal(err)
	}
	if err := a.Write("DB1.8", int64(math.MaxInt32)+1); err == nil {
		t.Fatal("out-of-range TIME accepted")
	}
	p.mu.Lock()
	gotReal := math.Float32frombits(binary.BigEndian.Uint32(p.db1[16:]))
	gotTime := int32(binary.BigEndian.Uint32(p.db1[8:]))
	p.mu.Unlock()
	if gotReal != 2.25 || gotTime != -1 {
		t.Fatalf("stored REAL %v TIME %d", gotReal, gotTime)
	}
}

func TestS7AdapterKeepalive(t *testing.T) {
	a, _ := NewS7Adapter(&PLCConfig{Address: "127.0.0.1:1", Family: FamilyS7})
	if err := a.Keepalive(); err == nil {
		t.Fatal("keepalive without a connection returned nil")
	}
	p := newS7MultiPeer(t)
	a, _ = NewS7Adapter(&PLCConfig{Address: p.ln.Addr().String(), Family: FamilyS7, Timeout: time.Second})
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.Keepalive(); err != nil {
		t.Fatal(err)
	}
	p.ln.Close()
	p.mu.Lock()
	for c := range p.conns {
		c.Close()
	}
	p.mu.Unlock()
	err := a.Keepalive()
	if !errors.Is(err, s7.ErrConnectionLost) || !a.IsConnectionError(err) || a.IsConnected() {
		t.Fatalf("keepalive on dropped link: %v", err)
	}
}

func TestS7AdapterRejectsInvalidSlot(t *testing.T) {
	a, _ := NewS7Adapter(&PLCConfig{Address: "127.0.0.1:1", Family: FamilyS7, Slot: 32, Timeout: time.Second})
	if err := a.Connect(); err == nil {
		t.Fatal("slot 32 accepted")
	}
}
