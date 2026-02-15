package driver

import (
	"fmt"
	"strings"

	"github.com/yatesdr/plcio/s7"
)

// S7Adapter wraps s7.Client to implement the Driver interface.
type S7Adapter struct {
	client *s7.Client
	config *PLCConfig
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

// Connect establishes connection to the S7 PLC.
func (a *S7Adapter) Connect() error {
	opts := []s7.Option{s7.WithRackSlot(0, int(a.config.Slot))}
	if a.config.Timeout > 0 {
		opts = append(opts, s7.WithTimeout(a.config.Timeout))
	}

	client, err := s7.Connect(a.config.Address, opts...)
	if err != nil {
		return fmt.Errorf("s7 connect: %w", err)
	}

	a.client = client
	return nil
}

// Close releases the connection.
func (a *S7Adapter) Close() error {
	if a.client != nil {
		a.client.Close()
		a.client = nil
	}
	return nil
}

// IsConnected returns true if connected to the PLC.
func (a *S7Adapter) IsConnected() bool {
	return a.client != nil && a.client.IsConnected()
}

// Family returns the PLC family.
func (a *S7Adapter) Family() PLCFamily {
	return FamilyS7
}

// ConnectionMode returns a description of the connection mode.
func (a *S7Adapter) ConnectionMode() string {
	if a.client == nil {
		return "Not connected"
	}
	return a.client.ConnectionMode()
}

// GetDeviceInfo returns information about the connected PLC.
func (a *S7Adapter) GetDeviceInfo() (*DeviceInfo, error) {
	if a.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	info, err := a.client.GetCPUInfo()
	if err != nil {
		return nil, err
	}

	return &DeviceInfo{
		Family:       FamilyS7,
		Vendor:       "Siemens",
		Model:        info.ModuleTypeName,
		Version:      info.ASName,
		SerialNumber: info.SerialNumber,
		Description:  info.ModuleName,
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
	if a.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	// Convert to s7.TagRequest
	s7Requests := make([]s7.TagRequest, len(requests))
	for i, req := range requests {
		s7Requests[i] = s7.TagRequest{
			Address:  req.Name,
			TypeHint: req.TypeHint,
		}
	}

	values, err := a.client.ReadWithTypes(s7Requests)
	if err != nil {
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
		goValue := v.GoValue()

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

	return result, nil
}

// Write writes a value to a tag.
func (a *S7Adapter) Write(tag string, value interface{}) error {
	if a.client == nil {
		return fmt.Errorf("not connected")
	}

	// Look up the tag's configured type
	typeHint := ""
	if a.config != nil {
		for _, t := range a.config.Tags {
			if strings.EqualFold(t.Name, tag) {
				typeHint = t.DataType
				break
			}
		}
	}

	return a.client.WriteWithType(tag, value, typeHint)
}

// Keepalive is a no-op for S7 (TCP connection is kept alive by OS).
func (a *S7Adapter) Keepalive() error {
	return nil
}

// IsConnectionError returns true if the error indicates a connection problem.
func (a *S7Adapter) IsConnectionError(err error) bool {
	return IsLikelyConnectionError(err)
}

// Client returns the underlying s7.Client for advanced operations.
func (a *S7Adapter) Client() *s7.Client {
	return a.client
}
