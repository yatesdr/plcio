package driver

import (
	"bytes"
	"encoding/binary"
	"net"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestOmronEIPCanonicalArraysAndBOOLWrite(t *testing.T) {
	var writes atomic.Int32
	host := eipPeer(t, func(req []byte) []byte {
		start := 2 + int(req[1])*2
		switch req[0] {
		case 0x5b, 0x54:
			return []byte{req[0] | 0x80, 0, 1, 0}
		case 0x4c:
			if bytes.Contains(req[:start], []byte("bits")) {
				return []byte{0xcc, 0, 0, 0, 0xc1, 0, 1, 0, 1}
			}
			return []byte{0xcc, 0, 0, 0, 0xc7, 0, 0, 0, 255, 255}
		case 0x4d:
			expected := []byte{0xc7, 0, 2, 0, 0, 0, 255, 255}
			if bytes.Contains(req[:start], []byte("bits")) {
				expected = []byte{0xc1, 0, 3, 0, 1, 0, 1}
			}
			if !bytes.Equal(req[start:], expected) {
				t.Errorf("CIP array write %x want %x", req[start:], expected)
			}
			writes.Add(1)
			return []byte{0xcd, 0, 0, 0}
		default:
			t.Errorf("CIP %x", req)
			return nil
		}
	})
	a, _ := NewOmronAdapter(&PLCConfig{Address: host, Family: FamilyOmron, Protocol: "eip", Timeout: time.Second})
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, name := range []string{"numbers", "bits"} {
		values, err := a.Read([]TagRequest{{Name: name}})
		if err != nil || len(values) != 1 || values[0].Error != nil {
			t.Fatalf("%v %v", values, err)
		}
		var input any = []uint64{0, 65535}
		if name == "bits" {
			input = []bool{true, false, true}
		}
		if err = a.Write(name, input); err != nil {
			t.Fatal(err)
		}
	}
	if writes.Load() != 2 {
		t.Fatal("missing array writes")
	}
}

func TestOmronFINSBOOLArrayWrite(t *testing.T) {
	var writes atomic.Int32
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
			if binary.BigEndian.Uint16(frame[10:]) == 0x101 {
				if !bytes.Equal(frame[12:], []byte{2, 0, 100, 5, 0, 3}) {
					t.Errorf("FINS bit read %x", frame)
				}
				body := append([]byte(nil), frame[:12]...)
				body[0] = 0xc0
				finsReply(conn, 2, append(body, 0, 0, 1, 0, 1))
				continue
			}
			if binary.BigEndian.Uint16(frame[10:]) != 0x102 || !bytes.Equal(frame[12:], []byte{2, 0, 100, 5, 0, 3, 1, 0, 1}) {
				t.Errorf("FINS bits %x", frame)
			}
			writes.Add(1)
			body := append([]byte(nil), frame[:12]...)
			body[0] = 0xc0
			body = append(body, 0, 0)
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
	values, err := a.Read([]TagRequest{{Name: "DM100.5[3]"}})
	if err != nil || len(values) != 1 || !reflect.DeepEqual(values[0].Value, []bool{true, false, true}) {
		t.Fatalf("bit read %v %v", values, err)
	}
	if err := a.Write("DM100.5[3]", values[0].Value); err != nil {
		t.Fatal(err)
	}
	if writes.Load() != 1 {
		t.Fatal("missing bit write")
	}
}
