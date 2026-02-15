package logix

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"

	"plcio/logging"
)

// Client is a high-level wrapper that manages connection lifecycle
// and provides simplified methods for common PLC operations.
type Client struct {
	plc             *PLC               // Low-level access preserved
	micro800        bool               // True for Micro800 series (no batch reads)
	tagInfo         map[string]TagInfo // Discovered tags for element count lookup
	templateSizes   map[uint16]uint32  // Cache of template ID -> size in bytes
	templates       map[uint16]*Template // Cache of template ID -> full template definition
	failedTemplates map[uint16]bool    // Cache of template IDs that failed to fetch
}

// options holds configuration options for Connect.
type options struct {
	slot            byte
	routePath       []byte
	skipForwardOpen bool
	micro800        bool
	timeout         time.Duration
}

// Option is a functional option for Connect.
type Option func(*options)

// WithSlot configures the CPU slot for ControlLogix systems.
// This sets up backplane routing to the specified slot.
func WithSlot(slot byte) Option {
	return func(o *options) {
		o.slot = slot
		o.routePath = nil // Slot routing overrides custom route path
	}
}

// WithRoutePath configures explicit routing for the PLC.
// Use this when connecting through a gateway or communication module.
func WithRoutePath(path []byte) Option {
	return func(o *options) {
		o.routePath = path
	}
}

// WithoutConnection skips the Forward Open and uses unconnected messaging only.
// Useful when connected messaging is not supported or not desired.
func WithoutConnection() Option {
	return func(o *options) {
		o.skipForwardOpen = true
	}
}

// WithMicro800 configures options appropriate for Micro800 series PLCs.
// Micro800 PLCs don't use backplane routing (empty route path) and
// don't support Forward Open, so this uses unconnected messaging only.
func WithMicro800() Option {
	return func(o *options) {
		o.micro800 = true
		o.skipForwardOpen = true
		o.routePath = []byte{} // Empty route - no backplane routing for Micro800
	}
}

// WithTimeout sets the connection and operation timeout for the EIP client.
// If not set or zero, the EIP client's default timeout (5s) is used.
func WithTimeout(d time.Duration) Option {
	return func(o *options) {
		o.timeout = d
	}
}

// Connect establishes a connection to a Logix PLC at the given address.
// It attempts to establish a CIP connection (Forward Open) for efficient messaging.
// If Forward Open fails, it falls back to unconnected messaging with a warning.
func Connect(address string, opts ...Option) (*Client, error) {
	// Apply options
	cfg := &options{}
	for _, opt := range opts {
		opt(cfg)
	}

	debugLog("Connect %s: slot=%d, micro800=%v, skipForwardOpen=%v", address, cfg.slot, cfg.micro800, cfg.skipForwardOpen)

	// Create low-level PLC connection
	plc, err := NewPLC(address, cfg.timeout)
	if err != nil {
		return nil, fmt.Errorf("Connect: %w", err)
	}

	// Configure routing
	if cfg.routePath != nil {
		plc.SetRoutePath(cfg.routePath)
	} else if cfg.slot > 0 {
		plc.SetSlotRouting(cfg.slot)
	}

	// Attempt Forward Open for connected messaging
	if !cfg.skipForwardOpen {
		err = plc.OpenConnection()
		if err != nil {
			debugLog("Warning: Forward Open failed, using unconnected messaging: %v", err)
		}
	}

	return &Client{plc: &plc, micro800: cfg.micro800}, nil
}

// Close releases all resources associated with the client.
func (c *Client) Close() {
	if c == nil || c.plc == nil {
		return
	}
	c.plc.Close()
}

// PLC returns the underlying low-level PLC for advanced operations.
func (c *Client) PLC() *PLC {
	return c.plc
}

// IsConnected returns true if a CIP connection is established.
func (c *Client) IsConnected() bool {
	return c.plc != nil && c.plc.IsConnected()
}

// ConnectionInfo returns information about the current connection.
// Returns connected (CIP connection active), size (negotiated connection size in bytes).
// If not using connected messaging, size is 0.
func (c *Client) ConnectionInfo() (connected bool, size uint16) {
	if c == nil || c.plc == nil {
		return false, 0
	}
	return c.plc.IsConnected(), c.plc.connSize
}

// ConnectionMode returns a human-readable string describing the connection mode.
func (c *Client) ConnectionMode() string {
	if c == nil || c.plc == nil {
		return "Not connected"
	}
	if c.plc.IsConnected() {
		if c.plc.connSize == ConnectionSizeLarge {
			return "Connected (Large Forward Open, 4002 bytes)"
		}
		return "Connected (Standard Forward Open, 504 bytes)"
	}
	return "Unconnected messaging"
}

// Keepalive sends a NOP (No Operation) via connected messaging to keep
// the CIP ForwardOpen connection alive. Should be called periodically
// when no other operations are being performed to prevent connection timeout.
// Returns nil if not using connected messaging.
func (c *Client) Keepalive() error {
	if c == nil || c.plc == nil {
		return nil
	}
	return c.plc.Keepalive()
}

// SetTags stores discovered tag information for element count lookup during reads.
// For array tags without dimensions, queries attribute 8 (byte count) to calculate size.
// Returns the tags with updated dimensions (for display purposes).
func (c *Client) SetTags(tags []TagInfo) []TagInfo {
	if c == nil {
		return tags
	}
	c.tagInfo = make(map[string]TagInfo, len(tags))

	// Make a copy to avoid modifying the original slice
	result := make([]TagInfo, len(tags))
	copy(result, tags)

	for i := range result {
		// For array tags without dimensions, query to get the size
		if IsArrayType(result[i].TypeCode) && len(result[i].Dimensions) == 0 && result[i].Instance > 0 {
			dims, err := c.plc.GetArrayDimensions(result[i].Instance, result[i].TypeCode)
			if err == nil && len(dims) > 0 {
				result[i].Dimensions = dims
			}
		}
		c.tagInfo[result[i].Name] = result[i]
	}

	return result
}

// getElementCount returns the element count to request for a tag.
// Returns the product of dimensions for arrays, 1 for scalars or unknown tags.
func (c *Client) getElementCount(tagName string) uint16 {
	if c == nil || c.tagInfo == nil {
		return 1
	}
	if info, ok := c.tagInfo[tagName]; ok {
		count := info.ElementCount()
		if count > 65535 {
			return 65535 // Max uint16
		}
		if count > 1 {
			return uint16(count)
		}
	}
	return 1
}

// isArrayTag returns true if the tag is known to be an array with dimensions.
func (c *Client) isArrayTag(tagName string) bool {
	if c == nil || c.tagInfo == nil {
		return false
	}
	if info, ok := c.tagInfo[tagName]; ok {
		return len(info.Dimensions) > 0
	}
	return false
}

// isStructTag returns true if the tag is known to be a structure/UDT type.
func (c *Client) isStructTag(tagName string) bool {
	if c == nil || c.tagInfo == nil {
		return false
	}
	if info, ok := c.tagInfo[tagName]; ok {
		return IsStructure(info.TypeCode)
	}
	return false
}

// getInstanceID returns the symbol instance ID for a tag, or 0 if unknown.
// The instance ID enables Symbol Instance Addressing which is more reliable
// for reading structures on some PLC configurations.
func (c *Client) getInstanceID(tagName string) uint32 {
	if c == nil || c.tagInfo == nil {
		return 0
	}
	if info, ok := c.tagInfo[tagName]; ok {
		return info.Instance
	}
	return 0
}

// GetTagInfo returns the stored tag info for a tag name, if available.
// Returns (tagInfo, true) if found, (TagInfo{}, false) if not.
// Used for debugging to verify tag info is stored correctly.
func (c *Client) GetTagInfo(tagName string) (TagInfo, bool) {
	if c == nil || c.tagInfo == nil {
		return TagInfo{}, false
	}
	info, ok := c.tagInfo[tagName]
	return info, ok
}

// ResolveTagType searches the symbol table for a tag by name and returns its
// symbol table TypeCode (with structure flag + template ID for structures).
// This is useful in manual mode where the CIP Read Tag response DataType (0x02A0)
// doesn't contain the template ID needed for structure expansion.
// Returns (typeCode, true) if found, (0, false) if not found or on error.
func (c *Client) ResolveTagType(tagName string) (uint16, bool) {
	if c == nil || c.plc == nil {
		return 0, false
	}
	info, err := c.plc.FindSymbolByName(tagName)
	if err != nil || info == nil {
		return 0, false
	}
	return info.TypeCode, true
}

// GetElementCount returns the element count that would be used when reading a tag.
// Exported for debugging purposes.
func (c *Client) GetElementCount(tagName string) uint16 {
	return c.getElementCount(tagName)
}

// GetElementSize returns the size in bytes of a single element for the given type.
// For structures, queries the template to get the size (cached after first query).
// Returns 0 if size cannot be determined.
func (c *Client) GetElementSize(typeCode uint16) uint32 {
	// For atomic types, use TypeSize
	baseType := BaseType(typeCode)
	if size := TypeSize(baseType); size > 0 {
		return uint32(size)
	}

	// For structures, look up or query the template size
	if IsStructure(typeCode) {
		templateID := TemplateID(typeCode)
		if templateID == 0 {
			return 0
		}

		// Check cache first
		if c.templateSizes != nil {
			if size, ok := c.templateSizes[templateID]; ok {
				return size
			}
		}

		// Query from PLC
		if c.plc != nil {
			size, err := c.plc.GetTemplateSize(typeCode)
			if err == nil && size > 0 {
				// Cache the result
				if c.templateSizes == nil {
					c.templateSizes = make(map[uint16]uint32)
				}
				c.templateSizes[templateID] = size
				return size
			}
		}
	}

	return 0
}

// Programs returns the list of program names in the PLC.
// Returns names like "MainProgram", "SafetyProgram", etc. (without "Program:" prefix).
func (c *Client) Programs() ([]string, error) {
	if c == nil || c.plc == nil {
		return nil, fmt.Errorf("Programs: nil client")
	}

	fullNames, err := c.plc.ListPrograms()
	if err != nil {
		return nil, fmt.Errorf("Programs: %w", err)
	}

	// Strip "Program:" prefix for cleaner API
	programs := make([]string, len(fullNames))
	for i, name := range fullNames {
		if len(name) > 8 && name[:8] == "Program:" {
			programs[i] = name[8:]
		} else {
			programs[i] = name
		}
	}

	return programs, nil
}

// ControllerTags returns all controller-scope tags (excluding program entries and system tags).
func (c *Client) ControllerTags() ([]TagInfo, error) {
	if c == nil || c.plc == nil {
		return nil, fmt.Errorf("ControllerTags: nil client")
	}

	allTags, err := c.plc.ListTags()
	if err != nil {
		return nil, fmt.Errorf("ControllerTags: %w", err)
	}

	// Filter to only readable data tags at controller scope
	var dataTags []TagInfo
	for _, t := range allTags {
		if t.IsReadable() {
			dataTags = append(dataTags, t)
		}
	}

	return dataTags, nil
}

// ProgramTags returns all tags within a specific program.
// programName can be just the name (e.g., "MainProgram") or full form ("Program:MainProgram").
func (c *Client) ProgramTags(program string) ([]TagInfo, error) {
	if c == nil || c.plc == nil {
		return nil, fmt.Errorf("ProgramTags: nil client")
	}

	tags, err := c.plc.ListProgramTags(program)
	if err != nil {
		return nil, fmt.Errorf("ProgramTags: %w", err)
	}

	// Filter to only readable data tags
	var dataTags []TagInfo
	for _, t := range tags {
		if t.IsReadable() {
			dataTags = append(dataTags, t)
		}
	}

	return dataTags, nil
}

// AllTags returns all readable tags (controller-scope and program-scope).
// This excludes program entries, routines, and system tags.
func (c *Client) AllTags() ([]TagInfo, error) {
	if c == nil || c.plc == nil {
		return nil, fmt.Errorf("AllTags: nil client")
	}

	tags, err := c.plc.ListDataTags()
	if err != nil {
		return nil, fmt.Errorf("AllTags: %w", err)
	}

	return tags, nil
}


// Read reads one or more tags by name and returns their values.
// Each tag in the result includes its own error status (nil if successful).
// The method returns an error only for transport-level failures.
// Arrays and structures are read individually; atomic scalars are batched for efficiency.
// For UDT/structure types that don't allow direct reads, automatically expands to read members.
func (c *Client) Read(tagNames ...string) ([]*TagValue, error) {
	if c == nil || c.plc == nil {
		return nil, fmt.Errorf("Read: nil client")
	}
	if len(tagNames) == 0 {
		return nil, nil
	}

	// Micro800 doesn't support Multiple Service Packet - read tags individually
	if c.micro800 {
		return c.readIndividual(tagNames)
	}

	// Separate complex types from simple scalars for proper handling
	// Arrays and structures need individual reads (large data, potential fragmented reads)
	// Simple scalars can be batched efficiently
	var scalars []string
	var individual []string
	for _, name := range tagNames {
		if c.isArrayTag(name) || c.isStructTag(name) {
			individual = append(individual, name)
		} else {
			scalars = append(scalars, name)
		}
	}

	results := make([]*TagValue, 0, len(tagNames))

	// Read arrays and structures individually with proper element counts
	for _, name := range individual {
		count := c.getElementCount(name)
		instanceID := c.getInstanceID(name)

		// For structures, get expected size from template for complete reads
		var expectedSize uint32
		isStruct := c.isStructTag(name)
		if isStruct {
			if info, ok := c.tagInfo[name]; ok {
				expectedSize = c.GetElementSize(info.TypeCode)
				debugLogVerbose("Read struct %q: typeCode=0x%04X, expectedSize=%d, count=%d",
					name, info.TypeCode, expectedSize, count)
				// For arrays of structures, multiply by element count
				if count > 1 {
					expectedSize *= uint32(count)
				}
			} else {
				debugLogVerbose("Read struct %q: NOT FOUND in tagInfo", name)
			}
		}

		tag, err := c.plc.ReadTagCountWithInstance(name, count, instanceID)
		if err != nil {
			debugLogVerbose("Read individual tag %q (count=%d, instance=%d) failed: %v", name, count, instanceID, err)

			// If direct read failed and this is a structure, try fragmented read
			if isStruct && expectedSize > 0 {
				debugLogVerbose("Trying fragmented read for %q (expected size: %d)", name, expectedSize)
				tag, err = c.plc.ReadTagFragmented(name, expectedSize)
				if err == nil {
					debugLogVerbose("Fragmented read for %q succeeded: got %d bytes", name, len(tag.Bytes))
				}
			}

			if err != nil {
				// If fragmented read also failed, try reading members individually
				if isStruct {
					memberResults, memberErr := c.readStructMembers(name)
					if memberErr == nil && len(memberResults) > 0 {
						debugLogVerbose("Read UDT %q via %d members succeeded", name, len(memberResults))
						results = append(results, memberResults...)
						continue
					}
					debugLogVerbose("Read UDT %q members also failed: %v", name, memberErr)
				}

				results = append(results, &TagValue{
					Name:  name,
					Error: err,
				})
				continue
			}
		}

		// Check if we got incomplete data for structures
		debugLogVerbose("Read %q got %d bytes (isStruct=%v, expectedSize=%d)",
			name, len(tag.Bytes), isStruct, expectedSize)
		if isStruct && expectedSize > 0 && uint32(len(tag.Bytes)) < expectedSize {
			debugLogVerbose("Read %q got incomplete data (%d/%d bytes), trying fragmented read",
				name, len(tag.Bytes), expectedSize)
			fragTag, fragErr := c.plc.ReadTagFragmented(name, expectedSize)
			if fragErr == nil && len(fragTag.Bytes) > len(tag.Bytes) {
				debugLogVerbose("Fragmented read for %q got more data: %d bytes", name, len(fragTag.Bytes))
				tag = fragTag
			} else if fragErr != nil {
				debugLogVerbose("Fragmented read for %q failed: %v", name, fragErr)
			} else {
				debugLogVerbose("Fragmented read for %q got same or less data: %d bytes", name, len(fragTag.Bytes))
			}
		}

		// Prefer tag info type code (from discovery) over read response
		// The PLC read response sometimes returns a simplified type code
		// that lacks the structure flag for UDTs
		dataType := tag.DataType
		if info, ok := c.tagInfo[name]; ok && info.TypeCode != 0 {
			// Use discovered type code - it has correct structure/array flags
			dataType = info.TypeCode
		}
		results = append(results, &TagValue{
			Name:     tag.Name,
			DataType: dataType,
			Bytes:    tag.Bytes,
			Error:    nil,
		})
	}

	// Batch read scalars
	if len(scalars) > 0 {
		// Determine batch size based on connection mode
		batchSize := 5 // Conservative for unconnected messaging
		if c.plc.IsConnected() {
			batchSize = 50
		}

		for i := 0; i < len(scalars); i += batchSize {
			end := i + batchSize
			if end > len(scalars) {
				end = len(scalars)
			}
			batch := scalars[i:end]

			tags, err := c.plc.ReadMultiple(batch)
			if err != nil {
				// Transport-level failure - mark all tags in batch as failed
				for _, name := range batch {
					results = append(results, &TagValue{
						Name:  name,
						Error: err,
					})
				}
				continue
			}

			// Convert results
			for j, tag := range tags {
				if tag == nil {
					results = append(results, &TagValue{
						Name:  batch[j],
						Error: fmt.Errorf("tag read failed"),
					})
				} else {
					results = append(results, &TagValue{
						Name:     tag.Name,
						DataType: tag.DataType,
						Bytes:    tag.Bytes,
						Error:    nil,
					})
				}
			}
		}
	}

	return results, nil
}

// ReadWithCount reads a single tag with a specified element count.
// This is useful for reading arrays where you know the exact element count.
// For structures, the count typically represents the number of structure instances.
func (c *Client) ReadWithCount(tagName string, count uint16) (*TagValue, error) {
	if c == nil || c.plc == nil {
		return nil, fmt.Errorf("ReadWithCount: nil client")
	}
	if tagName == "" {
		return nil, fmt.Errorf("ReadWithCount: empty tag name")
	}
	if count == 0 {
		count = 1
	}

	tag, err := c.plc.ReadTagCount(tagName, count)
	if err != nil {
		return nil, err
	}

	// Prefer tag info type code (from discovery) over read response
	dataType := tag.DataType
	if info, ok := c.tagInfo[tagName]; ok && info.TypeCode != 0 {
		dataType = info.TypeCode
	}

	return &TagValue{
		Name:     tag.Name,
		DataType: dataType,
		Bytes:    tag.Bytes,
		Error:    nil,
	}, nil
}

// readIndividual reads tags one at a time (for Micro800 which doesn't support batch reads).
func (c *Client) readIndividual(tagNames []string) ([]*TagValue, error) {
	results := make([]*TagValue, 0, len(tagNames))

	for _, name := range tagNames {
		// Look up element count and instance ID for better reliability
		count := c.getElementCount(name)
		instanceID := c.getInstanceID(name)
		tag, err := c.plc.ReadTagCountWithInstance(name, count, instanceID)
		if err != nil {
			results = append(results, &TagValue{
				Name:  name,
				Error: err,
			})
		} else {
			// Prefer tag info type code (from discovery) over read response
			// The PLC read response sometimes returns a simplified type code
			// that lacks the structure flag for UDTs
			dataType := tag.DataType
			if info, ok := c.tagInfo[name]; ok && info.TypeCode != 0 {
				// Use discovered type code - it has correct structure/array flags
				dataType = info.TypeCode
			}
			results = append(results, &TagValue{
				Name:     tag.Name,
				DataType: dataType,
				Bytes:    tag.Bytes,
				Error:    nil,
			})
		}
	}

	return results, nil
}

// readStructMembers reads a UDT/structure by reading each member individually.
// This is used when direct structure reads fail (e.g., due to access restrictions).
// Returns TagValue entries for each member with paths like "StructName.MemberName".
// Nested UDTs are recursively expanded to their atomic members.
func (c *Client) readStructMembers(tagName string) ([]*TagValue, error) {
	// Get the type code for this tag
	info, ok := c.tagInfo[tagName]
	if !ok {
		return nil, fmt.Errorf("no tag info for %q", tagName)
	}

	if !IsStructure(info.TypeCode) {
		return nil, fmt.Errorf("tag %q is not a structure type", tagName)
	}

	// Get the template for this structure
	tmpl, err := c.GetTemplate(info.TypeCode)
	if err != nil {
		return nil, fmt.Errorf("failed to get template: %w", err)
	}

	if len(tmpl.Members) == 0 {
		return nil, fmt.Errorf("template %q has no members", tmpl.Name)
	}

	debugLogVerbose("readStructMembers: reading %d members of %q (template: %q)", len(tmpl.MemberMap), tagName, tmpl.Name)

	// Expand all member paths, recursively handling nested UDTs
	// maxDepth prevents infinite recursion
	memberPaths, memberTypes := c.expandMemberPaths(tagName, tmpl, 10)

	if len(memberPaths) == 0 {
		return nil, fmt.Errorf("no readable members in template %q", tmpl.Name)
	}

	debugLogVerbose("readStructMembers: expanded to %d atomic member paths", len(memberPaths))

	// Read all members - use batch read if possible
	results := make([]*TagValue, 0, len(memberPaths))

	// Determine batch size based on connection mode
	batchSize := 5 // Conservative for unconnected messaging
	if c.plc.IsConnected() {
		batchSize = 50
	}

	for i := 0; i < len(memberPaths); i += batchSize {
		end := i + batchSize
		if end > len(memberPaths) {
			end = len(memberPaths)
		}
		batch := memberPaths[i:end]
		batchTypes := memberTypes[i:end]

		// Try batch read first
		tags, err := c.plc.ReadMultiple(batch)
		if err != nil {
			// Batch failed - try reading individually
			debugLogVerbose("Batch read of UDT members failed: %v, trying individual reads", err)
			for j, path := range batch {
				tag, readErr := c.plc.ReadTagCount(path, 1)
				if readErr != nil {
					results = append(results, &TagValue{
						Name:  path,
						Error: readErr,
					})
				} else {
					dataType := tag.DataType
					if dataType == 0 {
						dataType = batchTypes[j]
					}
					results = append(results, &TagValue{
						Name:     path,
						DataType: dataType,
						Bytes:    tag.Bytes,
						Error:    nil,
					})
				}
			}
			continue
		}

		// Process batch results
		for j, tag := range tags {
			if tag == nil {
				results = append(results, &TagValue{
					Name:  batch[j],
					Error: fmt.Errorf("member read failed"),
				})
			} else {
				dataType := tag.DataType
				if dataType == 0 {
					dataType = batchTypes[j]
				}
				results = append(results, &TagValue{
					Name:     tag.Name,
					DataType: dataType,
					Bytes:    tag.Bytes,
					Error:    nil,
				})
			}
		}
	}

	return results, nil
}

// expandMemberPaths recursively expands UDT members to their atomic (non-struct) paths.
// For nested UDTs, it fetches their templates and expands their members.
// Returns parallel slices of paths and types.
func (c *Client) expandMemberPaths(basePath string, tmpl *Template, maxDepth int) ([]string, []uint16) {
	if maxDepth <= 0 {
		debugLogVerbose("expandMemberPaths: max depth reached for %q", basePath)
		return nil, nil
	}

	var paths []string
	var types []uint16

	for _, member := range tmpl.Members {
		if member.Hidden || member.Name == "" {
			continue
		}

		memberPath := basePath + "." + member.Name

		// Check if this member is a nested structure
		if IsStructure(member.Type) {
			// Get template for nested UDT
			nestedTmpl, err := c.GetTemplate(member.Type)
			if err != nil {
				debugLogVerbose("expandMemberPaths: failed to get template for nested UDT %q (type 0x%04X): %v",
					memberPath, member.Type, err)
				// Skip this nested UDT - can't expand it
				continue
			}

			debugLogVerbose("expandMemberPaths: expanding nested UDT %q (template: %q) with %d members",
				memberPath, nestedTmpl.Name, len(nestedTmpl.MemberMap))

			// Recursively expand nested UDT
			nestedPaths, nestedTypes := c.expandMemberPaths(memberPath, nestedTmpl, maxDepth-1)
			paths = append(paths, nestedPaths...)
			types = append(types, nestedTypes...)
		} else {
			// Atomic member - add directly
			paths = append(paths, memberPath)
			types = append(types, member.Type)
		}
	}

	return paths, types
}

// GetStructMembers returns the member paths for a UDT/structure tag.
// This is useful for expanding a UDT into its readable members for display.
// Returns paths like ["TagName.Member1", "TagName.Member2", ...].
func (c *Client) GetStructMembers(tagName string) ([]string, error) {
	info, ok := c.tagInfo[tagName]
	if !ok {
		return nil, fmt.Errorf("no tag info for %q", tagName)
	}

	if !IsStructure(info.TypeCode) {
		return nil, fmt.Errorf("tag %q is not a structure type", tagName)
	}

	tmpl, err := c.GetTemplate(info.TypeCode)
	if err != nil {
		return nil, fmt.Errorf("failed to get template: %w", err)
	}

	var memberPaths []string
	for _, member := range tmpl.Members {
		if member.Hidden || member.Name == "" {
			continue
		}
		memberPaths = append(memberPaths, tagName+"."+member.Name)
	}

	return memberPaths, nil
}

// GetAllStructMembers returns all member paths for a UDT, recursively expanding nested UDTs.
// This returns only atomic (non-structure) members that can be read individually.
// The basePath should be the tag name (or tagName[0] for arrays).
func (c *Client) GetAllStructMembers(typeCode uint16, basePath string) ([]string, error) {
	if !IsStructure(typeCode) {
		return nil, fmt.Errorf("type 0x%04X is not a structure", typeCode)
	}

	tmpl, err := c.GetTemplate(typeCode)
	if err != nil {
		return nil, fmt.Errorf("failed to get template: %w", err)
	}

	paths, _ := c.expandMemberPaths(basePath, tmpl, 10)
	return paths, nil
}

// ReadAll discovers and reads all readable tags from the PLC.
// This is a convenience method that combines AllTags() and Read().
func (c *Client) ReadAll() ([]*TagValue, error) {
	if c == nil || c.plc == nil {
		return nil, fmt.Errorf("ReadAll: nil client")
	}

	tags, err := c.AllTags()
	if err != nil {
		return nil, fmt.Errorf("ReadAll: %w", err)
	}

	tagNames := make([]string, len(tags))
	for i, t := range tags {
		tagNames[i] = t.Name
	}

	return c.Read(tagNames...)
}

// getMemberTypeFromTemplate looks up a UDT member's type from its template definition.
// For a path like "Program:MainProgram.MyUDT.Member1", it finds the MyUDT template
// and returns Member1's type code. Returns 0 if not a UDT member or not found.
func (c *Client) getMemberTypeFromTemplate(tagName string) uint16 {
	// Find the base tag (before any member access)
	// Handle both controller-scope and program-scope tags:
	// - "MyUDT.Member" -> base="MyUDT", member="Member"
	// - "Program:MainProgram.MyUDT.Member" -> base="Program:MainProgram.MyUDT", member="Member"

	// First, check if this path has any member access
	dotIdx := strings.LastIndex(tagName, ".")
	if dotIdx == -1 {
		return 0 // No dot, not a member access
	}

	// Try progressively shorter base paths to find a UDT
	path := tagName
	for {
		dotIdx = strings.LastIndex(path, ".")
		if dotIdx == -1 {
			break
		}

		basePath := path[:dotIdx]
		memberPath := tagName[dotIdx+1:] // Everything after this dot

		// Check if basePath is a known UDT
		baseInfo, ok := c.tagInfo[basePath]
		if ok && IsStructure(baseInfo.TypeCode) {
			// Found a UDT, get its template
			tmpl, err := c.GetTemplate(baseInfo.TypeCode)
			if err != nil {
				logging.DebugLog("logix", "getMemberTypeFromTemplate: failed to get template for %s: %v", basePath, err)
				path = basePath
				continue
			}

			// Find the member in the template
			memberType := c.findMemberType(tmpl, strings.TrimPrefix(tagName, basePath+"."))
			if memberType != 0 {
				logging.DebugLog("logix", "getMemberTypeFromTemplate: found %s member type 0x%04X (%s)",
					tagName, memberType, TypeName(memberType))
				return memberType
			}
		}

		// Try a shorter base path
		path = basePath
		_ = memberPath // Used in next iteration conceptually
	}

	return 0
}

// findMemberType recursively finds a member's type in a template.
// memberPath can be simple ("Member1") or nested ("NestedUDT.Member1").
func (c *Client) findMemberType(tmpl *Template, memberPath string) uint16 {
	if tmpl == nil {
		return 0
	}

	// Split into first component and rest
	dotIdx := strings.Index(memberPath, ".")
	var firstName, restPath string
	if dotIdx == -1 {
		firstName = memberPath
	} else {
		firstName = memberPath[:dotIdx]
		restPath = memberPath[dotIdx+1:]
	}

	// Handle array index in member name (e.g., "Member[0]")
	if bracketIdx := strings.Index(firstName, "["); bracketIdx != -1 {
		firstName = firstName[:bracketIdx]
	}

	// Look up the member index
	memberIdx, ok := tmpl.MemberMap[firstName]
	if !ok || memberIdx < 0 || memberIdx >= len(tmpl.Members) {
		return 0
	}
	member := &tmpl.Members[memberIdx]

	// If no more path components, return this member's type
	if restPath == "" {
		return member.Type
	}

	// Need to recurse into nested structure
	if !IsStructure(member.Type) {
		return 0 // Not a structure, can't recurse
	}

	nestedTmpl, err := c.GetTemplate(member.Type)
	if err != nil {
		return 0
	}

	return c.findMemberType(nestedTmpl, restPath)
}

// Write writes a value to a tag. If the tag's type is known from discovery,
// the value is converted to match. Otherwise, the type is inferred from the Go value type.
func (c *Client) Write(tagName string, value interface{}) error {
	if c == nil || c.plc == nil {
		return fmt.Errorf("Write: nil client")
	}

	// For UDT member access (path contains dot after base tag), look up type from template
	// This is more reliable than tagInfo which may have incorrect types for UDT members
	if memberType := c.getMemberTypeFromTemplate(tagName); memberType != 0 {
		logging.DebugLog("logix", "Write %s: found member type from template, TypeCode=0x%04X (%s), value=%v (%T)",
			tagName, memberType, TypeName(memberType), value, value)
		return c.writeTyped(tagName, value, memberType)
	}

	// Look up the tag's actual type from discovery
	if info, ok := c.tagInfo[tagName]; ok && info.TypeCode != 0 {
		logging.DebugLog("logix", "Write %s: found type info, TypeCode=0x%04X (%s), value=%v (%T)",
			tagName, info.TypeCode, TypeName(info.TypeCode), value, value)
		return c.writeTyped(tagName, value, info.TypeCode)
	}

	// Fall back to inferring type from value
	logging.DebugLog("logix", "Write %s: no type info, inferring from value=%v (%T)", tagName, value, value)
	return c.writeInferred(tagName, value)
}

// writeTyped writes a value using the specified CIP type code.
// The value is converted to match the target type.
func (c *Client) writeTyped(tagName string, value interface{}, targetType uint16) error {
	baseType := targetType & 0x0FFF
	isArray := (targetType & TypeArrayMask) != 0

	// Also check if value is a slice - for UDT members, the array flag may not be in the type code
	if !isArray {
		isArray = isSliceType(value)
	}

	logging.DebugLog("logix", "writeTyped %s: targetType=0x%04X, baseType=0x%04X (%s), isArray=%v",
		tagName, targetType, baseType, TypeName(baseType), isArray)

	// Handle arrays
	if isArray {
		return c.writeArrayTyped(tagName, value, baseType)
	}

	// Convert value to target type
	data, err := c.convertToType(value, baseType)
	if err != nil {
		logging.DebugLog("logix", "writeTyped %s: conversion failed: %v", tagName, err)
		return fmt.Errorf("Write %s: %w", tagName, err)
	}

	logging.DebugLog("logix", "writeTyped %s: sending %d bytes: %X", tagName, len(data), data)
	err = c.plc.WriteTag(tagName, baseType, data)
	if err != nil {
		logging.DebugLog("logix", "writeTyped %s: WriteTag failed: %v", tagName, err)
	}
	return err
}

// isSliceType returns true if the value is a slice type that should be written as an array.
func isSliceType(value interface{}) bool {
	switch value.(type) {
	case []int32, []int64, []float32, []float64, []bool, []string,
		[]int, []int8, []int16, []uint, []byte, []uint16, []uint32, []uint64:
		return true
	default:
		return false
	}
}

// convertToType converts a Go value to bytes for the specified CIP type.
func (c *Client) convertToType(value interface{}, targetType uint16) ([]byte, error) {
	// Extract numeric value from the input
	var intVal int64
	var floatVal float64
	var strVal string
	var boolVal bool
	var isInt, isFloat, isStr, isBool bool

	switch v := value.(type) {
	case bool:
		boolVal = v
		isBool = true
		if v {
			intVal = 1
		}
		isInt = true
	case int8:
		intVal = int64(v)
		isInt = true
	case int16:
		intVal = int64(v)
		isInt = true
	case int32:
		intVal = int64(v)
		isInt = true
	case int64:
		intVal = v
		isInt = true
	case int:
		intVal = int64(v)
		isInt = true
	case uint8:
		intVal = int64(v)
		isInt = true
	case uint16:
		intVal = int64(v)
		isInt = true
	case uint32:
		intVal = int64(v)
		isInt = true
	case uint64:
		intVal = int64(v)
		isInt = true
	case uint:
		intVal = int64(v)
		isInt = true
	case float32:
		floatVal = float64(v)
		isFloat = true
	case float64:
		floatVal = v
		isFloat = true
	case string:
		strVal = v
		isStr = true
	default:
		return nil, fmt.Errorf("unsupported value type %T", value)
	}

	// Convert to target type
	switch targetType {
	case TypeBOOL:
		if isBool {
			if boolVal {
				return []byte{1}, nil
			}
			return []byte{0}, nil
		}
		if isInt {
			if intVal != 0 {
				return []byte{1}, nil
			}
			return []byte{0}, nil
		}
		return nil, fmt.Errorf("cannot convert %T to BOOL", value)

	case TypeSINT:
		if isInt {
			return []byte{byte(int8(intVal))}, nil
		}
		if isFloat {
			return []byte{byte(int8(floatVal))}, nil
		}
		return nil, fmt.Errorf("cannot convert %T to SINT", value)

	case TypeINT:
		if isInt {
			return binary.LittleEndian.AppendUint16(nil, uint16(int16(intVal))), nil
		}
		if isFloat {
			return binary.LittleEndian.AppendUint16(nil, uint16(int16(floatVal))), nil
		}
		return nil, fmt.Errorf("cannot convert %T to INT", value)

	case TypeDINT:
		if isInt {
			return binary.LittleEndian.AppendUint32(nil, uint32(int32(intVal))), nil
		}
		if isFloat {
			return binary.LittleEndian.AppendUint32(nil, uint32(int32(floatVal))), nil
		}
		return nil, fmt.Errorf("cannot convert %T to DINT", value)

	case TypeLINT:
		if isInt {
			return binary.LittleEndian.AppendUint64(nil, uint64(intVal)), nil
		}
		if isFloat {
			return binary.LittleEndian.AppendUint64(nil, uint64(int64(floatVal))), nil
		}
		return nil, fmt.Errorf("cannot convert %T to LINT", value)

	case TypeUSINT:
		if isInt {
			return []byte{byte(uint8(intVal))}, nil
		}
		if isFloat {
			return []byte{byte(uint8(floatVal))}, nil
		}
		return nil, fmt.Errorf("cannot convert %T to USINT", value)

	case TypeUINT:
		if isInt {
			return binary.LittleEndian.AppendUint16(nil, uint16(intVal)), nil
		}
		if isFloat {
			return binary.LittleEndian.AppendUint16(nil, uint16(floatVal)), nil
		}
		return nil, fmt.Errorf("cannot convert %T to UINT", value)

	case TypeUDINT:
		if isInt {
			return binary.LittleEndian.AppendUint32(nil, uint32(intVal)), nil
		}
		if isFloat {
			return binary.LittleEndian.AppendUint32(nil, uint32(floatVal)), nil
		}
		return nil, fmt.Errorf("cannot convert %T to UDINT", value)

	case TypeULINT:
		if isInt {
			return binary.LittleEndian.AppendUint64(nil, uint64(intVal)), nil
		}
		if isFloat {
			return binary.LittleEndian.AppendUint64(nil, uint64(floatVal)), nil
		}
		return nil, fmt.Errorf("cannot convert %T to ULINT", value)

	case TypeREAL:
		if isFloat {
			return binary.LittleEndian.AppendUint32(nil, math.Float32bits(float32(floatVal))), nil
		}
		if isInt {
			return binary.LittleEndian.AppendUint32(nil, math.Float32bits(float32(intVal))), nil
		}
		return nil, fmt.Errorf("cannot convert %T to REAL", value)

	case TypeLREAL:
		if isFloat {
			return binary.LittleEndian.AppendUint64(nil, math.Float64bits(floatVal)), nil
		}
		if isInt {
			return binary.LittleEndian.AppendUint64(nil, math.Float64bits(float64(intVal))), nil
		}
		return nil, fmt.Errorf("cannot convert %T to LREAL", value)

	case TypeSTRING:
		if isStr {
			strBytes := []byte(strVal)
			data := binary.LittleEndian.AppendUint32(nil, uint32(len(strBytes)))
			data = append(data, strBytes...)
			return data, nil
		}
		// Convert numbers to string
		if isInt {
			strBytes := []byte(fmt.Sprintf("%d", intVal))
			data := binary.LittleEndian.AppendUint32(nil, uint32(len(strBytes)))
			data = append(data, strBytes...)
			return data, nil
		}
		return nil, fmt.Errorf("cannot convert %T to STRING", value)

	case TypeShortSTRING:
		if isStr {
			strBytes := []byte(strVal)
			if len(strBytes) > 255 {
				strBytes = strBytes[:255]
			}
			data := []byte{byte(len(strBytes))}
			data = append(data, strBytes...)
			return data, nil
		}
		return nil, fmt.Errorf("cannot convert %T to SHORT_STRING", value)

	case TypeBYTE:
		if isInt {
			return []byte{byte(uint8(intVal))}, nil
		}
		if isFloat {
			return []byte{byte(uint8(floatVal))}, nil
		}
		return nil, fmt.Errorf("cannot convert %T to BYTE", value)

	case TypeWORD:
		if isInt {
			return binary.LittleEndian.AppendUint16(nil, uint16(intVal)), nil
		}
		if isFloat {
			return binary.LittleEndian.AppendUint16(nil, uint16(floatVal)), nil
		}
		return nil, fmt.Errorf("cannot convert %T to WORD", value)

	case TypeDWORD:
		if isInt {
			return binary.LittleEndian.AppendUint32(nil, uint32(intVal)), nil
		}
		if isFloat {
			return binary.LittleEndian.AppendUint32(nil, uint32(floatVal)), nil
		}
		return nil, fmt.Errorf("cannot convert %T to DWORD", value)

	case TypeLWORD:
		if isInt {
			return binary.LittleEndian.AppendUint64(nil, uint64(intVal)), nil
		}
		if isFloat {
			return binary.LittleEndian.AppendUint64(nil, uint64(floatVal)), nil
		}
		return nil, fmt.Errorf("cannot convert %T to LWORD", value)

	default:
		return nil, fmt.Errorf("unsupported target type 0x%04X", targetType)
	}
}

// writeArrayTyped writes an array value with the specified element type.
func (c *Client) writeArrayTyped(tagName string, value interface{}, elemType uint16) error {
	var data []byte
	var count int

	switch v := value.(type) {
	case []bool:
		count = len(v)
		for _, val := range v {
			elem, _ := c.convertToType(val, elemType)
			data = append(data, elem...)
		}
	case []int32:
		count = len(v)
		for _, val := range v {
			elem, err := c.convertToType(val, elemType)
			if err != nil {
				return err
			}
			data = append(data, elem...)
		}
	case []int64:
		count = len(v)
		for _, val := range v {
			elem, err := c.convertToType(val, elemType)
			if err != nil {
				return err
			}
			data = append(data, elem...)
		}
	case []float32:
		count = len(v)
		for _, val := range v {
			elem, err := c.convertToType(val, elemType)
			if err != nil {
				return err
			}
			data = append(data, elem...)
		}
	case []float64:
		count = len(v)
		for _, val := range v {
			elem, err := c.convertToType(val, elemType)
			if err != nil {
				return err
			}
			data = append(data, elem...)
		}
	case []string:
		count = len(v)
		for _, val := range v {
			elem, err := c.convertToType(val, elemType)
			if err != nil {
				return err
			}
			// For STRING arrays, pad each element to 88 bytes (4-byte len + 84 chars)
			if elemType == TypeSTRING {
				for len(elem) < 88 {
					elem = append(elem, 0)
				}
			}
			data = append(data, elem...)
		}
	default:
		return fmt.Errorf("unsupported array type %T", value)
	}

	if count == 0 {
		return fmt.Errorf("empty array")
	}

	return c.plc.WriteTagCount(tagName, elemType, data, uint16(count))
}

// writeInferred writes using type inferred from the Go value (fallback when tag type unknown).
func (c *Client) writeInferred(tagName string, value interface{}) error {
	var dataType uint16
	var data []byte

	switch v := value.(type) {
	case bool:
		dataType = TypeBOOL
		if v {
			data = []byte{1}
		} else {
			data = []byte{0}
		}

	case int8:
		dataType = TypeSINT
		data = []byte{byte(v)}

	case int16:
		dataType = TypeINT
		data = binary.LittleEndian.AppendUint16(nil, uint16(v))

	case int32:
		dataType = TypeDINT
		data = binary.LittleEndian.AppendUint32(nil, uint32(v))

	case int64:
		dataType = TypeLINT
		data = binary.LittleEndian.AppendUint64(nil, uint64(v))

	case int:
		dataType = TypeDINT
		data = binary.LittleEndian.AppendUint32(nil, uint32(v))

	case uint8:
		dataType = TypeUSINT
		data = []byte{v}

	case uint16:
		dataType = TypeUINT
		data = binary.LittleEndian.AppendUint16(nil, v)

	case uint32:
		dataType = TypeUDINT
		data = binary.LittleEndian.AppendUint32(nil, v)

	case uint64:
		dataType = TypeULINT
		data = binary.LittleEndian.AppendUint64(nil, v)

	case uint:
		dataType = TypeUDINT
		data = binary.LittleEndian.AppendUint32(nil, uint32(v))

	case float32:
		dataType = TypeREAL
		data = binary.LittleEndian.AppendUint32(nil, math.Float32bits(v))

	case float64:
		dataType = TypeLREAL
		data = binary.LittleEndian.AppendUint64(nil, math.Float64bits(v))

	case string:
		dataType = TypeSTRING
		strBytes := []byte(v)
		data = binary.LittleEndian.AppendUint32(nil, uint32(len(strBytes)))
		data = append(data, strBytes...)

	case []bool:
		if len(v) == 0 {
			return fmt.Errorf("Write: empty array")
		}
		dataType = TypeBOOL
		for _, val := range v {
			if val {
				data = append(data, 1)
			} else {
				data = append(data, 0)
			}
		}
		return c.plc.WriteTagCount(tagName, dataType, data, uint16(len(v)))

	case []int32:
		if len(v) == 0 {
			return fmt.Errorf("Write: empty array")
		}
		dataType = TypeDINT
		for _, val := range v {
			data = binary.LittleEndian.AppendUint32(data, uint32(val))
		}
		return c.plc.WriteTagCount(tagName, dataType, data, uint16(len(v)))

	case []int64:
		if len(v) == 0 {
			return fmt.Errorf("Write: empty array")
		}
		dataType = TypeDINT
		for _, val := range v {
			data = binary.LittleEndian.AppendUint32(data, uint32(val))
		}
		return c.plc.WriteTagCount(tagName, dataType, data, uint16(len(v)))

	case []float32:
		if len(v) == 0 {
			return fmt.Errorf("Write: empty array")
		}
		dataType = TypeREAL
		for _, val := range v {
			data = binary.LittleEndian.AppendUint32(data, math.Float32bits(val))
		}
		return c.plc.WriteTagCount(tagName, dataType, data, uint16(len(v)))

	case []float64:
		if len(v) == 0 {
			return fmt.Errorf("Write: empty array")
		}
		dataType = TypeREAL
		for _, val := range v {
			data = binary.LittleEndian.AppendUint32(data, math.Float32bits(float32(val)))
		}
		return c.plc.WriteTagCount(tagName, dataType, data, uint16(len(v)))

	case []string:
		if len(v) == 0 {
			return fmt.Errorf("Write: empty array")
		}
		dataType = TypeSTRING
		for _, s := range v {
			strBytes := []byte(s)
			if len(strBytes) > 82 {
				strBytes = strBytes[:82]
			}
			elem := binary.LittleEndian.AppendUint32(nil, uint32(len(strBytes)))
			elem = append(elem, strBytes...)
			for len(elem) < 88 {
				elem = append(elem, 0)
			}
			data = append(data, elem...)
		}
		return c.plc.WriteTagCount(tagName, dataType, data, uint16(len(v)))

	default:
		return fmt.Errorf("Write: unsupported value type %T", value)
	}

	return c.plc.WriteTag(tagName, dataType, data)
}

// WriteBool writes a boolean value to a tag.
func (c *Client) WriteBool(tagName string, val bool) error {
	if c == nil || c.plc == nil {
		return fmt.Errorf("WriteBool: nil client")
	}
	data := []byte{0}
	if val {
		data[0] = 1
	}
	return c.plc.WriteTag(tagName, TypeBOOL, data)
}

// WriteInt writes an integer value to a tag.
// Writes as DINT (32-bit signed integer).
func (c *Client) WriteInt(tagName string, val int64) error {
	if c == nil || c.plc == nil {
		return fmt.Errorf("WriteInt: nil client")
	}
	data := binary.LittleEndian.AppendUint32(nil, uint32(val))
	return c.plc.WriteTag(tagName, TypeDINT, data)
}

// WriteFloat writes a floating-point value to a tag.
// Writes as REAL (32-bit float).
func (c *Client) WriteFloat(tagName string, val float64) error {
	if c == nil || c.plc == nil {
		return fmt.Errorf("WriteFloat: nil client")
	}
	data := binary.LittleEndian.AppendUint32(nil, math.Float32bits(float32(val)))
	return c.plc.WriteTag(tagName, TypeREAL, data)
}

// WriteString writes a string value to a tag.
// Writes as Logix STRING (4-byte length prefix + character data).
func (c *Client) WriteString(tagName string, val string) error {
	if c == nil || c.plc == nil {
		return fmt.Errorf("WriteString: nil client")
	}
	strBytes := []byte(val)
	data := binary.LittleEndian.AppendUint32(nil, uint32(len(strBytes)))
	data = append(data, strBytes...)
	return c.plc.WriteTag(tagName, TypeSTRING, data)
}

// GetTemplate returns the template for a structure type, fetching from PLC if not cached.
func (c *Client) GetTemplate(typeCode uint16) (*Template, error) {
	if c == nil || c.plc == nil {
		return nil, fmt.Errorf("GetTemplate: nil client")
	}

	if !IsStructure(typeCode) {
		return nil, fmt.Errorf("type 0x%04X is not a structure", typeCode)
	}

	templateID := TemplateID(typeCode)
	if templateID == 0 {
		return nil, fmt.Errorf("invalid template ID")
	}

	// Check success cache first
	if c.templates != nil {
		if tmpl, ok := c.templates[templateID]; ok {
			return tmpl, nil
		}
	}

	// Check failure cache - don't retry templates that already failed
	if c.failedTemplates != nil {
		if c.failedTemplates[templateID] {
			return nil, fmt.Errorf("template %d previously failed to fetch", templateID)
		}
	}

	// Fetch from PLC
	tmpl, err := c.plc.GetTemplate(templateID)
	if err != nil {
		// Only cache permanent failures, not transient network errors
		// Timeouts and connection errors should be retried on next attempt
		errStr := err.Error()
		isTransient := strings.Contains(errStr, "timeout") ||
			strings.Contains(errStr, "connection reset") ||
			strings.Contains(errStr, "connection refused") ||
			strings.Contains(errStr, "i/o timeout") ||
			strings.Contains(errStr, "broken pipe")

		if !isTransient {
			// Cache only permanent failures (e.g., template doesn't exist)
			if c.failedTemplates == nil {
				c.failedTemplates = make(map[uint16]bool)
			}
			c.failedTemplates[templateID] = true
		}
		return nil, err
	}

	// Cache success
	if c.templates == nil {
		c.templates = make(map[uint16]*Template)
	}
	c.templates[templateID] = tmpl

	debugLogVerbose("Cached template %q (ID: %d) with %d total members (%d visible)",
		tmpl.Name, tmpl.ID, len(tmpl.Members), len(tmpl.MemberMap))

	// Dump full template structure for debugging
	for i, m := range tmpl.Members {
		hiddenStr := ""
		if m.Hidden {
			hiddenStr = " [HIDDEN]"
		}
		debugLogVerbose("  Template member %d: %q offset=%d type=0x%04X%s",
			i, m.Name, m.Offset, m.Type, hiddenStr)
	}

	return tmpl, nil
}

// GetTemplateByID returns the template by ID, fetching from PLC if not cached.
func (c *Client) GetTemplateByID(templateID uint16) (*Template, error) {
	// Construct a type code with structure flag
	typeCode := TypeStructureMask | templateID
	return c.GetTemplate(typeCode)
}

// GetMemberTypes returns a map of member name → type name for a structure type.
// Keys use dot-path notation for nested struct members (e.g., "First_Name.LEN").
// This allows the Web UI to display correct types at all nesting levels.
func (c *Client) GetMemberTypes(typeCode uint16) map[string]string {
	types := make(map[string]string)
	visited := make(map[uint16]bool)
	c.getMemberTypesRecursive(typeCode, "", types, visited)
	if len(types) == 0 {
		return nil
	}
	return types
}

// getMemberTypesRecursive populates types with dot-path keys for all members,
// recursing into nested structures. visited prevents infinite recursion on
// self-referencing UDTs.
func (c *Client) getMemberTypesRecursive(typeCode uint16, prefix string, types map[string]string, visited map[uint16]bool) {
	if visited[typeCode] {
		return
	}
	visited[typeCode] = true

	tmpl, err := c.GetTemplate(typeCode)
	if err != nil {
		return
	}

	for _, member := range tmpl.Members {
		if member.Hidden || member.Name == "" {
			continue
		}
		key := member.Name
		if prefix != "" {
			key = prefix + "." + member.Name
		}
		types[key] = TypeName(member.Type)

		// Recurse into non-array structure members
		if member.IsStructure() && !member.IsArray() {
			c.getMemberTypesRecursive(member.Type, key, types, visited)
		}
	}
}

// ClearTemplateCache clears all cached templates, forcing re-fetch on next access.
// Use this for debugging template parsing issues.
func (c *Client) ClearTemplateCache() {
	if c == nil {
		return
	}
	c.templates = nil
	c.templateSizes = nil
	c.failedTemplates = nil
	debugLogVerbose("Template cache cleared")
}

// FetchTemplatesForTags eagerly fetches and caches templates for all structure-type tags.
// This is OPTIONAL - templates are also fetched lazily on-demand when tags are read.
// Use this only if you want to pre-warm the cache (not recommended for slow connections).
// Note: Built-in AB types (modules, etc.) don't have fetchable templates - this is expected.
func (c *Client) FetchTemplatesForTags() {
	if c == nil || c.tagInfo == nil {
		return
	}

	var fetched, failed int
	for _, tag := range c.tagInfo {
		if IsStructure(tag.TypeCode) {
			tmpl, err := c.GetTemplate(tag.TypeCode)
			if err != nil {
				failed++
				// Only log first few failures to avoid spam
				if failed <= 3 {
					debugLogVerbose("Failed to fetch template for tag %q: %v", tag.Name, err)
				}
			} else {
				fetched++
				debugLogVerbose("Fetched template %q for tag %q", tmpl.Name, tag.Name)
			}
		}
	}

	if failed > 3 {
		debugLogVerbose("... and %d more template fetch failures (built-in types don't have fetchable templates)", failed-3)
	}
	if fetched > 0 || failed > 0 {
		debugLogVerbose("Template fetch summary: %d successful, %d failed", fetched, failed)
	}
}

// DecodeUDT decodes raw UDT bytes into a structured map using the template.
// Returns a map with member names as keys and decoded values.
// Nested UDTs are recursively decoded into nested maps.
func (c *Client) DecodeUDT(typeCode uint16, data []byte) (map[string]interface{}, error) {
	tmpl, err := c.GetTemplate(typeCode)
	if err != nil {
		return nil, fmt.Errorf("failed to get template: %w", err)
	}

	return c.decodeUDTWithTemplate(tmpl, data)
}

// decodeUDTWithTemplate decodes UDT data using a specific template.
// Set topLevel=true for data read directly from PLC (has structure handle prefix).
// Set topLevel=false for nested structures within parent UDT data (no handle prefix).
func (c *Client) decodeUDTWithTemplate(tmpl *Template, data []byte) (map[string]interface{}, error) {
	return c.decodeUDTWithTemplateInternal(tmpl, data, true)
}

// decodeUDTWithTemplateInternal decodes UDT data with control over handle stripping.
func (c *Client) decodeUDTWithTemplateInternal(tmpl *Template, data []byte, topLevel bool) (map[string]interface{}, error) {
	result := make(map[string]interface{})

	// Only top-level UDT responses from the PLC have a 2-byte structure handle prefix.
	// Nested structures within parent UDT data do NOT have this prefix.
	if topLevel && len(data) >= 2 {
		handle := binary.LittleEndian.Uint16(data[0:2])
		debugLogVerbose("decodeUDTWithTemplate: stripping 2-byte structure handle 0x%04X (template handle 0x%04X)",
			handle, tmpl.RawHandle)
		data = data[2:] // Skip the structure handle
	}

	dataLen := uint32(len(data))
	skipped := 0
	decoded := 0

	debugLogVerbose("decodeUDTWithTemplate: template %q (size=%d) with %d bytes of member data (topLevel=%v)",
		tmpl.Name, tmpl.Size, dataLen, topLevel)

	// Debug: show first 32 bytes of member data
	showLen := len(data)
	if showLen > 32 {
		showLen = 32
	}
	debugLogVerbose("decodeUDTWithTemplate: member data first %d bytes: % X", showLen, data[:showLen])

	for _, member := range tmpl.Members {
		if member.Hidden || member.Name == "" {
			continue
		}

		// Check if we have enough data
		if member.Offset >= dataLen {
			if skipped < 5 {
				debugLogVerbose("decodeUDTWithTemplate: SKIPPED member %q at offset %d (data len %d, type 0x%04X)",
					member.Name, member.Offset, dataLen, member.Type)
			}
			skipped++
			continue
		}

		memberData := data[member.Offset:]
		value, err := c.decodeMemberValue(&member, memberData)
		if err != nil {
			debugLogVerbose("Failed to decode member %q: %v", member.Name, err)
			continue
		}

		// Debug: show what we decoded for first 10 members
		if decoded < 10 {
			bytesAtOffset := memberData
			if len(bytesAtOffset) > 8 {
				bytesAtOffset = bytesAtOffset[:8]
			}
			debugLogVerbose("  Decoded %q: offset=%d type=0x%04X bytes=% X -> value=%v",
				member.Name, member.Offset, member.Type, bytesAtOffset, value)
		}
		decoded++

		result[member.Name] = value
	}

	if skipped > 0 {
		debugLogVerbose("decodeUDTWithTemplate: skipped %d members due to insufficient data", skipped)
	}

	return result, nil
}

// decodeMemberValue decodes a single member's value from raw bytes.
func (c *Client) decodeMemberValue(member *TemplateMember, data []byte) (interface{}, error) {
	if member.IsArray() {
		return c.decodeArrayMember(member, data)
	}

	return c.decodeScalarMember(member.Type, data)
}

// decodeScalarMember decodes a scalar value of the given type.
func (c *Client) decodeScalarMember(typeCode uint16, data []byte) (interface{}, error) {
	// Handle nested structures
	if IsStructure(typeCode) {
		tmpl, err := c.GetTemplate(typeCode)
		if err != nil {
			// Return raw bytes as fallback
			return data, nil
		}
		// Nested structures don't have a handle prefix - only top-level reads do
		return c.decodeUDTWithTemplateInternal(tmpl, data, false)
	}

	// Handle atomic types
	baseType := BaseType(typeCode)

	switch baseType {
	case TypeBOOL:
		if len(data) < 1 {
			return nil, fmt.Errorf("insufficient data for BOOL")
		}
		return data[0] != 0, nil

	case TypeSINT:
		if len(data) < 1 {
			return nil, fmt.Errorf("insufficient data for SINT")
		}
		return int64(int8(data[0])), nil

	case TypeINT:
		if len(data) < 2 {
			return nil, fmt.Errorf("insufficient data for INT")
		}
		return int64(int16(binary.LittleEndian.Uint16(data))), nil

	case TypeDINT:
		if len(data) < 4 {
			return nil, fmt.Errorf("insufficient data for DINT")
		}
		return int64(int32(binary.LittleEndian.Uint32(data))), nil

	case TypeLINT:
		if len(data) < 8 {
			return nil, fmt.Errorf("insufficient data for LINT")
		}
		return int64(binary.LittleEndian.Uint64(data)), nil

	case TypeUSINT:
		if len(data) < 1 {
			return nil, fmt.Errorf("insufficient data for USINT")
		}
		return uint64(data[0]), nil

	case TypeUINT:
		if len(data) < 2 {
			return nil, fmt.Errorf("insufficient data for UINT")
		}
		return uint64(binary.LittleEndian.Uint16(data)), nil

	case TypeUDINT:
		if len(data) < 4 {
			return nil, fmt.Errorf("insufficient data for UDINT")
		}
		return uint64(binary.LittleEndian.Uint32(data)), nil

	case TypeULINT:
		if len(data) < 8 {
			return nil, fmt.Errorf("insufficient data for ULINT")
		}
		return binary.LittleEndian.Uint64(data), nil

	case TypeREAL:
		if len(data) < 4 {
			return nil, fmt.Errorf("insufficient data for REAL")
		}
		bits := binary.LittleEndian.Uint32(data)
		return float64(math.Float32frombits(bits)), nil

	case TypeLREAL:
		if len(data) < 8 {
			return nil, fmt.Errorf("insufficient data for LREAL")
		}
		bits := binary.LittleEndian.Uint64(data)
		return math.Float64frombits(bits), nil

	case TypeSTRING:
		if len(data) < 4 {
			return nil, fmt.Errorf("insufficient data for STRING")
		}
		strLen := binary.LittleEndian.Uint32(data[:4])
		if int(strLen) > len(data)-4 {
			strLen = uint32(len(data) - 4)
		}
		return string(data[4 : 4+strLen]), nil

	default:
		// Unknown type - return raw bytes
		size := TypeSize(baseType)
		if size == 0 {
			size = 4 // Default
		}
		if size > len(data) {
			size = len(data)
		}
		return data[:size], nil
	}
}

// decodeArrayMember decodes an array member's values.
func (c *Client) decodeArrayMember(member *TemplateMember, data []byte) (interface{}, error) {
	elemCount := member.ElementCount()
	if elemCount <= 0 {
		return nil, nil
	}

	// Determine element size
	var elemSize int
	if IsStructure(member.Type) {
		tmpl, err := c.GetTemplate(member.Type)
		if err != nil {
			return nil, err
		}
		elemSize = int(tmpl.Size)
	} else {
		elemSize = TypeSize(BaseType(member.Type))
		if elemSize == 0 {
			elemSize = 4 // Default
		}
	}

	// Decode each element
	results := make([]interface{}, 0, elemCount)
	for i := 0; i < elemCount; i++ {
		offset := i * elemSize
		if offset >= len(data) {
			break
		}

		elemData := data[offset:]
		if len(elemData) > elemSize {
			elemData = elemData[:elemSize]
		}

		val, err := c.decodeScalarMember(member.Type, elemData)
		if err != nil {
			continue
		}
		results = append(results, val)
	}

	return results, nil
}

// GetCachedTemplates returns all cached templates.
func (c *Client) GetCachedTemplates() map[uint16]*Template {
	if c == nil {
		return nil
	}
	return c.templates
}
