package ads

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublishedNestedAccessIsInstanceScoped(t *testing.T) {
	var symbols []byte
	for _, name := range []string{"MAIN.a", "MAIN.b", "MAIN.items[1]", "MAIN.items[2]"} {
		wire := testSymbol(name, "Record")
		binary.LittleEndian.PutUint32(wire[16:20], 65)
		symbols = append(symbols, wire...)
	}
	arrayWire := testSymbol("MAIN.items", "ARRAY [1..2] OF Record")
	binary.LittleEndian.PutUint32(arrayWire[12:16], 8)
	binary.LittleEndian.PutUint32(arrayWire[16:20], 65)
	symbols = append(symbols, arrayWire...)
	for _, name := range []string{"MAIN.a.inner", "MAIN.items[1].inner.locked"} {
		typeName, code := "DINT", uint32(3)
		if name == "MAIN.a.inner" {
			typeName, code = "Inner", 65
		}
		wire := testSymbol(name, typeName)
		binary.LittleEndian.PutUint32(wire[16:20], code)
		binary.LittleEndian.PutUint32(wire[20:24], 0x20)
		symbols = append(symbols, wire...)
	}
	types := wireDatatype("Inner", "", 4, 0, 1, 65, wireDatatype("locked", "DINT", 4, 0, 2, 3))
	types = append(types, wireDatatype("Record", "", 4, 0, 1, 65, wireDatatype("inner", "Inner", 4, 0, 2, 65))...)
	var writes atomic.Int32
	c := testClient(t, func(req []byte) []byte {
		switch binary.LittleEndian.Uint32(req[38:42]) {
		case 0xf00f:
			info := make([]byte, 24)
			binary.LittleEndian.PutUint32(info[:4], 7)
			binary.LittleEndian.PutUint32(info[4:8], uint32(len(symbols)))
			binary.LittleEndian.PutUint32(info[8:12], 2)
			binary.LittleEndian.PutUint32(info[12:16], uint32(len(types)))
			return testReadReply(info)
		case 0xf00b:
			return testReadReply(symbols)
		case 0xf00e:
			return testReadReply(types)
		case 0xf009:
			name := strings.TrimRight(string(req[54:]), "\x00")
			wire := testSymbol(name, "DINT")
			if name == "MAIN.b.inner.locked" {
				binary.LittleEndian.PutUint32(wire[20:24], 0x20)
			}
			return testReadReply(wire)
		case 0xf003:
			return testReadReply([]byte{42, 0, 0, 0})
		case 0xf005:
			writes.Add(1)
			if !bytes.Equal(req[50:], []byte{25, 0, 0, 0}) {
				t.Errorf("write payload %x", req[50:])
			}
			return []byte{0, 0, 0, 0}
		case 0xf006:
			return []byte{0, 0, 0, 0}
		}
		t.Errorf("unexpected service %x", req)
		return nil
	})
	c.catalogUnavailable = false
	value := map[string]any{"inner": map[string]any{"locked": int64(25)}}
	array, err := c.Describe("MAIN.items")
	if err != nil || len(array.Type.Dimensions) != 1 || !array.Type.Members[0].Type.Members[0].ReadOnly {
		t.Fatalf("restricted array element description: %+v %v", array, err)
	}
	for _, input := range []any{[]any{value, value}, make([]byte, 8)} {
		if err := c.Write("MAIN.items", input); err == nil || writes.Load() != 0 {
			t.Fatalf("array write ignored indexed restriction: %v writes=%d", err, writes.Load())
		}
	}
	for _, name := range []string{"MAIN.a", "MAIN.items[1]"} {
		desc, err := c.Describe(name)
		if err != nil || !desc.Writable || !desc.Type.Members[0].Type.Members[0].ReadOnly {
			t.Fatalf("nested restriction %s: %+v %v", name, desc, err)
		}
		for _, input := range []any{value, []byte{25, 0, 0, 0}} {
			if err := c.Write(name, input); err == nil || writes.Load() != 0 {
				t.Fatalf("restricted semantic/raw write %s: %v writes=%d", name, err, writes.Load())
			}
		}
	}
	for _, name := range []string{"MAIN.b", "MAIN.items[2]"} {
		desc, err := c.Describe(name)
		if err != nil || desc.Type.Members[0].ReadOnly || desc.Type.Members[0].Type.Members[0].ReadOnly {
			t.Fatalf("other instance contaminated %s: %+v %v", name, desc, err)
		}
		if err := c.Write(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if writes.Load() != 2 {
		t.Fatalf("value writes %d", writes.Load())
	}
	child, err := c.Describe("MAIN.a.inner.locked")
	if err != nil || child.Writable {
		t.Fatalf("ancestor restriction lost: %+v %v", child, err)
	}
	// An omitted child lookup establishes access without changing membership.
	child, err = c.Describe("MAIN.b.inner.locked")
	if err != nil || child.Writable {
		t.Fatalf("lookup restriction lost: %+v %v", child, err)
	}
	if err := c.Write("MAIN.b", []byte{25, 0, 0, 0}); err == nil || writes.Load() != 2 {
		t.Fatalf("known omitted child did not protect parent: %v writes=%d", err, writes.Load())
	}
}

func encodingDatatype(name, base, encoding string, array bool) []byte {
	size := uint32(5)
	if array {
		size = 10
	}
	wire := wireDatatype(name, base, size, 0, 0x1001, 30)
	if array {
		binary.LittleEndian.PutUint16(wire[38:40], 1)
		wire = append(wire, 0, 0, 0, 0, 2, 0, 0, 0)
	}
	wire = append(wire, 1, 0, 10, byte(len(encoding)))
	wire = append(wire, []byte("TcEncoding\x00"+encoding+"\x00")...)
	binary.LittleEndian.PutUint32(wire[:4], uint32(len(wire)))
	return wire
}

func TestPublishedArrayEncodingReadsWritesAndOverrides(t *testing.T) {
	wire := encodingDatatype("UTF8", "STRING(4)", "UTF-8", true)
	wire = append(wire, encodingDatatype("Latin", "UTF8", "Latin-1", false)...)
	// An alias to the complete array retains the array's 10-byte storage.
	firstSize := int(binary.LittleEndian.Uint32(wire[:4]))
	binary.LittleEndian.PutUint32(wire[firstSize+16:firstSize+20], 10)
	wire = append(wire, encodingDatatype("Unsupported", "STRING(4)", "UTF-16", true)...)
	cfg := defaultOptions()
	entries, err := parseTypeTable(wire, 3, cfg)
	if err != nil {
		t.Fatal(err)
	}
	utf8Bytes := []byte{0xc3, 0xa9, 0, 0, 0, 0xc3, 0xa9, 0, 0, 0}
	latinBytes := []byte{0xe9, 0, 0, 0, 0, 0xe9, 0, 0, 0, 0}
	for _, connection := range []string{"", "UTF-8", "Latin-1"} {
		cfg := defaultOptions()
		if connection != "" {
			WithStringEncoding(connection)(&cfg)
		}
		r := newResolver(entries, cfg)
		for _, name := range []string{"UTF8", "Latin", "Unsupported"} {
			schema := r.resolveName(name, 1)
			if name == "Unsupported" && connection == "" {
				if _, err := decodeValue(schema, latinBytes, cfg); err == nil {
					t.Fatal("unsupported array encoding decoded")
				}
				if _, err := encodeValue(schema, []string{"é", "é"}, cfg); err == nil {
					t.Fatal("unsupported array encoding encoded")
				}
				continue
			}
			wantBytes := latinBytes
			if connection == "UTF-8" || (connection == "" && name == "UTF8") {
				wantBytes = utf8Bytes
			}
			value, err := decodeValue(schema, wantBytes, cfg)
			if err != nil || !reflect.DeepEqual(value, []string{"é", "é"}) {
				t.Fatalf("read %s override %q: %#v %v", name, connection, value, err)
			}
			encoded, err := encodeValue(schema, []string{"é", "é"}, cfg)
			if err != nil || !bytes.Equal(encoded, wantBytes) {
				t.Fatalf("write %s override %q: %x %v", name, connection, encoded, err)
			}
		}
		// Symbol-level Latin-1 also overrides the UTF-8 array without mutating it.
		symbolWire := testSymbol("MAIN.text", "UTF8")
		binary.LittleEndian.PutUint32(symbolWire[12:16], 10)
		binary.LittleEndian.PutUint32(symbolWire[16:20], 30)
		binary.LittleEndian.PutUint32(symbolWire[20:24], SymFlagAttributes)
		symbolWire = append(symbolWire, 1, 0, 10, 7)
		symbolWire = append(symbolWire, []byte("TcEncoding\x00Latin-1\x00")...)
		binary.LittleEndian.PutUint32(symbolWire[:4], uint32(len(symbolWire)))
		budget := parseBudget{remaining: 100, maxDepth: 64}
		record, err := parseSymbolRecord(symbolWire, &budget)
		if err != nil {
			t.Fatal(err)
		}
		c := &Client{lookupSymbols: map[string]*symbolRecord{record.info.Name: record}}
		symbol := c.schemaFor(record.info, r, nil)
		want := latinBytes
		if connection == "UTF-8" {
			want = utf8Bytes
		}
		if got, err := encodeValue(symbol, []string{"é", "é"}, cfg); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("symbol array override %q: %x %v", connection, got, err)
		}
		baseBytes := utf8Bytes
		if connection == "Latin-1" {
			baseBytes = latinBytes
		}
		if got, err := encodeValue(r.resolveName("UTF8", 1), []string{"é", "é"}, cfg); err != nil || !bytes.Equal(got, baseBytes) {
			t.Fatalf("symbol override mutated base array: %x %v", got, err)
		}
	}
}

func TestSchemaValidationHonorsDeadlineAndWorkLimits(t *testing.T) {
	cfg := defaultOptions()
	cfg.deadline = time.Now().Add(-time.Second)
	schema := newResolver(nil, cfg).resolveName("DINT", 1)
	for _, value := range []any{int64(25), []byte{25, 0, 0, 0}} {
		if _, err := encodeValue(schema, value, cfg); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("encode ignored deadline: %v", err)
		}
	}
	if value, err := decodeValue(schema, []byte{25, 0, 0, 0}, cfg); value != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("decode ignored deadline: %#v %v", value, err)
	}
	// Logical expansion of a small shared graph must stop at the configured
	// count, rather than recomputing a complete subtree with a fresh budget.
	for range 24 {
		schema = &schemaType{members: []schemaMember{{typeOf: schema}, {typeOf: schema}}}
	}
	if _, err := valueExpansion(schema, 100, 1, 64, time.Time{}); err == nil {
		t.Fatal("shared graph ignored logical expansion limit")
	}
}
