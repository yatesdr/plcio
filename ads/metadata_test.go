package ads

import (
	"encoding/binary"
	"os"
	"testing"
)

func capturedTypes(t testing.TB) map[string]*typeEntry {
	t.Helper()
	data, err := os.ReadFile("testdata/beckhoff/datatypes.bin")
	if err != nil {
		t.Fatal(err)
	}
	types, err := parseTypeTable(data, 75, defaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return types
}

func TestCapturedDatatypeTable(t *testing.T) {
	types := capturedTypes(t)
	if len(types) != 75 {
		t.Fatalf("types=%d", len(types))
	}
	record := types["TEST_STRUCT"]
	if record.size != 124 || len(record.members) != 5 {
		t.Fatal("record size/members")
	}
	for i, want := range []struct {
		name         string
		offset, size uint32
	}{{"my_byte", 0, 1}, {"my_dint", 4, 4}, {"my_sint", 8, 1}, {"my_string", 9, 81}, {"my_dint_array", 92, 32}} {
		m := record.members[i]
		if m.name != want.name || m.offset != want.offset || m.size != want.size {
			t.Fatalf("member %d: %+v", i, m)
		}
	}
	packed := types["PACKED_STRUCT"]
	for i, m := range packed.members {
		if m.flags&0x20 == 0 || m.offset != uint32(i) || m.size != 1 {
			t.Fatalf("packed member %d: %+v", i, m)
		}
	}
	enum := types["PLC.EPlcPersistentStatus"]
	if len(enum.enums) != 3 || len(enum.enums["PS_All"]) != 1 || enum.enums["PS_All"][0] != 1 {
		t.Fatalf("captured enum: %+v", enum.enums)
	}
	implicit := types["_Implicit_KindOfTask"]
	if len(implicit.enums) != 4 || len(implicit.attributes) != 2 {
		t.Fatalf("attribute/enum extension: %+v", implicit)
	}
	for name, e := range types {
		if e.unsupported != "" {
			t.Errorf("unaccounted captured extension %s: %s", name, e.unsupported)
		}
	}
}

func TestDatatypeParserBudgetsAndMalformed(t *testing.T) {
	data, err := os.ReadFile("testdata/beckhoff/datatypes.bin")
	if err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*options){func(o *options) { o.maxMetadata = 100 }, func(o *options) { o.maxTypes = 74 }, func(o *options) { o.maxElements = 10 }, func(o *options) { o.maxDepth = 1 }} {
		cfg := defaultOptions()
		edit(&cfg)
		if _, err := parseTypeTable(data, 75, cfg); err == nil {
			t.Fatal("accepted exceeded metadata budget")
		}
	}
	first := data[:binary.LittleEndian.Uint32(data[:4])]
	for _, mutate := range []func([]byte) []byte{
		func(b []byte) []byte { binary.LittleEndian.PutUint32(b[:4], 0); return b },
		func(b []byte) []byte { binary.LittleEndian.PutUint16(b[32:34], 65535); return b },
		func(b []byte) []byte { binary.LittleEndian.PutUint16(b[38:40], 65535); return b },
		func(b []byte) []byte { binary.LittleEndian.PutUint16(b[40:42], 65535); return b },
		func(b []byte) []byte { return b[:len(b)-1] },
	} {
		bad := mutate(append([]byte(nil), first...))
		budget := parseBudget{remaining: 1000000, maxDepth: 64}
		if _, err := parseTypeEntry(bad, &budget, 1); err == nil {
			t.Fatal("malformed datatype accepted")
		}
	}
}

func FuzzDatatypeParser(f *testing.F) {
	data, err := os.ReadFile("testdata/beckhoff/datatypes.bin")
	if err != nil {
		f.Fatal(err)
	}
	var seeds func([]byte)
	seeds = func(data []byte) {
		f.Add(data)
		nl, tl, cl := int(binary.LittleEndian.Uint16(data[32:34])), int(binary.LittleEndian.Uint16(data[34:36])), int(binary.LittleEndian.Uint16(data[36:38]))
		dims, members := int(binary.LittleEndian.Uint16(data[38:40])), int(binary.LittleEndian.Uint16(data[40:42]))
		offset := 45 + nl + tl + cl + dims*8
		for range members {
			size := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
			seeds(data[offset : offset+size])
			offset += size
		}
	}
	for offset := 0; offset < len(data); {
		size := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
		seeds(data[offset : offset+size])
		offset += size
	}
	f.Add([]byte{0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		budget := parseBudget{remaining: 1000000, maxDepth: 64}
		parseTypeEntry(data, &budget, 1)
		parseTypeTable(data, 1, defaultOptions())
	})
}
