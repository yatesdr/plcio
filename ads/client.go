package ads

import (
	"encoding/binary"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"plcio/logging"
)

// Client provides high-level access to a Beckhoff TwinCAT PLC via ADS protocol.
// It handles symbol discovery, handle management, and type-safe read/write operations.
type Client struct {
	conn        *adsConnection
	targetNetId AmsNetId
	targetPort  uint16
	localNetId  AmsNetId
	localPort   uint16

	// Symbol cache for efficient access
	symbols     map[string]*SymbolEntry
	symbolsMu   sync.RWMutex
	symbolsLoaded bool

	// Connection state
	connected bool
	mu        sync.Mutex

	// Device info (cached after first read)
	deviceInfo *DeviceInfo
}

// SymbolEntry holds cached information about a symbol.
type SymbolEntry struct {
	Info   TagInfo
	Handle uint32 // Cached handle (0 if not acquired)
}

// DeviceInfo contains information about the connected TwinCAT device.
type DeviceInfo struct {
	MajorVersion uint8
	MinorVersion uint8
	BuildVersion uint16
	DeviceName   string
}

// String returns a human-readable device description.
func (d *DeviceInfo) String() string {
	if d == nil {
		return "Unknown"
	}
	return fmt.Sprintf("%s v%d.%d.%d", d.DeviceName, d.MajorVersion, d.MinorVersion, d.BuildVersion)
}

// options holds configuration options for Connect.
type options struct {
	targetNetId AmsNetId
	targetPort  uint16
	timeout     time.Duration
}

// Option is a functional option for Connect.
type Option func(*options)

// WithAmsNetId configures the target AMS Net ID.
// If not specified, it will be derived from the IP address (IP.1.1).
func WithAmsNetId(netId string) Option {
	return func(o *options) {
		parsed, err := ParseAmsNetId(netId)
		if err == nil {
			o.targetNetId = parsed
		}
	}
}

// WithAmsPort configures the target AMS port.
// Default is 851 (TwinCAT 3 PLC runtime 1).
func WithAmsPort(port uint16) Option {
	return func(o *options) {
		o.targetPort = port
	}
}

// WithTimeout configures the connection and operation timeout.
// Default is 5 seconds.
func WithTimeout(d time.Duration) Option {
	return func(o *options) {
		o.timeout = d
	}
}

// Connect establishes a connection to a Beckhoff TwinCAT PLC at the given address.
// The address should be an IP address or hostname (port 48898 is used for ADS).
func Connect(address string, opts ...Option) (*Client, error) {
	// Apply options
	cfg := &options{
		targetPort: PortTC3PLC1, // Default TwinCAT 3 PLC runtime 1
		timeout:    5 * time.Second,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	// Parse address and extract IP
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		// No port specified, use address as-is
		host = address
	}

	// Derive target Net ID from IP if not specified
	if cfg.targetNetId.IsZero() {
		cfg.targetNetId, err = AmsNetIdFromIP(host)
		if err != nil {
			return nil, fmt.Errorf("Connect: cannot derive AMS Net ID from %q: %w", host, err)
		}
	}

	// Connect to ADS TCP port
	tcpAddr := fmt.Sprintf("%s:%d", host, DefaultTCPPort)
	logging.DebugConnect("ADS", tcpAddr)
	logging.DebugLog("ADS", "Connection params: targetNetId=%s, targetPort=%d", cfg.targetNetId.String(), cfg.targetPort)

	conn, err := net.DialTimeout("tcp", tcpAddr, cfg.timeout)
	if err != nil {
		logging.DebugConnectError("ADS", tcpAddr, err)
		return nil, fmt.Errorf("Connect: %w", err)
	}

	logging.DebugLog("ADS", "TCP connection established to %s", tcpAddr)

	// Set connection timeouts
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetKeepAlive(true)
		tcpConn.SetKeepAlivePeriod(30 * time.Second)
	}

	// Derive local AMS Net ID from the connection's local IP address.
	// This must match what the Beckhoff route expects (typically IP.1.1).
	var localNetId AmsNetId
	if localAddr, ok := conn.LocalAddr().(*net.TCPAddr); ok {
		// Get IPv4 address (handles IPv6-mapped IPv4 addresses)
		ip := localAddr.IP
		if ip4 := ip.To4(); ip4 != nil {
			ip = ip4
		}
		localNetId, err = AmsNetIdFromIP(ip.String())
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("Connect: cannot derive local AMS Net ID from %s: %w", ip.String(), err)
		}
	} else {
		// Fallback to generic ID (may not work with strict routes)
		localNetId = AmsNetId{0, 0, 0, 0, 1, 1}
	}
	localPort := uint16(32768 + (time.Now().UnixNano() % 1000)) // Random-ish port

	adsConn := newAdsConnection(conn, localNetId, localPort)

	client := &Client{
		conn:        adsConn,
		targetNetId: cfg.targetNetId,
		targetPort:  cfg.targetPort,
		localNetId:  localNetId,
		localPort:   localPort,
		symbols:     make(map[string]*SymbolEntry),
		connected:   true,
	}

	// Verify connection by reading device info
	info, err := client.readDeviceInfo()
	if err != nil {
		logging.DebugError("ADS", "readDeviceInfo", err)
		conn.Close()
		return nil, fmt.Errorf("Connect: failed to read device info: %w", err)
	}
	client.deviceInfo = info

	logging.DebugConnectSuccess("ADS", tcpAddr, fmt.Sprintf("device=%s, local=%s:%d, target=%s:%d",
		info.String(), localNetId.String(), localPort, cfg.targetNetId.String(), cfg.targetPort))

	return client, nil
}

// Close releases all resources associated with the client.
func (c *Client) Close() {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	logging.DebugDisconnect("ADS", c.targetNetId.String(), "close requested")

	c.connected = false

	// Release any acquired handles
	c.symbolsMu.Lock()
	handleCount := 0
	for _, entry := range c.symbols {
		if entry.Handle != 0 {
			_ = c.releaseHandleUnsafe(entry.Handle)
			entry.Handle = 0
			handleCount++
		}
	}
	c.symbolsMu.Unlock()

	logging.DebugLog("ADS", "Released %d symbol handles", handleCount)

	if c.conn != nil {
		c.conn.close()
		c.conn = nil
	}
}

// IsConnected returns true if the client is connected.
func (c *Client) IsConnected() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

// SetDisconnected marks the client as disconnected.
// This is called when a read/write error indicates the connection is lost.
func (c *Client) SetDisconnected() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connected = false
}

// Reconnect attempts to re-establish the connection.
// Returns nil if already connected, otherwise attempts reconnection.
func (c *Client) Reconnect() error {
	if c == nil {
		return fmt.Errorf("nil client")
	}

	c.mu.Lock()
	if c.connected {
		c.mu.Unlock()
		return nil
	}

	// Close existing connection if any
	if c.conn != nil {
		c.conn.close()
		c.conn = nil
	}

	// Clear symbol handles (they're invalid after reconnection)
	c.symbolsMu.Lock()
	for _, entry := range c.symbols {
		entry.Handle = 0
	}
	c.symbolsMu.Unlock()

	targetNetId := c.targetNetId
	localPort := c.localPort
	c.mu.Unlock()

	// Derive host from target Net ID (first 4 bytes are typically the IP)
	host := fmt.Sprintf("%d.%d.%d.%d", targetNetId[0], targetNetId[1], targetNetId[2], targetNetId[3])

	// Connect to ADS TCP port
	tcpAddr := fmt.Sprintf("%s:%d", host, DefaultTCPPort)
	logging.DebugLog("ADS", "Reconnecting to %s", tcpAddr)

	conn, err := net.DialTimeout("tcp", tcpAddr, 10*time.Second)
	if err != nil {
		logging.DebugConnectError("ADS", tcpAddr, err)
		return fmt.Errorf("reconnect failed: %w", err)
	}

	logging.DebugLog("ADS", "Reconnect TCP connection established to %s", tcpAddr)

	// Set connection timeouts
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetKeepAlive(true)
		tcpConn.SetKeepAlivePeriod(30 * time.Second)
	}

	// Derive local AMS Net ID from the connection's local IP address.
	var localNetId AmsNetId
	if localAddr, ok := conn.LocalAddr().(*net.TCPAddr); ok {
		localNetId, err = AmsNetIdFromIP(localAddr.IP.String())
		if err != nil {
			conn.Close()
			return fmt.Errorf("reconnect: cannot derive local AMS Net ID: %w", err)
		}
	} else {
		localNetId = AmsNetId{0, 0, 0, 0, 1, 1}
	}

	adsConn := newAdsConnection(conn, localNetId, localPort)

	c.mu.Lock()
	c.conn = adsConn
	c.localNetId = localNetId
	c.connected = true
	c.mu.Unlock()

	// Verify connection by reading device info
	info, err := c.readDeviceInfo()
	if err != nil {
		logging.DebugError("ADS", "reconnect readDeviceInfo", err)
		c.mu.Lock()
		c.connected = false
		c.conn.close()
		c.conn = nil
		c.mu.Unlock()
		return fmt.Errorf("reconnect verification failed: %w", err)
	}
	c.deviceInfo = info

	logging.DebugConnectSuccess("ADS", tcpAddr, fmt.Sprintf("reconnected, device=%s", info.String()))

	return nil
}

// isConnectionError checks if an error indicates the TCP connection is broken.
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	// Common connection-related error patterns
	return strings.Contains(errStr, "connection") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "reset by peer") ||
		strings.Contains(errStr, "eof") ||
		strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "refused") ||
		strings.Contains(errStr, "closed") ||
		strings.Contains(errStr, "nil")
}

// ConnectionDetails returns a string describing the AMS addressing for debugging.
func (c *Client) ConnectionDetails() string {
	if c == nil {
		return ""
	}
	return fmt.Sprintf("Target: %s:%d, Local: %s:%d",
		c.targetNetId.String(), c.targetPort,
		c.localNetId.String(), c.localPort)
}

// ConnectionMode returns a human-readable string describing the connection mode.
func (c *Client) ConnectionMode() string {
	if c == nil {
		return "Not connected"
	}
	c.mu.Lock()
	connected := c.connected
	c.mu.Unlock()

	if connected {
		if c.deviceInfo != nil {
			return fmt.Sprintf("ADS Connected (%s)", c.deviceInfo.String())
		}
		return "ADS Connected"
	}
	return "Disconnected"
}

// GetDeviceInfo returns information about the connected device.
func (c *Client) GetDeviceInfo() (*DeviceInfo, error) {
	if c == nil {
		return nil, fmt.Errorf("GetDeviceInfo: nil client")
	}
	if c.deviceInfo != nil {
		return c.deviceInfo, nil
	}
	return c.readDeviceInfo()
}

// readDeviceInfo reads device information from the PLC.
func (c *Client) readDeviceInfo() (*DeviceInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return nil, fmt.Errorf("not connected")
	}

	// Send ReadDeviceInfo command (no data)
	resp, err := c.conn.sendRequest(c.targetNetId, c.targetPort, CmdReadDeviceInfo, nil)
	if err != nil {
		return nil, err
	}

	// Response: [Result 4] [MajorVersion 1] [MinorVersion 1] [BuildVersion 2] [DeviceName 16]
	if len(resp) < 24 {
		return nil, fmt.Errorf("response too short: %d bytes", len(resp))
	}

	result := binary.LittleEndian.Uint32(resp[0:4])
	if result != 0 {
		return nil, &AdsError{Code: result}
	}

	info := &DeviceInfo{
		MajorVersion: resp[4],
		MinorVersion: resp[5],
		BuildVersion: binary.LittleEndian.Uint16(resp[6:8]),
	}

	// Device name is null-terminated within 16 bytes
	nameBytes := resp[8:24]
	for i, b := range nameBytes {
		if b == 0 {
			info.DeviceName = string(nameBytes[:i])
			break
		}
	}
	if info.DeviceName == "" {
		info.DeviceName = string(nameBytes)
	}

	return info, nil
}

// Read reads one or more symbols by name and returns their values.
// Symbol names are typically in the format "MAIN.VariableName" or "GVL.GlobalVar".
// This uses ADS SumUp Read to batch all reads into a single request for efficiency.
func (c *Client) Read(symbolNames ...string) ([]*TagValue, error) {
	if c == nil || c.conn == nil {
		logging.DebugLog("ADS", "Read called but not connected")
		return nil, fmt.Errorf("Read: nil client")
	}
	if len(symbolNames) == 0 {
		return nil, nil
	}

	// For a single symbol, use the simple path
	if len(symbolNames) == 1 {
		logging.DebugLog("ADS", "Read 1 symbol (single)")
		value, err := c.readSymbol(symbolNames[0])
		if err != nil {
			return []*TagValue{{Name: symbolNames[0], Error: err}}, nil
		}
		return []*TagValue{value}, nil
	}

	logging.DebugLog("ADS", "Read %d symbols (batched)", len(symbolNames))

	// Get symbol entries (from cache or PLC)
	entries, err := c.getSymbolEntries(symbolNames)
	if err != nil {
		// If we can't get symbol info, fall back to individual reads
		logging.DebugLog("ADS", "Symbol lookup failed, falling back to individual reads: %v", err)
		return c.readIndividual(symbolNames)
	}

	// Perform batched read using SumUp Read with direct addressing
	results, err := c.readBatch(symbolNames, entries)
	if err != nil {
		// If batch read fails, fall back to individual reads
		logging.DebugLog("ADS", "Batch read failed, falling back to individual reads: %v", err)
		return c.readIndividual(symbolNames)
	}

	return results, nil
}

// getSymbolEntries retrieves symbol entries for multiple symbols.
// Returns entries in the same order as symbolNames.
func (c *Client) getSymbolEntries(symbolNames []string) ([]*SymbolEntry, error) {
	entries := make([]*SymbolEntry, len(symbolNames))

	for i, name := range symbolNames {
		entry, err := c.getSymbolEntry(name)
		if err != nil {
			return nil, fmt.Errorf("get symbol entry for %s: %w", name, err)
		}
		entries[i] = entry
	}

	return entries, nil
}

// readBatch reads multiple symbols in a single ADS SumUp Read request.
// Uses direct addressing via IndexGroup/IndexOffset (no handles required).
func (c *Client) readBatch(symbolNames []string, entries []*SymbolEntry) ([]*TagValue, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		c.connected = false
		return nil, fmt.Errorf("not connected")
	}

	count := len(entries)

	// Calculate total data size (sum of all symbol sizes)
	totalDataSize := uint32(0)
	for _, entry := range entries {
		totalDataSize += entry.Info.Size
	}

	// SumUp Read response format:
	// [Result1 4][Result2 4]...[ResultN 4][Data1][Data2]...[DataN]
	// So readLen = N * 4 (results) + sum of data sizes
	totalReadLen := uint32(count)*4 + totalDataSize

	// Build SumUp Read request using direct addressing
	// WriteData: For each symbol: [IndexGroup 4][IndexOffset 4][Size 4]
	writeLen := uint32(count * 12)
	writeData := make([]byte, writeLen)

	for i, entry := range entries {
		offset := i * 12
		// Use direct IndexGroup/IndexOffset from symbol info (typically 0x4040/offset)
		binary.LittleEndian.PutUint32(writeData[offset:offset+4], entry.Info.IndexGroup)
		binary.LittleEndian.PutUint32(writeData[offset+4:offset+8], entry.Info.IndexOffset)
		binary.LittleEndian.PutUint32(writeData[offset+8:offset+12], entry.Info.Size)
	}

	// Build ReadWrite request: [IndexGroup 4][IndexOffset 4][ReadLength 4][WriteLength 4][WriteData]
	reqData := make([]byte, 16+len(writeData))
	binary.LittleEndian.PutUint32(reqData[0:4], IndexGroupSumUpRead)
	binary.LittleEndian.PutUint32(reqData[4:8], uint32(count))
	binary.LittleEndian.PutUint32(reqData[8:12], totalReadLen)
	binary.LittleEndian.PutUint32(reqData[12:16], writeLen)
	copy(reqData[16:], writeData)

	logging.DebugLog("ADS", "SumUp Read: %d symbols, readLen=%d, writeLen=%d", count, totalReadLen, writeLen)

	resp, err := c.conn.sendRequest(c.targetNetId, c.targetPort, CmdReadWrite, reqData)
	if err != nil {
		if isConnectionError(err) {
			logging.DebugDisconnect("ADS", c.targetNetId.String(), fmt.Sprintf("batch read failed: %v", err))
			c.connected = false
		}
		return nil, err
	}

	// Parse response: [Result 4][ReadLength 4][SubResults...][Data...]
	if len(resp) < 8 {
		return nil, fmt.Errorf("batch read response too short: %d bytes", len(resp))
	}

	result := binary.LittleEndian.Uint32(resp[0:4])
	if result != 0 {
		return nil, &AdsError{Code: result}
	}

	readLen := binary.LittleEndian.Uint32(resp[4:8])
	if uint32(len(resp)) < 8+readLen {
		return nil, fmt.Errorf("batch read response truncated: expected %d, got %d", 8+readLen, len(resp))
	}

	// SumUp Read response structure:
	// [Result1 4][Result2 4]...[ResultN 4][Data1][Data2]...[DataN]
	// First, read all N result codes
	subResults := make([]uint32, count)
	offset := uint32(8)
	for i := 0; i < count; i++ {
		if offset+4 > uint32(len(resp)) {
			return nil, fmt.Errorf("sub-result %d truncated", i)
		}
		subResults[i] = binary.LittleEndian.Uint32(resp[offset : offset+4])
		offset += 4
	}

	// Then read all data sections
	results := make([]*TagValue, count)
	for i := 0; i < count; i++ {
		entry := entries[i]
		name := symbolNames[i]
		size := entry.Info.Size

		if subResults[i] != 0 {
			results[i] = &TagValue{Name: name, Error: &AdsError{Code: subResults[i]}}
			// Still advance offset by the expected size
			offset += size
			continue
		}

		if offset+size > uint32(len(resp)) {
			results[i] = &TagValue{Name: name, Error: fmt.Errorf("data truncated")}
			continue
		}

		// Determine element count for arrays
		elemSize := TypeSize(entry.Info.TypeCode)
		elemCount := 1
		if elemSize > 0 && int(size) > elemSize {
			elemCount = int(size) / elemSize
		}

		// Special handling for string/wstring arrays
		if elemCount == 1 && (entry.Info.TypeCode == TypeString || entry.Info.TypeCode == TypeWString) {
			typeName := strings.ToUpper(entry.Info.TypeName)
			if strings.Contains(typeName, "ARRAY") {
				arrayCount := parseArrayCountFromTypeName(typeName)
				if arrayCount > 1 {
					elemCount = arrayCount
				}
			}
		}

		// Copy data bytes
		dataBytes := make([]byte, size)
		copy(dataBytes, resp[offset:offset+size])

		results[i] = &TagValue{
			Name:     name,
			DataType: entry.Info.TypeCode,
			Bytes:    dataBytes,
			Count:    elemCount,
			Error:    nil,
		}

		offset += size
	}

	logging.DebugLog("ADS", "SumUp Read completed: %d symbols", count)

	return results, nil
}

// readIndividual reads symbols one at a time (fallback for batch failures).
func (c *Client) readIndividual(symbolNames []string) ([]*TagValue, error) {
	results := make([]*TagValue, 0, len(symbolNames))
	errorCount := 0

	for _, name := range symbolNames {
		value, err := c.readSymbol(name)
		if err != nil {
			errorCount++
			logging.DebugError("ADS", fmt.Sprintf("readSymbol %s", name), err)
			results = append(results, &TagValue{
				Name:  name,
				Error: err,
			})
		} else {
			results = append(results, value)
		}
	}

	if errorCount > 0 {
		logging.DebugLog("ADS", "Individual read completed: %d success, %d errors", len(symbolNames)-errorCount, errorCount)
	}

	return results, nil
}

// readSymbol reads a single symbol value.
func (c *Client) readSymbol(name string) (*TagValue, error) {
	// Get symbol info (from cache or PLC)
	entry, err := c.getSymbolEntry(name)
	if err != nil {
		// Check for connection error
		if isConnectionError(err) {
			c.mu.Lock()
			c.connected = false
			c.mu.Unlock()
		}
		return nil, err
	}

	// Ensure we have a handle
	if entry.Handle == 0 {
		logging.DebugLog("ADS", "Acquiring handle for %s", name)
		handle, err := c.acquireHandle(name)
		if err != nil {
			// Check for connection error
			if isConnectionError(err) {
				logging.DebugDisconnect("ADS", c.targetNetId.String(), fmt.Sprintf("handle acquisition failed: %v", err))
				c.mu.Lock()
				c.connected = false
				c.mu.Unlock()
			}
			return nil, err
		}
		logging.DebugLog("ADS", "Acquired handle 0x%08X for %s", handle, name)
		c.symbolsMu.Lock()
		entry.Handle = handle
		c.symbolsMu.Unlock()
	}

	// Read using handle
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		c.connected = false
		return nil, fmt.Errorf("not connected")
	}

	// Build read request: [IndexGroup 4] [IndexOffset 4] [Length 4]
	data := make([]byte, 12)
	binary.LittleEndian.PutUint32(data[0:4], IndexGroupSymbolValueByHandle)
	binary.LittleEndian.PutUint32(data[4:8], entry.Handle)
	binary.LittleEndian.PutUint32(data[8:12], entry.Info.Size)

	resp, err := c.conn.sendRequest(c.targetNetId, c.targetPort, CmdRead, data)
	if err != nil {
		// Check for connection error
		if isConnectionError(err) {
			logging.DebugDisconnect("ADS", c.targetNetId.String(), fmt.Sprintf("read failed: %v", err))
			c.connected = false
		}
		return nil, err
	}

	// Response: [Result 4] [Length 4] [Data n]
	if len(resp) < 8 {
		return nil, fmt.Errorf("response too short: %d bytes", len(resp))
	}

	result := binary.LittleEndian.Uint32(resp[0:4])
	if result != 0 {
		return nil, &AdsError{Code: result}
	}

	length := binary.LittleEndian.Uint32(resp[4:8])
	if len(resp) < int(8+length) {
		return nil, fmt.Errorf("response data truncated: expected %d, got %d", length, len(resp)-8)
	}

	// Determine element count for arrays
	elemSize := TypeSize(entry.Info.TypeCode)
	count := 1
	if elemSize > 0 && int(length) > elemSize {
		count = int(length) / elemSize
	}

	// Special handling for string/wstring arrays - parse from TypeName
	if count == 1 && (entry.Info.TypeCode == TypeString || entry.Info.TypeCode == TypeWString) {
		// Check if TypeName indicates an array (e.g., "ARRAY [0..4] OF STRING")
		typeName := strings.ToUpper(entry.Info.TypeName)
		if strings.Contains(typeName, "ARRAY") {
			// Try to extract array bounds from TypeName
			arrayCount := parseArrayCountFromTypeName(typeName)
			if arrayCount > 1 {
				count = arrayCount
			}
		}
	}

	return &TagValue{
		Name:     name,
		DataType: entry.Info.TypeCode,
		Bytes:    resp[8 : 8+length],
		Count:    count,
		Error:    nil,
	}, nil
}

// Write writes a value to a symbol.
func (c *Client) Write(symbolName string, value interface{}) error {
	if c == nil || c.conn == nil {
		return fmt.Errorf("Write: nil client")
	}

	// Get symbol info
	entry, err := c.getSymbolEntry(symbolName)
	if err != nil {
		return err
	}

	// Check if writable
	if !entry.Info.IsWritable() {
		return fmt.Errorf("symbol %q is read-only", symbolName)
	}

	// Encode value - special handling for string arrays
	var data []byte
	if strSlice, ok := value.([]string); ok && (entry.Info.TypeCode == TypeString || entry.Info.TypeCode == TypeWString) {
		// String array: need to pad each element to fixed size
		// Get actual array element count from TypeName (e.g., "ARRAY [0..4] OF STRING")
		arrayCount := parseArrayCountFromTypeName(entry.Info.TypeName)
		if arrayCount < 1 {
			arrayCount = len(strSlice) // Fallback to input length
		}
		elemSize := int(entry.Info.Size) / arrayCount
		if elemSize < 1 {
			elemSize = 81 // Default STRING size
		}
		data, err = encodeStringArray(strSlice, elemSize, entry.Info.TypeCode == TypeWString)
	} else {
		data, err = EncodeValueWithType(value, entry.Info.TypeCode)
	}
	if err != nil {
		return fmt.Errorf("encode value: %w", err)
	}

	// Ensure we have a handle
	if entry.Handle == 0 {
		handle, err := c.acquireHandle(symbolName)
		if err != nil {
			return err
		}
		c.symbolsMu.Lock()
		entry.Handle = handle
		c.symbolsMu.Unlock()
	}

	// Write using handle
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return fmt.Errorf("not connected")
	}

	// Build write request: [IndexGroup 4] [IndexOffset 4] [Length 4] [Data n]
	req := make([]byte, 12+len(data))
	binary.LittleEndian.PutUint32(req[0:4], IndexGroupSymbolValueByHandle)
	binary.LittleEndian.PutUint32(req[4:8], entry.Handle)
	binary.LittleEndian.PutUint32(req[8:12], uint32(len(data)))
	copy(req[12:], data)

	resp, err := c.conn.sendRequest(c.targetNetId, c.targetPort, CmdWrite, req)
	if err != nil {
		return err
	}

	// Response: [Result 4]
	if len(resp) < 4 {
		return fmt.Errorf("response too short: %d bytes", len(resp))
	}

	result := binary.LittleEndian.Uint32(resp[0:4])
	if result != 0 {
		return &AdsError{Code: result}
	}

	return nil
}

// encodeStringArray encodes a string slice with fixed-size padding for each element.
// This is needed for TwinCAT STRING arrays where each element has a fixed size.
func encodeStringArray(strings []string, elemSize int, isWString bool) ([]byte, error) {
	if elemSize <= 0 {
		return nil, fmt.Errorf("invalid element size: %d", elemSize)
	}

	result := make([]byte, len(strings)*elemSize)

	for i, s := range strings {
		offset := i * elemSize
		if isWString {
			// WSTRING: UTF-16LE encoding with null terminator
			// For now, just do simple ASCII to UTF-16LE conversion
			maxChars := (elemSize - 2) / 2 // Reserve 2 bytes for null terminator
			for j, r := range s {
				if j >= maxChars {
					break
				}
				result[offset+j*2] = byte(r)
				result[offset+j*2+1] = 0
			}
		} else {
			// STRING: null-terminated, pad rest with zeros
			maxLen := elemSize - 1 // Reserve 1 byte for null terminator
			copyLen := len(s)
			if copyLen > maxLen {
				copyLen = maxLen
			}
			copy(result[offset:offset+copyLen], s)
			// Rest is already zero from make()
		}
	}

	return result, nil
}

// getSymbolEntry retrieves a symbol entry from cache or discovers it from the PLC.
func (c *Client) getSymbolEntry(name string) (*SymbolEntry, error) {
	// Check cache first
	c.symbolsMu.RLock()
	entry, ok := c.symbols[name]
	c.symbolsMu.RUnlock()

	if ok {
		return entry, nil
	}

	// Get symbol info from PLC
	info, err := c.getSymbolInfo(name)
	if err != nil {
		return nil, err
	}

	// Cache it
	entry = &SymbolEntry{
		Info:   *info,
		Handle: 0,
	}
	c.symbolsMu.Lock()
	c.symbols[name] = entry
	c.symbolsMu.Unlock()

	return entry, nil
}

// getSymbolInfo retrieves symbol information from the PLC.
func (c *Client) getSymbolInfo(name string) (*TagInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return nil, fmt.Errorf("not connected")
	}

	// Use ReadWrite to get symbol info by name
	// Request: [IndexGroup 4] [IndexOffset 4] [ReadLength 4] [WriteLength 4] [SymbolName n]
	nameBytes := append([]byte(name), 0) // Null-terminated

	req := make([]byte, 16+len(nameBytes))
	binary.LittleEndian.PutUint32(req[0:4], IndexGroupSymbolInfoByNameEx)
	binary.LittleEndian.PutUint32(req[4:8], 0)                   // Offset = 0
	binary.LittleEndian.PutUint32(req[8:12], 0xFFFF)             // Read up to 64KB
	binary.LittleEndian.PutUint32(req[12:16], uint32(len(nameBytes)))
	copy(req[16:], nameBytes)

	resp, err := c.conn.sendRequest(c.targetNetId, c.targetPort, CmdReadWrite, req)
	if err != nil {
		return nil, err
	}

	// Response: [Result 4] [ReadLength 4] [SymbolInfo]
	if len(resp) < 8 {
		return nil, fmt.Errorf("response too short: %d bytes", len(resp))
	}

	result := binary.LittleEndian.Uint32(resp[0:4])
	if result != 0 {
		return nil, &AdsError{Code: result}
	}

	readLen := binary.LittleEndian.Uint32(resp[4:8])
	if len(resp) < int(8+readLen) {
		return nil, fmt.Errorf("response data truncated")
	}

	infoData := resp[8:]
	return parseSymbolInfo(infoData)
}

// parseSymbolInfo parses the symbol info response from TwinCAT.
// Format (AdsSymbolEntry):
// [EntryLength 4] [IndexGroup 4] [IndexOffset 4] [Size 4] [DataType 4]
// [Flags 4] [NameLength 2] [TypeLength 2] [CommentLength 2]
// [Name] [Type] [Comment]
func parseSymbolInfo(data []byte) (*TagInfo, error) {
	if len(data) < 30 {
		return nil, fmt.Errorf("symbol info too short: %d bytes", len(data))
	}

	info := &TagInfo{
		IndexGroup:  binary.LittleEndian.Uint32(data[4:8]),
		IndexOffset: binary.LittleEndian.Uint32(data[8:12]),
		Size:        binary.LittleEndian.Uint32(data[12:16]),
		Flags:       binary.LittleEndian.Uint32(data[20:24]),
	}

	// TwinCAT data type is stored as ADST_* enum, need to map to our type codes
	adsType := binary.LittleEndian.Uint32(data[16:20])
	info.TypeCode = mapAdsType(adsType)

	nameLen := binary.LittleEndian.Uint16(data[24:26])
	typeLen := binary.LittleEndian.Uint16(data[26:28])
	commentLen := binary.LittleEndian.Uint16(data[28:30])

	offset := uint16(30)

	// Parse name (null-terminated)
	if len(data) > int(offset+nameLen) {
		info.Name = string(data[offset : offset+nameLen])
		if len(info.Name) > 0 && info.Name[len(info.Name)-1] == 0 {
			info.Name = info.Name[:len(info.Name)-1]
		}
		offset += nameLen + 1 // +1 for null terminator
	}

	// Parse type name
	if len(data) > int(offset+typeLen) {
		info.TypeName = string(data[offset : offset+typeLen])
		if len(info.TypeName) > 0 && info.TypeName[len(info.TypeName)-1] == 0 {
			info.TypeName = info.TypeName[:len(info.TypeName)-1]
		}
		offset += typeLen + 1
	}

	// Parse comment
	if len(data) > int(offset+commentLen) {
		info.Comment = string(data[offset : offset+commentLen])
		if len(info.Comment) > 0 && info.Comment[len(info.Comment)-1] == 0 {
			info.Comment = info.Comment[:len(info.Comment)-1]
		}
	}

	// If type code is unknown, try to determine from TypeName
	if info.TypeCode == TypeUnknown && info.TypeName != "" {
		info.TypeCode = mapTypeFromName(info.TypeName, info.Size)
	}

	return info, nil
}

// mapTypeFromName attempts to determine the type code from the type name string.
// This is used as a fallback when the ADS type code is unknown (e.g., LTIME, TOD).
func mapTypeFromName(typeName string, size uint32) uint16 {
	// Normalize to uppercase for matching
	upper := strings.ToUpper(typeName)

	// Handle array types by extracting the base type
	if strings.HasPrefix(upper, "ARRAY") {
		// Extract base type from "ARRAY [x..y] OF TYPE"
		if idx := strings.Index(upper, " OF "); idx != -1 {
			upper = strings.TrimSpace(upper[idx+4:])
		}
	}

	// Match known type names
	switch upper {
	case "LTIME":
		return TypeLTime
	case "TIME":
		return TypeTime
	case "DATE":
		return TypeDate
	case "TIME_OF_DAY", "TOD":
		return TypeTimeOfDay
	case "DATE_AND_TIME", "DT":
		return TypeDateTime
	case "BOOL":
		return TypeBool
	case "BYTE", "USINT":
		return TypeByte
	case "SINT":
		return TypeSByte
	case "WORD", "UINT":
		return TypeWord
	case "INT":
		return TypeInt16
	case "DWORD", "UDINT":
		return TypeDWord
	case "DINT":
		return TypeInt32
	case "LWORD", "ULINT":
		return TypeLWord
	case "LINT":
		return TypeInt64
	case "REAL":
		return TypeReal
	case "LREAL":
		return TypeLReal
	case "STRING":
		return TypeString
	case "WSTRING":
		return TypeWString
	}

	// Check if type name starts with STRING (e.g., "STRING(80)")
	if strings.HasPrefix(upper, "STRING") {
		return TypeString
	}
	if strings.HasPrefix(upper, "WSTRING") {
		return TypeWString
	}

	return TypeUnknown
}

// mapAdsType maps TwinCAT ADST_* type enum to our type codes.
func mapAdsType(adsType uint32) uint16 {
	switch adsType {
	case 0: // ADST_VOID
		return TypeVoid
	case 16: // ADST_INT8
		return TypeSByte
	case 17: // ADST_UINT8
		return TypeByte
	case 2: // ADST_INT16
		return TypeInt16
	case 18: // ADST_UINT16
		return TypeWord
	case 3: // ADST_INT32
		return TypeInt32
	case 19: // ADST_UINT32
		return TypeDWord
	case 20: // ADST_INT64
		return TypeInt64
	case 21: // ADST_UINT64
		return TypeLWord
	case 4: // ADST_REAL32
		return TypeReal
	case 5: // ADST_REAL64
		return TypeLReal
	case 30: // ADST_STRING
		return TypeString
	case 31: // ADST_WSTRING
		return TypeWString
	case 33: // ADST_BOOL / ADST_BIT
		return TypeBool
	default:
		// For complex types, return TypeUnknown
		return TypeUnknown
	}
}

// parseArrayCountFromTypeName extracts array element count from TwinCAT type names.
// Examples: "ARRAY [0..4] OF STRING" -> 5, "ARRAY [1..10] OF INT" -> 10
func parseArrayCountFromTypeName(typeName string) int {
	// Look for pattern like "[0..4]" or "[1..10]"
	startIdx := strings.Index(typeName, "[")
	endIdx := strings.Index(typeName, "]")
	if startIdx == -1 || endIdx == -1 || endIdx <= startIdx {
		return 1
	}

	bounds := typeName[startIdx+1 : endIdx]
	// Split by ".." to get lower and upper bounds
	parts := strings.Split(bounds, "..")
	if len(parts) != 2 {
		return 1
	}

	// Parse bounds - handle both "0..4" and "1..5" styles
	lower := 0
	upper := 0
	fmt.Sscanf(strings.TrimSpace(parts[0]), "%d", &lower)
	fmt.Sscanf(strings.TrimSpace(parts[1]), "%d", &upper)

	if upper >= lower {
		return upper - lower + 1
	}
	return 1
}

// acquireHandle gets a handle for a symbol name.
func (c *Client) acquireHandle(name string) (uint32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return 0, fmt.Errorf("not connected")
	}

	// ReadWrite to get handle by name
	// Write: symbol name (null-terminated)
	// Read: handle (4 bytes)
	nameBytes := append([]byte(name), 0)

	req := make([]byte, 16+len(nameBytes))
	binary.LittleEndian.PutUint32(req[0:4], IndexGroupSymbolHandleByName)
	binary.LittleEndian.PutUint32(req[4:8], 0)
	binary.LittleEndian.PutUint32(req[8:12], 4) // Read 4 bytes (handle)
	binary.LittleEndian.PutUint32(req[12:16], uint32(len(nameBytes)))
	copy(req[16:], nameBytes)

	resp, err := c.conn.sendRequest(c.targetNetId, c.targetPort, CmdReadWrite, req)
	if err != nil {
		return 0, err
	}

	// Response: [Result 4] [ReadLength 4] [Handle 4]
	if len(resp) < 12 {
		return 0, fmt.Errorf("response too short: %d bytes", len(resp))
	}

	result := binary.LittleEndian.Uint32(resp[0:4])
	if result != 0 {
		return 0, &AdsError{Code: result}
	}

	handle := binary.LittleEndian.Uint32(resp[8:12])
	return handle, nil
}

// releaseHandleUnsafe releases a symbol handle (caller must hold c.mu).
func (c *Client) releaseHandleUnsafe(handle uint32) error {
	if c.conn == nil {
		return nil
	}

	// Write to release handle index group
	req := make([]byte, 16)
	binary.LittleEndian.PutUint32(req[0:4], IndexGroupSymbolReleaseHandle)
	binary.LittleEndian.PutUint32(req[4:8], 0)
	binary.LittleEndian.PutUint32(req[8:12], 4) // Size of handle
	handleBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(handleBytes, handle)

	fullReq := append(req, handleBytes...)

	_, err := c.conn.sendRequest(c.targetNetId, c.targetPort, CmdWrite, fullReq)
	return err
}

// AllTags discovers and returns all symbols from the PLC.
// This performs a full symbol table upload which may take time on large projects.
func (c *Client) AllTags() ([]TagInfo, error) {
	if c == nil || c.conn == nil {
		logging.DebugLog("ADS", "AllTags called but not connected")
		return nil, fmt.Errorf("AllTags: nil client")
	}

	// Check if already loaded
	c.symbolsMu.RLock()
	if c.symbolsLoaded {
		tags := make([]TagInfo, 0, len(c.symbols))
		for _, entry := range c.symbols {
			tags = append(tags, entry.Info)
		}
		c.symbolsMu.RUnlock()
		logging.DebugLog("ADS", "AllTags returning %d cached symbols", len(tags))
		return tags, nil
	}
	c.symbolsMu.RUnlock()

	// Get upload info first
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return nil, fmt.Errorf("not connected")
	}

	// Read upload info: symbol count and size
	req := make([]byte, 12)
	binary.LittleEndian.PutUint32(req[0:4], IndexGroupSymbolUploadInfo2)
	binary.LittleEndian.PutUint32(req[4:8], 0)
	binary.LittleEndian.PutUint32(req[8:12], 24) // Read 24 bytes of info

	resp, err := c.conn.sendRequest(c.targetNetId, c.targetPort, CmdRead, req)
	if err != nil {
		return nil, fmt.Errorf("read upload info: %w", err)
	}

	if len(resp) < 16 {
		return nil, fmt.Errorf("upload info response too short: %d bytes", len(resp))
	}

	result := binary.LittleEndian.Uint32(resp[0:4])
	if result != 0 {
		return nil, &AdsError{Code: result}
	}

	// Info structure: [SymbolCount 4] [SymbolSize 4] [DataTypeCount 4] [DataTypeSize 4] ...
	symbolCount := binary.LittleEndian.Uint32(resp[8:12])
	symbolSize := binary.LittleEndian.Uint32(resp[12:16])

	logging.DebugLog("ADS", "Symbol upload info: %d symbols, %d bytes", symbolCount, symbolSize)

	if symbolCount == 0 {
		return nil, nil
	}

	// Upload symbol table
	req2 := make([]byte, 12)
	binary.LittleEndian.PutUint32(req2[0:4], IndexGroupSymbolUpload)
	binary.LittleEndian.PutUint32(req2[4:8], 0)
	binary.LittleEndian.PutUint32(req2[8:12], symbolSize)

	resp2, err := c.conn.sendRequest(c.targetNetId, c.targetPort, CmdRead, req2)
	if err != nil {
		return nil, fmt.Errorf("upload symbols: %w", err)
	}

	if len(resp2) < 8 {
		return nil, fmt.Errorf("symbol upload response too short")
	}

	result = binary.LittleEndian.Uint32(resp2[0:4])
	if result != 0 {
		return nil, &AdsError{Code: result}
	}

	dataLen := binary.LittleEndian.Uint32(resp2[4:8])
	symbolData := resp2[8:]
	if uint32(len(symbolData)) < dataLen {
		return nil, fmt.Errorf("symbol data truncated: expected %d, got %d", dataLen, len(symbolData))
	}

	// Parse symbol entries
	tags := make([]TagInfo, 0, symbolCount)
	offset := uint32(0)

	for i := uint32(0); i < symbolCount && offset < dataLen; i++ {
		if offset+4 > dataLen {
			break
		}

		entryLen := binary.LittleEndian.Uint32(symbolData[offset : offset+4])
		if offset+entryLen > dataLen {
			break
		}

		info, err := parseSymbolInfo(symbolData[offset : offset+entryLen])
		if err == nil && info.IsPrimitive() {
			tags = append(tags, *info)

			// Cache symbol
			c.symbolsMu.Lock()
			c.symbols[info.Name] = &SymbolEntry{Info: *info, Handle: 0}
			c.symbolsMu.Unlock()
		}

		offset += entryLen
	}

	c.symbolsMu.Lock()
	c.symbolsLoaded = true
	c.symbolsMu.Unlock()

	logging.DebugLog("ADS", "AllTags discovered %d primitive symbols from %d total", len(tags), symbolCount)

	return tags, nil
}

// Programs returns the unique top-level prefixes from discovered symbols.
// TwinCAT symbols are accessed by their full path (e.g., "MAIN.Variable", "GVL.GlobalVar").
// This extracts prefixes like MAIN, GVL, FB_Motor, etc. from the symbol table.
func (c *Client) Programs() ([]string, error) {
	c.symbolsMu.RLock()
	defer c.symbolsMu.RUnlock()

	if !c.symbolsLoaded || len(c.symbols) == 0 {
		// Symbols not yet loaded, return common defaults
		return []string{"MAIN", "GVL"}, nil
	}

	// Extract unique top-level prefixes from symbol names
	prefixes := make(map[string]bool)
	for name := range c.symbols {
		if idx := strings.Index(name, "."); idx > 0 {
			prefix := name[:idx]
			// Skip internal/system symbols (start with underscore or are all caps constants)
			if !strings.HasPrefix(prefix, "_") {
				prefixes[prefix] = true
			}
		}
	}

	// Convert to sorted slice
	result := make([]string, 0, len(prefixes))
	for prefix := range prefixes {
		result = append(result, prefix)
	}
	sort.Strings(result)

	if len(result) == 0 {
		// Fallback to defaults if no prefixes found
		return []string{"MAIN", "GVL"}, nil
	}

	return result, nil
}

// Identity returns device information in a format compatible with the plcman interface.
// This is an alias for GetDeviceInfo for API consistency.
func (c *Client) Identity() (*DeviceInfo, error) {
	return c.GetDeviceInfo()
}
