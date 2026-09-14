package driver

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"
	"time"
)

// The peer supplies independent Template Object definitions and generic A002
// value descriptors. Handles deliberately differ from template instance IDs.
func TestLogixDriverStringTemplatesPreserveNativeStorage(t *testing.T) {
	text := make([]byte, 88)
	binary.LittleEndian.PutUint32(text, 6)
	copy(text[4:], "Sample")
	record := append(append(append([]byte(nil), text...), text...), text...)
	record = binary.LittleEndian.AppendUint32(record, 1234)
	definitions := map[byte][]byte{
		1: {0, 0, 2, 128, 0, 0, 0, 0, 2, 0, 2, 160, 88, 0, 0, 0, 0, 0, 196, 0, 8, 1, 0, 0},
		2: {0, 0, 196, 0, 0, 0, 0, 0, 82, 0, 194, 32, 4, 0, 0, 0},
	}
	definitions[1] = append(definitions[1], []byte("SyntheticRecord\x00First\x00Texts\x00Card\x00")...)
	definitions[2] = append(definitions[2], []byte("ASCIISTRING82\x00LEN\x00DATA\x00")...)
	endpoint := eipPeer(t, func(req []byte) []byte {
		start := 2 + int(req[1])*2
		template := len(req) > 5 && req[2] == 0x20 && req[3] == 0x6c
		if template {
			id := req[5]
			def := definitions[id]
			switch req[0] {
			case 3:
				size, members, handle := uint32(88), uint16(2), uint16(0xbeef)
				if id == 1 {
					size, members, handle = 268, 3, 0xdef0
				}
				out := []byte{0x83, 0, 0, 0, 5, 0}
				for _, attr := range []uint16{5, 4, 3, 2, 1} {
					out = binary.LittleEndian.AppendUint16(out, attr)
					out = append(out, 0, 0)
					switch attr {
					case 5:
						out = binary.LittleEndian.AppendUint32(out, size)
					case 4:
						out = binary.LittleEndian.AppendUint32(out, uint32((len(def)+23+3)/4))
					case 3:
						out = binary.LittleEndian.AppendUint16(out, uint16(size))
					case 2:
						out = binary.LittleEndian.AppendUint16(out, members)
					case 1:
						out = binary.LittleEndian.AppendUint16(out, handle)
					}
				}
				return out
			case 0x4c:
				return append([]byte{0xcc, 0, 0, 0}, def...)
			}
		}
		switch req[0] {
		case 0x5b, 0x54:
			return []byte{req[0] | 128, 0, 1, 0}
		case 0x4c:
			data, handle := record, uint16(0xdef0)
			if bytes.Contains(req[2:start], []byte("First")) {
				data, handle = text, 0xbeef
			}
			if bytes.Contains(req[2:start], []byte("Texts")) {
				data, handle = record[:176], 0xbeef
			}
			return append([]byte{0xcc, 0, 0, 0, 0xa0, 2, byte(handle), byte(handle >> 8)}, data...)
		default:
			t.Errorf("unexpected STRING peer service %x", req[0])
			return nil
		}
	})
	a, _ := NewLogixAdapter(&PLCConfig{Family: FamilyLogix, Address: endpoint, Timeout: time.Second})
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.SetTags([]TagInfo{{Name: "Record", TypeCode: 0x8001, Instance: 42}})
	for _, requests := range [][]TagRequest{{{Name: "Record.First"}}, {{Name: "Record.First"}, {Name: "Record.Texts"}, {Name: "Record"}}} {
		values, err := a.Read(requests)
		if err != nil || len(values) != len(requests) {
			t.Fatalf("driver results %d: %v", len(values), err)
		}
		for _, v := range values {
			if v.Error != nil {
				t.Fatal(v.Error)
			}
			if !reflect.DeepEqual(v.Value, v.StableValue) {
				t.Fatal("stable STRING value differs")
			}
			switch v.Name {
			case "Record.First":
				if v.Value != "Sample" || v.DataType != 0x8002 || !bytes.Equal(v.Bytes, append([]byte{0xef, 0xbe}, text...)) {
					t.Fatal("STRING value/type/raw storage mismatch")
				}
			case "Record.Texts":
				if !reflect.DeepEqual(v.Value, []string{"Sample", "Sample"}) || v.DataType != 0xa002 || v.Count != 2 || len(v.Bytes) != 178 {
					t.Fatalf("STRING array mismatch %T code %x count %d bytes %d", v.Value, v.DataType, v.Count, len(v.Bytes))
				}
			case "Record":
				m, ok := v.Value.(map[string]any)
				if !ok || m["First"] != "Sample" || m["Card"] != int64(1234) || !reflect.DeepEqual(m["Texts"], []string{"Sample", "Sample"}) || v.DataType != 0x8001 || len(v.Bytes) != 270 {
					t.Fatalf("record mismatch %T", v.Value)
				}
			}
		}
	}
}
