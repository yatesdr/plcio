package driver

import (
	"fmt"
	"sort"
	"sync"

	"github.com/yatesdr/plcio/cip"
	"github.com/yatesdr/plcio/pccc"
)

// PCCCAdapter wraps pccc.Client to implement the Driver interface.
// Supports SLC500, PLC-5, and MicroLogix processors.
type PCCCAdapter struct {
	client *pccc.Client
	config *PLCConfig
	mu     sync.RWMutex
	epoch  uint64
}

// NewPCCCAdapter creates a new PCCCAdapter from configuration.
// The connection is not established until Connect() is called.
func NewPCCCAdapter(cfg *PLCConfig) (*PCCCAdapter, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}
	return &PCCCAdapter{config: cfg}, nil
}

// currentClient returns the client in use. Operations capture it once, so a
// concurrent Close or Connect cannot swap it out mid-operation (a closed
// client fails its I/O with an error instead of a nil dereference).
func (a *PCCCAdapter) currentClient() *pccc.Client {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.client
}

// Connect establishes connection to the SLC500/PLC-5/MicroLogix PLC.
// Reconnecting closes the previous session: SLC 5/05 and MicroLogix
// processors allow very few concurrent EtherNet/IP sessions.
func (a *PCCCAdapter) Connect() error {
	a.mu.RLock()
	epoch := a.epoch
	a.mu.RUnlock()
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

	a.mu.Lock()
	if a.epoch != epoch {
		a.mu.Unlock()
		client.Close()
		return fmt.Errorf("PCCC connect superseded by Close")
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
func (a *PCCCAdapter) Close() error {
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
func (a *PCCCAdapter) IsConnected() bool {
	client := a.currentClient()
	return client != nil && client.IsConnected()
}

// Family returns the PLC family.
func (a *PCCCAdapter) Family() PLCFamily {
	return a.config.GetFamily()
}

// ConnectionMode returns a description of the connection mode.
func (a *PCCCAdapter) ConnectionMode() string {
	client := a.currentClient()
	if client == nil {
		return "Not connected"
	}
	return client.ConnectionMode()
}

// GetDeviceInfo returns information about the connected PLC.
func (a *PCCCAdapter) GetDeviceInfo() (*DeviceInfo, error) {
	client := a.currentClient()
	if client == nil {
		return nil, fmt.Errorf("not connected")
	}

	identity, err := client.GetIdentity()
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

// SupportsDiscovery returns true for SLC500 and MicroLogix (file directory discovery).
// PLC-5 does not support file directory reads.
func (a *PCCCAdapter) SupportsDiscovery() bool {
	return a.config.GetFamily() != FamilyPLC5
}

// AllTags discovers data files from the file directory and returns them as TagInfo entries.
// Supported for SLC500 and MicroLogix only.
func (a *PCCCAdapter) AllTags() ([]TagInfo, error) {
	client := a.currentClient()
	if client == nil {
		return nil, fmt.Errorf("not connected")
	}
	if a.config.GetFamily() == FamilyPLC5 {
		return nil, fmt.Errorf("tag discovery not supported for PLC-5")
	}

	entries, err := client.DiscoverDataFiles()
	if err != nil {
		return nil, err
	}

	tags := make([]TagInfo, 0, len(entries))
	for _, e := range entries {
		var name string
		if e.TypePrefix != "" {
			name = fmt.Sprintf("%s%d", e.TypePrefix, e.FileNumber)
		} else {
			name = fmt.Sprintf("FILE%d", e.FileNumber)
		}

		tags = append(tags, TagInfo{
			Name:       name,
			TypeCode:   uint16(e.FileType),
			Dimensions: []uint32{uint32(e.ElementCount)},
			TypeName:   pccc.TypeName(uint16(e.FileType)),
			Writable:   true,
		})
	}

	return tags, nil
}

// Programs is not supported for PCCC-based PLCs.
func (a *PCCCAdapter) Programs() ([]string, error) {
	return nil, fmt.Errorf("program listing not supported for %s", a.config.GetFamily())
}

// maxPCCCReadBytes is the data cap for one SLC/MicroLogix batch read
// (Protected Typed Logical Read, FNC 0xA2). The DF1 manual (1770-6.5.16
// p. 7-17) gives 236 data bytes for SLC 5/03 and 5/04 ("225 bytes with IP",
// which does not apply to DF1 drivers). No reference gives a smaller SLC 5/03
// limit, so the cap is unchanged; a batch the processor rejects falls back to
// per-element reads.
const maxPCCCReadBytes = 236

// maxPLC5ReadBytes is the data cap for one PLC-5 Typed Read (FNC 0x68):
// "up to 240 bytes minus the number of bytes used in the type/data
// parameter" (1770-6.5.16 p. 7-28). An array parameter is at most 6 bytes
// here (flag, type ID, 1-byte size, element descriptor of up to 3 bytes).
const maxPLC5ReadBytes = 234

// pcccPLCType maps the configured family to the pccc command set and address
// syntax (PLC-5 I/O addresses are octal).
func (a *PCCCAdapter) pcccPLCType() pccc.PLCType {
	switch a.config.GetFamily() {
	case FamilyPLC5:
		return pccc.TypePLC5
	case FamilyMicroLogix:
		return pccc.TypeMicroLogix
	default:
		return pccc.TypeSLC500
	}
}

// Read reads data table addresses from the PLC, batching contiguous full-element
// reads in the same data file into single PCCC round-trips for efficiency.
//
// Addresses that use sub-element or bit access (e.g., T4:0.ACC, B3:0/5) are
// always read individually. If a batch read fails, the affected elements fall
// back to individual reads automatically.
func (a *PCCCAdapter) Read(requests []TagRequest) ([]*TagValue, error) {
	client := a.currentClient()
	if client == nil {
		return nil, fmt.Errorf("not connected")
	}
	if len(requests) == 0 {
		return nil, nil
	}

	results := make([]*TagValue, len(requests))
	family := string(a.config.GetFamily())

	// Parse all addresses and classify as bulkable or not.
	type parsedReq struct {
		addr     *pccc.FileAddress
		err      error
		bulkable bool
	}
	parsed := make([]parsedReq, len(requests))
	plcType := a.pcccPLCType()
	for i, req := range requests {
		addr, err := pccc.ParseAddressFor(req.Name, plcType)
		parsed[i] = parsedReq{addr: addr, err: err}
		if err == nil && addr.SubElement == 0 && addr.BitNumber < 0 && !addr.HasSubElement {
			parsed[i].bulkable = true
		}
	}
	maxBytes := maxPCCCReadBytes
	if plcType == pccc.TypePLC5 {
		maxBytes = maxPLC5ReadBytes
	}

	// Fill in parse errors immediately.
	for i, p := range parsed {
		if p.err != nil {
			results[i] = &TagValue{
				Name:   requests[i].Name,
				Family: family,
				Error:  fmt.Errorf("invalid address: %w", p.err),
			}
		}
	}

	// Group bulkable addresses by (FileNumber, FileType).
	type groupKey struct {
		fileNumber uint16
		fileType   byte
	}
	groups := make(map[groupKey][]int)
	for i, p := range parsed {
		if p.bulkable {
			key := groupKey{p.addr.FileNumber, p.addr.FileType}
			groups[key] = append(groups[key], i)
		}
	}

	// Track which indices were handled by bulk reads.
	handled := make([]bool, len(requests))
	// Mark parse errors as handled.
	for i, p := range parsed {
		if p.err != nil {
			handled[i] = true
		}
	}

	// For each group, find contiguous runs and issue bulk reads.
	for _, indices := range groups {
		if len(indices) < 2 {
			continue
		}

		// Sort by element number.
		sort.Slice(indices, func(a, b int) bool {
			return parsed[indices[a]].addr.Element < parsed[indices[b]].addr.Element
		})

		// Detect contiguous runs.
		runs := pcccContiguousRuns(indices, func(i int) uint16 {
			return parsed[i].addr.Element
		})

		for _, run := range runs {
			if len(run) < 2 {
				continue
			}

			startAddr := parsed[run[0]].addr
			elemSize := pccc.ElementSize(startAddr.FileType)
			maxCount := maxBytes / elemSize

			// Process run in chunks that fit within the PCCC payload limit.
			for chunkStart := 0; chunkStart < len(run); chunkStart += maxCount {
				chunkEnd := chunkStart + maxCount
				if chunkEnd > len(run) {
					chunkEnd = len(run)
				}
				chunk := run[chunkStart:chunkEnd]
				if len(chunk) < 2 {
					continue
				}

				chunkAddr := parsed[chunk[0]].addr
				count := len(chunk)

				tag, err := client.PLC().ReadAddressN(chunkAddr, count)
				if err != nil {
					// Fall back to individual reads for this chunk.
					continue
				}

				for j, origIdx := range chunk {
					offset := j * elemSize
					if offset+elemSize > len(tag.Bytes) {
						break // truncated response
					}

					elemBytes := make([]byte, elemSize)
					copy(elemBytes, tag.Bytes[offset:offset+elemSize])

					value := normalizeDecoded(pccc.DecodeValue(parsed[origIdx].addr, elemBytes))

					results[origIdx] = &TagValue{
						Name:        requests[origIdx].Name,
						DataType:    uint16(chunkAddr.FileType),
						Family:      family,
						Value:       value,
						StableValue: value,
						Bytes:       elemBytes,
						Count:       1,
					}
					handled[origIdx] = true
				}
			}
		}
	}

	// Handle remaining reads individually (non-bulkable, single elements, fallback).
	var remaining []string
	var remainingIdx []int
	for i, h := range handled {
		if !h {
			remaining = append(remaining, requests[i].Name)
			remainingIdx = append(remainingIdx, i)
		}
	}

	var readErr error
	if len(remaining) > 0 {
		values, err := client.Read(remaining...)
		readErr = err
		for j, v := range values {
			if j >= len(remainingIdx) {
				break
			}
			origIdx := remainingIdx[j]
			if v == nil {
				results[origIdx] = &TagValue{
					Name:   requests[origIdx].Name,
					Family: family,
					Error:  fmt.Errorf("nil response"),
				}
				continue
			}
			goValue := normalizeDecoded(v.Value)
			results[origIdx] = &TagValue{
				Name:        v.Name,
				DataType:    uint16(v.FileType),
				Family:      family,
				Value:       goValue,
				StableValue: goValue,
				Bytes:       v.Bytes,
				Count:       1,
				Error:       v.Error,
			}
		}
	}

	for i, value := range results {
		if value == nil {
			slotErr := readErr
			if slotErr == nil {
				slotErr = fmt.Errorf("missing response")
			}
			results[i] = &TagValue{Name: requests[i].Name, Family: family, Error: slotErr}
		}
	}
	return results, readErr
}

// pcccContiguousRuns detects runs of consecutive elements within a sorted slice
// of request indices. elemOf returns the element number for a given index.
func pcccContiguousRuns(sortedIndices []int, elemOf func(int) uint16) [][]int {
	if len(sortedIndices) == 0 {
		return nil
	}

	var runs [][]int
	current := []int{sortedIndices[0]}

	for i := 1; i < len(sortedIndices); i++ {
		prev := elemOf(sortedIndices[i-1])
		curr := elemOf(sortedIndices[i])
		if curr == prev+1 {
			current = append(current, sortedIndices[i])
		} else {
			runs = append(runs, current)
			current = []int{sortedIndices[i]}
		}
	}
	runs = append(runs, current)
	return runs
}

// Write writes a value to a data table address.
func (a *PCCCAdapter) Write(tag string, value interface{}) error {
	client := a.currentClient()
	if client == nil {
		return fmt.Errorf("not connected")
	}
	addr, err := pccc.ParseAddressFor(tag, a.pcccPLCType())
	if err != nil {
		return err
	}
	value, err = pcccCanonical(addr, value)
	if err != nil {
		return err
	}
	return client.Write(tag, value)
}

// Keepalive sends a NOP to maintain the connection.
func (a *PCCCAdapter) Keepalive() error {
	client := a.currentClient()
	if client == nil {
		return nil
	}
	return client.Keepalive()
}

// IsConnectionError returns true if the error indicates a connection problem.
func (a *PCCCAdapter) IsConnectionError(err error) bool {
	return IsLikelyConnectionError(err)
}

// Client returns the underlying pccc.Client for advanced operations.
func (a *PCCCAdapter) Client() *pccc.Client {
	return a.currentClient()
}
