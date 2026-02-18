package pccc

import (
	"encoding/binary"
	"fmt"
	"math"
	"time"

	"github.com/yatesdr/plcio/eip"
)

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
		addr, err := ParseAddress(addrStr)
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

	return results, nil
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

	addr, err := ParseAddress(address)
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

// writeBit performs a read-modify-write to set/clear a single bit.
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

	// Read the current word
	readAddr := &FileAddress{
		FileType:   addr.FileType,
		FileNumber: addr.FileNumber,
		Element:    addr.Element,
		SubElement: addr.SubElement,
		BitNumber:  -1, // Read the full word
		RawAddress: addr.RawAddress,
	}

	tag, err := c.plc.ReadAddress(readAddr)
	if err != nil {
		return fmt.Errorf("bit write read-back failed: %w", err)
	}

	if len(tag.Bytes) < 2 {
		return fmt.Errorf("bit write: read returned %d bytes, need 2", len(tag.Bytes))
	}

	// Modify the bit
	word := binary.LittleEndian.Uint16(tag.Bytes[:2])
	if bitVal {
		word |= 1 << uint(addr.BitNumber)
	} else {
		word &^= 1 << uint(addr.BitNumber)
	}

	// Write back
	data := binary.LittleEndian.AppendUint16(nil, word)
	return c.plc.WriteAddress(readAddr, data)
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
		// SLC string: 2-byte length + up to 82 chars
		if len(data) < 2 {
			return data
		}
		strLen := int(binary.LittleEndian.Uint16(data[:2]))
		if strLen > len(data)-2 {
			strLen = len(data) - 2
		}
		if strLen > 82 {
			strLen = 82
		}
		return string(data[2 : 2+strLen])

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
			return encodeInt16(value)
		}
		return nil, fmt.Errorf("cannot write full Timer/Counter/Control element; specify a sub-element (e.g., .PRE, .ACC)")

	case FileTypeString:
		return encodeString(value)

	default:
		return nil, fmt.Errorf("unsupported file type 0x%02X for write", addr.FileType)
	}
}

func encodeInt16(value interface{}) ([]byte, error) {
	var intVal int16
	switch v := value.(type) {
	case int16:
		intVal = v
	case int:
		intVal = int16(v)
	case int32:
		intVal = int16(v)
	case int64:
		intVal = int16(v)
	case int8:
		intVal = int16(v)
	case uint8:
		intVal = int16(v)
	case uint16:
		intVal = int16(v)
	case float32:
		intVal = int16(v)
	case float64:
		intVal = int16(v)
	case bool:
		if v {
			intVal = 1
		}
	default:
		return nil, fmt.Errorf("cannot convert %T to INT (int16)", value)
	}
	return binary.LittleEndian.AppendUint16(nil, uint16(intVal)), nil
}

func encodeFloat32(value interface{}) ([]byte, error) {
	var floatVal float32
	switch v := value.(type) {
	case float32:
		floatVal = v
	case float64:
		floatVal = float32(v)
	case int:
		floatVal = float32(v)
	case int16:
		floatVal = float32(v)
	case int32:
		floatVal = float32(v)
	case int64:
		floatVal = float32(v)
	default:
		return nil, fmt.Errorf("cannot convert %T to REAL (float32)", value)
	}
	return binary.LittleEndian.AppendUint32(nil, math.Float32bits(floatVal)), nil
}

func encodeInt32(value interface{}) ([]byte, error) {
	var intVal int32
	switch v := value.(type) {
	case int32:
		intVal = v
	case int:
		intVal = int32(v)
	case int16:
		intVal = int32(v)
	case int64:
		intVal = int32(v)
	case int8:
		intVal = int32(v)
	case uint8:
		intVal = int32(v)
	case uint16:
		intVal = int32(v)
	case uint32:
		intVal = int32(v)
	case float32:
		intVal = int32(v)
	case float64:
		intVal = int32(v)
	default:
		return nil, fmt.Errorf("cannot convert %T to LONG (int32)", value)
	}
	return binary.LittleEndian.AppendUint32(nil, uint32(intVal)), nil
}

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
	if len(strBytes) > 82 {
		strBytes = strBytes[:82]
	}

	// SLC string format: 2-byte length (LE) + character data
	data := binary.LittleEndian.AppendUint16(nil, uint16(len(strBytes)))
	data = append(data, strBytes...)
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
