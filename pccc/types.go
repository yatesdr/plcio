package pccc

import "strings"

// PCCC file type codes — identify the data file type in SLC500/PLC5/MicroLogix data tables.
// The high bit (0x80) indicates a "typed" file in the PCCC protocol.
const (
	FileTypeOutput  byte = 0x82 // O - Output
	FileTypeInput   byte = 0x83 // I - Input
	FileTypeStatus  byte = 0x84 // S - Status
	FileTypeBinary  byte = 0x85 // B - Binary/Bit
	FileTypeTimer   byte = 0x86 // T - Timer
	FileTypeCounter byte = 0x87 // C - Counter
	FileTypeControl byte = 0x88 // R - Control
	FileTypeInteger byte = 0x89 // N - Integer (16-bit signed)
	FileTypeFloat   byte = 0x8A // F - Float (32-bit IEEE 754)
	FileTypeString  byte = 0x8D // ST - String
	FileTypeASCII   byte = 0x8E // A - ASCII
	FileTypeLong    byte = 0x91 // L - Long Integer (32-bit signed)
	FileTypeMessage byte = 0x92 // MG - Message (MicroLogix)
	FileTypePID     byte = 0x93 // PD - PID
)

// Element sizes in bytes for each file type.
const (
	ElementSizeOutput  = 2  // 1 x 16-bit word
	ElementSizeInput   = 2  // 1 x 16-bit word
	ElementSizeStatus  = 2  // 1 x 16-bit word
	ElementSizeBinary  = 2  // 1 x 16-bit word
	ElementSizeTimer   = 6  // 3 x 16-bit words (Control, PRE, ACC)
	ElementSizeCounter = 6  // 3 x 16-bit words (Control, PRE, ACC)
	ElementSizeControl = 6  // 3 x 16-bit words (Control, LEN, POS)
	ElementSizeInteger = 2  // 1 x 16-bit word
	ElementSizeFloat   = 4  // 32-bit IEEE 754
	ElementSizeString  = 84 // 2-byte length + 82 chars
	ElementSizeASCII   = 2  // 1 x 16-bit word
	ElementSizeLong    = 4  // 32-bit integer
	ElementSizeMessage = 50 // MG - Message control (varies, 50 typical)
	ElementSizePID     = 46 // PD - PID control (varies, 46 typical)
)

// Sub-element word sizes (for Timer, Counter, Control — each sub-element is 16-bit).
const SubElementSize = 2

// Timer sub-element indices.
const (
	TimerControl byte = 0 // Control word (EN, TT, DN bits)
	TimerPRE     byte = 1 // Preset value
	TimerACC     byte = 2 // Accumulated value
)

// Counter sub-element indices.
const (
	CounterControl byte = 0 // Control word (CU, CD, DN, OV, UN bits)
	CounterPRE     byte = 1 // Preset value
	CounterACC     byte = 2 // Accumulated value
)

// Control sub-element indices.
const (
	ControlWord byte = 0 // Control word (EN, EU, DN, EM, ER, UL, IN, FD bits)
	ControlLEN  byte = 1 // Length
	ControlPOS  byte = 2 // Position
)

// Timer control word bit positions (within the 16-bit control word).
const (
	TimerBitEN = 15 // Enable
	TimerBitTT = 14 // Timer Timing
	TimerBitDN = 13 // Done
)

// Counter control word bit positions.
const (
	CounterBitCU = 15 // Count Up Enable
	CounterBitCD = 14 // Count Down Enable
	CounterBitDN = 13 // Done
	CounterBitOV = 12 // Overflow
	CounterBitUN = 11 // Underflow
)

// Control word bit positions.
const (
	ControlBitEN = 15 // Enable
	ControlBitEU = 14 // Enable Unload
	ControlBitDN = 13 // Done
	ControlBitEM = 12 // Stack Empty
	ControlBitER = 11 // Error
	ControlBitUL = 10 // Unload
	ControlBitIN = 9  // Inhibit
	ControlBitFD = 8  // Found
)

// PLCType distinguishes the PCCC processor family.
// When using PCCC-over-CIP via EtherNet/IP, all three processor families use the same
// SLC Protected Typed Logical Read/Write commands (FNC 0xA2/0xAA). The PLCType is stored
// for identification/logging but does not currently change the wire protocol behavior.
type PLCType byte

const (
	TypeSLC500    PLCType = iota // SLC 5/03, 5/04, 5/05
	TypePLC5                     // PLC-5 series
	TypeMicroLogix               // MicroLogix 1000/1100/1200/1400/1500
)

// String returns the human-readable name for the PLC type.
func (t PLCType) String() string {
	switch t {
	case TypeSLC500:
		return "SLC 500"
	case TypePLC5:
		return "PLC-5"
	case TypeMicroLogix:
		return "MicroLogix"
	default:
		return "Unknown"
	}
}

// ElementSize returns the size in bytes for one element of the given file type.
func ElementSize(fileType byte) int {
	switch fileType {
	case FileTypeOutput:
		return ElementSizeOutput
	case FileTypeInput:
		return ElementSizeInput
	case FileTypeStatus:
		return ElementSizeStatus
	case FileTypeBinary:
		return ElementSizeBinary
	case FileTypeTimer:
		return ElementSizeTimer
	case FileTypeCounter:
		return ElementSizeCounter
	case FileTypeControl:
		return ElementSizeControl
	case FileTypeInteger:
		return ElementSizeInteger
	case FileTypeFloat:
		return ElementSizeFloat
	case FileTypeString:
		return ElementSizeString
	case FileTypeASCII:
		return ElementSizeASCII
	case FileTypeLong:
		return ElementSizeLong
	case FileTypeMessage:
		return ElementSizeMessage
	case FileTypePID:
		return ElementSizePID
	default:
		return 2 // Default to 16-bit word
	}
}

// FileTypeName returns a human-readable name for the file type code.
func FileTypeName(fileType byte) string {
	switch fileType {
	case FileTypeOutput:
		return "Output"
	case FileTypeInput:
		return "Input"
	case FileTypeStatus:
		return "Status"
	case FileTypeBinary:
		return "Binary"
	case FileTypeTimer:
		return "Timer"
	case FileTypeCounter:
		return "Counter"
	case FileTypeControl:
		return "Control"
	case FileTypeInteger:
		return "Integer"
	case FileTypeFloat:
		return "Float"
	case FileTypeString:
		return "String"
	case FileTypeASCII:
		return "ASCII"
	case FileTypeLong:
		return "Long"
	case FileTypeMessage:
		return "Message"
	case FileTypePID:
		return "PID"
	default:
		return "Unknown"
	}
}

// FileTypePrefix returns the single-letter address prefix for a PCCC file type code.
// For example, 0x89 → "N", 0x8A → "F", 0x86 → "T".
// Returns "" for unknown types.
func FileTypePrefix(fileType byte) string {
	switch fileType {
	case FileTypeOutput:
		return "O"
	case FileTypeInput:
		return "I"
	case FileTypeStatus:
		return "S"
	case FileTypeBinary:
		return "B"
	case FileTypeTimer:
		return "T"
	case FileTypeCounter:
		return "C"
	case FileTypeControl:
		return "R"
	case FileTypeInteger:
		return "N"
	case FileTypeFloat:
		return "F"
	case FileTypeString:
		return "ST"
	case FileTypeASCII:
		return "A"
	case FileTypeLong:
		return "L"
	case FileTypeMessage:
		return "MG"
	case FileTypePID:
		return "PD"
	default:
		return ""
	}
}

// IsComplexType returns true for file types with sub-elements (Timer, Counter, Control).
func IsComplexType(fileType byte) bool {
	return fileType == FileTypeTimer || fileType == FileTypeCounter || fileType == FileTypeControl
}

// TypeInteger is the default data type code for PCCC address-based tags (N-file, 16-bit integer).
const TypeInteger = uint16(FileTypeInteger) // 0x89

// TypeName returns a short name for the given PCCC data type code (uint16 form).
func TypeName(dataType uint16) string {
	switch byte(dataType) {
	case FileTypeOutput:
		return "OUTPUT"
	case FileTypeInput:
		return "INPUT"
	case FileTypeStatus:
		return "STATUS"
	case FileTypeBinary:
		return "BINARY"
	case FileTypeTimer:
		return "TIMER"
	case FileTypeCounter:
		return "COUNTER"
	case FileTypeControl:
		return "CONTROL"
	case FileTypeInteger:
		return "INT"
	case FileTypeFloat:
		return "FLOAT"
	case FileTypeString:
		return "STRING"
	case FileTypeASCII:
		return "ASCII"
	case FileTypeLong:
		return "LONG"
	case FileTypeMessage:
		return "MESSAGE"
	case FileTypePID:
		return "PID"
	default:
		return "UNKNOWN"
	}
}

// TypeCodeFromName returns the PCCC type code for the given short name (case-insensitive).
// Returns (0, false) if the name is not recognized.
func TypeCodeFromName(name string) (uint16, bool) {
	switch strings.ToUpper(name) {
	case "OUTPUT":
		return uint16(FileTypeOutput), true
	case "INPUT":
		return uint16(FileTypeInput), true
	case "STATUS":
		return uint16(FileTypeStatus), true
	case "BINARY":
		return uint16(FileTypeBinary), true
	case "TIMER":
		return uint16(FileTypeTimer), true
	case "COUNTER":
		return uint16(FileTypeCounter), true
	case "CONTROL":
		return uint16(FileTypeControl), true
	case "INT":
		return uint16(FileTypeInteger), true
	case "FLOAT":
		return uint16(FileTypeFloat), true
	case "STRING":
		return uint16(FileTypeString), true
	case "ASCII":
		return uint16(FileTypeASCII), true
	case "LONG":
		return uint16(FileTypeLong), true
	case "MESSAGE":
		return uint16(FileTypeMessage), true
	case "PID":
		return uint16(FileTypePID), true
	default:
		return 0, false
	}
}

// SupportedTypeNames returns the list of user-selectable PCCC type names for UI dropdowns.
func SupportedTypeNames() []string {
	return []string{"INT", "FLOAT", "BINARY", "TIMER", "COUNTER", "CONTROL", "STRING", "LONG"}
}

// TypeSize returns the element size in bytes for the given PCCC data type code (uint16 form).
func TypeSize(dataType uint16) int {
	return ElementSize(byte(dataType))
}
