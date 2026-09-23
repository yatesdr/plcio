package driver

import (
	"fmt"
	"strings"
)

// normalizeFamily returns the canonical family for a configured name: matching
// is case-insensitive and ignores surrounding whitespace, so "S7", " Logix "
// and "BECKHOFF" select their families. An empty (or blank) name is Logix.
// Unknown names are returned lower-cased and trimmed.
func normalizeFamily(family PLCFamily) PLCFamily {
	normalized := PLCFamily(strings.ToLower(strings.TrimSpace(string(family))))
	if normalized == "" {
		return FamilyLogix
	}
	return normalized
}

// Create creates a Driver for the given PLC configuration.
// The connection is not established until Connect() is called on the returned driver.
// Family names are matched case-insensitively and whitespace-tolerantly; the
// configuration passed to the adapter carries the canonical family name.
func Create(cfg *PLCConfig) (Driver, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}

	configured := cfg.Family
	family := normalizeFamily(configured)
	if configured != "" && family != configured {
		// Adapters and PLCConfig helpers compare the exact constant, so hand
		// them a copy with the canonical name instead of mutating the caller's.
		normalized := *cfg
		normalized.Family = family
		cfg = &normalized
	}

	switch family {
	case FamilySLC500, FamilyPLC5, FamilyMicroLogix:
		return NewPCCCAdapter(cfg)
	case FamilyS7:
		return NewS7Adapter(cfg)
	case FamilyBeckhoff:
		return NewADSAdapter(cfg)
	case FamilyOmron:
		return NewOmronAdapter(cfg)
	case FamilyLogix, FamilyMicro800:
		return NewLogixAdapter(cfg)
	default:
		// An empty family already maps to Logix via GetFamily.
		return nil, fmt.Errorf("unknown PLC family %q", string(configured))
	}
}
