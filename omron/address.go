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

// ParseAddress parses a FINS address string into its components.
// Supports formats:
//   - DM100 - Word address
//   - DM100[10] - Array of 10 words
//   - DM100.5 - Bit address (bit 5 of DM100)
//   - CIO0, HR10, WR5, AR20 - Other memory areas
//   - EM0:100 - Extended memory bank 0, address 100
func ParseAddress(addr string) (*ParsedAddress, error) {
	addr = strings.ToUpper(strings.TrimSpace(addr))

	// Try bit address pattern first
	if matches := bitAddrPattern.FindStringSubmatch(addr); matches != nil {
		areaName := matches[1]
		wordAddr, _ := strconv.ParseUint(matches[2], 10, 16)
		bitOffset, _ := strconv.ParseUint(matches[3], 10, 8)
		count := 1
		if matches[4] != "" {
			count, _ = strconv.Atoi(matches[4])
		}

		area, ok := AreaFromName(areaName)
		if !ok {
			return nil, fmt.Errorf("unknown memory area: %s", areaName)
		}

		if bitOffset > 15 {
			return nil, fmt.Errorf("bit offset must be 0-15, got %d", bitOffset)
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
		wordAddr, _ := strconv.ParseUint(matches[2], 10, 16)
		count := 1
		if matches[3] != "" {
			count, _ = strconv.Atoi(matches[3])
		}

		area, ok := AreaFromName(areaName)
		if !ok {
			return nil, fmt.Errorf("unknown memory area: %s", areaName)
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

// ParseAddressWithType parses a FINS address and applies the type hint.
func ParseAddressWithType(addr string, typeHint string) (*ParsedAddress, error) {
	parsed, err := ParseAddress(addr)
	if err != nil {
		return nil, err
	}

	// Apply type hint if provided
	if typeHint != "" {
		if tc, ok := TypeCodeFromName(typeHint); ok {
			parsed.TypeCode = tc
		}
	}

	return parsed, nil
}

// ValidateAddress checks if an address string is valid.
func ValidateAddress(addr string) error {
	_, err := ParseAddress(addr)
	return err
}

