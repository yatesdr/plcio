package logix

import (
	"encoding/binary"
	"reflect"
	"testing"
)

func TestSerializedTemplateBOOLPositions(t *testing.T) {
	// Member INFO is the published bit index, including gaps and positions
	// across bytes. Host storage is a DINT and names intentionally reorder bits.
	definition := []byte{
		0, 0, 0xc4, 0, 0, 0, 0, 0,
		31, 0, 0xc1, 0, 0, 0, 0, 0,
		3, 0, 0xc1, 0, 0, 0, 0, 0,
		15, 0, 0xc1, 0, 0, 0, 0, 0,
		8, 0, 0xc1, 0, 0, 0, 0, 0,
	}
	definition = append(definition, []byte("Flags;n\x00__host\x00High\x00Enabled\x00Middle\x00Off\x00")...)
	tmpl := &Template{ID: 1, Size: 4, RawHandle: 0x1234, MemberMap: make(map[string]int)}
	if err := tmpl.parseDefinition(definition, 5); err != nil {
		t.Fatal(err)
	}
	c := &Client{plc: &PLC{}, templates: map[uint16]*Template{1: tmpl}}
	want := map[string]any{"High": true, "Enabled": true, "Middle": true, "Off": false}
	got, err := c.DecodeUDT(0x8001, []byte{0x34, 0x12, 8, 128, 0, 128})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("published positions: %#v %v", got, err)
	}
	// A nested template has no handle prefix and must use the same positions.
	outerWire := []byte{0, 0, 1, 128, 0, 0, 0, 0}
	outerWire = append(outerWire, []byte("Outer;n\x00Flags\x00")...)
	outer := &Template{ID: 2, Size: 4, RawHandle: 0x4321, MemberMap: make(map[string]int)}
	if err := outer.parseDefinition(outerWire, 1); err != nil {
		t.Fatal(err)
	}
	c.templates[2] = outer
	got, err = c.DecodeUDT(0x8002, []byte{0x21, 0x43, 8, 128, 0, 128})
	if err != nil || !reflect.DeepEqual(got, map[string]any{"Flags": want}) {
		t.Fatalf("nested published positions: %#v %v", got, err)
	}
	for _, bit := range []uint16{32, 256, 65535} {
		invalid := append([]byte(nil), definition...)
		binary.LittleEndian.PutUint16(invalid[8:10], bit)
		if err := new(Template).parseDefinition(invalid, 5); err == nil {
			t.Fatalf("invalid BOOL bit %d accepted", bit)
		}
	}
	// Array INFO retains its length; it must never become a scalar bit index.
	arrayWire := []byte{40, 0, 0xc1, 0x20, 0, 0, 0, 0}
	arrayWire = append(arrayWire, []byte("Array;n\x00Flags\x00")...)
	array := &Template{MemberMap: make(map[string]int)}
	if err := array.parseDefinition(arrayWire, 1); err != nil || !reflect.DeepEqual(array.Members[0].ArrayDims, []int{40}) {
		t.Fatalf("BOOL array INFO changed: %+v %v", array.Members, err)
	}
}

func TestNativeTemplateAndPackedBOOLFixture(t *testing.T) {
	root := &Template{ID: 1, Name: "FixtureUDT", Size: 16, RawHandle: 0x1234,
		Members: []TemplateMember{
			{Name: "off", Type: TypeBOOL, Offset: 0},
			{Name: "on", Type: TypeBOOL, Offset: 0, BitOffset: 1},
			{Name: "hidden", Type: TypeDINT, Offset: 0, Hidden: true},
			{Name: "ints", Type: TypeINT, Offset: 4, ArrayDims: []int{2}},
			{Name: "nested", Type: 0x8002, Offset: 8},
		}}
	if root.Members[0].BitOffset != 0 || root.Members[1].BitOffset != 1 {
		t.Fatal("template BOOL positions")
	}
	nested := &Template{ID: 2, Name: "FixtureAOI", Size: 8, Members: []TemplateMember{
		{Name: "number", Type: TypeUINT, Offset: 0},
		{Name: "real", Type: TypeREAL, Offset: 4},
	}}
	c := &Client{plc: &PLC{}, templates: map[uint16]*Template{1: root, 2: nested}}
	// Independent little-endian capture-shaped bytes: handle, packed flags,
	// padding, signed INT boundaries, nested UINT and REAL 1.5.
	data := []byte{0x34, 0x12, 2, 0, 0, 0, 0, 128, 255, 127, 255, 255, 0, 0, 0, 0, 192, 63}
	got, err := c.DecodeUDT(0x8001, data)
	want := map[string]any{"off": false, "on": true, "ints": []any{int64(-32768), int64(32767)},
		"nested": map[string]any{"number": uint64(65535), "real": float64(1.5)}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("template fixture %#v: %v", got, err)
	}
	for _, tc := range []struct {
		bit  uint8
		want bool
	}{{8, true}, {9, false}, {31, true}} {
		got, err := c.decodeMemberValue(&TemplateMember{Type: TypeBOOL, BitOffset: tc.bit}, []byte{0, 1, 0, 128})
		if err != nil || got != tc.want {
			t.Fatalf("bit %d: %v %v", tc.bit, got, err)
		}
	}
	if _, err := c.decodeMemberValue(&TemplateMember{Type: TypeBOOL, BitOffset: 8}, []byte{1}); err == nil {
		t.Fatal("short packed BOOL accepted")
	}
}
