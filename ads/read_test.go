package ads

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func capturedReadPeer(t *testing.T) (*Client, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	symbols, err := os.ReadFile("testdata/beckhoff/symbols.bin")
	if err != nil {
		t.Fatal(err)
	}
	types, err := os.ReadFile("testdata/beckhoff/datatypes.bin")
	if err != nil {
		t.Fatal(err)
	}
	tags, err := parseSymbolTable(symbols, 40, 100000)
	if err != nil {
		t.Fatal(err)
	}
	sizes := make(map[string]int)
	values := make(map[string][]byte)
	for _, tag := range tags {
		sizes[tag.Name] = int(tag.Size)
	}
	for _, name := range []string{"MAIN.test_struct", "MAIN.test_bitpacked_struct", "MAIN.test_2d_dint_array_style1", "MAIN.test_2d_dint_array_style2", "MAIN.test_ltime", "MAIN.test_struct.my_dint"} {
		value, err := os.ReadFile("testdata/beckhoff/" + name + ".bin")
		if err != nil {
			t.Fatal(err)
		}
		values[name] = value
		sizes[name] = len(value)
	}
	bitLookups := make(map[string][]byte)
	for i, value := range []byte{0, 1, 0, 1} {
		name := fmt.Sprintf("MAIN.test_bitpacked_struct.my_bit%d", i+1)
		lookup, err := os.ReadFile("testdata/beckhoff-bit-lookups-2026-09-14/" + name + ".f009.bin")
		if err != nil {
			t.Fatal(err)
		}
		bitLookups[name], values[name], sizes[name] = lookup, []byte{value}, 1
	}
	handles := make(map[uint32]string)
	var next uint32
	var uploads, reads atomic.Int32
	c := testClient(t, func(request []byte) []byte {
		cmd := binary.LittleEndian.Uint16(request[22:24])
		group := binary.LittleEndian.Uint32(request[38:42])
		offset := binary.LittleEndian.Uint32(request[42:46])
		switch group {
		case 0xf008:
			return testReadReply([]byte{1, 0, 0, 0})
		case 0xf00f:
			info := make([]byte, 24)
			binary.LittleEndian.PutUint32(info[:4], 40)
			binary.LittleEndian.PutUint32(info[4:8], 3440)
			binary.LittleEndian.PutUint32(info[8:12], 75)
			binary.LittleEndian.PutUint32(info[12:16], 13608)
			return testReadReply(info)
		case 0xf00b:
			uploads.Add(1)
			return testReadReply(symbols)
		case 0xf00e:
			uploads.Add(1)
			return testReadReply(types)
		case 0xf009:
			name := string(bytes.TrimSuffix(request[54:], []byte{0}))
			if name == "MAIN.test_struct.my_dint" {
				return testReadReply(testSymbol(name, "DINT"))
			}
			if lookup := bitLookups[name]; lookup != nil {
				return testReadReply(lookup)
			}
			return []byte{0x10, 7, 0, 0, 0, 0, 0, 0}
		case 0xf003:
			name := string(bytes.TrimSuffix(request[54:], []byte{0}))
			if _, ok := sizes[name]; !ok {
				return []byte{0x10, 7, 0, 0, 0, 0, 0, 0}
			}
			next++
			handles[next] = name
			value := make([]byte, 4)
			binary.LittleEndian.PutUint32(value, next)
			return testReadReply(value)
		case 0xf005:
			if cmd != 2 {
				t.Errorf("unexpected value command %d", cmd)
			}
			reads.Add(1)
			name := handles[offset]
			value := values[name]
			if value == nil {
				value = make([]byte, sizes[name])
			}
			return testReadReply(value)
		case 0xf080:
			reads.Add(1)
			count := int(offset)
			data := make([]byte, 4*count)
			for i := range count {
				handle := binary.LittleEndian.Uint32(request[54+12*i+4 : 54+12*i+8])
				name := handles[handle]
				value := values[name]
				if value == nil {
					value = make([]byte, sizes[name])
				}
				data = append(data, value...)
			}
			return testReadReply(data)
		case 0xf006:
			delete(handles, binary.LittleEndian.Uint32(request[50:54]))
			return []byte{0, 0, 0, 0}
		default:
			t.Errorf("unexpected group %x", group)
			return nil
		}
	})
	c.versionCapability, c.catalogUnavailable = 0, false
	return c, &uploads, &reads
}

func TestReadDecodedAutomaticallyPinsSchema(t *testing.T) {
	c, uploads, reads := capturedReadPeer(t)
	names := []string{"MAIN.test_struct", "MAIN.test_bitpacked_struct", "MAIN.test_2d_dint_array_style1", "MAIN.test_2d_dint_array_style2", "MAIN.test_ltime", "MAIN.test_struct.my_dint", "MAIN.not_published", "MAIN.test_struct"}
	values, err := c.ReadDecoded(names...)
	if err != nil || len(values) != len(names) {
		t.Fatalf("read: %v %v", values, err)
	}
	for i, value := range values {
		if value == nil || value.Raw == nil || value.Raw.Name != names[i] {
			t.Fatalf("slot %d: %+v", i, value)
		}
		if i != 6 && value.Raw.Error != nil {
			t.Fatalf("slot %d: %v", i, value.Raw.Error)
		}
	}
	record := map[string]any{"my_byte": uint64(15), "my_dint": int64(25), "my_sint": int64(5), "my_string": "Test structure string", "my_dint_array": []int64{1, 2, 3, 4, 5, 6, 7, 8}}
	if !reflect.DeepEqual(values[0].Value, record) || !reflect.DeepEqual(values[7].Value, record) || !reflect.DeepEqual(values[2].Value, []int64{1, 2, 3, 4, 5, 6}) || values[2].Raw.Count != 6 || values[4].Value != uint64(8649040500600700) {
		t.Fatal("ordinary decoded values differ from captures")
	}
	var device *AdsError
	if !errors.As(values[6].Raw.Error, &device) || device.Code != 0x710 {
		t.Fatal("bad tag ADS error lost")
	}
	if uploads.Load() != 2 || reads.Load() != 2 {
		t.Fatalf("uploads=%d value groups=%d", uploads.Load(), reads.Load())
	}
	raw, err := c.Read("MAIN.test_struct")
	if err != nil || raw[0].Error != nil {
		t.Fatal(raw, err)
	}
	if _, ok := raw[0].GoValue().([]int); !ok {
		t.Fatalf("raw fallback changed: %T", raw[0].GoValue())
	}
	if uploads.Load() != 2 {
		t.Fatal("unchanged generation reuploads metadata")
	}
}

func TestUnsupportedDecodedValueRetainsBytes(t *testing.T) {
	c, _, _ := capturedReadPeer(t)
	values, err := c.ReadDecoded("TwinCAT_SystemInfoVarList._AppInfo")
	if err != nil || values[0].Raw.Error == nil || values[0].Value != nil || len(values[0].Raw.Bytes) != 256 {
		t.Fatalf("unsupported value: %+v %v", values, err)
	}
	if !strings.Contains(values[0].Raw.Error.Error(), "unsupported") {
		t.Fatal(values[0].Raw.Error)
	}
}

func TestBatchesSplitByCountAndBytes(t *testing.T) {
	c, _, reads := capturedReadPeer(t)
	if _, err := c.AllTags(); err != nil {
		t.Fatal(err)
	}
	c.cfg.maxBatch = 2
	c.conn.maxPayload = 80
	names := []string{"MAIN.test_struct.my_dint", "MAIN.test_struct.my_dint", "MAIN.test_struct.my_dint", "MAIN.test_struct.my_dint", "MAIN.test_struct.my_dint"}
	values, err := c.ReadDecoded(names...)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range values {
		if value.Raw.Error != nil || value.Value != int64(25) {
			t.Fatalf("split read: %+v", value)
		}
	}
	if reads.Load() != 3 {
		t.Fatalf("count splitting groups=%d", reads.Load())
	}
	c.cfg.maxBatch = 500
	c.conn.maxPayload = 39
	reads.Store(0)
	values, err = c.ReadDecoded(names...)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range values {
		if value.Raw.Error != nil || value.Value != int64(25) {
			t.Fatalf("byte split read: %+v", value)
		}
	}
	if reads.Load() != 5 {
		t.Fatalf("byte splitting groups=%d", reads.Load())
	}
}
