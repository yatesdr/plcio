package eip

import (
	"encoding/binary"
	"fmt"
)

// Generic Ethernet/IP Encapsulation
type EipEncap struct {
	command       uint16
	length        uint16
	sessionHandle uint32
	status        uint32
	context       [8]byte
	options       uint32
	data          []byte
}

// General Request/Receive data wrapper type.
type EipCommandData struct {
	interfaceHandle uint32
	timeout         uint16
	packet          []byte
}

// Convert to bytes
func (m *EipEncap) Bytes() []byte {
	buf := []byte{}
	buf = binary.LittleEndian.AppendUint16(buf, m.command)
	buf = binary.LittleEndian.AppendUint16(buf, m.length)
	buf = binary.LittleEndian.AppendUint32(buf, m.sessionHandle)
	buf = binary.LittleEndian.AppendUint32(buf, m.status)
	buf = append(buf, m.context[:]...)
	buf = binary.LittleEndian.AppendUint32(buf, m.options)
	buf = append(buf, m.data...)
	return buf
}

// Generate a LittleEndian encoded byte slice for RrData.
func (r *EipCommandData) Bytes() []byte {
	raw := binary.LittleEndian.AppendUint32(nil, r.interfaceHandle)
	raw = binary.LittleEndian.AppendUint16(raw, r.timeout)
	raw = append(raw, r.packet...)
	return raw
}

func ParseEipCommandData(raw []byte) (*EipCommandData, error) {
	if len(raw) < 8 {
		return nil, fmt.Errorf("ParseCommandData:  Raw bytes too short: Minimum 8, got %d", len(raw))
	}

	return &EipCommandData{
		interfaceHandle: binary.LittleEndian.Uint32(raw[:4]),
		timeout:         binary.LittleEndian.Uint16(raw[4:6]),
		packet:          raw[6:],
	}, nil
}
