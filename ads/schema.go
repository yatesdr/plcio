package ads

import (
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yatesdr/plcio/metadata"
)

type schemaMember struct {
	name                 string
	typeOf               *schemaType
	offsetBits, sizeBits uint64
	readOnly             bool
}
type schemaType struct {
	kind                           metadata.Kind
	bits                           uint16
	name, unit, epoch, unsupported string
	size                           uint64 // complete wire size in bytes
	bit                            bool
	dimensions                     []metadata.Dimension // effective, flattened axes
	element                        *schemaType
	members                        []schemaMember
	utf8                           bool
	min, max                       *big.Int // immutable subrange bounds
	enums                          map[string][]byte
	wide                           bool
	timeOfDayLimit                 uint64
}

type schemaSnapshot struct {
	bytes         uint64
	generation    uint64
	version       uint32
	versionKnown  bool
	catalog       []TagInfo
	entries       map[string]*typeEntry
	resolver      *typeResolver
	symbols       map[string]*symbolRecord
	readOnlyNames []string          // sorted access index derived from this catalog only
	folded        map[string]string // ASCII-folded name -> canonical catalog name; "" if ambiguous
}

type typeResolver struct {
	entries  map[string]*typeEntry
	memo     map[string]*schemaType
	visiting map[string]bool
	cfg      options
	expanded uint64
	deadline time.Time // refreshed by the operation-gate owner, never a cached option
}

func newResolver(entries map[string]*typeEntry, cfg options) *typeResolver {
	// Resolvers are cached across operations; deadlines belong to callers.
	cfg.deadline = time.Time{}
	return &typeResolver{entries: entries, memo: make(map[string]*schemaType), visiting: make(map[string]bool), cfg: cfg}
}

func opaqueType(name string, size uint64, reason string) *schemaType {
	return &schemaType{name: name, size: size, kind: metadata.KindOpaque, unsupported: reason}
}

func (r *typeResolver) resolveName(name string, depth uint32) *schemaType {
	if err := checkDeadline(r.deadline); err != nil {
		return opaqueType(name, 0, err.Error())
	}
	if cached := r.memo[name]; cached != nil {
		return cached
	}
	if depth > r.cfg.maxDepth {
		return opaqueType(name, 0, "schema depth limit exceeded")
	}
	if r.visiting[name] {
		return opaqueType(name, 0, "cyclic datatype reference")
	}
	r.expanded++
	if r.expanded > uint64(r.cfg.maxElements) {
		return opaqueType(name, 0, "schema expansion limit exceeded")
	}
	r.visiting[name] = true
	defer delete(r.visiting, name)
	var result *schemaType
	if entry := r.entries[name]; entry != nil {
		result = r.resolveEntry(entry, depth)
	} else {
		result = r.resolveDeclaration(name, depth)
	}
	// An interrupted resolution must not poison a retained resolver with a
	// timeout-derived unsupported schema on the next operation.
	if err := checkDeadline(r.deadline); err != nil {
		return opaqueType(name, 0, err.Error())
	}
	r.memo[name] = result
	return result
}

func countDimensions(dimensions []metadata.Dimension, limit uint64) (uint64, error) {
	count := uint64(1)
	for _, dim := range dimensions {
		if dim.Length == 0 || count > limit/uint64(dim.Length) {
			return 0, fmt.Errorf("array element limit exceeded")
		}
		count *= uint64(dim.Length)
	}
	return count, nil
}

var arrayDeclaration = regexp.MustCompile(`(?i)^ARRAY\s*\[([^\]]+)\]\s+OF\s+(.+)$`)
var subrangeDeclaration = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z_0-9.]*)\s*\(\s*([+-]?[0-9]+)\s*\.\.\s*([+-]?[0-9]+)\s*\)\s*$`)
var stringDeclaration = regexp.MustCompile(`(?i)^(W?STRING)(?:\s*\(\s*([0-9]+)\s*\))?$`)

func declarationDimensions(name string, limit uint64) ([]metadata.Dimension, string, error) {
	match := arrayDeclaration.FindStringSubmatch(strings.TrimSpace(name))
	if match == nil {
		return nil, name, nil
	}
	parts := strings.Split(match[1], ",")
	if uint64(len(parts)) > limit {
		return nil, "", fmt.Errorf("array dimension limit exceeded")
	}
	dims := make([]metadata.Dimension, len(parts))
	for i, part := range parts {
		bounds := strings.Split(part, "..")
		if len(bounds) != 2 {
			return nil, "", fmt.Errorf("invalid array bounds")
		}
		lower, err := strconv.ParseInt(strings.TrimSpace(bounds[0]), 10, 64)
		if err != nil {
			return nil, "", err
		}
		upper, err := strconv.ParseInt(strings.TrimSpace(bounds[1]), 10, 64)
		if err != nil || upper < lower {
			return nil, "", fmt.Errorf("invalid array bounds")
		}
		// Unsigned subtraction avoids signed overflow even at int64 boundaries.
		distance := uint64(upper) - uint64(lower)
		if distance >= uint64(^uint32(0)) {
			return nil, "", fmt.Errorf("array bound length overflow")
		}
		dims[i] = metadata.Dimension{LowerBound: lower, Length: uint32(distance + 1)}
	}
	return dims, strings.TrimSpace(match[2]), nil
}

func (r *typeResolver) array(name string, size uint64, dims []metadata.Dimension, base *schemaType) *schemaType {
	count, err := countDimensions(dims, uint64(r.cfg.maxElements))
	if err != nil {
		return opaqueType(name, size, err.Error())
	}
	if base.unsupported != "" {
		result := *base
		result.name, result.size = name, size
		result.kind = metadata.KindOpaque
		result.dimensions = append(append([]metadata.Dimension(nil), dims...), base.dimensions...)
		result.element = base
		return &result
	}
	if base.size == 0 || count > ^uint64(0)/base.size {
		return opaqueType(name, size, "array storage size overflow")
	}
	expected := count * base.size
	if size == 0 {
		size = expected
	}
	if size != expected {
		return opaqueType(name, size, "array count/stride does not match advertised storage")
	}
	result := *base
	result.name, result.size = name, size
	result.dimensions = append(append([]metadata.Dimension(nil), dims...), base.dimensions...)
	if _, err := countDimensions(result.dimensions, uint64(r.cfg.maxElements)); err != nil {
		return opaqueType(name, size, err.Error())
	}
	if base.element != nil {
		result.element = base.element
	} else {
		result.element = base
	}
	return &result
}

func (r *typeResolver) resolveDeclaration(name string, depth uint32) *schemaType {
	dims, base, err := declarationDimensions(name, uint64(r.cfg.maxDepth))
	if err != nil {
		return opaqueType(name, 0, err.Error())
	}
	if len(dims) != 0 {
		return r.array(name, 0, dims, r.resolveName(base, depth+1))
	}
	if match := subrangeDeclaration.FindStringSubmatch(name); match != nil {
		base := r.resolveName(match[1], depth+1)
		if base.kind != metadata.KindInt && base.kind != metadata.KindUint {
			return opaqueType(name, base.size, "subrange underlying type is not an integer")
		}
		lo, ok1 := new(big.Int).SetString(match[2], 10)
		hi, ok2 := new(big.Int).SetString(match[3], 10)
		if !ok1 || !ok2 || lo.Cmp(hi) > 0 || !integerBoundFits(lo, base.kind, base.bits) || !integerBoundFits(hi, base.kind, base.bits) {
			return opaqueType(name, base.size, "invalid subrange bounds")
		}
		result := *base
		result.name, result.min, result.max = name, lo, hi
		return &result
	}
	if match := stringDeclaration.FindStringSubmatch(strings.TrimSpace(name)); match != nil {
		capacity := uint64(80)
		if match[2] != "" {
			var err error
			capacity, err = strconv.ParseUint(match[2], 10, 32)
			if err != nil {
				return opaqueType(name, 0, "invalid string capacity")
			}
		}
		size := capacity + 1
		if strings.EqualFold(match[1], "WSTRING") {
			size *= 2
		}
		if size > uint64(r.cfg.maxPayload) {
			return opaqueType(name, size, "string storage limit exceeded")
		}
		return &schemaType{kind: metadata.KindString, name: name, size: size, wide: strings.EqualFold(match[1], "WSTRING"), utf8: r.cfg.utf8Strings}
	}
	upper := strings.ToUpper(strings.TrimSpace(name))
	result := &schemaType{name: name}
	switch upper {
	case "BOOL":
		result.kind, result.bits, result.size = metadata.KindBool, 8, 1
	case "BIT":
		result.kind, result.bits, result.size, result.bit = metadata.KindBool, 1, 1, true
	case "SINT":
		result.kind, result.bits, result.size = metadata.KindInt, 8, 1
	case "INT":
		result.kind, result.bits, result.size = metadata.KindInt, 16, 2
	case "DINT":
		result.kind, result.bits, result.size = metadata.KindInt, 32, 4
	case "LINT":
		result.kind, result.bits, result.size = metadata.KindInt, 64, 8
	case "BYTE", "USINT":
		result.kind, result.bits, result.size = metadata.KindUint, 8, 1
	case "WORD", "UINT":
		result.kind, result.bits, result.size = metadata.KindUint, 16, 2
	case "DWORD", "UDINT":
		result.kind, result.bits, result.size = metadata.KindUint, 32, 4
	case "LWORD", "ULINT":
		result.kind, result.bits, result.size = metadata.KindUint, 64, 8
	case "REAL":
		result.kind, result.bits, result.size = metadata.KindFloat, 32, 4
	case "LREAL":
		result.kind, result.bits, result.size = metadata.KindFloat, 64, 8
	case "TIME", "TOD", "TIME_OF_DAY":
		result.kind, result.bits, result.size, result.unit = metadata.KindUint, 32, 4, metadata.UnitMilliseconds
	case "DATE", "DT", "DATE_AND_TIME":
		result.kind, result.bits, result.size, result.unit, result.epoch = metadata.KindUint, 32, 4, metadata.UnitSeconds, metadata.EpochUnix
	case "LTIME", "LTOD", "LTIME_OF_DAY":
		result.kind, result.bits, result.size, result.unit = metadata.KindUint, 64, 8, metadata.UnitNanoseconds
	case "LDATE", "LDT", "LDATE_AND_TIME":
		result.kind, result.bits, result.size, result.unit, result.epoch = metadata.KindInt, 64, 8, metadata.UnitNanoseconds, metadata.EpochUnix
	default:
		return opaqueType(name, 0, "unpublished or unsupported datatype")
	}
	if upper == "TOD" || upper == "TIME_OF_DAY" {
		result.timeOfDayLimit = 86400000
	}
	if upper == "LTOD" || upper == "LTIME_OF_DAY" {
		result.timeOfDayLimit = 86400000000000
	}
	return result
}

func integerBoundFits(n *big.Int, kind metadata.Kind, bits uint16) bool {
	if bits == 0 || bits > 64 {
		return false
	}
	if kind == metadata.KindUint {
		return n.Sign() >= 0 && n.BitLen() <= int(bits)
	}
	limit := new(big.Int).Lsh(big.NewInt(1), uint(bits-1))
	return n.Cmp(new(big.Int).Neg(limit)) >= 0 && n.Cmp(new(big.Int).Sub(limit, big.NewInt(1))) <= 0
}

func (r *typeResolver) resolveEntry(e *typeEntry, depth uint32) *schemaType {
	if err := checkDeadline(r.deadline); err != nil {
		return opaqueType(e.name, uint64(e.size), err.Error())
	}
	return r.stringEncoding(r.resolveEntryLayout(e, depth), e.attributes)
}

func (r *typeResolver) resolveEntryLayout(e *typeEntry, depth uint32) *schemaType {
	if depth > r.cfg.maxDepth {
		return opaqueType(e.name, uint64(e.size), "schema depth limit exceeded")
	}
	size := uint64(e.size)
	if e.flags&dtBitValues != 0 {
		size = (size + 7) / 8
	}
	if e.unsupported != "" {
		return opaqueType(e.name, size, e.unsupported)
	}
	upper := strings.ToUpper(e.name + " " + e.typeName)
	if strings.Contains(upper, "POINTER TO") || strings.Contains(upper, "REFERENCE TO") || e.flags&(dtReference|dtInterface|dtPointer|dtOnlinePtr|dtVariant|dtIncomplete|dtHideMembers|dtMethodDeref|dtProperty|dtOversample) != 0 {
		return opaqueType(e.name, size, "pointer, interface, reference or unpublished data layout")
	}
	if len(e.dimensions) != 0 {
		count, err := countDimensions(e.dimensions, uint64(r.cfg.maxElements))
		if err != nil {
			return opaqueType(e.name, size, err.Error())
		}
		if len(e.members) != 0 {
			if count == 0 || size%count != 0 {
				return opaqueType(e.name, size, "record array stride mismatch")
			}
			element := *e
			element.name = e.name + " element"
			element.size = uint32(size / count)
			element.dimensions = nil
			return r.array(e.name, size, e.dimensions, r.resolveEntry(&element, depth+1))
		}
		return r.array(e.name, size, e.dimensions, r.resolveName(e.typeName, depth+1))
	}
	if len(e.members) != 0 {
		result := &schemaType{name: e.name, size: size, kind: metadata.KindStruct, members: make([]schemaMember, len(e.members))}
		intervals := make([][2]uint64, len(e.members))
		for i, member := range e.members {
			typeOf := r.resolveEntry(member, depth+1)
			start, length := uint64(member.offset)*8, uint64(member.size)*8
			if member.flags&dtBitValues != 0 {
				start, length = uint64(member.offset), uint64(member.size)
			}
			if length == 0 || start > size*8 || length > size*8-start {
				return opaqueType(e.name, size, "member storage outside advertised record")
			}
			if typeOf.unsupported != "" {
				result.unsupported = fmt.Sprintf("member %s: %s", member.name, typeOf.unsupported)
			}
			result.members[i] = schemaMember{member.name, typeOf, start, length, false}
			intervals[i] = [2]uint64{start, start + length}
		}
		sort.Slice(intervals, func(i, j int) bool { return intervals[i][0] < intervals[j][0] })
		for i := 1; i < len(intervals); i++ {
			if intervals[i][0] < intervals[i-1][1] {
				result.unsupported = "union or overlapping members are unsupported"
			}
		}
		return result
	}
	declaration := e.name
	if e.flags&dtDataItem != 0 {
		declaration = e.typeName
	}
	// Primitive declarations retain semantics (TIME, enums, strings), rather than
	// allowing ADST_UINT32 or a BOOL's BYTE base declaration to erase them.
	known := r.resolveDeclaration(declaration, depth)
	var result *schemaType
	if known.unsupported == "" {
		result = known
	} else if e.typeName != "" {
		base := r.resolveName(e.typeName, depth+1)
		copy := *base
		copy.name = declaration
		result = &copy
	} else {
		result = opaqueType(declaration, size, "unpublished or unsupported datatype")
	}
	if result.unsupported != "" {
		copy := *result
		copy.size = size
		return &copy
	}
	if result.size != size {
		return opaqueType(declaration, size, "datatype storage width mismatch")
	}
	copy := *result
	copy.name = declaration
	copy.enums = e.enums
	if e.flags&dtBitValues != 0 {
		if result.kind != metadata.KindBool || e.size != 1 {
			return opaqueType(declaration, size, "unsupported packed bit width")
		}
		copy.bit, copy.bits = true, 1
	}
	return &copy
}

// Attributes apply after layout resolution, including array and alias paths.
// Copy only the affected string nodes; cached base declarations stay immutable.
func (r *typeResolver) stringEncoding(schema *schemaType, attributes map[string]string) *schemaType {
	if r.cfg.stringEncodingSet || schema.kind != metadata.KindString || schema.wide {
		return schema
	}
	for name, value := range attributes {
		if !strings.EqualFold(name, "TcEncoding") {
			continue
		}
		copy := *schema
		switch {
		case strings.EqualFold(value, "UTF-8"):
			copy.utf8 = true
		case strings.EqualFold(value, "Latin-1"):
			copy.utf8 = false
		default:
			copy.unsupported = "unsupported declared STRING encoding"
			return &copy
		}
		if copy.element != nil {
			element := *copy.element
			element.utf8 = copy.utf8
			copy.element = &element
		}
		return &copy
	}
	return schema
}

func (r *typeResolver) symbol(info TagInfo) *schemaType {
	result := r.resolveName(info.TypeName, 1)
	if info.Flags&(SymFlagInterfacePointer|SymFlagReferenceTo) != 0 {
		return opaqueType(info.TypeName, uint64(info.Size), "symbol is a pointer or reference")
	}
	if uint64(info.Size) != result.size {
		copy := *result
		copy.size = uint64(info.Size)
		copy.unsupported = "symbol size does not match datatype storage"
		return &copy
	}
	return result
}

func describeType(schema *schemaType, budget *parseBudget, depth uint32) (metadata.Type, error) {
	if depth > budget.maxDepth {
		return metadata.Type{}, fmt.Errorf("description depth limit exceeded")
	}
	if err := budget.consume(1); err != nil {
		return metadata.Type{}, err
	}
	kind := schema.kind
	if schema.unsupported != "" {
		kind = metadata.KindOpaque
	}
	result := metadata.Type{Kind: kind, Bits: schema.bits, DeclaredName: schema.name, Dimensions: append([]metadata.Dimension(nil), schema.dimensions...), Unit: schema.unit, Epoch: schema.epoch, UnsupportedReason: schema.unsupported}
	if uint64(len(schema.members)) > budget.remaining {
		return metadata.Type{}, fmt.Errorf("description member limit exceeded")
	}
	result.Members = make([]metadata.Member, len(schema.members))
	for i, member := range schema.members {
		typeOf, err := describeType(member.typeOf, budget, depth+1)
		if err != nil {
			return metadata.Type{}, err
		}
		result.Members[i] = metadata.Member{Name: member.name, Type: typeOf, ReadOnly: member.readOnly}
	}
	return result, nil
}

func (c *Client) schemaFor(info TagInfo, resolver *typeResolver, snapshot *schemaSnapshot) *schemaType {
	schema := resolver.symbol(info)
	var record *symbolRecord
	if snapshot != nil {
		record = snapshot.symbols[info.Name]
	}
	if record == nil {
		record = c.lookupSymbols[info.Name]
	}
	if record == nil || (record.unsupported == "" && len(record.attributes) == 0) {
		return schema
	}
	schema = resolver.stringEncoding(schema, record.attributes)
	copy := *schema
	if record.unsupported != "" {
		copy.unsupported = record.unsupported
	}
	return &copy
}
