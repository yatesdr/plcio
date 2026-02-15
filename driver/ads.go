package driver

import (
	"fmt"

	"github.com/yatesdr/plcio/ads"
)

// ADSAdapter wraps ads.Client to implement the Driver interface.
type ADSAdapter struct {
	client *ads.Client
	config *PLCConfig
}

// NewADSAdapter creates a new ADSAdapter from configuration.
// The connection is not established until Connect() is called.
func NewADSAdapter(cfg *PLCConfig) (*ADSAdapter, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}
	return &ADSAdapter{
		config: cfg,
	}, nil
}

// Connect establishes connection to the TwinCAT PLC.
func (a *ADSAdapter) Connect() error {
	opts := []ads.Option{}

	if a.config.Timeout > 0 {
		opts = append(opts, ads.WithTimeout(a.config.Timeout))
	}
	if a.config.AmsNetId != "" {
		opts = append(opts, ads.WithAmsNetId(a.config.AmsNetId))
	}
	if a.config.AmsPort > 0 {
		opts = append(opts, ads.WithAmsPort(a.config.AmsPort))
	}

	client, err := ads.Connect(a.config.Address, opts...)
	if err != nil {
		return fmt.Errorf("ads connect: %w", err)
	}

	a.client = client
	return nil
}

// Close releases the connection.
func (a *ADSAdapter) Close() error {
	if a.client != nil {
		a.client.Close()
		a.client = nil
	}
	return nil
}

// IsConnected returns true if connected to the PLC.
func (a *ADSAdapter) IsConnected() bool {
	return a.client != nil && a.client.IsConnected()
}

// Family returns the PLC family.
func (a *ADSAdapter) Family() PLCFamily {
	return FamilyBeckhoff
}

// ConnectionMode returns a description of the connection mode.
func (a *ADSAdapter) ConnectionMode() string {
	if a.client == nil {
		return "Not connected"
	}
	return a.client.ConnectionMode()
}

// GetDeviceInfo returns information about the connected PLC.
func (a *ADSAdapter) GetDeviceInfo() (*DeviceInfo, error) {
	if a.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	info, err := a.client.GetDeviceInfo()
	if err != nil {
		return nil, err
	}

	return &DeviceInfo{
		Family:       FamilyBeckhoff,
		Vendor:       "Beckhoff",
		Model:        info.DeviceName,
		Version:      fmt.Sprintf("%d.%d.%d", info.MajorVersion, info.MinorVersion, info.BuildVersion),
		SerialNumber: "",
		Description:  "TwinCAT PLC",
	}, nil
}

// SupportsDiscovery returns true since TwinCAT supports symbol discovery.
func (a *ADSAdapter) SupportsDiscovery() bool {
	return true
}

// AllTags returns all symbols from the PLC.
func (a *ADSAdapter) AllTags() ([]TagInfo, error) {
	if a.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	tags, err := a.client.AllTags()
	if err != nil {
		return nil, err
	}

	result := make([]TagInfo, len(tags))
	for i, t := range tags {
		result[i] = TagInfo{
			Name:     t.Name,
			TypeCode: t.TypeCode,
			TypeName: t.TypeName,
			Writable: t.IsWritable(),
		}
	}

	return result, nil
}

// Programs returns "MAIN" and "GVL" as common TwinCAT POUs.
func (a *ADSAdapter) Programs() ([]string, error) {
	if a.client == nil {
		return nil, fmt.Errorf("not connected")
	}
	return a.client.Programs()
}

// Read reads tag values from the PLC.
func (a *ADSAdapter) Read(requests []TagRequest) ([]*TagValue, error) {
	if a.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	names := make([]string, len(requests))
	for i, req := range requests {
		names[i] = req.Name
	}

	values, err := a.client.Read(names...)
	if err != nil {
		return nil, err
	}

	result := make([]*TagValue, len(values))
	for i, v := range values {
		if v == nil {
			result[i] = &TagValue{
				Name:   names[i],
				Family: "ads",
				Error:  fmt.Errorf("nil response"),
			}
			continue
		}

		goValue := v.GoValue()

		result[i] = &TagValue{
			Name:        v.Name,
			DataType:    v.DataType,
			Family:      "ads",
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
func (a *ADSAdapter) Write(tag string, value interface{}) error {
	if a.client == nil {
		return fmt.Errorf("not connected")
	}
	return a.client.Write(tag, value)
}

// Keepalive is a no-op for ADS (TCP keepalive handles connection maintenance).
func (a *ADSAdapter) Keepalive() error {
	return nil
}

// IsConnectionError returns true if the error indicates a connection problem.
func (a *ADSAdapter) IsConnectionError(err error) bool {
	return IsLikelyConnectionError(err)
}

// Client returns the underlying ads.Client for advanced operations.
func (a *ADSAdapter) Client() *ads.Client {
	return a.client
}
