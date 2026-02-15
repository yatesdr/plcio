// Package omron provides unified Omron PLC communication.
// Supports FINS/UDP, FINS/TCP, and EIP/CIP protocols.
package omron

import (
	"encoding/binary"
	"fmt"
	"math"
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
	AreaEM0Word  byte = 0xA0 // EM bank 0; word
	AreaEM1Word  byte = 0xA1 // EM bank 1; word
	AreaEM2Word  byte = 0xA2 // EM bank 2; word
	AreaEM3Word  byte = 0xA3 // EM bank 3; word
	AreaEM4Word  byte = 0xA4 // EM bank 4; word
	AreaEM5Word  byte = 0xA5 // EM bank 5; word
	AreaEM6Word  byte = 0xA6 // EM bank 6; word
	AreaEM7Word  byte = 0xA7 // EM bank 7; word
	AreaEM8Word  byte = 0xA8 // EM bank 8; word
	AreaEM9Word  byte = 0xA9 // EM bank 9; word
	AreaEMAWord  byte = 0xAA // EM bank A; word
	AreaEMBWord  byte = 0xAB // EM bank B; word
	AreaEMCWord  byte = 0xAC // EM bank C; word
	AreaEMCurr   byte = 0x98 // EM current bank; word
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

	// Omron-specific type codes
	// Based on libplctag research and Wireshark captures
	TypeOmronByte   uint16 = 0xD1 // Omron BYTE (sometimes used instead of USINT)
	TypeOmronWord   uint16 = 0xD2 // Omron WORD (sometimes used instead of UINT)
	TypeOmronDWord  uint16 = 0xD3 // Omron DWORD (sometimes used instead of UDINT)
	TypeOmronLWord  uint16 = 0xD4 // Omron LWORD (sometimes used instead of ULINT)
	TypeOmronTime   uint16 = 0xDB // Omron TIME (4 bytes, milliseconds)
	TypeOmronDate   uint16 = 0xDC // Omron DATE
	TypeOmronTOD    uint16 = 0xDD // Omron TIME_OF_DAY
	TypeOmronDT     uint16 = 0xDE // Omron DATE_AND_TIME

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
	case TypeOmronTime:
		name = "TIME"
	case TypeOmronDate:
		name = "DATE"
	case TypeOmronTOD:
		name = "TIME_OF_DAY"
	case TypeOmronDT:
		name = "DATE_AND_TIME"
	default:
		name = fmt.Sprintf("TYPE_%04X", baseType)
	}

	if isArr {
		return name + "[]"
	}
	return name
}

// TypeCodeFromName returns the type code for a type name.
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
	case TypeDWord, TypeInt32, TypeReal, TypeCIPUDINT, TypeCIPDINT, TypeCIPREAL, TypeOmronDWord, TypeOmronTime:
		return 4
	case TypeLWord, TypeInt64, TypeLReal, TypeCIPULINT, TypeCIPLINT, TypeCIPLREAL, TypeOmronLWord, TypeOmronDT:
		return 8
	case TypeOmronDate, TypeOmronTOD:
		return 4
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
func AreaFromName(name string) (byte, bool) {
	switch name {
	case "CIO", "C":
		return AreaCIOWord, true
	case "WR", "W":
		return AreaWRWord, true
	case "HR", "H":
		return AreaHRWord, true
	case "AR", "A":
		return AreaARWord, true
	case "DM", "D":
		return AreaDMWord, true
	case "TK", "T":
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

// DecodeValue decodes raw bytes into a Go value based on the type code.
// FINS uses big-endian, CIP uses little-endian.
func DecodeValue(typeCode uint16, data []byte, bigEndian bool) interface{} {
	var order binary.ByteOrder
	if bigEndian {
		order = binary.BigEndian
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

	case TypeByte, TypeCIPUSINT:
		if len(data) < 1 {
			return uint8(0)
		}
		return data[0]

	case TypeSByte, TypeCIPSINT:
		if len(data) < 1 {
			return int8(0)
		}
		return int8(data[0])

	case TypeWord, TypeCIPUINT:
		if len(data) < 2 {
			return uint16(0)
		}
		return order.Uint16(data)

	case TypeInt16, TypeCIPINT:
		if len(data) < 2 {
			return int16(0)
		}
		return int16(order.Uint16(data))

	case TypeDWord, TypeCIPUDINT:
		if len(data) < 4 {
			return uint32(0)
		}
		return order.Uint32(data)

	case TypeInt32, TypeCIPDINT:
		if len(data) < 4 {
			return int32(0)
		}
		return int32(order.Uint32(data))

	case TypeLWord, TypeCIPULINT:
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
		// Find null terminator
		for i, b := range data {
			if b == 0 {
				return string(data[:i])
			}
		}
		return string(data)

	default:
		return data
	}
}

// EncodeValue encodes a Go value into bytes for writing.
// Supports both scalar values and slices (for array writes).
func EncodeValue(value interface{}, typeCode uint16, bigEndian bool) ([]byte, error) {
	var order binary.ByteOrder
	if bigEndian {
		order = binary.BigEndian
	} else {
		order = binary.LittleEndian
	}

	// Handle slice types - encode each element and concatenate
	switch v := value.(type) {
	case []int64:
		var result []byte
		for _, elem := range v {
			encoded, err := encodeScalar(elem, typeCode, order)
			if err != nil {
				return nil, err
			}
			result = append(result, encoded...)
		}
		return result, nil
	case []float64:
		var result []byte
		for _, elem := range v {
			encoded, err := encodeScalar(elem, typeCode, order)
			if err != nil {
				return nil, err
			}
			result = append(result, encoded...)
		}
		return result, nil
	case []int:
		var result []byte
		for _, elem := range v {
			encoded, err := encodeScalar(int64(elem), typeCode, order)
			if err != nil {
				return nil, err
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
	switch BaseType(typeCode) {
	case TypeBool, TypeCIPBool:
		var v uint16
		switch b := value.(type) {
		case bool:
			if b {
				v = 1
			}
		case int:
			if b != 0 {
				v = 1
			}
		case int32:
			if b != 0 {
				v = 1
			}
		case int64:
			if b != 0 {
				v = 1
			}
		case float64:
			if b != 0 {
				v = 1
			}
		default:
			return nil, fmt.Errorf("cannot convert %T to BOOL", value)
		}
		buf := make([]byte, 2)
		order.PutUint16(buf, v)
		return buf, nil

	case TypeByte, TypeCIPUSINT:
		switch v := value.(type) {
		case uint8:
			return []byte{v}, nil
		case int:
			return []byte{byte(v)}, nil
		case int64:
			return []byte{byte(v)}, nil
		case float64:
			return []byte{byte(int64(v))}, nil
		default:
			return nil, fmt.Errorf("cannot convert %T to BYTE", value)
		}

	case TypeSByte, TypeCIPSINT:
		switch v := value.(type) {
		case int8:
			return []byte{byte(v)}, nil
		case int:
			return []byte{byte(v)}, nil
		case int64:
			return []byte{byte(v)}, nil
		case float64:
			return []byte{byte(int8(v))}, nil
		default:
			return nil, fmt.Errorf("cannot convert %T to SINT", value)
		}

	case TypeWord, TypeCIPUINT:
		buf := make([]byte, 2)
		switch v := value.(type) {
		case uint16:
			order.PutUint16(buf, v)
		case int:
			order.PutUint16(buf, uint16(v))
		case int64:
			order.PutUint16(buf, uint16(v))
		case float64:
			order.PutUint16(buf, uint16(int64(v)))
		default:
			return nil, fmt.Errorf("cannot convert %T to WORD", value)
		}
		return buf, nil

	case TypeInt16, TypeCIPINT:
		buf := make([]byte, 2)
		switch v := value.(type) {
		case int16:
			order.PutUint16(buf, uint16(v))
		case int:
			order.PutUint16(buf, uint16(v))
		case int64:
			order.PutUint16(buf, uint16(v))
		case float64:
			order.PutUint16(buf, uint16(int16(v)))
		default:
			return nil, fmt.Errorf("cannot convert %T to INT", value)
		}
		return buf, nil

	case TypeDWord, TypeCIPUDINT:
		buf := make([]byte, 4)
		switch v := value.(type) {
		case uint32:
			order.PutUint32(buf, v)
		case int:
			order.PutUint32(buf, uint32(v))
		case int64:
			order.PutUint32(buf, uint32(v))
		case float64:
			order.PutUint32(buf, uint32(int64(v)))
		default:
			return nil, fmt.Errorf("cannot convert %T to DWORD", value)
		}
		return buf, nil

	case TypeInt32, TypeCIPDINT:
		buf := make([]byte, 4)
		switch v := value.(type) {
		case int32:
			order.PutUint32(buf, uint32(v))
		case int:
			order.PutUint32(buf, uint32(v))
		case int64:
			order.PutUint32(buf, uint32(v))
		case float64:
			order.PutUint32(buf, uint32(int32(v)))
		default:
			return nil, fmt.Errorf("cannot convert %T to DINT", value)
		}
		return buf, nil

	case TypeLWord, TypeCIPULINT:
		buf := make([]byte, 8)
		switch v := value.(type) {
		case uint64:
			order.PutUint64(buf, v)
		case int:
			order.PutUint64(buf, uint64(v))
		case int64:
			order.PutUint64(buf, uint64(v))
		case float64:
			order.PutUint64(buf, uint64(int64(v)))
		default:
			return nil, fmt.Errorf("cannot convert %T to LWORD", value)
		}
		return buf, nil

	case TypeInt64, TypeCIPLINT:
		buf := make([]byte, 8)
		switch v := value.(type) {
		case int64:
			order.PutUint64(buf, uint64(v))
		case int:
			order.PutUint64(buf, uint64(v))
		case float64:
			order.PutUint64(buf, uint64(int64(v)))
		default:
			return nil, fmt.Errorf("cannot convert %T to LINT", value)
		}
		return buf, nil

	case TypeReal, TypeCIPREAL:
		buf := make([]byte, 4)
		switch v := value.(type) {
		case float32:
			order.PutUint32(buf, math.Float32bits(v))
		case float64:
			order.PutUint32(buf, math.Float32bits(float32(v)))
		case int:
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
		default:
			return nil, fmt.Errorf("cannot convert %T to LREAL", value)
		}
		return buf, nil

	case TypeString, TypeCIPSTRING:
		switch v := value.(type) {
		case string:
			return append([]byte(v), 0), nil
		case []byte:
			return append(v, 0), nil
		default:
			return nil, fmt.Errorf("cannot convert %T to STRING", value)
		}

	default:
		return nil, fmt.Errorf("unsupported type code: %s", TypeName(typeCode))
	}
}
