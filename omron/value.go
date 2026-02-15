package omron

import "fmt"

// TagValue holds the result of a tag read operation.
type TagValue struct {
	Name      string      // Tag name or address
	DataType  uint16      // Type code
	Bytes     []byte      // Raw data
	Count     int         // Element count for arrays
	Error     error       // Per-item error (nil if successful)
	bigEndian bool        // True for FINS, false for CIP
}

// GoValue returns the decoded Go value.
func (tv *TagValue) GoValue() interface{} {
	if tv == nil || tv.Error != nil || len(tv.Bytes) == 0 {
		return nil
	}

	baseType := BaseType(tv.DataType)

	// STRING is special - the "array dimension" is the string length, not multiple strings
	// Decode entire buffer as a single null-terminated string
	if baseType == TypeString || baseType == TypeCIPSTRING {
		return decodeString(tv.Bytes)
	}

	elemSize := TypeSize(baseType)
	if elemSize == 0 {
		elemSize = 2 // Default to word size
	}

	// Handle arrays
	if tv.Count > 1 && IsArray(tv.DataType) {
		return tv.decodeArray(baseType, elemSize)
	}

	// Single value
	return DecodeValue(baseType, tv.Bytes, tv.bigEndian)
}

// decodeString decodes bytes as a null-terminated string.
func decodeString(data []byte) string {
	for i, b := range data {
		if b == 0 {
			return string(data[:i])
		}
	}
	return string(data)
}

// decodeArray decodes array data into a slice.
func (tv *TagValue) decodeArray(baseType uint16, elemSize int) interface{} {
	count := tv.Count
	if count*elemSize > len(tv.Bytes) {
		count = len(tv.Bytes) / elemSize
	}

	switch baseType {
	case TypeBool, TypeCIPBool:
		result := make([]bool, count)
		for i := 0; i < count; i++ {
			result[i] = DecodeValue(baseType, tv.Bytes[i*elemSize:], tv.bigEndian).(bool)
		}
		return result

	case TypeByte, TypeCIPUSINT:
		result := make([]uint8, count)
		for i := 0; i < count; i++ {
			result[i] = tv.Bytes[i]
		}
		return result

	case TypeSByte, TypeCIPSINT:
		result := make([]int8, count)
		for i := 0; i < count; i++ {
			result[i] = int8(tv.Bytes[i])
		}
		return result

	case TypeWord, TypeCIPUINT:
		result := make([]uint16, count)
		for i := 0; i < count; i++ {
			result[i] = DecodeValue(baseType, tv.Bytes[i*2:], tv.bigEndian).(uint16)
		}
		return result

	case TypeInt16, TypeCIPINT:
		result := make([]int16, count)
		for i := 0; i < count; i++ {
			result[i] = DecodeValue(baseType, tv.Bytes[i*2:], tv.bigEndian).(int16)
		}
		return result

	case TypeDWord, TypeCIPUDINT:
		result := make([]uint32, count)
		for i := 0; i < count; i++ {
			result[i] = DecodeValue(baseType, tv.Bytes[i*4:], tv.bigEndian).(uint32)
		}
		return result

	case TypeInt32, TypeCIPDINT:
		result := make([]int32, count)
		for i := 0; i < count; i++ {
			result[i] = DecodeValue(baseType, tv.Bytes[i*4:], tv.bigEndian).(int32)
		}
		return result

	case TypeReal, TypeCIPREAL:
		result := make([]float32, count)
		for i := 0; i < count; i++ {
			result[i] = DecodeValue(baseType, tv.Bytes[i*4:], tv.bigEndian).(float32)
		}
		return result

	case TypeLWord, TypeCIPULINT:
		result := make([]uint64, count)
		for i := 0; i < count; i++ {
			result[i] = DecodeValue(baseType, tv.Bytes[i*8:], tv.bigEndian).(uint64)
		}
		return result

	case TypeInt64, TypeCIPLINT:
		result := make([]int64, count)
		for i := 0; i < count; i++ {
			result[i] = DecodeValue(baseType, tv.Bytes[i*8:], tv.bigEndian).(int64)
		}
		return result

	case TypeLReal, TypeCIPLREAL:
		result := make([]float64, count)
		for i := 0; i < count; i++ {
			result[i] = DecodeValue(baseType, tv.Bytes[i*8:], tv.bigEndian).(float64)
		}
		return result

	default:
		return tv.Bytes
	}
}

// TypeName returns the human-readable type name.
func (tv *TagValue) TypeName() string {
	if tv == nil {
		return "UNKNOWN"
	}
	return TypeName(tv.DataType)
}

// Bool returns the value as a boolean.
func (tv *TagValue) Bool() (bool, error) {
	if tv == nil || tv.Error != nil {
		return false, tv.Error
	}
	v := tv.GoValue()
	switch b := v.(type) {
	case bool:
		return b, nil
	case int:
		return b != 0, nil
	case int16:
		return b != 0, nil
	case int32:
		return b != 0, nil
	case int64:
		return b != 0, nil
	case uint16:
		return b != 0, nil
	case uint32:
		return b != 0, nil
	case uint64:
		return b != 0, nil
	default:
		return false, fmt.Errorf("cannot convert %T to bool", v)
	}
}

// Int returns the value as an int64.
func (tv *TagValue) Int() (int64, error) {
	if tv == nil || tv.Error != nil {
		return 0, tv.Error
	}
	v := tv.GoValue()
	switch n := v.(type) {
	case bool:
		if n {
			return 1, nil
		}
		return 0, nil
	case int8:
		return int64(n), nil
	case int16:
		return int64(n), nil
	case int32:
		return int64(n), nil
	case int64:
		return n, nil
	case uint8:
		return int64(n), nil
	case uint16:
		return int64(n), nil
	case uint32:
		return int64(n), nil
	case uint64:
		return int64(n), nil
	case float32:
		return int64(n), nil
	case float64:
		return int64(n), nil
	default:
		return 0, fmt.Errorf("cannot convert %T to int64", v)
	}
}

// Uint returns the value as a uint64.
func (tv *TagValue) Uint() (uint64, error) {
	if tv == nil || tv.Error != nil {
		return 0, tv.Error
	}
	v := tv.GoValue()
	switch n := v.(type) {
	case bool:
		if n {
			return 1, nil
		}
		return 0, nil
	case int8:
		return uint64(n), nil
	case int16:
		return uint64(n), nil
	case int32:
		return uint64(n), nil
	case int64:
		return uint64(n), nil
	case uint8:
		return uint64(n), nil
	case uint16:
		return uint64(n), nil
	case uint32:
		return uint64(n), nil
	case uint64:
		return n, nil
	case float32:
		return uint64(n), nil
	case float64:
		return uint64(n), nil
	default:
		return 0, fmt.Errorf("cannot convert %T to uint64", v)
	}
}

// Float returns the value as a float64.
func (tv *TagValue) Float() (float64, error) {
	if tv == nil || tv.Error != nil {
		return 0, tv.Error
	}
	v := tv.GoValue()
	switch n := v.(type) {
	case float32:
		return float64(n), nil
	case float64:
		return n, nil
	case int8:
		return float64(n), nil
	case int16:
		return float64(n), nil
	case int32:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case uint8:
		return float64(n), nil
	case uint16:
		return float64(n), nil
	case uint32:
		return float64(n), nil
	case uint64:
		return float64(n), nil
	default:
		return 0, fmt.Errorf("cannot convert %T to float64", v)
	}
}

// String returns the value as a string.
func (tv *TagValue) String() string {
	if tv == nil || tv.Error != nil {
		return ""
	}
	v := tv.GoValue()
	switch s := v.(type) {
	case string:
		return s
	default:
		return fmt.Sprintf("%v", v)
	}
}

// TagInfo holds metadata about a tag.
type TagInfo struct {
	Name       string   // Tag name or address
	TypeCode   uint16   // Data type
	Instance   uint32   // For CIP symbol browsing
	Dimensions []uint32 // Array dimensions
}

// DeviceInfo holds information about the connected PLC.
type DeviceInfo struct {
	Model        string
	Version      string
	CPUType      string
	SerialNumber uint32
	ProductCode  uint16
	VendorID     uint16
}

// TagRequest represents a request to read a tag with optional type hint.
type TagRequest struct {
	Address  string // Tag name or FINS address
	TypeHint string // Optional type hint (e.g., "DINT", "REAL")
}
