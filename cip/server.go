package cip

// Server-side (adapter) helpers for CIP Connection Manager services. These
// are the inverse of the existing client-side BuildForwardOpenRequest /
// ParseForwardOpenResponse code: parse what a scanner sent us, build what we
// reply.
//
// All additions in this file. The existing client-side code is unchanged.

import (
	"encoding/binary"
	"fmt"
)

// CIP general status codes (CIP Vol 1, Appendix B). Only the ones the adapter
// actually emits are listed; the rest are documented in the spec.
const (
	StatusSuccess               byte = 0x00
	StatusConnectionFailure     byte = 0x01
	StatusResourceUnavailable   byte = 0x02
	StatusServiceNotSupported   byte = 0x08
	StatusInvalidAttribute      byte = 0x09
	StatusAlreadyInRequestedMode byte = 0x0B
	StatusObjectStateConflict   byte = 0x0C
	StatusReplyDataTooLarge     byte = 0x11
	StatusNotEnoughData         byte = 0x13
	StatusAttrNotSupported      byte = 0x14
	StatusTooMuchData           byte = 0x15
	StatusObjectDoesNotExist    byte = 0x16
	StatusNoStoredAttrData      byte = 0x17
	StatusPathSegmentError      byte = 0x04
	StatusPathDestUnknown       byte = 0x05
	StatusInvalidParameter      byte = 0x20
	StatusVendorSpecificError   byte = 0x1F
	// Connection-manager specific extended statuses (passed in additional status)
	ExtConnectionInUse          uint16 = 0x0100
	ExtTransportClassUnsupp     uint16 = 0x0103
	ExtOwnershipConflict        uint16 = 0x0106
	ExtTargetConnNotFound       uint16 = 0x0107
	ExtInvalidConnSize          uint16 = 0x0109
	ExtRPIOutOfRange            uint16 = 0x0111
	ExtInvalidConnPath          uint16 = 0x0315
)

// ForwardOpenRequest is the parsed payload of a Forward_Open (0x54) or
// Large Forward_Open (0x5B) request sent by a scanner.
type ForwardOpenRequest struct {
	Large bool // true if Large Forward_Open (0x5B) — 32-bit connection params

	PriorityTickTime byte
	TimeoutTicks     byte

	OTConnectionID uint32 // Originator->Target connection ID (scanner-chosen for explicit, fixed for I/O)
	TOConnectionID uint32 // Target->Originator connection ID

	ConnectionSerial uint16
	VendorID         uint16
	OriginatorSerial uint32

	TimeoutMultiplier byte // packed in a 32-bit word with 3 reserved bytes

	OTRPI    uint32 // microseconds
	OTParams uint32 // network connection parameters (16-bit for standard, 32-bit for large)

	TORPI    uint32
	TOParams uint32

	TransportTrigger byte
	ConnectionPath   []byte // raw EPATH segments specifying the connection target
}

// ParseForwardOpenRequest decodes a Forward_Open request body. The input
// `data` is everything after the CIP service+path header — i.e., the request
// data field. Pass `large=true` for service 0x5B, false for 0x54.
func ParseForwardOpenRequest(data []byte, large bool) (*ForwardOpenRequest, error) {
	// Fixed-size prefix:
	// PriorityTickTime(1) TimeoutTicks(1) OTConnId(4) TOConnId(4)
	// ConnSerial(2) VendorID(2) OrigSerial(4) TimeoutMult(4)
	// OTRPI(4) OTParams(2 or 4) TORPI(4) TOParams(2 or 4)
	// TransportTrigger(1) PathSize(1, words) Path(N*2 bytes)
	minFixed := 1 + 1 + 4 + 4 + 2 + 2 + 4 + 4 + 4 + 4 + 1 + 1
	if large {
		minFixed += 4 // OTParams 32-bit
		minFixed += 4 // TOParams 32-bit
	} else {
		minFixed += 2
		minFixed += 2
	}
	if len(data) < minFixed {
		return nil, fmt.Errorf("cip: Forward_Open too short: %d < %d", len(data), minFixed)
	}

	r := &ForwardOpenRequest{Large: large}
	off := 0
	r.PriorityTickTime = data[off]
	off++
	r.TimeoutTicks = data[off]
	off++
	r.OTConnectionID = binary.LittleEndian.Uint32(data[off : off+4])
	off += 4
	r.TOConnectionID = binary.LittleEndian.Uint32(data[off : off+4])
	off += 4
	r.ConnectionSerial = binary.LittleEndian.Uint16(data[off : off+2])
	off += 2
	r.VendorID = binary.LittleEndian.Uint16(data[off : off+2])
	off += 2
	r.OriginatorSerial = binary.LittleEndian.Uint32(data[off : off+4])
	off += 4
	mult := binary.LittleEndian.Uint32(data[off : off+4])
	r.TimeoutMultiplier = byte(mult & 0xFF)
	off += 4

	r.OTRPI = binary.LittleEndian.Uint32(data[off : off+4])
	off += 4
	if large {
		r.OTParams = binary.LittleEndian.Uint32(data[off : off+4])
		off += 4
	} else {
		r.OTParams = uint32(binary.LittleEndian.Uint16(data[off : off+2]))
		off += 2
	}

	r.TORPI = binary.LittleEndian.Uint32(data[off : off+4])
	off += 4
	if large {
		r.TOParams = binary.LittleEndian.Uint32(data[off : off+4])
		off += 4
	} else {
		r.TOParams = uint32(binary.LittleEndian.Uint16(data[off : off+2]))
		off += 2
	}

	r.TransportTrigger = data[off]
	off++
	pathWords := int(data[off])
	off++
	pathBytes := pathWords * 2
	if off+pathBytes > len(data) {
		return nil, fmt.Errorf("cip: Forward_Open path truncated: need %d, have %d", pathBytes, len(data)-off)
	}
	r.ConnectionPath = append([]byte(nil), data[off:off+pathBytes]...)
	return r, nil
}

// ForwardOpenSuccess is the data the adapter writes back when accepting a
// Forward_Open. Field order matches CIP Vol 1 3-5.5.2.
type ForwardOpenSuccess struct {
	OTConnectionID   uint32 // Adapter-chosen for T->O direction, echo for O->T
	TOConnectionID   uint32
	ConnectionSerial uint16
	VendorID         uint16
	OriginatorSerial uint32
	OTAPI            uint32 // Actual Packet Interval (we usually echo the requested RPI)
	TOAPI            uint32
	AppReply         []byte // optional application reply, typically empty
}

// BuildForwardOpenSuccess builds the CIP response data for a successful
// Forward_Open. Wrap with the standard success response header
// (service|0x80, 0x00, 0x00, 0x00) before sending.
func BuildForwardOpenSuccess(s ForwardOpenSuccess) []byte {
	out := make([]byte, 0, 26+len(s.AppReply))
	out = binary.LittleEndian.AppendUint32(out, s.OTConnectionID)
	out = binary.LittleEndian.AppendUint32(out, s.TOConnectionID)
	out = binary.LittleEndian.AppendUint16(out, s.ConnectionSerial)
	out = binary.LittleEndian.AppendUint16(out, s.VendorID)
	out = binary.LittleEndian.AppendUint32(out, s.OriginatorSerial)
	out = binary.LittleEndian.AppendUint32(out, s.OTAPI)
	out = binary.LittleEndian.AppendUint32(out, s.TOAPI)
	// Application reply size (in 16-bit words) + reserved
	appWords := byte(len(s.AppReply) / 2)
	out = append(out, appWords, 0x00)
	out = append(out, s.AppReply...)
	return out
}

// ForwardOpenError encodes a Forward_Open failure response. Extended status
// is the connection-manager specific code (e.g., ExtInvalidConnPath).
type ForwardOpenError struct {
	GeneralStatus    byte // typically 0x01 (Connection Failure)
	ExtendedStatus   uint16
	ConnectionSerial uint16
	VendorID         uint16
	OriginatorSerial uint32
	RemainingPathSize byte // for path errors; 0 otherwise
}

// BuildForwardOpenError builds the error response body. The caller is
// responsible for the CIP response header (service|0x80, reserved,
// general_status, additional_status_size, additional_status_words...).
// Returns (responseHeader, responseBody) so the caller can assemble.
func BuildForwardOpenError(e ForwardOpenError, requestService byte) []byte {
	out := make([]byte, 0, 12)
	// CIP response header
	out = append(out, requestService|0x80, 0x00, e.GeneralStatus)
	if e.ExtendedStatus != 0 {
		out = append(out, 0x01) // 1 additional status word
		out = binary.LittleEndian.AppendUint16(out, e.ExtendedStatus)
	} else {
		out = append(out, 0x00)
	}
	// Response body
	out = binary.LittleEndian.AppendUint16(out, e.ConnectionSerial)
	out = binary.LittleEndian.AppendUint16(out, e.VendorID)
	out = binary.LittleEndian.AppendUint32(out, e.OriginatorSerial)
	out = append(out, e.RemainingPathSize, 0x00)
	return out
}

// ForwardCloseRequest is the parsed payload of a Forward_Close (0x4E) request.
type ForwardCloseRequest struct {
	PriorityTickTime byte
	TimeoutTicks     byte
	ConnectionSerial uint16
	VendorID         uint16
	OriginatorSerial uint32
	ConnectionPath   []byte
}

func ParseForwardCloseRequest(data []byte) (*ForwardCloseRequest, error) {
	// PriorityTickTime(1) TimeoutTicks(1) ConnSerial(2) VendorID(2)
	// OrigSerial(4) PathSize(1) Reserved(1) Path(N*2)
	if len(data) < 12 {
		return nil, fmt.Errorf("cip: Forward_Close too short: %d", len(data))
	}
	r := &ForwardCloseRequest{
		PriorityTickTime: data[0],
		TimeoutTicks:     data[1],
		ConnectionSerial: binary.LittleEndian.Uint16(data[2:4]),
		VendorID:         binary.LittleEndian.Uint16(data[4:6]),
		OriginatorSerial: binary.LittleEndian.Uint32(data[6:10]),
	}
	pathWords := int(data[10])
	// data[11] is reserved
	pathBytes := pathWords * 2
	if 12+pathBytes > len(data) {
		return nil, fmt.Errorf("cip: Forward_Close path truncated")
	}
	r.ConnectionPath = append([]byte(nil), data[12:12+pathBytes]...)
	return r, nil
}

// BuildForwardCloseSuccess builds the response body for a successful
// Forward_Close. Caller wraps with CIP success header.
func BuildForwardCloseSuccess(connSerial, vendorID uint16, origSerial uint32) []byte {
	out := make([]byte, 0, 12)
	out = binary.LittleEndian.AppendUint16(out, connSerial)
	out = binary.LittleEndian.AppendUint16(out, vendorID)
	out = binary.LittleEndian.AppendUint32(out, origSerial)
	out = append(out, 0x00, 0x00) // app reply size (0) + reserved
	return out
}

// ParsePath decodes a padded EPATH into class/instance/attribute/connection
// point fields. Only logical segments are recognised — port segments are
// returned in the leftover bytes for downstream connection-path handling.
type ParsedPath struct {
	Class           uint32
	HasClass        bool
	Instance        uint32
	HasInstance     bool
	Attribute       uint32
	HasAttribute    bool
	ConnectionPoint uint32
	HasConnPoint    bool
	Member          uint32
	HasMember       bool
	Symbolic        string
	HasSymbolic     bool
	Leftover        []byte
}

func ParsePath(path []byte) (*ParsedPath, error) {
	p := &ParsedPath{}
	i := 0
	for i < len(path) {
		seg := path[i]
		segType := (seg >> 5) & 0b111

		switch segType {
		case 0b001: // Logical segment
			logType := (seg >> 2) & 0b111
			logFmt := seg & 0b11
			i++
			var val uint32
			switch logFmt {
			case 0b00: // 8-bit
				if i+1 > len(path) {
					return nil, fmt.Errorf("cip: truncated 8-bit logical segment")
				}
				val = uint32(path[i])
				i++
			case 0b01: // 16-bit (with pad byte)
				if i+3 > len(path) {
					return nil, fmt.Errorf("cip: truncated 16-bit logical segment")
				}
				val = uint32(binary.LittleEndian.Uint16(path[i+1 : i+3]))
				i += 3
			case 0b10: // 32-bit (with pad byte)
				if i+5 > len(path) {
					return nil, fmt.Errorf("cip: truncated 32-bit logical segment")
				}
				val = binary.LittleEndian.Uint32(path[i+1 : i+5])
				i += 5
			default:
				return nil, fmt.Errorf("cip: reserved logical format")
			}
			switch logType {
			case 0b000:
				p.Class = val
				p.HasClass = true
			case 0b001:
				p.Instance = val
				p.HasInstance = true
			case 0b010:
				p.Member = val
				p.HasMember = true
			case 0b011:
				p.ConnectionPoint = val
				p.HasConnPoint = true
			case 0b100:
				p.Attribute = val
				p.HasAttribute = true
			default:
				return nil, fmt.Errorf("cip: unsupported logical type 0b%03b", logType)
			}
		case 0b011: // Symbolic segment (ANSI Extended)
			if seg != 0x91 {
				return nil, fmt.Errorf("cip: unsupported symbolic segment 0x%02X", seg)
			}
			if i+2 > len(path) {
				return nil, fmt.Errorf("cip: truncated symbolic segment header")
			}
			ln := int(path[i+1])
			if i+2+ln > len(path) {
				return nil, fmt.Errorf("cip: truncated symbolic segment body")
			}
			p.Symbolic = string(path[i+2 : i+2+ln])
			p.HasSymbolic = true
			adv := 2 + ln
			if adv%2 != 0 {
				adv++ // pad to word boundary
			}
			i += adv
		case 0b000: // Port segment — used in connection paths; stash remainder
			p.Leftover = append([]byte(nil), path[i:]...)
			return p, nil
		default:
			return nil, fmt.Errorf("cip: unsupported segment type 0b%03b at offset %d", segType, i)
		}
	}
	return p, nil
}
