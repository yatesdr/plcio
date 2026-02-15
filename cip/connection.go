package cip

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"sync/atomic"
	"time"
)

// CIP Connection Manager services
const (
	SvcForwardOpen      byte = 0x54 // Standard Forward Open (16-bit params, ≤511 bytes)
	SvcForwardOpenLarge byte = 0x5B // Large Forward Open (32-bit params, >511 bytes)
	SvcForwardClose     byte = 0x4E
	SvcUnconnectedSend  byte = 0x52

	// Connection Manager class/instance
	ClassConnectionManager byte = 0x06
	InstanceConnManager    byte = 0x01
)

// Connection represents an established CIP connection.
type Connection struct {
	OTConnID     uint32 // Originator -> Target connection ID
	TOConnID     uint32 // Target -> Originator connection ID
	SerialNumber uint16 // Connection serial number (for Forward Close)
	VendorID     uint16 // Originator vendor ID
	OrigSerial   uint32 // Originator serial number

	seq uint32 // Atomic sequence counter (low 16 bits used)
}

// NextSequence returns the next sequence number for connected messaging.
func (c *Connection) NextSequence() uint16 {
	return uint16(atomic.AddUint32(&c.seq, 1))
}

// WrapConnected prefixes a 16-bit sequence number to the CIP payload.
func (c *Connection) WrapConnected(cipPayload []byte) []byte {
	s := c.NextSequence()
	out := make([]byte, 2+len(cipPayload))
	binary.LittleEndian.PutUint16(out[0:2], s)
	copy(out[2:], cipPayload)
	return out
}

// UnwrapConnected extracts the sequence and CIP response payload.
func (c *Connection) UnwrapConnected(raw []byte) (seq uint16, cipPayload []byte, err error) {
	if len(raw) < 2 {
		return 0, nil, fmt.Errorf("connected data too short: %d bytes", len(raw))
	}
	seq = binary.LittleEndian.Uint16(raw[0:2])
	return seq, raw[2:], nil
}

// ForwardOpenConfig contains parameters for establishing a CIP connection.
type ForwardOpenConfig struct {
	// Timing parameters
	OTConnectionTimeout time.Duration // Originator->Target timeout
	TOConnectionTimeout time.Duration // Target->Originator timeout

	// Connection parameters
	OTConnectionSize uint16 // Max packet size O->T (default 500)
	TOConnectionSize uint16 // Max packet size T->O (default 500)

	// Connection path to target (e.g., backplane port 1, slot 0)
	ConnectionPath []byte

	// Vendor/serial for connection tracking
	VendorID         uint16
	OriginatorSerial uint32
}

// DefaultForwardOpenConfig returns a config with sensible defaults for Logix.
func DefaultForwardOpenConfig() ForwardOpenConfig {
	return ForwardOpenConfig{
		OTConnectionTimeout: 8 * time.Second,
		TOConnectionTimeout: 8 * time.Second,
		OTConnectionSize:    504,
		TOConnectionSize:    504,
		VendorID:            0x0001, // Rockwell
		OriginatorSerial:    uint32(rand.Int31()),
	}
}

// BuildForwardOpenRequest builds a Large Forward Open (0x54) CIP request.
// Returns the request data to be wrapped in CPF and sent via SendRRData.
func BuildForwardOpenRequest(cfg ForwardOpenConfig) ([]byte, uint16, error) {
	return buildForwardOpenInternal(cfg, true) // true = large format
}

// BuildForwardOpenRequestSmall builds a regular Forward Open (0x5B) CIP request.
// Uses 16-bit connection parameters instead of 32-bit.
func BuildForwardOpenRequestSmall(cfg ForwardOpenConfig) ([]byte, uint16, error) {
	return buildForwardOpenInternal(cfg, false) // false = small format
}

func buildForwardOpenInternal(cfg ForwardOpenConfig, large bool) ([]byte, uint16, error) {
	// Generate connection serial number (pylogix: randrange(65000))
	connSerial := uint16(rand.Intn(65000))

	// Pylogix values:
	// cip_priority = 0x0A
	// cip_timeout_ticks = 0x0e
	// cip_ot_connection_id = 0x20000002
	// cip_multiplier = 0x03
	// cip_ot_rpi = 0x00201234
	// cip_to_rpi = 0x00204001
	// cip_connection_parameters = 0x4200
	// cip_transport_trigger = 0xA3

	otRPI := uint32(0x00201234)  // ~2.1 seconds
	toRPI := uint32(0x00204001)  // ~2.1 seconds
	connParamsBase := uint16(0x4200)

	// Connection parameters: 0x4200 + size for standard, (0x4200 << 16) + size for large
	var otParams, toParams uint32
	if large {
		otParams = (uint32(connParamsBase) << 16) | uint32(cfg.OTConnectionSize)
		toParams = (uint32(connParamsBase) << 16) | uint32(cfg.TOConnectionSize)
	} else {
		otParams = uint32(connParamsBase) | uint32(cfg.OTConnectionSize)
		toParams = uint32(connParamsBase) | uint32(cfg.TOConnectionSize)
	}

	// Determine service code (pylogix: 0x54 for ≤511, 0x5B for >511)
	svcCode := SvcForwardOpen // 0x54 standard
	if large {
		svcCode = SvcForwardOpenLarge // 0x5B large
	}

	// Build the Forward Open packet (matching pylogix structure exactly)
	// Format: service, path_size, class_type, class, instance_type, instance,
	//         priority, timeout_ticks, ot_conn_id, to_conn_id, serial, vendor,
	//         orig_serial, multiplier(+reserved), ot_rpi, ot_params, to_rpi, to_params,
	//         transport, path_size, path...

	data := make([]byte, 0, 64+len(cfg.ConnectionPath))

	// Service (1 byte)
	data = append(data, svcCode)
	// Path size to Connection Manager (1 byte) = 2 words
	data = append(data, 0x02)
	// Class segment: 0x20 0x06 (class 6 = Connection Manager)
	data = append(data, 0x20, 0x06)
	// Instance segment: 0x24 0x01 (instance 1)
	data = append(data, 0x24, 0x01)

	// Priority/Tick time (1 byte) - pylogix uses 0x0A
	data = append(data, 0x0A)
	// Timeout ticks (1 byte) - pylogix uses 0x0e
	data = append(data, 0x0e)

	// O->T Connection ID (4 bytes) - pylogix uses 0x20000002
	data = binary.LittleEndian.AppendUint32(data, 0x20000002)
	// T->O Connection ID (4 bytes) - random
	toConnID := uint32(rand.Intn(65000))
	data = binary.LittleEndian.AppendUint32(data, toConnID)

	// Connection Serial Number (2 bytes)
	data = binary.LittleEndian.AppendUint16(data, connSerial)

	// Originator Vendor ID (2 bytes) - pylogix uses 0x1337
	data = binary.LittleEndian.AppendUint16(data, 0x1337)

	// Originator Serial Number (4 bytes) - pylogix uses 42
	data = binary.LittleEndian.AppendUint32(data, 42)

	// Connection Timeout Multiplier (4 bytes) - pylogix packs as I (includes 3 reserved)
	// Value 0x03 = multiplier 3
	data = binary.LittleEndian.AppendUint32(data, 0x03)

	// O->T RPI (4 bytes)
	data = binary.LittleEndian.AppendUint32(data, otRPI)

	// O->T Network Connection Parameters
	if large {
		data = binary.LittleEndian.AppendUint32(data, otParams)
	} else {
		data = binary.LittleEndian.AppendUint16(data, uint16(otParams))
	}

	// T->O RPI (4 bytes)
	data = binary.LittleEndian.AppendUint32(data, toRPI)

	// T->O Network Connection Parameters
	if large {
		data = binary.LittleEndian.AppendUint32(data, toParams)
	} else {
		data = binary.LittleEndian.AppendUint16(data, uint16(toParams))
	}

	// Transport Type/Trigger (1 byte) - 0xA3
	data = append(data, 0xA3)

	// Connection Path Size (1 byte, in words)
	pathSizeWords := byte(len(cfg.ConnectionPath) / 2)
	data = append(data, pathSizeWords)

	// Connection Path
	data = append(data, cfg.ConnectionPath...)

	// The data IS the complete CIP request (service + CM path already included)
	reqData := data

	return reqData, connSerial, nil
}

// ForwardOpenResponse contains the parsed response from Forward Open.
type ForwardOpenResponse struct {
	OTConnectionID   uint32
	TOConnectionID   uint32
	ConnectionSerial uint16
	VendorID         uint16
	OriginatorSerial uint32
	OTRPI            uint32
	TORPI            uint32
}

// ParseForwardOpenResponse parses a Forward Open response.
// Input should be the CIP response data (after service/status header).
func ParseForwardOpenResponse(data []byte) (*ForwardOpenResponse, error) {
	if len(data) < 26 {
		return nil, fmt.Errorf("Forward Open response too short: %d bytes", len(data))
	}

	return &ForwardOpenResponse{
		OTConnectionID:   binary.LittleEndian.Uint32(data[0:4]),
		TOConnectionID:   binary.LittleEndian.Uint32(data[4:8]),
		ConnectionSerial: binary.LittleEndian.Uint16(data[8:10]),
		VendorID:         binary.LittleEndian.Uint16(data[10:12]),
		OriginatorSerial: binary.LittleEndian.Uint32(data[12:16]),
		OTRPI:            binary.LittleEndian.Uint32(data[16:20]),
		TORPI:            binary.LittleEndian.Uint32(data[20:24]),
	}, nil
}

// BuildForwardCloseRequest builds a Forward Close (0x4E) CIP request.
func BuildForwardCloseRequest(conn *Connection, connectionPath []byte) ([]byte, error) {
	if conn == nil {
		return nil, fmt.Errorf("ForwardClose: nil connection")
	}

	// Build the path to Connection Manager
	cmPath, _ := EPath().Class(ClassConnectionManager).Instance(InstanceConnManager).Build()

	// Build Forward Close data
	data := make([]byte, 0, 16+len(connectionPath))

	// Priority/Tick time (1 byte)
	data = append(data, 0x0A)

	// Timeout ticks (1 byte)
	data = append(data, 0x01)

	// Connection Serial Number (2 bytes)
	data = binary.LittleEndian.AppendUint16(data, conn.SerialNumber)

	// Originator Vendor ID (2 bytes)
	data = binary.LittleEndian.AppendUint16(data, conn.VendorID)

	// Originator Serial Number (4 bytes)
	data = binary.LittleEndian.AppendUint32(data, conn.OrigSerial)

	// Connection Path Size (1 byte, in words)
	pathSizeWords := byte(len(connectionPath) / 2)
	if len(connectionPath)%2 != 0 {
		pathSizeWords++
	}
	data = append(data, pathSizeWords)

	// Reserved (1 byte)
	data = append(data, 0x00)

	// Connection Path
	data = append(data, connectionPath...)
	if len(connectionPath)%2 != 0 {
		data = append(data, 0x00)
	}

	// Build the complete CIP request
	reqData := make([]byte, 0, 2+len(cmPath)+len(data))
	reqData = append(reqData, SvcForwardClose)
	reqData = append(reqData, cmPath.WordLen())
	reqData = append(reqData, cmPath...)
	reqData = append(reqData, data...)

	return reqData, nil
}

