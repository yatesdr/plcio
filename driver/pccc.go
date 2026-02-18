package driver

import (
	"fmt"
	"sort"

	"github.com/yatesdr/plcio/cip"
	"github.com/yatesdr/plcio/pccc"
)

// PCCCAdapter wraps pccc.Client to implement the Driver interface.
// Supports SLC500, PLC-5, and MicroLogix processors.
type PCCCAdapter struct {
	client *pccc.Client
	config *PLCConfig
}

// NewPCCCAdapter creates a new PCCCAdapter from configuration.
// The connection is not established until Connect() is called.
func NewPCCCAdapter(cfg *PLCConfig) (*PCCCAdapter, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}
	return &PCCCAdapter{config: cfg}, nil
}

// Connect establishes connection to the SLC500/PLC-5/MicroLogix PLC.
func (a *PCCCAdapter) Connect() error {
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

	a.client = client
	return nil
}

// Close releases the connection.
func (a *PCCCAdapter) Close() error {
	if a.client != nil {
		a.client.Close()
		a.client = nil
	}
	return nil
}

// IsConnected returns true if connected to the PLC.
func (a *PCCCAdapter) IsConnected() bool {
	return a.client != nil && a.client.IsConnected()
}

// Family returns the PLC family.
func (a *PCCCAdapter) Family() PLCFamily {
	return a.config.GetFamily()
}

// ConnectionMode returns a description of the connection mode.
func (a *PCCCAdapter) ConnectionMode() string {
	if a.client == nil {
		return "Not connected"
	}
	return a.client.ConnectionMode()
}

// GetDeviceInfo returns information about the connected PLC.
func (a *PCCCAdapter) GetDeviceInfo() (*DeviceInfo, error) {
	if a.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	identity, err := a.client.GetIdentity()
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
	if a.client == nil {
		return nil, fmt.Errorf("not connected")
	}
	if a.config.GetFamily() == FamilyPLC5 {
		return nil, fmt.Errorf("tag discovery not supported for PLC-5")
	}

	entries, err := a.client.DiscoverDataFiles()
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

// maxPCCCReadBytes is the maximum data payload for a single PCCC typed read.
// SLC 5/03 supports ~164 bytes; SLC 5/04 and 5/05 support ~236 bytes.
// We use 236 as the cap, which works on 5/04+ and MicroLogix.
const maxPCCCReadBytes = 236

// Read reads data table addresses from the PLC, batching contiguous full-element
// reads in the same data file into single PCCC round-trips for efficiency.
//
// Addresses that use sub-element or bit access (e.g., T4:0.ACC, B3:0/5) are
// always read individually. If a batch read fails, the affected elements fall
// back to individual reads automatically.
func (a *PCCCAdapter) Read(requests []TagRequest) ([]*TagValue, error) {
	if a.client == nil {
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
	for i, req := range requests {
		addr, err := pccc.ParseAddress(req.Name)
		parsed[i] = parsedReq{addr: addr, err: err}
		if err == nil && addr.SubElement == 0 && addr.BitNumber < 0 {
			parsed[i].bulkable = true
		}
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
			maxCount := maxPCCCReadBytes / elemSize

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

				tag, err := a.client.PLC().ReadAddressN(chunkAddr, count)
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

					value := pccc.DecodeValue(parsed[origIdx].addr, elemBytes)

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

	if len(remaining) > 0 {
		values, err := a.client.Read(remaining...)
		if err != nil {
			return nil, err
		}
		for j, v := range values {
			origIdx := remainingIdx[j]
			if v == nil {
				results[origIdx] = &TagValue{
					Name:   requests[origIdx].Name,
					Family: family,
					Error:  fmt.Errorf("nil response"),
				}
				continue
			}
			results[origIdx] = &TagValue{
				Name:        v.Name,
				DataType:    uint16(v.FileType),
				Family:      family,
				Value:       v.Value,
				StableValue: v.Value,
				Bytes:       v.Bytes,
				Count:       1,
				Error:       v.Error,
			}
		}
	}

	return results, nil
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
	if a.client == nil {
		return fmt.Errorf("not connected")
	}
	return a.client.Write(tag, value)
}

// Keepalive sends a NOP to maintain the connection.
func (a *PCCCAdapter) Keepalive() error {
	if a.client == nil {
		return nil
	}
	return a.client.Keepalive()
}

// IsConnectionError returns true if the error indicates a connection problem.
func (a *PCCCAdapter) IsConnectionError(err error) bool {
	return IsLikelyConnectionError(err)
}

// Client returns the underlying pccc.Client for advanced operations.
func (a *PCCCAdapter) Client() *pccc.Client {
	return a.client
}
