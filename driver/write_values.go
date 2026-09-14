package driver

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/yatesdr/plcio/logix"
	"github.com/yatesdr/plcio/omron"
	"github.com/yatesdr/plcio/pccc"
	"github.com/yatesdr/plcio/s7"
)

func canonicalNumeric(value any) bool {
	switch value.(type) {
	case int64, uint64, float64, []int64, []uint64, []float64:
		return true
	}
	return false
}

// Check the canonical numeric forms at the adapter boundary, preserving native
// protocol encoders for existing input forms and vendor-specific text/time rules.
func canonicalStorage(value any, width int, signed, floating bool, order binary.ByteOrder) ([]byte, int, bool, error) {
	var values []any
	switch v := value.(type) {
	case int64:
		values = []any{v}
	case uint64:
		values = []any{v}
	case float64:
		values = []any{v}
	case []int64:
		values = make([]any, len(v))
		for i, n := range v {
			values[i] = n
		}
	case []uint64:
		values = make([]any, len(v))
		for i, n := range v {
			values[i] = n
		}
	case []float64:
		values = make([]any, len(v))
		for i, n := range v {
			values[i] = n
		}
	default:
		return nil, 0, false, nil
	}
	if width != 8 && width != 16 && width != 32 && width != 64 {
		return nil, 0, false, nil
	}
	if len(values) == 0 || len(values) > math.MaxUint16 {
		return nil, 0, true, fmt.Errorf("numeric write requires 1..65535 elements")
	}
	data := make([]byte, len(values)*(width/8))
	for i, value := range values {
		var bits uint64
		if floating {
			number, ok := value.(float64)
			if !ok {
				return nil, 0, false, nil
			} // Preserve existing numeric-to-REAL forms.
			if width == 32 {
				narrow := float32(number)
				if math.IsInf(float64(narrow), 0) && !math.IsInf(number, 0) {
					return nil, 0, true, fmt.Errorf("REAL overflow")
				}
				bits = uint64(math.Float32bits(narrow))
			} else {
				bits = math.Float64bits(number)
			}
		} else {
			switch number := value.(type) {
			case int64:
				if (!signed && number < 0) || (signed && width < 64 && (number < -(int64(1)<<(width-1)) || number >= int64(1)<<(width-1))) || (!signed && width < 64 && uint64(number) >= uint64(1)<<width) {
					return nil, 0, true, fmt.Errorf("integer outside target range")
				}
				bits = uint64(number)
			case uint64:
				bound := width
				if signed {
					bound--
				}
				if bound < 64 && number >= uint64(1)<<bound {
					return nil, 0, true, fmt.Errorf("integer outside target range")
				}
				bits = number
			case float64:
				lower, upper := float64(0), math.Ldexp(1, width)
				if signed {
					upper = math.Ldexp(1, width-1)
					lower = -upper
				}
				if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number || number < lower || number >= upper {
					return nil, 0, true, fmt.Errorf("integer input must be finite, integral and in range")
				}
				if signed {
					bits = uint64(int64(number))
				} else {
					bits = uint64(number)
				}
			}
		}
		out := data[i*(width/8):]
		switch width {
		case 8:
			out[0] = byte(bits)
		case 16:
			order.PutUint16(out, uint16(bits))
		case 32:
			order.PutUint32(out, uint32(bits))
		case 64:
			order.PutUint64(out, bits)
		}
	}
	return data, len(values), true, nil
}

func logixNumeric(code uint16) (int, bool, bool) {
	if logix.IsStructure(code) {
		return 0, false, false
	}
	switch code & 0x0fff {
	case logix.TypeSINT:
		return 8, true, false
	case logix.TypeINT:
		return 16, true, false
	case logix.TypeDINT:
		return 32, true, false
	case logix.TypeLINT:
		return 64, true, false
	case logix.TypeUSINT, logix.TypeBYTE:
		return 8, false, false
	case logix.TypeUINT, logix.TypeWORD:
		return 16, false, false
	case logix.TypeUDINT, logix.TypeDWORD:
		return 32, false, false
	case logix.TypeULINT, logix.TypeLWORD:
		return 64, false, false
	case logix.TypeREAL:
		return 32, false, true
	case logix.TypeLREAL:
		return 64, false, true
	}
	return 0, false, false
}

func omronNumeric(code uint16) (int, bool, bool) {
	switch omron.BaseType(code) {
	case omron.TypeSByte, omron.TypeCIPSINT:
		return 8, true, false
	case omron.TypeInt16, omron.TypeCIPINT:
		return 16, true, false
	case omron.TypeInt32, omron.TypeCIPDINT:
		return 32, true, false
	case omron.TypeInt64, omron.TypeCIPLINT:
		return 64, true, false
	case omron.TypeByte, omron.TypeCIPUSINT:
		return 8, false, false
	case omron.TypeWord, omron.TypeCIPUINT:
		return 16, false, false
	case omron.TypeDWord, omron.TypeCIPUDINT:
		return 32, false, false
	case omron.TypeLWord, omron.TypeCIPULINT:
		return 64, false, false
	case omron.TypeReal, omron.TypeCIPREAL:
		return 32, false, true
	case omron.TypeLReal, omron.TypeCIPLREAL:
		return 64, false, true
	}
	return 0, false, false
}

func s7Numeric(code uint16) (int, bool, bool) {
	switch s7.BaseType(code) {
	case s7.TypeSInt:
		return 8, true, false
	case s7.TypeInt:
		return 16, true, false
	case s7.TypeDInt:
		return 32, true, false
	case s7.TypeLInt:
		return 64, true, false
	case s7.TypeByte:
		return 8, false, false
	case s7.TypeWord:
		return 16, false, false
	case s7.TypeDWord:
		return 32, false, false
	case s7.TypeULInt:
		return 64, false, false
	case s7.TypeReal:
		return 32, false, true
	case s7.TypeLReal:
		return 64, false, true
	}
	return 0, false, false
}

func pcccCanonical(addr *pccc.FileAddress, value any) (any, error) {
	if addr.BitNumber >= 0 {
		return value, nil
	}
	width, floating := 0, false
	switch addr.FileType {
	case pccc.FileTypeInteger, pccc.FileTypeOutput, pccc.FileTypeInput, pccc.FileTypeStatus, pccc.FileTypeBinary, pccc.FileTypeASCII:
		width = 16
	case pccc.FileTypeLong:
		width = 32
	case pccc.FileTypeFloat:
		width, floating = 32, true
	case pccc.FileTypeTimer, pccc.FileTypeCounter, pccc.FileTypeControl:
		if addr.SubElement > 0 {
			width = 16
		}
	}
	data, count, handled, err := canonicalStorage(value, width, true, floating, binary.LittleEndian)
	if !handled || err != nil {
		return value, err
	}
	if count != 1 {
		return nil, fmt.Errorf("PCCC address write requires one element")
	}
	if floating {
		return math.Float32frombits(binary.LittleEndian.Uint32(data)), nil
	}
	if width == 16 {
		return int16(binary.LittleEndian.Uint16(data)), nil
	}
	return int32(binary.LittleEndian.Uint32(data)), nil
}

func s7Canonical(code uint16, value any) (any, error) {
	width, signed, floating := s7Numeric(code)
	data, count, handled, err := canonicalStorage(value, width, signed, floating, binary.BigEndian)
	if !handled || err != nil {
		return value, err
	}
	if floating {
		return value, nil
	}
	array := false
	switch value.(type) {
	case []int64, []uint64, []float64:
		array = true
	}
	if width == 64 {
		values := make([]int64, count)
		for i := range count {
			values[i] = int64(binary.BigEndian.Uint64(data[i*8:]))
		}
		if array {
			return values, nil
		}
		if signed {
			return values[0], nil
		}
		return uint64(values[0]), nil
	}
	values := make([]int32, count)
	for i := range count {
		switch width {
		case 8:
			if signed {
				values[i] = int32(int8(data[i]))
			} else {
				values[i] = int32(data[i])
			}
		case 16:
			if signed {
				values[i] = int32(int16(binary.BigEndian.Uint16(data[i*2:])))
			} else {
				values[i] = int32(binary.BigEndian.Uint16(data[i*2:]))
			}
		case 32:
			values[i] = int32(binary.BigEndian.Uint32(data[i*4:]))
		}
	}
	if array {
		return values, nil
	}
	if signed {
		return int64(values[0]), nil
	}
	if width == 32 {
		return uint64(uint32(values[0])), nil
	}
	return uint64(values[0]), nil
}

func omronCanonical(code uint16, value any) (any, error) {
	width, signed, floating := omronNumeric(code)
	_, _, handled, err := canonicalStorage(value, width, signed, floating, binary.LittleEndian)
	if !handled || err != nil {
		return value, err
	}
	// int64 is an existing accepted form for every integer target. Its two's
	// complement storage preserves all unsigned64 bits without floating point.
	switch v := value.(type) {
	case uint64:
		if width < 64 {
			return int64(v), nil
		}
	case []uint64:
		return widenSlice(v, func(n uint64) int64 { return int64(n) }), nil
	}
	return value, nil
}
