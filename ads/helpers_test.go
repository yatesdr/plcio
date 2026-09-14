package ads

import (
	"bytes"
	"math"
	"reflect"
	"testing"
)

func TestStatelessHelpersMatchNativeCodec(t *testing.T) {
	for _, test := range []struct {
		code  uint16
		count int
		value any
		data  []byte
	}{
		{TypeDateTime, 1, uint64(42), []byte{42, 0, 0, 0}},
		{TypeTime, 1, uint64(math.MaxUint32), []byte{255, 255, 255, 255}},
		{TypeLTime, 1, uint64(8649040500600700), []byte{0x7c, 0xaf, 0xaf, 0xaa, 0x41, 0xba, 0x1e, 0}},
		{TypeWord, 2, []uint64{42, 43}, []byte{42, 0, 43, 0}},
		{TypeInt16 | TypeArrayFlag, 1, []int64{-1}, []byte{255, 255}},
		{TypeString, 1, "café", []byte{'c', 'a', 'f', 0xe9, 0}},
		{TypeWString, 1, "éA", []byte{0xe9, 0, 0x41, 0, 0, 0}},
		{TypeWString, 2, []string{"Ā", "éA"}, []byte{0, 1, 0, 0, 0, 0, 0xe9, 0, 0x41, 0, 0, 0}},
	} {
		encoded, err := EncodeValueWithType(test.value, test.code)
		if err != nil || !bytes.Equal(encoded, test.data) {
			t.Fatalf("%s encode: %x want %x: %v", TypeName(test.code), encoded, test.data, err)
		}
		raw := &TagValue{DataType: test.code, Count: test.count, Bytes: test.data}
		if got := raw.GoValue(); !reflect.DeepEqual(got, test.value) {
			t.Fatalf("%s decode: %T %#v want %T %#v", TypeName(test.code), got, got, test.value, test.value)
		}
	}
	if TypeSize(TypeDateTime) != 4 {
		t.Fatal("DT storage width")
	}
	for _, input := range []any{int64(-1), uint64(65536), float64(1.5), math.Inf(1)} {
		if _, err := EncodeValueWithType(input, TypeWord); err == nil {
			t.Fatalf("invalid UINT input %v", input)
		}
	}
	for _, text := range []string{"🙂", "\x00", "\xff"} {
		if _, err := EncodeValueWithType(text, TypeWString); err == nil {
			t.Fatalf("invalid WSTRING %q", text)
		}
	}
	for _, data := range [][]byte{{1}, {1, 0, 2}} {
		if (&TagValue{DataType: TypeWord, Bytes: data}).GoValue() != nil {
			t.Fatal("malformed primitive tail ignored")
		}
	}
}
