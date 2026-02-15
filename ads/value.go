package ads

import (
	"encoding/binary"
	"math"
)

// TagValue holds the result of a tag read operation.
// This structure is designed to be compatible with the warlink plcman package.
type TagValue struct {
	Name     string // Symbol name
	DataType uint16 // ADS type code
	Bytes    []byte // Raw value bytes (little-endian, native x86/TwinCAT format)
	Count    int    // Number of elements (1 for scalar, >1 for array)
	Error    error  // Per-tag error (nil if successful)
}

// GoValue returns the decoded Go value from the raw bytes.
// Returns the appropriate Go type based on the ADS data type:
//   - BOOL -> bool (or []bool for arrays)
//   - SINT -> int64 (or []int64 for arrays)
//   - BYTE -> uint64 (or []uint64 for arrays)
//   - INT -> int64 (or []int64 for arrays)
//   - WORD/UINT -> uint64 (or []uint64 for arrays)
//   - DINT -> int64 (or []int64 for arrays)
//   - DWORD/UDINT -> uint64 (or []uint64 for arrays)
//   - LINT -> int64 (or []int64 for arrays)
//   - LWORD/ULINT -> uint64 (or []uint64 for arrays)
//   - REAL -> float64 (or []float64 for arrays)
//   - LREAL -> float64 (or []float64 for arrays)
//   - STRING -> string (or []string for arrays)
//   - Unknown -> []int (byte array for JSON compatibility)
//
// Note: ADS/TwinCAT uses little-endian byte order (native x86 format).
func (v *TagValue) GoValue() interface{} {
	if v == nil || v.Error != nil || len(v.Bytes) == 0 {
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

	// Special handling for STRING/WSTRING arrays - they have variable element size
	// but Count > 1 indicates an array
	if !isArray && v.Count > 1 && (baseType == TypeString || baseType == TypeWString) {
		isArray = true
	}

	// Handle arrays
	if isArray {
		return v.parseArray(baseType)
	}

	// Handle scalar values
	return v.parseScalar(baseType)
}

// parseScalar parses a single ADS value based on type code.
// Uses little-endian byte order (native x86/TwinCAT format).
func (v *TagValue) parseScalar(baseType uint16) interface{} {
	switch baseType {
	case TypeBool:
		if len(v.Bytes) >= 1 {
			return v.Bytes[0] != 0
		}

	case TypeSByte:
		if len(v.Bytes) >= 1 {
			return int64(int8(v.Bytes[0]))
		}

	case TypeByte:
		if len(v.Bytes) >= 1 {
			return uint64(v.Bytes[0])
		}

	case TypeInt16:
		if len(v.Bytes) >= 2 {
			return int64(int16(binary.LittleEndian.Uint16(v.Bytes)))
		}

	case TypeWord:
		if len(v.Bytes) >= 2 {
			return uint64(binary.LittleEndian.Uint16(v.Bytes))
		}

	case TypeInt32, TypeTime, TypeTimeOfDay:
		if len(v.Bytes) >= 4 {
			return int64(int32(binary.LittleEndian.Uint32(v.Bytes)))
		}

	case TypeDWord, TypeDate:
		if len(v.Bytes) >= 4 {
			return uint64(binary.LittleEndian.Uint32(v.Bytes))
		}

	case TypeReal:
		if len(v.Bytes) >= 4 {
			bits := binary.LittleEndian.Uint32(v.Bytes)
			return float64(math.Float32frombits(bits))
		}

	case TypeInt64, TypeDateTime:
		if len(v.Bytes) >= 8 {
			return int64(binary.LittleEndian.Uint64(v.Bytes))
		}

	case TypeLWord, TypeLTime:
		if len(v.Bytes) >= 8 {
			return binary.LittleEndian.Uint64(v.Bytes)
		}

	case TypeLReal:
		if len(v.Bytes) >= 8 {
			bits := binary.LittleEndian.Uint64(v.Bytes)
			return math.Float64frombits(bits)
		}

	case TypeString:
		// TwinCAT strings are null-terminated
		for i, b := range v.Bytes {
			if b == 0 {
				return string(v.Bytes[:i])
			}
		}
		return string(v.Bytes)

	case TypeWString:
		// Wide strings: UTF-16LE, null-terminated
		var chars []rune
		for i := 0; i+1 < len(v.Bytes); i += 2 {
			c := binary.LittleEndian.Uint16(v.Bytes[i:])
			if c == 0 {
				break
			}
			chars = append(chars, rune(c))
		}
		return string(chars)
	}

	// Unknown type - return as byte array for JSON compatibility
	return v.bytesToIntArray()
}

// parseArray parses an ADS array value based on type code.
// Uses little-endian byte order (native x86/TwinCAT format).
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

	case TypeSByte:
		result := make([]int64, count)
		for i := 0; i < count; i++ {
			result[i] = int64(int8(v.Bytes[i]))
		}
		return result

	case TypeByte:
		result := make([]uint64, count)
		for i := 0; i < count; i++ {
			result[i] = uint64(v.Bytes[i])
		}
		return result

	case TypeInt16:
		result := make([]int64, count)
		for i := 0; i < count; i++ {
			offset := i * 2
			result[i] = int64(int16(binary.LittleEndian.Uint16(v.Bytes[offset:])))
		}
		return result

	case TypeWord:
		result := make([]uint64, count)
		for i := 0; i < count; i++ {
			offset := i * 2
			result[i] = uint64(binary.LittleEndian.Uint16(v.Bytes[offset:]))
		}
		return result

	case TypeInt32, TypeTime, TypeTimeOfDay:
		result := make([]int64, count)
		for i := 0; i < count; i++ {
			offset := i * 4
			result[i] = int64(int32(binary.LittleEndian.Uint32(v.Bytes[offset:])))
		}
		return result

	case TypeDWord, TypeDate:
		result := make([]uint64, count)
		for i := 0; i < count; i++ {
			offset := i * 4
			result[i] = uint64(binary.LittleEndian.Uint32(v.Bytes[offset:]))
		}
		return result

	case TypeReal:
		result := make([]float64, count)
		for i := 0; i < count; i++ {
			offset := i * 4
			bits := binary.LittleEndian.Uint32(v.Bytes[offset:])
			result[i] = float64(math.Float32frombits(bits))
		}
		return result

	case TypeInt64, TypeDateTime:
		result := make([]int64, count)
		for i := 0; i < count; i++ {
			offset := i * 8
			result[i] = int64(binary.LittleEndian.Uint64(v.Bytes[offset:]))
		}
		return result

	case TypeLWord, TypeLTime:
		result := make([]uint64, count)
		for i := 0; i < count; i++ {
			offset := i * 8
			result[i] = binary.LittleEndian.Uint64(v.Bytes[offset:])
		}
		return result

	case TypeLReal:
		result := make([]float64, count)
		for i := 0; i < count; i++ {
			offset := i * 8
			bits := binary.LittleEndian.Uint64(v.Bytes[offset:])
			result[i] = math.Float64frombits(bits)
		}
		return result

	default:
		return v.bytesToIntArray()
	}
}

// parseStringArray parses an array of TwinCAT STRING values.
// TwinCAT STRING arrays have fixed-size elements based on the declared string length.
// Default STRING(80) = 81 bytes (80 chars + null terminator).
func (v *TagValue) parseStringArray() []string {
	// Try to detect element size from the data
	// Look for null terminators to estimate string boundaries
	if len(v.Bytes) == 0 {
		return []string{}
	}

	// If Count is set, use it to determine element size
	if v.Count > 1 {
		elemSize := len(v.Bytes) / v.Count
		if elemSize > 0 {
			result := make([]string, v.Count)
			for i := 0; i < v.Count; i++ {
				offset := i * elemSize
				end := offset + elemSize
				if end > len(v.Bytes) {
					end = len(v.Bytes)
				}
				// Find null terminator within element
				strEnd := offset
				for j := offset; j < end; j++ {
					if v.Bytes[j] == 0 {
						strEnd = j
						break
					}
					strEnd = j + 1
				}
				result[i] = string(v.Bytes[offset:strEnd])
			}
			return result
		}
	}

	// Single string fallback
	for i, b := range v.Bytes {
		if b == 0 {
			return []string{string(v.Bytes[:i])}
		}
	}
	return []string{string(v.Bytes)}
}

// parseWStringArray parses an array of TwinCAT WSTRING values.
// TwinCAT WSTRING arrays have fixed-size elements (UTF-16LE encoded).
func (v *TagValue) parseWStringArray() []string {
	if len(v.Bytes) < 2 {
		return []string{}
	}

	// If Count is set, use it to determine element size
	if v.Count > 1 {
		elemSize := len(v.Bytes) / v.Count
		if elemSize >= 2 {
			result := make([]string, v.Count)
			for i := 0; i < v.Count; i++ {
				offset := i * elemSize
				end := offset + elemSize
				if end > len(v.Bytes) {
					end = len(v.Bytes)
				}
				// Parse UTF-16LE within element
				var chars []rune
				for j := offset; j+1 < end; j += 2 {
					c := binary.LittleEndian.Uint16(v.Bytes[j:])
					if c == 0 {
						break
					}
					chars = append(chars, rune(c))
				}
				result[i] = string(chars)
			}
			return result
		}
	}

	// Single wstring fallback
	var chars []rune
	for i := 0; i+1 < len(v.Bytes); i += 2 {
		c := binary.LittleEndian.Uint16(v.Bytes[i:])
		if c == 0 {
			break
		}
		chars = append(chars, rune(c))
	}
	return []string{string(chars)}
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
	if v == nil {
		return ""
	}
	return TypeName(v.DataType)
}
