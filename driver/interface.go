package driver


// Driver is the unified interface for all PLC communications.
// Each PLC family has an adapter that implements this interface.
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
