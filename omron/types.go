// Package omron provides unified Omron PLC communication.
// Supports FINS/UDP, FINS/TCP, and EIP/CIP protocols.
package omron

import (
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
)

// Transport represents the communication protocol.
type Transport string

const (
	TransportFINS    Transport = "fins"     // FINS protocol (tries TCP first, falls back to UDP)
	TransportFINSUDP Transport = "fins-udp" // FINS over UDP (internal use)
	TransportFINSTCP Transport = "fins-tcp" // FINS over TCP (internal use)
	TransportEIP     Transport = "eip"      // EtherNet/IP CIP (for NJ/NX series)
)

// Memory area codes for FINS protocol.
const (
	// Bit areas (for bit-level access)
	AreaCIOBit  byte = 0x30 // CIO area; bit
	AreaWRBit   byte = 0x31 // Work area; bit
	AreaHRBit   byte = 0x32 // Holding area; bit
	AreaARBit   byte = 0x33 // Auxiliary area; bit
	AreaDMBit   byte = 0x02 // Data memory area; bit
	AreaTaskBit byte = 0x06 // Task flags; bit

	// Word areas (for word-level access)
	AreaCIOWord byte = 0xB0 // CIO area; word
	AreaWRWord  byte = 0xB1 // Work area; word
	AreaHRWord  byte = 0xB2 // Holding area; word
	AreaARWord  byte = 0xB3 // Auxiliary area; word
	AreaDMWord  byte = 0x82 // Data memory area; word

	// Timer/Counter areas
	AreaTimerCounterPV byte = 0x89 // Timer/Counter PV

	// Extended Memory (EM) bank areas
	AreaEM0Word byte = 0xA0 // EM bank 0; word
	AreaEM1Word byte = 0xA1 // EM bank 1; word
	AreaEM2Word byte = 0xA2 // EM bank 2; word
	AreaEM3Word byte = 0xA3 // EM bank 3; word
	AreaEM4Word byte = 0xA4 // EM bank 4; word
	AreaEM5Word byte = 0xA5 // EM bank 5; word
	AreaEM6Word byte = 0xA6 // EM bank 6; word
	AreaEM7Word byte = 0xA7 // EM bank 7; word
	AreaEM8Word byte = 0xA8 // EM bank 8; word
	AreaEM9Word byte = 0xA9 // EM bank 9; word
	AreaEMAWord byte = 0xAA // EM bank A; word
	AreaEMBWord byte = 0xAB // EM bank B; word
	AreaEMCWord byte = 0xAC // EM bank C; word
	AreaEMCurr  byte = 0x98 // EM current bank; word
)

// Data type codes for Omron.
const (
	TypeVoid   uint16 = 0x00
	TypeBool   uint16 = 0x01 // BOOL (1 bit, read as word)
	TypeByte   uint16 = 0x02 // BYTE/USINT (1 byte)
	TypeSByte  uint16 = 0x03 // SINT (1 byte signed)
	TypeWord   uint16 = 0x04 // WORD/UINT (2 bytes unsigned)
	TypeInt16  uint16 = 0x05 // INT (2 bytes signed)
	TypeDWord  uint16 = 0x06 // DWORD/UDINT (4 bytes unsigned)
	TypeInt32  uint16 = 0x07 // DINT (4 bytes signed)
	TypeLWord  uint16 = 0x08 // LWORD/ULINT (8 bytes unsigned)
	TypeInt64  uint16 = 0x09 // LINT (8 bytes signed)
	TypeReal   uint16 = 0x0A // REAL (4 bytes float)
	TypeLReal  uint16 = 0x0B // LREAL (8 bytes double)
	TypeString uint16 = 0x0C // STRING

	// CIP-specific types (for EIP transport)
	// These are standard CIP type codes used by both Rockwell and Omron
	TypeCIPBool   uint16 = 0xC1 // CIP BOOL
	TypeCIPSINT   uint16 = 0xC2 // CIP SINT (1 byte signed)
	TypeCIPINT    uint16 = 0xC3 // CIP INT (2 bytes signed)
	TypeCIPDINT   uint16 = 0xC4 // CIP DINT (4 bytes signed)
	TypeCIPLINT   uint16 = 0xC5 // CIP LINT (8 bytes signed)
	TypeCIPUSINT  uint16 = 0xC6 // CIP USINT (1 byte unsigned)
	TypeCIPUINT   uint16 = 0xC7 // CIP UINT (2 bytes unsigned)
	TypeCIPUDINT  uint16 = 0xC8 // CIP UDINT (4 bytes unsigned)
	TypeCIPULINT  uint16 = 0xC9 // CIP ULINT (8 bytes unsigned)
	TypeCIPREAL   uint16 = 0xCA // CIP REAL (4 bytes float)
	TypeCIPLREAL  uint16 = 0xCB // CIP LREAL (8 bytes double)
	TypeCIPSTRING uint16 = 0xD0 // CIP STRING (Omron: 16-bit LE length prefix)

	// NJ/NX bit-string and TIME codes, W506 section 7-7-1 "Data Type Codes"
	// ("CIP Common" group).
	TypeOmronByte  uint16 = 0xD1 // BYTE (1-byte hexadecimal)
	TypeOmronWord  uint16 = 0xD2 // WORD (1-word hexadecimal)
	TypeOmronDWord uint16 = 0xD3 // DWORD (2-word hexadecimal)
	TypeOmronLWord uint16 = 0xD4 // LWORD (4-word hexadecimal)
	TypeOmronTime  uint16 = 0xDB // TIME (8-byte data): signed 64-bit nanoseconds

	// NJ/NX "Vendor Specific" data type codes (W506 section 7-7-1). On the
	// wire they are the single bytes 0x04..0x0C, which collide with this
	// package's FINS pseudo-type codes (TypeWord..TypeString), so a CIP reply
	// carrying one is mapped to 0x0100|code (see cipTypeFromWire) and mapped
	// back when writing. Sizes are from W506 appendix A (8 bytes for the
	// time types, 4 for enumerations); value ranges from W501 section 6-3:
	//   TIME            signed ns (T#-106751d_23h47m16s854.775808ms ..)
	//   DATE            ns since 1970-01-01 00:00 (D#1970-01-01..D#2106-02-06)
	//   TIME_OF_DAY     ns since midnight (TOD#00:00:00..23:59:59.999999999)
	//   DATE_AND_TIME   ns since 1970-01-01 00:00:00 (..DT#2106-02-06-23:59:59.999999999)
	// The controller clock has no time zone: DATE/DATE_AND_TIME count the
	// controller's local wall clock and are decoded as a time.Time in UTC
	// carrying that wall clock. The nanosecond encoding matches aphyt
	// (github.com/aphyt/aphytcomm, omron/omron_datatypes.py).
	TypeOmronUINTBCD  uint16 = 0x0104 // UINT BCD (wire 0x04, 2 bytes, returned raw)
	TypeOmronUDINTBCD uint16 = 0x0105 // UDINT BCD (wire 0x05, 4 bytes, returned raw)
	TypeOmronULINTBCD uint16 = 0x0106 // ULINT BCD (wire 0x06, 8 bytes, returned raw)
	TypeOmronEnum     uint16 = 0x0107 // enumeration (wire 0x07, DINT-range, 4 bytes)
	TypeOmronDate     uint16 = 0x0108 // DATE (wire 0x08 DATE_NSEC)
	TypeOmronTimeNSec uint16 = 0x0109 // TIME (wire 0x09 TIME_NSEC)
	TypeOmronDT       uint16 = 0x010A // DATE_AND_TIME (wire 0x0A DATE_AND_TIME_NSEC)
	TypeOmronTOD      uint16 = 0x010B // TIME_OF_DAY (wire 0x0B TIME_OF_DAY_NSEC)
	TypeOmronUnion    uint16 = 0x010C // union (wire 0x0C, returned raw)

	// omronVendorTypeBase is OR-ed onto an NJ/NX vendor-specific wire code.
	omronVendorTypeBase uint16 = 0x0100

	// Structure/UDT type indicator (high byte = 0x02 indicates struct)
	TypeStructFlag uint16 = 0x0200

	// Pseudo-types
	TypeUnknown uint16 = 0xFFFF

	// Array flag - high bit indicates array type
	TypeArrayFlag uint16 = 0x8000
)

// IsArray returns true if the type code represents an array.
func IsArray(typeCode uint16) bool {
	return (typeCode & TypeArrayFlag) != 0
}

// MakeArrayType returns the array version of a base type.
func MakeArrayType(baseType uint16) uint16 {
	return baseType | TypeArrayFlag
}

// BaseType returns the base type code with array flag removed.
func BaseType(typeCode uint16) uint16 {
	return typeCode &^ TypeArrayFlag
}

// TypeName returns the human-readable name for a data type.
func TypeName(typeCode uint16) string {
	baseType := BaseType(typeCode)
	isArr := IsArray(typeCode)

	// Check for structure type (high byte = 0x02)
	if (baseType & TypeStructFlag) == TypeStructFlag {
		structID := baseType &^ TypeStructFlag
		name := fmt.Sprintf("STRUCT_%02X", structID)
		if isArr {
			return name + "[]"
		}
		return name
	}

	var name string
	switch baseType {
	case TypeVoid:
		name = "VOID"
	case TypeBool, TypeCIPBool:
		name = "BOOL"
	case TypeByte, TypeCIPUSINT, TypeOmronByte:
		name = "BYTE"
	case TypeSByte, TypeCIPSINT:
		name = "SINT"
	case TypeWord, TypeCIPUINT, TypeOmronWord:
		name = "WORD"
	case TypeInt16, TypeCIPINT:
		name = "INT"
	case TypeDWord, TypeCIPUDINT, TypeOmronDWord:
		name = "DWORD"
	case TypeInt32, TypeCIPDINT:
		name = "DINT"
	case TypeLWord, TypeCIPULINT, TypeOmronLWord:
		name = "LWORD"
	case TypeInt64, TypeCIPLINT:
		name = "LINT"
	case TypeReal, TypeCIPREAL:
		name = "REAL"
	case TypeLReal, TypeCIPLREAL:
		name = "LREAL"
	case TypeString, TypeCIPSTRING:
		name = "STRING"
	case TypeOmronTime, TypeOmronTimeNSec:
		name = "TIME"
	case TypeOmronDate:
		name = "DATE"
	case TypeOmronTOD:
		name = "TIME_OF_DAY"
	case TypeOmronDT:
		name = "DATE_AND_TIME"
	case TypeOmronUINTBCD:
		name = "UINT_BCD"
	case TypeOmronUDINTBCD:
		name = "UDINT_BCD"
	case TypeOmronULINTBCD:
		name = "ULINT_BCD"
	case TypeOmronEnum:
		name = "ENUM"
	case TypeOmronUnion:
		name = "UNION"
	default:
		name = fmt.Sprintf("TYPE_%04X", baseType)
	}

	if isArr {
		return name + "[]"
	}
	return name
}

// TypeCodeFromName returns the type code for a FINS type name. INT16, INT32
// and INT64 are accepted as documented aliases of INT, DINT and LINT.
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
	case "INT", "INT16":
		return TypeInt16, true
	case "DWORD", "UDINT":
		return TypeDWord, true
	case "DINT", "INT32":
		return TypeInt32, true
	case "LWORD", "ULINT":
		return TypeLWord, true
	case "LINT", "INT64":
		return TypeInt64, true
	case "REAL":
		return TypeReal, true
	case "LREAL":
		return TypeLReal, true
	case "STRING":
		return TypeString, true
	default:
		return TypeUnknown, false
	}
}

// TypeSize returns the byte size for a primitive type code.
func TypeSize(typeCode uint16) int {
	baseType := BaseType(typeCode)

	// Structures have variable size
	if (baseType & TypeStructFlag) == TypeStructFlag {
		return 0
	}

	switch baseType {
	case TypeBool, TypeCIPBool:
		return 2 // BOOL is read as a word in FINS
	case TypeByte, TypeSByte, TypeCIPUSINT, TypeCIPSINT, TypeOmronByte:
		return 1
	case TypeWord, TypeInt16, TypeCIPUINT, TypeCIPINT, TypeOmronWord:
		return 2
	case TypeDWord, TypeInt32, TypeReal, TypeCIPUDINT, TypeCIPDINT, TypeCIPREAL, TypeOmronDWord:
		return 4
	case TypeLWord, TypeInt64, TypeLReal, TypeCIPULINT, TypeCIPLINT, TypeCIPLREAL, TypeOmronLWord:
		return 8
	case TypeOmronTime, TypeOmronTimeNSec, TypeOmronDate, TypeOmronTOD, TypeOmronDT:
		return 8 // W506 appendix A: TIME, DATE, TIME_OF_DAY, DATE_AND_TIME are 8 bytes
	case TypeOmronUINTBCD:
		return 2
	case TypeOmronUDINTBCD, TypeOmronEnum:
		return 4
	case TypeOmronULINTBCD:
		return 8
	case TypeString, TypeCIPSTRING:
		return 1 // Per-character size; count determines string length
	default:
		return 0 // Variable or unknown
	}
}

// AreaName returns the human-readable name for a memory area code.
func AreaName(area byte) string {
	switch area {
	case AreaCIOBit, AreaCIOWord:
		return "CIO"
	case AreaWRBit, AreaWRWord:
		return "WR"
	case AreaHRBit, AreaHRWord:
		return "HR"
	case AreaARBit, AreaARWord:
		return "AR"
	case AreaDMBit, AreaDMWord:
		return "DM"
	case AreaTaskBit:
		return "TK"
	case AreaTimerCounterPV:
		return "TC"
	case AreaEM0Word:
		return "EM0"
	case AreaEM1Word:
		return "EM1"
	case AreaEM2Word:
		return "EM2"
	case AreaEM3Word:
		return "EM3"
	case AreaEM4Word:
		return "EM4"
	case AreaEM5Word:
		return "EM5"
	case AreaEM6Word:
		return "EM6"
	case AreaEM7Word:
		return "EM7"
	case AreaEM8Word:
		return "EM8"
	case AreaEM9Word:
		return "EM9"
	case AreaEMAWord:
		return "EMA"
	case AreaEMBWord:
		return "EMB"
	case AreaEMCWord:
		return "EMC"
	case AreaEMCurr:
		return "EM"
	default:
		return fmt.Sprintf("AREA_%02X", area)
	}
}

// AreaFromName returns the memory area code for a name.
//
// The bare prefixes "C" and "T" are deliberately not accepted: in Omron
// notation they denote counters and timers, not CIO or task flags. Use "CIO",
// "TK", or the TIM/CNT prefixes understood by ParseAddress.
func AreaFromName(name string) (byte, bool) {
	switch name {
	case "CIO":
		return AreaCIOWord, true
	case "WR", "W":
		return AreaWRWord, true
	case "HR", "H":
		return AreaHRWord, true
	case "AR", "A":
		return AreaARWord, true
	case "DM", "D":
		return AreaDMWord, true
	case "TK":
		return AreaTaskBit, true
	case "TC":
		return AreaTimerCounterPV, true
	case "EM", "E":
		return AreaEMCurr, true
	case "EM0":
		return AreaEM0Word, true
	case "EM1":
		return AreaEM1Word, true
	case "EM2":
		return AreaEM2Word, true
	case "EM3":
		return AreaEM3Word, true
	case "EM4":
		return AreaEM4Word, true
	case "EM5":
		return AreaEM5Word, true
	case "EM6":
		return AreaEM6Word, true
	case "EM7":
		return AreaEM7Word, true
	case "EM8":
		return AreaEM8Word, true
	case "EM9":
		return AreaEM9Word, true
	case "EMA":
		return AreaEMAWord, true
	case "EMB":
		return AreaEMBWord, true
	case "EMC":
		return AreaEMCWord, true
	default:
		return 0, false
	}
}

// BitAreaFromWordArea converts a word area code to its corresponding bit area code.
func BitAreaFromWordArea(wordArea byte) byte {
	switch wordArea {
	case AreaCIOWord:
		return AreaCIOBit
	case AreaWRWord:
		return AreaWRBit
	case AreaHRWord:
		return AreaHRBit
	case AreaARWord:
		return AreaARBit
	case AreaDMWord:
		return AreaDMBit
	default:
		return wordArea
	}
}

// IsBitArea returns true if the memory area code is for bit-level access.
func IsBitArea(area byte) bool {
	switch area {
	case AreaCIOBit, AreaWRBit, AreaHRBit, AreaARBit, AreaDMBit, AreaTaskBit:
		return true
	default:
		return false
	}
}

// SupportedTypeNames returns a list of supported type names.
func SupportedTypeNames() []string {
	return []string{
		"BOOL", "BYTE", "SINT",
		"WORD", "INT",
		"DWORD", "DINT", "REAL",
		"LWORD", "LINT", "LREAL",
		"STRING",
	}
}

// finsWordOrder is the byte order of multi-word values in CS/CJ/CP memory as
// returned by FINS memory reads: each 16-bit word is big-endian, and a 32/64-bit
// value occupies consecutive words with the LEAST significant word at the
// LOWEST address. REAL 1.0 (0x3F800000) in D100/D101 is D100=0x0000,
// D101=0x3F80, i.e. wire bytes 00 00 3F 80.
type finsWordOrder struct{}

var finsOrder binary.ByteOrder = finsWordOrder{}

func (finsWordOrder) Uint16(b []byte) uint16 { return binary.BigEndian.Uint16(b) }

func (finsWordOrder) PutUint16(b []byte, v uint16) { binary.BigEndian.PutUint16(b, v) }

func (finsWordOrder) Uint32(b []byte) uint32 {
	_ = b[3]
	return uint32(binary.BigEndian.Uint16(b[2:4]))<<16 | uint32(binary.BigEndian.Uint16(b[0:2]))
}

func (finsWordOrder) PutUint32(b []byte, v uint32) {
	_ = b[3]
	binary.BigEndian.PutUint16(b[0:2], uint16(v))
	binary.BigEndian.PutUint16(b[2:4], uint16(v>>16))
}

func (finsWordOrder) Uint64(b []byte) uint64 {
	_ = b[7]
	return uint64(binary.BigEndian.Uint16(b[6:8]))<<48 | uint64(binary.BigEndian.Uint16(b[4:6]))<<32 |
		uint64(binary.BigEndian.Uint16(b[2:4]))<<16 | uint64(binary.BigEndian.Uint16(b[0:2]))
}

func (finsWordOrder) PutUint64(b []byte, v uint64) {
	_ = b[7]
	binary.BigEndian.PutUint16(b[0:2], uint16(v))
	binary.BigEndian.PutUint16(b[2:4], uint16(v>>16))
	binary.BigEndian.PutUint16(b[4:6], uint16(v>>32))
	binary.BigEndian.PutUint16(b[6:8], uint16(v>>48))
}

func (finsWordOrder) String() string { return "FINSWordOrder" }

// decodeCIPString strips the Omron NJ/NX CIP STRING 16-bit little-endian
// length prefix. A declared length longer than the payload is clipped.
func decodeCIPString(data []byte) string {
	if len(data) < 2 {
		return ""
	}
	n := int(binary.LittleEndian.Uint16(data[0:2]))
	body := data[2:]
	if n > len(body) {
		n = len(body)
	}
	return decodeString(body[:n])
}

// DecodeValue decodes raw bytes into a Go value based on the type code.
// bigEndian selects the FINS layout (big-endian words, least significant word
// first for 32/64-bit values); otherwise CIP little-endian is used.
func DecodeValue(typeCode uint16, data []byte, bigEndian bool) interface{} {
	var order binary.ByteOrder
	if bigEndian {
		order = finsOrder
	} else {
		order = binary.LittleEndian
	}

	switch BaseType(typeCode) {
	case TypeBool, TypeCIPBool:
		if len(data) < 1 {
			return false
		}
		if bigEndian && len(data) >= 2 {
			return order.Uint16(data) != 0
		}
		return data[0] != 0

	case TypeByte, TypeCIPUSINT, TypeOmronByte:
		if len(data) < 1 {
			return uint8(0)
		}
		return data[0]

	case TypeSByte, TypeCIPSINT:
		if len(data) < 1 {
			return int8(0)
		}
		return int8(data[0])

	case TypeWord, TypeCIPUINT, TypeOmronWord:
		if len(data) < 2 {
			return uint16(0)
		}
		return order.Uint16(data)

	case TypeInt16, TypeCIPINT:
		if len(data) < 2 {
			return int16(0)
		}
		return int16(order.Uint16(data))

	case TypeDWord, TypeCIPUDINT, TypeOmronDWord:
		if len(data) < 4 {
			return uint32(0)
		}
		return order.Uint32(data)

	case TypeInt32, TypeCIPDINT:
		if len(data) < 4 {
			return int32(0)
		}
		return int32(order.Uint32(data))

	case TypeLWord, TypeCIPULINT, TypeOmronLWord:
		if len(data) < 8 {
			return uint64(0)
		}
		return order.Uint64(data)

	case TypeInt64, TypeCIPLINT:
		if len(data) < 8 {
			return int64(0)
		}
		return int64(order.Uint64(data))

	case TypeReal, TypeCIPREAL:
		if len(data) < 4 {
			return float32(0)
		}
		return math.Float32frombits(order.Uint32(data))

	case TypeLReal, TypeCIPLREAL:
		if len(data) < 8 {
			return float64(0)
		}
		return math.Float64frombits(order.Uint64(data))

	case TypeString, TypeCIPSTRING:
		if !bigEndian && BaseType(typeCode) == TypeCIPSTRING {
			return decodeCIPString(data)
		}
		// Find null terminator
		for i, b := range data {
			if b == 0 {
				return string(data[:i])
			}
		}
		return string(data)

	case TypeOmronTime, TypeOmronTimeNSec, TypeOmronTOD, TypeOmronDate, TypeOmronDT, TypeOmronEnum:
		// NJ/NX CIP-only types; always little-endian.
		if v, ok := decodeNJValue(BaseType(typeCode), data); ok {
			return v
		}
		return data

	default:
		return data
	}
}

// EncodeValue encodes a Go value into bytes for writing.
// Supports both scalar values and slices (for array writes). Every integer
// target is range-checked: a value that does not fit the PLC type (for
// example 70000 for a WORD, -1 for a UDINT or 1.5 for a DINT) is rejected
// instead of being truncated or wrapped.
func EncodeValue(value interface{}, typeCode uint16, bigEndian bool) ([]byte, error) {
	var order binary.ByteOrder
	if bigEndian {
		order = finsOrder
	} else {
		order = binary.LittleEndian
	}

	base := BaseType(typeCode)
	isString := base == TypeString || base == TypeCIPSTRING
	rv := reflect.ValueOf(value)
	if rv.IsValid() && rv.Kind() == reflect.Slice && !(isString && rv.Type().Elem().Kind() == reflect.Uint8) {
		if rv.Len() == 0 {
			return nil, fmt.Errorf("cannot write an empty array")
		}
		var result []byte
		for i := 0; i < rv.Len(); i++ {
			elem := rv.Index(i).Interface()
			var encoded []byte
			var err error
			if b, ok := elem.(bool); ok && !bigEndian && (base == TypeBool || base == TypeCIPBool) {
				// W506 7-7-4: when Num of Element is given for a BOOL
				// array, each element is a single status byte.
				encoded = []byte{0}
				if b {
					encoded[0] = 1
				}
			} else {
				encoded, err = encodeScalar(elem, typeCode, order)
			}
			if err != nil {
				return nil, fmt.Errorf("element %d: %w", i, err)
			}
			result = append(result, encoded...)
		}
		return result, nil
	}

	// Handle scalar values
	return encodeScalar(value, typeCode, order)
}

// encodeScalar encodes a single scalar value into bytes.
func encodeScalar(value interface{}, typeCode uint16, order binary.ByteOrder) ([]byte, error) {
	base := BaseType(typeCode)
	switch base {
	case TypeBool, TypeCIPBool:
		var v uint16
		switch b := value.(type) {
		case bool:
			if b {
				v = 1
			}
		case int, int32, int64, float64:
			n, err := signedValue(b, 64)
			if err != nil {
				return nil, fmt.Errorf("BOOL: %w", err)
			}
			if n != 0 && n != 1 {
				return nil, fmt.Errorf("BOOL: %d is not 0 or 1", n)
			}
			v = uint16(n)
		default:
			return nil, fmt.Errorf("cannot convert %T to BOOL", value)
		}
		if order == binary.LittleEndian {
			// W506 7-7-3 Boolean Data: status byte then the forced
			// set/reset byte, which must be 0 when writing.
			return []byte{byte(v), 0}, nil
		}
		buf := make([]byte, 2)
		order.PutUint16(buf, v)
		return buf, nil

	case TypeByte, TypeCIPUSINT, TypeOmronByte:
		n, err := unsignedValue(value, 8)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", TypeName(base), err)
		}
		return []byte{byte(n)}, nil

	case TypeSByte, TypeCIPSINT:
		n, err := signedValue(value, 8)
		if err != nil {
			return nil, fmt.Errorf("SINT: %w", err)
		}
		return []byte{byte(int8(n))}, nil

	case TypeWord, TypeCIPUINT, TypeOmronWord:
		n, err := unsignedValue(value, 16)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", TypeName(base), err)
		}
		buf := make([]byte, 2)
		order.PutUint16(buf, uint16(n))
		return buf, nil

	case TypeInt16, TypeCIPINT:
		n, err := signedValue(value, 16)
		if err != nil {
			return nil, fmt.Errorf("INT: %w", err)
		}
		buf := make([]byte, 2)
		order.PutUint16(buf, uint16(int16(n)))
		return buf, nil

	case TypeDWord, TypeCIPUDINT, TypeOmronDWord:
		n, err := unsignedValue(value, 32)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", TypeName(base), err)
		}
		buf := make([]byte, 4)
		order.PutUint32(buf, uint32(n))
		return buf, nil

	case TypeInt32, TypeCIPDINT, TypeOmronEnum:
		n, err := signedValue(value, 32)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", TypeName(base), err)
		}
		buf := make([]byte, 4)
		order.PutUint32(buf, uint32(int32(n)))
		return buf, nil

	case TypeLWord, TypeCIPULINT, TypeOmronLWord:
		n, err := unsignedValue(value, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", TypeName(base), err)
		}
		buf := make([]byte, 8)
		order.PutUint64(buf, n)
		return buf, nil

	case TypeInt64, TypeCIPLINT:
		n, err := signedValue(value, 64)
		if err != nil {
			return nil, fmt.Errorf("LINT: %w", err)
		}
		buf := make([]byte, 8)
		order.PutUint64(buf, uint64(n))
		return buf, nil

	case TypeReal, TypeCIPREAL:
		buf := make([]byte, 4)
		switch v := value.(type) {
		case float32:
			order.PutUint32(buf, math.Float32bits(v))
		case float64:
			if !math.IsInf(v, 0) && !math.IsNaN(v) && math.Abs(v) > math.MaxFloat32 {
				return nil, fmt.Errorf("REAL: %v is outside the float32 range", v)
			}
			order.PutUint32(buf, math.Float32bits(float32(v)))
		case int:
			order.PutUint32(buf, math.Float32bits(float32(v)))
		case int64:
			order.PutUint32(buf, math.Float32bits(float32(v)))
		default:
			return nil, fmt.Errorf("cannot convert %T to REAL", value)
		}
		return buf, nil

	case TypeLReal, TypeCIPLREAL:
		buf := make([]byte, 8)
		switch v := value.(type) {
		case float64:
			order.PutUint64(buf, math.Float64bits(v))
		case float32:
			order.PutUint64(buf, math.Float64bits(float64(v)))
		case int:
			order.PutUint64(buf, math.Float64bits(float64(v)))
		case int64:
			order.PutUint64(buf, math.Float64bits(float64(v)))
		default:
			return nil, fmt.Errorf("cannot convert %T to LREAL", value)
		}
		return buf, nil

	case TypeString, TypeCIPSTRING:
		if order == binary.LittleEndian && base == TypeCIPSTRING {
			// Omron NJ/NX CIP STRING: 16-bit little-endian byte count, then
			// the characters (no terminator).
			var body []byte
			switch v := value.(type) {
			case string:
				body = []byte(v)
			case []byte:
				body = v
			default:
				return nil, fmt.Errorf("cannot convert %T to STRING", value)
			}
			if len(body) > math.MaxUint16 {
				return nil, fmt.Errorf("STRING too long: %d bytes", len(body))
			}
			return append(binary.LittleEndian.AppendUint16(nil, uint16(len(body))), body...), nil
		}
		// Always build a fresh buffer: appending the terminator to a
		// caller's []byte could write into its backing array.
		switch v := value.(type) {
		case string:
			return append([]byte(v), 0), nil
		case []byte:
			return append(append(make([]byte, 0, len(v)+1), v...), 0), nil
		default:
			return nil, fmt.Errorf("cannot convert %T to STRING", value)
		}

	case TypeOmronTime, TypeOmronTimeNSec, TypeOmronTOD, TypeOmronDate, TypeOmronDT:
		if order != binary.LittleEndian {
			return nil, fmt.Errorf("%s is an NJ/NX (EtherNet/IP) type", TypeName(base))
		}
		return encodeNJTime(value, base)

	default:
		return nil, fmt.Errorf("unsupported type code: %s", TypeName(typeCode))
	}
}
