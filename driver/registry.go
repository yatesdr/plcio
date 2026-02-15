package driver

import (
	"fmt"

)

// Create creates a Driver for the given PLC configuration.
// The connection is not established until Connect() is called on the returned driver.
func Create(cfg *PLCConfig) (Driver, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}

	switch cfg.GetFamily() {
	case FamilyS7:
		return NewS7Adapter(cfg)
	case FamilyBeckhoff:
		return NewADSAdapter(cfg)
	case FamilyOmron:
		return NewOmronAdapter(cfg)
	case FamilyLogix, FamilyMicro800:
		return NewLogixAdapter(cfg)
	default:
		// Default to Logix for unknown families
		return NewLogixAdapter(cfg)
	}
}
