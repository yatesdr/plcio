// Package omron provides unified Omron PLC communication.
// This file implements CIP-based tag discovery for EIP (NJ/NX series).
//
// Omron NJ/NX series PLCs use CIP (Common Industrial Protocol) over EtherNet/IP,
// but their implementation differs from Allen-Bradley Logix:
//
//   - Get Instance Attribute List (0x55) may or may not be supported depending on model
//   - Falls back to Get Attributes All (0x01) on individual Symbol Object instances
//   - Class 0x6A contains symbol table metadata (instance count)
//   - Class 0x6B contains the actual symbol/tag instances
//   - String data uses odd-byte padding (single byte when name length is odd)
//   - System variables start with underscore ('_')
//
// Discovery strategy (in order):
//  1. Service 0x55 (Get Instance Attribute List) — paginated, efficient
//  2. Service 0x5F (Omron Get All Instances) — paginated names on class 0x6A
//  3. GAA (Get Attributes All, 0x01) per instance — Omron-specific format
//  4. GAS (Get Attribute Single, 0x0E) per instance — per-instance fallback
//
// References:
//   - libplctag GitHub issues #317, #466
//   - Omron NJ/NX-series CPU Unit Built-in EtherNet/IP Port User's Manual (W506)
package omron

import (
	"encoding/binary"
	"fmt"

	"plcio/cip"
	"plcio/logging"
)

// CIP service codes for symbol discovery.
const (
	svcGetAttributesAll    byte = 0x01 // Get Attributes All
	svcGetAttributeSingle  byte = 0x0E // Get Attribute Single - reliable across firmware versions
	svcGetInstanceAttrList byte = 0x55 // Get Instance Attribute List - CIP tag browsing (paginated)
	svcOmronGetAllInst     byte = 0x5F // Omron-specific: Get All Instances (paginated name listing)

	// Symbol Object class IDs (Omron-specific)
	classSymbolTable byte = 0x6A // Symbol table metadata (contains instance count)
	classSymbol      byte = 0x6B // Symbol Object (tag instances)

	// Symbol Object attribute IDs
	attrSymbolName byte = 0x01 // Attribute 1: Name (CIP STRING)
	attrSymbolType byte = 0x02 // Attribute 2: Type (UINT16)
)


// listSymbols queries the Symbol Object (class 0x6B) for tag information.
// Uses an adaptive strategy: tries GAA (Get Attributes All) on each instance,
// and if GAA returns an invalid name, falls back to GAS (Get Attribute Single)
// for that instance. GAS is disabled after 3 consecutive GAS failures to avoid
// wasting requests on PLCs that don't support it.
func (c *Client) listSymbols() ([]TagInfo, error) {
	logging.DebugLog("EIP/Discovery", "Starting Omron-specific tag discovery using GAA/GAS iteration")

	maxInstance := c.getSymbolTableCount()
	if maxInstance > 0 {
		logging.DebugLog("EIP/Discovery", "Symbol table reports %d instances", maxInstance)
	} else {
		maxInstance = 10000
		logging.DebugLog("EIP/Discovery", "Symbol table metadata unavailable, using limit of %d", maxInstance)
	}

	var allTags []TagInfo
	consecutiveErrors := 0
	consecutiveGASErrors := 0
	gasDisabled := false
	const maxConsecutiveErrors = 10
	const maxConsecutiveGASErrors = 3

	for instance := uint32(1); instance <= uint32(maxInstance); instance++ {
		var tag TagInfo
		var err error

		// Try GAA first on every instance
		tag, err = c.getSymbolInstanceGAA(instance)
		if err == nil && tag.Name != "" && isValidTagName(tag.Name) {
			// GAA worked for this instance
		} else {
			// GAA failed or returned invalid name — try GAS as fallback for this instance
			if !gasDisabled {
				tag, err = c.getSymbolInstanceGAS(instance)
				if err != nil {
					consecutiveGASErrors++
					if consecutiveGASErrors >= maxConsecutiveGASErrors {
						gasDisabled = true
						logging.DebugLog("EIP/Discovery", "GAS disabled after %d consecutive failures", consecutiveGASErrors)
					}
				} else {
					consecutiveGASErrors = 0
				}
			} else {
				// Both GAA and GAS are ineffective; count as error
				err = fmt.Errorf("instance %d: GAA invalid, GAS disabled", instance)
			}
		}

		if err != nil {
			if isEIPConnectionError(err) {
				logging.DebugLog("EIP/Discovery", "Connection error at instance %d, stopping discovery: %v", instance, err)
				break
			}

			consecutiveErrors++
			if instance <= 10 {
				logging.DebugLog("EIP/Discovery", "Instance %d error: %v", instance, err)
			}

			if consecutiveErrors >= maxConsecutiveErrors {
				logging.DebugLog("EIP/Discovery", "Stopping after %d consecutive errors at instance %d",
					consecutiveErrors, instance)
				break
			}
			continue
		}

		consecutiveErrors = 0

		if tag.Name == "" {
			continue
		}

		if len(tag.Name) > 0 && (tag.Name[0] == '_' || tag.Name[0] == '$') {
			if instance <= 20 {
				logging.DebugLog("EIP/Discovery", "Skipping system tag: %s", tag.Name)
			}
			continue
		}

		allTags = append(allTags, tag)

		if instance <= 10 || instance%100 == 0 {
			logging.DebugLog("EIP/Discovery", "Instance %d: found tag %q (type=0x%04X %s)",
				instance, tag.Name, tag.TypeCode, TypeName(tag.TypeCode))
		}
	}

	logging.DebugLog("EIP/Discovery", "Discovery complete: %d user tags found (gasDisabled=%v)", len(allTags), gasDisabled)
	return allTags, nil
}

// listSymbolsService55 discovers tags using CIP service 0x55 (Get Instance Attribute List).
// This is the same paginated approach used by the logix driver and works on PLCs that
// support standard CIP tag browsing on class 0x6B.
func (c *Client) listSymbolsService55() ([]TagInfo, error) {
	logging.DebugLog("EIP/Discovery", "Trying Get Instance Attribute List (0x55) on class 0x6B")

	var allTags []TagInfo
	startInstance := uint32(0) // Start from instance 0 to get all tags

	for page := 0; page < 1000; page++ {
		// Build path: class 0x6B, instance N
		var path cip.EPath_t
		var err error
		if startInstance <= 0xFF {
			path, err = cip.EPath().Class(classSymbol).Instance(byte(startInstance)).Build()
		} else if startInstance <= 0xFFFF {
			path, err = cip.EPath().Class(classSymbol).Instance16(uint16(startInstance)).Build()
		} else {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("build path for instance %d: %w", startInstance, err)
		}

		// Request attributes 1 (name) and 2 (type)
		attrData := []byte{
			0x02, 0x00, // Attribute count: 2
			0x01, 0x00, // Attribute 1: Symbol Name
			0x02, 0x00, // Attribute 2: Symbol Type
		}

		req := cip.Request{
			Service: svcGetInstanceAttrList,
			Path:    path,
			Data:    attrData,
		}

		data, status, err := c.sendCIPRequestWithStatus(req)
		if err != nil {
			return nil, fmt.Errorf("service 0x55 page %d: %w", page, err)
		}

		hasMore := (status == 0x06) // Partial transfer = more pages

		// Parse the response data
		tags, lastInstance := parseService55Response(data)

		if len(tags) == 0 && page == 0 {
			return nil, fmt.Errorf("service 0x55: no tags parsed from %d bytes of response data", len(data))
		}

		allTags = append(allTags, tags...)

		logging.DebugLog("EIP/Discovery", "Service 0x55 page %d: %d tags, lastInstance=%d, hasMore=%v",
			page, len(tags), lastInstance, hasMore)

		if !hasMore || len(tags) == 0 {
			break
		}

		// Next page starts after the last instance
		startInstance = lastInstance + 1
	}

	logging.DebugLog("EIP/Discovery", "Service 0x55 complete: %d tags discovered", len(allTags))
	return allTags, nil
}

// parseService55Response parses the response from service 0x55 (Get Instance Attribute List).
// Tries AB/Logix format first, then falls back to a simpler CIP format.
func parseService55Response(data []byte) (tags []TagInfo, lastInstance uint32) {
	if len(data) == 0 {
		return nil, 0
	}

	// Try AB/Logix format first: [instance:2][unknown:2][nameLen:2][name:N][type:2][arraySize:2][metadata:8]
	// Each entry is nameLen + 20 bytes total.
	tags, lastInstance = parseService55ABFormat(data)
	if len(tags) > 0 {
		return tags, lastInstance
	}

	// Try standard CIP format: entries with [instance:2][attrCount:2][attrID:2][status:1][nameLen:2][name][attrID:2][status:1][type:2]
	// This is the generic CIP Get Instance Attribute List response format.
	tags, lastInstance = parseService55CIPFormat(data)
	if len(tags) > 0 {
		return tags, lastInstance
	}

	logging.DebugLog("EIP/Discovery", "Service 0x55: neither AB nor CIP format parsed from %d bytes: %X",
		len(data), data[:min(len(data), 64)])
	return nil, 0
}

// parseService55ABFormat parses service 0x55 response in AB/Logix format.
// Format per entry (nameLen + 20 bytes total):
//
//	[instance:2][unknown:2][nameLen:2][name:N][type:2][arraySize:2][metadata:8]
func parseService55ABFormat(data []byte) (tags []TagInfo, lastInstance uint32) {
	i := 0

	for i < len(data) {
		// Need at least 8 bytes: instance(2) + unknown(2) + nameLen(2) + min type(2)
		if i+8 > len(data) {
			break
		}

		instance := uint32(binary.LittleEndian.Uint16(data[i : i+2]))
		nameLen := int(binary.LittleEndian.Uint16(data[i+4 : i+6]))

		// Each entry is nameLen + 20 bytes total
		entrySize := nameLen + 20
		if nameLen <= 0 || nameLen > 255 || i+entrySize > len(data) {
			break
		}

		entry := data[i : i+entrySize]
		name := string(entry[6 : 6+nameLen])
		typeCode := binary.LittleEndian.Uint16(entry[6+nameLen : 8+nameLen])

		i += entrySize

		if instance == 0 || !isValidTagName(name) {
			continue
		}

		tag := TagInfo{
			Name:     name,
			TypeCode: typeCode,
			Instance: instance,
		}

		// Array size at offset 8+nameLen
		arraySize := binary.LittleEndian.Uint16(entry[8+nameLen : 10+nameLen])
		if IsArray(typeCode) && arraySize > 0 {
			tag.Dimensions = []uint32{uint32(arraySize)}
		}

		tags = append(tags, tag)
		lastInstance = instance
	}

	return tags, lastInstance
}

// parseService55CIPFormat parses service 0x55 response in standard CIP format.
// Standard CIP Get Instance Attribute List response:
//
//	[instance:2][name attr data][type attr data]
//
// Where each attribute block may include: [attrID:2][status:1][value...]
// But many implementations just pack: [instance:2][nameLen:2][name:N][type:2]
func parseService55CIPFormat(data []byte) (tags []TagInfo, lastInstance uint32) {
	i := 0

	for i < len(data) {
		// Need at least: instance(2) + nameLen(2) + minName(1) + type(2) = 7
		if i+7 > len(data) {
			break
		}

		instance := uint32(binary.LittleEndian.Uint16(data[i : i+2]))
		nameLen := int(binary.LittleEndian.Uint16(data[i+2 : i+4]))

		if nameLen <= 0 || nameLen > 255 || i+4+nameLen+2 > len(data) {
			break
		}

		name := string(data[i+4 : i+4+nameLen])
		typeCode := binary.LittleEndian.Uint16(data[i+4+nameLen : i+4+nameLen+2])
		i += 4 + nameLen + 2

		if instance == 0 || !isValidTagName(name) {
			continue
		}

		tags = append(tags, TagInfo{
			Name:     name,
			TypeCode: typeCode,
			Instance: instance,
		})
		lastInstance = instance
	}

	return tags, lastInstance
}

// listSymbolsOmron5F discovers tags using Omron-specific service 0x5F on class 0x6A.
// This service returns tag names in paginated batches. After getting names,
// type info is resolved per-tag using symbolic GAA (Get Attributes All with
// a symbolic path). This is the primary discovery method for NX1P2 and similar
// PLCs where class 0x6B GAA does not return tag names.
func (c *Client) listSymbolsOmron5F() ([]TagInfo, error) {
	logging.DebugLog("EIP/Discovery", "Trying Omron service 0x5F on class 0x6A")

	var allNames []omron5FEntry

	// Query both tag types, matching the C++ reference implementation.
	// tagType=1 (System) may contain application tags without _ or $ prefix.
	// tagType=2 (User) contains user-defined tags.
	tagTypes := []uint16{1, 2}
	tagTypeNames := []string{"System", "User"}

	for ti, tagType := range tagTypes {
		nextInstanceID := uint32(1)

		for page := 0; page < 1000; page++ {
			path, err := cip.EPath().Class(classSymbolTable).Instance(0x00).Build()
			if err != nil {
				return nil, fmt.Errorf("build 0x5F path: %w", err)
			}

			// Request data: [nextInstanceID:4][0x20000000:4][tagType:2]
			reqData := make([]byte, 10)
			binary.LittleEndian.PutUint32(reqData[0:4], nextInstanceID)
			reqData[4] = 0x20
			reqData[5] = 0x00
			reqData[6] = 0x00
			reqData[7] = 0x00
			binary.LittleEndian.PutUint16(reqData[8:10], tagType)

			req := cip.Request{
				Service: svcOmronGetAllInst,
				Path:    path,
				Data:    reqData,
			}

			data, _, err := c.sendCIPRequestWithStatus(req)
			if err != nil {
				if page == 0 {
					logging.DebugLog("EIP/Discovery", "Service 0x5F tagType=%s failed: %v", tagTypeNames[ti], err)
					break // Try next tag type
				}
				logging.DebugLog("EIP/Discovery", "Service 0x5F tagType=%s page %d error (continuing with %d names): %v",
					tagTypeNames[ti], page, len(allNames), err)
				break
			}

			entries, lastID := parseOmron5FResponse(data)

			logging.DebugLog("EIP/Discovery", "Service 0x5F tagType=%s page %d: %d names, lastInstance=%d",
				tagTypeNames[ti], page, len(entries), lastID)

			if len(entries) == 0 {
				break
			}

			allNames = append(allNames, entries...)
			nextInstanceID = lastID + 1
		}
	}

	if len(allNames) == 0 {
		return nil, fmt.Errorf("service 0x5F: no tags found")
	}

	// Filter system tags before type resolution
	var userEntries []omron5FEntry
	for _, entry := range allNames {
		if len(entry.name) > 0 && (entry.name[0] == '_' || entry.name[0] == '$') {
			continue
		}
		userEntries = append(userEntries, entry)
	}

	logging.DebugLog("EIP/Discovery", "Service 0x5F: got %d tag names (%d user), resolving types...",
		len(allNames), len(userEntries))

	// Resolve type info using batched MSP GAA
	names := make([]string, len(userEntries))
	for i, e := range userEntries {
		names[i] = e.name
	}
	typeMap := c.getSymbolTypesBatched(names)

	var tags []TagInfo
	for i, entry := range userEntries {
		typeCode := typeMap[entry.name]
		tag := TagInfo{
			Name:     entry.name,
			Instance: entry.instanceID,
			TypeCode: typeCode,
		}
		tags = append(tags, tag)

		if i < 10 || (i+1)%50 == 0 {
			logging.DebugLog("EIP/Discovery", "Tag %d: %q type=0x%04X (%s)",
				entry.instanceID, entry.name, typeCode, TypeName(typeCode))
		}
	}

	logging.DebugLog("EIP/Discovery", "Service 0x5F complete: %d tags discovered", len(tags))
	return tags, nil
}

// omron5FEntry holds a name and instance ID parsed from a 0x5F response.
type omron5FEntry struct {
	instanceID uint32
	name       string
}

// parseOmron5FResponse parses the response from Omron service 0x5F.
// Response format:
//
//	[numInstances:2][unknown:2]
//	Per entry: [instanceID:4][dataLen:2][class:2][instanceID:4][nameLen:1][name:N][padding...]
func parseOmron5FResponse(data []byte) (entries []omron5FEntry, lastID uint32) {
	if len(data) < 4 {
		return nil, 0
	}

	numInstances := binary.LittleEndian.Uint16(data[0:2])
	// Skip 2 unknown bytes
	i := 4

	for n := uint16(0); n < numInstances; n++ {
		// Need at least: instanceID(4) + dataLen(2) = 6
		if i+6 > len(data) {
			break
		}

		instanceID := binary.LittleEndian.Uint32(data[i : i+4])
		dataLen := int(binary.LittleEndian.Uint16(data[i+4 : i+6]))
		entryStart := i + 6

		// Validate we have enough data for this entry
		if entryStart+dataLen > len(data) {
			break
		}

		entryData := data[entryStart : entryStart+dataLen]

		// Entry data: [class:2][instanceID:4][nameLen:1][name:N][padding...]
		if len(entryData) < 7 { // 2+4+1 minimum
			i = entryStart + dataLen
			continue
		}

		nameLen := int(entryData[6])
		if nameLen <= 0 || 7+nameLen > len(entryData) {
			i = entryStart + dataLen
			continue
		}

		name := string(entryData[7 : 7+nameLen])

		if isValidTagName(name) {
			entries = append(entries, omron5FEntry{instanceID, name})
			lastID = instanceID
		}

		i = entryStart + dataLen
	}

	return entries, lastID
}

// getSymbolTypeByName gets type info for a tag using symbolic GAA.
// Sends Get Attributes All (0x01) with a symbolic path (0x91 nameLen name).
// Response format: [byteSize:4][typeCode:1][metadata...]
// Returns 0 on error (type unknown — tag is still usable).
func (c *Client) getSymbolTypeByName(name string) uint16 {
	path, err := cip.EPath().Symbol(name).Build()
	if err != nil {
		return 0
	}

	req := cip.Request{
		Service: svcGetAttributesAll,
		Path:    path,
	}

	data, err := c.sendCIPRequest(req)
	if err != nil {
		return 0
	}

	// Response: [byteSize:4][typeCode:1]
	if len(data) < 5 {
		return 0
	}

	typeCode := uint16(data[4])

	// Handle array type (0xA3): element type follows
	if typeCode == 0xA3 && len(data) >= 6 {
		elemType := uint16(data[5])
		return MakeArrayType(elemType)
	}

	return typeCode
}

// getSymbolTypesBatched resolves type codes for multiple tags using MSP-batched GAA.
// Falls back to sequential getSymbolTypeByName if MSP fails.
func (c *Client) getSymbolTypesBatched(names []string) map[string]uint16 {
	result := make(map[string]uint16, len(names))

	if len(names) == 0 {
		return result
	}

	// Process in batches to stay within connection size limits.
	// Each GAA request is ~(4 + nameLen) bytes; use conservative batch size.
	const batchSize = 40
	for start := 0; start < len(names); start += batchSize {
		end := start + batchSize
		if end > len(names) {
			end = len(names)
		}
		batch := names[start:end]
		c.getSymbolTypesMSP(batch, result)
	}

	return result
}

// getSymbolTypesMSP resolves types for a single batch of names via MSP.
// On MSP failure, falls back to sequential resolution for this batch.
func (c *Client) getSymbolTypesMSP(names []string, result map[string]uint16) {
	// Build individual GAA requests with symbolic paths
	requests := make([]cip.MultiServiceRequest, 0, len(names))
	validNames := make([]string, 0, len(names))
	for _, name := range names {
		path, err := cip.EPath().Symbol(name).Build()
		if err != nil {
			logging.DebugLog("EIP/Discovery", "Skipping type lookup for %q: bad path: %v", name, err)
			continue
		}
		requests = append(requests, cip.MultiServiceRequest{
			Service: svcGetAttributesAll,
			Path:    path,
		})
		validNames = append(validNames, name)
	}

	if len(requests) == 0 {
		return
	}

	// Build Multiple Service Packet
	msData, err := cip.BuildMultipleServiceRequest(requests)
	if err != nil {
		logging.DebugLog("EIP/Discovery", "MSP build failed, falling back to sequential: %v", err)
		for _, name := range validNames {
			result[name] = c.getSymbolTypeByName(name)
		}
		return
	}

	// Wrap in MSP envelope: service 0x0A → Message Router (class 2, instance 1)
	msPath, _ := cip.EPath().Class(0x02).Instance(1).Build()
	reqData := make([]byte, 0, 2+len(msPath)+len(msData))
	reqData = append(reqData, cip.SvcMultipleServicePacket)
	reqData = append(reqData, msPath.WordLen())
	reqData = append(reqData, msPath...)
	reqData = append(reqData, msData...)

	logging.DebugLog("EIP/Discovery", "Sending batched GAA MSP for %d tags (%d bytes)", len(validNames), len(reqData))

	cipResp, err := c.sendCIPRequestBatched(reqData)
	if err != nil {
		logging.DebugLog("EIP/Discovery", "MSP GAA request failed, falling back to sequential: %v", err)
		for _, name := range validNames {
			result[name] = c.getSymbolTypeByName(name)
		}
		return
	}

	// Parse MSP response header
	if len(cipResp) < 4 {
		logging.DebugLog("EIP/Discovery", "MSP GAA response too short (%d bytes), falling back", len(cipResp))
		for _, name := range validNames {
			result[name] = c.getSymbolTypeByName(name)
		}
		return
	}

	replyService := cipResp[0]
	status := cipResp[2]
	addlStatusSize := int(cipResp[3])

	if replyService != (cip.SvcMultipleServicePacket | 0x80) {
		logging.DebugLog("EIP/Discovery", "Unexpected MSP reply service 0x%02X, falling back", replyService)
		for _, name := range validNames {
			result[name] = c.getSymbolTypeByName(name)
		}
		return
	}

	// Status 0x00 = success, 0x1E = embedded service error (some succeeded)
	if status != 0x00 && status != 0x1E {
		logging.DebugLog("EIP/Discovery", "MSP GAA status 0x%02X, falling back to sequential", status)
		for _, name := range validNames {
			result[name] = c.getSymbolTypeByName(name)
		}
		return
	}

	dataStart := 4 + addlStatusSize*2
	if dataStart > len(cipResp) {
		logging.DebugLog("EIP/Discovery", "MSP GAA response missing data, falling back")
		for _, name := range validNames {
			result[name] = c.getSymbolTypeByName(name)
		}
		return
	}

	responses, err := cip.ParseMultipleServiceResponse(cipResp[dataStart:])
	if err != nil || len(responses) != len(validNames) {
		logging.DebugLog("EIP/Discovery", "MSP GAA parse error (got %d responses for %d names): %v, falling back",
			len(responses), len(validNames), err)
		for _, name := range validNames {
			result[name] = c.getSymbolTypeByName(name)
		}
		return
	}

	logging.DebugLog("EIP/Discovery", "Batched GAA: parsed %d responses", len(responses))

	for i, resp := range responses {
		name := validNames[i]
		if resp.Status != 0x00 {
			logging.DebugLog("EIP/Discovery", "Batched GAA %q: CIP error 0x%02X", name, resp.Status)
			continue
		}
		// GAA response data: [byteSize:4][typeCode:1][metadata...]
		if len(resp.Data) < 5 {
			continue
		}
		typeCode := uint16(resp.Data[4])
		if typeCode == 0xA3 && len(resp.Data) >= 6 {
			elemType := uint16(resp.Data[5])
			typeCode = MakeArrayType(elemType)
		}
		result[name] = typeCode
	}
}

// getSymbolTableCount queries class 0x6A to get the number of symbol instances.
// This is Omron-specific - the Symbol Table class contains metadata about
// the symbol table including the instance count.
// Returns 0 if the query fails (caller should use a default limit).
func (c *Client) getSymbolTableCount() uint32 {
	logging.DebugLog("EIP/Discovery", "Querying symbol table metadata (class 0x6A)")

	// Build path to Symbol Table class (0x6A), instance 0 (class-level)
	path, _ := cip.EPath().Class(classSymbolTable).Instance(0x00).Build()

	req := cip.Request{
		Service: svcGetAttributesAll,
		Path:    path,
	}

	respData, err := c.sendCIPRequest(req)
	if err != nil {
		logging.DebugLog("EIP/Discovery", "Symbol table query failed: %v", err)
		return 0
	}

	if len(respData) == 0 {
		logging.DebugLog("EIP/Discovery", "Symbol table response has no data")
		return 0
	}

	logging.DebugLog("EIP/Discovery", "Symbol table attributes (%d bytes): %X", len(respData), respData)
	attrData := respData

	// Standard CIP class-level Get Attributes All returns:
	//   Attr 1: Revision         (UINT16) — bytes [0:2]
	//   Attr 2: Max Instance ID  (UINT16) — bytes [2:4]
	//   Attr 3: Number of Instances (UINT16) — bytes [4:6]
	if len(attrData) >= 6 {
		numInstances := binary.LittleEndian.Uint16(attrData[4:6])
		logging.DebugLog("EIP/Discovery", "Symbol table: numInstances=%d (attr 3)", numInstances)
		return uint32(numInstances)
	} else if len(attrData) >= 4 {
		maxInstance := binary.LittleEndian.Uint16(attrData[2:4])
		logging.DebugLog("EIP/Discovery", "Symbol table: maxInstance=%d (attr 2, fallback)", maxInstance)
		return uint32(maxInstance)
	} else if len(attrData) >= 2 {
		val := binary.LittleEndian.Uint16(attrData[0:2])
		logging.DebugLog("EIP/Discovery", "Symbol table: value=%d (attr 1, raw fallback)", val)
		return uint32(val)
	}

	return 0
}


// getSymbolInstanceGAA tries Get Attributes All (0x01) for a symbol instance.
func (c *Client) getSymbolInstanceGAA(instance uint32) (TagInfo, error) {
	tag := TagInfo{Instance: instance}

	path := c.symbolInstancePath(instance)
	req := cip.Request{
		Service: svcGetAttributesAll,
		Path:    path,
	}

	respData, err := c.sendCIPRequest(req)
	if err != nil {
		return tag, fmt.Errorf("instance %d GAA: %w", instance, err)
	}
	if len(respData) == 0 {
		return tag, fmt.Errorf("instance %d GAA: no data", instance)
	}

	tag = c.parseOmronSymbolAttributes(respData, instance)
	return tag, nil
}

// getSymbolInstanceGAS reads a symbol instance using Get Attribute Single (0x0E).
func (c *Client) getSymbolInstanceGAS(instance uint32) (TagInfo, error) {
	tag := TagInfo{Instance: instance}

	// Read Name (attribute 1)
	namePath := c.symbolInstanceAttrPath(instance, attrSymbolName)
	nameReq := cip.Request{
		Service: svcGetAttributeSingle,
		Path:    namePath,
	}

	nameData, err := c.sendCIPRequest(nameReq)
	if err != nil {
		return tag, fmt.Errorf("instance %d name: %w", instance, err)
	}

	// Parse Name as CIP STRING: UINT16 LE length + chars
	if len(nameData) < 2 {
		return tag, fmt.Errorf("instance %d: name data too short (%d bytes)", instance, len(nameData))
	}
	nameLen := int(binary.LittleEndian.Uint16(nameData[0:2]))
	if nameLen <= 0 || 2+nameLen > len(nameData) {
		return tag, fmt.Errorf("instance %d: invalid name length %d", instance, nameLen)
	}
	tag.Name = string(nameData[2 : 2+nameLen])

	if !isValidTagName(tag.Name) {
		return tag, fmt.Errorf("instance %d: invalid name %q", instance, tag.Name)
	}

	// Read Type (attribute 2)
	typePath := c.symbolInstanceAttrPath(instance, attrSymbolType)
	typeReq := cip.Request{
		Service: svcGetAttributeSingle,
		Path:    typePath,
	}

	typeData, err := c.sendCIPRequest(typeReq)
	if err != nil {
		// Name succeeded but type failed — return with name only
		logging.DebugLog("EIP/Discovery", "Instance %d (%s): type query failed: %v", instance, tag.Name, err)
		return tag, nil
	}

	if len(typeData) >= 2 {
		tag.TypeCode = binary.LittleEndian.Uint16(typeData[0:2])
	}

	logging.DebugLog("EIP/Discovery", "Instance %d (GAS): name=%q type=0x%04X (%s)",
		instance, tag.Name, tag.TypeCode, TypeName(tag.TypeCode))

	return tag, nil
}

// symbolInstancePath builds an EPath to Symbol Object class 0x6B with the given instance.
func (c *Client) symbolInstancePath(instance uint32) cip.EPath_t {
	var path cip.EPath_t
	if instance <= 0xFF {
		path, _ = cip.EPath().Class(classSymbol).Instance(byte(instance)).Build()
	} else if instance <= 0xFFFF {
		path, _ = cip.EPath().Class(classSymbol).Instance16(uint16(instance)).Build()
	} else {
		path, _ = cip.EPath().Class(classSymbol).Instance32(instance).Build()
	}
	return path
}

// symbolInstanceAttrPath builds an EPath to a specific attribute of a Symbol Object instance.
func (c *Client) symbolInstanceAttrPath(instance uint32, attrID byte) cip.EPath_t {
	var path cip.EPath_t
	if instance <= 0xFF {
		path, _ = cip.EPath().Class(classSymbol).Instance(byte(instance)).Attribute(attrID).Build()
	} else if instance <= 0xFFFF {
		path, _ = cip.EPath().Class(classSymbol).Instance16(uint16(instance)).Attribute(attrID).Build()
	} else {
		path, _ = cip.EPath().Class(classSymbol).Instance32(instance).Attribute(attrID).Build()
	}
	return path
}

// isValidTagName checks if a tag name contains only printable ASCII characters.
func isValidTagName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if c < 0x20 || c > 0x7E {
			return false
		}
	}
	return true
}

// parseOmronSymbolAttributes parses the attribute data from Get Attributes All
// response for an Omron Symbol Object instance.
//
// Omron Symbol Object attributes (based on libplctag research and Wireshark captures):
//   - Name: STRING (1-byte length prefix + chars + optional padding byte if odd)
//   - Type: UINT (2 bytes, little-endian) - CIP data type code
//   - Additional attributes may follow (dimensions, byte count, etc.)
//
// NOTE: This format differs from Allen-Bradley Logix which uses a different
// attribute layout. The exact format may vary between Omron firmware versions.
func (c *Client) parseOmronSymbolAttributes(data []byte, instance uint32) TagInfo {
	tag := TagInfo{Instance: instance}

	if len(data) == 0 {
		return tag
	}

	logging.DebugLog("EIP/Discovery", "Parsing symbol instance %d: %d bytes: %X",
		instance, len(data), data)

	i := 0

	// Parse name: 1-byte length prefix followed by chars
	if i >= len(data) {
		return tag
	}
	nameLen := int(data[i])
	i++

	// Sanity check name length
	if nameLen <= 0 || nameLen > 255 || i+nameLen > len(data) {
		logging.DebugLog("EIP/Discovery", "Instance %d: invalid name length %d", instance, nameLen)
		return tag
	}

	tag.Name = string(data[i : i+nameLen])
	i += nameLen

	// Omron uses odd-byte padding: if name length is odd, skip 1 padding byte
	if nameLen%2 == 1 && i < len(data) {
		i++ // Skip padding byte
	}

	// Parse type code (2 bytes, little-endian)
	if i+2 > len(data) {
		logging.DebugLog("EIP/Discovery", "Instance %d (%s): no type code", instance, tag.Name)
		return tag
	}
	tag.TypeCode = binary.LittleEndian.Uint16(data[i : i+2])
	i += 2

	// Parse dimensions if this is an array type
	if IsArray(tag.TypeCode) && i+4 <= len(data) {
		// Try to read dimension count or element count
		// Format varies - try reading as element count first
		dimOrCount := binary.LittleEndian.Uint32(data[i : i+4])
		if dimOrCount > 0 && dimOrCount < 1000000 { // Reasonable array size
			tag.Dimensions = []uint32{dimOrCount}
			logging.DebugLog("EIP/Discovery", "Instance %d (%s): array with %d elements",
				instance, tag.Name, dimOrCount)
		}
	}

	logging.DebugLog("EIP/Discovery", "Instance %d: name=%q type=0x%04X (%s) dims=%v",
		instance, tag.Name, tag.TypeCode, TypeName(tag.TypeCode), tag.Dimensions)

	return tag
}

// min returns the minimum of two integers.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// allTagsEIP discovers all tags, trying multiple strategies:
//  1. Service 0x55 (Get Instance Attribute List) — standard CIP tag browsing, paginated
//  2. Service 0x5F (Omron Get All Instances) — paginated name listing on class 0x6A
//  3. GAA/GAS instance iteration — Omron-specific fallback
func (c *Client) allTagsEIP() ([]TagInfo, error) {
	logging.DebugLog("EIP/Discovery", "allTagsEIP: trying service 0x55, then 0x5F, then GAA/GAS fallback")

	// Try service 0x55 (works on many CIP devices including some Omron NX)
	tags, err := c.listSymbolsService55()
	if err == nil && len(tags) > 0 {
		logging.DebugLog("EIP/Discovery", "Service 0x55 succeeded: %d tags", len(tags))
		return tags, nil
	}
	if err != nil {
		logging.DebugLog("EIP/Discovery", "Service 0x55 failed: %v — trying service 0x5F", err)
	} else {
		logging.DebugLog("EIP/Discovery", "Service 0x55 returned 0 tags — trying service 0x5F")
	}

	// Try Omron-specific service 0x5F (works on NX1P2 and similar)
	tags, err = c.listSymbolsOmron5F()
	if err == nil && len(tags) > 0 {
		logging.DebugLog("EIP/Discovery", "Service 0x5F succeeded: %d tags", len(tags))
		return tags, nil
	}
	if err != nil {
		logging.DebugLog("EIP/Discovery", "Service 0x5F failed: %v — falling back to GAA/GAS", err)
	} else {
		logging.DebugLog("EIP/Discovery", "Service 0x5F returned 0 tags — falling back to GAA/GAS")
	}

	// Fall back to Omron-specific GAA/GAS instance iteration
	return c.listSymbols()
}

// allTagsEIPFallback is a fallback method if the primary discovery fails.
// It uses a simpler approach with smaller instance IDs and the same
// adaptive GAA/GAS strategy as listSymbols.
func (c *Client) allTagsEIPFallback() ([]TagInfo, error) {
	logging.DebugLog("EIP/Discovery", "allTagsEIPFallback: using simplified instance iteration")

	var tags []TagInfo
	consecutiveErrors := 0
	consecutiveGASErrors := 0
	gasDisabled := false
	const maxConsecutiveErrors = 10
	const maxConsecutiveGASErrors = 3

	for instance := uint32(1); instance <= 1000; instance++ {
		var tag TagInfo
		var err error

		// Try GAA first on every instance
		tag, err = c.getSymbolInstanceGAA(instance)
		if err == nil && tag.Name != "" && isValidTagName(tag.Name) {
			// GAA worked
		} else {
			if !gasDisabled {
				tag, err = c.getSymbolInstanceGAS(instance)
				if err != nil {
					consecutiveGASErrors++
					if consecutiveGASErrors >= maxConsecutiveGASErrors {
						gasDisabled = true
						logging.DebugLog("EIP/Discovery", "Fallback: GAS disabled after %d consecutive failures", consecutiveGASErrors)
					}
				} else {
					consecutiveGASErrors = 0
				}
			} else {
				err = fmt.Errorf("instance %d: GAA invalid, GAS disabled", instance)
			}
		}

		if err != nil {
			if isEIPConnectionError(err) {
				logging.DebugLog("EIP/Discovery", "Fallback: connection error at instance %d, stopping: %v", instance, err)
				break
			}
			consecutiveErrors++
			if instance <= 10 {
				logging.DebugLog("EIP/Discovery", "Fallback instance %d error: %v", instance, err)
			}
			if consecutiveErrors >= maxConsecutiveErrors {
				logging.DebugLog("EIP/Discovery", "Fallback: stopping after %d consecutive errors", consecutiveErrors)
				break
			}
			continue
		}

		consecutiveErrors = 0

		if tag.Name == "" {
			continue
		}
		if len(tag.Name) > 0 && (tag.Name[0] == '_' || tag.Name[0] == '$') {
			continue
		}

		tags = append(tags, tag)
		if len(tags) <= 10 {
			logging.DebugLog("EIP/Discovery", "Fallback: found tag %q (type=0x%04X)", tag.Name, tag.TypeCode)
		}
	}

	logging.DebugLog("EIP/Discovery", "Fallback complete: found %d tags (gasDisabled=%v)", len(tags), gasDisabled)
	return tags, nil
}
