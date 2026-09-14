package ads

import (
	"fmt"
	"reflect"

	"github.com/yatesdr/plcio/metadata"
)

// ADS primitive codes and legacy semantic helper codes. TIME/date/long-time
// helpers do not replace the native ADS storage code carried by published
// symbols; connected I/O resolves their declared type names. Arrays use the
// library's TypeArrayFlag. ADS value storage is little endian.
const (
	TypeVoid      uint16 = 0x00
	TypeBool      uint16 = 0x21 // BOOL (1 byte)
	TypeByte      uint16 = 0x11 // BYTE/USINT (1 byte unsigned)
	TypeSByte     uint16 = 0x10 // SINT (1 byte signed)
	TypeWord      uint16 = 0x12 // WORD/UINT (2 bytes unsigned)
	TypeInt16     uint16 = 0x02 // INT (2 bytes signed)
	TypeDWord     uint16 = 0x13 // DWORD/UDINT (4 bytes unsigned)
	TypeInt32     uint16 = 0x03 // DINT (4 bytes signed)
	TypeLWord     uint16 = 0x15 // LWORD/ULINT (8 bytes unsigned)
	TypeInt64     uint16 = 0x14 // LINT (8 bytes signed)
	TypeReal      uint16 = 0x04 // REAL (4 bytes float)
	TypeLReal     uint16 = 0x05 // LREAL (8 bytes double)
	TypeString    uint16 = 0x1E // STRING
	TypeWString   uint16 = 0x1F // WSTRING
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
	case TypeDWord, TypeInt32, TypeReal, TypeTime, TypeDate, TypeTimeOfDay, TypeDateTime:
		return 4
	case TypeLWord, TypeInt64, TypeLReal, TypeLTime:
		return 8
	default:
		return 0 // Variable or unknown
	}
}

// primitiveSchemas are immutable native definitions shared by stateless helpers.
var primitiveSchemas = func() map[uint16]*schemaType {
	result := make(map[uint16]*schemaType)
	resolver := newResolver(nil, defaultOptions())
	for _, code := range []uint16{TypeBool, TypeByte, TypeSByte, TypeWord, TypeInt16, TypeDWord, TypeInt32, TypeLWord, TypeInt64, TypeReal, TypeLReal, TypeTime, TypeLTime, TypeDate, TypeTimeOfDay, TypeDateTime, TypeString, TypeWString} {
		result[code] = resolver.resolveName(TypeName(code), 1)
	}
	return result
}()

// EncodeValueWithType encodes checked native scalar or flat array values.
// It has no target declaration: string arrays use a uniform capacity large
// enough for their input. Client.Write instead uses the published target size,
// dimensions, encoding and access restrictions. Exact raw []byte storage remains
// an advanced input; fixed-width storage must contain complete elements.
func EncodeValueWithType(value interface{}, typeCode uint16) ([]byte, error) {
	cfg := defaultOptions()
	base := BaseType(typeCode)
	if data, ok := value.([]byte); ok {
		width := TypeSize(base)
		if uint64(len(data)) > uint64(cfg.maxPayload) || (width != 0 && len(data)%width != 0) {
			return nil, fmt.Errorf("invalid raw storage length for %s", TypeName(base))
		}
		return append([]byte(nil), data...), nil
	}
	schema := primitiveSchemas[base]
	if schema == nil {
		return nil, fmt.Errorf("unsupported type %s", TypeName(base))
	}
	values := reflect.ValueOf(value)
	if values.IsValid() && (values.Kind() == reflect.Slice || values.Kind() == reflect.Array) {
		count := values.Len()
		if uint64(count) > uint64(cfg.maxElements) {
			return nil, fmt.Errorf("array element limit exceeded")
		}
		if count == 0 {
			return []byte{}, nil
		}
		element := *schema
		if schema.kind == metadata.KindString {
			element.size = 0
			for i := 0; i < count; i++ {
				text, ok := values.Index(i).Interface().(string)
				if !ok {
					return nil, fmt.Errorf("string array element %d has type %T", i, values.Index(i).Interface())
				}
				encoded, err := encodeText(text, schema.wide, false)
				if err != nil {
					return nil, err
				}
				if uint64(len(encoded)) > element.size {
					element.size = uint64(len(encoded))
				}
			}
		}
		if element.size == 0 || uint64(count) > uint64(cfg.maxPayload)/element.size {
			return nil, fmt.Errorf("array payload limit exceeded")
		}
		data := make([]byte, int(element.size)*count)
		for i := 0; i < count; i++ {
			start := i * int(element.size)
			if err := encodeInto(&element, values.Index(i).Interface(), data[start:start+int(element.size)], cfg.deadline); err != nil {
				return nil, fmt.Errorf("element %d: %w", i, err)
			}
		}
		return data, nil
	}
	if IsArray(typeCode) {
		return nil, fmt.Errorf("array input required")
	}
	if schema.kind == metadata.KindString {
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("string input required, got %T", value)
		}
		data, err := encodeText(text, schema.wide, false)
		if uint64(len(data)) > uint64(cfg.maxPayload) {
			return nil, fmt.Errorf("string payload limit exceeded")
		}
		return data, err
	}
	data := make([]byte, int(schema.size))
	if err := encodeInto(schema, value, data, cfg.deadline); err != nil {
		return nil, err
	}
	return data, nil
}
