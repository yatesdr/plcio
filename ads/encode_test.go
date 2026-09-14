package ads

import (
	"bytes"
	"math"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestIndependentEncodingBytes(t *testing.T) {
	r := newResolver(capturedTypes(t), defaultOptions())
	for _, fixture := range []struct {
		typ   string
		value any
		bytes []byte
	}{
		{"UINT", uint64(42), []byte{42, 0}},
		{"ARRAY [1..2] OF UINT", []uint64{42, 43}, []byte{42, 0, 43, 0}},
		{"INT", int64(-32768), []byte{0, 0x80}},
		{"ULINT", uint64(math.MaxUint64), []byte{255, 255, 255, 255, 255, 255, 255, 255}},
		{"LINT", int64(math.MinInt64), []byte{0, 0, 0, 0, 0, 0, 0, 128}},
		{"TIME", uint64(math.MaxUint32), []byte{255, 255, 255, 255}},
		{"DT", uint64(1), []byte{1, 0, 0, 0}},
		{"LDT", int64(-1), []byte{255, 255, 255, 255, 255, 255, 255, 255}},
		{"STRING(4)", "café", []byte{'c', 'a', 'f', 0xe9, 0}},
		{"WSTRING(2)", "éA", []byte{0xe9, 0, 65, 0, 0, 0}},
		{"WSTRING(1)", "Ā", []byte{0, 1, 0, 0}},
		{"ARRAY [1..2] OF STRING(1)", []string{"é", "A"}, []byte{0xe9, 0, 65, 0}},
		{"ARRAY [1..2] OF WSTRING(1)", []string{"Ā", "A"}, []byte{0, 1, 0, 0, 65, 0, 0, 0}},
		{"ARRAY [-3..-3] OF INT", []int64{42}, []byte{42, 0}},
		{"PLC.EPlcPersistentStatus", uint64(255), []byte{255}},
	} {
		schema := r.resolveName(fixture.typ, 1)
		got, err := encodeValue(schema, fixture.value, defaultOptions())
		if err != nil || !bytes.Equal(got, fixture.bytes) {
			t.Fatalf("%s (%T): %x want %x error %v", fixture.typ, fixture.value, got, fixture.bytes, err)
		}
		read, err := decodeValue(schema, fixture.bytes, defaultOptions())
		if err != nil {
			t.Fatal(err)
		}
		back, err := encodeValue(schema, read, defaultOptions())
		if err != nil || !bytes.Equal(back, fixture.bytes) {
			t.Fatalf("read/write %s: %x %v", fixture.typ, back, err)
		}
	}
}

func TestCompleteCapturedRecordWrites(t *testing.T) {
	r := newResolver(capturedTypes(t), defaultOptions())
	for _, fixture := range []struct {
		typ, name string
		value     any
	}{
		{"TEST_STRUCT", "MAIN.test_struct", map[string]any{"my_byte": uint64(15), "my_dint": int64(25), "my_sint": int64(5), "my_string": "Test structure string", "my_dint_array": []int64{1, 2, 3, 4, 5, 6, 7, 8}}},
		{"PACKED_STRUCT", "MAIN.test_bitpacked_struct", map[string]any{"my_bit1": false, "my_bit2": true, "my_bit3": false, "my_bit4": true}},
	} {
		expected, err := os.ReadFile("testdata/beckhoff/" + fixture.name + ".bin")
		if err != nil {
			t.Fatal(err)
		}
		got, err := encodeValue(r.resolveName(fixture.typ, 1), fixture.value, defaultOptions())
		if err != nil || !bytes.Equal(got, expected) {
			t.Fatalf("%s: %x expected %x error %v", fixture.name, got, expected, err)
		}
	}
}

func TestInvalidEncoding(t *testing.T) {
	r := newResolver(capturedTypes(t), defaultOptions())
	for _, fixture := range []struct {
		typ   string
		value any
	}{
		{"UINT", int64(-1)}, {"UINT", uint64(65536)}, {"SINT", int64(128)}, {"INT", int64(-32769)},
		{"LINT", uint64(1 << 63)}, {"ULINT", float64(18446744073709551616)},
		{"DINT", 1.5}, {"DINT", math.NaN()}, {"BOOL", 2},
		{"REAL", math.MaxFloat64}, {"STRING(1)", "AA"}, {"STRING(4)", "Ā"},
		{"STRING(4)", "A\x00B"}, {"STRING(4)", string([]byte{0xff})}, {"WSTRING(2)", "😀"},
		{"WSTRING(1)", "AB"}, {"ARRAY [1..2] OF INT", []int64{1}}, {"ARRAY [1..1] OF INT", int64(1)},
		{"ARRAY [1..2] OF STRING(1)", []string{"A", "AA"}},
		{"INT(-2..2)", int64(3)}, {"UINT(3..4)", uint64(2)},
		{"TOD", uint64(86400000)}, {"LTOD", uint64(86400000000000)},
		{"TEST_STRUCT", map[string]any{"my_byte": uint64(15)}},
		{"Unknown", 42}, {"INT", []byte{1}},
	} {
		if _, err := encodeValue(r.resolveName(fixture.typ, 1), fixture.value, defaultOptions()); err == nil {
			t.Fatalf("invalid %s %T %v accepted", fixture.typ, fixture.value, fixture.value)
		}
	}
}

func TestNegativeWritesSendNoValues(t *testing.T) {
	var writes atomic.Int32
	c := testClient(t, func(request []byte) []byte { writes.Add(1); return []byte{0, 0, 0, 0} })
	seedDINT(c, "MAIN.n", 42)
	for _, value := range []any{int64(math.MaxInt32) + 1, 1.25, []int64{1, 2}, []byte{1}, "wrong"} {
		if err := c.Write("MAIN.n", value); err == nil {
			t.Fatalf("invalid write accepted: %v", value)
		}
	}
	c.symbols["MAIN.n"].Info.Flags = 32
	if err := c.Write("MAIN.n", int64(25)); err == nil {
		t.Fatal("read-only accepted")
	}
	if writes.Load() != 0 {
		t.Fatalf("negative validation sent requests: %d", writes.Load())
	}
}

func TestStringEncodingMetadataAndAlias(t *testing.T) {
	cfg := defaultOptions()
	r := newResolver(map[string]*typeEntry{"WideAlias": {name: "WideAlias", typeName: "WSTRING(2)", size: 6, version: 1, flags: 1}, "TODAlias": {name: "TODAlias", typeName: "TOD", size: 4, version: 1, flags: 1}}, cfg)
	schema := r.resolveName("WideAlias", 1)
	got, err := encodeValue(schema, "éA", cfg)
	if err != nil || !bytes.Equal(got, []byte{0xe9, 0, 65, 0, 0, 0}) {
		t.Fatalf("wide alias: %x %v", got, err)
	}
	read, err := decodeValue(schema, got, cfg)
	if err != nil || read != "éA" {
		t.Fatalf("alias decode: %v %v", read, err)
	}
	if _, err := encodeValue(r.resolveName("TODAlias", 1), uint64(86400000), cfg); err == nil {
		t.Fatal("alias lost time-of-day bounds")
	}
	schema = r.resolveName("STRING(5)", 1)
	copy := *schema
	copy.utf8 = true
	got, err = encodeValue(&copy, "café", cfg)
	if err != nil || !bytes.Equal(got, []byte{'c', 'a', 'f', 0xc3, 0xa9, 0}) {
		t.Fatalf("UTF-8 string: %x %v", got, err)
	}
	read, err = decodeValue(&copy, got, cfg)
	if err != nil || !reflect.DeepEqual(read, "café") {
		t.Fatal(read, err)
	}
}
