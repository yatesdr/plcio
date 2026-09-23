package driver

import (
	"fmt"
	"strings"
	"sync"

	"github.com/yatesdr/plcio/omron"
)

// OmronAdapter wraps omron.Client to implement the Driver interface.
type OmronAdapter struct {
	client   *omron.Client
	config   *PLCConfig
	protocol string // "fins", "fins-tcp", "fins-udp" or "eip" (normalized)
	mu       sync.RWMutex
	epoch    uint64 // bumped by Close so an in-flight Connect cannot resurrect the client
}

// NewOmronAdapter creates a new OmronAdapter from configuration.
// The connection is not established until Connect() is called.
// An unrecognized protocol is an error rather than a silent FINS fallback.
func NewOmronAdapter(cfg *PLCConfig) (*OmronAdapter, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}
	protocol, err := omronProtocol(cfg.Protocol)
	if err != nil {
		return nil, err
	}
	return &OmronAdapter{
		config:   cfg,
		protocol: protocol,
	}, nil
}

// omronProtocol normalizes an Omron protocol setting (case-insensitive,
// surrounding spaces ignored). "" and "fins" select FINS with TCP-then-UDP
// fallback; "fins-tcp" and "fins-udp" force one FINS transport; "eip" selects
// EtherNet/IP (NJ/NX).
func omronProtocol(p string) (string, error) {
	switch v := strings.ToLower(strings.TrimSpace(p)); v {
	case "", "fins":
		return "fins", nil
	case "fins-tcp", "fins-udp", "eip":
		return v, nil
	default:
		return "", fmt.Errorf("omron: unsupported protocol %q (want \"fins\" (default), \"fins-tcp\", \"fins-udp\" or \"eip\")", p)
	}
}

func (a *OmronAdapter) currentClient() *omron.Client {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.client
}

// Connect establishes connection to the Omron PLC.
// Any previously connected client is closed once the new one is installed.
func (a *OmronAdapter) Connect() error {
	a.mu.RLock()
	epoch := a.epoch
	a.mu.RUnlock()
	opts := []omron.Option{}

	protocol := a.protocol

	if a.config.Timeout > 0 {
		opts = append(opts, omron.WithTimeout(a.config.Timeout))
	}

	if protocol == "eip" {
		opts = append(opts, omron.WithTransport(omron.TransportEIP))
	} else {
		// FINS transport: "fins" tries TCP then UDP; "fins-tcp"/"fins-udp"
		// force one.
		opts = append(opts, omron.WithTransport(omron.Transport(protocol)))

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

	a.mu.Lock()
	if a.epoch != epoch {
		a.mu.Unlock()
		client.Close()
		return fmt.Errorf("omron connect superseded by Close")
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
func (a *OmronAdapter) Close() error {
	a.mu.Lock()
	client := a.client
	a.client = nil
	a.epoch++
	a.mu.Unlock()
	if client != nil {
		return client.Close()
	}
	return nil
}

// IsConnected returns true if connected to the PLC.
func (a *OmronAdapter) IsConnected() bool {
	client := a.currentClient()
	return client != nil && client.IsConnected()
}

// Family returns the PLC family.
func (a *OmronAdapter) Family() PLCFamily {
	return FamilyOmron
}

// ConnectionMode returns a description of the connection mode.
func (a *OmronAdapter) ConnectionMode() string {
	client := a.currentClient()
	if client == nil {
		return "Not connected"
	}
	return client.ConnectionMode()
}

// GetDeviceInfo returns information about the connected PLC.
func (a *OmronAdapter) GetDeviceInfo() (*DeviceInfo, error) {
	client := a.currentClient()
	if client == nil {
		return nil, fmt.Errorf("not connected")
	}

	info, err := client.GetDeviceInfo()
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
	client := a.currentClient()
	if client == nil {
		return nil, fmt.Errorf("not connected")
	}

	if a.protocol != "eip" {
		return nil, nil // FINS doesn't support discovery
	}

	tags, err := client.AllTags()
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
	client := a.currentClient()
	if client == nil {
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

	values, err := client.ReadWithTypes(omronRequests)
	if err != nil && len(values) == 0 {
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
		if omronPrimitive(v.DataType) {
			goValue = normalizePrimitive(goValue)
		}

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

	return result, err
}

// Write writes a value to a tag.
func (a *OmronAdapter) Write(tag string, value interface{}) error {
	client := a.currentClient()
	if client == nil {
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

	if canonicalNumeric(value) {
		var code uint16
		if a.protocol == "eip" {
			values, err := client.Read(tag)
			if err != nil {
				return err
			}
			if len(values) != 1 || values[0] == nil {
				return fmt.Errorf("missing tag type response")
			}
			if values[0].Error != nil {
				return values[0].Error
			}
			code = values[0].DataType
		} else {
			parsed, err := omron.ParseAddressWithType(tag, typeHint)
			if err != nil {
				return err
			}
			code = parsed.TypeCode
		}
		canonical, err := omronCanonical(code, value)
		if err != nil {
			return err
		}
		switch value.(type) {
		case uint64, []uint64:
			// Range-checked above; omron encodes unsigned values natively
			// (and itself rejects a negative int64 for an unsigned type).
		default:
			value = canonical
		}
	}
	return client.WriteWithType(tag, value, typeHint)
}

// Keepalive sends a keepalive to maintain the CIP connection.
func (a *OmronAdapter) Keepalive() error {
	if client := a.currentClient(); client != nil {
		return client.Keepalive()
	}
	return nil
}

// IsConnectionError returns true if the error indicates a connection problem.
func (a *OmronAdapter) IsConnectionError(err error) bool {
	return IsLikelyConnectionError(err)
}

// Client returns the underlying omron.Client for advanced operations.
func (a *OmronAdapter) Client() *omron.Client {
	return a.currentClient()
}

// Match categories actually decoded by Omron, rather than guessing from a width.
func omronPrimitive(code uint16) bool {
	switch omron.BaseType(code) {
	case omron.TypeBool, omron.TypeCIPBool, omron.TypeByte, omron.TypeCIPUSINT,
		omron.TypeSByte, omron.TypeCIPSINT, omron.TypeWord, omron.TypeCIPUINT,
		omron.TypeInt16, omron.TypeCIPINT, omron.TypeDWord, omron.TypeCIPUDINT,
		omron.TypeInt32, omron.TypeCIPDINT, omron.TypeLWord, omron.TypeCIPULINT,
		omron.TypeInt64, omron.TypeCIPLINT, omron.TypeReal, omron.TypeCIPREAL,
		omron.TypeLReal, omron.TypeCIPLREAL, omron.TypeString, omron.TypeCIPSTRING,
		omron.TypeOmronByte, omron.TypeOmronWord, omron.TypeOmronDWord, omron.TypeOmronLWord,
		omron.TypeOmronTime, omron.TypeOmronTimeNSec, omron.TypeOmronTOD, omron.TypeOmronEnum:
		return true
	}
	return false
}
