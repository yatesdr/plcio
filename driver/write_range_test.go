package driver

import (
	"encoding/binary"
	"math"
	"reflect"
	"testing"

	"github.com/yatesdr/plcio/omron"
	"github.com/yatesdr/plcio/pccc"
	"github.com/yatesdr/plcio/s7"
)

type namedInt16 int16
type namedFloat32 float32
type namedInts []int

func mustPCCC(t *testing.T, address string) *pccc.FileAddress {
	t.Helper()
	addr, err := pccc.ParseAddress(address)
	if err != nil {
		t.Fatal(err)
	}
	return addr
}

// Pins the pre-existing results for canonical int64/uint64/float64 inputs and
// the untouched pass-through of in-range native Go numbers.
func TestCanonicalWritePinnedResults(t *testing.T) {
	type result struct {
		want any
		ok   bool
	}
	pcccCases := []struct {
		address string
		value   any
		result
	}{
		{"N7:0", int64(-32768), result{int16(-32768), true}},
		{"N7:0", int64(32767), result{int16(32767), true}},
		{"N7:0", int64(32768), result{nil, false}},
		{"N7:0", int64(65535), result{nil, false}},
		{"N7:0", uint64(32767), result{int16(32767), true}},
		{"N7:0", uint64(32768), result{nil, false}},
		{"N7:0", float64(3), result{int16(3), true}},
		{"N7:0", float64(3.5), result{nil, false}},
		{"N7:0", math.NaN(), result{nil, false}},
		{"N7:0", math.Inf(-1), result{nil, false}},
		{"N7:0", []int64{1, 2}, result{nil, false}},
		{"L10:0", int64(math.MinInt32), result{int32(math.MinInt32), true}},
		{"L10:0", int64(math.MaxInt32) + 1, result{nil, false}},
		{"F8:0", float64(1.5), result{float32(1.5), true}},
		{"F8:0", int64(3), result{int64(3), true}},
		{"B3:0", int64(-1), result{int16(-1), true}},
		{"B3:0", int64(-32768), result{int16(-32768), true}},
		{"B3:0", int64(32767), result{int16(32767), true}},
		{"B3:0", int64(-32769), result{nil, false}},
		{"B3:0/3", int64(1), result{int64(1), true}},
		{"T4:0.PRE", int64(-1), result{int16(-1), true}},
		{"N7:0", int32(5), result{int32(5), true}},
		{"N7:0", int(-5), result{int(-5), true}},
		{"N7:0", "text", result{"text", true}},
	}
	for _, tc := range pcccCases {
		got, err := pcccCanonical(mustPCCC(t, tc.address), tc.value)
		if (err == nil) != tc.ok || (tc.ok && !reflect.DeepEqual(got, tc.want)) {
			t.Errorf("PCCC %s %T %v: %T %v, %v", tc.address, tc.value, tc.value, got, got, err)
		}
	}
	s7Cases := []struct {
		code  uint16
		value any
		result
	}{
		{s7.TypeInt, int64(-32768), result{int64(-32768), true}},
		{s7.TypeInt, int64(32768), result{nil, false}},
		{s7.TypeSInt, int64(-129), result{nil, false}},
		{s7.TypeWord, int64(65535), result{uint64(65535), true}},
		{s7.TypeWord, int64(-1), result{nil, false}},
		{s7.TypeDWord, uint64(math.MaxUint32), result{uint64(math.MaxUint32), true}},
		{s7.TypeByte, []int64{1, 255}, result{[]int32{1, 255}, true}},
		{s7.TypeByte, []int64{1, 256}, result{nil, false}},
		{s7.TypeLInt, int64(math.MinInt64), result{int64(math.MinInt64), true}},
		{s7.TypeULInt, uint64(math.MaxUint64), result{uint64(math.MaxUint64), true}},
		{s7.TypeReal, float64(1.5), result{float64(1.5), true}},
		{s7.TypeInt, float64(2.5), result{nil, false}},
		{s7.TypeInt, int16(5), result{int16(5), true}},
		{s7.TypeReal, float32(1.5), result{float32(1.5), true}},
	}
	for _, tc := range s7Cases {
		got, err := s7Canonical(tc.code, tc.value)
		if (err == nil) != tc.ok || (tc.ok && !reflect.DeepEqual(got, tc.want)) {
			t.Errorf("S7 %#x %T %v: %T %v, %v", tc.code, tc.value, tc.value, got, got, err)
		}
	}
	omronCases := []struct {
		code  uint16
		value any
		result
	}{
		{omron.TypeCIPINT, uint64(5), result{int64(5), true}},
		{omron.TypeCIPINT, int64(-32768), result{int64(-32768), true}},
		{omron.TypeCIPINT, int64(32768), result{nil, false}},
		{omron.TypeCIPUINT, int64(-1), result{nil, false}},
		{omron.TypeCIPINT, []uint64{1}, result{[]int64{1}, true}},
		{omron.TypeCIPULINT, uint64(math.MaxUint64), result{uint64(math.MaxUint64), true}},
		{omron.TypeCIPREAL, float64(1.5), result{float64(1.5), true}},
		{omron.TypeCIPINT, uint16(7), result{uint16(7), true}},
	}
	for _, tc := range omronCases {
		got, err := omronCanonical(tc.code, tc.value)
		if (err == nil) != tc.ok || (tc.ok && !reflect.DeepEqual(got, tc.want)) {
			t.Errorf("Omron %#x %T %v: %T %v, %v", tc.code, tc.value, tc.value, got, got, err)
		}
	}
	for _, value := range []any{int64(1), uint64(1), float64(1), []int64{1}, []uint64{1}, []float64{1}} {
		if !canonicalNumeric(value) {
			t.Errorf("canonicalNumeric(%T) = false", value)
		}
	}
	for _, value := range []any{nil, "1", true, []byte{1}, []bool{true}, []string{"1"}, map[string]any{}} {
		if canonicalNumeric(value) {
			t.Errorf("canonicalNumeric(%T) = true", value)
		}
	}
}

// Every numeric Go kind (and slices of them) must meet the same range checks
// as int64/uint64/float64; in-range values still reach the native encoder as-is.
func TestCanonicalWriteRangeAllNumericKinds(t *testing.T) {
	for _, value := range []any{int(1), int8(1), int16(1), int32(1), uint(1), uint8(1), uint16(1),
		uint32(1), uintptr(1), float32(1), namedInt16(1), namedFloat32(1), []int{1}, []int8{1},
		[]int16{1}, []int32{1}, []uint{1}, []uint16{1}, []uint32{1}, []float32{1}, namedInts{1}} {
		if !canonicalNumeric(value) {
			t.Errorf("canonicalNumeric(%T) = false", value)
		}
	}
	rejectPCCC := []struct {
		address string
		value   any
	}{
		{"N7:0", int(70000)}, {"N7:0", int32(70000)}, {"N7:0", uint16(40000)}, {"N7:0", uint(40000)},
		{"N7:0", int(-32769)}, {"N7:0", float32(1.5)}, {"N7:0", float32(math.NaN())},
		{"N7:0", float32(math.Inf(1))}, {"N7:0", namedFloat32(0.5)},
		{"L10:0", int(1) << 40}, {"B3:0", int(70000)}, {"T4:0.ACC", uint32(70000)},
	}
	for _, tc := range rejectPCCC {
		if got, err := pcccCanonical(mustPCCC(t, tc.address), tc.value); err == nil {
			t.Errorf("PCCC %s accepted %T %v as %T %v", tc.address, tc.value, tc.value, got, got)
		}
	}
	acceptPCCC := []struct {
		address string
		value   any
	}{
		{"N7:0", int(-32768)}, {"N7:0", uint16(32767)}, {"N7:0", float32(3)}, {"N7:0", namedInt16(-2)},
		{"L10:0", int(math.MinInt32)}, {"F8:0", float32(1.5)}, {"F8:0", int(3)}, {"B3:0/1", int(1)},
	}
	for _, tc := range acceptPCCC {
		got, err := pcccCanonical(mustPCCC(t, tc.address), tc.value)
		if err != nil || !reflect.DeepEqual(got, tc.value) {
			t.Errorf("PCCC %s %T %v: %T %v, %v", tc.address, tc.value, tc.value, got, got, err)
		}
	}
	rejectS7 := []struct {
		code  uint16
		value any
	}{
		{s7.TypeSInt, int(300)}, {s7.TypeSInt, uint8(200)}, {s7.TypeInt, int32(40000)},
		{s7.TypeWord, int8(-1)}, {s7.TypeByte, int(256)}, {s7.TypeDInt, uint32(math.MaxUint32)},
		{s7.TypeInt, []int32{1, 70000}}, {s7.TypeInt, namedInts{1, -40000}}, {s7.TypeDInt, float32(math.NaN())},
		{s7.TypeDInt, []float32{1, 2.5}}, {s7.TypeWord, []int{-1}},
	}
	for _, tc := range rejectS7 {
		if got, err := s7Canonical(tc.code, tc.value); err == nil {
			t.Errorf("S7 %#x accepted %T %v as %T %v", tc.code, tc.value, tc.value, got, got)
		}
	}
	acceptS7 := []struct {
		code  uint16
		value any
	}{
		{s7.TypeSInt, int8(-128)}, {s7.TypeInt, []int16{-32768, 32767}}, {s7.TypeWord, uint16(65535)},
		{s7.TypeReal, float32(math.Inf(1))}, {s7.TypeReal, int32(7)}, {s7.TypeDInt, float32(-2)},
		{s7.TypeString, int(5)},
	}
	for _, tc := range acceptS7 {
		got, err := s7Canonical(tc.code, tc.value)
		if err != nil || !reflect.DeepEqual(got, tc.value) {
			t.Errorf("S7 %#x %T %v: %T %v, %v", tc.code, tc.value, tc.value, got, got, err)
		}
	}
	rejectOmron := []struct {
		code  uint16
		value any
	}{
		{omron.TypeCIPSINT, int(300)}, {omron.TypeCIPUINT, int16(-1)}, {omron.TypeCIPINT, []uint16{65535}},
		{omron.TypeCIPDINT, float32(math.Inf(-1))},
	}
	for _, tc := range rejectOmron {
		if got, err := omronCanonical(tc.code, tc.value); err == nil {
			t.Errorf("Omron %#x accepted %T %v as %T %v", tc.code, tc.value, tc.value, got, got)
		}
	}
	if got, err := omronCanonical(omron.TypeCIPUINT, uint16(65535)); err != nil || got != uint16(65535) {
		t.Errorf("Omron UINT 65535: %T %v, %v", got, got, err)
	}
	// Logix consumes canonicalStorage bytes directly only for canonical types;
	// other in-range numbers fall through (handled=false) to the native encoder.
	for _, tc := range []struct {
		value            any
		width            int
		signed, floating bool
	}{
		{int(300), 8, true, false}, {uint8(200), 8, true, false}, {int32(40000), 16, true, false},
		{int16(-1), 16, false, false}, {float32(1.5), 32, true, false}, {float32(math.NaN()), 32, true, false},
		{[]int{1, 70000}, 16, true, false}, {[]int32{}, 32, true, false}, {uint(math.MaxUint32) + 1, 32, false, false},
	} {
		_, _, handled, err := canonicalStorage(tc.value, tc.width, tc.signed, tc.floating, binary.LittleEndian)
		if !handled || err == nil {
			t.Errorf("canonicalStorage accepted %T %v for %d", tc.value, tc.value, tc.width)
		}
	}
	for _, tc := range []struct {
		value            any
		width            int
		signed, floating bool
	}{
		{int8(-128), 8, true, false}, {uint16(65535), 16, false, false}, {float32(1.5), 32, false, true},
		{int32(5), 32, false, true}, {[]int{1, 2}, 32, true, false}, {int(5), 0, false, false},
	} {
		data, count, handled, err := canonicalStorage(tc.value, tc.width, tc.signed, tc.floating, binary.LittleEndian)
		if handled || err != nil || data != nil || count != 0 {
			t.Errorf("canonicalStorage %T %v for %d: %x %d %v %v", tc.value, tc.value, tc.width, data, count, handled, err)
		}
	}
}

// B, S and A words are bit patterns: 0..65535 and -32768..-1 are both valid.
func TestPCCCBitPatternWordsAcceptUnsigned(t *testing.T) {
	for _, address := range []string{"B3:0", "S2:1", "A9:0"} {
		addr := mustPCCC(t, address)
		for _, tc := range []struct {
			value any
			want  any
		}{
			{int64(65535), int16(-1)}, {uint64(65535), int16(-1)}, {float64(40000), int16(-25536)},
			{int64(-1), int16(-1)}, {int64(32768), int16(-32768)}, {uint16(65535), uint16(65535)},
			{int(65535), int(65535)},
		} {
			got, err := pcccCanonical(addr, tc.value)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%s %T %v: %T %v, %v", address, tc.value, tc.value, got, got, err)
			}
		}
		for _, value := range []any{int64(65536), int64(-32769), uint64(65536), int(70000), float64(1.5)} {
			if got, err := pcccCanonical(addr, value); err == nil {
				t.Errorf("%s accepted %T %v as %v", address, value, value, got)
			}
		}
	}
	for _, address := range []string{"N7:0", "T4:0.PRE", "O:0", "I:0"} {
		if got, err := pcccCanonical(mustPCCC(t, address), int64(65535)); err == nil {
			t.Errorf("%s accepted 65535 as %v", address, got)
		}
	}
}
