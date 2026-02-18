package driver

import "time"

// PLCFamily represents the type/protocol family of a PLC.
type PLCFamily string

const (
	FamilyLogix     PLCFamily = "logix"     // Allen-Bradley ControlLogix/CompactLogix
	FamilyMicro800  PLCFamily = "micro800"  // Allen-Bradley Micro800 series
	FamilySLC500    PLCFamily = "slc500"    // Allen-Bradley SLC 5/03, 5/04, 5/05
	FamilyPLC5      PLCFamily = "plc5"      // Allen-Bradley PLC-5 series
	FamilyMicroLogix PLCFamily = "micrologix" // Allen-Bradley MicroLogix 1000/1100/1200/1400/1500
	FamilyS7        PLCFamily = "s7"        // Siemens S7
	FamilyOmron     PLCFamily = "omron"     // Omron PLCs (FINS or EIP based on Protocol field)
	FamilyBeckhoff  PLCFamily = "beckhoff"  // Beckhoff TwinCAT (ADS protocol)
)

// SupportsDiscovery returns true if the PLC family supports tag discovery.
// Note: For Omron PLCs, discovery depends on the protocol (EIP supports it, FINS doesn't).
// Use PLCConfig.SupportsDiscovery() for protocol-aware check.
func (f PLCFamily) SupportsDiscovery() bool {
	return f == FamilyLogix || f == "" || f == FamilyMicro800 || f == FamilyBeckhoff
}

// String returns the string representation of the PLC family.
func (f PLCFamily) String() string {
	if f == "" {
		return "logix"
	}
	return string(f)
}

// Driver returns the driver/protocol name used by this PLC family.
func (f PLCFamily) Driver() string {
	switch f {
	case FamilySLC500, FamilyPLC5, FamilyMicroLogix:
		return "pccc"
	case FamilyS7:
		return "s7"
	case FamilyBeckhoff:
		return "ads"
	case FamilyOmron:
		return "omron"
	default:
		return "logix"
	}
}

// PLCConfig stores configuration for a single PLC connection.
type PLCConfig struct {
	Name               string         `yaml:"name"`
	Address            string         `yaml:"address"`
	Slot               byte           `yaml:"slot"`
	Family             PLCFamily      `yaml:"family,omitempty"`
	Enabled            bool           `yaml:"enabled"`
	DiscoverTags       *bool          `yaml:"discover_tags,omitempty"`
	HealthCheckEnabled *bool          `yaml:"health_check_enabled,omitempty"`
	PollRate           time.Duration  `yaml:"poll_rate,omitempty"`
	Timeout            time.Duration  `yaml:"timeout,omitempty"`
	Tags               []TagSelection `yaml:"tags,omitempty"`

	// Logix/CIP-specific settings
	ConnectionPath string `yaml:"connection_path,omitempty"` // Rockwell-style route, e.g. "1,0" or "1,1,2,192.168.100.1"

	// Beckhoff/TwinCAT-specific settings
	AmsNetId string `yaml:"ams_net_id,omitempty"`
	AmsPort  uint16 `yaml:"ams_port,omitempty"`

	// Omron-specific settings
	Protocol    string `yaml:"protocol,omitempty"`
	FinsPort    int    `yaml:"fins_port,omitempty"`
	FinsNetwork byte   `yaml:"fins_network,omitempty"`
	FinsNode    byte   `yaml:"fins_node,omitempty"`
	FinsUnit    byte   `yaml:"fins_unit,omitempty"`
}

// GetFamily returns the PLC family, defaulting to logix if not set.
func (p *PLCConfig) GetFamily() PLCFamily {
	if p.Family == "" {
		return FamilyLogix
	}
	return p.Family
}

// GetProtocol returns the protocol for Omron PLCs ("fins" or "eip").
func (p *PLCConfig) GetProtocol() string {
	if p.GetFamily() != FamilyOmron {
		return ""
	}
	if p.Protocol == "" || p.Protocol == "fins" {
		return "fins"
	}
	return p.Protocol
}

// IsOmronEIP returns true if this is an Omron PLC using EtherNet/IP protocol.
func (p *PLCConfig) IsOmronEIP() bool {
	return p.GetFamily() == FamilyOmron && p.GetProtocol() == "eip"
}

// IsOmronFINS returns true if this is an Omron PLC using FINS protocol.
func (p *PLCConfig) IsOmronFINS() bool {
	return p.GetFamily() == FamilyOmron && p.GetProtocol() == "fins"
}

// SupportsDiscovery returns true if this PLC configuration supports tag discovery.
func (p *PLCConfig) SupportsDiscovery() bool {
	if p.DiscoverTags != nil {
		return *p.DiscoverTags
	}
	family := p.GetFamily()
	if family == FamilyOmron {
		return p.IsOmronEIP()
	}
	return family.SupportsDiscovery()
}

// IsAddressBased returns true if this PLC family uses address-based tag names.
func (p *PLCConfig) IsAddressBased() bool {
	family := p.GetFamily()
	switch family {
	case FamilyS7, FamilySLC500, FamilyPLC5, FamilyMicroLogix:
		return true
	}
	if family == FamilyOmron && p.IsOmronFINS() {
		return true
	}
	return false
}

// IsDiscoverTagsExplicit returns whether DiscoverTags was explicitly set in config.
func (p *PLCConfig) IsDiscoverTagsExplicit() bool {
	return p.DiscoverTags != nil
}

// IsHealthCheckEnabled returns whether health check publishing is enabled (defaults to true).
func (p *PLCConfig) IsHealthCheckEnabled() bool {
	if p.HealthCheckEnabled == nil {
		return true
	}
	return *p.HealthCheckEnabled
}

// TagSelection represents a tag selected for monitoring/republishing.
type TagSelection struct {
	Name          string   `yaml:"name"`
	Alias         string   `yaml:"alias,omitempty"`
	DataType      string   `yaml:"data_type,omitempty"`
	Enabled       bool     `yaml:"enabled"`
	Writable      bool     `yaml:"writable,omitempty"`
	IgnoreChanges []string `yaml:"ignore_changes,omitempty"`
	NoREST        bool     `yaml:"no_rest,omitempty"`
	NoMQTT        bool     `yaml:"no_mqtt,omitempty"`
	NoKafka       bool     `yaml:"no_kafka,omitempty"`
	NoValkey      bool     `yaml:"no_valkey,omitempty"`
}

// PublishesToAny returns true if the tag publishes to at least one service.
func (t *TagSelection) PublishesToAny() bool {
	return !t.NoREST || !t.NoMQTT || !t.NoKafka || !t.NoValkey
}

// GetEnabledServices returns a list of service names this tag publishes to.
func (t *TagSelection) GetEnabledServices() []string {
	var services []string
	if !t.NoREST {
		services = append(services, "REST")
	}
	if !t.NoMQTT {
		services = append(services, "MQTT")
	}
	if !t.NoKafka {
		services = append(services, "Kafka")
	}
	if !t.NoValkey {
		services = append(services, "Valkey")
	}
	return services
}

// ShouldIgnoreMember returns true if the given member name is in the ignore list.
func (t *TagSelection) ShouldIgnoreMember(memberName string) bool {
	for _, ignored := range t.IgnoreChanges {
		if ignored == memberName {
			return true
		}
	}
	return false
}

// AddIgnoreMember adds a member name to the ignore list if not already present.
func (t *TagSelection) AddIgnoreMember(memberName string) {
	if !t.ShouldIgnoreMember(memberName) {
		t.IgnoreChanges = append(t.IgnoreChanges, memberName)
	}
}

// RemoveIgnoreMember removes a member name from the ignore list.
func (t *TagSelection) RemoveIgnoreMember(memberName string) {
	for i, ignored := range t.IgnoreChanges {
		if ignored == memberName {
			t.IgnoreChanges = append(t.IgnoreChanges[:i], t.IgnoreChanges[i+1:]...)
			return
		}
	}
}
