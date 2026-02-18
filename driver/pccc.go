package driver

import (
	"fmt"

	"github.com/yatesdr/plcio/cip"
	"github.com/yatesdr/plcio/pccc"
)

// PCCCAdapter wraps pccc.Client to implement the Driver interface.
// Supports SLC500, PLC-5, and MicroLogix processors.
type PCCCAdapter struct {
	client *pccc.Client
	config *PLCConfig
}

// NewPCCCAdapter creates a new PCCCAdapter from configuration.
// The connection is not established until Connect() is called.
func NewPCCCAdapter(cfg *PLCConfig) (*PCCCAdapter, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}
	return &PCCCAdapter{config: cfg}, nil
}

// Connect establishes connection to the SLC500/PLC-5/MicroLogix PLC.
func (a *PCCCAdapter) Connect() error {
	opts := []pccc.Option{}

	if a.config.Timeout > 0 {
		opts = append(opts, pccc.WithTimeout(a.config.Timeout))
	}

	if a.config.ConnectionPath != "" {
		routePath, err := cip.ParseConnectionPath(a.config.ConnectionPath)
		if err != nil {
			return fmt.Errorf("invalid connection path %q: %w", a.config.ConnectionPath, err)
		}
		opts = append(opts, pccc.WithRoutePath(routePath))
	}

	switch a.config.GetFamily() {
	case FamilyPLC5:
		opts = append(opts, pccc.WithPLC5())
	case FamilyMicroLogix:
		opts = append(opts, pccc.WithMicroLogix())
	}

	client, err := pccc.Connect(a.config.Address, opts...)
	if err != nil {
		return fmt.Errorf("pccc connect: %w", err)
	}

	a.client = client
	return nil
}

// Close releases the connection.
func (a *PCCCAdapter) Close() error {
	if a.client != nil {
		a.client.Close()
		a.client = nil
	}
	return nil
}

// IsConnected returns true if connected to the PLC.
func (a *PCCCAdapter) IsConnected() bool {
	return a.client != nil && a.client.IsConnected()
}

// Family returns the PLC family.
func (a *PCCCAdapter) Family() PLCFamily {
	return a.config.GetFamily()
}

// ConnectionMode returns a description of the connection mode.
func (a *PCCCAdapter) ConnectionMode() string {
	if a.client == nil {
		return "Not connected"
	}
	return a.client.ConnectionMode()
}

// GetDeviceInfo returns information about the connected PLC.
func (a *PCCCAdapter) GetDeviceInfo() (*DeviceInfo, error) {
	if a.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	identity, err := a.client.GetIdentity()
	if err != nil {
		return nil, err
	}

	return &DeviceInfo{
		Family:       a.config.GetFamily(),
		Vendor:       fmt.Sprintf("Vendor %d", identity.VendorID),
		Model:        identity.ProductName,
		Version:      fmt.Sprintf("%d.%d", identity.RevisionMajor, identity.RevisionMinor),
		SerialNumber: fmt.Sprintf("%08X", identity.SerialNumber),
		Description:  identity.ProductName,
	}, nil
}

// SupportsDiscovery returns false since SLC500/PLC-5 don't support tag browsing over EIP.
func (a *PCCCAdapter) SupportsDiscovery() bool {
	return false
}

// AllTags is not supported for PCCC-based PLCs.
func (a *PCCCAdapter) AllTags() ([]TagInfo, error) {
	return nil, fmt.Errorf("tag discovery not supported for %s", a.config.GetFamily())
}

// Programs is not supported for PCCC-based PLCs.
func (a *PCCCAdapter) Programs() ([]string, error) {
	return nil, fmt.Errorf("program listing not supported for %s", a.config.GetFamily())
}

// Read reads data table addresses from the PLC.
func (a *PCCCAdapter) Read(requests []TagRequest) ([]*TagValue, error) {
	if a.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	addresses := make([]string, len(requests))
	for i, req := range requests {
		addresses[i] = req.Name
	}

	values, err := a.client.Read(addresses...)
	if err != nil {
		return nil, err
	}

	results := make([]*TagValue, len(values))
	for i, v := range values {
		if v == nil {
			name := "unknown"
			if i < len(addresses) {
				name = addresses[i]
			}
			results[i] = &TagValue{
				Name:   name,
				Family: string(a.config.GetFamily()),
				Error:  fmt.Errorf("nil response"),
			}
			continue
		}

		results[i] = &TagValue{
			Name:        v.Name,
			DataType:    uint16(v.FileType),
			Family:      string(a.config.GetFamily()),
			Value:       v.Value,
			StableValue: v.Value,
			Bytes:       v.Bytes,
			Count:       1,
			Error:       v.Error,
		}
	}

	return results, nil
}

// Write writes a value to a data table address.
func (a *PCCCAdapter) Write(tag string, value interface{}) error {
	if a.client == nil {
		return fmt.Errorf("not connected")
	}
	return a.client.Write(tag, value)
}

// Keepalive sends a NOP to maintain the connection.
func (a *PCCCAdapter) Keepalive() error {
	if a.client == nil {
		return nil
	}
	return a.client.Keepalive()
}

// IsConnectionError returns true if the error indicates a connection problem.
func (a *PCCCAdapter) IsConnectionError(err error) bool {
	return IsLikelyConnectionError(err)
}

// Client returns the underlying pccc.Client for advanced operations.
func (a *PCCCAdapter) Client() *pccc.Client {
	return a.client
}
