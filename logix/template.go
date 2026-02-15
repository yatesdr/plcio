package logix

import (
	"encoding/binary"
	"fmt"
	"strings"

	"plcio/cip"
)

// Template represents a UDT/AOI structure definition from the PLC.
type Template struct {
	ID          uint16           // Template instance ID
	Name        string           // Structure name (e.g., "MyUDT")
	Size        uint32           // Size in bytes of structure instances
	Members     []TemplateMember // Member definitions
	MemberMap   map[string]int   // Map of member name to index in Members slice
	RawHandle   uint16           // Structure handle from PLC
	MemberCount uint16           // Number of members
}

// TemplateMember represents a single member within a UDT.
type TemplateMember struct {
	Name      string // Member name
	Type      uint16 // Type code (can be nested struct if 0x8xxx)
	Offset    uint32 // Byte offset within structure
	ArrayDims []int  // Array dimensions (nil for scalar)
	BitOffset uint8  // Bit offset for BOOL members within a byte
	Hidden    bool   // True if this is a hidden/internal member
}

// IsStructure returns true if this member is a nested structure/UDT.
func (m *TemplateMember) IsStructure() bool {
	return IsStructure(m.Type)
}

// IsArray returns true if this member is an array.
func (m *TemplateMember) IsArray() bool {
	return len(m.ArrayDims) > 0
}

// ElementCount returns the total number of elements (1 for scalar).
func (m *TemplateMember) ElementCount() int {
	if len(m.ArrayDims) == 0 {
		return 1
	}
	count := 1
	for _, d := range m.ArrayDims {
		count *= d
	}
	return count
}

// GetMember returns a member by name, or nil if not found.
func (t *Template) GetMember(name string) *TemplateMember {
	if t.MemberMap == nil {
		return nil
	}
	if idx, ok := t.MemberMap[name]; ok {
		return &t.Members[idx]
	}
	return nil
}

// templateAttributes holds the attributes fetched from the Template Object.
type templateAttributes struct {
	ObjectDefinitionSize uint32 // Attribute 4: size of definition in 32-bit words
	StructureSize        uint32 // Attribute 5: size of structure instances in bytes (pycomm3) or attr 3 (pylogix)
	MemberCount          uint16 // Attribute 2: number of members
	StructureHandle      uint16 // Attribute 1: handle/identifier
}

// GetTemplate fetches and parses a UDT template from the PLC.
// The templateID is extracted from a structure type code (lower 12 bits).
// This follows the pylogix/pycomm3 approach:
// 1. Get template attributes using Get Attribute List (0x03)
// 2. Read template definition using Read Tag (0x4C) with offset/length
func (p *PLC) GetTemplate(templateID uint16) (*Template, error) {
	if templateID == 0 {
		return nil, fmt.Errorf("invalid template ID 0")
	}

	debugLogVerbose("GetTemplate: fetching template ID %d (0x%04X)", templateID, templateID)

	// Step 1: Get template attributes to know how much data to read
	attrs, err := p.getTemplateAttributes(templateID)
	if err != nil {
		debugLogVerbose("GetTemplate: failed to get attributes for template %d: %v", templateID, err)
		return nil, fmt.Errorf("failed to get template attributes: %w", err)
	}

	debugLogVerbose("GetTemplate: attributes - defSize=%d, structSize=%d, memberCount=%d, handle=0x%04X",
		attrs.ObjectDefinitionSize, attrs.StructureSize, attrs.MemberCount, attrs.StructureHandle)

	// Step 2: Read the template definition data
	// Calculate bytes to read: (object_definition_size * 4) - 23, rounded up to 4-byte boundary
	bytesToRead := (attrs.ObjectDefinitionSize * 4) - 23
	// Round up to 4-byte boundary
	bytesToRead = ((bytesToRead + 3) / 4) * 4

	defData, err := p.readTemplateData(templateID, bytesToRead)
	if err != nil {
		debugLogVerbose("GetTemplate: failed to read definition for template %d: %v", templateID, err)
		return nil, fmt.Errorf("failed to read template definition: %w", err)
	}
	debugLogVerbose("GetTemplate: got %d bytes of definition data for template %d", len(defData), templateID)

	// Debug: show first 64 bytes of template data
	showLen := len(defData)
	if showLen > 64 {
		showLen = 64
	}
	debugLogVerbose("GetTemplate: first %d bytes of definition: % X", showLen, defData[:showLen])

	// Step 3: Parse the template definition
	tmpl := &Template{
		ID:          templateID,
		Size:        attrs.StructureSize,
		MemberCount: attrs.MemberCount,
		RawHandle:   attrs.StructureHandle,
		MemberMap:   make(map[string]int),
	}

	if err := tmpl.parseDefinition(defData, int(attrs.MemberCount)); err != nil {
		debugLogVerbose("GetTemplate: failed to parse definition for template %d: %v", templateID, err)
		return nil, fmt.Errorf("failed to parse template definition: %w", err)
	}

	// Calculate BOOL bit positions for packed BOOLs sharing the same offset
	// (Offsets come from PLC, but bit positions within a DINT need calculation)
	tmpl.calculateBoolBitOffsets()

	debugLogVerbose("GetTemplate: parsed template %d %q with %d visible members", templateID, tmpl.Name, len(tmpl.MemberMap))
	return tmpl, nil
}

// getTemplateAttributes fetches template attributes using Get Attribute List (0x03).
// This follows the pylogix approach: request attributes [0x04, 0x03, 0x02, 0x01].
func (p *PLC) getTemplateAttributes(templateID uint16) (*templateAttributes, error) {
	// Build path to Template Object (class 0x6C), specific instance
	var builder *cip.PathBuilder
	builder = cip.EPath().Class(0x6C)
	if templateID <= 0xFF {
		builder = builder.Instance(byte(templateID))
	} else {
		builder = builder.Instance16(templateID)
	}
	path, err := builder.Build()
	if err != nil {
		return nil, err
	}

	// Build Get Attribute List request
	// Service: 0x03 (Get Attribute List)
	// Attribute list: [count=5][attr5][attr4][attr3][attr2][attr1]
	// pycomm3 uses attribute 5 for structure size (UDINT, 4 bytes)
	// pylogix uses attribute 3 (UINT, 2 bytes) as fallback
	attrData := []byte{
		0x05, 0x00, // Attribute count = 5
		0x05, 0x00, // Attribute 5: Structure size in bytes (UDINT, pycomm3)
		0x04, 0x00, // Attribute 4: Object definition size (in 32-bit words)
		0x03, 0x00, // Attribute 3: Member byte count (UINT, fallback)
		0x02, 0x00, // Attribute 2: Member count
		0x01, 0x00, // Attribute 1: Structure handle
	}

	reqData := make([]byte, 0, 2+len(path)+len(attrData))
	reqData = append(reqData, 0x03) // Get Attribute List service
	reqData = append(reqData, path.WordLen())
	reqData = append(reqData, path...)
	reqData = append(reqData, attrData...)

	cipResp, err := p.sendCipRequest(reqData)
	if err != nil {
		return nil, err
	}

	// Parse response
	if len(cipResp) < 4 {
		return nil, fmt.Errorf("response too short: %d bytes", len(cipResp))
	}

	replyService := cipResp[0]
	status := cipResp[2]
	addlStatusSize := cipResp[3]

	// Verify it's a reply to Get Attribute List (0x03 | 0x80 = 0x83)
	if replyService != 0x83 {
		return nil, fmt.Errorf("unexpected reply service: 0x%02X", replyService)
	}

	if status != StatusSuccess {
		return nil, parseCipError(status, addlStatusSize, cipResp[4:])
	}

	// Parse attribute values from response
	// Response format: [attr_count:2] followed by [attr_id:2][status:2][value:n] for each
	dataStart := 4 + int(addlStatusSize)*2
	if len(cipResp) < dataStart+2 {
		return nil, fmt.Errorf("response missing attribute data")
	}

	data := cipResp[dataStart:]
	if len(data) < 2 {
		return nil, fmt.Errorf("response data too short")
	}

	// Debug: show raw attribute response data
	debugLogVerbose("getTemplateAttributes: raw response (%d bytes): % X", len(data), data)

	attrCount := binary.LittleEndian.Uint16(data[0:2])
	debugLogVerbose("getTemplateAttributes: got %d attributes in response", attrCount)

	attrs := &templateAttributes{}
	offset := 2

	for i := 0; i < int(attrCount) && offset+4 <= len(data); i++ {
		attrID := binary.LittleEndian.Uint16(data[offset : offset+2])
		attrStatus := binary.LittleEndian.Uint16(data[offset+2 : offset+4])
		offset += 4

		if attrStatus != 0 {
			debugLogVerbose("getTemplateAttributes: attribute %d has error status 0x%04X", attrID, attrStatus)
			// Still need to skip the value bytes - but we don't know the size
			// For safety, try to continue based on expected attribute sizes
			switch attrID {
			case 1, 2, 3: // UINT attributes (2 bytes)
				offset += 2
			case 4, 5: // UDINT attributes (4 bytes)
				offset += 4
			default:
				offset += 2 // Default to UINT
			}
			continue
		}

		// Parse attribute value based on ID
		// Template Object attribute sizes:
		// - Attribute 1: Structure handle (UINT, 2 bytes)
		// - Attribute 2: Member count (UINT, 2 bytes)
		// - Attribute 3: Member byte count (UINT, 2 bytes) - NOT structure size
		// - Attribute 4: Object definition size (UDINT, 4 bytes)
		// - Attribute 5: Structure size (UDINT, 4 bytes)
		switch attrID {
		case 1: // Structure handle (UINT, 2 bytes)
			if offset+2 <= len(data) {
				attrs.StructureHandle = binary.LittleEndian.Uint16(data[offset : offset+2])
				offset += 2
			}
		case 2: // Member count (UINT, 2 bytes)
			if offset+2 <= len(data) {
				attrs.MemberCount = binary.LittleEndian.Uint16(data[offset : offset+2])
				offset += 2
			}
		case 3: // Member byte count (UINT, 2 bytes) - use as structure size fallback
			if offset+2 <= len(data) {
				// This is NOT the structure size, but we can use it as fallback
				val := binary.LittleEndian.Uint16(data[offset : offset+2])
				if attrs.StructureSize == 0 {
					attrs.StructureSize = uint32(val)
				}
				offset += 2
			}
		case 4: // Object definition size (UDINT, 4 bytes)
			if offset+4 <= len(data) {
				attrs.ObjectDefinitionSize = binary.LittleEndian.Uint32(data[offset : offset+4])
				offset += 4
			}
		case 5: // Structure size (UDINT, 4 bytes)
			if offset+4 <= len(data) {
				attrs.StructureSize = binary.LittleEndian.Uint32(data[offset : offset+4])
				offset += 4
			}
		default:
			debugLogVerbose("getTemplateAttributes: unknown attribute %d", attrID)
			// Skip unknown attributes - assume UINT (2 bytes) as safer default
			offset += 2
		}
	}

	if attrs.ObjectDefinitionSize == 0 {
		return nil, fmt.Errorf("failed to get object definition size")
	}

	return attrs, nil
}

// readTemplateData reads the raw template definition bytes using Read Tag (0x4C).
// Uses the pylogix format: request_data = pack("<IH", offset, bytes_to_read)
func (p *PLC) readTemplateData(templateID uint16, totalBytes uint32) ([]byte, error) {
	// Build path to Template Object (class 0x6C), specific instance
	var builder *cip.PathBuilder
	builder = cip.EPath().Class(0x6C)
	if templateID <= 0xFF {
		builder = builder.Instance(byte(templateID))
	} else {
		builder = builder.Instance16(templateID)
	}
	path, err := builder.Build()
	if err != nil {
		return nil, err
	}

	var allData []byte
	offset := uint32(0)

	for offset < totalBytes {
		remaining := totalBytes - offset

		// Build Read Tag request with offset and length
		// pylogix format: pack("<IH", offset, remaining)
		// offset: 4 bytes (DINT)
		// remaining: 2 bytes (UINT) - but we may need to limit chunk size
		chunkSize := remaining
		if chunkSize > 4000 {
			chunkSize = 4000
		}

		reqPayload := make([]byte, 6)
		binary.LittleEndian.PutUint32(reqPayload[0:4], offset)
		binary.LittleEndian.PutUint16(reqPayload[4:6], uint16(chunkSize))

		reqData := make([]byte, 0, 2+len(path)+len(reqPayload))
		reqData = append(reqData, SvcReadTag) // 0x4C
		reqData = append(reqData, path.WordLen())
		reqData = append(reqData, path...)
		reqData = append(reqData, reqPayload...)

		cipResp, err := p.sendCipRequest(reqData)
		if err != nil {
			if len(allData) > 0 {
				debugLogVerbose("readTemplateData: error after %d bytes: %v", len(allData), err)
				break
			}
			return nil, err
		}

		if len(cipResp) < 4 {
			if len(allData) > 0 {
				break
			}
			return nil, fmt.Errorf("response too short: %d bytes", len(cipResp))
		}

		replyService := cipResp[0]
		status := cipResp[2]
		addlStatusSize := cipResp[3]

		// Verify it's a reply to Read Tag (0x4C | 0x80 = 0xCC)
		if replyService != 0xCC {
			return nil, fmt.Errorf("unexpected reply service: 0x%02X", replyService)
		}

		// Status 0x06 = partial transfer, continue reading
		if status != StatusSuccess && status != StatusPartialTransfer {
			if len(allData) > 0 {
				debugLogVerbose("readTemplateData: error after %d bytes: %v", len(allData), parseCipError(status, addlStatusSize, cipResp[4:]))
				break
			}
			return nil, parseCipError(status, addlStatusSize, cipResp[4:])
		}

		dataStart := 4 + int(addlStatusSize)*2
		if dataStart >= len(cipResp) {
			break
		}

		chunkData := cipResp[dataStart:]

		// Note: Template definition data does NOT have a type code prefix
		// unlike regular Read Tag responses. The data starts directly with
		// member definitions.

		allData = append(allData, chunkData...)
		offset += uint32(len(chunkData))

		debugLogVerbose("readTemplateData: read %d bytes at offset %d, total %d/%d, status=0x%02X",
			len(chunkData), offset-uint32(len(chunkData)), len(allData), totalBytes, status)

		// If success (not partial), we're done
		if status == StatusSuccess {
			break
		}
	}

	if len(allData) == 0 {
		return nil, fmt.Errorf("no definition data received")
	}

	return allData, nil
}

// parseDefinition parses the raw template definition bytes.
// Format (per pylogix):
// - Member definitions: memberCount * 8 bytes each
// - String table: null-terminated strings (template name, then member names)
func (t *Template) parseDefinition(data []byte, memberCount int) error {
	if memberCount <= 0 {
		return fmt.Errorf("invalid member count: %d", memberCount)
	}

	memberInfoSize := 8
	expectedInfoBytes := memberCount * memberInfoSize

	if len(data) < expectedInfoBytes {
		debugLogVerbose("parseDefinition: data too short for %d members (need %d bytes, have %d)",
			memberCount, expectedInfoBytes, len(data))
		// Adjust member count to fit available data
		memberCount = len(data) / memberInfoSize
		if memberCount == 0 {
			return fmt.Errorf("data too short: %d bytes", len(data))
		}
		expectedInfoBytes = memberCount * memberInfoSize
	}

	debugLogVerbose("parseDefinition: parsing %d members from %d bytes", memberCount, len(data))

	// Parse member info entries
	members := make([]TemplateMember, 0, memberCount)
	for i := 0; i < memberCount; i++ {
		idx := i * memberInfoSize
		if idx+8 > len(data) {
			break
		}

		entry := data[idx : idx+8]

		// Parse member entry (per pycomm3/CIP format):
		// Bytes 0-1: Type info (UINT) - array size for arrays
		// Bytes 2-3: Type code (UINT) - lower 12 bits = type, bit 13-14 = array flag, bit 15 = struct flag
		// Bytes 4-7: Member offset (UDINT) - actual byte offset within structure provided by PLC
		arraySize := binary.LittleEndian.Uint16(entry[0:2])
		typeVal := binary.LittleEndian.Uint16(entry[2:4])
		memberOffset := binary.LittleEndian.Uint32(entry[4:8])

		// Extract type info from typeVal
		dataTypeValue := typeVal & 0x0FFF   // Lower 12 bits (base type)
		isArray := (typeVal & 0x6000) >> 13 // Bits 13-14

		member := TemplateMember{
			Type:   typeVal,      // Keep full type value for IsStructure() check
			Offset: memberOffset, // Use actual offset from PLC, not calculated
		}

		// Set array dimensions if this is an array
		if isArray > 0 && arraySize > 0 {
			member.ArrayDims = []int{int(arraySize)}
		}

		// Log member details for debugging (show raw bytes for first few)
		if i < 5 {
			debugLogVerbose("  Member %d: raw=% X -> arraySize=%d typeVal=0x%04X offset=%d",
				i, entry, arraySize, typeVal, memberOffset)
		}

		// Check for hidden/internal members (type 0)
		if dataTypeValue == 0 {
			member.Hidden = true
		}

		members = append(members, member)
	}

	debugLogVerbose("parseDefinition: parsed %d member info entries", len(members))

	// Parse member names from the string table
	// String table starts after member definitions
	nameDataStart := len(members) * memberInfoSize
	if nameDataStart < len(data) {
		nameData := data[nameDataStart:]
		debugLogVerbose("parseDefinition: parsing names from %d bytes at offset %d", len(nameData), nameDataStart)
		// Show first 128 bytes of string table for debugging
		showLen := len(nameData)
		if showLen > 128 {
			showLen = 128
		}
		debugLogVerbose("parseDefinition: string table first %d bytes: % X", showLen, nameData[:showLen])

		names := parseNullTerminatedStrings(nameData, len(members)+1) // +1 for template name
		if len(names) > 0 {
			debugLogVerbose("parseDefinition: found %d names, first: %q", len(names), names[0])
		}

		// First name is the template name (may contain semicolon-separated parts)
		if len(names) > 0 {
			// Template name may be in format "Name;extra;info"
			// Extract just the name part before any semicolon
			templateName := names[0]
			if idx := strings.Index(templateName, ";"); idx >= 0 {
				templateName = templateName[:idx]
			}
			t.Name = templateName
			debugLogVerbose("parseDefinition: template name extracted: %q (full: %q)", templateName, names[0])
		}

		// Assign names to members
		for i := 0; i < len(members) && i+1 < len(names); i++ {
			members[i].Name = names[i+1]
			// Mark members as hidden if:
			// - Name starts with double underscore (pylogix convention)
			// - Name starts with colon (internal member)
			// - Name is empty or has control characters
			if len(members[i].Name) > 0 {
				name := members[i].Name
				if strings.HasPrefix(name, "__") || strings.HasPrefix(name, ":") || name[0] < 32 {
					members[i].Hidden = true
				}
			}
			// Log first 10 member names for debugging
			if i < 10 {
				debugLogVerbose("  Assigned member %d: name=%q offset=%d type=0x%04X",
					i, members[i].Name, members[i].Offset, members[i].Type)
			}
		}
		if len(members) > 10 {
			debugLogVerbose("  ... and %d more members", len(members)-10)
		}
	}

	// Build member map for quick lookup (only visible members)
	t.Members = members
	for i, m := range members {
		if m.Name != "" && !m.Hidden {
			t.MemberMap[m.Name] = i
		}
	}

	debugLogVerbose("parseDefinition: template %q has %d visible members (offsets from PLC)", t.Name, len(t.MemberMap))
	return nil
}

// calculateBoolBitOffsets calculates bit positions for BOOL members that share the same byte offset.
// PLC provides byte offsets, but BOOLs packed into a DINT share the same offset with different bit positions.
func (t *Template) calculateBoolBitOffsets() {
	if len(t.Members) == 0 {
		return
	}

	// Track bit position for BOOLs at each offset
	boolBitAtOffset := make(map[uint32]uint8)

	for i := range t.Members {
		member := &t.Members[i]
		baseType := member.Type & 0x0FFF

		if baseType == TypeBOOL {
			// Get current bit position for this offset, then increment
			bitPos := boolBitAtOffset[member.Offset]
			member.BitOffset = bitPos
			boolBitAtOffset[member.Offset] = bitPos + 1

			if bitPos < 5 {
				debugLogVerbose("  BOOL member %q: offset=%d, bitOffset=%d", member.Name, member.Offset, bitPos)
			}
		}
	}
}

// parseNullTerminatedStrings parses null-terminated strings from a byte slice.
func parseNullTerminatedStrings(data []byte, maxCount int) []string {
	var result []string
	var current []byte

	for _, b := range data {
		if b == 0 {
			if len(current) > 0 {
				result = append(result, string(current))
				current = nil
				if len(result) >= maxCount {
					break
				}
			}
		} else if b >= 32 && b < 127 { // Printable ASCII
			current = append(current, b)
		}
	}

	// Don't forget the last string if no null terminator
	if len(current) > 0 && len(result) < maxCount {
		result = append(result, string(current))
	}

	return result
}

// String returns a human-readable representation of the template.
func (t *Template) String() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Template %q (ID: %d, Size: %d bytes)\n", t.Name, t.ID, t.Size))
	for _, m := range t.Members {
		if m.Hidden {
			continue
		}
		typeStr := TypeName(m.Type)
		if m.IsArray() {
			typeStr += fmt.Sprintf("[%d]", m.ArrayDims[0])
		}
		sb.WriteString(fmt.Sprintf("  +%04X: %s %s\n", m.Offset, m.Name, typeStr))
	}
	return sb.String()
}
