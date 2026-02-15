package driver

import (
	"fmt"

	"github.com/yatesdr/plcio/logix"
)

// LogixAdapter wraps logix.Client to implement the Driver interface.
type LogixAdapter struct {
	client   *logix.Client
	config   *PLCConfig
	micro800 bool
}

// NewLogixAdapter creates a new LogixAdapter from configuration.
// The connection is not established until Connect() is called.
func NewLogixAdapter(cfg *PLCConfig) (*LogixAdapter, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}
	return &LogixAdapter{
		config:   cfg,
		micro800: cfg.GetFamily() == FamilyMicro800,
	}, nil
}

// Connect establishes connection to the Logix PLC.
func (a *LogixAdapter) Connect() error {
	opts := []logix.Option{}

	if a.config.Timeout > 0 {
		opts = append(opts, logix.WithTimeout(a.config.Timeout))
	}

	if a.micro800 {
		opts = append(opts, logix.WithMicro800())
	} else if a.config.Slot > 0 {
		opts = append(opts, logix.WithSlot(a.config.Slot))
	}

	client, err := logix.Connect(a.config.Address, opts...)
	if err != nil {
		return fmt.Errorf("logix connect: %w", err)
	}

	a.client = client
	return nil
}

// Close releases the connection.
func (a *LogixAdapter) Close() error {
	if a.client != nil {
		a.client.Close()
		a.client = nil
	}
	return nil
}

// IsConnected returns true if connected to the PLC.
func (a *LogixAdapter) IsConnected() bool {
	return a.client != nil && a.client.IsConnected()
}

// Family returns the PLC family.
func (a *LogixAdapter) Family() PLCFamily {
	if a.micro800 {
		return FamilyMicro800
	}
	return FamilyLogix
}

// ConnectionMode returns a description of the connection mode.
func (a *LogixAdapter) ConnectionMode() string {
	if a.client == nil {
		return "Not connected"
	}
	return a.client.ConnectionMode()
}

// GetDeviceInfo returns information about the connected PLC.
func (a *LogixAdapter) GetDeviceInfo() (*DeviceInfo, error) {
	if a.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	identity, err := a.client.Identity()
	if err != nil {
		return nil, err
	}

	return &DeviceInfo{
		Family:       a.Family(),
		Vendor:       identity.VendorName(),
		Model:        identity.ProductName,
		Version:      identity.Revision,
		SerialNumber: fmt.Sprintf("%08X", identity.Serial),
		Description:  identity.DeviceTypeName(),
	}, nil
}

// SupportsDiscovery returns true since Logix PLCs support tag browsing.
func (a *LogixAdapter) SupportsDiscovery() bool {
	return true
}

// AllTags returns all readable tags from the PLC.
func (a *LogixAdapter) AllTags() ([]TagInfo, error) {
	if a.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	tags, err := a.client.AllTags()
	if err != nil {
		return nil, err
	}

	result := make([]TagInfo, len(tags))
	for i, t := range tags {
		dims := make([]uint32, len(t.Dimensions))
		for j, d := range t.Dimensions {
			dims[j] = uint32(d)
		}
		result[i] = TagInfo{
			Name:       t.Name,
			TypeCode:   t.TypeCode,
			Instance:   t.Instance,
			Dimensions: dims,
			TypeName:   t.TypeName(),
			Writable:   t.IsReadable(), // For Logix, readable tags are also writable
		}
	}

	return result, nil
}

// Programs returns the list of program names.
func (a *LogixAdapter) Programs() ([]string, error) {
	if a.client == nil {
		return nil, fmt.Errorf("not connected")
	}
	return a.client.Programs()
}

// Read reads tag values from the PLC.
func (a *LogixAdapter) Read(requests []TagRequest) ([]*TagValue, error) {
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
			name := "unknown"
			if i < len(names) {
				name = names[i]
			}
			result[i] = &TagValue{
				Name:   name,
				Family: "logix",
				Error:  fmt.Errorf("nil response"),
			}
			continue
		}

		// Use decoded value for structures when possible
		var goValue interface{}
		goValue = v.GoValueDecoded(a.client)

		result[i] = &TagValue{
			Name:        v.Name,
			DataType:    v.DataType,
			Family:      "logix",
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
func (a *LogixAdapter) Write(tag string, value interface{}) error {
	if a.client == nil {
		return fmt.Errorf("not connected")
	}
	return a.client.Write(tag, value)
}

// Keepalive sends a keepalive message to maintain the connection.
func (a *LogixAdapter) Keepalive() error {
	if a.client == nil {
		return nil
	}
	return a.client.Keepalive()
}

// IsConnectionError returns true if the error indicates a connection problem.
func (a *LogixAdapter) IsConnectionError(err error) bool {
	return IsLikelyConnectionError(err)
}

// Client returns the underlying logix.Client for advanced operations.
func (a *LogixAdapter) Client() *logix.Client {
	return a.client
}

// ResolveTagType looks up a tag's symbol table TypeCode by name.
// Returns (typeCode, true) if found, (0, false) otherwise.
func (a *LogixAdapter) ResolveTagType(tagName string) (uint16, bool) {
	if a.client == nil {
		return 0, false
	}
	return a.client.ResolveTagType(tagName)
}

// GetMemberTypes returns member name → type name for a structure type.
func (a *LogixAdapter) GetMemberTypes(typeCode uint16) map[string]string {
	if a.client == nil {
		return nil
	}
	return a.client.GetMemberTypes(typeCode)
}

// SetTags stores discovered tag information for optimized reads.
func (a *LogixAdapter) SetTags(tags []TagInfo) []TagInfo {
	if a.client == nil {
		return tags
	}

	// Convert to logix.TagInfo
	logixTags := make([]logix.TagInfo, len(tags))
	for i, t := range tags {
		dims := make([]int, len(t.Dimensions))
		for j, d := range t.Dimensions {
			dims[j] = int(d)
		}
		logixTags[i] = logix.TagInfo{
			Name:       t.Name,
			TypeCode:   t.TypeCode,
			Instance:   t.Instance,
			Dimensions: dims,
		}
	}

	// Let client update any missing dimensions
	updated := a.client.SetTags(logixTags)

	// Convert back to driver.TagInfo
	result := make([]TagInfo, len(updated))
	for i, t := range updated {
		dims := make([]uint32, len(t.Dimensions))
		for j, d := range t.Dimensions {
			dims[j] = uint32(d)
		}
		result[i] = TagInfo{
			Name:       t.Name,
			TypeCode:   t.TypeCode,
			Instance:   t.Instance,
			Dimensions: dims,
			TypeName:   t.TypeName(),
			Writable:   t.IsReadable(),
		}
	}

	return result
}
