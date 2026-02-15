package s7

import (
	"encoding/binary"
	"fmt"
	"math"
)

// TagValue represents the result of reading an S7 address with type conversion helpers.
type TagValue struct {
	Name     string // Address string as requested (e.g., "DB1.DBW0")
	DataType uint16 // S7 data type code
	Bytes    []byte // Raw value bytes (big-endian, as S7 uses)
	BitNum   int    // Bit number for BOOL types (-1 for non-bit)
	Count    int    // Number of elements (1 for scalar, >1 for array)
	Error    error  // Per-tag error (nil if successful)
}

// Bool returns the tag value as a boolean.
// Works for BOOL type or extracts a specific bit from a byte.
func (v *TagValue) Bool() (bool, error) {
	if v.Error != nil {
		return false, v.Error
	}
	if len(v.Bytes) < 1 {
		return false, fmt.Errorf("insufficient data for BOOL")
	}

	if v.BitNum >= 0 && v.BitNum <= 7 {
		// Extract specific bit
		return (v.Bytes[0] & (1 << v.BitNum)) != 0, nil
	}

	// Non-bit BOOL (full byte)
	return v.Bytes[0] != 0, nil
}

// Int returns the tag value as a signed 64-bit integer.
// Works for SINT, INT, DINT, and LINT types.
func (v *TagValue) Int() (int64, error) {
	if v.Error != nil {
		return 0, v.Error
	}

	switch v.DataType {
	case TypeSInt:
		if len(v.Bytes) < 1 {
			return 0, fmt.Errorf("insufficient data for SINT")
		}
		return int64(int8(v.Bytes[0])), nil
	case TypeInt:
		if len(v.Bytes) < 2 {
			return 0, fmt.Errorf("insufficient data for INT")
		}
		return int64(int16(binary.BigEndian.Uint16(v.Bytes))), nil
	case TypeDInt:
		if len(v.Bytes) < 4 {
			return 0, fmt.Errorf("insufficient data for DINT")
		}
		return int64(int32(binary.BigEndian.Uint32(v.Bytes))), nil
	case TypeLInt:
		if len(v.Bytes) < 8 {
			return 0, fmt.Errorf("insufficient data for LINT")
		}
		return int64(binary.BigEndian.Uint64(v.Bytes)), nil
	default:
		return 0, fmt.Errorf("type mismatch: expected signed integer, got %s", v.TypeName())
	}
}

// Uint returns the tag value as an unsigned 64-bit integer.
// Works for BYTE, WORD, DWORD, and ULINT types.
func (v *TagValue) Uint() (uint64, error) {
	if v.Error != nil {
		return 0, v.Error
	}

	switch v.DataType {
	case TypeByte:
		if len(v.Bytes) < 1 {
			return 0, fmt.Errorf("insufficient data for BYTE")
		}
		return uint64(v.Bytes[0]), nil
	case TypeWord:
		if len(v.Bytes) < 2 {
			return 0, fmt.Errorf("insufficient data for WORD")
		}
		return uint64(binary.BigEndian.Uint16(v.Bytes)), nil
	case TypeDWord:
		if len(v.Bytes) < 4 {
			return 0, fmt.Errorf("insufficient data for DWORD")
		}
		return uint64(binary.BigEndian.Uint32(v.Bytes)), nil
	case TypeULInt:
		if len(v.Bytes) < 8 {
			return 0, fmt.Errorf("insufficient data for ULINT")
		}
		return binary.BigEndian.Uint64(v.Bytes), nil
	default:
		return 0, fmt.Errorf("type mismatch: expected unsigned integer, got %s", v.TypeName())
	}
}

// Float returns the tag value as a 64-bit float.
// Works for REAL (float32) and LREAL (float64) types.
func (v *TagValue) Float() (float64, error) {
	if v.Error != nil {
		return 0, v.Error
	}

	switch v.DataType {
	case TypeReal:
		if len(v.Bytes) < 4 {
			return 0, fmt.Errorf("insufficient data for REAL")
		}
		bits := binary.BigEndian.Uint32(v.Bytes)
		return float64(math.Float32frombits(bits)), nil
	case TypeLReal:
		if len(v.Bytes) < 8 {
			return 0, fmt.Errorf("insufficient data for LREAL")
		}
		bits := binary.BigEndian.Uint64(v.Bytes)
		return math.Float64frombits(bits), nil
	default:
		return 0, fmt.Errorf("type mismatch: expected float, got %s", v.TypeName())
	}
}

// GoValue returns the tag value converted to an appropriate Go type.
// Returns nil if there's an error. The returned type depends on the tag's data type:
//   - BOOL -> bool (or []bool for arrays)
//   - SINT, INT, DINT, LINT -> int64 (or []int64 for arrays)
//   - BYTE, WORD, DWORD, ULINT -> uint64 (or []uint64 for arrays)
//   - REAL, LREAL -> float64 (or []float64 for arrays)
//   - CHAR, WCHAR -> uint64 (character code)
//   - TIME, TIME_OF_DAY -> int64 (milliseconds)
//   - DATE -> int64 (days since 1990-01-01)
//   - Unknown -> []int (byte array for JSON compatibility)
//
// Note: S7 uses big-endian byte order natively.
func (v *TagValue) GoValue() interface{} {
	if v.Error != nil {
		return nil
	}

	baseType := BaseType(v.DataType)

	// Determine if this is an array
	isArray := IsArray(v.DataType) || v.Count > 1

	// Detect arrays by data size if not already marked
	if !isArray {
		elemSize := TypeSize(baseType)
		if elemSize > 0 && len(v.Bytes) > elemSize {
			isArray = true
		}
	}

	// Handle arrays
	if isArray {
		return v.parseArray(baseType)
	}

	// Handle scalar values
	return v.parseScalar(baseType)
}

// parseScalar parses a single S7 value based on type code.
// Uses big-endian byte order (native S7 format).
func (v *TagValue) parseScalar(baseType uint16) interface{} {
	switch baseType {
	case TypeBool:
		if len(v.Bytes) >= 1 {
			if v.BitNum >= 0 && v.BitNum <= 7 {
				return (v.Bytes[0] & (1 << v.BitNum)) != 0
			}
			return v.Bytes[0] != 0
		}
	case TypeSInt:
		if len(v.Bytes) >= 1 {
			return int64(int8(v.Bytes[0]))
		}
	case TypeByte, TypeChar:
		if len(v.Bytes) >= 1 {
			return uint64(v.Bytes[0])
		}
	case TypeInt:
		if len(v.Bytes) >= 2 {
			return int64(int16(binary.BigEndian.Uint16(v.Bytes)))
		}
	case TypeWord: // TypeUInt is an alias for TypeWord
		if len(v.Bytes) >= 2 {
			return uint64(binary.BigEndian.Uint16(v.Bytes))
		}
	case TypeWChar:
		if len(v.Bytes) >= 2 {
			return uint64(binary.BigEndian.Uint16(v.Bytes))
		}
	case TypeDate:
		if len(v.Bytes) >= 2 {
			return int64(binary.BigEndian.Uint16(v.Bytes)) // Days since 1990-01-01
		}
	case TypeDInt:
		if len(v.Bytes) >= 4 {
			return int64(int32(binary.BigEndian.Uint32(v.Bytes)))
		}
	case TypeDWord: // TypeUDInt is an alias for TypeDWord
		if len(v.Bytes) >= 4 {
			return uint64(binary.BigEndian.Uint32(v.Bytes))
		}
	case TypeReal:
		if len(v.Bytes) >= 4 {
			bits := binary.BigEndian.Uint32(v.Bytes)
			return float64(math.Float32frombits(bits))
		}
	case TypeTime, TypeTimeOfDay:
		if len(v.Bytes) >= 4 {
			return int64(binary.BigEndian.Uint32(v.Bytes)) // Milliseconds
		}
	case TypeLInt:
		if len(v.Bytes) >= 8 {
			return int64(binary.BigEndian.Uint64(v.Bytes))
		}
	case TypeLWord: // TypeULInt is an alias for TypeLWord
		if len(v.Bytes) >= 8 {
			return binary.BigEndian.Uint64(v.Bytes)
		}
	case TypeLReal:
		if len(v.Bytes) >= 8 {
			bits := binary.BigEndian.Uint64(v.Bytes)
			return math.Float64frombits(bits)
		}
	case TypeString:
		// S7 String format: 1 byte max length, 1 byte actual length, then chars
		if len(v.Bytes) >= 2 {
			strLen := int(v.Bytes[1])
			if strLen > len(v.Bytes)-2 {
				strLen = len(v.Bytes) - 2
			}
			return string(v.Bytes[2 : 2+strLen])
		}
	case TypeWString:
		// S7 WString format: 2 bytes max length, 2 bytes actual length, then UTF-16BE chars
		if len(v.Bytes) >= 4 {
			strLen := int(binary.BigEndian.Uint16(v.Bytes[2:4])) * 2 // UTF-16 chars
			if strLen > len(v.Bytes)-4 {
				strLen = len(v.Bytes) - 4
			}
			// Simple ASCII extraction from UTF-16BE
			result := make([]byte, strLen/2)
			for i := 0; i < len(result); i++ {
				result[i] = v.Bytes[4+i*2+1] // Low byte of UTF-16BE
			}
			return string(result)
		}
	}

	// Unknown type - return as byte array
	return v.bytesToIntArray()
}

// parseArray parses an S7 array value based on type code.
// Uses big-endian byte order (native S7 format).
func (v *TagValue) parseArray(baseType uint16) interface{} {
	elemSize := TypeSize(baseType)

	// Handle variable-length string types specially
	if baseType == TypeString {
		return v.parseStringArray()
	}
	if baseType == TypeWString {
		return v.parseWStringArray()
	}

	if elemSize == 0 {
		return v.bytesToIntArray()
	}

	count := len(v.Bytes) / elemSize
	if count == 0 {
		return v.bytesToIntArray()
	}

	switch baseType {
	case TypeBool:
		result := make([]bool, count)
		for i := 0; i < count; i++ {
			result[i] = v.Bytes[i] != 0
		}
		return result

	case TypeSInt:
		result := make([]int64, count)
		for i := 0; i < count; i++ {
			result[i] = int64(int8(v.Bytes[i]))
		}
		return result

	case TypeByte, TypeChar:
		result := make([]uint64, count)
		for i := 0; i < count; i++ {
			result[i] = uint64(v.Bytes[i])
		}
		return result

	case TypeInt:
		result := make([]int64, count)
		for i := 0; i < count; i++ {
			offset := i * 2
			result[i] = int64(int16(binary.BigEndian.Uint16(v.Bytes[offset:])))
		}
		return result

	case TypeWord, TypeDate, TypeWChar: // TypeUInt is an alias for TypeWord
		result := make([]uint64, count)
		for i := 0; i < count; i++ {
			offset := i * 2
			result[i] = uint64(binary.BigEndian.Uint16(v.Bytes[offset:]))
		}
		return result

	case TypeDInt:
		result := make([]int64, count)
		for i := 0; i < count; i++ {
			offset := i * 4
			result[i] = int64(int32(binary.BigEndian.Uint32(v.Bytes[offset:])))
		}
		return result

	case TypeDWord, TypeTime, TypeTimeOfDay: // TypeUDInt is an alias for TypeDWord
		result := make([]uint64, count)
		for i := 0; i < count; i++ {
			offset := i * 4
			result[i] = uint64(binary.BigEndian.Uint32(v.Bytes[offset:]))
		}
		return result

	case TypeReal:
		result := make([]float64, count)
		for i := 0; i < count; i++ {
			offset := i * 4
			bits := binary.BigEndian.Uint32(v.Bytes[offset:])
			result[i] = float64(math.Float32frombits(bits))
		}
		return result

	case TypeLInt:
		result := make([]int64, count)
		for i := 0; i < count; i++ {
			offset := i * 8
			result[i] = int64(binary.BigEndian.Uint64(v.Bytes[offset:]))
		}
		return result

	case TypeLWord: // TypeULInt is an alias for TypeLWord
		result := make([]uint64, count)
		for i := 0; i < count; i++ {
			offset := i * 8
			result[i] = binary.BigEndian.Uint64(v.Bytes[offset:])
		}
		return result

	case TypeLReal:
		result := make([]float64, count)
		for i := 0; i < count; i++ {
			offset := i * 8
			bits := binary.BigEndian.Uint64(v.Bytes[offset:])
			result[i] = math.Float64frombits(bits)
		}
		return result

	default:
		return v.bytesToIntArray()
	}
}

// parseStringArray parses an array of S7 STRING values.
// Each S7 STRING is 256 bytes: 1 byte max length, 1 byte actual length, 254 chars.
func (v *TagValue) parseStringArray() []string {
	const elemSize = 256 // Standard S7 STRING size
	count := len(v.Bytes) / elemSize
	if count == 0 {
		// Try to parse as a single string if we have at least the header
		if len(v.Bytes) >= 2 {
			strLen := int(v.Bytes[1])
			if strLen > len(v.Bytes)-2 {
				strLen = len(v.Bytes) - 2
			}
			return []string{string(v.Bytes[2 : 2+strLen])}
		}
		return []string{}
	}

	result := make([]string, count)
	for i := 0; i < count; i++ {
		offset := i * elemSize
		if offset+2 > len(v.Bytes) {
			break
		}
		strLen := int(v.Bytes[offset+1]) // Actual length byte
		if strLen > 254 {
			strLen = 254
		}
		if offset+2+strLen > len(v.Bytes) {
			strLen = len(v.Bytes) - offset - 2
		}
		if strLen > 0 {
			result[i] = string(v.Bytes[offset+2 : offset+2+strLen])
		}
	}
	return result
}

// parseWStringArray parses an array of S7 WSTRING values.
// Each S7 WSTRING is 512 bytes: 2 bytes max length, 2 bytes actual length, 508 bytes UTF-16BE chars.
func (v *TagValue) parseWStringArray() []string {
	const elemSize = 512 // Standard S7 WSTRING size
	count := len(v.Bytes) / elemSize
	if count == 0 {
		// Try to parse as a single wstring if we have at least the header
		if len(v.Bytes) >= 4 {
			strLen := int(binary.BigEndian.Uint16(v.Bytes[2:4])) * 2
			if strLen > len(v.Bytes)-4 {
				strLen = len(v.Bytes) - 4
			}
			// Simple ASCII extraction from UTF-16BE
			chars := make([]byte, strLen/2)
			for j := 0; j < len(chars); j++ {
				chars[j] = v.Bytes[4+j*2+1]
			}
			return []string{string(chars)}
		}
		return []string{}
	}

	result := make([]string, count)
	for i := 0; i < count; i++ {
		offset := i * elemSize
		if offset+4 > len(v.Bytes) {
			break
		}
		strLen := int(binary.BigEndian.Uint16(v.Bytes[offset+2:offset+4])) * 2 // UTF-16 char count * 2
		if strLen > 508 {
			strLen = 508
		}
		if offset+4+strLen > len(v.Bytes) {
			strLen = len(v.Bytes) - offset - 4
		}
		// Simple ASCII extraction from UTF-16BE
		chars := make([]byte, strLen/2)
		for j := 0; j < len(chars); j++ {
			chars[j] = v.Bytes[offset+4+j*2+1]
		}
		result[i] = string(chars)
	}
	return result
}

// bytesToIntArray converts the raw bytes to []int for JSON-friendly output.
func (v *TagValue) bytesToIntArray() []int {
	intBytes := make([]int, len(v.Bytes))
	for i, b := range v.Bytes {
		intBytes[i] = int(b)
	}
	return intBytes
}

// TypeName returns the human-readable type name for this tag.
func (v *TagValue) TypeName() string {
	return TypeName(v.DataType)
}
