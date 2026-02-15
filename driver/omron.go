package driver

import (
	"fmt"
	"strings"

	"github.com/yatesdr/plcio/omron"
)

// OmronAdapter wraps omron.Client to implement the Driver interface.
type OmronAdapter struct {
	client   *omron.Client
	config   *PLCConfig
	protocol string // "fins" or "eip"
}

// NewOmronAdapter creates a new OmronAdapter from configuration.
// The connection is not established until Connect() is called.
func NewOmronAdapter(cfg *PLCConfig) (*OmronAdapter, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}
	return &OmronAdapter{
		config:   cfg,
		protocol: strings.ToLower(cfg.Protocol),
	}, nil
}

// Connect establishes connection to the Omron PLC.
func (a *OmronAdapter) Connect() error {
	opts := []omron.Option{}

	protocol := a.protocol
	if protocol == "" {
		protocol = "fins" // Default to FINS
	}

	if a.config.Timeout > 0 {
		opts = append(opts, omron.WithTimeout(a.config.Timeout))
	}

	if protocol == "eip" {
		opts = append(opts, omron.WithTransport(omron.TransportEIP))
	} else {
		// FINS transport
		opts = append(opts, omron.WithTransport(omron.TransportFINS))

		if a.config.FinsPort > 0 {
			opts = append(opts, omron.WithPort(a.config.FinsPort))
		}
		// Always apply FINS addressing settings - even 0 is a valid value
		// Node is typically the last octet of the PLC's IP address
		opts = append(opts, omron.WithNetwork(a.config.FinsNetwork))
		opts = append(opts, omron.WithNode(a.config.FinsNode))
		opts = append(opts, omron.WithUnit(a.config.FinsUnit))
	}

	client, err := omron.Connect(a.config.Address, opts...)
	if err != nil {
		return fmt.Errorf("omron connect: %w", err)
	}

	a.client = client
	return nil
}

// Close releases the connection.
func (a *OmronAdapter) Close() error {
	if a.client != nil {
		err := a.client.Close()
		a.client = nil
		return err
	}
	return nil
}

// IsConnected returns true if connected to the PLC.
func (a *OmronAdapter) IsConnected() bool {
	return a.client != nil && a.client.IsConnected()
}

// Family returns the PLC family.
func (a *OmronAdapter) Family() PLCFamily {
	return FamilyOmron
}

// ConnectionMode returns a description of the connection mode.
func (a *OmronAdapter) ConnectionMode() string {
	if a.client == nil {
		return "Not connected"
	}
	return a.client.ConnectionMode()
}

// GetDeviceInfo returns information about the connected PLC.
func (a *OmronAdapter) GetDeviceInfo() (*DeviceInfo, error) {
	if a.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	info, err := a.client.GetDeviceInfo()
	if err != nil {
		return nil, err
	}

	return &DeviceInfo{
		Family:       FamilyOmron,
		Vendor:       "Omron",
		Model:        info.Model,
		Version:      info.Version,
		SerialNumber: fmt.Sprintf("%d", info.SerialNumber),
		Description:  info.CPUType,
	}, nil
}

// SupportsDiscovery returns true if using EIP protocol.
func (a *OmronAdapter) SupportsDiscovery() bool {
	return a.protocol == "eip"
}

// AllTags returns all tags (EIP only).
func (a *OmronAdapter) AllTags() ([]TagInfo, error) {
	if a.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	if a.protocol != "eip" {
		return nil, nil // FINS doesn't support discovery
	}

	tags, err := a.client.AllTags()
	if err != nil {
		return nil, err
	}

	result := make([]TagInfo, len(tags))
	for i, t := range tags {
		dims := make([]uint32, len(t.Dimensions))
		for j, d := range t.Dimensions {
			dims[j] = d
		}
		result[i] = TagInfo{
			Name:       t.Name,
			TypeCode:   t.TypeCode,
			Instance:   t.Instance,
			Dimensions: dims,
			TypeName:   omron.TypeName(t.TypeCode),
			Writable:   true, // Assume writable for now
		}
	}

	return result, nil
}

// Programs returns nil since Omron doesn't have programs like Logix.
func (a *OmronAdapter) Programs() ([]string, error) {
	return nil, nil
}

// Read reads tag values from the PLC.
func (a *OmronAdapter) Read(requests []TagRequest) ([]*TagValue, error) {
	if a.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	// Convert to omron.TagRequest
	omronRequests := make([]omron.TagRequest, len(requests))
	for i, req := range requests {
		omronRequests[i] = omron.TagRequest{
			Address:  req.Name,
			TypeHint: req.TypeHint,
		}
	}

	values, err := a.client.ReadWithTypes(omronRequests)
	if err != nil {
		return nil, err
	}

	result := make([]*TagValue, len(values))
	for i, v := range values {
		if v == nil {
			result[i] = &TagValue{
				Name:   requests[i].Name,
				Family: "omron",
				Error:  fmt.Errorf("nil response"),
			}
			continue
		}

		goValue := v.GoValue()

		result[i] = &TagValue{
			Name:        v.Name,
			DataType:    v.DataType,
			Family:      "omron",
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
func (a *OmronAdapter) Write(tag string, value interface{}) error {
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

// Keepalive sends a keepalive to maintain the CIP connection.
func (a *OmronAdapter) Keepalive() error {
	if a.client != nil {
		return a.client.Keepalive()
	}
	return nil
}

// IsConnectionError returns true if the error indicates a connection problem.
func (a *OmronAdapter) IsConnectionError(err error) bool {
	return IsLikelyConnectionError(err)
}

// Client returns the underlying omron.Client for advanced operations.
func (a *OmronAdapter) Client() *omron.Client {
	return a.client
}
