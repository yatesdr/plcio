package ads

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCapturedChecksums(t *testing.T) {
	for _, directory := range []string{"testdata/beckhoff", "testdata/beckhoff-bit-lookups-2026-09-14"} {
		data, err := os.ReadFile(filepath.Join(directory, "sha256.json"))
		if err != nil {
			t.Fatal(err)
		}
		var sums map[string]string
		if err := json.Unmarshal(data, &sums); err != nil {
			t.Fatal(err)
		}
		for name, want := range sums {
			data, err := os.ReadFile(filepath.Join(directory, name))
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != want {
				t.Fatalf("capture %s changed", name)
			}
		}
	}
}

func BenchmarkPrimitiveGoValue(b *testing.B) {
	v := &TagValue{DataType: TypeInt32, Bytes: []byte{25, 0, 0, 0}, Count: 1}
	b.ReportAllocs()
	for b.Loop() {
		_ = v.GoValue()
	}
}

func BenchmarkPrimitiveEncode(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := EncodeValueWithType(int64(25), TypeInt32); err != nil {
			b.Fatal(err)
		}
	}
}
