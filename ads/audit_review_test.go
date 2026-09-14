package ads

import (
	"encoding/binary"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yatesdr/plcio/metadata"
)

func TestAuditPublishedReadOnlyMember(t *testing.T) {
	root := testSymbol("MAIN.record", "Record")
	binary.LittleEndian.PutUint32(root[16:20], 65)
	leaf := testSymbol("MAIN.record.locked", "DINT")
	binary.LittleEndian.PutUint32(leaf[20:24], 0x20)
	symbols := append(root, leaf...)
	types := wireDatatype("Record", "", 4, 0, 1, 65,
		wireDatatype("locked", "DINT", 4, 0, 2, 3))
	var writes atomic.Int32
	c := testClient(t, func(req []byte) []byte {
		switch binary.LittleEndian.Uint32(req[38:42]) {
		case 0xf00f:
			info := make([]byte, 24)
			binary.LittleEndian.PutUint32(info[0:4], 2)
			binary.LittleEndian.PutUint32(info[4:8], uint32(len(symbols)))
			binary.LittleEndian.PutUint32(info[8:12], 1)
			binary.LittleEndian.PutUint32(info[12:16], uint32(len(types)))
			return testReadReply(info)
		case 0xf00b:
			return testReadReply(symbols)
		case 0xf00e:
			return testReadReply(types)
		case 0xf003:
			return testReadReply([]byte{42, 0, 0, 0})
		case 0xf005:
			if binary.LittleEndian.Uint16(req[22:24]) != 3 {
				t.Error("unexpected value read")
			}
			writes.Add(1)
			return []byte{0, 0, 0, 0}
		case 0xf006:
			return []byte{0, 0, 0, 0}
		default:
			t.Errorf("unexpected service: %x", req)
			return nil
		}
	})
	c.catalogUnavailable = false
	description, err := c.Describe("MAIN.record")
	if err != nil {
		t.Fatal(err)
	}
	if len(description.Type.Members) != 1 || !description.Type.Members[0].ReadOnly {
		t.Errorf("published read-only child described as writable: %+v", description.Type.Members)
	}
	if err := c.Write("MAIN.record", map[string]any{"locked": int64(25)}); err == nil || writes.Load() != 0 {
		t.Errorf("whole-record write ignored published child access: err=%v value writes=%d", err, writes.Load())
	}
}

func TestAuditArrayDatatypeEncodingAttribute(t *testing.T) {
	wire := wireDatatype("EncodedArray", "STRING(4)", 5, 0, 0x1001, 30)
	binary.LittleEndian.PutUint16(wire[38:40], 1)
	wire = append(wire, 0, 0, 0, 0, 1, 0, 0, 0) // lower bound 0, length 1
	wire = append(wire, 1, 0, 10, 5)            // one attribute, name/value lengths
	wire = append(wire, []byte("TcEncoding\x00UTF-8\x00")...)
	binary.LittleEndian.PutUint32(wire[:4], uint32(len(wire)))
	cfg := defaultOptions()
	entries, err := parseTypeTable(wire, 1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	schema := newResolver(entries, cfg).resolveName("EncodedArray", 1)
	value, err := decodeValue(schema, []byte{0xc3, 0xa9, 0, 0, 0}, cfg)
	if err != nil || !reflect.DeepEqual(value, []string{"é"}) {
		t.Fatalf("array TcEncoding attribute was lost: got=%#v error=%v", value, err)
	}
}

func TestAuditUnsupportedLayoutWalkBudget(t *testing.T) {
	cfg := defaultOptions()
	cfg.maxElements = 100
	var wire []byte
	previous := "BYTE"
	for i := 0; i < 24; i++ {
		name := fmt.Sprintf("Union%d", i)
		childCode := uint32(65)
		if i == 0 {
			childCode = 17
		}
		wire = append(wire, wireDatatype(name, "", 1, 0, 1, 65,
			wireDatatype("left", previous, 1, 0, 2, childCode),
			wireDatatype("right", previous, 1, 0, 2, childCode))...)
		previous = name
	}
	entries, parseErr := parseTypeTable(wire, 24, cfg)
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	t.Logf("metadata input=%d bytes, 24 types; configured expansion limit=%d", len(wire), cfg.maxElements)
	resolver := newResolver(entries, cfg)
	schema := resolver.resolveName(previous, 1)
	if schema.unsupported == "" {
		t.Fatal("expected unsupported overlapping layout")
	}
	// Deterministic work bound, including the advanced raw-storage path.
	budget := parseBudget{remaining: 100, maxDepth: cfg.maxDepth}
	// Each serialized level adds a shared record and two member aliases:
	// at most four edge/node attempts per level, rather than doubling work.
	if readOnly, err := knownReadOnly(schema, &budget); err != nil || readOnly || budget.remaining < 100-4*24 {
		t.Fatalf("shared graph exceeded linear access work: remaining=%d readOnly=%v err=%v", budget.remaining, readOnly, err)
	}
	if _, err := encodeValue(schema, []byte{0}, cfg); err != nil {
		t.Fatalf("bounded opaque storage write: %v", err)
	}
	budget = parseBudget{remaining: 10, maxDepth: cfg.maxDepth}
	if _, err := knownReadOnly(schema, &budget); err == nil {
		t.Fatal("access walk ignored configured work limit")
	}
	c := testClient(t, func(req []byte) []byte { t.Errorf("unexpected I/O %x", req); return nil })
	c.cfg = cfg
	c.cfg.timeout = time.Millisecond
	c.fallbackResolver = resolver
	c.symbols["MAIN.union"] = &SymbolEntry{Info: TagInfo{Name: "MAIN.union", TypeName: previous, TypeCode: TypeUnknown, Size: 1}}
	start := time.Now()
	err := c.Write("MAIN.union", map[string]any{})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("unsupported overlapping layout accepted")
	}
	t.Logf("one-millisecond operation took %s: %v", elapsed, err)
	if elapsed > 20*time.Millisecond {
		t.Errorf("unsupported layout traversed exponentially before expansion/deadline guard")
	}
}

func TestAuditUnsupportedTypeEncodingAttribute(t *testing.T) {
	cfg := defaultOptions()
	e := &typeEntry{name: "Encoded", typeName: "STRING(4)", size: 5, flags: 1, version: 1,
		attributes: map[string]string{"TcEncoding": "UTF-16"}}
	schema := newResolver(map[string]*typeEntry{"Encoded": e}, cfg).resolveName("Encoded", 1)
	if schema.kind != metadata.KindOpaque && schema.unsupported == "" {
		t.Error("unknown datatype-level encoding silently accepted as Latin-1")
	}
}

func TestAuditLatin1AliasOverridesUTF8Base(t *testing.T) {
	cfg := defaultOptions()
	entries := map[string]*typeEntry{
		"Base":  {name: "Base", typeName: "STRING(4)", size: 5, flags: 1, version: 1, attributes: map[string]string{"TcEncoding": "UTF-8"}},
		"Alias": {name: "Alias", typeName: "Base", size: 5, flags: 1, version: 1, attributes: map[string]string{"TcEncoding": "Latin-1"}},
	}
	schema := newResolver(entries, cfg).resolveName("Alias", 1)
	got, err := decodeValue(schema, []byte{0xe9, 0, 0, 0, 0}, cfg)
	if err != nil || got != "é" {
		t.Errorf("Latin-1 alias retained UTF-8 base encoding: got=%#v err=%v", got, err)
	}
}
