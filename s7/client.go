package s7

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/yatesdr/plcio/logging"
)

// ErrConnectionLost indicates the link to the PLC dropped during a read. The
// read paths fold per-address failures into each TagValue.Error and otherwise
// return a nil top-level error, so callers cannot tell a lost link from a
// single bad address. When the transport has dropped, the read methods surface
// this error at the top level — alongside any partial results — so callers can
// reconnect. Detect it with errors.Is(err, ErrConnectionLost).
var ErrConnectionLost = errors.New("s7: connection lost during read")

// connErrorIfDownLocked returns a wrapped ErrConnectionLost when the underlying
// transport has dropped, otherwise nil. It must be called with c.mu held (the
// read paths hold it); it queries the transport's own state without re-locking
// c.mu, so it cannot be replaced by IsConnected() (which locks c.mu).
func (c *Client) connErrorIfDownLocked() error {
	if c.transport == nil || !c.transport.isConnected() {
		return fmt.Errorf("read incomplete: %w", ErrConnectionLost)
	}
	return nil
}

// Client is a high-level wrapper for S7 PLC communication.
type Client struct {
	transport *transport
	address   string
	rack      int
	slot      int
	pduRef    uint16
	timeout   time.Duration // configured connect/IO timeout, reused by Reconnect
	epoch     uint64        // bumped by Close so an in-flight Reconnect is discarded
	mu        sync.Mutex
	// reconnectMu serializes Reconnect so concurrent callers cannot each dial
	// a transport and leak all but the last one.
	reconnectMu sync.Mutex
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
	if err := validateRackSlot(cfg.rack, cfg.slot); err != nil {
		return nil, fmt.Errorf("Connect: %w", err)
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
		timeout:   cfg.timeout,
	}, nil
}

// Close releases all resources associated with the client.
func (c *Client) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch++
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

	c.reconnectMu.Lock()
	defer c.reconnectMu.Unlock()

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
	epoch := c.epoch
	timeout := c.timeout
	c.mu.Unlock()

	// Create new transport
	t := newTransport()
	if timeout > 0 {
		t.timeout = timeout
	}

	if err := t.connect(address, rack, slot); err != nil {
		return fmt.Errorf("reconnect failed: %w", err)
	}

	c.mu.Lock()
	if c.epoch != epoch {
		// Close was called while we were dialing; don't resurrect the client.
		c.mu.Unlock()
		t.close()
		return fmt.Errorf("reconnect failed: client closed during reconnect")
	}
	c.transport = t
	c.mu.Unlock()

	return nil
}

// Keepalive performs one cheap request/response round trip so a dead link is
// detected (and the idle connection kept open) between polls. It reads SZL
// 0x0424 index 0 (CPU mode/status, a single 20-byte record). Any well-formed
// answer counts as alive, including a CPU refusing that SZL; transport
// failures and mismatched responses mark the connection lost and return an
// error matching ErrConnectionLost. Returns an error when not connected.
func (c *Client) Keepalive() error {
	if c == nil {
		return fmt.Errorf("Keepalive: nil client")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.transport == nil || !c.transport.isConnected() {
		return fmt.Errorf("Keepalive: not connected: %w", ErrConnectionLost)
	}
	response, err := c.transport.sendReceive(buildSZLRequest(0x0424, 0x0000, c.nextPDURef()))
	if err != nil {
		if !c.transport.isConnected() && !errors.Is(err, ErrConnectionLost) {
			return fmt.Errorf("Keepalive: %w: %w", err, ErrConnectionLost)
		}
		return fmt.Errorf("Keepalive: %w", err)
	}
	if _, err := parseSZLResponse(response); err != nil {
		// The CPU answered, so the link is alive; only the SZL was refused.
		logging.DebugLog("S7", "Keepalive: SZL 0x0424 not served (%v); link is alive", err)
	}
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
	if c == nil {
		return nil, fmt.Errorf("Read: nil client")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.transport == nil {
		return nil, fmt.Errorf("Read: nil client")
	}
	if len(requests) == 0 {
		return nil, nil
	}

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

		// Offset-only addresses take their type from the hint; sized
		// addresses take it only when the hint has the same width.
		if err := applyTypeHint(addr, req.TypeHint, req.Address); err != nil {
			parsed[i].err = err
			continue
		}
		if addr.Size == 0 && addr.DataType != 0 {
			// Variable-length types: default STRING[254] / WSTRING[254]
			switch BaseType(addr.DataType) {
			case TypeString:
				addr.Size = 256
			case TypeWString:
				addr.Size = 512
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
		if isBoolArray(addr) {
			// S7 packs BOOL arrays 8 per byte: read the covering bytes and
			// unpack them after the read (see parsedRequest.tagValue).
			rd := parsed[i].readAddr
			rd.BitNum = -1
			rd.DataType = TypeByte
			rd.Size = (max(addr.BitNum, 0) + addr.Count + 7) / 8
			rd.Count = rd.Size
		}
		if addr.Offset+parsed[i].readAddr.Size-1 > maxByteOffset {
			parsed[i].err = fmt.Errorf("%s: read of %d bytes extends past max byte offset %d",
				req.Address, parsed[i].readAddr.Size, maxByteOffset)
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
	// Exact wire budget: 12-byte ack header + 2-byte parameter, then every
	// item except the last is followed by a fill byte when its length is odd.
	maxResponseWire := pduSize - 14

	// Prepare results slice (indexed by original position)
	results := make([]*TagValue, len(requests))

	// Group requests into batches
	var currentBatch []parsedRequest
	var currentResponseSize int
	var currentWireSize int // includes the fill byte of every odd item so far

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
				results[p.index] = p.tagValue(data)
			}
		} else {
			// Multi-item batch read
			c.readBatch(currentBatch, results)
		}

		currentBatch = nil
		currentResponseSize = 0
		currentWireSize = 0
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
				results[p.index] = p.tagValue(data)
			}
			continue
		}

		// Check if adding this item would exceed batch limits
		// Must check both request item count AND response payload size
		// The wire check only adds fill bytes on top of the legacy estimate,
		// so batches that already fit are packed exactly as before.
		newResponseSize := currentResponseSize + itemResponseSize
		if newResponseSize > maxResponsePayload || currentWireSize+itemResponseSize > maxResponseWire ||
			len(currentBatch) >= maxRequestItems {
			flushBatch()
		}

		// Add to current batch
		currentBatch = append(currentBatch, *p)
		currentResponseSize += itemResponseSize
		currentWireSize += itemResponseSize + p.readAddr.Size%2
	}

	// Flush remaining batch
	flushBatch()

	return results, c.connErrorIfDownLocked()
}

// applyTypeHint resolves addr's data type from a type hint (explicit or
// configured). An offset-only address (DB1.4) has no size and takes the hint's
// type. A sized address (DB1.DBD4, MW2, DB1.DBX0.0, T5, ...) takes the hint's
// type only when it has exactly the address width, e.g. REAL/DINT/TIME on a
// D address; any other hint is an error rather than being silently ignored.
func applyTypeHint(addr *Address, hint, address string) error {
	if hint == "" {
		return nil
	}
	code, ok := TypeCodeFromName(hint)
	if !ok {
		return fmt.Errorf("unknown S7 data type %q for %s", hint, address)
	}
	if addr.DataType == 0 {
		addr.DataType = code
		addr.Size = TypeSize(code)
		return nil
	}
	base := BaseType(code)
	if addr.BitNum >= 0 {
		if base != TypeBool {
			return fmt.Errorf("type %s does not fit bit address %s (only BOOL)", TypeName(code), address)
		}
		return nil
	}
	if size := TypeSize(base); base == TypeBool || size != addr.Size {
		return fmt.Errorf("type %s (%d bytes) does not match the %d-byte address %s; use an offset-only address (e.g. DB1.4) or a type of that width",
			TypeName(code), size, addr.Size, address)
	}
	addr.DataType = base
	return nil
}

// isBoolArray reports whether addr is a BOOL array (bits packed 8 per byte).
func isBoolArray(addr *Address) bool {
	return BaseType(addr.DataType) == TypeBool && addr.Count > 1
}

// tagValue builds the successful TagValue for p from the bytes read. BOOL
// arrays are unpacked from the PLC's 8-per-byte layout (starting at the
// address's bit offset) into one 0x00/0x01 byte per element.
func (p *parsedRequest) tagValue(data []byte) *TagValue {
	if isBoolArray(p.addr) {
		data = unpackBools(data, max(p.addr.BitNum, 0), p.addr.Count)
	}
	if err := decodeCheck(p.addr.DataType, data); err != nil {
		return &TagValue{Name: p.request.Address, DataType: p.addr.DataType, Bytes: data,
			BitNum: p.addr.BitNum, Count: p.addr.Count, Error: err}
	}
	return &TagValue{
		Name:     p.request.Address,
		DataType: p.addr.DataType,
		Bytes:    data,
		BitNum:   p.addr.BitNum,
		Count:    p.addr.Count,
		Error:    nil,
	}
}

// unpackBools expands count packed bits (LSB first, starting at bit) into one
// byte per element.
func unpackBools(packed []byte, bit, count int) []byte {
	if avail := len(packed)*8 - bit; count > avail {
		count = max(avail, 0)
	}
	out := make([]byte, count)
	for i := range out {
		n := bit + i
		out[i] = (packed[n/8] >> (n % 8)) & 1
	}
	return out
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
			results[p.index] = p.tagValue(dataBytes)
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
	if c == nil {
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

	if addr.DataType == 0 && typeHint == "" {
		// Simple format like DB1.0: the address carries no size, so the type
		// must come from the hint. Guessing from the Go value (int64/float64
		// -> 8 bytes) would clobber neighbouring variables.
		return fmt.Errorf("Write: address %q does not specify a data size; a data type is required "+
			"(configure the tag's DataType, e.g. DINT/REAL/INT, or use WriteWithType)", address)
	}
	if err := applyTypeHint(addr, typeHint, address); err != nil {
		return fmt.Errorf("Write: %w", err)
	}
	logging.DebugLog("S7", "Write: resolved type %s size=%d (hint %q)", TypeName(addr.DataType), addr.Size, typeHint)
	if base := BaseType(addr.DataType); addr.BitNum < 0 && isFloatValue(value) && !isIntegralFloat(value) &&
		base != TypeReal && base != TypeLReal {
		// Integer-typed target (DBD/DBW/MD/... or an integer type hint) with a
		// fractional float: the integer encoder would silently truncate.
		// Integral floats (e.g. JSON numbers) keep their integer encoding.
		return fmt.Errorf("Write: non-integral value %v for %s target %q; configure a REAL/LREAL data type",
			value, TypeName(addr.DataType), address)
	}

	if BaseType(addr.DataType) == TypeBool && isSliceValue(value) {
		return fmt.Errorf("Write: BOOL array writes are not supported for %s (S7 packs BOOLs 8 per byte); "+
			"write each element by bit address instead", address)
	}

	// For BOOL type without bit number specified, default to bit 0
	if addr.DataType == TypeBool && addr.BitNum < 0 {
		addr.BitNum = 0
		logging.DebugLog("S7", "Write: BOOL type without bit number, defaulting to bit 0")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.transport == nil {
		return fmt.Errorf("Write: nil client")
	}

	data, err := c.encodeValue(addr, value)
	if err != nil {
		logging.DebugLog("S7", "Write: encodeValue failed: %v", err)
		return err
	}

	logging.DebugLog("S7", "Write: encoded %d bytes: %x", len(data), data)

	if err := checkWriteSize(addr, value, data); err != nil {
		return fmt.Errorf("Write %s: %w", address, err)
	}

	err = c.writeAddress(addr, data)
	if err != nil {
		logging.DebugLog("S7", "Write: writeAddress failed: %v", err)
		return err
	}

	logging.DebugLog("S7", "Write: success")
	return nil
}

// isFloatValue reports whether value is a Go floating-point scalar or slice.
func isFloatValue(value interface{}) bool {
	switch value.(type) {
	case float32, float64, []float32, []float64:
		return true
	}
	return false
}

// isIntegralFloat reports whether every float in value is a finite whole number.
func isIntegralFloat(value interface{}) bool {
	whole := func(f float64) bool { return !math.IsInf(f, 0) && f == math.Trunc(f) }
	switch v := value.(type) {
	case float32:
		return whole(float64(v))
	case float64:
		return whole(v)
	case []float32:
		for _, f := range v {
			if !whole(float64(f)) {
				return false
			}
		}
		return true
	case []float64:
		for _, f := range v {
			if !whole(f) {
				return false
			}
		}
		return true
	}
	return false
}

// checkWriteSize rejects writes whose encoded length exceeds the target's
// declared size (element size x count), so an oversized value cannot be
// chunked into neighbouring data.
func checkWriteSize(addr *Address, value interface{}, data []byte) error {
	count := max(addr.Count, 1)
	switch base := BaseType(addr.DataType); {
	case addr.BitNum >= 0:
		// Single bit write
	case base == TypeString || base == TypeWString:
		// Each element is encoded at exactly the size its DB header declares
		if v, ok := value.([]string); ok && len(v) > count {
			return fmt.Errorf("%d strings exceed the %d-element target", len(v), count)
		}
	default:
		if limit := TypeSize(addr.DataType) * count; len(data) > limit {
			return fmt.Errorf("encoded value is %d bytes but the %s target holds %d", len(data), TypeName(addr.DataType), limit)
		}
	}
	if addr.Offset+len(data)-1 > maxByteOffset {
		return fmt.Errorf("write of %d bytes extends past max byte offset %d", len(data), maxByteOffset)
	}
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
			// Multi-PDU writes are not atomic: earlier chunks stay written.
			if dataPos == 0 {
				return fmt.Errorf("chunk write at offset %d failed; 0 of %d bytes were written: %w", offset, totalSize, err)
			}
			return fmt.Errorf("chunk write at offset %d failed after %d of %d bytes were already written "+
				"(multi-PDU writes are not atomic; bytes %d..%d keep their new values): %w",
				offset, dataPos, totalSize, addr.Offset, addr.Offset+dataPos-1, err)
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
		if enc := timeEncoder(baseType); enc != nil {
			return enc(value)
		}
		return nil, fmt.Errorf("unsupported data type: %s", TypeName(addr.DataType))
	}
}

// timeEncoder returns the strict encoder for the S7 date/time types.
func timeEncoder(baseType uint16) func(interface{}) ([]byte, error) {
	switch baseType {
	case TypeTime:
		return encodeTime
	case TypeTimeOfDay:
		return encodeTimeOfDay
	case TypeDate:
		return encodeDate
	case TypeS5Time:
		return encodeS5Time
	case TypeDateAndTime:
		return encodeDateAndTime
	case TypeDTL:
		return encodeDTL
	}
	return nil
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
				enc := timeEncoder(baseType)
				if enc == nil || baseType == TypeDateAndTime || baseType == TypeDTL {
					return nil, fmt.Errorf("cannot encode []int32 as %s", TypeName(addr.DataType))
				}
				encoded, err = enc(elem)
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
				if elem < math.MinInt32 || elem > math.MaxInt32 {
					return nil, fmt.Errorf("value %d out of DINT range", elem)
				}
				encoded, err = encodeDInt(int32(elem))
			default:
				enc := timeEncoder(baseType)
				if enc == nil || baseType == TypeDateAndTime || baseType == TypeDTL {
					return nil, fmt.Errorf("cannot encode []int64 as %s", TypeName(addr.DataType))
				}
				encoded, err = enc(elem)
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
		if err := checkStringMaxLen(baseType, maxLen); err != nil {
			return nil, err
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

	// The header decides how many bytes get written, so never guess it.
	currentData, err := c.readAddress(readAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to read STRING header: %w", err)
	}

	if len(currentData) < 2 {
		return nil, fmt.Errorf("STRING header too short (%d bytes)", len(currentData))
	}

	maxLen := int(currentData[0])
	logging.DebugLog("S7", "encodeString: read maxLen=%d from DB", maxLen)
	if err := checkStringMaxLen(TypeString, maxLen); err != nil {
		return nil, err
	}

	return encodeStringWithMaxLen(s, maxLen)
}

// Largest max lengths an S7 STRING / WSTRING header can legitimately declare.
const (
	maxStringLen  = 254
	maxWStringLen = 16382
)

// checkStringMaxLen rejects implausible max lengths read from a DB header
// (e.g. 0xFFFF from a misconfigured offset) before they size a write.
func checkStringMaxLen(baseType uint16, maxLen int) error {
	if baseType == TypeWString {
		if maxLen > maxWStringLen {
			return fmt.Errorf("WSTRING header declares max length %d (> %d); check the address", maxLen, maxWStringLen)
		}
	} else if maxLen > maxStringLen {
		return fmt.Errorf("STRING header declares max length %d (> %d); check the address", maxLen, maxStringLen)
	}
	return nil
}

// encodeStringWithMaxLen encodes a string with the specified max length.
func encodeStringWithMaxLen(s string, maxLen int) ([]byte, error) {
	if len(s) > maxLen {
		return nil, fmt.Errorf("string of %d characters exceeds the STRING's maximum length %d (from its DB header)", len(s), maxLen)
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

	// The header decides how many bytes get written, so never guess it.
	currentData, err := c.readAddress(readAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to read WSTRING header: %w", err)
	}

	if len(currentData) < 4 {
		return nil, fmt.Errorf("WSTRING header too short (%d bytes)", len(currentData))
	}

	maxLen := int(binary.BigEndian.Uint16(currentData[0:2]))
	logging.DebugLog("S7", "encodeWString: read maxLen=%d from DB", maxLen)
	if err := checkStringMaxLen(TypeWString, maxLen); err != nil {
		return nil, err
	}

	return encodeWStringWithMaxLen(s, maxLen)
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

// GetCPUInfo returns information about the connected CPU, read from the
// system status lists (SZL) the way Snap7's GetCpuInfo/GetOrderCode do:
// SZL 0x0011 (module identification: order number, firmware) and SZL 0x001C
// (component identification: names, serial number). CPUs serve different
// subsets (an S7-1200 answers 0x0011 but refuses 0x001C), so each list is
// optional; an error is returned only when neither can be read.
func (c *Client) GetCPUInfo() (*CPUInfo, error) {
	if c == nil {
		return nil, fmt.Errorf("GetCPUInfo: nil client")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.transport == nil || !c.transport.isConnected() {
		return nil, fmt.Errorf("GetCPUInfo: not connected: %w", ErrConnectionLost)
	}
	info := &CPUInfo{}
	var errs []error
	if recs, err := c.readSZLRecordsLocked(0x0011); err != nil {
		errs = append(errs, fmt.Errorf("SZL 0x0011: %w", err))
	} else {
		for _, r := range recs {
			// Record: index(2) MLFB(20) BGTyp(2) Ausbg(2) Ausbe(2).
			if len(r.data) < 26 {
				continue
			}
			switch r.index {
			case 0x0001:
				info.OrderCode = szlText(r.data[0:20])
			case 0x0007:
				if r.data[22] == 'V' {
					info.FirmwareVersion = fmt.Sprintf("V%d.%d.%d", r.data[23], r.data[24], r.data[25])
				}
			}
		}
	}
	if recs, err := c.readSZLRecordsLocked(0x001C); err != nil {
		errs = append(errs, fmt.Errorf("SZL 0x001C: %w", err))
	} else {
		for _, r := range recs {
			text := szlText(r.data)
			switch r.index {
			case 0x0001:
				info.ASName = text
			case 0x0002:
				info.ModuleName = text
			case 0x0004:
				info.Copyright = text
			case 0x0005:
				info.SerialNumber = text
			case 0x0007:
				info.ModuleTypeName = text
			}
		}
	}
	if len(errs) == 2 {
		return nil, fmt.Errorf("GetCPUInfo: %w; %w", errs[0], errs[1])
	}
	if info.ModuleTypeName == "" {
		info.ModuleTypeName = "S7 PLC"
	}
	return info, nil
}

type szlRecord struct {
	index uint16
	data  []byte // record contents after the 2-byte index
}

// readSZLRecordsLocked reads SZL id (index 0) and splits it into records.
// c.mu must be held.
func (c *Client) readSZLRecordsLocked(id uint16) ([]szlRecord, error) {
	resp, err := c.transport.sendReceive(buildSZLRequest(id, 0x0000, c.nextPDURef()))
	if err != nil {
		return nil, err
	}
	d, err := parseSZLResponse(resp)
	if err != nil {
		return nil, err
	}
	return parseSZLRecords(id, d)
}

// parseSZLRecords splits SZL data (SZL-ID(2) index(2) LENTHDR(2) N_DR(2)
// records) into records.
func parseSZLRecords(id uint16, d []byte) ([]szlRecord, error) {
	if len(d) < 8 || binary.BigEndian.Uint16(d[0:2]) != id {
		return nil, fmt.Errorf("SZL 0x%04X: unexpected header", id)
	}
	recLen := int(binary.BigEndian.Uint16(d[4:6]))
	count := int(binary.BigEndian.Uint16(d[6:8]))
	if recLen < 2 {
		return nil, fmt.Errorf("SZL 0x%04X: invalid record length %d", id, recLen)
	}
	var recs []szlRecord
	for i, off := 0, 8; i < count && off+recLen <= len(d); i, off = i+1, off+recLen {
		rec := d[off : off+recLen]
		recs = append(recs, szlRecord{index: binary.BigEndian.Uint16(rec[0:2]), data: rec[2:]})
	}
	return recs, nil
}

// szlText trims the NUL/space padding of an SZL text field.
func szlText(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSpace(string(b))
}

// CPUInfo contains information about the S7 CPU.
type CPUInfo struct {
	ModuleTypeName  string
	SerialNumber    string
	ASName          string
	Copyright       string
	ModuleName      string
	OrderCode       string // e.g. "6ES7 214-1AG40-0XB0" (SZL 0x0011)
	FirmwareVersion string // e.g. "V4.4.1" (SZL 0x0011)
}
