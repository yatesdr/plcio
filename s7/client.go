package s7

import (
	"encoding/binary"
	"fmt"
	"math"
	"sync"
	"time"

	"plcio/logging"
)

// Client is a high-level wrapper for S7 PLC communication.
type Client struct {
	transport *transport
	address   string
	rack      int
	slot      int
	pduRef    uint16
	mu        sync.Mutex
}

// options holds configuration options for Connect.
type options struct {
	rack    int
	slot    int
	timeout time.Duration
}

// Option is a functional option for Connect.
type Option func(*options)

// WithRackSlot configures the rack and slot numbers for the PLC.
// Default is rack 0, slot 2 (common for S7-300/400 where CPU is in slot 2).
// For S7-1200/1500, use rack 0, slot 0 (CPU is onboard).
func WithRackSlot(rack, slot int) Option {
	return func(o *options) {
		o.rack = rack
		o.slot = slot
	}
}

// WithTimeout configures the connection timeout.
func WithTimeout(d time.Duration) Option {
	return func(o *options) {
		o.timeout = d
	}
}

// Connect establishes a connection to an S7 PLC at the given address.
func Connect(address string, opts ...Option) (*Client, error) {
	// Apply options
	// Default to slot 2 for S7-300/400 (CPU typically in slot 2)
	// S7-1200/1500 users should explicitly set slot 0 (integrated CPU)
	cfg := &options{
		rack:    0,
		slot:    2,
		timeout: 10 * time.Second,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	t := newTransport()
	t.timeout = cfg.timeout

	if err := t.connect(address, cfg.rack, cfg.slot); err != nil {
		return nil, fmt.Errorf("Connect: %w", err)
	}

	return &Client{
		transport: t,
		address:   address,
		rack:      cfg.rack,
		slot:      cfg.slot,
		pduRef:    0,
	}, nil
}

// Close releases all resources associated with the client.
func (c *Client) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.transport != nil {
		c.transport.close()
	}
}

// IsConnected returns true if the client is connected.
func (c *Client) IsConnected() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transport != nil && c.transport.isConnected()
}

// SetDisconnected marks the client as disconnected.
// This is called when a read/write error indicates the connection is lost.
func (c *Client) SetDisconnected() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.transport != nil {
		c.transport.mu.Lock()
		c.transport.connected = false
		c.transport.mu.Unlock()
	}
}

// Reconnect attempts to re-establish the connection.
// Returns nil if already connected, otherwise attempts reconnection.
func (c *Client) Reconnect() error {
	if c == nil {
		return fmt.Errorf("nil client")
	}

	c.mu.Lock()
	if c.transport != nil && c.transport.isConnected() {
		c.mu.Unlock()
		return nil
	}

	// Close existing transport if any
	if c.transport != nil {
		c.transport.close()
	}

	address := c.address
	rack := c.rack
	slot := c.slot
	c.mu.Unlock()

	// Create new transport
	t := newTransport()
	t.timeout = 10 * time.Second

	if err := t.connect(address, rack, slot); err != nil {
		return fmt.Errorf("reconnect failed: %w", err)
	}

	c.mu.Lock()
	c.transport = t
	c.mu.Unlock()

	return nil
}

// ConnectionMode returns a human-readable string describing the connection mode.
func (c *Client) ConnectionMode() string {
	if c == nil {
		return "Not connected"
	}
	c.mu.Lock()
	connected := c.transport != nil && c.transport.isConnected()
	rack := c.rack
	slot := c.slot
	c.mu.Unlock()
	if connected {
		return fmt.Sprintf("S7 Connected (Rack %d, Slot %d)", rack, slot)
	}
	return "Disconnected"
}

// nextPDURef returns the next PDU reference number.
func (c *Client) nextPDURef() uint16 {
	c.pduRef++
	if c.pduRef == 0 {
		c.pduRef = 1
	}
	return c.pduRef
}

// TagRequest represents a tag to read with optional type hint.
type TagRequest struct {
	Address  string // S7 address (e.g., "DB1.0" or "DB1.DBD0")
	TypeHint string // Optional type name (e.g., "DINT") - used when address doesn't specify type
}

// Read reads one or more addresses by their S7 address strings.
// Each address in the result includes its own error status (nil if successful).
func (c *Client) Read(addresses ...string) ([]*TagValue, error) {
	// Convert to TagRequests with no type hints
	requests := make([]TagRequest, len(addresses))
	for i, addr := range addresses {
		requests[i] = TagRequest{Address: addr}
	}
	return c.ReadWithTypes(requests)
}

// parsedRequest holds a parsed tag request for batching
type parsedRequest struct {
	index    int      // Original index in requests slice
	request  TagRequest
	addr     *Address
	readAddr *Address // Address with totalSize calculated
	err      error    // Parse error if any
}

// ReadWithTypes reads addresses with optional type hints.
// Type hints are used for simple addresses (DB1.0) that don't specify the data type.
// This implementation batches multiple small reads into single requests for efficiency.
func (c *Client) ReadWithTypes(requests []TagRequest) ([]*TagValue, error) {
	if c == nil || c.transport == nil {
		return nil, fmt.Errorf("Read: nil client")
	}
	if len(requests) == 0 {
		return nil, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Parse all requests first
	parsed := make([]parsedRequest, len(requests))
	for i, req := range requests {
		parsed[i].index = i
		parsed[i].request = req

		addr, err := ParseAddress(req.Address)
		if err != nil {
			logging.DebugLog("S7", "ParseAddress failed for %q: %v", req.Address, err)
			parsed[i].err = err
			continue
		}

		// If address didn't specify type/size, use the type hint
		if addr.Size == 0 && req.TypeHint != "" {
			typeCode, ok := TypeCodeFromName(req.TypeHint)
			if ok {
				addr.DataType = typeCode
				addr.Size = TypeSize(typeCode)
				// Handle variable-length types
				if addr.Size == 0 {
					switch BaseType(typeCode) {
					case TypeString:
						addr.Size = 256
					case TypeWString:
						addr.Size = 512
					}
				}
			}
		}

		// Default to DINT if still no size
		if addr.Size == 0 {
			addr.DataType = TypeDInt
			addr.Size = 4
		}

		// Default Count to 1 if not set
		if addr.Count < 1 {
			addr.Count = 1
		}

		parsed[i].addr = addr

		// For arrays, read Count * element size bytes
		totalSize := addr.Size * addr.Count
		parsed[i].readAddr = &Address{
			Area:     addr.Area,
			DBNumber: addr.DBNumber,
			Offset:   addr.Offset,
			BitNum:   addr.BitNum,
			DataType: addr.DataType,
			Size:     totalSize,
			Count:    addr.Count,
		}
	}

	// Calculate PDU limits for batching
	pduSize := int(c.transport.getPDUSize())
	if pduSize < 50 {
		pduSize = 240 // Fallback to minimum S7 PDU if not set
	}

	// Request constraints:
	//   Header: 10 bytes
	//   Params: 2 bytes (function + count) + 12 bytes per item
	//   Max request size = pduSize
	// Response constraints:
	//   Header: 12 bytes
	//   Params: 2 bytes
	//   Data: 4 bytes header per item + actual data
	//   Max response size = pduSize
	//
	// For request: maxItems = (pduSize - 12) / 12
	// For response: need to track cumulative data size
	maxRequestItems := (pduSize - 12) / 12
	if maxRequestItems > 19 {
		maxRequestItems = 19 // S7 protocol limit is often 20, use 19 to be safe
	}
	if maxRequestItems < 1 {
		maxRequestItems = 1
	}
	maxResponsePayload := pduSize - 18 // Leave room for response headers

	// Prepare results slice (indexed by original position)
	results := make([]*TagValue, len(requests))

	// Group requests into batches
	var currentBatch []parsedRequest
	var currentResponseSize int

	flushBatch := func() {
		if len(currentBatch) == 0 {
			return
		}

		if len(currentBatch) == 1 {
			// Single item - use existing single-read path
			p := currentBatch[0]
			logging.DebugLog("S7", "Read %q: area=%s db=%d offset=%d type=%s size=%d",
				p.request.Address, p.addr.Area, p.addr.DBNumber, p.addr.Offset,
				TypeName(p.addr.DataType), p.readAddr.Size)

			data, err := c.readAddress(p.readAddr)
			if err != nil {
				logging.DebugLog("S7", "Read %q failed: %v", p.request.Address, err)
				results[p.index] = &TagValue{
					Name:  p.request.Address,
					Error: err,
				}
			} else {
				logging.DebugLog("S7", "Read %q success: got %d bytes", p.request.Address, len(data))
				results[p.index] = &TagValue{
					Name:     p.request.Address,
					DataType: p.addr.DataType,
					Bytes:    data,
					BitNum:   p.addr.BitNum,
					Count:    p.addr.Count,
					Error:    nil,
				}
			}
		} else {
			// Multi-item batch read
			c.readBatch(currentBatch, results)
		}

		currentBatch = nil
		currentResponseSize = 0
	}

	for i := range parsed {
		p := &parsed[i]

		// Handle parse errors
		if p.err != nil {
			results[p.index] = &TagValue{
				Name:  p.request.Address,
				Error: p.err,
			}
			continue
		}

		// Validate parsed address
		if p.readAddr == nil || p.addr == nil {
			results[p.index] = &TagValue{
				Name:  p.request.Address,
				Error: fmt.Errorf("internal error: nil address after parsing"),
			}
			continue
		}

		// Validate size is positive
		if p.readAddr.Size <= 0 {
			results[p.index] = &TagValue{
				Name:  p.request.Address,
				Error: fmt.Errorf("invalid read size: %d", p.readAddr.Size),
			}
			continue
		}

		// Check if this item needs chunked reading (too large for single response)
		itemResponseSize := 4 + p.readAddr.Size // 4 byte header + data
		if itemResponseSize > maxResponsePayload {
			// Flush current batch first
			flushBatch()

			// Read this large item individually with chunking
			logging.DebugLog("S7", "Large read %q: %d bytes (chunked)",
				p.request.Address, p.readAddr.Size)

			data, err := c.readAddress(p.readAddr)
			if err != nil {
				logging.DebugLog("S7", "Read %q failed: %v", p.request.Address, err)
				results[p.index] = &TagValue{
					Name:  p.request.Address,
					Error: err,
				}
			} else {
				logging.DebugLog("S7", "Read %q success: got %d bytes", p.request.Address, len(data))
				results[p.index] = &TagValue{
					Name:     p.request.Address,
					DataType: p.addr.DataType,
					Bytes:    data,
					BitNum:   p.addr.BitNum,
					Count:    p.addr.Count,
					Error:    nil,
				}
			}
			continue
		}

		// Check if adding this item would exceed batch limits
		// Must check both request item count AND response payload size
		newResponseSize := currentResponseSize + itemResponseSize
		if newResponseSize > maxResponsePayload || len(currentBatch) >= maxRequestItems {
			flushBatch()
		}

		// Add to current batch
		currentBatch = append(currentBatch, *p)
		currentResponseSize += itemResponseSize
	}

	// Flush remaining batch
	flushBatch()

	return results, nil
}

// readBatch reads multiple addresses in a single S7 request.
func (c *Client) readBatch(batch []parsedRequest, results []*TagValue) {
	if len(batch) == 0 {
		return
	}

	// Build list of addresses for the batch
	addrs := make([]*Address, len(batch))
	for i, p := range batch {
		if p.readAddr == nil {
			// Shouldn't happen, but protect against it
			logging.DebugLog("S7", "Batch item %d has nil address", i)
			continue
		}
		addrs[i] = p.readAddr
	}

	// Log the batch
	names := make([]string, len(batch))
	for i, p := range batch {
		names[i] = p.request.Address
	}
	logging.DebugLog("S7", "Batch read %d items: %v", len(batch), names)

	// Build and send request
	request := buildReadRequest(addrs, c.nextPDURef())
	response, err := c.transport.sendReceive(request)
	if err != nil {
		// All items in batch fail with same error
		logging.DebugLog("S7", "Batch read failed: %v", err)
		for _, p := range batch {
			if p.index >= 0 && p.index < len(results) {
				results[p.index] = &TagValue{
					Name:  p.request.Address,
					Error: err,
				}
			}
		}
		return
	}

	// Parse response
	data, errors := parseReadResponse(response, len(batch))

	// Map results back to original positions
	for i, p := range batch {
		// Bounds check for safety
		if p.index < 0 || p.index >= len(results) {
			logging.DebugLog("S7", "Batch item %d has invalid index %d (results len=%d)", i, p.index, len(results))
			continue
		}

		if i >= len(errors) || i >= len(data) {
			logging.DebugLog("S7", "Batch item %d: response arrays too short (errors=%d, data=%d)", i, len(errors), len(data))
			results[p.index] = &TagValue{
				Name:  p.request.Address,
				Error: fmt.Errorf("internal error: response parsing mismatch"),
			}
			continue
		}

		if errors[i] != nil {
			logging.DebugLog("S7", "Batch item %q failed: %v", p.request.Address, errors[i])
			results[p.index] = &TagValue{
				Name:  p.request.Address,
				Error: errors[i],
			}
		} else {
			dataBytes := data[i]
			if dataBytes == nil {
				dataBytes = []byte{} // Ensure non-nil for successful reads with no data
			}
			logging.DebugLog("S7", "Batch item %q success: got %d bytes", p.request.Address, len(dataBytes))
			results[p.index] = &TagValue{
				Name:     p.request.Address,
				DataType: p.addr.DataType,
				Bytes:    dataBytes,
				BitNum:   p.addr.BitNum,
				Count:    p.addr.Count,
				Error:    nil,
			}
		}
	}

	logging.DebugLog("S7", "Batch read complete: %d items", len(batch))
}

// readAddress reads data from a specific S7 address.
// For large reads that exceed PDU size, the read is split into multiple chunks.
func (c *Client) readAddress(addr *Address) ([]byte, error) {
	// Calculate max payload per read based on PDU size
	// PDU overhead: ~18-20 bytes (header + params + data item header)
	pduSize := int(c.transport.getPDUSize())
	maxPayload := pduSize - 20
	if maxPayload < 20 {
		maxPayload = 200 // Fallback minimum
	}

	totalSize := addr.Size
	if totalSize <= maxPayload {
		// Single read is sufficient
		return c.readAddressSingle(addr)
	}

	// Need to split into multiple reads
	logging.DebugLog("S7", "Large read %d bytes exceeds PDU payload %d, splitting into chunks",
		totalSize, maxPayload)

	result := make([]byte, 0, totalSize)
	offset := addr.Offset
	remaining := totalSize

	// Safety limit to prevent infinite loops
	maxChunks := (totalSize / maxPayload) + 10
	chunkCount := 0

	for remaining > 0 {
		chunkCount++
		if chunkCount > maxChunks {
			return nil, fmt.Errorf("chunk read exceeded maximum iterations (%d)", maxChunks)
		}

		chunkSize := remaining
		if chunkSize > maxPayload {
			chunkSize = maxPayload
		}

		chunkAddr := &Address{
			Area:     addr.Area,
			DBNumber: addr.DBNumber,
			Offset:   offset,
			BitNum:   -1, // Byte-level access for chunks
			DataType: TypeByte,
			Size:     chunkSize,
			Count:    chunkSize, // For BYTE transport, count = bytes
		}

		logging.DebugLog("S7", "Reading chunk %d: offset=%d size=%d remaining=%d",
			chunkCount, offset, chunkSize, remaining)

		chunk, err := c.readAddressSingle(chunkAddr)
		if err != nil {
			return nil, fmt.Errorf("chunk read at offset %d failed: %w", offset, err)
		}

		// Validate chunk size
		if len(chunk) != chunkSize {
			logging.DebugLog("S7", "Chunk size mismatch: expected %d, got %d", chunkSize, len(chunk))
			// Accept what we got, but adjust remaining accordingly
			if len(chunk) == 0 {
				return nil, fmt.Errorf("chunk read at offset %d returned empty data", offset)
			}
		}

		result = append(result, chunk...)
		bytesRead := len(chunk)
		offset += bytesRead
		remaining -= bytesRead
	}

	logging.DebugLog("S7", "Large read complete: got %d bytes total", len(result))
	return result, nil
}

// readAddressSingle reads data from a specific S7 address in a single request.
func (c *Client) readAddressSingle(addr *Address) ([]byte, error) {
	// Build read request
	request := buildReadRequest([]*Address{addr}, c.nextPDURef())

	// Send and receive
	response, err := c.transport.sendReceive(request)
	if err != nil {
		return nil, err
	}

	// Parse response
	results, errors := parseReadResponse(response, 1)
	if errors[0] != nil {
		return nil, errors[0]
	}

	return results[0], nil
}

// Write writes a value to an S7 address.
// The value type is inferred and converted appropriately.
func (c *Client) Write(address string, value interface{}) error {
	return c.WriteWithType(address, value, "")
}

// WriteWithType writes a value to an S7 address with an explicit type hint.
// The typeHint should be a type name like "DINT", "REAL", "BOOL", etc.
func (c *Client) WriteWithType(address string, value interface{}, typeHint string) error {
	if c == nil || c.transport == nil {
		return fmt.Errorf("Write: nil client")
	}

	logging.DebugLog("S7", "Write: address=%q value=%v (type %T) typeHint=%q", address, value, value, typeHint)

	addr, err := ParseAddress(address)
	if err != nil {
		logging.DebugLog("S7", "Write: ParseAddress failed: %v", err)
		return fmt.Errorf("Write: %w", err)
	}

	logging.DebugLog("S7", "Write: parsed addr area=%s db=%d offset=%d dataType=%s size=%d",
		addr.Area, addr.DBNumber, addr.Offset, TypeName(addr.DataType), addr.Size)

	// If address didn't specify a type (simple format like DB1.0),
	// use the type hint from config
	if addr.DataType == 0 && typeHint != "" {
		if typeCode, ok := TypeCodeFromName(typeHint); ok {
			addr.DataType = typeCode
			addr.Size = TypeSize(typeCode)
			logging.DebugLog("S7", "Write: using type hint: %s (0x%04X) size=%d", typeHint, typeCode, addr.Size)
		}
	}

	// If still no type, infer from the Go value
	if addr.DataType == 0 {
		addr.DataType = inferTypeFromValue(value)
		addr.Size = TypeSize(addr.DataType)
		logging.DebugLog("S7", "Write: inferred type from value: %s size=%d", TypeName(addr.DataType), addr.Size)
	}

	// For BOOL type without bit number specified, default to bit 0
	if addr.DataType == TypeBool && addr.BitNum < 0 {
		addr.BitNum = 0
		logging.DebugLog("S7", "Write: BOOL type without bit number, defaulting to bit 0")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := c.encodeValue(addr, value)
	if err != nil {
		logging.DebugLog("S7", "Write: encodeValue failed: %v", err)
		return err
	}

	logging.DebugLog("S7", "Write: encoded %d bytes: %x", len(data), data)

	err = c.writeAddress(addr, data)
	if err != nil {
		logging.DebugLog("S7", "Write: writeAddress failed: %v", err)
		return err
	}

	logging.DebugLog("S7", "Write: success")
	return nil
}

// writeAddress writes data to a specific S7 address.
// For large writes that exceed PDU size, the write is split into multiple chunks.
func (c *Client) writeAddress(addr *Address, data []byte) error {
	// Calculate max payload per write based on PDU size
	// PDU overhead: ~30 bytes (TPKT + COTP + S7 header + params + data header)
	pduSize := int(c.transport.getPDUSize())
	maxPayload := pduSize - 35
	if maxPayload < 20 {
		maxPayload = 180 // Fallback minimum
	}

	totalSize := len(data)
	if totalSize <= maxPayload {
		// Single write is sufficient
		return c.writeAddressSingle(addr, data)
	}

	// Need to split into multiple writes
	logging.DebugLog("S7", "Large write %d bytes exceeds PDU payload %d, splitting into chunks",
		totalSize, maxPayload)

	offset := addr.Offset
	remaining := totalSize
	dataPos := 0

	// Safety limit to prevent infinite loops
	maxChunks := (totalSize / maxPayload) + 10
	chunkCount := 0

	for remaining > 0 {
		chunkCount++
		if chunkCount > maxChunks {
			return fmt.Errorf("chunk write exceeded maximum iterations (%d)", maxChunks)
		}

		chunkSize := remaining
		if chunkSize > maxPayload {
			chunkSize = maxPayload
		}

		chunkAddr := &Address{
			Area:     addr.Area,
			DBNumber: addr.DBNumber,
			Offset:   offset,
			BitNum:   -1, // Byte-level access for chunks
			DataType: TypeByte,
			Size:     chunkSize,
		}

		chunkData := data[dataPos : dataPos+chunkSize]

		logging.DebugLog("S7", "Writing chunk %d: offset=%d size=%d remaining=%d",
			chunkCount, offset, chunkSize, remaining)

		err := c.writeAddressSingle(chunkAddr, chunkData)
		if err != nil {
			return fmt.Errorf("chunk write at offset %d failed: %w", offset, err)
		}

		offset += chunkSize
		dataPos += chunkSize
		remaining -= chunkSize
	}

	logging.DebugLog("S7", "Large write complete: wrote %d bytes in %d chunks", totalSize, chunkCount)
	return nil
}

// writeAddressSingle writes data to a specific S7 address in a single request.
func (c *Client) writeAddressSingle(addr *Address, data []byte) error {
	// Build write request
	request := buildWriteRequest(addr, data, c.nextPDURef())

	// Send and receive
	response, err := c.transport.sendReceive(request)
	if err != nil {
		return err
	}

	// Parse response
	return parseWriteResponse(response)
}

// encodeValue converts a Go value to bytes for the given address type.
func (c *Client) encodeValue(addr *Address, value interface{}) ([]byte, error) {
	// For bit writes, encode directly as a bit
	if addr.BitNum >= 0 {
		return c.encodeBitValue(addr, value)
	}

	// Check if value is a slice (array write)
	if isSliceValue(value) {
		return c.encodeArrayValue(addr, value)
	}

	baseType := BaseType(addr.DataType)
	switch baseType {
	case TypeBool:
		return encodeBool(value)
	case TypeByte, TypeSInt:
		return encodeByte(value)
	case TypeWord:
		return encodeWord(value)
	case TypeInt:
		return encodeInt(value)
	case TypeDWord:
		return encodeDWord(value)
	case TypeDInt:
		return encodeDInt(value)
	case TypeReal:
		return encodeReal(value)
	case TypeLReal:
		return encodeLReal(value)
	case TypeLInt:
		return encodeLInt(value)
	case TypeULInt:
		return encodeULInt(value)
	case TypeString:
		return c.encodeStringWithRead(addr, value)
	case TypeWString:
		return c.encodeWStringWithRead(addr, value)
	default:
		return nil, fmt.Errorf("unsupported data type: %s", TypeName(addr.DataType))
	}
}

// isSliceValue returns true if the value is a slice type.
func isSliceValue(value interface{}) bool {
	switch value.(type) {
	case []int, []int8, []int16, []int32, []int64,
		[]uint, []uint8, []uint16, []uint32, []uint64,
		[]float32, []float64, []bool, []string:
		return true
	default:
		return false
	}
}

// encodeArrayValue encodes a slice value as an S7 array.
func (c *Client) encodeArrayValue(addr *Address, value interface{}) ([]byte, error) {
	baseType := BaseType(addr.DataType)
	var result []byte

	switch v := value.(type) {
	case []int32:
		for _, elem := range v {
			var encoded []byte
			var err error
			switch baseType {
			case TypeByte, TypeSInt:
				encoded, err = encodeByte(elem)
			case TypeWord:
				encoded, err = encodeWord(elem)
			case TypeInt:
				encoded, err = encodeInt(elem)
			case TypeDWord:
				encoded, err = encodeDWord(elem)
			case TypeDInt:
				encoded, err = encodeDInt(elem)
			default:
				return nil, fmt.Errorf("cannot encode []int32 as %s", TypeName(addr.DataType))
			}
			if err != nil {
				return nil, err
			}
			result = append(result, encoded...)
		}
	case []int64:
		for _, elem := range v {
			var encoded []byte
			var err error
			switch baseType {
			case TypeLInt:
				encoded, err = encodeLInt(elem)
			case TypeULInt:
				encoded, err = encodeULInt(uint64(elem))
			case TypeDInt:
				encoded, err = encodeDInt(int32(elem))
			default:
				return nil, fmt.Errorf("cannot encode []int64 as %s", TypeName(addr.DataType))
			}
			if err != nil {
				return nil, err
			}
			result = append(result, encoded...)
		}
	case []float32:
		for _, elem := range v {
			encoded, err := encodeReal(elem)
			if err != nil {
				return nil, err
			}
			result = append(result, encoded...)
		}
	case []float64:
		for _, elem := range v {
			var encoded []byte
			var err error
			switch baseType {
			case TypeReal:
				encoded, err = encodeReal(float32(elem))
			case TypeLReal:
				encoded, err = encodeLReal(elem)
			default:
				return nil, fmt.Errorf("cannot encode []float64 as %s", TypeName(addr.DataType))
			}
			if err != nil {
				return nil, err
			}
			result = append(result, encoded...)
		}
	case []bool:
		for _, elem := range v {
			encoded, err := encodeBool(elem)
			if err != nil {
				return nil, err
			}
			result = append(result, encoded...)
		}
	case []string:
		if baseType != TypeString && baseType != TypeWString {
			return nil, fmt.Errorf("cannot encode []string as %s", TypeName(addr.DataType))
		}
		// For string arrays, read the first element to get maxLen
		readAddr := &Address{
			Area:     addr.Area,
			DBNumber: addr.DBNumber,
			Offset:   addr.Offset,
			BitNum:   -1,
			DataType: TypeByte,
			Size:     2, // STRING header
		}
		if baseType == TypeWString {
			readAddr.Size = 4 // WSTRING header
		}

		headerData, err := c.readAddress(readAddr)
		if err != nil {
			return nil, fmt.Errorf("failed to read string array header: %w", err)
		}

		var maxLen int
		if baseType == TypeWString {
			if len(headerData) < 4 {
				return nil, fmt.Errorf("WSTRING header too short")
			}
			maxLen = int(binary.BigEndian.Uint16(headerData[0:2]))
		} else {
			if len(headerData) < 2 {
				return nil, fmt.Errorf("STRING header too short")
			}
			maxLen = int(headerData[0])
		}

		logging.DebugLog("S7", "encodeStringArray: %d elements, maxLen=%d per element", len(v), maxLen)

		// Encode each string element
		for i, s := range v {
			var encoded []byte
			if baseType == TypeWString {
				encoded, err = encodeWStringWithMaxLen(s, maxLen)
			} else {
				encoded, err = encodeStringWithMaxLen(s, maxLen)
			}
			if err != nil {
				return nil, fmt.Errorf("failed to encode string element %d: %w", i, err)
			}
			result = append(result, encoded...)
		}
	default:
		return nil, fmt.Errorf("unsupported array type: %T", value)
	}

	return result, nil
}

// encodeStringWithRead encodes a string value for S7 STRING type.
// It first reads the current string to get the max length defined in the DB.
// S7 STRING format: [max_len][actual_len][chars...]
func (c *Client) encodeStringWithRead(addr *Address, value interface{}) ([]byte, error) {
	var s string
	switch v := value.(type) {
	case string:
		s = v
	default:
		return nil, fmt.Errorf("cannot convert %T to string", value)
	}

	// Read current string to get max length from DB
	// STRING header is 2 bytes, read enough to get the structure
	readAddr := &Address{
		Area:     addr.Area,
		DBNumber: addr.DBNumber,
		Offset:   addr.Offset,
		BitNum:   -1,
		DataType: TypeByte,
		Size:     2, // Just read the header to get max length
	}

	currentData, err := c.readAddress(readAddr)
	if err != nil {
		logging.DebugLog("S7", "encodeString: failed to read current string header: %v, using default maxLen=254", err)
		// Fall back to default max length
		return encodeStringWithMaxLen(s, 254)
	}

	if len(currentData) < 2 {
		logging.DebugLog("S7", "encodeString: current data too short (%d bytes), using default maxLen=254", len(currentData))
		return encodeStringWithMaxLen(s, 254)
	}

	maxLen := int(currentData[0])
	logging.DebugLog("S7", "encodeString: read maxLen=%d from DB", maxLen)

	return encodeStringWithMaxLen(s, maxLen)
}

// encodeStringWithMaxLen encodes a string with the specified max length.
func encodeStringWithMaxLen(s string, maxLen int) ([]byte, error) {
	if len(s) > maxLen {
		s = s[:maxLen]
	}

	// S7 STRING format: [maxLen][actualLen][chars padded to maxLen]
	// Must write the full buffer size (maxLen + 2 bytes)
	totalSize := 2 + maxLen
	logging.DebugLog("S7", "encodeString: maxLen=%d, actualLen=%d, totalSize=%d", maxLen, len(s), totalSize)

	result := make([]byte, totalSize)
	result[0] = byte(maxLen)
	result[1] = byte(len(s))
	copy(result[2:], []byte(s))
	// Remaining bytes are zero-padded by make()

	return result, nil
}

// encodeWStringWithRead encodes a string value for S7 WSTRING type.
// It first reads the current string to get the max length defined in the DB.
// S7 WSTRING format: [max_len(2)][actual_len(2)][UTF-16BE chars...]
func (c *Client) encodeWStringWithRead(addr *Address, value interface{}) ([]byte, error) {
	var s string
	switch v := value.(type) {
	case string:
		s = v
	default:
		return nil, fmt.Errorf("cannot convert %T to wstring", value)
	}

	// Read current wstring to get max length from DB
	// WSTRING header is 4 bytes
	readAddr := &Address{
		Area:     addr.Area,
		DBNumber: addr.DBNumber,
		Offset:   addr.Offset,
		BitNum:   -1,
		DataType: TypeByte,
		Size:     4, // Just read the header to get max length
	}

	currentData, err := c.readAddress(readAddr)
	if err != nil {
		logging.DebugLog("S7", "encodeWString: failed to read current wstring header: %v, using default maxLen=254", err)
		return encodeWStringWithMaxLen(s, 254)
	}

	if len(currentData) < 4 {
		logging.DebugLog("S7", "encodeWString: current data too short (%d bytes), using default maxLen=254", len(currentData))
		return encodeWStringWithMaxLen(s, 254)
	}

	maxLen := int(binary.BigEndian.Uint16(currentData[0:2]))
	logging.DebugLog("S7", "encodeWString: read maxLen=%d from DB", maxLen)

	return encodeWStringWithMaxLen(s, maxLen)
}

// encodeWStringWithMaxLen encodes a wide string with the specified max length.
func encodeWStringWithMaxLen(s string, maxLen int) ([]byte, error) {
	runes := []rune(s)
	if len(runes) > maxLen {
		runes = runes[:maxLen]
	}

	// S7 WSTRING format: [maxLen(2)][actualLen(2)][UTF-16BE chars padded to maxLen]
	// Must write the full buffer size (4 + maxLen*2 bytes)
	totalSize := 4 + maxLen*2
	logging.DebugLog("S7", "encodeWString: maxLen=%d, actualLen=%d, totalSize=%d", maxLen, len(runes), totalSize)

	result := make([]byte, totalSize)
	binary.BigEndian.PutUint16(result[0:2], uint16(maxLen))
	binary.BigEndian.PutUint16(result[2:4], uint16(len(runes)))

	// UTF-16BE encoded characters
	for i, r := range runes {
		binary.BigEndian.PutUint16(result[4+i*2:], uint16(r))
	}
	// Remaining bytes are zero-padded by make()

	return result, nil
}

// encodeBitValue encodes a bit value for S7 bit writes.
// S7 bit writes send just the bit value (0 or 1), not a full byte.
func (c *Client) encodeBitValue(addr *Address, value interface{}) ([]byte, error) {
	var boolVal bool
	switch v := value.(type) {
	case bool:
		boolVal = v
	case int:
		boolVal = v != 0
	case int32:
		boolVal = v != 0
	case int64:
		boolVal = v != 0
	default:
		return nil, fmt.Errorf("cannot convert %T to bool", value)
	}

	// S7 bit write: just send the bit value as a single byte
	if boolVal {
		return []byte{0x01}, nil
	}
	return []byte{0x00}, nil
}

func encodeBool(value interface{}) ([]byte, error) {
	var v bool
	switch val := value.(type) {
	case bool:
		v = val
	case int:
		v = val != 0
	case int32:
		v = val != 0
	case int64:
		v = val != 0
	default:
		return nil, fmt.Errorf("cannot convert %T to bool", value)
	}
	if v {
		return []byte{1}, nil
	}
	return []byte{0}, nil
}

func encodeByte(value interface{}) ([]byte, error) {
	switch v := value.(type) {
	case uint8:
		return []byte{v}, nil
	case int8:
		return []byte{byte(v)}, nil
	case int:
		return []byte{byte(v)}, nil
	case int32:
		return []byte{byte(v)}, nil
	case int64:
		return []byte{byte(v)}, nil
	case uint64:
		return []byte{byte(v)}, nil
	case float64:
		return []byte{byte(int64(v))}, nil
	default:
		return nil, fmt.Errorf("cannot convert %T to byte", value)
	}
}

func encodeWord(value interface{}) ([]byte, error) {
	buf := make([]byte, 2)
	switch v := value.(type) {
	case uint16:
		binary.BigEndian.PutUint16(buf, v)
	case int16:
		binary.BigEndian.PutUint16(buf, uint16(v))
	case int:
		binary.BigEndian.PutUint16(buf, uint16(v))
	case int32:
		binary.BigEndian.PutUint16(buf, uint16(v))
	case int64:
		binary.BigEndian.PutUint16(buf, uint16(v))
	case uint64:
		binary.BigEndian.PutUint16(buf, uint16(v))
	case float64:
		binary.BigEndian.PutUint16(buf, uint16(int64(v)))
	default:
		return nil, fmt.Errorf("cannot convert %T to word", value)
	}
	return buf, nil
}

func encodeInt(value interface{}) ([]byte, error) {
	buf := make([]byte, 2)
	switch v := value.(type) {
	case int16:
		binary.BigEndian.PutUint16(buf, uint16(v))
	case int:
		binary.BigEndian.PutUint16(buf, uint16(v))
	case int32:
		binary.BigEndian.PutUint16(buf, uint16(v))
	case int64:
		binary.BigEndian.PutUint16(buf, uint16(v))
	case float64:
		binary.BigEndian.PutUint16(buf, uint16(int16(v)))
	default:
		return nil, fmt.Errorf("cannot convert %T to int", value)
	}
	return buf, nil
}

func encodeDWord(value interface{}) ([]byte, error) {
	buf := make([]byte, 4)
	switch v := value.(type) {
	case uint32:
		binary.BigEndian.PutUint32(buf, v)
	case int32:
		binary.BigEndian.PutUint32(buf, uint32(v))
	case int:
		binary.BigEndian.PutUint32(buf, uint32(v))
	case int64:
		binary.BigEndian.PutUint32(buf, uint32(v))
	case uint64:
		binary.BigEndian.PutUint32(buf, uint32(v))
	case float64:
		binary.BigEndian.PutUint32(buf, uint32(int64(v)))
	default:
		return nil, fmt.Errorf("cannot convert %T to dword", value)
	}
	return buf, nil
}

func encodeDInt(value interface{}) ([]byte, error) {
	buf := make([]byte, 4)
	switch v := value.(type) {
	case int32:
		binary.BigEndian.PutUint32(buf, uint32(v))
	case int:
		binary.BigEndian.PutUint32(buf, uint32(v))
	case int64:
		binary.BigEndian.PutUint32(buf, uint32(v))
	case float64:
		binary.BigEndian.PutUint32(buf, uint32(int32(v)))
	default:
		return nil, fmt.Errorf("cannot convert %T to dint", value)
	}
	return buf, nil
}

func encodeReal(value interface{}) ([]byte, error) {
	buf := make([]byte, 4)
	switch v := value.(type) {
	case float32:
		binary.BigEndian.PutUint32(buf, math.Float32bits(v))
	case float64:
		binary.BigEndian.PutUint32(buf, math.Float32bits(float32(v)))
	default:
		return nil, fmt.Errorf("cannot convert %T to real", value)
	}
	return buf, nil
}

func encodeLReal(value interface{}) ([]byte, error) {
	buf := make([]byte, 8)
	switch v := value.(type) {
	case float64:
		binary.BigEndian.PutUint64(buf, math.Float64bits(v))
	case float32:
		binary.BigEndian.PutUint64(buf, math.Float64bits(float64(v)))
	default:
		return nil, fmt.Errorf("cannot convert %T to lreal", value)
	}
	return buf, nil
}

func encodeLInt(value interface{}) ([]byte, error) {
	buf := make([]byte, 8)
	switch v := value.(type) {
	case int64:
		binary.BigEndian.PutUint64(buf, uint64(v))
	case int:
		binary.BigEndian.PutUint64(buf, uint64(v))
	case float64:
		binary.BigEndian.PutUint64(buf, uint64(int64(v)))
	default:
		return nil, fmt.Errorf("cannot convert %T to lint", value)
	}
	return buf, nil
}

func encodeULInt(value interface{}) ([]byte, error) {
	buf := make([]byte, 8)
	switch v := value.(type) {
	case uint64:
		binary.BigEndian.PutUint64(buf, v)
	case int64:
		binary.BigEndian.PutUint64(buf, uint64(v))
	case int:
		binary.BigEndian.PutUint64(buf, uint64(v))
	case float64:
		binary.BigEndian.PutUint64(buf, uint64(v))
	default:
		return nil, fmt.Errorf("cannot convert %T to ulint", value)
	}
	return buf, nil
}

// inferTypeFromValue infers the S7 data type from a Go value.
func inferTypeFromValue(value interface{}) uint16 {
	switch value.(type) {
	case bool:
		return TypeBool
	case int8, uint8:
		return TypeByte
	case int16:
		return TypeInt
	case uint16:
		return TypeWord
	case int32, int:
		return TypeDInt
	case uint32:
		return TypeDWord
	case int64:
		return TypeLInt
	case uint64:
		return TypeULInt
	case float32:
		return TypeReal
	case float64:
		return TypeLReal
	default:
		// Default to DINT for unknown types
		return TypeDInt
	}
}

// GetCPUInfo returns information about the connected CPU.
// Note: CPU info retrieval is not yet implemented in native protocol.
func (c *Client) GetCPUInfo() (*CPUInfo, error) {
	if c == nil || c.transport == nil {
		return nil, fmt.Errorf("GetCPUInfo: nil client")
	}

	// CPU info retrieval requires UserData PDU type which is more complex
	// Return a placeholder for now
	return &CPUInfo{
		ModuleTypeName: "S7 PLC",
		SerialNumber:   "",
		ASName:         "",
		Copyright:      "",
		ModuleName:     "",
	}, nil
}

// CPUInfo contains information about the S7 CPU.
type CPUInfo struct {
	ModuleTypeName string
	SerialNumber   string
	ASName         string
	Copyright      string
	ModuleName     string
}
