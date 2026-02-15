package ads

import (
	"encoding/binary"
	"fmt"
	"math"
)

// ADS/TwinCAT data type codes
// These match the internal TwinCAT type system.
// ADS uses little-endian byte order (native x86 format).
const (
	TypeVoid   uint16 = 0x00
	TypeBool   uint16 = 0x21 // BOOL (1 byte)
	TypeByte   uint16 = 0x11 // BYTE/USINT (1 byte unsigned)
	TypeSByte  uint16 = 0x10 // SINT (1 byte signed)
	TypeWord   uint16 = 0x12 // WORD/UINT (2 bytes unsigned)
	TypeInt16  uint16 = 0x02 // INT (2 bytes signed)
	TypeDWord  uint16 = 0x13 // DWORD/UDINT (4 bytes unsigned)
	TypeInt32  uint16 = 0x03 // DINT (4 bytes signed)
	TypeLWord  uint16 = 0x15 // LWORD/ULINT (8 bytes unsigned)
	TypeInt64  uint16 = 0x14 // LINT (8 bytes signed)
	TypeReal   uint16 = 0x04 // REAL (4 bytes float)
	TypeLReal  uint16 = 0x05 // LREAL (8 bytes double)
	TypeString uint16 = 0x1E // STRING
	TypeWString uint16 = 0x1F // WSTRING
	TypeTime      uint16 = 0x30 // TIME (32-bit, milliseconds)
	TypeLTime     uint16 = 0x16 // LTIME (64-bit, nanoseconds)
	TypeDate      uint16 = 0x31 // DATE
	TypeTimeOfDay uint16 = 0x32 // TIME_OF_DAY
	TypeDateTime  uint16 = 0x33 // DATE_AND_TIME

	// Pseudo-types for internal use
	TypeUnknown uint16 = 0xFFFF

	// Array flag - high bit indicates array type
	TypeArrayFlag uint16 = 0x8000
)

// IsArray returns true if the type code represents an array.
func IsArray(typeCode uint16) bool {
	return (typeCode & TypeArrayFlag) != 0
}

// BaseType returns the base type code with array flag removed.
func BaseType(typeCode uint16) uint16 {
	return typeCode &^ TypeArrayFlag
}

// TypeName returns the human-readable name for an ADS data type.
func TypeName(typeCode uint16) string {
	switch typeCode {
	case TypeVoid:
		return "VOID"
	case TypeBool:
		return "BOOL"
	case TypeByte:
		return "BYTE"
	case TypeSByte:
		return "SINT"
	case TypeWord:
		return "WORD"
	case TypeInt16:
		return "INT"
	case TypeDWord:
		return "DWORD"
	case TypeInt32:
		return "DINT"
	case TypeLWord:
		return "LWORD"
	case TypeInt64:
		return "LINT"
	case TypeReal:
		return "REAL"
	case TypeLReal:
		return "LREAL"
	case TypeString:
		return "STRING"
	case TypeWString:
		return "WSTRING"
	case TypeTime:
		return "TIME"
	case TypeLTime:
		return "LTIME"
	case TypeDate:
		return "DATE"
	case TypeTimeOfDay:
		return "TIME_OF_DAY"
	case TypeDateTime:
		return "DATE_AND_TIME"
	default:
		return fmt.Sprintf("TYPE_%04X", typeCode)
	}
}

// TypeCodeFromName returns the type code for a type name.
// Returns TypeUnknown if not recognized.
func TypeCodeFromName(name string) (uint16, bool) {
	switch name {
	case "VOID":
		return TypeVoid, true
	case "BOOL":
		return TypeBool, true
	case "BYTE", "USINT":
		return TypeByte, true
	case "SINT":
		return TypeSByte, true
	case "WORD", "UINT":
		return TypeWord, true
	case "INT":
		return TypeInt16, true
	case "DWORD", "UDINT":
		return TypeDWord, true
	case "DINT":
		return TypeInt32, true
	case "LWORD", "ULINT":
		return TypeLWord, true
	case "LINT":
		return TypeInt64, true
	case "REAL":
		return TypeReal, true
	case "LREAL":
		return TypeLReal, true
	case "STRING":
		return TypeString, true
	case "WSTRING":
		return TypeWString, true
	case "TIME":
		return TypeTime, true
	case "LTIME":
		return TypeLTime, true
	case "DATE":
		return TypeDate, true
	case "TIME_OF_DAY", "TOD":
		return TypeTimeOfDay, true
	case "DATE_AND_TIME", "DT":
		return TypeDateTime, true
	default:
		return TypeUnknown, false
	}
}

// SupportedTypeNames returns the list of supported data type names for Beckhoff/TwinCAT PLCs.
func SupportedTypeNames() []string {
	return []string{
		"BOOL", "BYTE", "SINT",
		"WORD", "INT", "UINT",
		"DWORD", "DINT", "UDINT", "REAL", "TIME",
		"LWORD", "LINT", "ULINT", "LREAL", "LTIME",
		"STRING", "WSTRING",
	}
}

// TypeSize returns the byte size for a primitive type code.
// Returns 0 for variable-length or unknown types.
func TypeSize(typeCode uint16) int {
	switch typeCode {
	case TypeBool, TypeByte, TypeSByte:
		return 1
	case TypeWord, TypeInt16:
		return 2
	case TypeDWord, TypeInt32, TypeReal, TypeTime, TypeDate, TypeTimeOfDay:
		return 4
	case TypeLWord, TypeInt64, TypeLReal, TypeDateTime, TypeLTime:
		return 8
	default:
		return 0 // Variable or unknown
	}
}

// EncodeValueWithType encodes a value using a specific type code.
// This is used when we know the target type from symbol info.
func EncodeValueWithType(value interface{}, typeCode uint16) ([]byte, error) {
	// Handle array types - check both the typeCode flag and the value type
	// TwinCAT often reports arrays with just the base type (no array flag)
	baseType := typeCode
	if IsArray(typeCode) {
		baseType = BaseType(typeCode)
	}

	// Check if value is a slice - if so, encode as array regardless of typeCode flag
	if isSliceValue(value) {
		return encodeArrayValue(value, baseType)
	}

	size := TypeSize(typeCode)
	if size == 0 && typeCode != TypeString && typeCode != TypeWString {
		return nil, fmt.Errorf("cannot encode to type %s", TypeName(typeCode))
	}

	switch typeCode {
	case TypeBool:
		var b byte
		switch v := value.(type) {
		case bool:
			if v {
				b = 1
			}
		case int:
			if v != 0 {
				b = 1
			}
		case int32:
			if v != 0 {
				b = 1
			}
		case int64:
			if v != 0 {
				b = 1
			}
		case float64:
			if v != 0 {
				b = 1
			}
		default:
			return nil, fmt.Errorf("cannot convert %T to BOOL", value)
		}
		return []byte{b}, nil

	case TypeByte:
		switch v := value.(type) {
		case uint8:
			return []byte{v}, nil
		case int:
			return []byte{byte(v)}, nil
		case int32:
			return []byte{byte(v)}, nil
		case int64:
			return []byte{byte(v)}, nil
		case float64:
			return []byte{byte(int64(v))}, nil
		default:
			return nil, fmt.Errorf("cannot convert %T to BYTE", value)
		}

	case TypeSByte:
		switch v := value.(type) {
		case int8:
			return []byte{byte(v)}, nil
		case int:
			return []byte{byte(v)}, nil
		case int32:
			return []byte{byte(v)}, nil
		case int64:
			return []byte{byte(v)}, nil
		case float64:
			return []byte{byte(int8(v))}, nil
		default:
			return nil, fmt.Errorf("cannot convert %T to SINT", value)
		}

	case TypeWord:
		buf := make([]byte, 2)
		switch v := value.(type) {
		case uint16:
			binary.LittleEndian.PutUint16(buf, v)
		case int:
			binary.LittleEndian.PutUint16(buf, uint16(v))
		case int32:
			binary.LittleEndian.PutUint16(buf, uint16(v))
		case int64:
			binary.LittleEndian.PutUint16(buf, uint16(v))
		case float64:
			binary.LittleEndian.PutUint16(buf, uint16(int64(v)))
		default:
			return nil, fmt.Errorf("cannot convert %T to WORD", value)
		}
		return buf, nil

	case TypeInt16:
		buf := make([]byte, 2)
		switch v := value.(type) {
		case int16:
			binary.LittleEndian.PutUint16(buf, uint16(v))
		case int:
			binary.LittleEndian.PutUint16(buf, uint16(v))
		case int32:
			binary.LittleEndian.PutUint16(buf, uint16(v))
		case int64:
			binary.LittleEndian.PutUint16(buf, uint16(v))
		case float64:
			binary.LittleEndian.PutUint16(buf, uint16(int16(v)))
		default:
			return nil, fmt.Errorf("cannot convert %T to INT", value)
		}
		return buf, nil

	case TypeDWord:
		buf := make([]byte, 4)
		switch v := value.(type) {
		case uint32:
			binary.LittleEndian.PutUint32(buf, v)
		case int:
			binary.LittleEndian.PutUint32(buf, uint32(v))
		case int32:
			binary.LittleEndian.PutUint32(buf, uint32(v))
		case int64:
			binary.LittleEndian.PutUint32(buf, uint32(v))
		case float64:
			binary.LittleEndian.PutUint32(buf, uint32(int64(v)))
		default:
			return nil, fmt.Errorf("cannot convert %T to DWORD", value)
		}
		return buf, nil

	case TypeInt32:
		buf := make([]byte, 4)
		switch v := value.(type) {
		case int32:
			binary.LittleEndian.PutUint32(buf, uint32(v))
		case int:
			binary.LittleEndian.PutUint32(buf, uint32(v))
		case int64:
			binary.LittleEndian.PutUint32(buf, uint32(v))
		case float64:
			binary.LittleEndian.PutUint32(buf, uint32(int32(v)))
		default:
			return nil, fmt.Errorf("cannot convert %T to DINT", value)
		}
		return buf, nil

	case TypeLWord:
		buf := make([]byte, 8)
		switch v := value.(type) {
		case uint64:
			binary.LittleEndian.PutUint64(buf, v)
		case int:
			binary.LittleEndian.PutUint64(buf, uint64(v))
		case int32:
			binary.LittleEndian.PutUint64(buf, uint64(v))
		case int64:
			binary.LittleEndian.PutUint64(buf, uint64(v))
		case float64:
			binary.LittleEndian.PutUint64(buf, uint64(int64(v)))
		default:
			return nil, fmt.Errorf("cannot convert %T to LWORD", value)
		}
		return buf, nil

	case TypeInt64:
		buf := make([]byte, 8)
		switch v := value.(type) {
		case int64:
			binary.LittleEndian.PutUint64(buf, uint64(v))
		case int:
			binary.LittleEndian.PutUint64(buf, uint64(v))
		case int32:
			binary.LittleEndian.PutUint64(buf, uint64(v))
		case float64:
			binary.LittleEndian.PutUint64(buf, uint64(int64(v)))
		default:
			return nil, fmt.Errorf("cannot convert %T to LINT", value)
		}
		return buf, nil

	case TypeReal:
		buf := make([]byte, 4)
		switch v := value.(type) {
		case float32:
			binary.LittleEndian.PutUint32(buf, math.Float32bits(v))
		case float64:
			binary.LittleEndian.PutUint32(buf, math.Float32bits(float32(v)))
		case int:
			binary.LittleEndian.PutUint32(buf, math.Float32bits(float32(v)))
		case int32:
			binary.LittleEndian.PutUint32(buf, math.Float32bits(float32(v)))
		default:
			return nil, fmt.Errorf("cannot convert %T to REAL", value)
		}
		return buf, nil

	case TypeLReal:
		buf := make([]byte, 8)
		switch v := value.(type) {
		case float64:
			binary.LittleEndian.PutUint64(buf, math.Float64bits(v))
		case float32:
			binary.LittleEndian.PutUint64(buf, math.Float64bits(float64(v)))
		case int:
			binary.LittleEndian.PutUint64(buf, math.Float64bits(float64(v)))
		case int32:
			binary.LittleEndian.PutUint64(buf, math.Float64bits(float64(v)))
		default:
			return nil, fmt.Errorf("cannot convert %T to LREAL", value)
		}
		return buf, nil

	case TypeLTime:
		// LTIME is 64-bit nanoseconds
		buf := make([]byte, 8)
		switch v := value.(type) {
		case int64:
			binary.LittleEndian.PutUint64(buf, uint64(v))
		case int:
			binary.LittleEndian.PutUint64(buf, uint64(v))
		case int32:
			binary.LittleEndian.PutUint64(buf, uint64(v))
		case uint64:
			binary.LittleEndian.PutUint64(buf, v)
		default:
			return nil, fmt.Errorf("cannot convert %T to LTIME", value)
		}
		return buf, nil

	case TypeTime:
		// TIME is 32-bit milliseconds
		buf := make([]byte, 4)
		switch v := value.(type) {
		case int32:
			binary.LittleEndian.PutUint32(buf, uint32(v))
		case int:
			binary.LittleEndian.PutUint32(buf, uint32(v))
		case int64:
			binary.LittleEndian.PutUint32(buf, uint32(v))
		case uint32:
			binary.LittleEndian.PutUint32(buf, v)
		default:
			return nil, fmt.Errorf("cannot convert %T to TIME", value)
		}
		return buf, nil

	case TypeDate, TypeTimeOfDay, TypeDateTime:
		// DATE, TOD, DT are all 32-bit values
		buf := make([]byte, 4)
		switch v := value.(type) {
		case int32:
			binary.LittleEndian.PutUint32(buf, uint32(v))
		case int:
			binary.LittleEndian.PutUint32(buf, uint32(v))
		case int64:
			binary.LittleEndian.PutUint32(buf, uint32(v))
		case uint32:
			binary.LittleEndian.PutUint32(buf, v)
		default:
			return nil, fmt.Errorf("cannot convert %T to %s", value, TypeName(typeCode))
		}
		return buf, nil

	case TypeString:
		switch v := value.(type) {
		case string:
			return append([]byte(v), 0), nil
		case []byte:
			return append(v, 0), nil
		default:
			return nil, fmt.Errorf("cannot convert %T to STRING", value)
		}

	case TypeWString:
		// WSTRING is UTF-16LE encoded with 2-byte null terminator
		switch v := value.(type) {
		case string:
			buf := make([]byte, len(v)*2+2) // Each char becomes 2 bytes + null terminator
			for i, r := range v {
				buf[i*2] = byte(r)
				buf[i*2+1] = byte(r >> 8)
			}
			// Last 2 bytes are already zero from make()
			return buf, nil
		default:
			return nil, fmt.Errorf("cannot convert %T to WSTRING", value)
		}

	default:
		return nil, fmt.Errorf("unsupported type code: %s", TypeName(typeCode))
	}
}

// isSliceValue returns true if the value is a slice type that should be encoded as an array.
func isSliceValue(value interface{}) bool {
	switch value.(type) {
	case []int32, []int64, []float32, []float64, []bool, []string, []byte:
		return true
	default:
		return false
	}
}

// encodeArrayValue encodes a slice of values for the given base type.
func encodeArrayValue(value interface{}, baseType uint16) ([]byte, error) {
	var result []byte

	switch v := value.(type) {
	case []int32:
		for _, elem := range v {
			encoded, err := EncodeValueWithType(elem, baseType)
			if err != nil {
				return nil, err
			}
			result = append(result, encoded...)
		}
	case []int64:
		for _, elem := range v {
			encoded, err := EncodeValueWithType(elem, baseType)
			if err != nil {
				return nil, err
			}
			result = append(result, encoded...)
		}
	case []float32:
		for _, elem := range v {
			encoded, err := EncodeValueWithType(elem, baseType)
			if err != nil {
				return nil, err
			}
			result = append(result, encoded...)
		}
	case []float64:
		for _, elem := range v {
			encoded, err := EncodeValueWithType(elem, baseType)
			if err != nil {
				return nil, err
			}
			result = append(result, encoded...)
		}
	case []bool:
		for _, elem := range v {
			encoded, err := EncodeValueWithType(elem, baseType)
			if err != nil {
				return nil, err
			}
			result = append(result, encoded...)
		}
	case []string:
		for _, elem := range v {
			encoded, err := EncodeValueWithType(elem, baseType)
			if err != nil {
				return nil, err
			}
			result = append(result, encoded...)
		}
	case []byte:
		// Already in byte form, just return as-is
		return v, nil
	default:
		return nil, fmt.Errorf("cannot convert %T to array of %s", value, TypeName(baseType))
	}

	return result, nil
}
