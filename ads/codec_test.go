package ads

import (
	"encoding/binary"
	"os"
	"reflect"
	"testing"

	"github.com/yatesdr/plcio/metadata"
)

func TestCapturedDecodedValues(t *testing.T) {
	r := newResolver(capturedTypes(t), defaultOptions())
	expected := map[string]any{"my_byte": uint64(15), "my_dint": int64(25), "my_sint": int64(5), "my_string": "Test structure string", "my_dint_array": []int64{1, 2, 3, 4, 5, 6, 7, 8}}
	for _, fixture := range []struct {
		name, typ string
		value     any
	}{
		{"MAIN.test_struct", "TEST_STRUCT", expected},
		{"MAIN.test_bitpacked_struct", "PACKED_STRUCT", map[string]any{"my_bit1": false, "my_bit2": true, "my_bit3": false, "my_bit4": true}},
		{"MAIN.test_2d_dint_array_style1", "ARRAY [1..2] OF ARRAY [1..3] OF INT", []int64{1, 2, 3, 4, 5, 6}},
		{"MAIN.test_2d_dint_array_style2", "ARRAY [1..2,1..3] OF INT", []int64{1, 2, 3, 4, 5, 6}},
		{"MAIN.test_ltime", "LTIME", uint64(8649040500600700)},
	} {
		data, err := os.ReadFile("testdata/beckhoff/" + fixture.name + ".bin")
		if err != nil {
			t.Fatal(err)
		}
		value, err := decodeValue(r.resolveName(fixture.typ, 1), data, defaultOptions())
		if err != nil || !reflect.DeepEqual(value, fixture.value) {
			t.Fatalf("%s: %#v (%T), error %v", fixture.name, value, value, err)
		}
	}
}

func TestDecodeStringsAndSingleArrays(t *testing.T) {
	r := newResolver(nil, defaultOptions())
	for _, fixture := range []struct {
		typ   string
		bytes []byte
		value any
	}{
		{"STRING(4)", []byte{'c', 'a', 'f', 0xe9, 0}, "café"},
		{"WSTRING(2)", []byte{0xe9, 0, 65, 0, 0, 0}, "éA"},
		{"WSTRING(1)", []byte{0, 1, 0, 0}, "Ā"},
		{"STRING(0)", []byte{0}, ""},
		{"ARRAY [-3..-3] OF INT", []byte{42, 0}, []int64{42}},
		{"ARRAY [1..2] OF STRING(1)", []byte{0xe9, 0, 65, 0}, []string{"é", "A"}},
		{"ARRAY [1..2] OF WSTRING(1)", []byte{0, 1, 0, 0, 65, 0, 0, 0}, []string{"Ā", "A"}},
	} {
		value, err := decodeValue(r.resolveName(fixture.typ, 1), fixture.bytes, defaultOptions())
		if err != nil || !reflect.DeepEqual(value, fixture.value) {
			t.Fatalf("%s: %#v %v", fixture.typ, value, err)
		}
	}
	for _, fixture := range []struct {
		typ   string
		bytes []byte
	}{{"STRING(1)", []byte{'A', 'B'}}, {"WSTRING(1)", []byte{0, 0xd8, 0, 0}}, {"WSTRING(1)", []byte{0, 1, 1}}, {"INT", []byte{42, 0, 0}}} {
		if _, err := decodeValue(r.resolveName(fixture.typ, 1), fixture.bytes, defaultOptions()); err == nil {
			t.Fatalf("malformed %s decoded", fixture.typ)
		}
	}
}

func TestTimeReadSemantics(t *testing.T) {
	r := newResolver(nil, defaultOptions())
	timeValue, err := decodeValue(r.resolveName("TIME", 1), []byte{255, 255, 255, 255}, defaultOptions())
	if err != nil || timeValue != uint64(4294967295) {
		t.Fatalf("TIME %v %v", timeValue, err)
	}
	dt, err := decodeValue(r.resolveName("DT", 1), []byte{1, 0, 0, 0}, defaultOptions())
	if err != nil || dt != uint64(1) {
		t.Fatalf("DT %v %v", dt, err)
	}
	for _, name := range []string{"LDATE", "LDT"} {
		for _, value := range []int64{-1, 0, 1} {
			bytes := make([]byte, 8)
			binary.LittleEndian.PutUint64(bytes, uint64(value))
			got, err := decodeValue(r.resolveName(name, 1), bytes, defaultOptions())
			if err != nil || got != value {
				t.Fatalf("%s %v %v", name, got, err)
			}
		}
	}
	for _, name := range []string{"TOD", "LTOD"} {
		schema := r.resolveName(name, 1)
		bytes := make([]byte, schema.size)
		if schema.size == 4 {
			binary.LittleEndian.PutUint32(bytes, uint32(schema.timeOfDayLimit))
		} else {
			binary.LittleEndian.PutUint64(bytes, schema.timeOfDayLimit)
		}
		if _, err := decodeValue(schema, bytes, defaultOptions()); err == nil {
			t.Fatalf("invalid %s accepted", name)
		}
	}
}

func TestNestedRecordArrayAndExpansion(t *testing.T) {
	cfg := defaultOptions()
	entries := map[string]*typeEntry{"R": {name: "R", size: 2, version: 1, flags: 1, members: []*typeEntry{{name: "n", typeName: "INT", size: 2, version: 1, flags: 2}}}}
	r := newResolver(entries, cfg)
	schema := r.resolveName("ARRAY [-1..0,2..3] OF R", 1)
	got, err := decodeValue(schema, []byte{1, 0, 2, 0, 3, 0, 4, 0}, cfg)
	want := []any{map[string]any{"n": int64(1)}, map[string]any{"n": int64(2)}, map[string]any{"n": int64(3)}, map[string]any{"n": int64(4)}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("record array: %#v %v", got, err)
	}
	cfg.maxElements = 8
	if _, err := decodeValue(schema, []byte{1, 0, 2, 0, 3, 0, 4, 0}, cfg); err == nil {
		t.Fatal("record array expansion not bounded")
	}
	cfg.maxElements = 9
	if _, err := decodeValue(schema, []byte{1, 0, 2, 0, 3, 0, 4, 0}, cfg); err != nil {
		t.Fatal(err)
	}
	if schema.kind != metadata.KindStruct {
		t.Fatal(schema.kind)
	}
}

func BenchmarkCapturedRecordDecode(b *testing.B) {
	r := newResolver(capturedTypes(b), defaultOptions())
	schema := r.resolveName("TEST_STRUCT", 1)
	data, err := os.ReadFile("testdata/beckhoff/MAIN.test_struct.bin")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := decodeValue(schema, data, defaultOptions()); err != nil {
			b.Fatal(err)
		}
	}
}
