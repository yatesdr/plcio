package cip

import (
	"encoding/binary"
	"fmt"
)

type LogicalType byte
type LogicalFormat byte
type SegmentType byte

// PortSegment Type Definitions
const (
	CipPortSegment            SegmentType = 0b000
	CipLogicalSegment         SegmentType = 0b001
	CipNetworkSegment         SegmentType = 0b010
	CipSymbolicSegment        SegmentType = 0b011
	CipDataSegmentConstructed SegmentType = 0b101
	CipDataSegmentElementary  SegmentType = 0b110

	CipLogicalTypeClassId         LogicalType = 0x0
	CipLogicalTypeInstanceId      LogicalType = 0b1
	CipLogicalTypeMemberId        LogicalType = 0b10
	CipLogicalTypeConnectionPoint LogicalType = 0b011
	CipLogicalTypeAttributeId     LogicalType = 0b100
	CipLogicalTypeSpecial         LogicalType = 0b101
	CipLogicalTypeServiceId       LogicalType = 0b110

	CipLogicalFormat8bit  LogicalFormat = 0b0
	CipLogicalFormat16bit LogicalFormat = 0b1
	CipLogicalFormat32bit LogicalFormat = 0b10
)

type PathBuilder struct {
	err    error
	epath  EPath_t
	padded bool
}

// A fluent-style Epath builder.   Typically this is the one to use.
func EPath() *PathBuilder {
	return &PathBuilder{padded: true}
}

func (b *PathBuilder) add(p EPath_t, err error) *PathBuilder {
	if b.err != nil {
		return b
	}
	if err != nil {
		b.err = err
		return b
	}
	b.epath = append(b.epath, p...)
	return b
}

func (b *PathBuilder) Class(id byte) *PathBuilder {
	return b.add(logicalSegment(CipLogicalTypeClassId, CipLogicalFormat8bit, []byte{id}, b.padded))
}

func (b *PathBuilder) Instance(id byte) *PathBuilder {
	return b.add(logicalSegment(CipLogicalTypeInstanceId, CipLogicalFormat8bit, []byte{id}, b.padded))
}

func (b *PathBuilder) Instance16(id uint16) *PathBuilder {
	return b.add(logicalSegment(CipLogicalTypeInstanceId, CipLogicalFormat16bit, binary.LittleEndian.AppendUint16(nil, id), b.padded))
}

func (b *PathBuilder) Instance32(id uint32) *PathBuilder {
	return b.add(logicalSegment(CipLogicalTypeInstanceId, CipLogicalFormat32bit, binary.LittleEndian.AppendUint32(nil, id), b.padded))
}

func (b *PathBuilder) Attribute(id byte) *PathBuilder {
	return b.add(logicalSegment(CipLogicalTypeAttributeId, CipLogicalFormat8bit, []byte{id}, b.padded))
}

func (b *PathBuilder) Symbol(tag string) *PathBuilder {
	// Handle dotted paths by creating separate symbolic segments for each part.
	// The period (.) is the segment separator.
	// The colon (:) is NOT a separator - "Program:MainProgram" stays as one segment.
	// Also handle array indices like "MyArray[5]" by adding member segments.

	parts := splitTagPath(tag)
	for _, part := range parts {
		if part.isIndex {
			// Array index - add as member segment
			b = b.add(memberSegment(part.index))
		} else {
			// Symbolic name
			b = b.add(symbolicSegmentAsciiExt([]byte(part.name)))
		}
	}
	return b
}

func (b *PathBuilder) Build() (EPath_t, error) {

	if b.err != nil {
		return nil, b.err
	}

	// return a copy to avoid messing up the builder if more paths need to be added.
	out := append(EPath_t{}, b.epath...)

	if b.padded && len(out)%2 != 0 {
		out = append(out, 0x00)
	}
	return out, b.err
}

func (p *EPath_t) WordLen() byte {
	return byte(len([]byte(*p)) / 2)
}

// EPath is an encoded path used in CIP communications.
type EPath_t []byte

// Encode a Logical Segment, returns a packed or unpacked Epath.   The padding requirements for a Logical Segment include inter-byte
// padding for some formats, so **padding must be specified at time of creation**.   Padding applies to 16- and 32-bit logical formats
// to achieve word alignment within the LogicalType encoded path.
func logicalSegment(logical_type LogicalType, logical_format LogicalFormat, value []byte, padded bool) (EPath_t, error) {

	segmentType := byte(CipLogicalSegment)

	if logical_type == CipLogicalTypeSpecial {
		out := []byte{0x34}
		out = append(out, value...)
		return EPath_t(out), nil
	}

	if logical_type == CipLogicalTypeServiceId {
		out := []byte{0x38}
		out = append(out, value...)
		return EPath_t(out), nil
	}

	// Validate value size for the format bits (this is the big missing piece).
	switch logical_format {
	case CipLogicalFormat8bit:
		if len(value) != 1 {
			return nil, fmt.Errorf("LogicalSegment: 8-bit format requires 1 byte, got %d", len(value))
		}
	case CipLogicalFormat16bit:
		if len(value) != 2 {
			return nil, fmt.Errorf("LogicalSegment: 16-bit format requires 2 bytes, got %d", len(value))
		}
	case CipLogicalFormat32bit:
		if len(value) != 4 {
			return nil, fmt.Errorf("LogicalSegment: 32-bit format requires 4 bytes, got %d", len(value))
		}
	default:
		return nil, fmt.Errorf("LogicalSegment: unsupported logical format %v", logical_format)
	}

	// The capacity of a padded 16 or 32-bit logical segment should account for the internal pad byte.
	capHint := 1 + len(value)
	if padded && (logical_format == CipLogicalFormat16bit || logical_format == CipLogicalFormat32bit) {
		capHint++
	}
	out := make([]byte, 1, capHint)

	out[0] |= (segmentType & 0b111) << 5
	out[0] |= (byte(logical_type) & 0b111) << 2
	out[0] |= (byte(logical_format) & 0b11)

	// A pad byte 0x00 is required before the value for padded paths if the segment is 16 or 32 bits per ODVA 1.4
	if padded && (logical_format == CipLogicalFormat16bit || logical_format == CipLogicalFormat32bit) {
		out = append(out, 0x00)
	}

	out = append(out, value...)

	return EPath_t(out), nil

}

// tagPart represents a component of a tag path (either a name or an array index)
type tagPart struct {
	name    string
	index   uint32
	isIndex bool
}

// splitTagPath parses a tag path like "Program.Tag[5].Member" into components.
func splitTagPath(tag string) []tagPart {
	var parts []tagPart
	current := ""

	for i := 0; i < len(tag); i++ {
		ch := tag[i]
		switch ch {
		case '.':
			// Dot separator - end current name segment
			if current != "" {
				parts = append(parts, tagPart{name: current})
				current = ""
			}
		case '[':
			// Start of array index
			if current != "" {
				parts = append(parts, tagPart{name: current})
				current = ""
			}
			// Find closing bracket and parse index
			j := i + 1
			for j < len(tag) && tag[j] != ']' {
				j++
			}
			if j > i+1 {
				indexStr := tag[i+1 : j]
				var idx uint32
				for _, c := range indexStr {
					if c >= '0' && c <= '9' {
						idx = idx*10 + uint32(c-'0')
					}
				}
				parts = append(parts, tagPart{index: idx, isIndex: true})
			}
			i = j // Skip past the ']'
		case ']':
			// Already handled in '[' case
		default:
			current += string(ch)
		}
	}

	// Don't forget the last segment
	if current != "" {
		parts = append(parts, tagPart{name: current})
	}

	return parts
}

// memberSegment creates a member/element segment for array indexing
func memberSegment(index uint32) (EPath_t, error) {
	if index <= 0xFF {
		// 8-bit member
		return EPath_t{0x28, byte(index)}, nil
	} else if index <= 0xFFFF {
		// 16-bit member (with pad byte for alignment)
		return EPath_t{0x29, 0x00, byte(index), byte(index >> 8)}, nil
	} else {
		// 32-bit member (with pad byte for alignment)
		return EPath_t{0x2A, 0x00, byte(index), byte(index >> 8), byte(index >> 16), byte(index >> 24)}, nil
	}
}

func symbolicSegmentAsciiExt(symbol []byte) (EPath_t, error) {

	if len(symbol) > 255 {
		return nil, fmt.Errorf("SymbolicSegmentAsciiExt: Symbol is too long, maximum 255 bytes.")
	}
	if len(symbol) == 0 {
		return nil, fmt.Errorf("SymbolicSegmentAsciiExt: Symbol length is zero - cannot encode epath.")
	}
	out := []byte{0x91, byte(len(symbol))}
	out = append(out, symbol...)
	if len(out)%2 != 0 {
		out = append(out, 0x00)
	}
	return EPath_t(out), nil
}
