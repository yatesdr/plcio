// Package s7 provides Siemens S7 PLC communication using the S7 protocol.
package s7

import (
	"fmt"
	"strings"
)

// S7 data type codes.
// These match Siemens S7 type identifiers.
const (
	TypeBool    uint16 = 0x0001 // 1 bit (stored as 1 byte)
	TypeByte    uint16 = 0x0002 // 8 bits unsigned
	TypeUSInt   uint16 = 0x0002 // 8 bits unsigned (alias for BYTE)
	TypeChar    uint16 = 0x0003 // 8 bits character
	TypeSInt    uint16 = 0x0004 // 8 bits signed
	TypeWord    uint16 = 0x0005 // 16 bits unsigned
	TypeUInt    uint16 = 0x0005 // 16 bits unsigned (alias for WORD)
	TypeInt     uint16 = 0x0006 // 16 bits signed
	TypeDWord   uint16 = 0x0007 // 32 bits unsigned
	TypeUDInt   uint16 = 0x0007 // 32 bits unsigned (alias for DWORD)
	TypeDInt    uint16 = 0x0008 // 32 bits signed
	TypeReal    uint16 = 0x0009 // 32 bits IEEE 754 float
	TypeDate    uint16 = 0x000A // 16 bits (days since 1990-01-01)
	TypeTime    uint16 = 0x000B // 32 bits (milliseconds)
	TypeTimeOfDay uint16 = 0x000C // 32 bits (milliseconds since midnight)
	TypeLWord   uint16 = 0x0010 // 64 bits unsigned (S7-1500)
	TypeULInt   uint16 = 0x0010 // 64 bits unsigned (alias for LWORD)
	TypeLInt    uint16 = 0x0011 // 64 bits signed (S7-1500)
	TypeLReal   uint16 = 0x0012 // 64 bits IEEE 754 double (S7-1500)
	TypeWChar   uint16 = 0x0013 // 16 bits wide character
	TypeString  uint16 = 0x0014 // S7 String (max 254 chars)
	TypeWString uint16 = 0x0015 // Wide string (S7-1500)

	// Array flag - when set, indicates an array of the base type
	TypeArrayFlag uint16 = 0x1000
)

// TypeSize returns the byte size of a single element of the data type.
// Returns 0 for variable-length or unknown types.
func TypeSize(dataType uint16) int {
	// Mask off array flag to get base type
	baseType := dataType & 0x0FFF

	switch baseType {
	case TypeBool:
		return 1 // Stored as 1 byte
	case TypeByte, TypeChar, TypeSInt: // TypeUSInt == TypeByte
		return 1
	case TypeWord, TypeInt, TypeDate, TypeWChar: // TypeUInt == TypeWord
		return 2
	case TypeDWord, TypeDInt, TypeReal, TypeTime, TypeTimeOfDay: // TypeUDInt == TypeDWord
		return 4
	case TypeLWord, TypeLInt, TypeLReal: // TypeULInt == TypeLWord
		return 8
	case TypeString, TypeWString:
		return 0 // Variable length
	default:
		return 0
	}
}

// IsArray returns true if the type code represents an array.
func IsArray(dataType uint16) bool {
	return (dataType & TypeArrayFlag) != 0
}

// BaseType returns the base type without the array flag.
func BaseType(dataType uint16) uint16 {
	return dataType & 0x0FFF
}

// MakeArrayType adds the array flag to a base type.
func MakeArrayType(baseType uint16) uint16 {
	return baseType | TypeArrayFlag
}

// TypeName returns a human-readable name for the data type.
func TypeName(dataType uint16) string {
	isArray := IsArray(dataType)
	baseType := BaseType(dataType)

	var name string
	switch baseType {
	case TypeBool:
		name = "BOOL"
	case TypeByte:
		name = "BYTE"
	case TypeChar:
		name = "CHAR"
	case TypeSInt:
		name = "SINT"
	case TypeWord:
		name = "WORD"
	case TypeInt:
		name = "INT"
	case TypeDWord:
		name = "DWORD"
	case TypeDInt:
		name = "DINT"
	case TypeReal:
		name = "REAL"
	case TypeDate:
		name = "DATE"
	case TypeTime:
		name = "TIME"
	case TypeTimeOfDay:
		name = "TIME_OF_DAY"
	case TypeLWord:
		name = "LWORD"
	case TypeLInt:
		name = "LINT"
	case TypeLReal:
		name = "LREAL"
	case TypeWChar:
		name = "WCHAR"
	case TypeString:
		name = "STRING"
	case TypeWString:
		name = "WSTRING"
	default:
		name = fmt.Sprintf("UNKNOWN(0x%04X)", baseType)
	}

	if isArray {
		return name + "[]"
	}
	return name
}

// TypeCodeFromName returns the type code for a given type name.
// Returns (typeCode, true) if found, (0, false) otherwise.
func TypeCodeFromName(name string) (uint16, bool) {
	upper := strings.ToUpper(strings.TrimSpace(name))

	// Check for array suffix
	isArray := strings.HasSuffix(upper, "[]")
	if isArray {
		upper = strings.TrimSuffix(upper, "[]")
	}

	var typeCode uint16
	var ok bool

	switch upper {
	case "BOOL":
		typeCode, ok = TypeBool, true
	case "BYTE", "USINT":
		typeCode, ok = TypeByte, true
	case "CHAR":
		typeCode, ok = TypeChar, true
	case "SINT":
		typeCode, ok = TypeSInt, true
	case "WORD", "UINT":
		typeCode, ok = TypeWord, true
	case "INT":
		typeCode, ok = TypeInt, true
	case "DWORD", "UDINT":
		typeCode, ok = TypeDWord, true
	case "DINT":
		typeCode, ok = TypeDInt, true
	case "REAL":
		typeCode, ok = TypeReal, true
	case "DATE":
		typeCode, ok = TypeDate, true
	case "TIME":
		typeCode, ok = TypeTime, true
	case "TIME_OF_DAY", "TOD":
		typeCode, ok = TypeTimeOfDay, true
	case "LWORD", "ULINT":
		typeCode, ok = TypeLWord, true
	case "LINT":
		typeCode, ok = TypeLInt, true
	case "LREAL":
		typeCode, ok = TypeLReal, true
	case "WCHAR":
		typeCode, ok = TypeWChar, true
	case "STRING":
		typeCode, ok = TypeString, true
	case "WSTRING":
		typeCode, ok = TypeWString, true
	default:
		return 0, false
	}

	if isArray && ok {
		typeCode = MakeArrayType(typeCode)
	}

	return typeCode, ok
}

// SupportedTypeNames returns a list of supported type names for manual tag entry.
func SupportedTypeNames() []string {
	return []string{
		"BOOL", "BYTE", "CHAR", "SINT", "USINT",
		"WORD", "INT", "UINT",
		"DWORD", "DINT", "UDINT", "REAL", "TIME",
		"LWORD", "LINT", "ULINT", "LREAL",
		"STRING", "WSTRING",
	}
}
