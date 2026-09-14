package ads

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/yatesdr/plcio/metadata"
)

// Wire definitions independently checked against the official Beckhoff SDK
// 7.0.339 (AdsDataTypeEntry XML and AdsDataTypeFlags constants) and captures.
const (
	dtDataType      uint32 = 0x1
	dtDataItem      uint32 = 0x2
	dtReference     uint32 = 0x4
	dtMethodDeref   uint32 = 0x8
	dtOversample    uint32 = 0x10
	dtBitValues     uint32 = 0x20
	dtProperty      uint32 = 0x40
	dtGUID          uint32 = 0x80
	dtPersistent    uint32 = 0x100
	dtCopyMask      uint32 = 0x200
	dtInterface     uint32 = 0x400
	dtMethods       uint32 = 0x800
	dtAttributes    uint32 = 0x1000
	dtEnums         uint32 = 0x2000
	dtAligned       uint32 = 0x10000
	dtStatic        uint32 = 0x20000
	dtIgnorePersist uint32 = 0x80000
	dtAnySize       uint32 = 0x100000
	dtPersistType   uint32 = 0x200000
	dtInitReset     uint32 = 0x400000
	dtPointer       uint32 = 0x800000
	dtRefactor      uint32 = 0x1000000
	dtHideMembers   uint32 = 0x2000000
	dtIncomplete    uint32 = 0x4000000
	dtOnlinePtr     uint32 = 0x8000000
	dtVariant       uint32 = 0x10000000
	dtExtendedEnums uint32 = 0x20000000
	dtExtendedFlags uint32 = 0x80000000
)

// typeEntry is immutable after parsing. Sizes/offsets are bits when dtBitValues
// is set, otherwise bytes. Identity includes native name/hash/GUID and snapshot.
type typeEntry struct {
	name, typeName, comment                               string
	version, hash, typeHash, size, offset, adsType, flags uint32
	dimensions                                            []metadata.Dimension
	members                                               []*typeEntry
	guid                                                  [16]byte
	attributes                                            map[string]string
	enums                                                 map[string][]byte
	copyMask, tail                                        []byte
	unsupported                                           string
}

type parseBudget struct {
	remaining uint64
	maxDepth  uint32
	deadline  time.Time
}

func (b *parseBudget) consume(count uint64) error {
	if err := checkDeadline(b.deadline); err != nil {
		return err
	}
	if count > b.remaining {
		return fmt.Errorf("metadata expansion limit exceeded")
	}
	b.remaining -= count
	return nil
}

func checkDeadline(deadline time.Time) error {
	if !deadline.IsZero() && !time.Now().Before(deadline) {
		return fmt.Errorf("ADS operation deadline: %w", context.DeadlineExceeded)
	}
	return nil
}

type metadataCursor struct {
	data   []byte
	offset int
}

func (c *metadataCursor) take(size uint64) ([]byte, error) {
	if size > uint64(len(c.data)-c.offset) {
		return nil, fmt.Errorf("metadata field truncated")
	}
	bytes := c.data[c.offset : c.offset+int(size)]
	c.offset += int(size)
	return bytes, nil
}
func (c *metadataCursor) text(size uint64) (string, error) {
	bytes, err := c.take(size + 1)
	if err != nil {
		return "", err
	}
	if bytes[len(bytes)-1] != 0 || strings.ContainsRune(string(bytes[:len(bytes)-1]), 0) {
		return "", fmt.Errorf("metadata string unterminated or contains NUL")
	}
	return string(bytes[:len(bytes)-1]), nil
}
func (c *metadataCursor) count() (uint16, error) {
	bytes, err := c.take(2)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(bytes), nil
}

func parseAttributes(c *metadataCursor, budget *parseBudget) (map[string]string, error) {
	count, err := c.count()
	if err != nil {
		return nil, err
	}
	if uint64(count)*4 > uint64(len(c.data)-c.offset) {
		return nil, fmt.Errorf("attribute count exceeds remaining bytes")
	}
	if err := budget.consume(uint64(count)); err != nil {
		return nil, err
	}
	attributes := make(map[string]string, int(count))
	for range count {
		lengths, err := c.take(2)
		if err != nil {
			return nil, err
		}
		name, err := c.text(uint64(lengths[0]))
		if err != nil {
			return nil, err
		}
		value, err := c.text(uint64(lengths[1]))
		if err != nil {
			return nil, err
		}
		if _, exists := attributes[name]; exists {
			return nil, fmt.Errorf("duplicate attribute %q", name)
		}
		attributes[name] = value
	}
	return attributes, nil
}

func parseTypeEntry(data []byte, budget *parseBudget, depth uint32) (*typeEntry, error) {
	if depth > budget.maxDepth {
		return nil, fmt.Errorf("datatype depth limit exceeded")
	}
	if err := budget.consume(1); err != nil {
		return nil, err
	}
	if len(data) < 45 || uint64(binary.LittleEndian.Uint32(data[:4])) != uint64(len(data)) {
		return nil, fmt.Errorf("invalid datatype entry length")
	}
	e := &typeEntry{version: binary.LittleEndian.Uint32(data[4:8]), hash: binary.LittleEndian.Uint32(data[8:12]), typeHash: binary.LittleEndian.Uint32(data[12:16]), size: binary.LittleEndian.Uint32(data[16:20]), offset: binary.LittleEndian.Uint32(data[20:24]), adsType: binary.LittleEndian.Uint32(data[24:28]), flags: binary.LittleEndian.Uint32(data[28:32])}
	c := metadataCursor{data: data, offset: 42}
	var err error
	fields := []*string{&e.name, &e.typeName, &e.comment}
	for i, field := range fields {
		*field, err = c.text(uint64(binary.LittleEndian.Uint16(data[32+i*2 : 34+i*2])))
		if err != nil {
			return nil, err
		}
	}
	if e.name == "" {
		return nil, fmt.Errorf("empty datatype/member name")
	}
	dims, members := uint64(binary.LittleEndian.Uint16(data[38:40])), uint64(binary.LittleEndian.Uint16(data[40:42]))
	if dims*8+members*45 > uint64(len(data)-c.offset) {
		return nil, fmt.Errorf("datatype counts exceed remaining bytes")
	}
	if err := budget.consume(dims); err != nil {
		return nil, err
	}
	e.dimensions = make([]metadata.Dimension, int(dims))
	for i := range e.dimensions {
		bytes, err := c.take(8)
		if err != nil {
			return nil, err
		}
		length := binary.LittleEndian.Uint32(bytes[4:8])
		if length == 0 {
			return nil, fmt.Errorf("zero-length array dimension")
		}
		e.dimensions[i] = metadata.Dimension{LowerBound: int64(int32(binary.LittleEndian.Uint32(bytes[:4]))), Length: length}
	}
	if members > budget.remaining {
		return nil, fmt.Errorf("datatype members exceed expansion limit")
	}
	e.members = make([]*typeEntry, int(members))
	names := make(map[string]bool, int(members))
	for i := range e.members {
		if len(data)-c.offset < 4 {
			return nil, fmt.Errorf("datatype member header truncated")
		}
		size := uint64(binary.LittleEndian.Uint32(data[c.offset : c.offset+4]))
		if size < 45 {
			return nil, fmt.Errorf("invalid datatype member length")
		}
		bytes, err := c.take(size)
		if err != nil {
			return nil, err
		}
		member, err := parseTypeEntry(bytes, budget, depth+1)
		if err != nil {
			return nil, fmt.Errorf("member %d: %w", i, err)
		}
		if names[member.name] {
			return nil, fmt.Errorf("duplicate member %q", member.name)
		}
		names[member.name] = true
		e.members[i] = member
	}
	if e.flags&dtGUID != 0 {
		bytes, err := c.take(16)
		if err != nil {
			return nil, err
		}
		copy(e.guid[:], bytes)
	}
	if e.flags&dtCopyMask != 0 {
		bytes, err := c.take(uint64(e.size))
		if err != nil {
			return nil, err
		}
		e.copyMask = append([]byte(nil), bytes...)
	}
	// Optional sections whose layout is not supported are retained as an opaque
	// extension. This prevents guessing offsets of subsequent attributes/enums.
	parsedFlags := dtDataType | dtDataItem | dtReference | dtMethodDeref | dtOversample | dtBitValues | dtProperty | dtGUID | dtPersistent | dtCopyMask | dtInterface | dtAttributes | dtEnums | dtAligned | dtStatic | dtIgnorePersist | dtPersistType | dtInitReset | dtPointer | dtHideMembers | dtIncomplete | dtOnlinePtr | dtVariant
	if unknown := e.flags & ^parsedFlags; unknown != 0 {
		e.unsupported = fmt.Sprintf("unsupported datatype extension flags 0x%x", unknown)
		e.tail = append([]byte(nil), data[c.offset:]...)
		return e, nil
	}
	if e.flags&dtAttributes != 0 {
		e.attributes, err = parseAttributes(&c, budget)
		if err != nil {
			return nil, err
		}
	}
	if e.flags&dtEnums != 0 {
		count, err := c.count()
		if err != nil {
			return nil, err
		}
		if e.size == 0 || e.size > 8 || uint64(count)*(uint64(e.size)+2) > uint64(len(data)-c.offset) {
			return nil, fmt.Errorf("invalid enum count/storage")
		}
		if err := budget.consume(uint64(count)); err != nil {
			return nil, err
		}
		e.enums = make(map[string][]byte, int(count))
		for range count {
			length, err := c.take(1)
			if err != nil {
				return nil, err
			}
			name, err := c.text(uint64(length[0]))
			if err != nil {
				return nil, err
			}
			value, err := c.take(uint64(e.size))
			if err != nil {
				return nil, err
			}
			if _, exists := e.enums[name]; exists {
				return nil, fmt.Errorf("duplicate enum identity %q", name)
			}
			e.enums[name] = append([]byte(nil), value...)
		}
	}
	if c.offset != len(data) {
		if len(data)%8 != 0 || len(data)-c.offset > 7 {
			return nil, fmt.Errorf("unaccounted datatype tail")
		}
		for _, value := range data[c.offset:] {
			if value != 0 {
				return nil, fmt.Errorf("nonzero datatype padding")
			}
		}
		e.tail = append([]byte(nil), data[c.offset:]...)
	}
	if e.version != 1 {
		e.unsupported = fmt.Sprintf("unsupported datatype entry version %d", e.version)
	}
	return e, nil
}

func parseTypeTable(data []byte, count uint32, cfg options) (map[string]*typeEntry, error) {
	if uint64(len(data)) > uint64(cfg.maxMetadata) || count > cfg.maxTypes || uint64(count)*45 > uint64(len(data)) {
		return nil, fmt.Errorf("datatype upload limits/count mismatch")
	}
	budget := parseBudget{remaining: uint64(cfg.maxElements), maxDepth: cfg.maxDepth, deadline: cfg.deadline}
	entries := make(map[string]*typeEntry, int(count))
	c := metadataCursor{data: data}
	for i := uint32(0); i < count; i++ {
		if len(data)-c.offset < 4 {
			return nil, fmt.Errorf("datatype header truncated")
		}
		size := uint64(binary.LittleEndian.Uint32(data[c.offset : c.offset+4]))
		bytes, err := c.take(size)
		if err != nil {
			return nil, err
		}
		entry, err := parseTypeEntry(bytes, &budget, 1)
		if err != nil {
			return nil, fmt.Errorf("datatype %d: %w", i, err)
		}
		if _, exists := entries[entry.name]; exists {
			return nil, fmt.Errorf("duplicate datatype %q", entry.name)
		}
		entries[entry.name] = entry
	}
	if c.offset != len(data) {
		return nil, fmt.Errorf("datatype upload trailing bytes")
	}
	return entries, nil
}
