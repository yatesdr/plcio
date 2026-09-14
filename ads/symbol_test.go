package ads

import (
	"encoding/binary"
	"fmt"
	"os"
	"testing"
)

func testSymbol(name, typ string) []byte {
	data := make([]byte, 33+len(name)+len(typ))
	binary.LittleEndian.PutUint32(data[:4], uint32(len(data)))
	binary.LittleEndian.PutUint32(data[12:16], 4)
	binary.LittleEndian.PutUint32(data[16:20], 3)
	binary.LittleEndian.PutUint16(data[24:26], uint16(len(name)))
	binary.LittleEndian.PutUint16(data[26:28], uint16(len(typ)))
	copy(data[30:], name)
	copy(data[31+len(name):], typ)
	return data
}

func TestRejectMalformedSymbolCatalog(t *testing.T) {
	valid := testSymbol("MAIN.n", "DINT")
	for name, mutate := range map[string]func([]byte) []byte{
		"zero entry":      func(b []byte) []byte { binary.LittleEndian.PutUint32(b[:4], 0); return b },
		"wrapped offsets": func(b []byte) []byte { binary.LittleEndian.PutUint16(b[24:26], 65535); return b },
		"missing NUL":     func(b []byte) []byte { b[36] = 1; return b },
		"truncated":       func(b []byte) []byte { return b[:len(b)-1] },
		"duplicate":       func(b []byte) []byte { return append(b, b...) },
		"trailing":        func(b []byte) []byte { return append(b, 0) },
	} {
		t.Run(name, func(t *testing.T) {
			b := mutate(append([]byte(nil), valid...))
			count := uint32(1)
			if name == "duplicate" {
				count = 2
			}
			if _, err := parseSymbolTable(b, count, 100000); err == nil {
				t.Fatal("malformed catalog accepted")
			}
		})
	}
	if _, err := parseSymbolTable(valid, 0xffffffff, 100000); err == nil {
		t.Fatal("unbounded count accepted")
	}
	if _, err := parseSymbolTable(nil, 0, 100000); err != nil {
		t.Fatal("valid empty catalog rejected")
	}
}

func FuzzSymbolParser(f *testing.F) {
	capture, err := os.ReadFile("testdata/beckhoff/symbols.bin")
	if err != nil {
		f.Fatal(err)
	}
	offset := 0
	for offset < len(capture) {
		size := int(binary.LittleEndian.Uint32(capture[offset : offset+4]))
		f.Add(capture[offset : offset+size])
		offset += size
	}
	f.Add(testSymbol("MAIN.n", "DINT"))
	f.Add([]byte{0, 0, 0, 0})
	for i := 1; i <= 4; i++ {
		data, err := os.ReadFile(fmt.Sprintf("testdata/beckhoff-bit-lookups-2026-09-14/MAIN.test_bitpacked_struct.my_bit%d.f009.bin", i))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		parseSymbolInfo(data)
		parseSymbolTable(data, 1, 100000)
		budget := parseBudget{remaining: 256, maxDepth: 8}
		parseSymbolRecordFields(data, &budget, true)
	})
}
