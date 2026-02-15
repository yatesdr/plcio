package ads

import (
	"fmt"
	"strconv"
	"strings"
)

// AmsNetId represents a 6-byte AMS Network ID.
// Format: "x.x.x.x.x.x" where each x is 0-255.
type AmsNetId [6]byte

// ParseAmsNetId parses an AMS Net ID string (e.g., "192.168.1.100.1.1").
func ParseAmsNetId(s string) (AmsNetId, error) {
	var netId AmsNetId

	if s == "" {
		return netId, fmt.Errorf("empty AMS Net ID")
	}

	parts := strings.Split(s, ".")
	if len(parts) != 6 {
		return netId, fmt.Errorf("invalid AMS Net ID format: %q (expected x.x.x.x.x.x)", s)
	}

	for i, part := range parts {
		val, err := strconv.ParseUint(part, 10, 8)
		if err != nil {
			return netId, fmt.Errorf("invalid AMS Net ID component %q: %w", part, err)
		}
		netId[i] = byte(val)
	}

	return netId, nil
}

// String returns the string representation of the AMS Net ID.
func (n AmsNetId) String() string {
	return fmt.Sprintf("%d.%d.%d.%d.%d.%d", n[0], n[1], n[2], n[3], n[4], n[5])
}

// IsZero returns true if the Net ID is all zeros.
func (n AmsNetId) IsZero() bool {
	return n[0] == 0 && n[1] == 0 && n[2] == 0 && n[3] == 0 && n[4] == 0 && n[5] == 0
}

// AmsNetIdFromIP creates an AMS Net ID from an IP address.
// This is a common convention where the Net ID is IP.1.1 (e.g., 192.168.1.100.1.1).
func AmsNetIdFromIP(ip string) (AmsNetId, error) {
	var netId AmsNetId

	// Remove port if present
	if idx := strings.Index(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}

	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return netId, fmt.Errorf("invalid IP address: %q", ip)
	}

	for i, part := range parts {
		val, err := strconv.ParseUint(part, 10, 8)
		if err != nil {
			return netId, fmt.Errorf("invalid IP address component: %w", err)
		}
		netId[i] = byte(val)
	}

	// Default suffix .1.1 for standard TwinCAT systems
	netId[4] = 1
	netId[5] = 1

	return netId, nil
}

// AmsAddress combines an AMS Net ID and port number.
type AmsAddress struct {
	NetId AmsNetId
	Port  uint16
}

