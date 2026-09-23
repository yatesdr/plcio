package ads

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yatesdr/plcio/metadata"
)

// ErrConnectionLost indicates an unusable ADS stream. Partial read slots are
// retained. Wrapped I/O causes and *AdsError remain inspectable with errors.As.
var ErrConnectionLost = errors.New("ads: connection lost during read")

// Client provides bounded symbolic ADS access. Lock ownership: mu protects only
// session publication/state and operation-gate initialization; never hold mu
// during I/O or while waiting on gate. gate serializes complete operations and
// owns all caches, handles and deadline. Close may close the stream under mu to
// abort active work, but never mutates operation-owned caches without gate.
type Client struct {
	mu                                   sync.Mutex
	gate                                 chan struct{}
	conn                                 *adsConnection
	connected, closed                    bool
	epoch, generation                    uint64
	endpoint                             string
	cfg                                  options
	targetNetId, localNetId              AmsNetId
	targetPort, localPort                uint16
	deviceInfo                           *DeviceInfo
	deadline                             time.Time
	symbols                              map[string]*SymbolEntry // direct lookup cache; not catalog membership
	catalog                              []TagInfo
	symbolsLoaded                        bool
	snapshot                             *schemaSnapshot
	versionCapability                    uint8 // 0 unknown, 1 supported, 2 documented unsupported
	symbolVersion                        uint32
	versionSet                           bool
	catalogUnavailable, typesUnavailable bool
	lookupSymbols                        map[string]*symbolRecord
	fallbackResolver                     *typeResolver
	lookupBytes                          uint64
}

// SymbolEntry retains the published raw symbol and handle layout.
type SymbolEntry struct {
	Info   TagInfo
	Handle uint32
}

// DeviceInfo describes the verified device identity.
type DeviceInfo struct {
	MajorVersion uint8
	MinorVersion uint8
	BuildVersion uint16
	DeviceName   string
}

func (d *DeviceInfo) String() string {
	if d == nil {
		return "Unknown"
	}
	return fmt.Sprintf("%s v%d.%d.%d", d.DeviceName, d.MajorVersion, d.MinorVersion, d.BuildVersion)
}

// Connect verifies identity before publishing a session. A supplied TCP port is
// retained; target/local AMS identities do not determine the TCP endpoint.
func Connect(address string, opts ...Option) (*Client, error) {
	endpoint, cfg, err := configure(address, opts)
	if err != nil {
		return nil, fmt.Errorf("Connect: %w", err)
	}
	c := &Client{endpoint: endpoint, cfg: cfg, targetNetId: cfg.targetNetId,
		targetPort: cfg.targetPort, localNetId: cfg.localNetId, localPort: cfg.localPort}
	if err := c.Reconnect(); err != nil {
		return nil, fmt.Errorf("Connect: %w", err)
	}
	return c, nil
}

func (c *Client) budgetLocked() time.Duration {
	if c.cfg.timeout == 0 {
		return 5 * time.Second
	} // zero Client is usable in offline tests
	return c.cfg.timeout
}

func (c *Client) gateLocked() chan struct{} {
	if c.gate == nil {
		c.gate = make(chan struct{}, 1)
		c.gate <- struct{}{}
	}
	return c.gate
}

func (c *Client) begin(allowDisconnected bool) (uint64, func(), error) {
	if c == nil {
		return 0, nil, fmt.Errorf("nil ADS client")
	}
	c.mu.Lock()
	gate, epoch, budget := c.gateLocked(), c.epoch, c.budgetLocked()
	c.mu.Unlock()
	deadline := time.Now().Add(budget)
	select {
	case <-gate:
	default:
		timer := time.NewTimer(time.Until(deadline))
		defer timer.Stop()
		select {
		case <-gate:
		case <-timer.C:
			return 0, nil, fmt.Errorf("ADS operation lock: %w", context.DeadlineExceeded)
		}
	}
	done := func() { gate <- struct{}{} }
	c.mu.Lock()
	valid := epoch == c.epoch && (allowDisconnected || (!c.closed && c.connected && c.conn != nil && !c.conn.dead.Load()))
	c.mu.Unlock()
	if !valid {
		done()
		return 0, nil, fmt.Errorf("%w: %w", ErrConnectionLost, net.ErrClosed)
	}
	c.deadline = deadline
	if c.symbols == nil {
		c.symbols = make(map[string]*SymbolEntry)
	}
	return epoch, done, nil
}

// Reconnect deduplicates attempts and retains the original TCP endpoint/options.
// An attempt predating Close cannot publish a late replacement session.
func (c *Client) Reconnect() error {
	epoch, done, err := c.begin(true)
	if err != nil {
		return err
	}
	defer done()
	if c.IsConnected() {
		return nil
	}
	if c.endpoint == "" {
		return fmt.Errorf("reconnect: original TCP endpoint unavailable")
	}
	ctx, cancel := context.WithDeadline(context.Background(), c.deadline)
	defer cancel()
	tcp, err := (&net.Dialer{}).DialContext(ctx, "tcp", c.endpoint)
	if err != nil {
		return fmt.Errorf("reconnect dial %s: %w", c.endpoint, err)
	}
	local := c.localNetId
	if local.IsZero() {
		addr, ok := tcp.LocalAddr().(*net.TCPAddr)
		if !ok {
			tcp.Close()
			return fmt.Errorf("cannot derive local AMS identity")
		}
		local, err = AmsNetIdFromIP(addr.IP.String())
		if err != nil {
			tcp.Close()
			return err
		}
	}
	port := c.localPort
	if port == 0 {
		port = uint16(32768 + time.Now().UnixNano()%1000)
	}
	conn := newAdsConnection(tcp, local, port)
	conn.maxPayload, conn.timeout = c.cfg.maxPayload, c.cfg.timeout
	if conn.maxPayload == 0 {
		conn.maxPayload = 1 << 20
	}
	if conn.timeout == 0 {
		conn.timeout = 5 * time.Second
	}
	resp, err := conn.sendRequestUntil(c.targetNetId, c.targetPort, CmdReadDeviceInfo, nil, c.deadline)
	if err != nil {
		conn.close()
		return fmt.Errorf("reconnect identity: %w", err)
	}
	info, err := decodeDeviceInfo(resp)
	if err != nil {
		conn.close()
		return fmt.Errorf("reconnect identity: %w", err)
	}
	c.mu.Lock()
	if epoch != c.epoch {
		c.mu.Unlock()
		conn.close()
		return fmt.Errorf("reconnect superseded by Close: %w", net.ErrClosed)
	}
	previous := c.conn
	c.conn, c.localNetId, c.localPort, c.deviceInfo = conn, local, port, info
	c.connected, c.closed = true, false
	c.generation++
	c.mu.Unlock()
	if previous != nil {
		previous.close()
	}
	c.invalidateCaches()
	c.versionCapability, c.versionSet, c.catalogUnavailable, c.typesUnavailable = 0, false, false, false
	return nil
}

func (c *Client) invalidateCaches() {
	c.symbols = make(map[string]*SymbolEntry)
	c.catalog = nil
	c.symbolsLoaded = false
	c.snapshot = nil
	c.lookupSymbols = make(map[string]*symbolRecord)
	c.fallbackResolver = nil
	c.lookupBytes = 0
}

// invalidateLive discards generation-scoped caches on the current healthy
// stream and releases the discarded handles immediately, before this client can
// acquire replacements; a deferred release could free a reissued handle number.
// Reconnect and Close use invalidateCaches: an old stream's handles are gone.
func (c *Client) invalidateLive() {
	var handles []uint32
	seen := make(map[uint32]bool)
	for _, entry := range c.symbols {
		if entry.Handle != 0 && !seen[entry.Handle] {
			seen[entry.Handle] = true
			handles = append(handles, entry.Handle)
		}
		entry.Handle = 0 // An operation still holding the entry cannot reuse it.
	}
	c.invalidateCaches()
	c.releaseHandles(handles)
}

// trimLookupCaches keeps the direct-lookup caches (element paths, aliases and
// catalog-miss lookups) from failing permanently once their count/byte budgets
// fill. When the next operation, resolving up to n names, might not fit, all
// non-catalog lookup state is evicted (clear-on-full). It runs only at an
// operation boundary, before the operation resolves any name, so no in-flight
// entry loses its handle; evicted handles are released immediately, before any
// replacement can be acquired (the same ordering as invalidateLive). Catalog
// entries, their handles and the schema snapshot are retained. A single
// request that alone exceeds the budgets still fails with the limit error.
func (c *Client) trimLookupCaches(n int) {
	cfg := c.effectiveOptions()
	need, limit := uint64(n), uint64(cfg.maxSymbols)
	var snapshotBytes uint64
	if c.snapshot != nil {
		snapshotBytes = c.snapshot.bytes
	}
	perLookup := uint64(0)
	if len(c.lookupSymbols) > 0 {
		perLookup = c.lookupBytes/uint64(len(c.lookupSymbols)) + 1
	}
	catalogCount := 0
	if c.snapshot != nil {
		catalogCount = len(c.snapshot.catalog)
	}
	dynamic := len(c.lookupSymbols) > 0 || len(c.symbols) > catalogCount
	full := uint64(len(c.symbols))+need > limit || uint64(len(c.lookupSymbols))+need > limit ||
		snapshotBytes+c.lookupBytes+need*perLookup > uint64(cfg.maxMetadata)
	if full && dynamic {
		c.evictLookups()
	}
}

// evictLookups drops every symbol-cache key that is not a catalog name, plus
// all direct-lookup records, releasing handles no retained key still shares.
func (c *Client) evictLookups() {
	isCatalog := func(name string) bool {
		return c.snapshot != nil && c.snapshot.symbols[name] != nil
	}
	retained := make(map[*SymbolEntry]bool)
	for name, entry := range c.symbols {
		if isCatalog(name) {
			retained[entry] = true
		}
	}
	var handles []uint32
	seen := make(map[uint32]bool)
	for name, entry := range c.symbols {
		if isCatalog(name) {
			continue
		}
		delete(c.symbols, name)
		if retained[entry] {
			continue // a case alias of a retained catalog entry
		}
		if entry.Handle != 0 && !seen[entry.Handle] {
			seen[entry.Handle] = true
			handles = append(handles, entry.Handle)
		}
		entry.Handle = 0
	}
	c.lookupSymbols = make(map[string]*symbolRecord)
	c.lookupBytes = 0
	c.releaseHandles(handles)
}

// releaseHandles is best effort and never reports failure: ADS rejections (for
// example already-invalid handles after an online change) are ignored. It runs
// only while at least half of the operation budget remains, so cleanup cannot
// consume the recovery that follows; skipped handles are dropped, not deferred.
func (c *Client) releaseHandles(handles []uint32) {
	budget := c.cfg.timeout
	if budget == 0 {
		budget = 5 * time.Second
	}
	usable := func() bool {
		return c.conn != nil && !c.conn.dead.Load() && time.Until(c.deadline) >= budget/2
	}
	limit := c.effectiveOptions().maxBatch
	for len(handles) > 0 && usable() {
		count := len(handles)
		if uint64(count) > uint64(limit) {
			count = int(limit)
		}
		// SumUp write request: 12-byte header per item plus its 4-byte handle.
		for count > 1 && (16+16*uint64(count) > uint64(c.conn.maxPayload) || 8+4*uint64(count) > uint64(c.conn.maxPayload)) {
			count /= 2
		}
		if count == 1 {
			if err := c.releaseHandleUnsafe(handles[0]); isConnectionError(err) {
				return
			}
			handles = handles[1:]
			continue
		}
		write := make([]byte, 16*count)
		for i, handle := range handles[:count] {
			binary.LittleEndian.PutUint32(write[i*12:], IndexGroupSymbolReleaseHandle)
			binary.LittleEndian.PutUint32(write[i*12+8:], 4)
			binary.LittleEndian.PutUint32(write[12*count+4*i:], handle)
		}
		_, err := c.readWriteData(IndexGroupSumUpWrite, uint32(count), uint32(4*count), write)
		if unsupportedService(err) {
			limit = 1 // Release individually while the budget allows.
			continue
		}
		if isConnectionError(err) {
			return
		}
		handles = handles[count:]
	}
}

// Close is idempotent and spends at most one budget on total handle cleanup.
// Active I/O is aborted immediately; a busy/desynchronized stream is not reused.
func (c *Client) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.closed, c.connected = true, false
	c.epoch++
	gate, conn, budget := c.gateLocked(), c.conn, c.budgetLocked()
	select {
	case <-gate:
		c.mu.Unlock()
		defer func() { gate <- struct{}{} }()
		c.deadline = time.Now().Add(budget)
		if conn != nil && !conn.dead.Load() {
			released := make(map[uint32]bool) // case aliases share one entry/handle
			for _, entry := range c.symbols {
				if entry.Handle != 0 && !released[entry.Handle] {
					released[entry.Handle] = true
					err := c.releaseHandleUnsafe(entry.Handle)
					if err != nil && errors.Is(err, ErrConnectionLost) {
						break
					}
					if !time.Now().Before(c.deadline) {
						break
					}
				}
			}
		}
		if conn != nil {
			conn.close()
		}
		c.invalidateCaches()
	default:
		// An operation owns caches; it will observe the closed stream. The next
		// successful reconnect resets all caches before permitting new operations.
		if conn != nil {
			conn.close()
		}
		c.mu.Unlock()
	}
}

func (c *Client) IsConnected() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected && !c.closed && c.conn != nil && !c.conn.dead.Load()
}

func (c *Client) SetDisconnected() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connected = false
	if c.conn != nil {
		c.conn.close()
	}
}

func (c *Client) connErrorIfDown() error {
	if !c.IsConnected() {
		return fmt.Errorf("read incomplete: %w", ErrConnectionLost)
	}
	return nil
}

func isConnectionError(err error) bool { return errors.Is(err, ErrConnectionLost) }

func (c *Client) ConnectionDetails() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return fmt.Sprintf("Target: %s:%d, Local: %s:%d", c.targetNetId, c.targetPort, c.localNetId, c.localPort)
}

func (c *Client) ConnectionMode() string {
	if c == nil {
		return "Not connected"
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.connected && !c.closed && c.conn != nil && !c.conn.dead.Load() {
		return fmt.Sprintf("ADS Connected (%s)", c.deviceInfo)
	}
	return "Disconnected"
}

func (c *Client) GetDeviceInfo() (*DeviceInfo, error) {
	_, done, err := c.begin(false)
	if err != nil {
		return nil, err
	}
	defer done()
	c.mu.Lock()
	info := c.deviceInfo
	c.mu.Unlock()
	if info != nil {
		copy := *info
		return &copy, nil
	}
	return c.readDeviceInfo()
}

func (c *Client) Identity() (*DeviceInfo, error) { return c.GetDeviceInfo() }

// ADS states reported by ReadState (AdsState in the Beckhoff ADS specification).
const (
	AdsStateInvalid  uint16 = 0
	AdsStateIdle     uint16 = 1
	AdsStateReset    uint16 = 2
	AdsStateInit     uint16 = 3
	AdsStateStart    uint16 = 4
	AdsStateRun      uint16 = 5
	AdsStateStop     uint16 = 6
	AdsStateConfig   uint16 = 15
	AdsStateReconfig uint16 = 16
)

// ReadState performs ADS ReadState (command 4) on the target AMS port and
// returns the ADS state (for example AdsStateRun) and the device state. It is a
// cheap, read-only exchange bounded by the operation timeout and is suitable as
// a keepalive. A transport failure closes the stream and wraps
// ErrConnectionLost; a device rejection is returned as *AdsError and keeps the
// stream usable. It is never retried.
func (c *Client) ReadState() (adsState, deviceState uint16, err error) {
	_, done, err := c.begin(false)
	if err != nil {
		return 0, 0, err
	}
	defer done()
	resp, err := c.exchange(CmdReadState, nil)
	if err != nil {
		return 0, 0, err
	}
	rest, err := commandResult(resp)
	var device *AdsError
	if err != nil {
		if errors.As(err, &device) {
			return 0, 0, err
		}
		return 0, 0, c.conn.fail(err)
	}
	if len(rest) != 4 {
		return 0, 0, c.conn.fail(fmt.Errorf("ADS ReadState size %d, want 4", len(rest)))
	}
	return binary.LittleEndian.Uint16(rest[:2]), binary.LittleEndian.Uint16(rest[2:4]), nil
}

// Internal command helpers require operation gate ownership.
func (c *Client) exchange(command uint16, data []byte) ([]byte, error) {
	if c.conn == nil {
		return nil, fmt.Errorf("%w: %w", ErrConnectionLost, net.ErrClosed)
	}
	return c.conn.sendRequestUntil(c.targetNetId, c.targetPort, command, data, c.deadline)
}

func (c *Client) checkedReadResponse(resp []byte, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	payload, err := readCommandData(resp)
	if err != nil {
		var device *AdsError
		if !errors.As(err, &device) {
			return nil, c.conn.fail(err)
		}
	}
	return payload, err
}

func (c *Client) readData(group, offset, size uint32) ([]byte, error) {
	if uint64(size)+8 > uint64(c.conn.maxPayload) {
		return nil, fmt.Errorf("ADS read size %d exceeds payload limit %d", size, c.conn.maxPayload)
	}
	req := make([]byte, 12)
	binary.LittleEndian.PutUint32(req[:4], group)
	binary.LittleEndian.PutUint32(req[4:8], offset)
	binary.LittleEndian.PutUint32(req[8:12], size)
	return c.checkedReadResponse(c.exchange(CmdRead, req))
}

// readUpload reads one complete metadata upload whose exact size the PLC
// advertised beforehand and loadSchema already checked against the aggregate
// metadata budget. That budget, not the per-command value payload budget, bounds
// the response; the request itself is still a single complete bounded read.
func (c *Client) readUpload(group, size uint32) ([]byte, error) {
	if c.conn == nil {
		return nil, fmt.Errorf("%w: %w", ErrConnectionLost, net.ErrClosed)
	}
	limit := uint64(c.conn.maxPayload)
	if need := uint64(size) + 8; need > limit {
		if need > uint64(^uint32(0)-38) || need > uint64(c.effectiveOptions().maxMetadata)+8 {
			return nil, fmt.Errorf("ADS upload size %d exceeds metadata limit", size)
		}
		limit = need
	}
	req := make([]byte, 12)
	binary.LittleEndian.PutUint32(req[:4], group)
	binary.LittleEndian.PutUint32(req[8:12], size)
	return c.checkedReadResponse(c.conn.sendRequestLimit(c.targetNetId, c.targetPort, CmdRead, req, c.deadline, uint32(limit)))
}

func (c *Client) readWriteData(group, offset, size uint32, write []byte) ([]byte, error) {
	if uint64(size)+8 > uint64(c.conn.maxPayload) || uint64(len(write))+16 > uint64(c.conn.maxPayload) {
		return nil, fmt.Errorf("ADS ReadWrite exceeds payload limit %d", c.conn.maxPayload)
	}
	req := make([]byte, 16+len(write))
	binary.LittleEndian.PutUint32(req[:4], group)
	binary.LittleEndian.PutUint32(req[4:8], offset)
	binary.LittleEndian.PutUint32(req[8:12], size)
	binary.LittleEndian.PutUint32(req[12:16], uint32(len(write)))
	copy(req[16:], write)
	return c.checkedReadResponse(c.exchange(CmdReadWrite, req))
}

func (c *Client) writeData(group, offset uint32, data []byte) error {
	if uint64(len(data))+12 > uint64(c.conn.maxPayload) {
		return fmt.Errorf("ADS write exceeds payload limit %d", c.conn.maxPayload)
	}
	req := make([]byte, 12+len(data))
	binary.LittleEndian.PutUint32(req[:4], group)
	binary.LittleEndian.PutUint32(req[4:8], offset)
	binary.LittleEndian.PutUint32(req[8:12], uint32(len(data)))
	copy(req[12:], data)
	resp, err := c.exchange(CmdWrite, req)
	if err != nil {
		return err
	}
	rest, err := commandResult(resp)
	var device *AdsError
	if err != nil && !errors.As(err, &device) {
		return c.conn.fail(err)
	}
	if err == nil && len(rest) != 0 {
		return c.conn.fail(fmt.Errorf("ADS write response trailing data"))
	}
	return err
}

func (c *Client) readDeviceInfo() (*DeviceInfo, error) {
	resp, err := c.exchange(CmdReadDeviceInfo, nil)
	if err != nil {
		return nil, err
	}
	info, err := decodeDeviceInfo(resp)
	var device *AdsError
	if err != nil && !errors.As(err, &device) {
		return nil, c.conn.fail(err)
	}
	return info, err
}

func symbolNameBytes(name string) ([]byte, error) {
	if name == "" || len(name) > 0xffff || strings.ContainsRune(name, 0) {
		return nil, fmt.Errorf("invalid empty/NUL symbol name")
	}
	return append([]byte(name), 0), nil
}

// TwinCAT symbol identifiers are ASCII and case-insensitive. Only ASCII letters
// fold, so cache identity never depends on Unicode case-folding rules.
func foldSymbolName(name string) string {
	for i := 0; i < len(name); i++ {
		if name[i] >= 'a' && name[i] <= 'z' {
			folded := []byte(name)
			for j := i; j < len(folded); j++ {
				if folded[j] >= 'a' && folded[j] <= 'z' {
					folded[j] -= 'a' - 'A'
				}
			}
			return string(folded)
		}
	}
	return name
}

func sameSymbolName(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if x >= 'a' && x <= 'z' {
			x -= 'a' - 'A'
		}
		if y >= 'a' && y <= 'z' {
			y -= 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

func (c *Client) getSymbolInfo(name string) (*TagInfo, error) {
	cfg := c.effectiveOptions()
	if uint64(len(c.lookupSymbols)) >= uint64(cfg.maxSymbols) {
		return nil, fmt.Errorf("symbol lookup count limit exceeded")
	}
	bytes, err := symbolNameBytes(name)
	if err != nil {
		return nil, err
	}
	size := uint32(0xffff)
	if c.conn.maxPayload-8 < size {
		size = c.conn.maxPayload - 8
	}
	payload, err := c.readWriteData(IndexGroupSymbolInfoByNameEx, 0, size, bytes)
	if err != nil {
		return nil, err
	}
	retained := c.lookupBytes
	if c.snapshot != nil {
		retained += c.snapshot.bytes
	}
	if uint64(len(payload)) > uint64(cfg.maxMetadata) || retained > uint64(cfg.maxMetadata)-uint64(len(payload)) {
		return nil, fmt.Errorf("symbol lookup aggregate metadata limit exceeded")
	}
	budget := parseBudget{remaining: uint64(c.effectiveOptions().maxElements), maxDepth: c.effectiveOptions().maxDepth, deadline: c.deadline}
	record, err := parseSymbolRecord(payload, &budget)
	strictErr := err
	if err != nil {
		record, err = parseSymbolRecordFields(payload, &budget, true)
	}
	if err != nil {
		return nil, err
	}
	// TwinCAT resolves names case-insensitively and reports its canonical
	// spelling; the record keeps that canonical name for metadata/access joins.
	if !sameSymbolName(record.info.Name, name) || strictErr != nil {
		canonical, err := c.validateBitLookup(name, record, &budget)
		if err != nil {
			return nil, err
		}
		record.info.Name = canonical // Handles always address the requested member.
	}
	if c.lookupSymbols == nil {
		c.lookupSymbols = make(map[string]*symbolRecord)
	}
	c.lookupSymbols[record.info.Name] = record
	c.lookupBytes += uint64(len(payload))
	return &record.info, nil
}

// getSymbolEntry resolves exact names first, then case-insensitively through the
// loaded catalog, then through the PLC lookup. A differently cased request is
// cached as an alias of the canonical entry, so both share one handle.
func (c *Client) getSymbolEntry(name string) (*SymbolEntry, error) {
	if entry := c.symbols[name]; entry != nil {
		return entry, nil
	}
	if c.snapshot != nil {
		if canonical := c.snapshot.folded[foldSymbolName(name)]; canonical != "" {
			if entry := c.symbols[canonical]; entry != nil {
				c.aliasSymbol(name, entry)
				return entry, nil
			}
		}
	}
	if uint64(len(c.symbols)) >= uint64(c.effectiveOptions().maxSymbols) {
		return nil, fmt.Errorf("symbol/handle cache count limit exceeded")
	}
	info, err := c.getSymbolInfo(name)
	if err != nil {
		return nil, err
	}
	if c.symbols == nil {
		c.symbols = make(map[string]*SymbolEntry)
	}
	entry := c.symbols[info.Name]
	if entry == nil {
		entry = &SymbolEntry{Info: *info}
		c.symbols[info.Name] = entry
	}
	c.aliasSymbol(name, entry)
	return entry, nil
}

// aliasSymbol caches a requested spelling when the count budget allows; an
// uncached alias is only resolved again, never an error.
func (c *Client) aliasSymbol(name string, entry *SymbolEntry) {
	if c.symbols[name] == nil && uint64(len(c.symbols)) < uint64(c.effectiveOptions().maxSymbols) {
		c.symbols[name] = entry
	}
}

func (c *Client) acquireHandle(name string) (uint32, error) {
	bytes, err := symbolNameBytes(name)
	if err != nil {
		return 0, err
	}
	data, err := c.readWriteData(IndexGroupSymbolHandleByName, 0, 4, bytes)
	if err != nil {
		return 0, err
	}
	if len(data) != 4 {
		return 0, c.conn.fail(fmt.Errorf("ADS handle size %d, want 4", len(data)))
	}
	handle := binary.LittleEndian.Uint32(data)
	if handle == 0 {
		return 0, fmt.Errorf("ADS returned zero symbol handle")
	}
	return handle, nil
}

func (c *Client) ensureHandle(name string, entry *SymbolEntry) error {
	if entry.Handle != 0 {
		return nil
	}
	handle, err := c.acquireHandle(name)
	if err != nil {
		return err
	}
	entry.Handle = handle
	return nil
}

func (c *Client) releaseHandleUnsafe(handle uint32) error {
	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data, handle)
	return c.writeData(IndexGroupSymbolReleaseHandle, 0, data)
}

func rawValue(name string, entry *SymbolEntry, data []byte) *TagValue {
	count := 1
	size := TypeSize(entry.Info.TypeCode)
	if size > 0 && len(data) > size {
		count = len(data) / size
	}
	if size == 0 && strings.Contains(strings.ToUpper(entry.Info.TypeName), "ARRAY") {
		count = parseArrayCountFromTypeName(entry.Info.TypeName)
	}
	return &TagValue{Name: name, DataType: entry.Info.TypeCode, Bytes: data, Count: count}
}

func (c *Client) readSymbol(name string) (*TagValue, error) {
	entry, err := c.getSymbolEntry(name)
	if err != nil {
		return nil, err
	}
	if err := c.ensureHandle(name, entry); err != nil {
		return nil, err
	}
	data, err := c.readData(IndexGroupSymbolValueByHandle, entry.Handle, entry.Info.Size)
	if err != nil {
		return nil, err
	}
	if uint64(len(data)) != uint64(entry.Info.Size) {
		return nil, fmt.Errorf("symbol %s size %d, want %d", name, len(data), entry.Info.Size)
	}
	return rawValue(name, entry, data), nil
}

// readValues owns the current immutable snapshot until each group is validated.
// Caller holds gate and owns the operation's single retry and deadline.
func (c *Client) readValues(names []string) ([]*TagValue, []*SymbolEntry, error) {
	if len(names) == 0 {
		return nil, nil, nil
	}
	results := make([]*TagValue, len(names))

	entries := make([]*SymbolEntry, len(names))
	var connectionErr error
	for i, name := range names {
		if connectionErr != nil {
			results[i] = &TagValue{Name: name, Error: connectionErr}
			continue
		}
		entry, err := c.getSymbolEntry(name)
		if err == nil {
			err = c.ensureHandle(name, entry)
		}
		if err != nil {
			results[i] = &TagValue{Name: name, Error: err}
			if isConnectionError(err) {
				connectionErr = err
			}
			continue
		}
		entries[i] = entry
	}
	if connectionErr != nil { // Completed lookups are not completed value reads.
		for i, v := range results {
			if v == nil {
				results[i] = &TagValue{Name: names[i], Error: connectionErr}
			}
		}
		return results, entries, connectionErr
	}
	limit := c.cfg.maxBatch
	if limit == 0 {
		limit = 500
	}
	for start := 0; start < len(names); {
		if entries[start] == nil {
			start++
			continue
		}
		end := start
		responseBytes := uint64(8)
		for end < len(names) && entries[end] != nil && uint64(end-start) < uint64(limit) {
			next := responseBytes + 4 + uint64(entries[end].Info.Size)
			if next > uint64(c.conn.maxPayload) || 16+12*uint64(end-start+1) > uint64(c.conn.maxPayload) {
				break
			}
			responseBytes = next
			end++
		}
		if end == start {
			if uint64(entries[start].Info.Size)+8 <= uint64(c.conn.maxPayload) {
				end = start + 1 // A plain Read needs less space than SumUp.
			} else {
				results[start] = &TagValue{Name: names[start], Error: fmt.Errorf("symbol exceeds ADS batch payload budget")}
				start++
				continue
			}
		}
		before := c.symbolVersion
		individualVersionChecks := false
		err := c.readBatch(names[start:end], entries[start:end], results[start:end])
		if err != nil {
			var device *AdsError
			if errors.As(err, &device) && (device.Code == ErrDeviceSrvNotSupp || device.Code == ErrDeviceInvalidGrp) {
				individualVersionChecks = true
				for i := start; i < end; i++ {
					individualBefore := c.symbolVersion
					v, individualErr := c.readSymbol(names[i])
					if individualErr == nil {
						_, individualErr = c.checkVersion()
						if individualErr != nil {
							v.Error = individualErr
							results[i] = v
							return results, entries, individualErr
						}
						if individualErr == nil && c.versionCapability == 1 && individualBefore != c.symbolVersion {
							return results, entries, &AdsError{Code: ErrDeviceSymbolVersionInvalid}
						}
					}
					if individualErr != nil {
						if v != nil {
							v.Error = individualErr
							results[i] = v
						} else {
							results[i] = &TagValue{Name: names[i], Error: individualErr}
						}
					} else {
						results[i] = v
					}
					if isConnectionError(individualErr) {
						err = individualErr
						break
					}
				}
				if !isConnectionError(err) {
					err = nil
				}
			} else {
				for i := start; i < end; i++ {
					results[i] = &TagValue{Name: names[i], Error: err}
				}
			}
		}
		if err != nil && isConnectionError(err) {
			for i := start; i < len(results); i++ {
				if results[i] == nil {
					results[i] = &TagValue{Name: names[i], Error: err}
				}
			}
			return results, entries, err
		}
		if !individualVersionChecks && !isConnectionError(err) {
			_, versionErr := c.checkVersion()
			if versionErr != nil {
				for i := start; i < end; i++ {
					if results[i] != nil && results[i].Error == nil {
						results[i].Error = versionErr
					}
				}
				for i := end; i < len(results); i++ {
					if results[i] == nil {
						results[i] = &TagValue{Name: names[i], Error: versionErr}
					}
				}
				return results, entries, versionErr
			}
			if c.versionCapability == 1 && before != c.symbolVersion {
				return results, entries, &AdsError{Code: ErrDeviceSymbolVersionInvalid}
			}
		}
		start = end
	}
	return results, entries, nil
}

func (c *Client) readBatch(names []string, entries []*SymbolEntry, results []*TagValue) error {
	count := uint64(len(entries))
	if len(names) != len(entries) || len(results) != len(entries) {
		return fmt.Errorf("batch names/entries count mismatch")
	}
	if count == 0 {
		return nil
	}
	for _, entry := range entries {
		if entry == nil {
			return fmt.Errorf("nil ADS batch entry")
		}
	}
	if len(entries) == 1 {
		v, err := c.readSymbol(names[0])
		if err != nil {
			return err
		}
		results[0] = v
		return nil
	}
	if count > uint64(^uint32(0)) || 16+12*count > uint64(c.conn.maxPayload) || 4*count+8 > uint64(c.conn.maxPayload) {
		return fmt.Errorf("ADS batch request count/byte limit exceeded")
	}
	readSize := 4 * uint64(len(entries))
	write := make([]byte, 12*len(entries))
	for i, entry := range entries {
		readSize += uint64(entry.Info.Size)
		if readSize+8 > uint64(c.conn.maxPayload) {
			return fmt.Errorf("ADS batch response exceeds budget")
		}
		binary.LittleEndian.PutUint32(write[i*12:i*12+4], IndexGroupSymbolValueByHandle)
		binary.LittleEndian.PutUint32(write[i*12+4:i*12+8], entry.Handle)
		binary.LittleEndian.PutUint32(write[i*12+8:i*12+12], entry.Info.Size)
	}
	data, err := c.readWriteData(IndexGroupSumUpRead, uint32(len(entries)), uint32(readSize), write)
	if err != nil {
		return err
	}
	if uint64(len(data)) != readSize {
		return c.conn.fail(fmt.Errorf("ADS SumUp size %d, want %d", len(data), readSize))
	}
	offset := 4 * len(entries)
	// F080 returns each requested-size data slot, including failed slots. F084
	// uses returned lengths instead; never apply its cursor rule to F080.
	for i, entry := range entries {
		size := int(entry.Info.Size)
		code := binary.LittleEndian.Uint32(data[i*4 : i*4+4])
		if code != 0 {
			results[i] = &TagValue{Name: names[i], Error: &AdsError{Code: code}}
		} else {
			bytes := append([]byte(nil), data[offset:offset+size]...)
			results[i] = rawValue(names[i], entry, bytes)
		}
		offset += size
	}
	return nil
}

// Write validates a complete schema-driven value before sending a value write.
// It never performs implicit record read/modify/write or replays a value write.
func (c *Client) Write(name string, value interface{}) error {
	_, done, err := c.begin(false)
	if err != nil {
		return err
	}
	defer done()
	for attempt := 0; attempt < 2; attempt++ {
		err = c.loadSchemaForIO()
		if staleSymbolError(err) && attempt == 0 {
			c.invalidateLive()
			continue
		}
		if err != nil {
			return err
		}
		break
	}
	c.trimLookupCaches(1)
	before := c.symbolVersion
	entry, err := c.getSymbolEntry(name)
	if err != nil {
		return err
	}
	if !entry.Info.IsWritable() {
		return fmt.Errorf("symbol %q is read-only", name)
	}
	paths, err := c.readOnlyPaths(entry.Info.Name) // canonical spelling of the catalog index
	if err != nil {
		return err
	}
	if len(paths) != 0 {
		return fmt.Errorf("symbol %q contains read-only storage %q", name, paths[0])
	}
	resolver := c.currentResolver()
	schema := c.schemaFor(entry.Info, resolver, c.snapshot)
	cfg := c.effectiveOptions()
	cfg.deadline = c.deadline
	data, err := encodeValue(schema, value, cfg)
	if err != nil {
		return fmt.Errorf("encode %s: %w", name, err)
	}
	cachedHandle := entry.Handle != 0
	if err := c.ensureHandle(name, entry); err != nil {
		if staleSymbolError(err) {
			c.invalidateLive()
		}
		return err
	}
	if _, err := c.checkVersion(); err != nil {
		return err
	}
	if c.versionCapability == 1 && before != c.symbolVersion {
		return &AdsError{Code: ErrDeviceSymbolVersionInvalid}
	}
	err = c.writeData(IndexGroupSymbolValueByHandle, entry.Handle, data)
	// A rejected write was not performed, but it is still never replayed: the
	// next operation re-resolves the stale handle and metadata instead.
	if staleSymbolError(err) || (cachedHandle && staleHandleNotFound(err)) {
		c.invalidateLive()
	}
	if isConnectionError(err) {
		return fmt.Errorf("write outcome uncertain; value may have been sent: %w", err)
	}
	return err
}

// AllTags returns the complete validated advertised catalog, sorted by name.
// Direct symbol lookups do not alter membership.
func (c *Client) AllTags() ([]TagInfo, error) {
	_, done, err := c.begin(false)
	if err != nil {
		return nil, err
	}
	defer done()
	return c.allTags()
}

func (c *Client) allTags() ([]TagInfo, error) {
	tags, _, err := c.catalogResult(false)
	return tags, err
}

// Describe returns a deep, caller-owned description of the exact symbol path.
// Ordinary reads and writes resolve schemas automatically; inspection is optional.
func (c *Client) Describe(name string) (*metadata.Symbol, error) {
	_, done, err := c.begin(false)
	if err != nil {
		return nil, err
	}
	defer done()
	for attempt := 0; attempt < 2; attempt++ {
		err := c.loadSchemaForIO()
		if err != nil {
			if staleSymbolError(err) && attempt == 0 {
				continue
			}
			return nil, err
		}
		c.trimLookupCaches(1)
		entry, err := c.getSymbolEntry(name)
		if err != nil {
			return nil, err
		}
		resolver := c.currentResolver()
		schema := c.schemaFor(entry.Info, resolver, c.snapshot)
		budget := parseBudget{remaining: uint64(c.effectiveOptions().maxElements), maxDepth: c.effectiveOptions().maxDepth, deadline: c.deadline}
		typeOf, err := describeType(schema, &budget, 1)
		if err != nil {
			return nil, err
		}
		paths, err := c.readOnlyPaths(entry.Info.Name)
		if err != nil {
			return nil, err
		}
		rootReadOnly, err := describeAccess(&typeOf, entry.Info.Name, paths, &budget)
		if err != nil {
			return nil, err
		}
		before := c.symbolVersion
		_, err = c.checkVersion()
		if err != nil {
			return nil, err
		}
		if c.versionCapability == 1 && before != c.symbolVersion {
			if attempt == 0 {
				continue
			}
			return nil, &AdsError{Code: ErrDeviceSymbolVersionInvalid}
		}
		return &metadata.Symbol{Name: name, Type: typeOf, Readable: entry.Info.IsReadable(), Writable: entry.Info.IsWritable() && !rootReadOnly}, nil
	}
	panic("unreachable")
}

// Programs projects published top-level namespaces, loading the catalog as needed.
func (c *Client) Programs() ([]string, error) {
	tags, err := c.AllTags()
	if err != nil {
		return nil, err
	}
	prefixes := make(map[string]bool)
	for _, tag := range tags {
		if i := strings.IndexByte(tag.Name, '.'); i > 0 {
			prefixes[tag.Name[:i]] = true
		}
	}
	result := make([]string, 0, len(prefixes))
	for prefix := range prefixes {
		result = append(result, prefix)
	}
	sort.Strings(result)
	return result, nil
}

// encodeStringArray is retained for protocol helper compatibility inside ads.
func encodeStringArray(values []string, size int, wide bool) ([]byte, error) {
	if size <= 0 || len(values) > int(^uint(0)>>1)/size {
		return nil, fmt.Errorf("invalid string array size")
	}
	result := make([]byte, len(values)*size)
	code := TypeString
	if wide {
		code = TypeWString
	}
	for i, value := range values {
		bytes, err := EncodeValueWithType(value, code)
		if err != nil {
			return nil, err
		}
		if len(bytes) > size {
			return nil, fmt.Errorf("string exceeds element capacity")
		}
		copy(result[i*size:(i+1)*size], bytes)
	}
	return result, nil
}
