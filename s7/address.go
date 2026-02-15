package s7

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Area represents an S7 memory area.
type Area int

const (
	AreaDB Area = iota // Data Block
	AreaI              // Process Image Input (IB, IW, ID)
	AreaQ              // Process Image Output (QB, QW, QD)
	AreaM              // Merker/Flag (MB, MW, MD)
	AreaT              // Timer
	AreaC              // Counter
)

// String returns the area name.
func (a Area) String() string {
	switch a {
	case AreaDB:
		return "DB"
	case AreaI:
		return "I"
	case AreaQ:
		return "Q"
	case AreaM:
		return "M"
	case AreaT:
		return "T"
	case AreaC:
		return "C"
	default:
		return "?"
	}
}

// Address represents a parsed S7 memory address.
type Address struct {
	Area     Area   // Memory area (DB, I, Q, M, T, C)
	DBNumber int    // Data block number (only for AreaDB)
	Offset   int    // Byte offset
	BitNum   int    // Bit number (0-7 for BOOL, -1 for other types)
	DataType uint16 // Inferred data type
	Size     int    // Size in bytes to read
	Count    int    // Number of elements (1 for scalar, >1 for array)
}

// Regular expressions for parsing S7 addresses
var (
	// DB addresses: DB1.DBX0.0 (bit), DB1.DBB0 (byte), DB1.DBW0 (word), DB1.DBD0 (dword)
	reDB = regexp.MustCompile(`^DB(\d+)\.DB([XBWDL])(\d+)(?:\.(\d))?$`)

	// Simple DB addresses: DB1.0 or DB1.0[6] (offset only, type from config, optional array count)
	reDBSimple = regexp.MustCompile(`^DB(\d+)\.(\d+)(?:\[(\d+)\])?$`)

	// I/Q/M addresses: M0.0 (bit), MB0 (byte), MW0 (word), MD0 (dword)
	reIQM = regexp.MustCompile(`^([IQM])([XBWDL])?(\d+)(?:\.(\d))?$`)

	// Timer/Counter: T0, C0
	reTC = regexp.MustCompile(`^([TC])(\d+)$`)
)

// ParseAddress parses an S7 address string and returns an Address.
// Supported formats:
//   - DB1.0      - Data Block with offset (requires type hint for size)
//   - DB1.DBX0.0 - Data Block bit
//   - DB1.DBB0   - Data Block byte
//   - DB1.DBW0   - Data Block word
//   - DB1.DBD0   - Data Block dword
//   - M0.0       - Merker bit
//   - MB0        - Merker byte
//   - MW0        - Merker word
//   - MD0        - Merker dword
//   - I0.0, IB0, IW0, ID0 - Input
//   - Q0.0, QB0, QW0, QD0 - Output
//   - T0         - Timer
//   - C0         - Counter
func ParseAddress(addr string) (*Address, error) {
	addr = strings.ToUpper(strings.TrimSpace(addr))
	if addr == "" {
		return nil, fmt.Errorf("empty address")
	}

	// Try simple DB address first (DB1.0 format)
	if m := reDBSimple.FindStringSubmatch(addr); m != nil {
		return parseDBSimpleAddress(m)
	}

	// Try full DB address (DB1.DBD0 format)
	if m := reDB.FindStringSubmatch(addr); m != nil {
		return parseDBAddress(m)
	}

	// Try I/Q/M address
	if m := reIQM.FindStringSubmatch(addr); m != nil {
		return parseIQMAddress(m)
	}

	// Try Timer/Counter
	if m := reTC.FindStringSubmatch(addr); m != nil {
		return parseTCAddress(m)
	}

	return nil, fmt.Errorf("invalid S7 address format: %s", addr)
}

// parseDBSimpleAddress parses simple DB addresses like "DB1.0" or "DB1.0[6]" for arrays.
// Returns an address with no type/size - caller must set these based on configuration.
func parseDBSimpleAddress(m []string) (*Address, error) {
	dbNum, _ := strconv.Atoi(m[1])
	offset, _ := strconv.Atoi(m[2])

	count := 1
	if m[3] != "" {
		count, _ = strconv.Atoi(m[3])
		if count < 1 {
			count = 1
		}
	}

	return &Address{
		Area:     AreaDB,
		DBNumber: dbNum,
		Offset:   offset,
		BitNum:   -1,
		DataType: 0,     // Must be set by caller based on config
		Size:     0,     // Must be set by caller based on config
		Count:    count, // Number of elements (1 for scalar, >1 for array)
	}, nil
}

func parseDBAddress(m []string) (*Address, error) {
	dbNum, _ := strconv.Atoi(m[1])
	typeLetter := m[2]
	offset, _ := strconv.Atoi(m[3])

	addr := &Address{
		Area:     AreaDB,
		DBNumber: dbNum,
		Offset:   offset,
		BitNum:   -1,
		Count:    1, // Scalar by default
	}

	switch typeLetter {
	case "X":
		// Bit access: DBX requires bit number
		if m[4] == "" {
			return nil, fmt.Errorf("DBX requires bit number (e.g., DB1.DBX0.0)")
		}
		bitNum, _ := strconv.Atoi(m[4])
		if bitNum < 0 || bitNum > 7 {
			return nil, fmt.Errorf("bit number must be 0-7, got %d", bitNum)
		}
		addr.BitNum = bitNum
		addr.DataType = TypeBool
		addr.Size = 1
	case "B":
		addr.DataType = TypeByte
		addr.Size = 1
	case "W":
		addr.DataType = TypeWord
		addr.Size = 2
	case "D":
		addr.DataType = TypeDWord
		addr.Size = 4
	case "L":
		addr.DataType = TypeLInt
		addr.Size = 8
	default:
		return nil, fmt.Errorf("unknown DB type: %s", typeLetter)
	}

	return addr, nil
}

func parseIQMAddress(m []string) (*Address, error) {
	var area Area
	switch m[1] {
	case "I":
		area = AreaI
	case "Q":
		area = AreaQ
	case "M":
		area = AreaM
	}

	typeLetter := m[2]
	if typeLetter == "" {
		typeLetter = "X" // Default to bit if no type specified
	}
	offset, _ := strconv.Atoi(m[3])

	addr := &Address{
		Area:   area,
		Offset: offset,
		BitNum: -1,
		Count:  1, // Scalar by default
	}

	switch typeLetter {
	case "X":
		// Bit access
		if m[4] != "" {
			bitNum, _ := strconv.Atoi(m[4])
			if bitNum < 0 || bitNum > 7 {
				return nil, fmt.Errorf("bit number must be 0-7, got %d", bitNum)
			}
			addr.BitNum = bitNum
		} else {
			// Default to bit 0 if no bit specified (M0 means M0.0)
			addr.BitNum = 0
		}
		addr.DataType = TypeBool
		addr.Size = 1
	case "B":
		addr.DataType = TypeByte
		addr.Size = 1
	case "W":
		addr.DataType = TypeWord
		addr.Size = 2
	case "D":
		addr.DataType = TypeDWord
		addr.Size = 4
	case "L":
		addr.DataType = TypeLInt
		addr.Size = 8
	default:
		return nil, fmt.Errorf("unknown type: %s", typeLetter)
	}

	return addr, nil
}

func parseTCAddress(m []string) (*Address, error) {
	var area Area
	switch m[1] {
	case "T":
		area = AreaT
	case "C":
		area = AreaC
	}

	num, _ := strconv.Atoi(m[2])

	return &Address{
		Area:     area,
		Offset:   num,
		BitNum:   -1,
		DataType: TypeWord, // Timers and counters are 16-bit
		Size:     2,
		Count:    1, // Scalar by default
	}, nil
}

// ValidateAddress checks if an address string is valid.
func ValidateAddress(addr string) error {
	_, err := ParseAddress(addr)
	return err
}
