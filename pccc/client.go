package pccc

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/yatesdr/plcio/eip"
)

// ErrConnectionLost indicates the link to the PLC dropped during a read. Read
// folds per-address failures into each TagValue.Error and otherwise returns a
// nil top-level error, so callers cannot tell a lost link from a single bad
// address. When the transport has dropped, Read surfaces this error at the top
// level — alongside any partial results — so callers can reconnect. Detect it
// with errors.Is(err, ErrConnectionLost).
var ErrConnectionLost = errors.New("pccc: connection lost during read")

// connErrorIfDown returns a wrapped ErrConnectionLost when the underlying
// transport has dropped, otherwise nil.
func (c *Client) connErrorIfDown() error {
	if !c.IsConnected() {
		return fmt.Errorf("read incomplete: %w", ErrConnectionLost)
	}
	return nil
}

// Client is a high-level wrapper for PCCC communication with SLC500, PLC-5,
// and MicroLogix processors. It provides type-safe read/write operations
// and automatic value conversion.
type Client struct {
	plc *PLC
}

// TagValue holds a decoded tag value from a PCCC read operation.
type TagValue struct {
	Name     string      // Address as requested (e.g., "N7:0")
	FileType byte        // PCCC file type code
	Value    interface{} // Decoded Go value
	Bytes    []byte      // Raw bytes from PLC
	Error    error       // Per-tag error (nil on success)
}

// options holds configuration for Connect.
type options struct {
	timeout   time.Duration
	routePath []byte
	plcType   PLCType
	vendorID  uint16
	serialNum uint32
}

// Option is a functional option for Connect.
type Option func(*options)

// WithTimeout sets the connection and operation timeout.
func WithTimeout(d time.Duration) Option {
	return func(o *options) {
		o.timeout = d
	}
}

// WithRoutePath configures explicit CIP routing for the PLC.
// Use this when connecting through a gateway (e.g., ControlLogix with 1756-DHRIO).
func WithRoutePath(path []byte) Option {
	return func(o *options) {
		o.routePath = path
	}
}

// WithPLC5 configures the client for PLC-5 processors.
func WithPLC5() Option {
	return func(o *options) {
		o.plcType = TypePLC5
	}
}

// WithMicroLogix configures the client for MicroLogix processors.
func WithMicroLogix() Option {
	return func(o *options) {
		o.plcType = TypeMicroLogix
	}
}

// Connect establishes a connection to an SLC500/PLC-5/MicroLogix processor.
// By default, assumes SLC500. Use WithPLC5() or WithMicroLogix() for other types.
//
// Example:
//
//	client, err := pccc.Connect("192.168.1.100")
//	client, err := pccc.Connect("192.168.1.100", pccc.WithPLC5())
//	client, err := pccc.Connect("192.168.1.100", pccc.WithTimeout(10*time.Second))
func Connect(address string, opts ...Option) (*Client, error) {
	cfg := &options{
		vendorID:  0x0001, // Default vendor ID
		serialNum: 0x12345678,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	debugLog("Connect %s: plcType=%s", address, cfg.plcType)

	if address == "" {
		return nil, fmt.Errorf("Connect: empty address")
	}

	// Create EIP client and connect
	eipClient := eip.NewEipClient(address)
	if cfg.timeout > 0 {
		eipClient.SetTimeout(cfg.timeout)
	}

	if err := eipClient.Connect(); err != nil {
		return nil, fmt.Errorf("Connect: %w", err)
	}

	debugLog("Connect %s: EIP session established, session=0x%08X", address, eipClient.GetSession())

	plc := &PLC{
		IpAddress:  address,
		Connection: eipClient,
		RoutePath:  cfg.routePath,
		PLCType:    cfg.plcType,
		vendorID:   cfg.vendorID,
		serialNum:  cfg.serialNum,
	}

	return &Client{plc: plc}, nil
}

// Close releases the connection.
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

// IsConnected returns true if the EIP session is active.
func (c *Client) IsConnected() bool {
	return c.plc != nil && c.plc.IsConnected()
}

// ConnectionMode returns a description of the connection mode.
func (c *Client) ConnectionMode() string {
	if c == nil || c.plc == nil {
		return "Not connected"
	}
	if len(c.plc.RoutePath) > 0 {
		return fmt.Sprintf("Unconnected (routed, %s)", c.plc.PLCType)
	}
	return fmt.Sprintf("Unconnected (direct, %s)", c.plc.PLCType)
}

// Keepalive sends a NOP to maintain the TCP connection.
func (c *Client) Keepalive() error {
	if c == nil || c.plc == nil {
		return nil
	}
	return c.plc.Keepalive()
}

// Read reads one or more data table addresses and returns their decoded values.
// Each result includes its own error status (nil on success).
//
// Example:
//
//	values, err := client.Read("N7:0", "F8:5", "T4:0.ACC", "B3:0/5")
func (c *Client) Read(addresses ...string) ([]*TagValue, error) {
	if c == nil || c.plc == nil {
		return nil, fmt.Errorf("Read: nil client")
	}
	if len(addresses) == 0 {
		return nil, nil
	}

	results := make([]*TagValue, 0, len(addresses))

	for _, addrStr := range addresses {
		addr, err := c.ParseAddress(addrStr)
		if err != nil {
			results = append(results, &TagValue{
				Name:  addrStr,
				Error: fmt.Errorf("invalid address: %w", err),
			})
			continue
		}

		tag, err := c.plc.ReadAddress(addr)
		if err != nil {
			results = append(results, &TagValue{
				Name:  addrStr,
				Error: err,
			})
			continue
		}

		// Decode the raw bytes into a Go value
		value := decodeValue(addr, tag.Bytes)

		results = append(results, &TagValue{
			Name:     addrStr,
			FileType: tag.FileType,
			Value:    value,
			Bytes:    tag.Bytes,
		})
	}

	return results, c.connErrorIfDown()
}

// ParseAddress parses addr using this client's processor family (PLC-5 I/O
// addresses are octal; see ParseAddressFor).
func (c *Client) ParseAddress(addr string) (*FileAddress, error) {
	plcType := TypeSLC500
	if c != nil && c.plc != nil {
		plcType = c.plc.PLCType
	}
	return ParseAddressFor(addr, plcType)
}

// Write writes a Go value to a data table address.
// The value is automatically converted to the appropriate wire format.
//
// Example:
//
//	err := client.Write("N7:0", int16(42))
//	err := client.Write("F8:0", float32(3.14))
//	err := client.Write("B3:0/5", true)
func (c *Client) Write(address string, value interface{}) error {
	if c == nil || c.plc == nil {
		return fmt.Errorf("Write: nil client")
	}

	addr, err := c.ParseAddress(address)
	if err != nil {
		return fmt.Errorf("Write: invalid address %q: %w", address, err)
	}

	// Handle bit writes specially
	if addr.BitNumber >= 0 {
		return c.writeBit(addr, value)
	}

	// Encode the value to bytes
	data, err := encodeValue(addr, value)
	if err != nil {
		return fmt.Errorf("Write %s: %w", address, err)
	}

	return c.plc.WriteAddress(addr, data)
}

// writeBit sets or clears a single bit of a 16-bit word. The processor
// changes only that bit; nothing is read first, so bits the processor or
// another client changes concurrently (counter CU/CD, one-shot storage, ...)
// are never reverted by this write.
//
// SLC 500 and MicroLogix: Protected Typed Logical Write with Mask (FNC 0xAB)
// with a one-bit mask (pycomm3 writeable_value; libplctag
// slc_tag_write_bit_start).
//
// PLC-5: Read-Modify-Write (FNC 0x26) with AND mask ~bit (clear) or 0xFFFF
// and OR mask bit (set) or 0 (1770-6.5.16 p. 7-20; libplctag
// plc5_tag_write_bit_start), applied by the processor to the word.
//
// Both are 16 bits wide (libplctag: "the mask is only 16 bits"), so bit
// writes are only accepted for word files (O, I, S, B, N, A) and T/C/R
// control words; L (long), F (float) and other files are rejected.
func (c *Client) writeBit(addr *FileAddress, value interface{}) error {
	// Determine the target bit value
	var bitVal bool
	switch v := value.(type) {
	case bool:
		bitVal = v
	case int:
		bitVal = v != 0
	case int16:
		bitVal = v != 0
	case int32:
		bitVal = v != 0
	case int64:
		bitVal = v != 0
	case uint16:
		bitVal = v != 0
	case float32:
		bitVal = v != 0
	case float64:
		bitVal = v != 0
	default:
		return fmt.Errorf("cannot convert %T to bit value", value)
	}

	switch addr.FileType {
	case FileTypeOutput, FileTypeInput, FileTypeStatus, FileTypeBinary, FileTypeInteger, FileTypeASCII,
		FileTypeTimer, FileTypeCounter, FileTypeControl:
	case FileTypeLong:
		return fmt.Errorf("bit write to %s: bit writes to L (long) files are not supported (masked write is 16 bits wide); write the whole element instead", addr.RawAddress)
	case FileTypeFloat:
		return fmt.Errorf("bit write to %s: bit writes to F (float) files are not supported", addr.RawAddress)
	default:
		return fmt.Errorf("bit write to %s: bit writes to %s files are not supported", addr.RawAddress, FileTypeName(addr.FileType))
	}
	if addr.BitNumber > 15 {
		return fmt.Errorf("bit write to %s: bit number %d out of range (0-15)", addr.RawAddress, addr.BitNumber)
	}

	// The whole 16-bit word (or T/C/R control word) that holds the bit.
	wordAddr := &FileAddress{
		FileType:   addr.FileType,
		FileNumber: addr.FileNumber,
		Element:    addr.Element,
		SubElement: addr.SubElement,
		BitNumber:  -1,
		TypeLetter: addr.TypeLetter,
		RawAddress: addr.RawAddress,
		// T/C/R bits live in a sub-element word (the control word): keep
		// the sub-element level so a PLC-5 address points at that word.
		HasSubElement: addr.HasSubElement || IsComplexType(addr.FileType),
	}
	mask := uint16(1) << uint(addr.BitNumber)

	if c.plc.PLCType == TypePLC5 {
		andMask, orMask := uint16(0xFFFF), uint16(0)
		if bitVal {
			orMask = mask
		} else {
			andMask = ^mask
		}
		return c.plc.readModifyWrite(wordAddr, andMask, orMask)
	}

	var word uint16
	if bitVal {
		word = mask
	}
	return c.plc.writeMasked(wordAddr,
		binary.LittleEndian.AppendUint16(nil, mask),
		binary.LittleEndian.AppendUint16(nil, word))
}

// DecodeValue converts raw PLC bytes to a Go value based on the address type.
// This is exported for use by the driver layer when slicing bulk read results.
func DecodeValue(addr *FileAddress, data []byte) interface{} {
	return decodeValue(addr, data)
}

// decodeValue converts raw PLC bytes to a Go value based on the address type.
func decodeValue(addr *FileAddress, data []byte) interface{} {
	if len(data) == 0 {
		return nil
	}

	// For bit addresses, extract the specific bit from the word
	if addr.BitNumber >= 0 && len(data) >= 2 {
		word := binary.LittleEndian.Uint16(data[:2])
		return (word>>uint(addr.BitNumber))&1 != 0
	}

	switch addr.FileType {
	case FileTypeInteger, FileTypeOutput, FileTypeInput, FileTypeStatus, FileTypeBinary, FileTypeASCII:
		// 16-bit signed integer
		if len(data) < 2 {
			return data
		}
		return int16(binary.LittleEndian.Uint16(data[:2]))

	case FileTypeFloat:
		// 32-bit IEEE 754 float
		if len(data) < 4 {
			return data
		}
		bits := binary.LittleEndian.Uint32(data[:4])
		return float32(math.Float32frombits(bits))

	case FileTypeLong:
		// 32-bit signed integer
		if len(data) < 4 {
			return data
		}
		return int32(binary.LittleEndian.Uint32(data[:4]))

	case FileTypeTimer, FileTypeCounter, FileTypeControl:
		// Complex type — decode depends on sub-element
		if addr.SubElement > 0 && len(data) >= 2 {
			// Specific sub-element: return as 16-bit integer
			return int16(binary.LittleEndian.Uint16(data[:2]))
		}
		// Full element: return as map of sub-elements
		return decodeComplexElement(addr.FileType, data)

	case FileTypeString:
		// SLC string: 2-byte LEN + 82 chars stored byte-swapped within
		// each 16-bit word (see swapStringBytes).
		if len(data) < 2 {
			return data
		}
		chars := swapStringBytes(data[2:])
		strLen := int(binary.LittleEndian.Uint16(data[:2]))
		if strLen > maxStringLen {
			strLen = maxStringLen
		}
		if strLen > len(chars) {
			strLen = len(chars)
		}
		return string(chars[:strLen])

	default:
		return data
	}
}

// decodeComplexElement decodes a full Timer, Counter, or Control element into a map.
func decodeComplexElement(fileType byte, data []byte) map[string]interface{} {
	result := make(map[string]interface{})

	if len(data) < 2 {
		return result
	}
	controlWord := binary.LittleEndian.Uint16(data[:2])

	switch fileType {
	case FileTypeTimer:
		result["EN"] = (controlWord>>TimerBitEN)&1 != 0
		result["TT"] = (controlWord>>TimerBitTT)&1 != 0
		result["DN"] = (controlWord>>TimerBitDN)&1 != 0
		if len(data) >= 4 {
			result["PRE"] = int16(binary.LittleEndian.Uint16(data[2:4]))
		}
		if len(data) >= 6 {
			result["ACC"] = int16(binary.LittleEndian.Uint16(data[4:6]))
		}

	case FileTypeCounter:
		result["CU"] = (controlWord>>CounterBitCU)&1 != 0
		result["CD"] = (controlWord>>CounterBitCD)&1 != 0
		result["DN"] = (controlWord>>CounterBitDN)&1 != 0
		result["OV"] = (controlWord>>CounterBitOV)&1 != 0
		result["UN"] = (controlWord>>CounterBitUN)&1 != 0
		if len(data) >= 4 {
			result["PRE"] = int16(binary.LittleEndian.Uint16(data[2:4]))
		}
		if len(data) >= 6 {
			result["ACC"] = int16(binary.LittleEndian.Uint16(data[4:6]))
		}

	case FileTypeControl:
		result["EN"] = (controlWord>>ControlBitEN)&1 != 0
		result["EU"] = (controlWord>>ControlBitEU)&1 != 0
		result["DN"] = (controlWord>>ControlBitDN)&1 != 0
		result["EM"] = (controlWord>>ControlBitEM)&1 != 0
		result["ER"] = (controlWord>>ControlBitER)&1 != 0
		result["UL"] = (controlWord>>ControlBitUL)&1 != 0
		result["IN"] = (controlWord>>ControlBitIN)&1 != 0
		result["FD"] = (controlWord>>ControlBitFD)&1 != 0
		if len(data) >= 4 {
			result["LEN"] = int16(binary.LittleEndian.Uint16(data[2:4]))
		}
		if len(data) >= 6 {
			result["POS"] = int16(binary.LittleEndian.Uint16(data[4:6]))
		}
	}

	return result
}

// encodeValue converts a Go value to bytes for the given address type.
func encodeValue(addr *FileAddress, value interface{}) ([]byte, error) {
	switch addr.FileType {
	case FileTypeInteger, FileTypeOutput, FileTypeInput, FileTypeStatus, FileTypeBinary, FileTypeASCII:
		return encodeInt16(value)

	case FileTypeFloat:
		return encodeFloat32(value)

	case FileTypeLong:
		return encodeInt32(value)

	case FileTypeTimer, FileTypeCounter, FileTypeControl:
		// For complex types with sub-element, write a 16-bit word
		if addr.SubElement > 0 {
			return encodeSubElement(addr, value)
		}
		return nil, fmt.Errorf("cannot write full Timer/Counter/Control element; specify a sub-element (e.g., .PRE, .ACC)")

	case FileTypeString:
		return encodeString(value)

	default:
		return nil, fmt.Errorf("unsupported file type 0x%02X for write", addr.FileType)
	}
}

// encodeSubElement encodes a T/C/R sub-element word. Timer PRE/ACC must be
// 0..32767: SLC processors major-fault on a negative timer preset or
// accumulator. Counter PRE/ACC and Control LEN/POS are signed 16-bit values.
// Other (numeric) sub-elements are treated as plain 16-bit words.
func encodeSubElement(addr *FileAddress, value interface{}) ([]byte, error) {
	switch {
	case addr.FileType == FileTypeTimer && (addr.SubElement == uint16(TimerPRE) || addr.SubElement == uint16(TimerACC)):
		return encodeInt16Range(value, 0, math.MaxInt16, "timer PRE/ACC")
	case addr.SubElement == 1 || addr.SubElement == 2:
		return encodeInt16Range(value, math.MinInt16, math.MaxInt16, "INT (int16)")
	default:
		return encodeInt16(value)
	}
}

// encodeInt16 encodes a 16-bit data-table word (N, B, S, A, O, I). It accepts
// -32768..65535: values 32768..65535 are stored as their two's-complement bit
// pattern, so 0xFFFF can be written to a B or S word. Anything outside that
// range is an error rather than being silently truncated.
func encodeInt16(value interface{}) ([]byte, error) {
	return encodeInt16Range(value, math.MinInt16, math.MaxUint16, "16-bit word")
}

func encodeInt16Range(value interface{}, min, max int64, what string) ([]byte, error) {
	var n int64
	if b, ok := value.(bool); ok {
		if b {
			n = 1
		}
	} else {
		var err error
		if n, err = integerValue(value, "INT (int16)"); err != nil {
			return nil, err
		}
	}
	if n < min || n > max {
		return nil, fmt.Errorf("value %d out of range for %s (%d..%d)", n, what, min, max)
	}
	return binary.LittleEndian.AppendUint16(nil, uint16(n)), nil
}

// integerValue converts a Go integer or an integral, finite float to int64.
func integerValue(value interface{}, target string) (int64, error) {
	switch v := value.(type) {
	case int:
		return int64(v), nil
	case int8:
		return int64(v), nil
	case int16:
		return int64(v), nil
	case int32:
		return int64(v), nil
	case int64:
		return v, nil
	case uint:
		if uint64(v) > math.MaxInt64 {
			return 0, fmt.Errorf("value %d out of range for %s", v, target)
		}
		return int64(v), nil
	case uint8:
		return int64(v), nil
	case uint16:
		return int64(v), nil
	case uint32:
		return int64(v), nil
	case uint64:
		if v > math.MaxInt64 {
			return 0, fmt.Errorf("value %d out of range for %s", v, target)
		}
		return int64(v), nil
	case float32:
		return floatToInteger(float64(v), target)
	case float64:
		return floatToInteger(v, target)
	default:
		return 0, fmt.Errorf("cannot convert %T to %s", value, target)
	}
}

func floatToInteger(f float64, target string) (int64, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) || math.Trunc(f) != f || f < math.MinInt64 || f >= math.MaxInt64 {
		return 0, fmt.Errorf("value %v is not an integral value in range for %s", f, target)
	}
	return int64(f), nil
}

// encodeFloat32 encodes an F element. A finite float64 outside the float32
// range used to become +/-Inf, and an integer above 2^24 was silently rounded
// (16777217 was written as 16777216); both are now errors, matching the
// driver layer's canonicalStorage overflow check.
func encodeFloat32(value interface{}) ([]byte, error) {
	var floatVal float32
	switch v := value.(type) {
	case float32:
		floatVal = v
	case float64:
		floatVal = float32(v)
		if math.IsInf(float64(floatVal), 0) && !math.IsInf(v, 0) {
			return nil, fmt.Errorf("value %v out of range for REAL (float32)", v)
		}
	case int, int16, int32, int64:
		n, _ := integerValue(v, "REAL (float32)")
		floatVal = float32(n)
		if int64(floatVal) != n {
			return nil, fmt.Errorf("value %d is not exactly representable as REAL (float32)", n)
		}
	default:
		return nil, fmt.Errorf("cannot convert %T to REAL (float32)", value)
	}
	return binary.LittleEndian.AppendUint32(nil, math.Float32bits(floatVal)), nil
}

// encodeInt32 encodes an L (long) element. Values must fit int32; a uint32
// argument is stored as its bit pattern, as before.
func encodeInt32(value interface{}) ([]byte, error) {
	if v, ok := value.(uint32); ok {
		return binary.LittleEndian.AppendUint32(nil, v), nil
	}
	n, err := integerValue(value, "LONG (int32)")
	if err != nil {
		return nil, err
	}
	if n < math.MinInt32 || n > math.MaxInt32 {
		return nil, fmt.Errorf("value %d out of range for LONG (int32) (%d..%d)", n, math.MinInt32, math.MaxInt32)
	}
	return binary.LittleEndian.AppendUint32(nil, uint32(int32(n))), nil
}

// maxStringLen is the character capacity of an SLC ST element.
const maxStringLen = 82

// swapStringBytes returns a copy of b with the two bytes of every 16-bit word
// exchanged, padding an odd length with a zero byte first. SLC/MicroLogix ST
// elements store characters as 16-bit words with the first character in the
// high byte, so "HELLO" is stored as "EH" "LL" "\x00O". This matches
// pycomm3's PCCCStringType._slc_string_swap.
func swapStringBytes(b []byte) []byte {
	out := make([]byte, len(b), len(b)+1)
	copy(out, b)
	if len(out)%2 != 0 {
		out = append(out, 0)
	}
	for i := 0; i+1 < len(out); i += 2 {
		out[i], out[i+1] = out[i+1], out[i]
	}
	return out
}

// encodeString builds a full 84-byte ST element: LEN (LE) followed by the
// characters byte-swapped per word and zero-filled to 82 bytes, so no stale
// characters from a previous, longer value remain in the element.
func encodeString(value interface{}) ([]byte, error) {
	var str string
	switch v := value.(type) {
	case string:
		str = v
	case []byte:
		str = string(v)
	default:
		return nil, fmt.Errorf("cannot convert %T to STRING", value)
	}

	strBytes := []byte(str)
	if len(strBytes) > maxStringLen {
		return nil, fmt.Errorf("string length %d exceeds ST element capacity of %d characters", len(strBytes), maxStringLen)
	}

	data := make([]byte, ElementSizeString)
	binary.LittleEndian.PutUint16(data[:2], uint16(len(strBytes)))
	copy(data[2:], swapStringBytes(strBytes))
	return data, nil
}

// GetIdentity queries the PLC's EtherNet/IP identity.
func (c *Client) GetIdentity() (*eip.Identity, error) {
	if c == nil || c.plc == nil || c.plc.Connection == nil {
		return nil, fmt.Errorf("GetIdentity: not connected")
	}
	identities, err := c.plc.Connection.ListIdentityTCP()
	if err != nil {
		return nil, err
	}
	if len(identities) == 0 {
		return nil, fmt.Errorf("no identity response")
	}
	return &identities[0], nil
}
