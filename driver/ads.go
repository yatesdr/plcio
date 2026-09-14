package driver

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/yatesdr/plcio/ads"
	"github.com/yatesdr/plcio/internal/adsbridge"
	"github.com/yatesdr/plcio/metadata"
)

// ADSAdapter wraps ads.Client to implement the Driver interface.
type ADSAdapter struct {
	client *ads.Client
	config *PLCConfig
	opts   []ads.Option
	mu     sync.RWMutex
	epoch  uint64
}

// NewADSAdapter creates a new ADSAdapter from configuration.
// The connection is not established until Connect() is called.
func NewADSAdapter(cfg *PLCConfig) (*ADSAdapter, error) {
	return NewADSAdapterWithOptions(cfg)
}

// NewADSAdapterWithOptions exposes ADS identity and resource options without
// changing the shared configuration or the published NewADSAdapter signature.
func NewADSAdapterWithOptions(cfg *PLCConfig, opts ...ads.Option) (*ADSAdapter, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}
	return &ADSAdapter{config: cfg, opts: append([]ads.Option(nil), opts...)}, nil
}

func (a *ADSAdapter) currentClient() *ads.Client {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.client
}

// Connect establishes connection to the TwinCAT PLC.
func (a *ADSAdapter) Connect() error {
	a.mu.RLock()
	epoch := a.epoch
	a.mu.RUnlock()
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

	opts = append(opts, a.opts...)
	client, err := ads.Connect(a.config.Address, opts...)
	if err != nil {
		return fmt.Errorf("ads connect: %w", err)
	}

	a.mu.Lock()
	if a.epoch != epoch {
		a.mu.Unlock()
		client.Close()
		return fmt.Errorf("ADS connect superseded by Close")
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
func (a *ADSAdapter) Close() error {
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
func (a *ADSAdapter) IsConnected() bool {
	client := a.currentClient()
	return client != nil && client.IsConnected()
}

// Family returns the PLC family.
func (a *ADSAdapter) Family() PLCFamily {
	return FamilyBeckhoff
}

// ConnectionMode returns a description of the connection mode.
func (a *ADSAdapter) ConnectionMode() string {
	client := a.currentClient()
	if client == nil {
		return "Not connected"
	}
	return client.ConnectionMode()
}

// GetDeviceInfo returns information about the connected PLC.
func (a *ADSAdapter) GetDeviceInfo() (*DeviceInfo, error) {
	client := a.currentClient()
	if client == nil {
		return nil, fmt.Errorf("not connected")
	}

	info, err := client.GetDeviceInfo()
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
	client := a.currentClient()
	if client == nil {
		return nil, fmt.Errorf("not connected")
	}

	tags, deadline, err := adsbridge.Catalog(client)
	if err != nil {
		return nil, err
	}

	result := make([]TagInfo, len(tags))
	for i, t := range tags {
		if i%64 == 0 && !time.Now().Before(deadline) {
			return nil, fmt.Errorf("ADS catalog projection: %w", context.DeadlineExceeded)
		}
		result[i] = TagInfo{
			Name:       t.Name,
			TypeCode:   t.TypeCode,
			TypeName:   t.TypeName,
			Writable:   t.Writable,
			Dimensions: t.Dimensions,
		}
	}
	if !time.Now().Before(deadline) {
		return nil, fmt.Errorf("ADS catalog projection: %w", context.DeadlineExceeded)
	}

	return result, nil
}

// Programs returns the published top-level namespaces from the catalog.
func (a *ADSAdapter) Programs() ([]string, error) {
	client := a.currentClient()
	if client == nil {
		return nil, fmt.Errorf("not connected")
	}
	return client.Programs()
}

// Read reads tag values from the PLC.
func (a *ADSAdapter) Read(requests []TagRequest) ([]*TagValue, error) {
	client := a.currentClient()
	if client == nil {
		return nil, fmt.Errorf("not connected")
	}

	names := make([]string, len(requests))
	for i, req := range requests {
		names[i] = req.Name
	}

	values, err := client.ReadDecoded(names...)
	result := make([]*TagValue, len(values))
	for i, v := range values {
		if v == nil || v.Raw == nil {
			result[i] = &TagValue{
				Name:   names[i],
				Family: "ads",
				Error:  fmt.Errorf("nil response"),
			}
			continue
		}

		goValue := v.Value
		raw := v.Raw

		result[i] = &TagValue{
			Name:        raw.Name,
			DataType:    raw.DataType,
			Family:      "ads",
			Value:       goValue,
			StableValue: goValue,
			Bytes:       raw.Bytes,
			Count:       raw.Count,
			Error:       raw.Error,
		}
	}

	return result, err
}

// Write writes a value to a tag.
func (a *ADSAdapter) Write(tag string, value interface{}) error {
	client := a.currentClient()
	if client == nil {
		return fmt.Errorf("not connected")
	}
	return client.Write(tag, value)
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
	return a.currentClient()
}

// Describe inspects a current ADS symbol without changing the ordinary I/O flow.
func (a *ADSAdapter) Describe(request TagRequest) (*metadata.Symbol, error) {
	client := a.currentClient()
	if client == nil {
		return nil, fmt.Errorf("not connected")
	}
	return client.Describe(request.Name)
}

var _ Describer = (*ADSAdapter)(nil)
