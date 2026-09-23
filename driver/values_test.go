package driver

import (
	"bytes"
	"encoding/binary"
	"math"
	"reflect"
	"testing"

	"github.com/yatesdr/plcio/logix"
	"github.com/yatesdr/plcio/omron"
	"github.com/yatesdr/plcio/pccc"
	"github.com/yatesdr/plcio/s7"
)

func TestCanonicalDecodedValues(t *testing.T) {
	for _, tc := range []struct{ in, want any }{
		{int8(-128), int64(-128)}, {int16(-32768), int64(-32768)},
		{int32(math.MinInt32), int64(math.MinInt32)}, {uint8(255), uint64(255)},
		{uint16(65535), uint64(65535)}, {uint32(math.MaxUint32), uint64(math.MaxUint32)},
		{uint64(math.MaxUint64), uint64(math.MaxUint64)}, {float32(1.5), float64(1.5)},
		{[]int16{-32768, 32767}, []int64{-32768, 32767}},
		{[]uint16{0, 65535}, []uint64{0, 65535}}, {[]float32{1.5}, []float64{1.5}},
		{[]any{int16(-1)}, []int64{-1}}, {[]any{uint16(65535)}, []uint64{65535}},
		{[]any{true, false}, []bool{true, false}}, {[]any{"a"}, []string{"a"}},
		{map[string]any{"n": int16(-1), "array": []any{uint16(65535)}, "raw": []byte{255}}, map[string]any{"n": int64(-1), "array": []uint64{65535}, "raw": []byte{255}}},
		{[]any{map[string]any{"n": int8(-1)}}, []any{map[string]any{"n": int64(-1)}}},
		{[]byte{0, 255}, []byte{0, 255}}, {[]int{0, 255}, []int{0, 255}},
	} {
		if got := normalizeDecoded(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("normalize %T: %T %v, want %T %v", tc.in, got, got, tc.want, tc.want)
		}
	}
	if got := normalizePrimitive([]byte{0, 255}); !reflect.DeepEqual(got, []uint64{0, 255}) {
		t.Fatal(got)
	}
}

// These fixtures come from native protocol decoders; expected storage bytes
// are literals, so a wrapping or float conversion cannot verify itself.
func TestCanonicalNativeEncoderRoundTrips(t *testing.T) {
	for _, tc := range []struct {
		address string
		data    []byte
	}{
		{"N7:0", []byte{0, 128}}, {"N7:0", []byte{255, 127}},
		{"L10:0", []byte{0, 0, 0, 128}}, {"F8:0", []byte{0, 0, 192, 63}},
	} {
		addr, err := pccc.ParseAddress(tc.address)
		if err != nil {
			t.Fatal(err)
		}
		value := normalizeDecoded(pccc.DecodeValue(addr, tc.data))
		input, err := pcccCanonical(addr, value)
		if err != nil {
			t.Fatal(err)
		}
		var got []byte
		switch n := input.(type) {
		case int16:
			got = binary.LittleEndian.AppendUint16(nil, uint16(n))
		case int32:
			got = binary.LittleEndian.AppendUint32(nil, uint32(n))
		case float32:
			got = binary.LittleEndian.AppendUint32(nil, math.Float32bits(n))
		default:
			t.Fatalf("unaccepted PCCC encoder input %T", input)
		}
		if !bytes.Equal(got, tc.data) {
			t.Fatalf("PCCC %s %T %v: %x", tc.address, value, value, got)
		}
	}
	for _, tc := range []struct {
		code uint16
		data []byte
		want any
	}{
		{s7.TypeInt, []byte{128, 0}, int64(-32768)},
		{s7.TypeWord, []byte{255, 255}, uint64(65535)},
		{s7.TypeDWord, []byte{255, 255, 255, 255}, uint64(math.MaxUint32)},
		{s7.TypeULInt, []byte{255, 255, 255, 255, 255, 255, 255, 255}, uint64(math.MaxUint64)},
		{s7.TypeReal, []byte{63, 192, 0, 0}, float64(1.5)},
	} {
		value := normalizeDecoded((&s7.TagValue{DataType: tc.code, Bytes: tc.data, Count: 1}).GoValue())
		if !reflect.DeepEqual(value, tc.want) {
			t.Fatalf("S7 %T %v", value, value)
		}
		input, err := s7Canonical(tc.code, value)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(input, tc.want) {
			t.Fatalf("S7 encoder input %T %v", input, input)
		}
	}
	for _, tc := range []struct {
		code uint16
		data []byte
		want any
	}{
		{omron.TypeWord, []byte{255, 255}, uint64(65535)},
		{omron.TypeCIPINT, []byte{0, 128}, int64(-32768)},
		{omron.TypeCIPULINT, []byte{255, 255, 255, 255, 255, 255, 255, 255}, uint64(math.MaxUint64)},
		{omron.TypeCIPREAL, []byte{0, 0, 192, 63}, float64(1.5)},
	} {
		value := normalizePrimitive(omron.DecodeValue(tc.code, tc.data, false))
		if !reflect.DeepEqual(value, tc.want) {
			t.Fatalf("Omron %T %v", value, value)
		}
		input, err := omronCanonical(tc.code, value)
		if err != nil {
			t.Fatal(err)
		}
		got, err := omron.EncodeValue(input, tc.code, false)
		if err != nil || !bytes.Equal(got, tc.data) {
			t.Fatalf("Omron %x %v", got, err)
		}
	}
	for _, tc := range []struct {
		code uint16
		data []byte
		want any
	}{
		{logix.TypeINT, []byte{0, 128}, int64(-32768)},
		{logix.TypeUINT, []byte{255, 255}, uint64(65535)},
		{logix.TypeULINT, []byte{255, 255, 255, 255, 255, 255, 255, 255}, uint64(math.MaxUint64)},
		{logix.TypeREAL, []byte{0, 0, 192, 63}, float64(1.5)},
	} {
		value := normalizeDecoded((&logix.TagValue{DataType: tc.code, Bytes: tc.data}).GoValue())
		if !reflect.DeepEqual(value, tc.want) {
			t.Fatalf("Logix %T %v", value, value)
		}
		width, signed, floating := logixNumeric(tc.code)
		got, count, handled, err := canonicalStorage(value, width, signed, floating, binary.LittleEndian)
		if err != nil || !handled || count != 1 || !bytes.Equal(got, tc.data) {
			t.Fatalf("Logix %x %v", got, err)
		}
	}
}

func TestCanonicalNumericWriteBounds(t *testing.T) {
	for _, tc := range []struct {
		value            any
		width            int
		signed, floating bool
		want             []byte
	}{
		{int64(math.MinInt64), 64, true, false, []byte{0, 0, 0, 0, 0, 0, 0, 128}},
		{uint64(math.MaxUint64), 64, false, false, []byte{255, 255, 255, 255, 255, 255, 255, 255}},
		{[]uint64{65535}, 16, false, false, []byte{255, 255}},
		{[]int64{-32768, 32767}, 16, true, false, []byte{0, 128, 255, 127}},
		{float64(1.5), 32, true, true, []byte{0, 0, 192, 63}},
		{float64(255), 8, false, false, []byte{255}},
	} {
		got, _, handled, err := canonicalStorage(tc.value, tc.width, tc.signed, tc.floating, binary.LittleEndian)
		if err != nil || !handled || !bytes.Equal(got, tc.want) {
			t.Fatalf("%T %v: %x %v", tc.value, tc.value, got, err)
		}
	}
	for _, tc := range []struct {
		value            any
		width            int
		signed, floating bool
	}{
		{int64(-1), 16, false, false}, {uint64(65536), 16, false, false}, {int64(32768), 16, true, false},
		{uint64(math.MaxUint64), 64, true, false}, {float64(1.25), 32, true, false},
		{math.NaN(), 32, true, false}, {math.Inf(1), 64, false, false},
		{math.Ldexp(1, 64), 64, false, false}, {math.Ldexp(1, 63), 64, true, false},
		{math.MaxFloat64, 32, true, true}, {[]uint64{}, 64, false, false},
	} {
		_, _, handled, err := canonicalStorage(tc.value, tc.width, tc.signed, tc.floating, binary.LittleEndian)
		if !handled || err == nil {
			t.Fatalf("accepted %T %v for %d", tc.value, tc.value, tc.width)
		}
	}
	if width, _, _ := logixNumeric(0x80c4); width != 0 {
		t.Fatal("structure handle interpreted as DINT")
	}
}

func TestNativeArrayTextTimeAndRecordFixtures(t *testing.T) {
	for _, tc := range []struct {
		code  uint16
		data  []byte
		count int
		want  any
	}{
		{s7.TypeInt, []byte{128, 0, 127, 255}, 2, []int64{-32768, 32767}},
		{s7.TypeWord, []byte{0, 0, 255, 255}, 2, []uint64{0, 65535}},
		{s7.TypeReal, []byte{63, 192, 0, 0, 192, 0, 0, 0}, 2, []float64{1.5, -2}},
		{s7.MakeArrayType(s7.TypeInt), []byte{0, 1}, 1, []int64{1}},
		{s7.TypeBool, []byte{1, 0, 1}, 3, []bool{true, false, true}},
		{s7.TypeString, []byte{4, 2, 'A', 'B', 0, 0}, 1, "AB"},
		{s7.TypeTime, []byte{255, 255, 255, 255}, 1, int64(-1)},
		{s7.TypeDate, []byte{0, 1}, 1, int64(1)},
	} {
		value := normalizeDecoded((&s7.TagValue{DataType: tc.code, Bytes: tc.data, Count: tc.count, BitNum: -1}).GoValue())
		if !reflect.DeepEqual(value, tc.want) {
			t.Fatalf("S7 %x %T %#v want %#v", tc.code, value, value, tc.want)
		}
	}
	for _, tc := range []struct {
		code uint16
		data []byte
		want any
	}{
		{logix.TypeINT, []byte{0, 128, 255, 127}, []int64{-32768, 32767}},
		{logix.TypeUINT, []byte{0, 0, 255, 255}, []uint64{0, 65535}},
		{logix.TypeREAL, []byte{0, 0, 192, 63, 0, 0, 0, 192}, []float64{1.5, -2}},
		{logix.TypeBOOL, []byte{1, 0, 1}, []bool{true, false, true}},
	} {
		value := normalizeDecoded((&logix.TagValue{DataType: tc.code, Bytes: tc.data, Count: 2}).GoValue())
		if !reflect.DeepEqual(value, tc.want) {
			t.Fatalf("Logix %x %T %#v", tc.code, value, value)
		}
	}
	for _, tc := range []struct {
		code uint16
		data []byte
		want any
	}{
		{omron.TypeCIPINT, []byte{0, 128, 255, 127}, []int64{-32768, 32767}},
		{omron.TypeCIPUINT, []byte{0, 0, 255, 255}, []uint64{0, 65535}},
		{omron.TypeCIPREAL, []byte{0, 0, 192, 63, 0, 0, 0, 192}, []float64{1.5, -2}},
	} {
		value := normalizePrimitive((&omron.TagValue{DataType: omron.MakeArrayType(tc.code), Bytes: tc.data, Count: 2}).GoValue())
		if !reflect.DeepEqual(value, tc.want) {
			t.Fatalf("Omron %x %T %#v", tc.code, value, value)
		}
	}
	addr, err := pccc.ParseAddress("T4:0")
	if err != nil {
		t.Fatal(err)
	}
	value := normalizeDecoded(pccc.DecodeValue(addr, []byte{0, 160, 0, 128, 255, 127})).(map[string]any)
	if value["PRE"] != int64(-32768) || value["ACC"] != int64(32767) || value["EN"] != true || value["DN"] != true || value["TT"] != false {
		t.Fatal(value)
	}
}
