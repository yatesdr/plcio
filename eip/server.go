package eip

// Server-side encapsulation primitives. These mirror the client-side EipEncap
// type but expose all fields so an adapter (the device being scanned) can
// receive, inspect, and respond to encapsulation frames.
//
// The existing client-side EipEncap type and its private read/build paths are
// untouched. Everything here is purely additive.

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	EncapHeaderLen = 24
	EncapMaxData   = 65511

	// Encapsulation-layer status codes (CIP Vol 2, 2-3.3).
	EncapStatusSuccess         uint32 = 0x0000_0000
	EncapStatusInvalidCommand  uint32 = 0x0000_0001
	EncapStatusInsufficientMem uint32 = 0x0000_0002
	EncapStatusInvalidData     uint32 = 0x0000_0003
	EncapStatusInvalidSession  uint32 = 0x0000_0064
	EncapStatusInvalidLength   uint32 = 0x0000_0065
	EncapStatusUnsupportedRev  uint32 = 0x0000_0069
)

// Frame is an EtherNet/IP encapsulation frame with public fields, suitable for
// adapter-side (server) implementations.
type Frame struct {
	Command       uint16
	SessionHandle uint32
	Status        uint32
	Context       [8]byte
	Options       uint32
	Data          []byte
}

// Bytes serialises the frame, computing length from len(Data).
func (f *Frame) Bytes() []byte {
	buf := make([]byte, 0, EncapHeaderLen+len(f.Data))
	buf = binary.LittleEndian.AppendUint16(buf, f.Command)
	buf = binary.LittleEndian.AppendUint16(buf, uint16(len(f.Data)))
	buf = binary.LittleEndian.AppendUint32(buf, f.SessionHandle)
	buf = binary.LittleEndian.AppendUint32(buf, f.Status)
	buf = append(buf, f.Context[:]...)
	buf = binary.LittleEndian.AppendUint32(buf, f.Options)
	buf = append(buf, f.Data...)
	return buf
}

// Reply builds a response Frame echoing this frame's command, session handle,
// context and options, with the supplied status and payload.
func (f *Frame) Reply(status uint32, data []byte) *Frame {
	return &Frame{
		Command:       f.Command,
		SessionHandle: f.SessionHandle,
		Status:        status,
		Context:       f.Context,
		Options:       f.Options,
		Data:          data,
	}
}

// ReadFrame reads one encapsulation frame from a stream (typically a TCP conn).
// On error the caller should close the connection — the framing is corrupt or
// the peer closed.
func ReadFrame(r io.Reader) (*Frame, error) {
	hdr := make([]byte, EncapHeaderLen)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	length := binary.LittleEndian.Uint16(hdr[2:4])
	if length > EncapMaxData {
		return nil, fmt.Errorf("eip: encap payload too large: %d", length)
	}
	var data []byte
	if length > 0 {
		data = make([]byte, length)
		if _, err := io.ReadFull(r, data); err != nil {
			return nil, fmt.Errorf("eip: read payload: %w", err)
		}
	}
	f := &Frame{
		Command:       binary.LittleEndian.Uint16(hdr[0:2]),
		SessionHandle: binary.LittleEndian.Uint32(hdr[4:8]),
		Status:        binary.LittleEndian.Uint32(hdr[8:12]),
		Options:       binary.LittleEndian.Uint32(hdr[20:24]),
		Data:          data,
	}
	copy(f.Context[:], hdr[12:20])
	return f, nil
}

// ParseFrame parses an encapsulation frame from an in-memory buffer (typically
// a UDP datagram). The returned Frame's Data references the input slice and
// must be copied if the caller needs to retain it past the buffer's lifetime.
func ParseFrame(b []byte) (*Frame, error) {
	if len(b) < EncapHeaderLen {
		return nil, fmt.Errorf("eip: short encap frame: %d bytes", len(b))
	}
	length := binary.LittleEndian.Uint16(b[2:4])
	if int(EncapHeaderLen)+int(length) > len(b) {
		return nil, fmt.Errorf("eip: truncated encap frame: header says %d, have %d", length, len(b)-EncapHeaderLen)
	}
	f := &Frame{
		Command:       binary.LittleEndian.Uint16(b[0:2]),
		SessionHandle: binary.LittleEndian.Uint32(b[4:8]),
		Status:        binary.LittleEndian.Uint32(b[8:12]),
		Options:       binary.LittleEndian.Uint32(b[20:24]),
		Data:          b[EncapHeaderLen : EncapHeaderLen+int(length)],
	}
	copy(f.Context[:], b[12:20])
	return f, nil
}

// BuildRRData wraps a CIP payload (already in CPF form) in the interface
// handle + timeout prefix expected by SendRRData and SendUnitData.
func BuildRRData(cpfBytes []byte) []byte {
	out := make([]byte, 0, 6+len(cpfBytes))
	out = binary.LittleEndian.AppendUint32(out, 0) // interface handle
	out = binary.LittleEndian.AppendUint16(out, 0) // timeout
	out = append(out, cpfBytes...)
	return out
}

// ParseRRData strips the interface handle + timeout prefix from a SendRRData
// or SendUnitData payload and returns the CPF bytes.
func ParseRRData(b []byte) ([]byte, error) {
	if len(b) < 6 {
		return nil, fmt.Errorf("eip: RR data too short: %d", len(b))
	}
	return b[6:], nil
}
