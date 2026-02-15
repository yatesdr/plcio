package logix

import (
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/yatesdr/plcio/cip"
)

// TagInfo contains metadata about a tag from the PLC's symbol table.
type TagInfo struct {
	Name       string // Full tag name (e.g., "MyTag" or "Program:MainProgram.MyTag")
	TypeCode   uint16 // CIP data type code
	Instance   uint32 // Symbol instance ID (used for pagination)
	Dimensions []int  // Array dimensions (nil for scalar)
}

// IsProgram returns true if this tag represents a program entry (not a program-scoped tag).
// Program entries look like "Program:MainProgram" (no dot after program name).
// Program-scoped tags look like "Program:MainProgram.TagName" (have a dot).
func (t TagInfo) IsProgram() bool {
	if len(t.Name) < 8 || t.Name[:8] != "Program:" {
		return false
	}
	// Check if there's a dot after "Program:" - if so, it's a tag, not a program entry
	for i := 8; i < len(t.Name); i++ {
		if t.Name[i] == '.' {
			return false // This is a program-scoped tag, not a program entry
		}
	}
	return true // No dot found - this is a program entry like "Program:MainProgram"
}

// IsSystem returns true if this is a system/internal tag (Map:, Task:, Cxn:, etc.)
func (t TagInfo) IsSystem() bool {
	if len(t.Name) >= 4 {
		prefix := t.Name[:4]
		if prefix == "Map:" || prefix == "Cxn:" {
			return true
		}
	}
	if len(t.Name) >= 5 && t.Name[:5] == "Task:" {
		return true
	}
	return false
}

// IsRoutine returns true if this is a routine entry (not a readable tag).
func (t TagInfo) IsRoutine() bool {
	// Check for ".Routine:" pattern anywhere in the name
	for i := 0; i < len(t.Name)-8; i++ {
		if t.Name[i:i+8] == "Routine:" {
			return true
		}
	}
	return false
}

// IsReadable returns true if this tag can be read/written (not a program, routine, or system entry).
func (t TagInfo) IsReadable() bool {
	return !t.IsProgram() && !t.IsRoutine() && !t.IsSystem()
}

// TypeName returns the human-readable type name.
func (t TagInfo) TypeName() string {
	return TypeName(t.TypeCode)
}

// ElementCount returns the total number of elements for this tag.
// For scalars, returns 1. For arrays, returns the product of all dimensions.
func (t TagInfo) ElementCount() int {
	if len(t.Dimensions) == 0 {
		return 1
	}
	count := 1
	for _, d := range t.Dimensions {
		if d > 0 {
			count *= d
		}
	}
	if count < 1 {
		return 1
	}
	return count
}

// IsArray returns true if this tag is an array.
func (t TagInfo) IsArray() bool {
	return len(t.Dimensions) > 0 || IsArrayType(t.TypeCode)
}

// GetArrayDimensions fetches the array dimensions for a tag using Get Attribute Single.
// First tries attribute 8 (byte count), then falls back to attribute 3 (dimensions).
// Returns nil for scalars. The instance ID must be from the tag's discovery.
func (p *PLC) GetArrayDimensions(instance uint32, typeCode uint16) ([]int, error) {
	// Check if this is an array type
	if !IsArrayType(typeCode) {
		return nil, nil // Not an array
	}

	var attr8Err, attr3Err error

	// Try attribute 8 (byte count) - more widely supported
	byteCount, err := p.getSymbolByteCount(instance)
	if err != nil {
		attr8Err = err
	} else if byteCount > 0 {
		baseType := BaseType(typeCode)
		elemSize := TypeSize(baseType)
		if elemSize > 0 {
			elementCount := int(byteCount) / elemSize
			if elementCount > 1 {
				return []int{elementCount}, nil
			}
		}
	}

	// Fall back to attribute 3 (dimensions) for ControlLogix
	numDims := ArrayDimensions(typeCode)
	if numDims == 0 {
		// Can't try attribute 3 - return attribute 8 error if we had one
		if attr8Err != nil {
			return nil, fmt.Errorf("attr8: %w", attr8Err)
		}
		return nil, nil
	}

	dims, err := p.getSymbolDimensions(instance, numDims)
	if err != nil {
		attr3Err = err
	} else if len(dims) > 0 {
		return dims, nil
	}

	// Both failed - return combined error
	if attr8Err != nil && attr3Err != nil {
		return nil, fmt.Errorf("attr8: %v; attr3: %v", attr8Err, attr3Err)
	}
	if attr3Err != nil {
		return nil, fmt.Errorf("attr3: %w", attr3Err)
	}
	if attr8Err != nil {
		return nil, fmt.Errorf("attr8: %w", attr8Err)
	}
	return nil, nil
}

// getSymbolByteCount fetches attribute 8 (byte count) from a Symbol Object instance.
func (p *PLC) getSymbolByteCount(instance uint32) (uint32, error) {
	// Build path to Symbol Object, specific instance, attribute 8
	builder := cip.EPath().Class(0x6B)
	if instance <= 0xFF {
		builder = builder.Instance(byte(instance))
	} else if instance <= 0xFFFF {
		builder = builder.Instance16(uint16(instance))
	} else {
		builder = builder.Instance32(instance)
	}
	path, err := builder.Attribute(8).Build()
	if err != nil {
		return 0, err
	}

	// Build Get Attribute Single request
	reqData := make([]byte, 0, 2+len(path))
	reqData = append(reqData, SvcGetAttributeSingle)
	reqData = append(reqData, path.WordLen())
	reqData = append(reqData, path...)

	cipResp, err := p.sendCipRequest(reqData)
	if err != nil {
		return 0, err
	}

	if len(cipResp) < 4 {
		return 0, fmt.Errorf("response too short")
	}

	status := cipResp[2]
	addlStatusSize := cipResp[3]

	if status != StatusSuccess {
		return 0, parseCipError(status, addlStatusSize, cipResp[4:])
	}

	// Parse byte count (UDINT - 4 bytes)
	dataStart := 4 + int(addlStatusSize)*2
	if len(cipResp) < dataStart+4 {
		return 0, fmt.Errorf("insufficient data for byte count")
	}

	byteCount := binary.LittleEndian.Uint32(cipResp[dataStart : dataStart+4])
	return byteCount, nil
}

// GetTemplateSize returns the size in bytes of a structure/UDT type.
// The templateID is extracted from the type code (lower 12 bits when struct flag is set).
func (p *PLC) GetTemplateSize(typeCode uint16) (uint32, error) {
	if !IsStructure(typeCode) {
		return 0, fmt.Errorf("type 0x%04X is not a structure", typeCode)
	}

	// Template instance ID is in bits 11-0 of the type code
	templateID := uint16(typeCode & 0x0FFF)
	if templateID == 0 {
		return 0, fmt.Errorf("invalid template ID 0")
	}

	// Use getTemplateAttributeList which uses Get Attribute List service (0x03)
	// This works on PLCs that don't support Get Attribute Single (0x0E)
	return p.getTemplateStructureSize(templateID)
}

// getTemplateStructureSize fetches the structure size using Get Attribute List (0x03).
// This is more compatible than Get Attribute Single (0x0E) which some PLCs don't support.
func (p *PLC) getTemplateStructureSize(templateID uint16) (uint32, error) {
	// Build path to Template Object (class 0x6C), specific instance
	builder := cip.EPath().Class(0x6C)
	if templateID <= 0xFF {
		builder = builder.Instance(byte(templateID))
	} else {
		builder = builder.Instance16(templateID)
	}
	path, err := builder.Build()
	if err != nil {
		return 0, err
	}

	// Request attribute 5 (structure size, UDINT) using Get Attribute List
	attrData := []byte{
		0x01, 0x00, // Attribute count = 1
		0x05, 0x00, // Attribute 5: Structure size in bytes (UDINT)
	}

	reqData := make([]byte, 0, 2+len(path)+len(attrData))
	reqData = append(reqData, 0x03) // Get Attribute List service
	reqData = append(reqData, path.WordLen())
	reqData = append(reqData, path...)
	reqData = append(reqData, attrData...)

	cipResp, err := p.sendCipRequest(reqData)
	if err != nil {
		return 0, err
	}

	if len(cipResp) < 4 {
		return 0, fmt.Errorf("response too short: %d bytes", len(cipResp))
	}

	replyService := cipResp[0]
	status := cipResp[2]
	addlStatusSize := cipResp[3]

	if replyService != 0x83 {
		return 0, fmt.Errorf("unexpected reply service: 0x%02X", replyService)
	}

	if status != StatusSuccess {
		return 0, parseCipError(status, addlStatusSize, cipResp[4:])
	}

	// Parse response: [attr_count:2] [attr_id:2] [status:2] [value:4]
	dataStart := 4 + int(addlStatusSize)*2
	if len(cipResp) < dataStart+10 {
		return 0, fmt.Errorf("response too short for attribute data")
	}

	data := cipResp[dataStart:]
	// Skip attr_count (2), attr_id (2), status (2) = 6 bytes to get to value
	if len(data) < 10 {
		return 0, fmt.Errorf("insufficient attribute data")
	}

	attrStatus := binary.LittleEndian.Uint16(data[4:6])
	if attrStatus != 0 {
		return 0, fmt.Errorf("attribute error status: 0x%04X", attrStatus)
	}

	size := binary.LittleEndian.Uint32(data[6:10])
	return size, nil
}

// getSymbolDimensions fetches attribute 3 (dimensions) from a Symbol Object instance.
func (p *PLC) getSymbolDimensions(instance uint32, numDims int) ([]int, error) {
	builder := cip.EPath().Class(0x6B)
	if instance <= 0xFF {
		builder = builder.Instance(byte(instance))
	} else if instance <= 0xFFFF {
		builder = builder.Instance16(uint16(instance))
	} else {
		builder = builder.Instance32(instance)
	}
	path, err := builder.Attribute(3).Build()
	if err != nil {
		return nil, err
	}

	reqData := make([]byte, 0, 2+len(path))
	reqData = append(reqData, SvcGetAttributeSingle)
	reqData = append(reqData, path.WordLen())
	reqData = append(reqData, path...)

	cipResp, err := p.sendCipRequest(reqData)
	if err != nil {
		return nil, err
	}

	if len(cipResp) < 4 {
		return nil, fmt.Errorf("response too short")
	}

	status := cipResp[2]
	addlStatusSize := cipResp[3]

	if status != StatusSuccess {
		return nil, parseCipError(status, addlStatusSize, cipResp[4:])
	}

	dataStart := 4 + int(addlStatusSize)*2
	data := cipResp[dataStart:]

	if len(data) < numDims*4 {
		return nil, fmt.Errorf("insufficient data for %d dimensions", numDims)
	}

	dimensions := make([]int, numDims)
	for i := 0; i < numDims; i++ {
		dimensions[i] = int(binary.LittleEndian.Uint32(data[i*4 : i*4+4]))
	}

	return dimensions, nil
}

// ListTags returns all controller-scope tags and program entries.
// Use ListProgramTags to get tags within a specific program.
func (p *PLC) ListTags() ([]TagInfo, error) {
	debugLog("ListTags: discovering controller-scope tags")
	tags, err := p.listSymbols("", 0)
	if err != nil {
		debugLog("ListTags: error: %v", err)
		return nil, err
	}
	debugLog("ListTags: discovered %d tags", len(tags))
	return tags, nil
}

// ListPrograms returns just the program names from the PLC.
// Returns names like "Program:MainProgram", "Program:SafetyProgram", etc.
func (p *PLC) ListPrograms() ([]string, error) {
	tags, err := p.ListTags()
	if err != nil {
		return nil, err
	}

	var programs []string
	seen := make(map[string]bool)
	for _, t := range tags {
		if t.IsProgram() && !seen[t.Name] {
			seen[t.Name] = true
			programs = append(programs, t.Name)
		}
	}
	return programs, nil
}

// ListProgramTags returns all tags within a specific program.
// programName should be just the program name (e.g., "MainProgram"),
// or the full form (e.g., "Program:MainProgram").
func (p *PLC) ListProgramTags(programName string) ([]TagInfo, error) {
	// Normalize the program name
	if len(programName) < 8 || programName[:8] != "Program:" {
		programName = "Program:" + programName
	}
	return p.listSymbols(programName, 0)
}

// ListDataTags returns only readable/writable data tags, excluding programs, routines, and system tags.
func (p *PLC) ListDataTags() ([]TagInfo, error) {
	allTags, err := p.ListAllTags()
	if err != nil {
		return nil, err
	}

	var dataTags []TagInfo
	for _, t := range allTags {
		if t.IsReadable() {
			dataTags = append(dataTags, t)
		}
	}
	debugLog("ListDataTags: %d readable tags (of %d total)", len(dataTags), len(allTags))
	return dataTags, nil
}

// ListAllTags returns all tags: controller-scope, program entries, and tags within each program.
func (p *PLC) ListAllTags() ([]TagInfo, error) {
	// First, get controller-scope tags and program entries
	baseTags, err := p.ListTags()
	if err != nil {
		return nil, fmt.Errorf("ListAllTags: %w", err)
	}

	// Find all program entries
	var programs []string
	seen := make(map[string]bool)
	for _, t := range baseTags {
		if t.IsProgram() && !seen[t.Name] {
			seen[t.Name] = true
			programs = append(programs, t.Name)
		}
	}

	// Collect all tags
	allTags := make([]TagInfo, 0, len(baseTags))
	allTags = append(allTags, baseTags...)

	// Browse each program's tags
	for _, prog := range programs {
		progTags, err := p.listSymbols(prog, 0)
		if err != nil {
			// Skip programs that can't be browsed
			continue
		}

		// Prefix tags with program name if not already prefixed
		prefix := prog + "."
		for i := range progTags {
			if len(progTags[i].Name) < len(prefix) || progTags[i].Name[:len(prefix)] != prefix {
				// Check if already has any Program: prefix
				if len(progTags[i].Name) < 8 || progTags[i].Name[:8] != "Program:" {
					progTags[i].Name = prefix + progTags[i].Name
				}
			}
		}

		allTags = append(allTags, progTags...)
	}

	return allTags, nil
}

// listSymbols queries the Symbol Object (class 0x6B) for tag information.
// scope: "" for controller scope, or "Program:ProgramName" for program scope
// startInstance: starting instance for pagination (0 for first page)
func (p *PLC) listSymbols(scope string, startInstance uint32) ([]TagInfo, error) {
	if p == nil || p.Connection == nil {
		return nil, fmt.Errorf("listSymbols: nil plc or connection")
	}

	var allTags []TagInfo
	instance := startInstance

	// Pagination loop - limit to prevent infinite loops
	for page := 0; page < 1000; page++ {
		tags, lastInstance, hasMore, err := p.listSymbolsPage(scope, instance)
		if err != nil {
			return nil, err
		}

		allTags = append(allTags, tags...)

		if !hasMore || len(tags) == 0 {
			break
		}

		// Next page starts after the last instance we received
		instance = lastInstance + 1
	}

	return allTags, nil
}

// listSymbolsPage fetches one page of symbols.
func (p *PLC) listSymbolsPage(scope string, startInstance uint32) (tags []TagInfo, lastInstance uint32, hasMore bool, err error) {
	// Build the request path
	path, err := p.buildSymbolPath(scope, startInstance)
	if err != nil {
		return nil, 0, false, fmt.Errorf("buildSymbolPath: %w", err)
	}

	// Request attributes: name (1), type (2), byte count (8)
	// This matches pylogix's approach for getting array sizes during tag discovery.
	attrData := []byte{
		0x03, 0x00, // Attribute count: 3
		0x01, 0x00, // Attribute 1: Symbol Name
		0x02, 0x00, // Attribute 2: Symbol Type
		0x08, 0x00, // Attribute 8: Byte Count
	}

	// Build the CIP request
	reqData := make([]byte, 0, 2+len(path)+len(attrData))
	reqData = append(reqData, SvcGetInstanceAttributeList) // Service 0x55
	reqData = append(reqData, path.WordLen())
	reqData = append(reqData, path...)
	reqData = append(reqData, attrData...)

	// Send request using connected or unconnected messaging
	cipResp, err := p.sendCipRequest(reqData)
	if err != nil {
		return nil, 0, false, err
	}

	// Parse response header
	if len(cipResp) < 4 {
		return nil, 0, false, fmt.Errorf("response too short: %d bytes", len(cipResp))
	}

	replyService := cipResp[0]
	status := cipResp[2]
	addlStatusSize := cipResp[3]

	// Verify service
	if replyService != (SvcGetInstanceAttributeList | 0x80) {
		return nil, 0, false, fmt.Errorf("unexpected reply service: 0x%02X", replyService)
	}

	// Check for "partial transfer" (more data available)
	hasMore = (status == StatusPartialTransfer)

	// Check for errors (but partial transfer is OK)
	if status != StatusSuccess && status != StatusPartialTransfer {
		return nil, 0, false, parseCipError(status, addlStatusSize, cipResp[4:])
	}

	// Parse tag data (skip 4-byte header + additional status)
	dataStart := 4 + int(addlStatusSize)*2
	if dataStart > len(cipResp) {
		return nil, 0, hasMore, nil // No data
	}

	tags, lastInstance = parseSymbolListResponse(cipResp[dataStart:])
	return tags, lastInstance, hasMore, nil
}

// buildSymbolPath builds the EPath for symbol listing.
func (p *PLC) buildSymbolPath(scope string, startInstance uint32) (cip.EPath_t, error) {
	builder := cip.EPath()

	// Add program scope if specified
	if scope != "" {
		builder = builder.Symbol(scope)
	}

	// Add Symbol Object class (0x6B)
	builder = builder.Class(0x6B)

	// Add instance (using 16-bit if > 255)
	if startInstance <= 0xFF {
		builder = builder.Instance(byte(startInstance))
	} else if startInstance <= 0xFFFF {
		builder = builder.Instance16(uint16(startInstance))
	} else {
		return nil, fmt.Errorf("instance %d exceeds 16-bit maximum", startInstance)
	}

	return builder.Build()
}

// parseSymbolListResponse parses the tag list data from Get Instance Attribute List response.
// Response format per tag (pylogix-compatible, each entry is nameLen + 20 bytes):
// - Offset 0: Instance ID (2 bytes, UINT) - used for pagination
// - Offset 2: Unknown (2 bytes)
// - Offset 4: Name length (2 bytes, UINT)
// - Offset 6: Tag name (nameLen bytes)
// - Offset 6+nameLen: Symbol type (2 bytes, UINT)
// - Offset 8+nameLen: Array size (2 bytes, UINT) - element count for arrays
// - Remaining bytes up to nameLen+20: additional metadata
func parseSymbolListResponse(data []byte) (tags []TagInfo, lastInstance uint32) {
	i := 0

	for i < len(data) {
		// Need at least 8 bytes to read the header (instance + unknown + nameLen)
		if i+8 > len(data) {
			break
		}

		// Instance ID at offset 0 (UINT - 2 bytes, used for pagination)
		instance := uint32(binary.LittleEndian.Uint16(data[i : i+2]))

		// Name length at offset 4 (UINT - 2 bytes)
		nameLen := int(binary.LittleEndian.Uint16(data[i+4 : i+6]))

		// Each entry is nameLen + 20 bytes total
		entrySize := nameLen + 20
		if i+entrySize > len(data) {
			break
		}

		// Extract the entry
		entry := data[i : i+entrySize]

		// Tag name at offset 6
		name := string(entry[6 : 6+nameLen])

		// Symbol type at offset 6+nameLen (UINT - 2 bytes)
		typeCode := binary.LittleEndian.Uint16(entry[6+nameLen : 8+nameLen])

		// Array size at offset 8+nameLen (UINT - 2 bytes) - element count for arrays
		arraySize := binary.LittleEndian.Uint16(entry[8+nameLen : 10+nameLen])

		// Move to next entry
		i += entrySize

		// Skip if this looks like a partial/invalid entry
		if name == "" || instance == 0 {
			continue
		}

		// Calculate dimensions from array size for array types
		var dimensions []int
		if IsArrayType(typeCode) && arraySize > 0 {
			dimensions = []int{int(arraySize)}
		}

		tags = append(tags, TagInfo{
			Name:       name,
			TypeCode:   typeCode,
			Instance:   instance,
			Dimensions: dimensions,
		})

		lastInstance = instance
	}

	return tags, lastInstance
}

// FindSymbolByName searches the symbol table for a tag with the given name
// and returns its TagInfo with the correct TypeCode (including structure flag
// + template ID). For program-scoped tags like "Program:MainProgram.TagName",
// searches within the program's symbol table. For member paths like
// "ParentUDT.Child.Leaf", searches for the root tag only since members
// inherit their parent's structure type.
// Returns (nil, nil) if the tag is not found.
func (p *PLC) FindSymbolByName(tagName string) (*TagInfo, error) {
	if p == nil || p.Connection == nil {
		return nil, fmt.Errorf("FindSymbolByName: nil plc or connection")
	}

	// Determine scope and base name to search for
	scope := ""
	searchName := tagName

	// Handle program-scoped tags: "Program:ProgramName.TagName[.Member...]"
	if strings.HasPrefix(tagName, "Program:") {
		rest := tagName[8:]
		dotIdx := strings.Index(rest, ".")
		if dotIdx > 0 {
			scope = "Program:" + rest[:dotIdx] // Full "Program:ProgramName" needed by listSymbolsPage
			searchName = rest[dotIdx+1:]
		}
	}

	// For member access like "ParentTag.Child.Leaf", search for the root tag
	if dotIdx := strings.Index(searchName, "."); dotIdx > 0 {
		searchName = searchName[:dotIdx]
	}

	// Iterate symbol table pages to find the tag
	var instance uint32 = 0
	for page := 0; page < 1000; page++ {
		tags, lastInstance, hasMore, err := p.listSymbolsPage(scope, instance)
		if err != nil {
			return nil, err
		}
		for i := range tags {
			if tags[i].Name == searchName {
				return &tags[i], nil
			}
		}
		if !hasMore || len(tags) == 0 {
			break
		}
		instance = lastInstance + 1
	}

	return nil, nil
}
