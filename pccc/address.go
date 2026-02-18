package pccc

import (
	"fmt"
	"strconv"
	"strings"
)

// FileAddress represents a parsed SLC500/PLC5/MicroLogix data table address.
//
// Address format: [TypePrefix][FileNumber]:[Element][/Bit][.SubElement]
//
// Examples:
//
//	N7:0        Integer file 7, element 0
//	F8:5        Float file 8, element 5
//	B3:0/5      Binary file 3, element 0, bit 5
//	T4:0.ACC    Timer file 4, element 0, accumulated value (sub-element 2)
//	T4:0.DN     Timer file 4, element 0, done bit (control word bit 13)
//	C5:2.PRE    Counter file 5, element 2, preset value (sub-element 1)
//	S:1/5       Status file (default file 2), element 1, bit 5
//	O:0/3       Output file (default file 0), element 0, bit 3
//	I:0/3       Input file (default file 1), element 0, bit 3
//	ST9:0       String file 9, element 0
type FileAddress struct {
	FileType    byte   // PCCC file type code (e.g., 0x89 for Integer)
	FileNumber  uint16 // Data file number
	Element     uint16 // Element number within the file
	SubElement  uint16 // Sub-element number (0 for simple types; PRE=1, ACC=2 for Timer/Counter)
	BitNumber   int    // Bit position within element/sub-element (-1 if not a bit address)
	TypeLetter  string // Original type prefix (e.g., "N", "T", "ST")
	RawAddress  string // Original address string
}

// ReadSize returns the number of bytes to request from the PLC for this address.
func (a *FileAddress) ReadSize() int {
	if a.BitNumber >= 0 {
		// Bit access: read the containing word (2 bytes)
		return SubElementSize
	}

	if IsComplexType(a.FileType) {
		if a.SubElement > 0 {
			// Specific sub-element: read one 16-bit word
			return SubElementSize
		}
		// Full complex element: read all sub-elements
		return ElementSize(a.FileType)
	}

	// Simple types: read one full element
	return ElementSize(a.FileType)
}

// ParseAddress parses an SLC500/PLC5 data table address string into a FileAddress.
func ParseAddress(addr string) (*FileAddress, error) {
	if addr == "" {
		return nil, fmt.Errorf("empty address")
	}

	result := &FileAddress{
		BitNumber:  -1,
		RawAddress: addr,
	}

	// Split on colon to separate file specifier from element
	colonIdx := strings.Index(addr, ":")
	if colonIdx < 0 {
		return nil, fmt.Errorf("invalid address %q: missing colon separator", addr)
	}

	fileSpec := addr[:colonIdx]
	remainder := addr[colonIdx+1:]

	// Parse file specifier: [TypeLetters][FileNumber]
	typeLetter, fileNum, err := parseFileSpec(fileSpec)
	if err != nil {
		return nil, fmt.Errorf("invalid address %q: %w", addr, err)
	}
	result.TypeLetter = typeLetter

	// Map type letter to PCCC file type code and default file number
	fileType, defaultFileNum, err := lookupFileType(typeLetter)
	if err != nil {
		return nil, fmt.Errorf("invalid address %q: %w", addr, err)
	}
	result.FileType = fileType

	if fileNum >= 0 {
		result.FileNumber = uint16(fileNum)
	} else {
		if defaultFileNum < 0 {
			return nil, fmt.Errorf("invalid address %q: file number required for type %q", addr, typeLetter)
		}
		result.FileNumber = uint16(defaultFileNum)
	}

	// Parse remainder: Element[/Bit][.SubElement]
	if remainder == "" {
		return nil, fmt.Errorf("invalid address %q: missing element number", addr)
	}

	if err := parseElementAndModifiers(remainder, result); err != nil {
		return nil, fmt.Errorf("invalid address %q: %w", addr, err)
	}

	return result, nil
}

// parseFileSpec extracts the type letter(s) and optional file number from the file specifier.
// Examples: "N7" → ("N", 7), "ST9" → ("ST", 9), "O" → ("O", -1), "S" → ("S", -1)
func parseFileSpec(spec string) (typeLetter string, fileNum int, err error) {
	if len(spec) == 0 {
		return "", -1, fmt.Errorf("empty file specifier")
	}

	// Check for two-letter type prefix (ST, MG, PD)
	if len(spec) >= 2 {
		prefix := strings.ToUpper(spec[:2])
		if prefix == "ST" || prefix == "MG" || prefix == "PD" {
			numStr := spec[2:]
			if numStr == "" {
				return prefix, -1, nil
			}
			n, err := strconv.Atoi(numStr)
			if err != nil {
				return "", -1, fmt.Errorf("invalid file number in %q", spec)
			}
			return prefix, n, nil
		}
	}

	// Single-letter type prefix
	prefix := strings.ToUpper(spec[:1])
	if !isValidTypePrefix(prefix) {
		return "", -1, fmt.Errorf("unknown file type %q", prefix)
	}

	numStr := spec[1:]
	if numStr == "" {
		return prefix, -1, nil
	}
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return "", -1, fmt.Errorf("invalid file number in %q", spec)
	}
	return prefix, n, nil
}

// isValidTypePrefix returns true if the single letter is a valid PCCC file type.
func isValidTypePrefix(prefix string) bool {
	switch prefix {
	case "O", "I", "S", "B", "T", "C", "R", "N", "F", "A", "L":
		return true
	default:
		return false
	}
}

// lookupFileType maps a type letter to its PCCC file type code and default file number.
// Returns (fileType, defaultFileNumber, error). defaultFileNumber is -1 if no default.
func lookupFileType(typeLetter string) (byte, int, error) {
	switch typeLetter {
	case "O":
		return FileTypeOutput, 0, nil
	case "I":
		return FileTypeInput, 1, nil
	case "S":
		return FileTypeStatus, 2, nil
	case "B":
		return FileTypeBinary, -1, nil
	case "T":
		return FileTypeTimer, -1, nil
	case "C":
		return FileTypeCounter, -1, nil
	case "R":
		return FileTypeControl, -1, nil
	case "N":
		return FileTypeInteger, -1, nil
	case "F":
		return FileTypeFloat, -1, nil
	case "A":
		return FileTypeASCII, -1, nil
	case "L":
		return FileTypeLong, -1, nil
	case "ST":
		return FileTypeString, -1, nil
	case "MG":
		return FileTypeMessage, -1, nil
	case "PD":
		return FileTypePID, -1, nil
	default:
		return 0, -1, fmt.Errorf("unsupported file type %q", typeLetter)
	}
}

// parseElementAndModifiers parses "Element[/Bit][.SubElement]" from the remainder after the colon.
func parseElementAndModifiers(remainder string, result *FileAddress) error {
	// Check for bit access: element/bit
	if slashIdx := strings.Index(remainder, "/"); slashIdx >= 0 {
		elemStr := remainder[:slashIdx]
		bitStr := remainder[slashIdx+1:]

		elem, err := strconv.ParseUint(elemStr, 10, 16)
		if err != nil {
			return fmt.Errorf("invalid element number %q", elemStr)
		}
		result.Element = uint16(elem)

		bit, err := strconv.Atoi(bitStr)
		if err != nil {
			return fmt.Errorf("invalid bit number %q", bitStr)
		}
		if bit < 0 || bit > 15 {
			return fmt.Errorf("bit number %d out of range (0-15)", bit)
		}
		result.BitNumber = bit
		return nil
	}

	// Check for sub-element access: element.subelement
	if dotIdx := strings.Index(remainder, "."); dotIdx >= 0 {
		elemStr := remainder[:dotIdx]
		subStr := remainder[dotIdx+1:]

		elem, err := strconv.ParseUint(elemStr, 10, 16)
		if err != nil {
			return fmt.Errorf("invalid element number %q", elemStr)
		}
		result.Element = uint16(elem)

		return parseSubElement(subStr, result)
	}

	// Simple element access
	elem, err := strconv.ParseUint(remainder, 10, 16)
	if err != nil {
		return fmt.Errorf("invalid element number %q", remainder)
	}
	result.Element = uint16(elem)
	return nil
}

// parseSubElement resolves a named sub-element (like PRE, ACC, DN) to a numeric
// sub-element index and optional bit position.
func parseSubElement(name string, result *FileAddress) error {
	name = strings.ToUpper(name)

	switch result.FileType {
	case FileTypeTimer:
		return parseTimerSubElement(name, result)
	case FileTypeCounter:
		return parseCounterSubElement(name, result)
	case FileTypeControl:
		return parseControlSubElement(name, result)
	default:
		// For non-complex types, try parsing as a numeric sub-element
		sub, err := strconv.ParseUint(name, 10, 16)
		if err != nil {
			return fmt.Errorf("unknown sub-element %q for file type %s", name, FileTypeName(result.FileType))
		}
		result.SubElement = uint16(sub)
		return nil
	}
}

func parseTimerSubElement(name string, result *FileAddress) error {
	switch name {
	case "PRE":
		result.SubElement = uint16(TimerPRE)
	case "ACC":
		result.SubElement = uint16(TimerACC)
	case "EN":
		result.SubElement = uint16(TimerControl)
		result.BitNumber = TimerBitEN
	case "TT":
		result.SubElement = uint16(TimerControl)
		result.BitNumber = TimerBitTT
	case "DN":
		result.SubElement = uint16(TimerControl)
		result.BitNumber = TimerBitDN
	default:
		sub, err := strconv.ParseUint(name, 10, 16)
		if err != nil {
			return fmt.Errorf("unknown timer sub-element %q (use PRE, ACC, EN, TT, DN)", name)
		}
		result.SubElement = uint16(sub)
	}
	return nil
}

func parseCounterSubElement(name string, result *FileAddress) error {
	switch name {
	case "PRE":
		result.SubElement = uint16(CounterPRE)
	case "ACC":
		result.SubElement = uint16(CounterACC)
	case "CU":
		result.SubElement = uint16(CounterControl)
		result.BitNumber = CounterBitCU
	case "CD":
		result.SubElement = uint16(CounterControl)
		result.BitNumber = CounterBitCD
	case "DN":
		result.SubElement = uint16(CounterControl)
		result.BitNumber = CounterBitDN
	case "OV":
		result.SubElement = uint16(CounterControl)
		result.BitNumber = CounterBitOV
	case "UN":
		result.SubElement = uint16(CounterControl)
		result.BitNumber = CounterBitUN
	default:
		sub, err := strconv.ParseUint(name, 10, 16)
		if err != nil {
			return fmt.Errorf("unknown counter sub-element %q (use PRE, ACC, CU, CD, DN, OV, UN)", name)
		}
		result.SubElement = uint16(sub)
	}
	return nil
}

func parseControlSubElement(name string, result *FileAddress) error {
	switch name {
	case "LEN":
		result.SubElement = uint16(ControlLEN)
	case "POS":
		result.SubElement = uint16(ControlPOS)
	case "EN":
		result.SubElement = uint16(ControlWord)
		result.BitNumber = ControlBitEN
	case "EU":
		result.SubElement = uint16(ControlWord)
		result.BitNumber = ControlBitEU
	case "DN":
		result.SubElement = uint16(ControlWord)
		result.BitNumber = ControlBitDN
	case "EM":
		result.SubElement = uint16(ControlWord)
		result.BitNumber = ControlBitEM
	case "ER":
		result.SubElement = uint16(ControlWord)
		result.BitNumber = ControlBitER
	case "UL":
		result.SubElement = uint16(ControlWord)
		result.BitNumber = ControlBitUL
	case "IN":
		result.SubElement = uint16(ControlWord)
		result.BitNumber = ControlBitIN
	case "FD":
		result.SubElement = uint16(ControlWord)
		result.BitNumber = ControlBitFD
	default:
		sub, err := strconv.ParseUint(name, 10, 16)
		if err != nil {
			return fmt.Errorf("unknown control sub-element %q (use LEN, POS, EN, EU, DN, EM, ER, UL, IN, FD)", name)
		}
		result.SubElement = uint16(sub)
	}
	return nil
}
