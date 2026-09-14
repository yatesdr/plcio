package ads

import (
	"reflect"
	"testing"

	"github.com/yatesdr/plcio/metadata"
)

func TestCapturedSchemasAndOwnedDescription(t *testing.T) {
	r := newResolver(capturedTypes(t), defaultOptions())
	record := r.resolveName("TEST_STRUCT", 1)
	if record.unsupported != "" || record.kind != metadata.KindStruct || record.size != 124 {
		t.Fatalf("record: %+v", record)
	}
	for i, want := range []struct {
		kind   metadata.Kind
		bits   uint16
		offset uint64
	}{{metadata.KindUint, 8, 0}, {metadata.KindInt, 32, 32}, {metadata.KindInt, 8, 64}, {metadata.KindString, 0, 72}, {metadata.KindInt, 32, 736}} {
		member := record.members[i]
		if member.typeOf.kind != want.kind || member.typeOf.bits != want.bits || member.offsetBits != want.offset {
			t.Fatalf("member %d: %+v", i, member)
		}
	}
	expected := []metadata.Dimension{{LowerBound: 1, Length: 2}, {LowerBound: 1, Length: 3}}
	for _, name := range []string{"ARRAY [1..2] OF ARRAY [1..3] OF INT", "ARRAY [1..2,1..3] OF INT"} {
		schema := r.resolveName(name, 1)
		if schema.unsupported != "" || schema.size != 12 || schema.bits != 16 || !reflect.DeepEqual(schema.dimensions, expected) {
			t.Fatalf("multidimensional %s: %+v", name, schema)
		}
	}
	packed := r.resolveName("PACKED_STRUCT", 1)
	if packed.unsupported != "" {
		t.Fatal(packed.unsupported)
	}
	for i, member := range packed.members {
		if member.offsetBits != uint64(i) || member.sizeBits != 1 || !member.typeOf.bit {
			t.Fatalf("packed member: %+v", member)
		}
	}
	budget := parseBudget{remaining: 1000000, maxDepth: 64}
	description, err := describeType(record, &budget, 1)
	if err != nil {
		t.Fatal(err)
	}
	description.Members[0].Name = "changed"
	description.Members[4].Type.Dimensions[0].Length = 999
	budget = parseBudget{remaining: 1000000, maxDepth: 64}
	again, err := describeType(record, &budget, 1)
	if err != nil || again.Members[0].Name != "my_byte" || again.Members[4].Type.Dimensions[0].Length != 8 {
		t.Fatalf("cache mutation: %+v %v", again, err)
	}
}

func TestSchemaResolutionBoundaries(t *testing.T) {
	r := newResolver(map[string]*typeEntry{
		"Alias":    {name: "Alias", typeName: "INT", size: 2, flags: 1, version: 1},
		"CycleA":   {name: "CycleA", typeName: "CycleB", size: 2, flags: 1, version: 1},
		"CycleB":   {name: "CycleB", typeName: "CycleA", size: 2, flags: 1, version: 1},
		"Missing":  {name: "Missing", typeName: "Unpublished", size: 4, flags: 1, version: 1},
		"BadArray": {name: "BadArray", typeName: "INT", size: 3, dimensions: []metadata.Dimension{{Length: 2}}, flags: 1, version: 1},
		"Unknown":  {name: "Unknown", size: 4, unsupported: "unknown extension", flags: 1, version: 1},
	}, defaultOptions())
	if got := r.resolveName("Alias", 1); got.kind != metadata.KindInt || got.bits != 16 || got.name != "Alias" {
		t.Fatal(got)
	}
	for _, name := range []string{"CycleA", "Missing", "BadArray", "Unknown", "ARRAY [-9223372036854775808..9223372036854775807] OF INT", "ARRAY [0..4294967295] OF BYTE", "INT(-32769..32767)", "UINT(-1..1)", "INT(2..1)"} {
		if got := r.resolveName(name, 1); got.unsupported == "" {
			t.Fatalf("unsupported %s accepted: %+v", name, got)
		}
	}
	for _, name := range []string{"INT(-32768..32767)", "UINT(0..65535)", "ULINT(0..18446744073709551615)", "LINT(-9223372036854775808..9223372036854775807)"} {
		if got := r.resolveName(name, 1); got.unsupported != "" || got.min == nil {
			t.Fatalf("subrange %s: %+v", name, got)
		}
	}
	singleton := r.resolveName("ARRAY [-3..-3] OF INT", 1)
	if singleton.unsupported != "" || singleton.size != 2 || len(singleton.dimensions) != 1 || singleton.dimensions[0].LowerBound != -3 || singleton.dimensions[0].Length != 1 {
		t.Fatal(singleton)
	}
}

func FuzzSchemaDeclaration(f *testing.F) {
	for _, name := range []string{"DINT", "STRING(80)", "WSTRING(2)", "ARRAY [-3..-3] OF INT", "ARRAY [1..2] OF ARRAY [1..3] OF INT", "ARRAY [1..2,1..3] OF INT", "LINT(-9223372036854775808..9223372036854775807)", "UINT(-1..1)", "ARRAY [-9223372036854775808..9223372036854775807] OF INT"} {
		f.Add(name)
	}
	f.Fuzz(func(t *testing.T, name string) {
		if len(name) > 65535 {
			return
		}
		cfg := defaultOptions()
		cfg.maxDepth, cfg.maxElements, cfg.maxPayload = 8, 256, 4096
		schema := newResolver(nil, cfg).resolveName(name, 1)
		budget := parseBudget{remaining: 256, maxDepth: 8}
		_, _ = describeType(schema, &budget, 1)
		if schema.unsupported == "" && schema.size <= 4096 {
			_, _ = decodeValue(schema, make([]byte, int(schema.size)), cfg)
		}
	})
}
