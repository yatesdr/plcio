// Package omron batch operations for high-throughput reads.
// Implements batching strategies similar to Logix and S7 drivers.
package omron

import (
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/yatesdr/plcio/cip"
	"github.com/yatesdr/plcio/eip"
	"github.com/yatesdr/plcio/logging"
)

// Batch configuration constants
const (
	// FINS limits
	FINSMaxWordsPerRead   = 998  // Max words per single FINS read (protocol limit)
	FINSMaxMultiAreas     = 64   // Max areas in multi-memory read (model dependent, conservative)
	FINSMaxBitsPerRead    = 256  // Max bits per single FINS bit read

	// EIP/CIP limits
	EIPMaxServicesPerMSP  = 200  // CIP Multiple Service Packet limit
	EIPBatchSizeConnected = 50   // Batch size for connected messaging
	EIPBatchSizeUnconnected = 20 // Batch size for unconnected (conservative)
	EIPMaxResponsePayload = 4000 // Max response size for large Forward Open
)

// finsReadRequest represents a parsed FINS read request for batching.
type finsReadRequest struct {
	originalIndex int            // Position in original request list
	address       string         // Original address string
	parsed        *ParsedAddress // Parsed address components
	wordCount     int            // Number of words to read
}

// readGroup represents a group of contiguous reads that can be combined.
type readGroup struct {
	area      byte             // Memory area code
	startAddr uint16           // Starting address
	wordCount int              // Total words to read
	requests  []finsReadRequest // Requests in this group
}

// readEIPBatched reads multiple EIP/CIP tags using Multiple Service Packet batching.
func (c *Client) readEIPBatched(tagNames []string) ([]*TagValue, error) {
	if len(tagNames) == 0 {
		return nil, nil
	}

	results := make([]*TagValue, len(tagNames))

	// Determine batch size based on connection mode
	batchSize := EIPBatchSizeUnconnected
	if c.cipConn != nil {
		batchSize = EIPBatchSizeConnected
	}

	logging.DebugLog("Omron", "EIP batched read: %d tags, batch size=%d, connected=%v",
		len(tagNames), batchSize, c.cipConn != nil)

	// Process in batches
	for start := 0; start < len(tagNames); start += batchSize {
		end := start + batchSize
		if end > len(tagNames) {
			end = len(tagNames)
		}
		batch := tagNames[start:end]

		batchResults, err := c.readEIPBatch(batch)
		if err != nil {
			// Check if this is a connection error — if so, stop immediately
			if isEIPConnectionError(err) {
				logging.DebugLog("Omron", "EIP batch connection error, aborting remaining reads: %v", err)
				// Fill remaining results with the error
				for i := start; i < len(tagNames); i++ {
					results[i] = &TagValue{Name: tagNames[i], Error: err, bigEndian: false}
				}
				return results, err
			}

			// Non-connection error: fall back to individual reads for this batch
			logging.DebugLog("Omron", "EIP batch failed, falling back to individual: %v", err)
			for i, name := range batch {
				tv := c.readEIPSingle(name)
				results[start+i] = tv
				// If individual read hits a connection error, abort
				if tv.Error != nil && isEIPConnectionError(tv.Error) {
					logging.DebugLog("Omron", "EIP single read connection error, aborting: %v", tv.Error)
					for j := start + i + 1; j < len(tagNames); j++ {
						results[j] = &TagValue{Name: tagNames[j], Error: tv.Error, bigEndian: false}
					}
					return results, tv.Error
				}
			}
			continue
		}

		// Copy batch results to main results
		for i, tv := range batchResults {
			results[start+i] = tv
		}
	}

	// Check if all results have connection errors — escalate as batch error
	var firstConnErr error
	connErrCount := 0
	for _, tv := range results {
		if tv != nil && tv.Error != nil && isEIPConnectionError(tv.Error) {
			connErrCount++
			if firstConnErr == nil {
				firstConnErr = tv.Error
			}
		}
	}
	if firstConnErr != nil && connErrCount == len(results) {
		return results, firstConnErr
	}

	return results, nil
}

// readEIPBatch reads a single batch of tags using Multiple Service Packet.
// Uses CIP service 0x0A (Multiple Service Packet) to batch multiple Read Tag (0x4C) requests.
func (c *Client) readEIPBatch(tagNames []string) ([]*TagValue, error) {
	if len(tagNames) == 0 {
		return nil, nil
	}
	if len(tagNames) == 1 {
		// Single tag, no need for MSP overhead
		logging.DebugLog("EIP", "Single tag read (no MSP): %s", tagNames[0])
		return []*TagValue{c.readEIPSingle(tagNames[0])}, nil
	}

	logging.DebugLog("EIP", "Building MSP request for %d tags", len(tagNames))

	// Build individual read requests
	requests := make([]cip.MultiServiceRequest, len(tagNames))
	for i, tagName := range tagNames {
		path, err := cip.EPath().Symbol(tagName).Build()
		if err != nil {
			logging.DebugLog("EIP", "Invalid tag path %q: %v", tagName, err)
			return nil, fmt.Errorf("invalid tag path %q: %w", tagName, err)
		}

		logging.DebugLog("EIP", "MSP request[%d]: tag=%s path=%X", i, tagName, path)

		requests[i] = cip.MultiServiceRequest{
			Service: svcReadTag,
			Path:    path,
			Data:    []byte{0x01, 0x00}, // Element count = 1
		}
	}

	// Build Multiple Service Packet
	msData, err := cip.BuildMultipleServiceRequest(requests)
	if err != nil {
		logging.DebugLog("EIP", "Failed to build MSP: %v", err)
		return nil, fmt.Errorf("failed to build MSP: %w", err)
	}

	// Build the complete CIP request with MSP service and path
	// Path targets the Message Router (class 0x02, instance 1)
	msPath, _ := cip.EPath().Class(0x02).Instance(1).Build()
	reqData := make([]byte, 0, 2+len(msPath)+len(msData))
	reqData = append(reqData, cip.SvcMultipleServicePacket) // Service 0x0A
	reqData = append(reqData, msPath.WordLen())              // Path word length
	reqData = append(reqData, msPath...)                     // Path to Message Router
	reqData = append(reqData, msData...)                     // MSP payload

	logging.DebugLog("EIP", "MSP request: %d bytes, service=0x%02X, path=%X", len(reqData), cip.SvcMultipleServicePacket, msPath)
	logging.DebugTX("EIP", reqData)

	// Send request
	cipResp, err := c.sendCIPRequestBatched(reqData)
	if err != nil {
		logging.DebugLog("EIP", "MSP request failed: %v", err)
		return nil, fmt.Errorf("MSP request failed: %w", err)
	}

	logging.DebugRX("EIP", cipResp)

	// Parse Multiple Service Packet response header
	if len(cipResp) < 4 {
		logging.DebugLog("EIP", "MSP response too short: %d bytes", len(cipResp))
		return nil, fmt.Errorf("MSP response too short: %d bytes", len(cipResp))
	}

	replyService := cipResp[0]
	reserved := cipResp[1]
	status := cipResp[2]
	addlStatusSize := cipResp[3]

	logging.DebugLog("EIP", "MSP response header: service=0x%02X reserved=0x%02X status=0x%02X addlStatusSize=%d",
		replyService, reserved, status, addlStatusSize)

	if replyService != (cip.SvcMultipleServicePacket | 0x80) {
		logging.DebugLog("EIP", "Unexpected MSP reply service: 0x%02X (expected 0x%02X)", replyService, cip.SvcMultipleServicePacket|0x80)
		return nil, fmt.Errorf("unexpected reply service: 0x%02X (expected 0x%02X)", replyService, cip.SvcMultipleServicePacket|0x80)
	}

	// Status 0x1E = "Embedded service error" means MSP succeeded but some services failed
	if status != 0x00 && status != 0x1E {
		statusMsg := cipStatusMessage(status)
		logging.DebugLog("EIP", "MSP failed: status=0x%02X (%s)", status, statusMsg)
		return nil, fmt.Errorf("MSP failed with status 0x%02X (%s)", status, statusMsg)
	}

	// Parse individual responses
	dataStart := 4 + int(addlStatusSize)*2
	if dataStart > len(cipResp) {
		logging.DebugLog("EIP", "MSP response missing data after header")
		return nil, fmt.Errorf("MSP response missing data after header")
	}

	logging.DebugLog("EIP", "Parsing MSP responses starting at offset %d", dataStart)
	responses, err := cip.ParseMultipleServiceResponse(cipResp[dataStart:])
	if err != nil {
		logging.DebugLog("EIP", "Failed to parse MSP response: %v", err)
		return nil, fmt.Errorf("failed to parse MSP response: %w", err)
	}

	logging.DebugLog("EIP", "Parsed %d MSP responses", len(responses))

	if len(responses) != len(tagNames) {
		logging.DebugLog("EIP", "MSP response count mismatch: expected %d, got %d", len(tagNames), len(responses))
		return nil, fmt.Errorf("expected %d responses, got %d", len(tagNames), len(responses))
	}

	// Convert responses to TagValues
	results := make([]*TagValue, len(tagNames))
	for i, resp := range responses {
		tv := &TagValue{
			Name:      tagNames[i],
			Count:     1,
			bigEndian: false, // CIP is little-endian
		}

		logging.DebugLog("EIP", "MSP response[%d] %s: service=0x%02X status=0x%02X dataLen=%d",
			i, tagNames[i], resp.Service, resp.Status, len(resp.Data))

		// Status 0x00 = success, 0x06 = partial transfer (OK for reads)
		if resp.Status != 0x00 && resp.Status != 0x06 {
			statusMsg := cipStatusMessage(resp.Status)
			tv.Error = fmt.Errorf("CIP error 0x%02X (%s)", resp.Status, statusMsg)
			logging.DebugLog("EIP", "MSP response[%d] %s: ERROR %s", i, tagNames[i], statusMsg)
			results[i] = tv
			continue
		}

		if len(resp.Data) < 2 {
			tv.Error = fmt.Errorf("response data too short: %d bytes", len(resp.Data))
			logging.DebugLog("EIP", "MSP response[%d] %s: data too short", i, tagNames[i])
			results[i] = tv
			continue
		}

		tv.DataType = binary.LittleEndian.Uint16(resp.Data[0:2])
		if len(resp.Data) > 2 {
			tv.Bytes = resp.Data[2:]
		}

		logging.DebugLog("EIP", "MSP response[%d] %s: type=0x%04X (%s) dataLen=%d",
			i, tagNames[i], tv.DataType, TypeName(tv.DataType), len(tv.Bytes))
		results[i] = tv
	}

	logging.DebugLog("EIP", "MSP batch complete: %d tags in single request", len(tagNames))
	return results, nil
}

// cipStatusMessage returns a human-readable message for CIP general status codes.
func cipStatusMessage(status byte) string {
	switch status {
	case 0x00:
		return "Success"
	case 0x01:
		return "Connection failure"
	case 0x02:
		return "Resource unavailable"
	case 0x03:
		return "Invalid parameter value"
	case 0x04:
		return "Path segment error"
	case 0x05:
		return "Path destination unknown"
	case 0x06:
		return "Partial transfer"
	case 0x08:
		return "Service not supported"
	case 0x09:
		return "Invalid attribute value"
	case 0x0A:
		return "Attribute list error"
	case 0x0B:
		return "Already in requested mode/state"
	case 0x0C:
		return "Object state conflict"
	case 0x0D:
		return "Object already exists"
	case 0x0E:
		return "Attribute not settable"
	case 0x0F:
		return "Privilege violation"
	case 0x10:
		return "Device state conflict"
	case 0x11:
		return "Reply data too large"
	case 0x12:
		return "Fragmentation of primitive"
	case 0x13:
		return "Not enough data"
	case 0x14:
		return "Attribute not supported"
	case 0x15:
		return "Too much data"
	case 0x16:
		return "Object does not exist"
	case 0x17:
		return "Service fragmentation sequence not in progress"
	case 0x18:
		return "No stored attribute data"
	case 0x19:
		return "Store operation failure"
	case 0x1A:
		return "Routing failure"
	case 0x1B:
		return "Request packet too large"
	case 0x1C:
		return "Response packet too large"
	case 0x1D:
		return "Missing attribute list entry data"
	case 0x1E:
		return "Invalid attribute value list"
	case 0x1F:
		return "Embedded service error"
	case 0x20:
		return "Vendor specific error"
	case 0x21:
		return "Invalid parameter"
	case 0x22:
		return "Write-once value or medium already written"
	case 0x25:
		return "Key failure in path"
	case 0x26:
		return "Path size invalid"
	case 0x27:
		return "Unexpected attribute in list"
	case 0x28:
		return "Invalid member ID"
	case 0x29:
		return "Member not settable"
	default:
		return fmt.Sprintf("Unknown status 0x%02X", status)
	}
}

// sendCIPRequestBatched sends a CIP request, using connected messaging if available.
func (c *Client) sendCIPRequestBatched(reqData []byte) ([]byte, error) {
	if c.cipConn != nil {
		// Use connected messaging
		connData := c.cipConn.WrapConnected(reqData)
		cpf := c.buildConnectedCpf(connData)

		resp, err := c.eipClient.SendUnitDataTransaction(*cpf)
		if err != nil {
			return nil, err
		}

		if len(resp.Items) < 2 {
			return nil, fmt.Errorf("expected 2 CPF items")
		}

		_, cipResp, err := c.cipConn.UnwrapConnected(resp.Items[1].Data)
		return cipResp, err
	}

	// Use unconnected messaging
	cpf := eip.EipCommonPacket{
		Items: []eip.EipCommonPacketItem{
			{TypeId: eip.CpfAddressNullId, Length: 0, Data: nil},
			{TypeId: eip.CpfUnconnectedMessageId, Length: uint16(len(reqData)), Data: reqData},
		},
	}

	resp, err := c.eipClient.SendRRData(cpf)
	if err != nil {
		return nil, err
	}

	if len(resp.Items) < 2 {
		return nil, fmt.Errorf("invalid CIP response")
	}

	return resp.Items[1].Data, nil
}

// buildConnectedCpf builds a CPF packet for connected messaging.
func (c *Client) buildConnectedCpf(data []byte) *eip.EipCommonPacket {
	return &eip.EipCommonPacket{
		Items: []eip.EipCommonPacketItem{
			{
				TypeId: eip.CpfAddressConnectionId,
				Length: 4,
				Data:   binary.LittleEndian.AppendUint32(nil, c.cipConn.OTConnID),
			},
			{
				TypeId: eip.CpfConnectedTransportPacketId,
				Length: uint16(len(data)),
				Data:   data,
			},
		},
	}
}

// readEIPSingle reads a single EIP tag (fallback for batch failures).
func (c *Client) readEIPSingle(tagName string) *TagValue {
	tv := &TagValue{
		Name:      tagName,
		Count:     1,
		bigEndian: false,
	}

	path, err := cip.EPath().Symbol(tagName).Build()
	if err != nil {
		tv.Error = fmt.Errorf("invalid tag path: %w", err)
		return tv
	}

	reqData := binary.LittleEndian.AppendUint16(nil, 1)
	req := cip.Request{
		Service: svcReadTag,
		Path:    path,
		Data:    reqData,
	}

	respData, err := c.sendCIPRequest(req)
	if err != nil {
		tv.Error = err
		return tv
	}

	if len(respData) < 2 {
		tv.Error = fmt.Errorf("response too short")
		return tv
	}

	tv.DataType = binary.LittleEndian.Uint16(respData[0:2])
	if len(respData) > 2 {
		tv.Bytes = respData[2:]
	}
	return tv
}

// readFINSBatched reads multiple FINS addresses using optimized batching.
// Strategy:
// 1. Parse and classify all addresses
// 2. Group contiguous word addresses into bulk reads
// 3. Use multi-memory-read for scattered addresses
// 4. Read bits separately (grouped by word if possible)
func (c *Client) readFINSBatched(addresses []string) ([]*TagValue, error) {
	// Convert to TagRequests without type hints
	requests := make([]TagRequest, len(addresses))
	for i, addr := range addresses {
		requests[i] = TagRequest{Address: addr}
	}
	return c.readFINSBatchedWithTypes(requests)
}

// readFINSBatchedWithTypes reads multiple FINS addresses with type hints using optimized batching.
func (c *Client) readFINSBatchedWithTypes(tagRequests []TagRequest) ([]*TagValue, error) {
	if len(tagRequests) == 0 {
		return nil, nil
	}

	results := make([]*TagValue, len(tagRequests))

	// Parse all addresses with type hints
	requests := make([]finsReadRequest, 0, len(tagRequests))
	var bitRequests []finsReadRequest

	for i, tagReq := range tagRequests {
		parsed, err := ParseAddressWithType(tagReq.Address, tagReq.TypeHint)
		if err != nil {
			results[i] = &TagValue{Name: tagReq.Address, Error: err, bigEndian: true}
			continue
		}

		// Round up to ensure we read enough words for odd-byte types like BYTE[3]
		byteCount := TypeSize(parsed.TypeCode) * parsed.Count
		wordCount := (byteCount + 1) / 2
		if wordCount < 1 {
			wordCount = 1
		}

		req := finsReadRequest{
			originalIndex: i,
			address:       tagReq.Address,
			parsed:        parsed,
			wordCount:     wordCount,
		}

		if parsed.TypeCode == TypeBool {
			bitRequests = append(bitRequests, req)
		} else {
			requests = append(requests, req)
		}
	}

	// Process word requests with batching
	if len(requests) > 0 {
		c.readFINSWordsBatched(requests, results)
	}

	// Process bit requests
	for _, req := range bitRequests {
		c.readFINSBits(req, results)
	}

	return results, nil
}

// readFINSWordsBatched reads word requests with optimized batching.
func (c *Client) readFINSWordsBatched(requests []finsReadRequest, results []*TagValue) {
	if len(requests) == 0 {
		return
	}

	// Sort requests by area and address for grouping
	sort.Slice(requests, func(i, j int) bool {
		if requests[i].parsed.MemoryArea != requests[j].parsed.MemoryArea {
			return requests[i].parsed.MemoryArea < requests[j].parsed.MemoryArea
		}
		return requests[i].parsed.Address < requests[j].parsed.Address
	})

	// Group contiguous addresses
	groups := c.groupContiguousAddresses(requests)

	logging.DebugLog("Omron", "FINS batched read: %d requests grouped into %d groups",
		len(requests), len(groups))

	// Decide strategy based on number of groups
	if len(groups) == 1 || len(groups) <= 3 {
		// Few groups: use individual bulk reads
		for _, group := range groups {
			c.readFINSGroup(group, results)
		}
	} else if len(groups) <= FINSMaxMultiAreas {
		// Many non-contiguous groups: try multi-memory read
		err := c.readFINSMultiMemory(groups, results)
		if err != nil {
			// Fall back to individual group reads
			logging.DebugLog("Omron", "Multi-memory read failed, falling back: %v", err)
			for _, group := range groups {
				c.readFINSGroup(group, results)
			}
		}
	} else {
		// Too many groups for multi-memory, batch them
		for i := 0; i < len(groups); i += FINSMaxMultiAreas {
			end := i + FINSMaxMultiAreas
			if end > len(groups) {
				end = len(groups)
			}
			batch := groups[i:end]
			err := c.readFINSMultiMemory(batch, results)
			if err != nil {
				for _, group := range batch {
					c.readFINSGroup(group, results)
				}
			}
		}
	}
}

// groupContiguousAddresses groups requests with contiguous addresses.
func (c *Client) groupContiguousAddresses(requests []finsReadRequest) []readGroup {
	if len(requests) == 0 {
		return nil
	}

	var groups []readGroup
	currentGroup := readGroup{
		area:      requests[0].parsed.MemoryArea,
		startAddr: requests[0].parsed.Address,
		wordCount: requests[0].wordCount,
		requests:  []finsReadRequest{requests[0]},
	}

	for i := 1; i < len(requests); i++ {
		req := requests[i]

		// Check if this request is contiguous with current group
		expectedAddr := currentGroup.startAddr + uint16(currentGroup.wordCount)
		isContiguous := req.parsed.MemoryArea == currentGroup.area &&
			req.parsed.Address == expectedAddr &&
			currentGroup.wordCount+req.wordCount <= FINSMaxWordsPerRead

		if isContiguous {
			// Add to current group
			currentGroup.wordCount += req.wordCount
			currentGroup.requests = append(currentGroup.requests, req)
		} else {
			// Start new group
			groups = append(groups, currentGroup)
			currentGroup = readGroup{
				area:      req.parsed.MemoryArea,
				startAddr: req.parsed.Address,
				wordCount: req.wordCount,
				requests:  []finsReadRequest{req},
			}
		}
	}

	// Don't forget the last group
	groups = append(groups, currentGroup)

	return groups
}

// readFINSGroup reads a contiguous group of addresses in a single FINS request.
func (c *Client) readFINSGroup(group readGroup, results []*TagValue) {
	logging.DebugLog("Omron", "FINS group read: area=0x%02X addr=%d count=%d (%d tags)",
		group.area, group.startAddr, group.wordCount, len(group.requests))

	// Read all words in one request
	words, err := c.fins.readWords(group.area, group.startAddr, uint16(group.wordCount))
	if err != nil {
		// Mark all requests in group as failed
		for _, req := range group.requests {
			results[req.originalIndex] = &TagValue{
				Name:      req.address,
				Error:     err,
				bigEndian: true,
			}
		}
		return
	}

	// Distribute results to each request
	wordOffset := 0
	for _, req := range group.requests {
		tv := &TagValue{
			Name:      req.address,
			DataType:  req.parsed.TypeCode,
			Count:     req.parsed.Count,
			bigEndian: true,
		}

		// Extract this request's words
		data := make([]byte, req.wordCount*2)
		for j := 0; j < req.wordCount && wordOffset+j < len(words); j++ {
			binary.BigEndian.PutUint16(data[j*2:j*2+2], words[wordOffset+j])
		}
		tv.Bytes = data

		if req.parsed.Count > 1 {
			tv.DataType = MakeArrayType(req.parsed.TypeCode)
		}

		results[req.originalIndex] = tv
		wordOffset += req.wordCount
	}
}

// readFINSMultiMemory reads multiple non-contiguous areas using FINS 0x0104.
// Note: FINS 0x0104 has limitations - it reads 1 word per address entry.
// For groups with wordCount > 1, this function falls back to individual 0x0101 reads.
func (c *Client) readFINSMultiMemory(groups []readGroup, results []*TagValue) error {
	if len(groups) == 0 {
		return nil
	}

	// Check if any group needs more than 1 word - if so, fall back to individual reads
	// because FINS 0x0104 only reads 1 word per address entry
	needFallback := false
	for _, group := range groups {
		if group.wordCount > 1 {
			needFallback = true
			break
		}
	}

	if needFallback {
		logging.DebugLog("FINS", "MultiMemory: groups have wordCount>1, using individual 0x0101 reads instead")
		for _, group := range groups {
			c.readFINSGroup(group, results)
		}
		return nil
	}

	logging.DebugLog("FINS", "MultiMemory read: %d groups (single-word each)", len(groups))

	// Build multi-memory read request
	data := BuildMultiMemoryReadRequest(groups)

	logging.DebugLog("FINS", "Sending 0x0104 command, request data: %X", data)
	resp, err := c.fins.sendCommand(FINSCmdMultiMemoryRead, data)
	if err != nil {
		logging.DebugLog("FINS", "MultiMemory 0x0104 command failed: %v", err)
		return err
	}

	logging.DebugLog("FINS", "MultiMemory response: %d bytes: %X", len(resp), resp)

	// Parse response and distribute to results
	return ParseMultiMemoryReadResponse(resp, groups, results)
}

// readFINSBits reads bit requests (one at a time for now, could be optimized).
func (c *Client) readFINSBits(req finsReadRequest, results []*TagValue) {
	tv := &TagValue{
		Name:      req.address,
		DataType:  TypeBool,
		Count:     req.parsed.Count,
		bigEndian: true,
	}

	bitArea := BitAreaFromWordArea(req.parsed.MemoryArea)
	bits, err := c.fins.readBits(bitArea, req.parsed.Address, req.parsed.BitOffset, uint16(req.parsed.Count))
	if err != nil {
		tv.Error = err
		results[req.originalIndex] = tv
		return
	}

	// Convert bits to bytes
	data := make([]byte, len(bits)*2)
	for j, b := range bits {
		if b {
			data[j*2+1] = 1
		}
	}
	tv.Bytes = data

	if req.parsed.Count > 1 {
		tv.DataType = MakeArrayType(TypeBool)
	}

	results[req.originalIndex] = tv
}

// BuildMultiMemoryReadRequest builds a FINS 0x0104 multi-memory read request.
// Per Omron W227 FINS Commands Reference, Memory Area Read Multiple (0x0104):
// Request format: [number_of_elements(1)] + [area(1) + address(3)]×n
// Where address is: beginning_address_high(1) + beginning_address_low(1) + bit_position(1)
// Note: This is NOT the same as 0x0101 format (which has count per area)
// 0x0104 reads exactly 1 word per specified address.
//
// For reading multiple consecutive words, we need to list each address individually,
// OR use the standard 0x0101 Memory Read which supports count.
//
// Fallback: This implementation uses individual 0x0101 calls instead.
func BuildMultiMemoryReadRequest(groups []readGroup) []byte {
	// IMPORTANT: FINS 0x0104 reads 1 word per address entry. It does NOT support
	// specifying a count per area. To read multiple words, we would need to list
	// each address individually (which is inefficient for large contiguous blocks).
	//
	// This format matches what was originally intended but may need adjustment
	// based on actual PLC behavior. The format below attempts to be compatible:
	//
	// Format: [number_of_elements(1)] + [area(1), addr_hi(1), addr_lo(1), bit(1)]×n
	// This reads 1 word per entry. For contiguous reads, use 0x0101 instead.

	// For now, we log a warning if any group has wordCount > 1
	for _, group := range groups {
		if group.wordCount > 1 {
			logging.DebugLog("FINS", "WARNING: MultiMemoryRead group has wordCount=%d but 0x0104 reads 1 word per address", group.wordCount)
		}
	}

	// Build request: each entry is 4 bytes (area + 3-byte address)
	data := make([]byte, 1+len(groups)*4)
	data[0] = byte(len(groups))

	for i, group := range groups {
		offset := 1 + i*4
		data[offset] = group.area
		data[offset+1] = byte(group.startAddr >> 8) // Address high byte
		data[offset+2] = byte(group.startAddr)      // Address low byte
		data[offset+3] = 0                          // Bit position (0 for word reads)

		logging.DebugLog("FINS", "MultiMemoryRead entry %d: area=0x%02X(%s) addr=%d",
			i, group.area, AreaName(group.area), group.startAddr)
	}

	logging.DebugLog("FINS", "MultiMemoryRead request: %d elements, %d bytes: %X", len(groups), len(data), data)
	return data
}

// ParseMultiMemoryReadResponse parses a FINS 0x0104 response.
// Per Omron W227, 0x0104 response format:
// - Data: [word1_hi][word1_lo] + [word2_hi][word2_lo] + ...
// - Each address entry returns exactly 2 bytes (1 word)
func ParseMultiMemoryReadResponse(resp []byte, groups []readGroup, results []*TagValue) error {
	logging.DebugLog("FINS", "Parsing MultiMemory response: %d bytes for %d groups", len(resp), len(groups))

	// For 0x0104: each group returns 1 word (2 bytes)
	expectedTotal := len(groups) * 2
	if len(resp) < expectedTotal {
		logging.DebugLog("FINS", "MultiMemory response too short: got %d bytes, expected %d", len(resp), expectedTotal)
		// Mark all as errors
		for _, group := range groups {
			for _, req := range group.requests {
				results[req.originalIndex] = &TagValue{
					Name:      req.address,
					Error:     fmt.Errorf("multi-memory response too short: got %d bytes, expected %d", len(resp), expectedTotal),
					bigEndian: true,
				}
			}
		}
		return fmt.Errorf("response too short")
	}

	// Each group gets 2 bytes (1 word)
	offset := 0
	for i, group := range groups {
		wordData := resp[offset : offset+2]
		offset += 2

		logging.DebugLog("FINS", "MultiMemory entry %d: area=0x%02X addr=%d data=%X (value=%d)",
			i, group.area, group.startAddr, wordData, binary.BigEndian.Uint16(wordData))

		// Each group should have exactly 1 request for 0x0104
		for _, req := range group.requests {
			tv := &TagValue{
				Name:      req.address,
				DataType:  req.parsed.TypeCode,
				Count:     1, // 0x0104 returns 1 word per entry
				Bytes:     make([]byte, 2),
				bigEndian: true,
			}
			copy(tv.Bytes, wordData)

			results[req.originalIndex] = tv
		}
	}

	return nil
}
