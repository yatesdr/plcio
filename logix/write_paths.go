package logix

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/yatesdr/plcio/cip"
	"github.com/yatesdr/plcio/logging"
)

// resolveWithLookup resolves a tag path like resolveTagInfo, and when the
// root tag is not yet known, finds it in the controller's symbol table once
// and caches it for this client. The cache serves type resolution only; it
// never feeds the read path. Cached roots are dropped again when the
// controller reports that the tag does not exist (forgetTagOnMissing); a new
// client (reconnect) starts with an empty cache.
func (c *Client) resolveWithLookup(name string) (TagInfo, bool) {
	if info, ok := c.resolveTagInfoIn(name, true); ok {
		return info, true
	}
	root := rootTagName(name)
	if _, known := c.lookupTagInfo(root); known {
		return TagInfo{}, false // Invalid/unresolved member; retain the known root.
	}
	if _, known := c.lookupResolvedType(root); known {
		return TagInfo{}, false
	}
	info, err := c.plc.FindSymbolByName(root)
	if err != nil || info == nil {
		return TagInfo{}, false
	}
	c.mu.Lock()
	if c.resolvedTypes == nil {
		c.resolvedTypes = make(map[string]TagInfo)
	}
	c.resolvedTypes[root] = *info
	c.mu.Unlock()
	return c.resolveTagInfoIn(name, true)
}

// lookupResolvedType returns a root cached by resolveWithLookup.
func (c *Client) lookupResolvedType(root string) (TagInfo, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	info, ok := c.resolvedTypes[root]
	return info, ok
}

// isTagMissingError reports whether the controller rejected a request
// because the addressed tag does not exist.
func isTagMissingError(err error) bool {
	var cipErr *cipStatusError
	if !errors.As(err, &cipErr) {
		return false
	}
	switch cipErr.status {
	case StatusPathSegmentError, StatusPathUnknown, StatusObjectNotExist:
		return true
	case StatusGeneralError:
		return cipErr.extStatus == ExtStatusTagNotFound
	}
	return false
}

// forgetTagOnMissing drops a root cached by resolveWithLookup when err says
// the tag no longer exists, so the next access looks it up again. Entries
// supplied through SetTags are left to the caller's catalog.
func (c *Client) forgetTagOnMissing(name string, err error) {
	if err == nil || !isTagMissingError(err) {
		return
	}
	c.mu.Lock()
	delete(c.resolvedTypes, rootTagName(name))
	c.mu.Unlock()
}

// WriteTagCount writes count elements of raw little-endian data using an
// atomic CIP type code. Unlike PLC().WriteTagCount it keeps this client's
// tag type cache consistent when the controller reports a missing tag.
func (c *Client) WriteTagCount(tagName string, dataType uint16, data []byte, count uint16) error {
	if c == nil || c.plc == nil {
		return fmt.Errorf("WriteTagCount: nil client")
	}
	err := c.plc.WriteTagCount(tagName, dataType, data, count)
	c.forgetTagOnMissing(tagName, err)
	return err
}

// integerBitWidth returns the bit width of an integer type whose individual
// bits can be addressed as Tag.N, or 0 for any other type.
func integerBitWidth(typeCode uint16) int {
	if IsStructure(typeCode) {
		return 0
	}
	switch BaseType(typeCode) {
	case TypeSINT, TypeUSINT:
		return 8
	case TypeINT, TypeUINT:
		return 16
	case TypeDINT, TypeUDINT:
		return 32
	case TypeLINT, TypeULINT:
		return 64
	}
	return 0
}

// bitReference reports whether name addresses one bit of an integer tag,
// written Parent.N. It is a bit reference only when the parent resolves to
// a scalar SINT/INT/DINT/LINT (or unsigned form) and N is below its width;
// BOOL array elements (Flags[37]) and structure members are not affected.
func (c *Client) bitReference(name string) (parent string, bit int, typeCode uint16, ok bool) {
	dot := strings.LastIndexByte(name, '.')
	if dot <= 0 || len(name)-dot-1 < 1 || len(name)-dot-1 > 2 {
		return "", 0, 0, false
	}
	for _, ch := range name[dot+1:] {
		if ch < '0' || ch > '9' {
			return "", 0, 0, false
		}
	}
	bit, _ = strconv.Atoi(name[dot+1:])
	parent = name[:dot]
	info, known := c.resolveWithLookup(parent)
	if !known || len(info.Dimensions) != 0 || IsArrayType(info.TypeCode) {
		return "", 0, 0, false
	}
	width := integerBitWidth(info.TypeCode)
	if width == 0 || bit >= width {
		return "", 0, 0, false
	}
	return parent, bit, BaseType(info.TypeCode), true
}

// readBitValue reads the parent integer of a bit reference and returns the
// addressed bit as a BOOL value. Failures are reported in the value's Error.
func (c *Client) readBitValue(name, parent string, bit int) *TagValue {
	tag, err := c.readTagWithMetadata(parent, 1)
	if err != nil {
		return &TagValue{Name: name, Error: err}
	}
	width := integerBitWidth(tag.DataType)
	if width == 0 || len(tag.Bytes) < width/8 || bit >= width {
		return &TagValue{Name: name, Error: fmt.Errorf("read %s: parent %s returned %d bytes of %s, not an integer",
			name, parent, len(tag.Bytes), TypeName(tag.DataType))}
	}
	return &TagValue{Name: name, DataType: TypeBOOL, Bytes: []byte{(tag.Bytes[bit/8] >> (bit % 8)) & 1}, Count: 1}
}

// bitWriteValue converts a Go value to the state of a single bit.
func bitWriteValue(value interface{}) (bool, error) {
	var n float64
	switch v := value.(type) {
	case bool:
		return v, nil
	case int:
		n = float64(v)
	case int8:
		n = float64(v)
	case int16:
		n = float64(v)
	case int32:
		n = float64(v)
	case int64:
		n = float64(v)
	case uint:
		n = float64(v)
	case uint8:
		n = float64(v)
	case uint16:
		n = float64(v)
	case uint32:
		n = float64(v)
	case uint64:
		n = float64(v)
	case float32:
		n = float64(v)
	case float64:
		n = v
	default:
		return false, fmt.Errorf("bit write requires a bool or 0/1, got %T", value)
	}
	if n != 0 && n != 1 {
		return false, fmt.Errorf("bit write requires a bool or 0/1, got %v", value)
	}
	return n == 1, nil
}

// writeBit sets or clears one bit of an integer tag with the Read Modify
// Write Tag service, which the controller applies atomically. There is
// deliberately no client-side read-then-write fallback: it would race with
// the controller's own logic and other writers.
func (c *Client) writeBit(name, parent string, bit int, typeCode uint16, value interface{}) error {
	set, err := bitWriteValue(value)
	if err != nil {
		return fmt.Errorf("Write %s: %w", name, err)
	}
	size := integerBitWidth(typeCode) / 8
	orMask, andMask := make([]byte, size), make([]byte, size)
	for i := range andMask {
		andMask[i] = 0xFF
	}
	if set {
		orMask[bit/8] |= 1 << (bit % 8)
	} else {
		andMask[bit/8] &^= 1 << (bit % 8)
	}
	if err := c.plc.ReadModifyWriteTag(parent, orMask, andMask); err != nil {
		return fmt.Errorf("Write %s: %w", name, err)
	}
	return nil
}

// ReadModifyWriteTag applies (value AND andMask) OR orMask to an integer tag
// on the controller using the Read Modify Write Tag service (0x4E). The
// masks must be the size of the tag's integer type (1, 2, 4 or 8 bytes).
// ControlLogix/CompactLogix support the service from firmware v16; other
// controllers may reply Service Not Supported, reported as an error.
func (p *PLC) ReadModifyWriteTag(tagName string, orMask, andMask []byte) error {
	if p == nil || p.Connection == nil {
		return fmt.Errorf("ReadModifyWriteTag: nil plc or connection")
	}
	if len(orMask) != len(andMask) || (len(orMask) != 1 && len(orMask) != 2 && len(orMask) != 4 && len(orMask) != 8) {
		return fmt.Errorf("ReadModifyWriteTag: masks must both be 1, 2, 4 or 8 bytes")
	}
	path, err := cip.EPath().Symbol(tagName).Build()
	if err != nil {
		return fmt.Errorf("ReadModifyWriteTag: failed to build path: %w", err)
	}
	// [Service] [PathSize] [Path] [MaskSize UINT] [OR mask] [AND mask]
	reqData := make([]byte, 0, 4+len(path)+2*len(orMask))
	reqData = append(reqData, SvcReadModifyWriteTag, path.WordLen())
	reqData = append(reqData, path...)
	reqData = binary.LittleEndian.AppendUint16(reqData, uint16(len(orMask)))
	reqData = append(reqData, orMask...)
	reqData = append(reqData, andMask...)

	cipResp, err := p.sendCipRequest(reqData)
	if err != nil {
		return fmt.Errorf("ReadModifyWriteTag: %w", err)
	}
	if len(cipResp) < 4 {
		return fmt.Errorf("ReadModifyWriteTag: response too short: %d bytes", len(cipResp))
	}
	if cipResp[0] != SvcReadModifyWriteTag|0x80 {
		return fmt.Errorf("ReadModifyWriteTag: unexpected reply service: 0x%02X", cipResp[0])
	}
	if status := cipResp[2]; status != StatusSuccess {
		err := parseCipError(status, cipResp[3], cipResp[4:])
		if status == StatusServiceNotSupport {
			return fmt.Errorf("ReadModifyWriteTag: controller does not support the Read Modify Write Tag service, so bits of integer tags cannot be written atomically: %w", err)
		}
		return fmt.Errorf("ReadModifyWriteTag: %w", err)
	}
	return nil
}

// stringTemplateCapacity reports whether tmpl has the layout of a Logix
// string type (built-in STRING or a custom string: LEN DINT at offset 0 and
// DATA SINT[n] at offset 4, nothing else) and returns its capacity n.
func stringTemplateCapacity(tmpl *Template) (int, bool) {
	if tmpl == nil || len(tmpl.MemberMap) != 2 {
		return 0, false
	}
	length, data := tmpl.GetMember("LEN"), tmpl.GetMember("DATA")
	if length == nil || data == nil || length.Hidden || data.Hidden ||
		length.IsArray() || length.Type != TypeDINT || length.Offset != 0 ||
		data.IsStructure() || BaseType(data.Type) != TypeSINT || data.Offset != 4 ||
		len(data.ArrayDims) != 1 || data.ArrayDims[0] <= 0 ||
		uint64(4+data.ArrayDims[0]) > uint64(tmpl.Size) {
		return 0, false
	}
	return data.ArrayDims[0], true
}

// writeString writes text to a tag whose type resolves to a string: a Logix
// STRING (or custom string) structure, or a Micro800 STRING (0xDA). It
// reports handled=false when the type is unknown or not a string type, so
// the caller can apply its ordinary conversion.
func (c *Client) writeString(tagName, text string) (handled bool, err error) {
	info, ok := c.resolveWithLookup(tagName)
	if !ok || len(info.Dimensions) != 0 || IsArrayType(info.TypeCode) {
		return false, nil
	}
	switch {
	case IsStructure(info.TypeCode):
		tmpl, err := c.GetTemplate(info.TypeCode)
		if err != nil {
			return true, fmt.Errorf("Write %s: %w", tagName, err)
		}
		capacity, ok := stringTemplateCapacity(tmpl)
		if !ok {
			return false, nil
		}
		if len(text) > capacity {
			return true, fmt.Errorf("Write %s: string is %d bytes, %s holds at most %d", tagName, len(text), tmpl.Name, capacity)
		}
		if tmpl.RawHandle == 0 {
			return true, fmt.Errorf("Write %s: structure handle of %s is unknown", tagName, tmpl.Name)
		}
		// The whole structure is written: LEN, then DATA zero-padded to the
		// structure size, typed as 0x02A0 + the template's structure handle.
		data := make([]byte, tmpl.Size)
		binary.LittleEndian.PutUint32(data, uint32(len(text)))
		copy(data[4:], text)
		typeBytes := binary.LittleEndian.AppendUint16([]byte{0xA0, 0x02}, tmpl.RawHandle)
		logging.DebugLog("logix", "Write %s: %s string, %d chars, handle 0x%04X", tagName, tmpl.Name, len(text), tmpl.RawHandle)
		return true, c.plc.writeTagTyped(tagName, typeBytes, 1, data)
	case BaseType(info.TypeCode) == TypeShortSTRING:
		// Micro800 STRING: one length byte followed by the characters, one
		// element (pylogix utils.make_special_string with type 0xDA).
		if len(text) > 255 {
			return true, fmt.Errorf("Write %s: string is %d bytes, a Micro800 STRING holds at most 255", tagName, len(text))
		}
		data := append([]byte{byte(len(text))}, text...)
		return true, c.plc.WriteTag(tagName, TypeShortSTRING, data)
	}
	return false, nil
}

// writeStrings writes consecutive string elements starting at tagName (an
// array, or an array element) in the element type's native layout. Every
// element is checked against the capacity before anything is sent. It
// reports handled=false when the type is unknown or not a string type.
func (c *Client) writeStrings(tagName string, texts []string) (handled bool, err error) {
	info, ok := c.resolveWithLookup(tagName)
	if !ok || (!IsStructure(info.TypeCode) && BaseType(info.TypeCode) != TypeShortSTRING) {
		return false, nil
	}
	if len(texts) == 0 || len(texts) > 0xFFFF {
		return true, fmt.Errorf("Write %s: string array needs 1..65535 elements, got %d", tagName, len(texts))
	}
	if len(info.Dimensions) > 0 && len(texts) > info.ElementCount() {
		return true, fmt.Errorf("Write %s: %d strings for %d elements", tagName, len(texts), info.ElementCount())
	}
	count := uint16(len(texts))
	if BaseType(info.TypeCode) == TypeShortSTRING && !IsStructure(info.TypeCode) {
		// Micro800 STRING elements are packed back to back, each a length
		// byte and its characters, in one Write Tag with count N (pylogix
		// _add_write_service: make_special_string per element, count =
		// len(write_data)); reads return the same packed form.
		var data []byte
		for i, text := range texts {
			if len(text) > 255 {
				return true, fmt.Errorf("Write %s: element %d is %d bytes, a Micro800 STRING holds at most 255", tagName, i, len(text))
			}
			data = append(append(data, byte(len(text))), text...)
		}
		path, err := cip.EPath().Symbol(tagName).Build()
		if err != nil {
			return true, fmt.Errorf("Write %s: %w", tagName, err)
		}
		if limit := c.plc.writePayloadLimit(len(path), 2, false); len(data) > limit {
			return true, fmt.Errorf("Write %s: %d bytes of strings exceed the %d-byte request limit; write fewer elements", tagName, len(data), limit)
		}
		return true, c.plc.WriteTagCount(tagName, TypeShortSTRING, data, count)
	}
	tmpl, err := c.GetTemplate(info.TypeCode)
	if err != nil {
		return true, fmt.Errorf("Write %s: %w", tagName, err)
	}
	capacity, ok := stringTemplateCapacity(tmpl)
	if !ok {
		return false, nil
	}
	for i, text := range texts {
		if len(text) > capacity {
			return true, fmt.Errorf("Write %s: element %d is %d bytes, %s holds at most %d", tagName, i, len(text), tmpl.Name, capacity)
		}
	}
	if tmpl.RawHandle == 0 {
		return true, fmt.Errorf("Write %s: structure handle of %s is unknown", tagName, tmpl.Name)
	}
	size := int(tmpl.Size)
	data := make([]byte, size*len(texts))
	for i, text := range texts {
		binary.LittleEndian.PutUint32(data[i*size:], uint32(len(text)))
		copy(data[i*size+4:], text)
	}
	typeBytes := binary.LittleEndian.AppendUint16([]byte{0xA0, 0x02}, tmpl.RawHandle)
	return true, c.plc.writeTagFragmented(tagName, typeBytes, count, data, size)
}

// writePayloadLimit returns how many value bytes fit in one Write Tag (or,
// with fragmented, Write Tag Fragmented) request on the current messaging
// path, mirroring the read batch budget.
func (p *PLC) writePayloadLimit(pathLen, typeLen int, fragmented bool) int {
	budget := 480
	if conn, connSize := p.activeConn(); conn != nil && connSize > 2 {
		budget = int(connSize) - 2 // Connected sequence number.
	} else if len(p.RoutePath) > 0 {
		budget -= 13 + len(p.RoutePath) // Unconnected Send wrapper and padding.
	}
	overhead := 2 + pathLen + typeLen + 2 // Service, path size, path, type, count.
	if fragmented {
		overhead += 4 // Byte offset.
	}
	return budget - overhead
}

// writeTagFragmented writes data with a single Write Tag when it fits one
// request, and otherwise with Write Tag Fragmented (0x53) requests at
// increasing byte offsets, split on element boundaries. A fragmented write is
// not atomic: a failure part-way leaves earlier fragments written.
func (p *PLC) writeTagFragmented(tagName string, typeBytes []byte, count uint16, data []byte, elemSize int) error {
	path, err := cip.EPath().Symbol(tagName).Build()
	if err != nil {
		return fmt.Errorf("WriteTag: failed to build path: %w", err)
	}
	if len(data) <= p.writePayloadLimit(len(path), len(typeBytes), false) {
		return p.writeTagTyped(tagName, typeBytes, count, data)
	}
	limit := p.writePayloadLimit(len(path), len(typeBytes), true)
	chunk := limit
	if elemSize > 0 && limit >= elemSize {
		chunk = limit / elemSize * elemSize
	}
	chunk &^= 3
	if chunk <= 0 {
		return fmt.Errorf("WriteTagFragmented: path too long for the request size")
	}
	for offset := 0; offset < len(data); offset += chunk {
		end := min(offset+chunk, len(data))
		// [Service] [PathSize] [Path] [Type] [Count] [Offset UDINT] [Data]
		reqData := make([]byte, 0, 2+len(path)+len(typeBytes)+6+end-offset)
		reqData = append(reqData, SvcWriteTagFragmented, path.WordLen())
		reqData = append(reqData, path...)
		reqData = append(reqData, typeBytes...)
		reqData = binary.LittleEndian.AppendUint16(reqData, count)
		reqData = binary.LittleEndian.AppendUint32(reqData, uint32(offset))
		reqData = append(reqData, data[offset:end]...)
		cipResp, err := p.sendCipRequest(reqData)
		if err != nil {
			return fmt.Errorf("WriteTagFragmented at offset %d: %w", offset, err)
		}
		if len(cipResp) < 4 {
			return fmt.Errorf("WriteTagFragmented: response too short: %d bytes", len(cipResp))
		}
		if cipResp[0] != SvcWriteTagFragmented|0x80 {
			return fmt.Errorf("WriteTagFragmented: unexpected reply service: 0x%02X", cipResp[0])
		}
		if cipResp[2] != StatusSuccess {
			return fmt.Errorf("WriteTagFragmented at offset %d: %w", offset, parseCipError(cipResp[2], cipResp[3], cipResp[4:]))
		}
	}
	return nil
}
