# API Reference

Complete reference for the plcio public API.

## Package `driver`

The `driver` package provides the unified interface and factory functions for all PLC communication.

```go
import "github.com/yatesdr/plcio/driver"
```

---

### Driver Interface

```go
type Driver interface {
    // Connection management
    Connect() error
    Close() error
    IsConnected() bool

    // Identification
    Family() PLCFamily
    ConnectionMode() string
    GetDeviceInfo() (*DeviceInfo, error)

    // Tag discovery (not all families support this)
    SupportsDiscovery() bool
    AllTags() ([]TagInfo, error)
    Programs() ([]string, error)

    // Read/Write operations
    Read(requests []TagRequest) ([]*TagValue, error)
    Write(tag string, value interface{}) error

    // Maintenance
    Keepalive() error
    IsConnectionError(err error) bool
}
```

All PLC adapters implement this interface. Use `driver.Create()` to get the appropriate implementation based on your `PLCConfig`.

---

### Create

```go
func Create(cfg *PLCConfig) (Driver, error)
```

Factory function that creates the appropriate `Driver` implementation based on the `PLCConfig.Family` field:

| Family | Adapter Created |
|---|---|
| `FamilyLogix` (default) | `LogixAdapter` |
| `FamilyMicro800` | `LogixAdapter` (with Micro800 mode) |
| `FamilySLC500` | `PCCCAdapter` |
| `FamilyPLC5` | `PCCCAdapter` |
| `FamilyMicroLogix` | `PCCCAdapter` |
| `FamilyS7` | `S7Adapter` |
| `FamilyBeckhoff` | `ADSAdapter` |
| `FamilyOmron` | `OmronAdapter` |

The connection is **not** established until `Connect()` is called on the returned driver.

---

### PLCFamily

```go
type PLCFamily string

const (
    FamilyLogix     PLCFamily = "logix"      // Allen-Bradley ControlLogix/CompactLogix
    FamilyMicro800  PLCFamily = "micro800"   // Allen-Bradley Micro800 series
    FamilySLC500    PLCFamily = "slc500"     // Allen-Bradley SLC 5/03, 5/04, 5/05
    FamilyPLC5      PLCFamily = "plc5"       // Allen-Bradley PLC-5 series
    FamilyMicroLogix PLCFamily = "micrologix" // Allen-Bradley MicroLogix 1000/1100/1200/1400/1500
    FamilyS7        PLCFamily = "s7"         // Siemens S7
    FamilyOmron     PLCFamily = "omron"      // Omron (FINS or EIP)
    FamilyBeckhoff  PLCFamily = "beckhoff"   // Beckhoff TwinCAT (ADS)
)
```

**Methods:**

| Method | Description |
|---|---|
| `String() string` | Returns the string representation (defaults to "logix" if empty) |
| `Driver() string` | Returns the protocol driver name ("logix", "pccc", "s7", "ads", "omron") |
| `SupportsDiscovery() bool` | Whether the family supports tag browsing |

---

### PLCConfig

```go
type PLCConfig struct {
    Name               string         // PLC display name
    Address            string         // IP address or hostname (with optional :port)
    Slot               byte           // CPU slot number (Logix, S7)
    Family             PLCFamily      // PLC family
    Enabled            bool           // Whether to connect
    DiscoverTags       *bool          // Enable tag discovery (nil = auto)
    HealthCheckEnabled *bool          // Enable health publishing (nil = true)
    PollRate           time.Duration  // Read cycle interval
    Timeout            time.Duration  // Per-operation timeout
    Tags               []TagSelection // Configured tags

    // Beckhoff-specific
    AmsNetId string // Target AMS Net ID (e.g., "192.168.1.40.1.1")
    AmsPort  uint16 // AMS port (851 for TC3, 801 for TC2)

    // Omron-specific
    Protocol    string // "fins" or "eip"
    FinsPort    int    // FINS port (default 9600)
    FinsNetwork byte   // FINS network number
    FinsNode    byte   // FINS destination node
    FinsUnit    byte   // FINS unit number
}
```

**Methods:**

| Method | Description |
|---|---|
| `GetFamily() PLCFamily` | Returns family (defaults to `FamilyLogix` if empty) |
| `GetProtocol() string` | Returns Omron protocol ("fins" or "eip") |
| `IsOmronEIP() bool` | True if Omron using EtherNet/IP |
| `IsOmronFINS() bool` | True if Omron using FINS |
| `SupportsDiscovery() bool` | Protocol-aware discovery check |
| `IsAddressBased() bool` | True for S7, SLC 500, PLC-5, MicroLogix, and Omron FINS (address-based tags) |
| `IsHealthCheckEnabled() bool` | Whether health check is enabled (defaults true) |

---

### TagSelection

```go
type TagSelection struct {
    Name          string   // Tag name or address
    Alias         string   // Display name (optional)
    DataType      string   // Type hint for address-based protocols
    Enabled       bool     // Include in reads
    Writable      bool     // Allow writes to this tag
    IgnoreChanges []string // Struct member names to ignore for change detection
    NoREST        bool     // Skip REST publishing
    NoMQTT        bool     // Skip MQTT publishing
    NoKafka       bool     // Skip Kafka publishing
    NoValkey      bool     // Skip Valkey publishing
}
```

**Methods:**

| Method | Description |
|---|---|
| `PublishesToAny() bool` | True if tag publishes to at least one service |
| `GetEnabledServices() []string` | List of enabled service names |
| `ShouldIgnoreMember(name string) bool` | Check if a member is in the ignore list |
| `AddIgnoreMember(name string)` | Add a member to the ignore list |
| `RemoveIgnoreMember(name string)` | Remove a member from the ignore list |

---

### TagRequest

```go
type TagRequest struct {
    Name     string // Tag name or address
    TypeHint string // Optional type hint (e.g., "INT", "REAL", "DINT")
}
```

Used as input to `Driver.Read()`. The `TypeHint` field is **required** for S7 and Omron FINS, and ignored for Logix, PCCC, and ADS (which carry type information in the protocol). PCCC addresses encode their type via the address prefix (e.g., `N` for integer, `F` for float).

---

### TagValue

```go
type TagValue struct {
    Name        string      // Tag name
    DataType    uint16      // Native type code (family-specific)
    Family      string      // PLC family ("logix", "s7", "ads", "omron")
    Value       interface{} // Decoded Go value
    StableValue interface{} // Value with ignored members filtered
    Bytes       []byte      // Raw bytes (native byte order)
    Count       int         // Element count (1 for scalar, >1 for array)
    Error       error       // Per-tag error (nil if successful)
}
```

**Methods:**

| Method | Description |
|---|---|
| `SetIgnoreList(ignoreList []string)` | Computes and sets `StableValue` by filtering out ignored members |

The `Value` field contains a decoded Go type:
- Numeric types → `int8`, `int16`, `int32`, `int64`, `uint8`, `uint16`, `uint32`, `uint64`, `float32`, `float64`
- Booleans → `bool`
- Strings → `string`
- Structures/UDTs → `map[string]interface{}`
- Arrays → `[]interface{}`

---

### TagInfo

```go
type TagInfo struct {
    Name       string   // Tag name
    TypeCode   uint16   // Native type code
    Instance   uint32   // CIP instance ID (Logix/Omron EIP)
    Dimensions []uint32 // Array dimensions (empty for scalars)
    TypeName   string   // Human-readable type name (e.g., "DINT", "REAL")
    Writable   bool     // Whether the tag can be written
}
```

Returned by `AllTags()` for PLCs that support tag discovery.

---

### DeviceInfo

```go
type DeviceInfo struct {
    Family       PLCFamily // PLC family
    Vendor       string    // Vendor name (e.g., "Rockwell Automation", "Siemens")
    Model        string    // Device model
    Version      string    // Firmware version
    SerialNumber string    // Serial number
    Description  string    // Additional description
}
```

Returned by `GetDeviceInfo()`.

---

### DiscoveredDevice

```go
type DiscoveredDevice struct {
    IP           net.IP
    Port         uint16
    Family       PLCFamily
    ProductName  string
    Protocol     string            // "EIP", "S7", "ADS", "FINS"
    Vendor       string
    Extra        map[string]string // Protocol-specific metadata
    DiscoveredAt time.Time
}
```

**Methods:**

| Method | Description |
|---|---|
| `Key() string` | Returns `"IP:Port:Protocol"` for deduplication |

---

### Discovery Functions

```go
// Discover PLCs using all protocols in parallel
func DiscoverAll(broadcastIP string, scanCIDR string, timeout time.Duration, concurrency int) []DiscoveredDevice

// Discover EIP devices only (Allen-Bradley, Omron NJ/NX)
func DiscoverEIPOnly(broadcastIP string, timeout time.Duration) []DiscoveredDevice
```

### Network Helpers

```go
// Get CIDR notations for all local network interfaces
func GetLocalSubnets() []string

// Get broadcast addresses for all local interfaces
func GetBroadcastAddresses() []string
```

---

### Error Detection

```go
// Check if an error indicates a connection problem
func IsLikelyConnectionError(err error) bool
```

Returns `true` for: EOF, network errors, connection reset/refused/aborted, broken pipe, timeouts, and other connection-related error patterns.

---

### Stable Value Computation

```go
// Filter out ignored members from a value (used for change detection)
func ComputeStableValue(value interface{}, ignoreList []string) interface{}
```

For `map[string]interface{}` values (decoded UDTs), returns a copy with specified keys removed. For other types, returns the value unchanged.

---

## Adapter-Specific Methods

Each adapter exposes a `Client()` method for direct access to the underlying protocol client:

```go
// Get underlying Logix client
adapter := drv.(*driver.LogixAdapter)
client := adapter.Client() // *logix.Client

// Get underlying S7 client
adapter := drv.(*driver.S7Adapter)
client := adapter.Client() // *s7.Client

// Get underlying ADS client
adapter := drv.(*driver.ADSAdapter)
client := adapter.Client() // *ads.Client

// Get underlying Omron client
adapter := drv.(*driver.OmronAdapter)
client := adapter.Client() // *omron.Client

// Get underlying PCCC client (SLC 500, PLC-5, MicroLogix)
adapter := drv.(*driver.PCCCAdapter)
client := adapter.Client() // *pccc.Client
```

### LogixAdapter Extra Methods

```go
// Look up a tag's type code from the symbol table
func (a *LogixAdapter) ResolveTagType(tagName string) (uint16, bool)

// Get member name → type name mapping for a structure type
func (a *LogixAdapter) GetMemberTypes(typeCode uint16) map[string]string

// Store discovered tags for optimized reads (element count hints)
func (a *LogixAdapter) SetTags(tags []TagInfo) []TagInfo
```
