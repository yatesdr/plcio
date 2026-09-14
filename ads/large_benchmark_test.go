package ads

import (
	"bytes"
	"fmt"
	"os"
	"testing"
)

func BenchmarkLargeCatalog(b *testing.B) {
	for _, count := range []int{2048, 20000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			var data []byte
			for i := range count {
				data = append(data, testSymbol(fmt.Sprintf("MAIN.n%05d", i), "DINT")...)
			}
			cfg := defaultOptions()
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			b.ResetTimer()
			for b.Loop() {
				records, _, err := parseSymbolRecords(data, uint32(count), cfg)
				if err != nil || len(records) != count {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkLargeRecordArrayDecode(b *testing.B) {
	cfg := defaultOptions()
	r := newResolver(capturedTypes(b), cfg)
	schema := r.resolveName("ARRAY [-512..511] OF TEST_STRUCT", 1)
	data, err := os.ReadFile("testdata/beckhoff/MAIN.test_struct.bin")
	if err != nil {
		b.Fatal(err)
	}
	data = bytes.Repeat(data, 1024)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for b.Loop() {
		value, err := decodeValue(schema, data, cfg)
		if err != nil || len(value.([]any)) != 1024 {
			b.Fatal(err)
		}
	}
}
