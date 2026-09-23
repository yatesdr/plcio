package driver

import (
	"encoding/binary"
	"fmt"
	"strings"
	"sync"

	"github.com/yatesdr/plcio/cip"
	"github.com/yatesdr/plcio/logix"
)

// LogixAdapter wraps logix.Client to implement the Driver interface.
type LogixAdapter struct {
	client   *logix.Client
	config   *PLCConfig
	micro800 bool
	mu       sync.RWMutex // guards client and epoch
	epoch    uint64       // incremented by Close to supersede in-flight Connects
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

func (a *LogixAdapter) currentClient() *logix.Client {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.client
}

// Connect establishes connection to the Logix PLC.
// An existing connection is closed once the new one is established.
func (a *LogixAdapter) Connect() error {
	a.mu.RLock()
	epoch := a.epoch
	a.mu.RUnlock()
	opts := []logix.Option{}

	if a.config.Timeout > 0 {
		opts = append(opts, logix.WithTimeout(a.config.Timeout))
	}

	// Micro800 has no backplane; an explicit connection path still wins
	// over its empty default route.
	if a.micro800 {
		opts = append(opts, logix.WithMicro800())
	}
	if a.config.ConnectionPath != "" {
		routePath, err := cip.ParseConnectionPath(a.config.ConnectionPath)
		if err != nil {
			return fmt.Errorf("invalid connection path %q: %w", a.config.ConnectionPath, err)
		}
		opts = append(opts, logix.WithRoutePath(routePath))
	} else if !a.micro800 && a.config.Slot > 0 {
		opts = append(opts, logix.WithSlot(a.config.Slot))
	}

	client, err := logix.Connect(a.config.Address, opts...)
	if err != nil {
		return fmt.Errorf("logix connect: %w", err)
	}

	a.mu.Lock()
	if a.epoch != epoch {
		a.mu.Unlock()
		client.Close()
		return fmt.Errorf("logix connect superseded by Close")
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
func (a *LogixAdapter) Close() error {
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
func (a *LogixAdapter) IsConnected() bool {
	client := a.currentClient()
	return client != nil && client.IsConnected()
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
	client := a.currentClient()
	if client == nil {
		return "Not connected"
	}
	return client.ConnectionMode()
}

// GetDeviceInfo returns information about the connected PLC.
func (a *LogixAdapter) GetDeviceInfo() (*DeviceInfo, error) {
	client := a.currentClient()
	if client == nil {
		return nil, fmt.Errorf("not connected")
	}

	identity, err := client.Identity()
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
	client := a.currentClient()
	if client == nil {
		return nil, fmt.Errorf("not connected")
	}

	tags, err := client.AllTags()
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
	client := a.currentClient()
	if client == nil {
		return nil, fmt.Errorf("not connected")
	}
	return client.Programs()
}

// Read reads tag values from the PLC.
// Results are returned in request order, one per request.
func (a *LogixAdapter) Read(requests []TagRequest) ([]*TagValue, error) {
	client := a.currentClient()
	if client == nil {
		return nil, fmt.Errorf("not connected")
	}

	names := make([]string, len(requests))
	for i, req := range requests {
		names[i] = req.Name
	}

	values, err := client.Read(names...)
	if err != nil && len(values) == 0 {
		return nil, err
	}

	// logix.Client.Read returns arrays/structures before batched scalars and
	// may expand an unreadable structure into its member values, so results
	// are matched to requests by name rather than by position.
	exact, members := alignLogixResults(names, values)

	result := make([]*TagValue, len(names))
	for i, name := range names {
		v := exact[i]
		if v == nil {
			if len(members[i]) > 0 {
				result[i] = foldLogixMembers(client, name, members[i])
				continue
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
		goValue = normalizeDecoded(v.GoValueDecoded(client))

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

	return result, err
}

// alignLogixResults pairs each requested name with its exact-name result, or,
// failing that, with the member results a structure read was expanded into.
// Duplicate request names each consume their own result.
func alignLogixResults(names []string, values []*logix.TagValue) ([]*logix.TagValue, [][]*logix.TagValue) {
	exact := make([]*logix.TagValue, len(names))
	members := make([][]*logix.TagValue, len(names))
	used := make([]bool, len(values))
	byName := make(map[string][]int, len(values))
	for j, v := range values {
		if v != nil {
			byName[v.Name] = append(byName[v.Name], j)
		}
	}
	for i, name := range names {
		if idx := byName[name]; len(idx) > 0 {
			exact[i] = values[idx[0]]
			used[idx[0]] = true
			byName[name] = idx[1:]
		}
	}
	for i, name := range names {
		if exact[i] != nil {
			continue
		}
		prefix := name + "."
		for j, v := range values {
			if !used[j] && v != nil && strings.HasPrefix(v.Name, prefix) {
				members[i] = append(members[i], v)
				used[j] = true
			}
		}
	}
	return exact, members
}

// foldLogixMembers combines the member values of a structure that was read
// member-by-member into one result, keyed by member path (nested members
// become nested maps). The first member error, if any, is reported.
func foldLogixMembers(client *logix.Client, name string, members []*logix.TagValue) *TagValue {
	value := make(map[string]any)
	var firstErr error
	for _, m := range members {
		if m.Error != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("member %s: %w", m.Name, m.Error)
			}
			continue
		}
		path := strings.Split(strings.TrimPrefix(m.Name, name+"."), ".")
		node := value
		for _, key := range path[:len(path)-1] {
			child, ok := node[key].(map[string]any)
			if !ok {
				child = make(map[string]any)
				node[key] = child
			}
			node = child
		}
		node[path[len(path)-1]] = normalizeDecoded(m.GoValueDecoded(client))
	}
	dataType, _ := client.ResolveTagType(name)
	return &TagValue{
		Name:        name,
		DataType:    dataType,
		Family:      "logix",
		Value:       value,
		StableValue: value,
		Count:       1,
		Error:       firstErr,
	}
}

// Write writes a value to a tag. The tag's type is resolved once per
// connection (the client caches symbol lookups), so repeated writes do not
// page through the controller's symbol table.
func (a *LogixAdapter) Write(tag string, value interface{}) error {
	client := a.currentClient()
	if client == nil {
		return fmt.Errorf("not connected")
	}
	if canonicalNumeric(value) {
		code, known := client.ResolveTagType(tag)
		if !known {
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
		}
		width, signed, floating := logixNumeric(code)
		data, count, handled, err := canonicalStorage(value, width, signed, floating, binary.LittleEndian)
		if err != nil {
			return err
		}
		if handled {
			return client.WriteTagCount(tag, code&0x0fff, data, uint16(count))
		}
	}
	return client.Write(tag, value)
}

// Keepalive sends a keepalive message to maintain the connection.
func (a *LogixAdapter) Keepalive() error {
	client := a.currentClient()
	if client == nil {
		return nil
	}
	return client.Keepalive()
}

// IsConnectionError returns true if the error indicates a connection problem.
func (a *LogixAdapter) IsConnectionError(err error) bool {
	return IsLikelyConnectionError(err)
}

// Client returns the underlying logix.Client for advanced operations.
func (a *LogixAdapter) Client() *logix.Client {
	return a.currentClient()
}

// ResolveTagType looks up a tag's symbol table TypeCode by name.
// Returns (typeCode, true) if found, (0, false) otherwise.
func (a *LogixAdapter) ResolveTagType(tagName string) (uint16, bool) {
	client := a.currentClient()
	if client == nil {
		return 0, false
	}
	return client.ResolveTagType(tagName)
}

// GetMemberTypes returns member name → type name for a structure type.
func (a *LogixAdapter) GetMemberTypes(typeCode uint16) map[string]string {
	client := a.currentClient()
	if client == nil {
		return nil
	}
	return client.GetMemberTypes(typeCode)
}

// SetTags stores discovered tag information for optimized reads.
func (a *LogixAdapter) SetTags(tags []TagInfo) []TagInfo {
	client := a.currentClient()
	if client == nil {
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
	updated := client.SetTags(logixTags)

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
