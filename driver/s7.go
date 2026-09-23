package driver

import (
	"fmt"
	"strings"
	"sync"

	"github.com/yatesdr/plcio/s7"
)

// S7Adapter wraps s7.Client to implement the Driver interface.
type S7Adapter struct {
	client *s7.Client
	config *PLCConfig
	mu     sync.RWMutex
	epoch  uint64 // bumped by Close so an in-flight Connect is discarded
}

// NewS7Adapter creates a new S7Adapter from configuration.
// The connection is not established until Connect() is called.
func NewS7Adapter(cfg *PLCConfig) (*S7Adapter, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}
	return &S7Adapter{
		config: cfg,
	}, nil
}

func (a *S7Adapter) currentClient() *s7.Client {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.client
}

// Connect establishes connection to the S7 PLC.
func (a *S7Adapter) Connect() error {
	a.mu.RLock()
	epoch := a.epoch
	a.mu.RUnlock()
	// Rack stays 0: PLCConfig has no Rack field (its layout is frozen by the
	// v0.3.0 unkeyed-literal compatibility contract). s7.Connect validates
	// the slot (0-31). Native s7.WithRackSlot supports racks 0-7.
	opts := []s7.Option{s7.WithRackSlot(0, int(a.config.Slot))}
	if a.config.Timeout > 0 {
		opts = append(opts, s7.WithTimeout(a.config.Timeout))
	}

	client, err := s7.Connect(a.config.Address, opts...)
	if err != nil {
		return fmt.Errorf("s7 connect: %w", err)
	}

	a.mu.Lock()
	if a.epoch != epoch {
		a.mu.Unlock()
		client.Close()
		return fmt.Errorf("s7 connect superseded by Close")
	}
	previous := a.client
	a.client = client
	a.mu.Unlock()
	if previous != nil {
		previous.Close()
	}
	return nil
}

// Close releases the connection.
func (a *S7Adapter) Close() error {
	a.mu.Lock()
	client := a.client
	a.client = nil
	a.epoch++
	a.mu.Unlock()
	if client != nil {
		client.Close()
	}
	return nil
}

// IsConnected returns true if connected to the PLC.
func (a *S7Adapter) IsConnected() bool {
	client := a.currentClient()
	return client != nil && client.IsConnected()
}

// Family returns the PLC family.
func (a *S7Adapter) Family() PLCFamily {
	return FamilyS7
}

// ConnectionMode returns a description of the connection mode.
func (a *S7Adapter) ConnectionMode() string {
	client := a.currentClient()
	if client == nil {
		return "Not connected"
	}
	return client.ConnectionMode()
}

// GetDeviceInfo returns information about the connected PLC.
func (a *S7Adapter) GetDeviceInfo() (*DeviceInfo, error) {
	client := a.currentClient()
	if client == nil {
		return nil, fmt.Errorf("not connected")
	}

	info, err := client.GetCPUInfo()
	if err != nil {
		return nil, err
	}

	model := info.OrderCode
	if model == "" {
		model = info.ModuleTypeName
	}
	description := info.ModuleName
	if description == "" && info.ModuleTypeName != "S7 PLC" {
		description = info.ModuleTypeName
	}
	return &DeviceInfo{
		Family:       FamilyS7,
		Vendor:       "Siemens",
		Model:        model,
		Version:      info.FirmwareVersion,
		SerialNumber: info.SerialNumber,
		Description:  description,
	}, nil
}

// SupportsDiscovery returns false since S7 PLCs don't support tag browsing.
func (a *S7Adapter) SupportsDiscovery() bool {
	return false
}

// AllTags returns nil since S7 doesn't support tag discovery.
func (a *S7Adapter) AllTags() ([]TagInfo, error) {
	return nil, nil
}

// Programs returns nil since S7 doesn't have the concept of programs.
func (a *S7Adapter) Programs() ([]string, error) {
	return nil, nil
}

// Read reads tag values from the PLC.
func (a *S7Adapter) Read(requests []TagRequest) ([]*TagValue, error) {
	client := a.currentClient()
	if client == nil {
		return nil, fmt.Errorf("not connected")
	}

	// Convert to s7.TagRequest. An explicit hint wins; otherwise use the
	// tag's configured DataType, exactly as Write does, so a read and a write
	// of the same configured tag agree on its width.
	s7Requests := make([]s7.TagRequest, len(requests))
	for i, req := range requests {
		hint := req.TypeHint
		if hint == "" {
			hint = a.configuredType(req.Name)
		}
		s7Requests[i] = s7.TagRequest{
			Address:  req.Name,
			TypeHint: hint,
		}
	}

	values, err := client.ReadWithTypes(s7Requests)
	if err != nil && len(values) == 0 {
		return nil, err
	}

	result := make([]*TagValue, len(values))
	for i, v := range values {
		if v == nil {
			result[i] = &TagValue{
				Name:   requests[i].Name,
				Family: "s7",
				Error:  fmt.Errorf("nil response"),
			}
			continue
		}

		// Get Go value from S7 tag
		goValue := normalizeDecoded(v.GoValue())

		// Handle array type code
		dataType := v.DataType
		if v.Count > 1 {
			dataType = s7.MakeArrayType(dataType)
		}

		result[i] = &TagValue{
			Name:        v.Name,
			DataType:    dataType,
			Family:      "s7",
			Value:       goValue,
			StableValue: goValue,
			Bytes:       v.Bytes,
			Count:       v.Count,
			Error:       v.Error,
		}
	}

	return result, err
}

// Write writes a value to a tag.
func (a *S7Adapter) Write(tag string, value interface{}) error {
	client := a.currentClient()
	if client == nil {
		return fmt.Errorf("not connected")
	}

	// Look up the tag's configured type
	typeHint := a.configuredType(tag)

	addr, err := s7.ParseAddress(tag)
	if err != nil {
		return err
	}
	code := addr.DataType
	if typeHint != "" {
		// The configured type decides the encoding for offset-only and
		// (same-width) sized addresses alike; s7 rejects width mismatches.
		if hinted, ok := s7.TypeCodeFromName(typeHint); ok {
			code = hinted
		}
	} else if code != 0 && addr.BitNum < 0 {
		// Mirrors s7.Client.WriteWithType: a float written to a 4/8-byte
		// sized address (DBD, MD, ...) is a REAL/LREAL of that width.
		switch value.(type) {
		case float32, float64, []float32, []float64:
			switch addr.Size {
			case 4:
				code = s7.TypeReal
			case 8:
				code = s7.TypeLReal
			}
		}
	}
	value, err = s7Canonical(code, value)
	if err != nil {
		return err
	}
	return client.WriteWithType(tag, value, typeHint)
}

// configuredType returns the DataType configured for tag in PLCConfig.Tags
// (case-insensitive name match, first match wins), or "".
func (a *S7Adapter) configuredType(tag string) string {
	if a.config == nil {
		return ""
	}
	for _, t := range a.config.Tags {
		if strings.EqualFold(t.Name, tag) {
			return t.DataType
		}
	}
	return ""
}

// Keepalive performs a lightweight request (SZL 0x0424 CPU status read) so a
// dead link is detected between polls. It returns an error when not
// connected and a connection error (matching s7.ErrConnectionLost) when the
// round trip fails.
func (a *S7Adapter) Keepalive() error {
	client := a.currentClient()
	if client == nil {
		return fmt.Errorf("s7 keepalive: not connected")
	}
	return client.Keepalive()
}

// IsConnectionError returns true if the error indicates a connection problem.
func (a *S7Adapter) IsConnectionError(err error) bool {
	return IsLikelyConnectionError(err)
}

// Client returns the underlying s7.Client for advanced operations.
func (a *S7Adapter) Client() *s7.Client {
	return a.currentClient()
}
