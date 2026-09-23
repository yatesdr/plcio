package driver

import (
	"bytes"
	"encoding/binary"
	"sync"
	"testing"
	"time"
)

// logixSymbolEntry encodes one symbol list entry (attributes 1, 2 and 8).
func logixSymbolEntry(instance uint32, name string, typeCode uint16) []byte {
	out := binary.LittleEndian.AppendUint32(nil, instance)
	out = binary.LittleEndian.AppendUint16(out, uint16(len(name)))
	out = append(out, name...)
	out = binary.LittleEndian.AppendUint16(out, typeCode)
	return append(out, make([]byte, 12)...)
}

// A Micro800 has no backplane: its Forward Open carries only the Message
// Router path. A refused Forward Open still leaves unconnected messaging
// working, and writes resolve the tag type once per connection.
func TestLogixMicro800ForwardOpenPathAndCachedWrites(t *testing.T) {
	var mu sync.Mutex
	var opens, writes, rmws [][]byte
	pages := 0
	endpoint := eipPeer(t, func(req []byte) []byte {
		mu.Lock()
		defer mu.Unlock()
		switch req[0] {
		case 0x54, 0x5b:
			opens = append(opens, append([]byte(nil), req...))
			return []byte{req[0] | 0x80, 0, 0x01, 0x01, 0x15, 0x03} // Rejected
		case 0x55:
			pages++
			out := []byte{0xd5, 0, 0, 0}
			out = append(out, logixSymbolEntry(1, "test_string", 0x00da)...)
			out = append(out, logixSymbolEntry(2, "Speed", 0x00c4)...)
			return append(out, logixSymbolEntry(3, "Flags", 0x00c3)...)
		case 0x4d:
			writes = append(writes, append([]byte(nil), req...))
			return []byte{0xcd, 0, 0, 0}
		case 0x4e:
			rmws = append(rmws, append([]byte(nil), req...))
			return []byte{0xce, 0, 0, 0}
		}
		t.Errorf("unexpected service %x", req[0])
		return nil
	})
	a, _ := NewLogixAdapter(&PLCConfig{Family: FamilyMicro800, Address: endpoint, Timeout: time.Second})
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if mode := a.ConnectionMode(); mode != "Unconnected messaging" {
		t.Fatalf("mode %q", mode)
	}
	for i := 0; i < 3; i++ {
		if err := a.Write("Speed", int64(5)); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	if pages != 1 {
		t.Errorf("symbol table paged %d times for repeated writes", pages)
	}
	mu.Unlock()
	if err := a.Write("test_string", "hi"); err != nil {
		t.Fatal(err)
	}
	if err := a.Write("Flags.3", true); err != nil {
		t.Fatal(err)
	}
	if err := a.Write("Flags.3", int64(0)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(opens) != 2 {
		t.Fatalf("%d Forward Open attempts", len(opens))
	}
	for _, open := range opens {
		if !bytes.HasSuffix(open, []byte{0xa3, 0x02, 0x20, 0x02, 0x24, 0x01}) {
			t.Fatalf("Micro800 Forward Open path: % x", open)
		}
	}
	if pages != 3 { // Once per distinct root tag.
		t.Fatalf("symbol table paged %d times", pages)
	}
	if len(writes) != 4 || !bytes.HasSuffix(writes[3], []byte{0xda, 0, 1, 0, 2, 'h', 'i'}) {
		t.Fatalf("writes % x", writes)
	}
	if len(rmws) != 2 || !bytes.HasSuffix(rmws[0], []byte{2, 0, 8, 0, 0xff, 0xff}) ||
		!bytes.HasSuffix(rmws[1], []byte{2, 0, 0, 0, 0xf7, 0xff}) {
		t.Fatalf("bit writes % x", rmws)
	}
}
