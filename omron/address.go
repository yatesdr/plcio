package omron

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ParsedAddress holds the parsed components of a FINS address.
type ParsedAddress struct {
	MemoryArea byte
	Address    uint16
	BitOffset  byte
	TypeCode   uint16
	Count      int
}

// Address parsing regex patterns.
var (
	// Pattern: DM100, CIO50, HR10, WR5, AR20, EM0:100
	// Note: Only EM (extended memory) areas have a digit suffix (EM0-EM9, EMA-EMC)
	// The area name is captured as letters only, with special handling for EM banks
	wordAddrPattern = regexp.MustCompile(`^(EM[0-9A-C]|[A-Z]+)(?::)?(\d+)(?:\[(\d+)\])?$`)
	// Pattern: DM100.5, CIO50.0 (bit access)
	bitAddrPattern = regexp.MustCompile(`^(EM[0-9A-C]|[A-Z]+)(?::)?(\d+)\.(\d+)(?:\[(\d+)\])?$`)
)

// Timer/counter present values (CS/CJ/CP): memory area 0x89 (word access),
// timer PV Tn at address n, counter PV Cn at address 0x8000+n (W227/W342).
const (
	maxTimerCounterNumber = 4095
	counterPVOffset       = 0x8000
	maxTaskFlag           = 31 // TK0000..TK0031 (W342 5-2-2)
)

// ParseAddress parses a FINS address string into its components.
// Supports formats:
//   - DM100 - Word address
//   - DM100[10] - Array of 10 words
//   - DM100.5 - Bit address (bit 5 of DM100)
//   - CIO0, HR10, WR5, AR20 - Other memory areas
//   - EM0:100 - Extended memory bank 0, address 100
//   - TIM5, CNT5 - Timer / counter present value (word access only)
//
// Bare "C" and "T" prefixes are rejected as ambiguous (Omron uses them for
// counters/timers); use CIO, TK, TIM or CNT explicitly.
func ParseAddress(addr string) (*ParsedAddress, error) {
	parsed, err := parseAddress(addr)
	if err != nil {
		return nil, err
	}
	if err := checkAddressRange(parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

func parseAddress(addr string) (*ParsedAddress, error) {
	addr = strings.ToUpper(strings.TrimSpace(addr))

	// Try bit address pattern first
	if matches := bitAddrPattern.FindStringSubmatch(addr); matches != nil {
		areaName := matches[1]
		wordAddr, err := strconv.ParseUint(matches[2], 10, 16)
		if err != nil {
			return nil, fmt.Errorf("address %s out of range 0-65535", matches[2])
		}
		bitOffset, err := strconv.ParseUint(matches[3], 10, 8)
		if err != nil || bitOffset > 15 {
			return nil, fmt.Errorf("bit offset must be 0-15, got %s", matches[3])
		}
		count, err := parseCount(matches[4])
		if err != nil {
			return nil, err
		}

		if areaName == "TIM" || areaName == "CNT" {
			return nil, fmt.Errorf("bit access is not supported for %s (present value is word access only)", areaName)
		}
		if areaName == "TK" {
			return nil, fmt.Errorf("task flags are addressed TK0..TK%d without a bit number", maxTaskFlag)
		}
		area, err := areaFromPrefix(areaName)
		if err != nil {
			return nil, err
		}

		return &ParsedAddress{
			MemoryArea: area,
			Address:    uint16(wordAddr),
			BitOffset:  byte(bitOffset),
			TypeCode:   TypeBool,
			Count:      count,
		}, nil
	}

	// Try word address pattern
	if matches := wordAddrPattern.FindStringSubmatch(addr); matches != nil {
		areaName := matches[1]
		wordAddr, err := strconv.ParseUint(matches[2], 10, 16)
		if err != nil {
			return nil, fmt.Errorf("address %s out of range 0-65535", matches[2])
		}
		count, err := parseCount(matches[3])
		if err != nil {
			return nil, err
		}

		var area byte
		switch areaName {
		case "TK":
			// W342 5-2-2: task flag TKnnnn is address nnnn, bit 00 of
			// memory area 06 (bit access only, one byte per flag).
			if wordAddr > maxTaskFlag || count != 1 {
				return nil, fmt.Errorf("task flag must be TK0..TK%d (single flag), got %s", maxTaskFlag, addr)
			}
			return &ParsedAddress{MemoryArea: AreaTaskBit, Address: uint16(wordAddr), TypeCode: TypeBool, Count: 1}, nil
		case "TIM", "CNT":
			if wordAddr > maxTimerCounterNumber {
				return nil, fmt.Errorf("%s number must be 0-%d, got %d", areaName, maxTimerCounterNumber, wordAddr)
			}
			if wordAddr+uint64(count)-1 > maxTimerCounterNumber {
				return nil, fmt.Errorf("%s%d[%d] exceeds %s%d", areaName, wordAddr, count, areaName, maxTimerCounterNumber)
			}
			area = AreaTimerCounterPV
			if areaName == "CNT" {
				wordAddr += counterPVOffset
			}
		default:
			area, err = areaFromPrefix(areaName)
			if err != nil {
				return nil, err
			}
		}

		return &ParsedAddress{
			MemoryArea: area,
			Address:    uint16(wordAddr),
			BitOffset:  0,
			TypeCode:   TypeWord, // Default to WORD
			Count:      count,
		}, nil
	}

	return nil, fmt.Errorf("invalid FINS address format: %s", addr)
}

// parseCount parses the optional [n] element count.
func parseCount(s string) (int, error) {
	if s == "" {
		return 1, nil
	}
	count, err := strconv.Atoi(s)
	if err != nil || count < 1 || count > 65535 {
		return 0, fmt.Errorf("element count must be 1-65535, got %s", s)
	}
	return count, nil
}

// areaFromPrefix resolves an area prefix, rejecting the ambiguous bare "C"
// and "T" prefixes with an explanatory error.
func areaFromPrefix(name string) (byte, error) {
	switch name {
	case "C":
		return 0, fmt.Errorf("ambiguous memory area prefix %q: in Omron notation C means counter; use CIO for the CIO area or CNT for a counter present value", name)
	case "T":
		return 0, fmt.Errorf("ambiguous memory area prefix %q: in Omron notation T means timer; use TIM for a timer present value or TK for task flags", name)
	}
	area, ok := AreaFromName(name)
	if !ok {
		return 0, fmt.Errorf("unknown memory area: %s", name)
	}
	return area, nil
}

// checkAddressRange rejects addresses whose data would run past word 65535.
func checkAddressRange(p *ParsedAddress) error {
	last := uint64(p.Address)
	if p.TypeCode == TypeBool {
		last += (uint64(p.BitOffset) + uint64(p.Count) - 1) / 16
	} else {
		words := (uint64(TypeSize(p.TypeCode))*uint64(p.Count) + 1) / 2
		if words < 1 {
			words = 1
		}
		last += words - 1
	}
	if last > 0xFFFF {
		return fmt.Errorf("address range %d..%d exceeds word 65535", p.Address, last)
	}
	return nil
}

// ParseAddressWithType parses a FINS address and applies the type hint.
// The hint is case-insensitive. An unknown hint is an error (it used to be
// ignored, silently reading the address as WORD), and a bit address (DM100.5,
// TK3) only accepts BOOL: any other type would read the whole word and drop
// the bit number.
func ParseAddressWithType(addr string, typeHint string) (*ParsedAddress, error) {
	parsed, err := parseAddress(addr)
	if err != nil {
		return nil, err
	}

	// Apply type hint if provided
	if hint := strings.ToUpper(strings.TrimSpace(typeHint)); hint != "" {
		tc, ok := TypeCodeFromName(hint)
		if !ok || tc == TypeVoid {
			return nil, fmt.Errorf("unsupported FINS type %q for %s (supported: %s)", typeHint, addr, strings.Join(SupportedTypeNames(), ", "))
		}
		if parsed.TypeCode == TypeBool && tc != TypeBool {
			return nil, fmt.Errorf("type %s cannot be used with bit address %s; use BOOL or a word address", hint, addr)
		}
		parsed.TypeCode = tc
	}

	if err := checkAddressRange(parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

// ValidateAddress checks if an address string is valid.
func ValidateAddress(addr string) error {
	_, err := ParseAddress(addr)
	return err
}

