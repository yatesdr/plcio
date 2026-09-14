package driver

import (
	"bytes"
	"sync/atomic"
	"testing"
	"time"
)

func TestLogixRoutedCanonicalArrayWrite(t *testing.T) {
	var writes atomic.Int32
	host := eipPeer(t, func(req []byte) []byte {
		switch req[0] {
		case 0x5b, 0x54:
			return []byte{req[0] | 0x80, 0, 1, 0}
		case 0x55:
			return []byte{0xd5, 0, 1, 0}
		case 0x4c:
			return []byte{0xcc, 0, 0, 0, 0xc7, 0, 0, 0, 255, 255}
		case 0x0a:
			return []byte{0x8a, 0, 0, 0, 1, 0, 4, 0, 0xcc, 0, 0, 0, 0xc7, 0, 0, 0, 255, 255}
		case 0x4d:
			start := 2 + int(req[1])*2
			if !bytes.Equal(req[start:], []byte{0xc7, 0, 2, 0, 0, 0, 255, 255}) {
				t.Errorf("routed write %x", req)
			}
			writes.Add(1)
			return []byte{0xcd, 0, 0, 0}
		default:
			t.Errorf("CIP %x", req)
			return nil
		}
	})
	a, _ := NewLogixAdapter(&PLCConfig{Address: host, Family: FamilyLogix, ConnectionPath: "1,0", Timeout: time.Second})
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	values, err := a.Read([]TagRequest{{Name: "numbers"}})
	if err != nil || len(values) != 1 || values[0].Error != nil {
		t.Fatalf("%v %v", values, err)
	}
	if err = a.Write("numbers", values[0].Value); err != nil {
		t.Fatal(err)
	}
	if writes.Load() != 1 {
		t.Fatal("missing routed array write")
	}
}
