package ads

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/yatesdr/plcio/metadata"
)

func TestCapturedPackedMemberWriteUsesRequestedHandle(t *testing.T) {
	name := "MAIN.test_bitpacked_struct.my_bit2"
	lookup, err := os.ReadFile("testdata/beckhoff-bit-lookups-2026-09-14/" + name + ".f009.bin")
	if err != nil {
		t.Fatal(err)
	}
	symbols, err := os.ReadFile("testdata/beckhoff/symbols.bin")
	if err != nil {
		t.Fatal(err)
	}
	_, records, err := parseSymbolRecords(symbols, 40, defaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	entries := capturedTypes(t)
	var writes atomic.Int32
	c := testClient(t, func(req []byte) []byte {
		switch binary.LittleEndian.Uint32(req[38:42]) {
		case 0xf009:
			return testReadReply(lookup)
		case 0xf003:
			if !bytes.Equal(req[54:], append([]byte(name), 0)) {
				t.Errorf("handle acquired for parent instead of requested member: %q", req[54:])
			}
			return testReadReply([]byte{42, 0, 0, 0})
		case 0xf005:
			if binary.LittleEndian.Uint16(req[22:24]) != 3 || binary.LittleEndian.Uint32(req[42:46]) != 42 || !bytes.Equal(req[50:], []byte{1}) {
				t.Errorf("direct bit write wire %x", req)
			}
			writes.Add(1)
			return []byte{0, 0, 0, 0}
		case 0xf006:
			return []byte{0, 0, 0, 0}
		}
		t.Errorf("unexpected parent read or service %x", req)
		return nil
	})
	c.catalogUnavailable = false
	c.snapshot = &schemaSnapshot{entries: entries, resolver: newResolver(entries, c.cfg), symbols: records}
	if err := c.Write(name, true); err != nil || writes.Load() != 1 {
		t.Fatalf("direct member write: %v writes=%d", err, writes.Load())
	}
}

func TestCapturedPackedMemberLookups(t *testing.T) {
	c, _, _ := capturedReadPeer(t)
	before, err := c.AllTags()
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 4)
	for i := range names {
		names[i] = fmt.Sprintf("MAIN.test_bitpacked_struct.my_bit%d", i+1)
	}
	values, err := c.ReadDecoded(names...)
	if err != nil || len(values) != 4 {
		t.Fatalf("packed member read: %v %v", values, err)
	}
	for i, want := range []bool{false, true, false, true} {
		v := values[i]
		if v.Raw.Error != nil || v.Value != want || v.Raw.Name != names[i] || v.Raw.DataType != TypeBool || v.Raw.Count != 1 {
			t.Fatalf("member %s: %+v decoded=%v", names[i], v.Raw, v.Value)
		}
		desc, err := c.Describe(names[i])
		if err != nil || desc.Name != names[i] || desc.Type.Kind != metadata.KindBool || desc.Type.DeclaredName != "BIT" || desc.Type.Bits != 1 {
			t.Fatalf("member description %+v %v", desc, err)
		}
		info := c.lookupSymbols[names[i]].info
		if info.IndexGroup != 0x4041 || info.IndexOffset != 3077200+uint32(i) || info.Name != names[i] {
			t.Fatalf("validated lookup identity/address: %+v", info)
		}
	}
	after, err := c.AllTags()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("packed lookup altered catalog: %v", err)
	}
}

func TestPackedLookupRejectsUnverifiedAliases(t *testing.T) {
	original, err := os.ReadFile("testdata/beckhoff-bit-lookups-2026-09-14/MAIN.test_bitpacked_struct.my_bit1.f009.bin")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		request string
		change  func([]byte)
		missing bool
	}{
		{"address", "MAIN.test_bitpacked_struct.my_bit1", func(b []byte) { b[8]++ }, false},
		{"guid", "MAIN.test_bitpacked_struct.my_bit1", func(b []byte) { b[70]++ }, false},
		{"padding", "MAIN.test_bitpacked_struct.my_bit1", func(b []byte) { b[60] = 'x' }, false},
		{"type", "MAIN.test_bitpacked_struct.my_bit1", func(b []byte) { b[65] = 'X' }, false},
		{"reference", "MAIN.test_bitpacked_struct.my_bit1", func(b []byte) { b[20] |= 4 }, false},
		{"interface", "MAIN.test_bitpacked_struct.my_bit1", func(b []byte) { b[20] |= 16 }, false},
		{"unknown member", "MAIN.test_bitpacked_struct.unknown", nil, false},
		{"trailing separator", "MAIN.test_bitpacked_struct.my_bit1.", nil, false},
		{"not a member", "MAIN.test_bitpacked_struct.my_bit1.more", nil, false},
		{"missing parent metadata", "MAIN.test_bitpacked_struct.my_bit1", nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			lookup := append([]byte(nil), original...)
			if test.change != nil {
				test.change(lookup)
			}
			var handles, reads, writes atomic.Int32
			c := testClient(t, func(req []byte) []byte {
				switch binary.LittleEndian.Uint32(req[38:42]) {
				case 0xf009:
					return testReadReply(lookup)
				case 0xf003:
					handles.Add(1)
					return testReadReply([]byte{42, 0, 0, 0})
				case 0xf005:
					if binary.LittleEndian.Uint16(req[22:24]) == 3 {
						writes.Add(1)
						return []byte{0, 0, 0, 0}
					}
					reads.Add(1)
					return testReadReply([]byte{1})
				case 0xf006:
					return []byte{0, 0, 0, 0}
				}
				t.Errorf("unexpected service %x", req)
				return nil
			})
			if !test.missing {
				parent := TagInfo{Name: "MAIN.test_bitpacked_struct", TypeName: "PACKED_STRUCT", TypeCode: TypeUnknown, Size: 1, IndexGroup: 0x4040, IndexOffset: 384650}
				c.catalogUnavailable = false
				entries := capturedTypes(t)
				c.snapshot = &schemaSnapshot{entries: entries, resolver: newResolver(entries, c.cfg), symbols: map[string]*symbolRecord{parent.Name: {info: parent}}}
			}
			values, err := c.ReadDecoded(test.request)
			if err != nil || len(values) != 1 || values[0].Raw.Error == nil || values[0].Value != nil {
				t.Fatalf("invalid alias read: %+v %v", values, err)
			}
			if _, err := c.Describe(test.request); err == nil {
				t.Fatal("unverified alias described")
			}
			if err := c.Write(test.request, true); err == nil {
				t.Fatal("unverified alias written")
			}
			if handles.Load() != 0 || reads.Load() != 0 || writes.Load() != 0 || len(c.lookupSymbols) != 0 {
				t.Fatalf("unverified alias used/cached: handles=%d reads=%d writes=%d lookups=%d", handles.Load(), reads.Load(), writes.Load(), len(c.lookupSymbols))
			}
		})
	}
	// The exception belongs to validated direct lookups only, never uploads.
	if _, _, err := parseSymbolRecords(original, 1, defaultOptions()); err == nil {
		t.Fatal("catalog accepted interior NUL name padding")
	}
}
