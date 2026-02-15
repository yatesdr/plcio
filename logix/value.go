package logix

import (
	"encoding/binary"
	"fmt"
	"math"
)

// TagValue represents the result of reading a tag with type conversion helpers.
// This is a stateless result object with no internal references.
type TagValue struct {
	Name     string // Tag name as requested
	DataType uint16 // CIP data type code
	Bytes    []byte // Raw tag value bytes (little-endian)
	Count    int    // Number of elements (0 or 1 for scalar, >1 for array)
	Error    error  // Per-tag error (nil if successful)
}

// Bool returns the tag value as a boolean.
// Returns an error if the tag is not a BOOL type or has an error.
func (v *TagValue) Bool() (bool, error) {
	if v.Error != nil {
		return false, v.Error
	}
	if v.DataType != TypeBOOL {
		return false, fmt.Errorf("type mismatch: expected BOOL, got %s", v.TypeName())
	}
	if len(v.Bytes) < 1 {
		return false, fmt.Errorf("insufficient data for BOOL")
	}
	return v.Bytes[0] != 0, nil
}

// Int returns the tag value as a signed 64-bit integer.
// Works for SINT, INT, DINT, and LINT types.
func (v *TagValue) Int() (int64, error) {
	if v.Error != nil {
		return 0, v.Error
	}

	baseType := v.DataType & 0x0FFF

	switch baseType {
	case TypeSINT:
		if len(v.Bytes) < 1 {
			return 0, fmt.Errorf("insufficient data for SINT")
		}
		return int64(int8(v.Bytes[0])), nil
	case TypeINT:
		if len(v.Bytes) < 2 {
			return 0, fmt.Errorf("insufficient data for INT")
		}
		return int64(int16(binary.LittleEndian.Uint16(v.Bytes))), nil
	case TypeDINT:
		if len(v.Bytes) < 4 {
			return 0, fmt.Errorf("insufficient data for DINT")
		}
		return int64(int32(binary.LittleEndian.Uint32(v.Bytes))), nil
	case TypeLINT:
		if len(v.Bytes) < 8 {
			return 0, fmt.Errorf("insufficient data for LINT")
		}
		return int64(binary.LittleEndian.Uint64(v.Bytes)), nil
	default:
		return 0, fmt.Errorf("type mismatch: expected signed integer, got %s", v.TypeName())
	}
}

// Uint returns the tag value as an unsigned 64-bit integer.
// Works for USINT, UINT, UDINT, and ULINT types.
func (v *TagValue) Uint() (uint64, error) {
	if v.Error != nil {
		return 0, v.Error
	}

	baseType := v.DataType & 0x0FFF

	switch baseType {
	case TypeUSINT:
		if len(v.Bytes) < 1 {
			return 0, fmt.Errorf("insufficient data for USINT")
		}
		return uint64(v.Bytes[0]), nil
	case TypeUINT:
		if len(v.Bytes) < 2 {
			return 0, fmt.Errorf("insufficient data for UINT")
		}
		return uint64(binary.LittleEndian.Uint16(v.Bytes)), nil
	case TypeUDINT:
		if len(v.Bytes) < 4 {
			return 0, fmt.Errorf("insufficient data for UDINT")
		}
		return uint64(binary.LittleEndian.Uint32(v.Bytes)), nil
	case TypeULINT:
		if len(v.Bytes) < 8 {
			return 0, fmt.Errorf("insufficient data for ULINT")
		}
		return binary.LittleEndian.Uint64(v.Bytes), nil
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

	baseType := v.DataType & 0x0FFF

	switch baseType {
	case TypeREAL:
		if len(v.Bytes) < 4 {
			return 0, fmt.Errorf("insufficient data for REAL")
		}
		bits := binary.LittleEndian.Uint32(v.Bytes)
		return float64(math.Float32frombits(bits)), nil
	case TypeLREAL:
		if len(v.Bytes) < 8 {
			return 0, fmt.Errorf("insufficient data for LREAL")
		}
		bits := binary.LittleEndian.Uint64(v.Bytes)
		return math.Float64frombits(bits), nil
	default:
		return 0, fmt.Errorf("type mismatch: expected float, got %s", v.TypeName())
	}
}

// String returns the tag value as a string.
// Works for STRING and SHORT_STRING types.
func (v *TagValue) String() (string, error) {
	if v.Error != nil {
		return "", v.Error
	}

	baseType := v.DataType & 0x0FFF

	switch baseType {
	case TypeSTRING:
		// Logix STRING format: 4-byte length prefix + character data
		if len(v.Bytes) < 4 {
			return "", fmt.Errorf("insufficient data for STRING")
		}
		strLen := binary.LittleEndian.Uint32(v.Bytes[:4])
		if int(strLen) > len(v.Bytes)-4 {
			strLen = uint32(len(v.Bytes) - 4)
		}
		return string(v.Bytes[4 : 4+strLen]), nil
	case TypeShortSTRING:
		// Short string: 1-byte length prefix + character data
		if len(v.Bytes) < 1 {
			return "", fmt.Errorf("insufficient data for SHORT_STRING")
		}
		strLen := int(v.Bytes[0])
		if strLen > len(v.Bytes)-1 {
			strLen = len(v.Bytes) - 1
		}
		return string(v.Bytes[1 : 1+strLen]), nil
	default:
		return "", fmt.Errorf("type mismatch: expected string, got %s", v.TypeName())
	}
}

// GoValue returns the tag value converted to an appropriate Go type.
// Returns nil if there's an error. The returned type depends on the tag's data type:
//   - BOOL -> bool (or []bool for arrays)
//   - SINT, INT, DINT, LINT -> int64 (or []int64 for arrays)
//   - USINT, UINT, UDINT, ULINT -> uint64 (or []uint64 for arrays)
//   - REAL, LREAL -> float64 (or []float64 for arrays)
//   - STRING, SHORT_STRING -> string
//   - STRUCT or unknown -> []int (byte array for JSON compatibility)
//
// For UDT/structure decoding with member names, use GoValueDecoded() with a Client.
func (v *TagValue) GoValue() interface{} {
	if v.Error != nil {
		return nil
	}

	baseType := v.DataType & 0x0FFF

	// Determine if this is an array
	isArray := IsArray(v.DataType)
	if v.Count > 1 {
		isArray = true
	}

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

// GoValueDecoded returns the tag value converted to a Go type, with UDT decoding support.
// For structure types, this decodes the raw bytes using the template from the Client,
// returning a map[string]interface{} with member names as keys.
// For non-structure types, this behaves the same as GoValue().
func (v *TagValue) GoValueDecoded(client *Client) interface{} {
	if v.Error != nil {
		return nil
	}

	// Check if this is a structure type
	isStruct := IsStructure(v.DataType)
	if isStruct && client != nil {
		decoded, err := client.DecodeUDT(v.DataType, v.Bytes)
		if err == nil {
			debugLogVerbose("GoValueDecoded: decoded UDT %q (type 0x%04X) with %d members",
				v.Name, v.DataType, len(decoded))
			return decoded
		}
		debugLogVerbose("GoValueDecoded: failed to decode UDT %q (type 0x%04X): %v",
			v.Name, v.DataType, err)
		// Fall back to raw bytes on decode error
	} else if isStruct && client == nil {
		debugLogVerbose("GoValueDecoded: no client for UDT %q (type 0x%04X)", v.Name, v.DataType)
	}

	// For non-structures or if decoding failed, use standard conversion
	return v.GoValue()
}

// IsStructureType returns true if this tag value is a structure/UDT type.
func (v *TagValue) IsStructureType() bool {
	return IsStructure(v.DataType)
}

// parseScalar parses a single Logix value based on type code.
func (v *TagValue) parseScalar(baseType uint16) interface{} {
	switch baseType {
	case TypeBOOL:
		if len(v.Bytes) >= 1 {
			return v.Bytes[0] != 0
		}
	case TypeSINT:
		if len(v.Bytes) >= 1 {
			return int64(int8(v.Bytes[0]))
		}
	case TypeINT:
		if len(v.Bytes) >= 2 {
			return int64(int16(binary.LittleEndian.Uint16(v.Bytes)))
		}
	case TypeDINT:
		if len(v.Bytes) >= 4 {
			return int64(int32(binary.LittleEndian.Uint32(v.Bytes)))
		}
	case TypeLINT:
		if len(v.Bytes) >= 8 {
			return int64(binary.LittleEndian.Uint64(v.Bytes))
		}
	case TypeUSINT:
		if len(v.Bytes) >= 1 {
			return uint64(v.Bytes[0])
		}
	case TypeUINT:
		if len(v.Bytes) >= 2 {
			return uint64(binary.LittleEndian.Uint16(v.Bytes))
		}
	case TypeUDINT:
		if len(v.Bytes) >= 4 {
			return uint64(binary.LittleEndian.Uint32(v.Bytes))
		}
	case TypeULINT:
		if len(v.Bytes) >= 8 {
			return binary.LittleEndian.Uint64(v.Bytes)
		}
	case TypeREAL:
		if len(v.Bytes) >= 4 {
			bits := binary.LittleEndian.Uint32(v.Bytes)
			return float64(math.Float32frombits(bits))
		}
	case TypeLREAL:
		if len(v.Bytes) >= 8 {
			bits := binary.LittleEndian.Uint64(v.Bytes)
			return math.Float64frombits(bits)
		}
	case TypeSTRING:
		if val, err := v.String(); err == nil {
			return val
		}
	case TypeShortSTRING:
		if val, err := v.String(); err == nil {
			return val
		}
	}

	// Unknown type - return as byte array
	return v.bytesToIntArray()
}

// parseArray parses the raw bytes as an array of the given base type.
// Returns a typed slice for known types, or []int (byte array) for unknown types.
func (v *TagValue) parseArray(baseType uint16) interface{} {
	switch baseType {
	case TypeBOOL:
		// BOOL arrays: each element is 1 byte (0 or non-zero)
		result := make([]bool, len(v.Bytes))
		for i, b := range v.Bytes {
			result[i] = b != 0
		}
		return result

	case TypeSINT:
		// SINT: 1 byte signed
		result := make([]int64, len(v.Bytes))
		for i, b := range v.Bytes {
			result[i] = int64(int8(b))
		}
		return result

	case TypeUSINT:
		// USINT: 1 byte unsigned
		result := make([]uint64, len(v.Bytes))
		for i, b := range v.Bytes {
			result[i] = uint64(b)
		}
		return result

	case TypeINT:
		// INT: 2 bytes signed
		elemSize := 2
		count := len(v.Bytes) / elemSize
		result := make([]int64, count)
		for i := 0; i < count; i++ {
			offset := i * elemSize
			result[i] = int64(int16(binary.LittleEndian.Uint16(v.Bytes[offset:])))
		}
		return result

	case TypeUINT:
		// UINT: 2 bytes unsigned
		elemSize := 2
		count := len(v.Bytes) / elemSize
		result := make([]uint64, count)
		for i := 0; i < count; i++ {
			offset := i * elemSize
			result[i] = uint64(binary.LittleEndian.Uint16(v.Bytes[offset:]))
		}
		return result

	case TypeDINT:
		// DINT: 4 bytes signed
		elemSize := 4
		count := len(v.Bytes) / elemSize
		result := make([]int64, count)
		for i := 0; i < count; i++ {
			offset := i * elemSize
			result[i] = int64(int32(binary.LittleEndian.Uint32(v.Bytes[offset:])))
		}
		return result

	case TypeUDINT:
		// UDINT: 4 bytes unsigned
		elemSize := 4
		count := len(v.Bytes) / elemSize
		result := make([]uint64, count)
		for i := 0; i < count; i++ {
			offset := i * elemSize
			result[i] = uint64(binary.LittleEndian.Uint32(v.Bytes[offset:]))
		}
		return result

	case TypeLINT:
		// LINT: 8 bytes signed
		elemSize := 8
		count := len(v.Bytes) / elemSize
		result := make([]int64, count)
		for i := 0; i < count; i++ {
			offset := i * elemSize
			result[i] = int64(binary.LittleEndian.Uint64(v.Bytes[offset:]))
		}
		return result

	case TypeULINT:
		// ULINT: 8 bytes unsigned
		elemSize := 8
		count := len(v.Bytes) / elemSize
		result := make([]uint64, count)
		for i := 0; i < count; i++ {
			offset := i * elemSize
			result[i] = binary.LittleEndian.Uint64(v.Bytes[offset:])
		}
		return result

	case TypeREAL:
		// REAL: 4 bytes float
		elemSize := 4
		count := len(v.Bytes) / elemSize
		result := make([]float64, count)
		for i := 0; i < count; i++ {
			offset := i * elemSize
			bits := binary.LittleEndian.Uint32(v.Bytes[offset:])
			result[i] = float64(math.Float32frombits(bits))
		}
		return result

	case TypeLREAL:
		// LREAL: 8 bytes float
		elemSize := 8
		count := len(v.Bytes) / elemSize
		result := make([]float64, count)
		for i := 0; i < count; i++ {
			offset := i * elemSize
			bits := binary.LittleEndian.Uint64(v.Bytes[offset:])
			result[i] = math.Float64frombits(bits)
		}
		return result

	case TypeShortSTRING:
		// SHORT_STRING array: each element is 1-byte length + string data
		var result []string
		data := v.Bytes
		for len(data) > 0 {
			strLen := int(data[0])
			data = data[1:]
			if strLen > len(data) {
				strLen = len(data)
			}
			result = append(result, string(data[:strLen]))
			data = data[strLen:]
		}
		return result

	case TypeSTRING:
		// STRING array: each element is 4-byte length + string data (up to 82 chars typically)
		var result []string
		data := v.Bytes
		for len(data) >= 4 {
			strLen := int(binary.LittleEndian.Uint32(data[:4]))
			data = data[4:]
			if strLen > len(data) {
				strLen = len(data)
			}
			result = append(result, string(data[:strLen]))
			data = data[strLen:]
		}
		return result

	default:
		// Unknown array element type - return as byte array
		return v.bytesToIntArray()
	}
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
