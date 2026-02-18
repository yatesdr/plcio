package driver

import (
	"testing"

	"github.com/yatesdr/plcio/pccc"
)

func TestPcccContiguousRuns(t *testing.T) {
	// Helper: builds element lookup from a map of index -> element
	makeElemOf := func(m map[int]uint16) func(int) uint16 {
		return func(i int) uint16 { return m[i] }
	}

	tests := []struct {
		name    string
		indices []int
		elems   map[int]uint16
		want    [][]int
	}{
		{
			name:    "empty",
			indices: nil,
			elems:   nil,
			want:    nil,
		},
		{
			name:    "single element",
			indices: []int{0},
			elems:   map[int]uint16{0: 5},
			want:    [][]int{{0}},
		},
		{
			name:    "two contiguous",
			indices: []int{0, 1},
			elems:   map[int]uint16{0: 5, 1: 6},
			want:    [][]int{{0, 1}},
		},
		{
			name:    "three contiguous",
			indices: []int{2, 5, 7},
			elems:   map[int]uint16{2: 10, 5: 11, 7: 12},
			want:    [][]int{{2, 5, 7}},
		},
		{
			name:    "gap splits into two runs",
			indices: []int{0, 1, 2, 3},
			elems:   map[int]uint16{0: 0, 1: 1, 2: 5, 3: 6},
			want:    [][]int{{0, 1}, {2, 3}},
		},
		{
			name:    "all separate",
			indices: []int{0, 1, 2},
			elems:   map[int]uint16{0: 0, 1: 10, 2: 20},
			want:    [][]int{{0}, {1}, {2}},
		},
		{
			name:    "mixed: run, single, run",
			indices: []int{0, 1, 2, 3, 4},
			elems:   map[int]uint16{0: 0, 1: 1, 2: 5, 3: 7, 4: 8},
			want:    [][]int{{0, 1}, {2}, {3, 4}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pcccContiguousRuns(tt.indices, makeElemOf(tt.elems))
			if len(got) != len(tt.want) {
				t.Fatalf("got %d runs, want %d: %v", len(got), len(tt.want), got)
			}
			for i := range got {
				if len(got[i]) != len(tt.want[i]) {
					t.Errorf("run[%d] len = %d, want %d: %v", i, len(got[i]), len(tt.want[i]), got[i])
					continue
				}
				for j := range got[i] {
					if got[i][j] != tt.want[i][j] {
						t.Errorf("run[%d][%d] = %d, want %d", i, j, got[i][j], tt.want[i][j])
					}
				}
			}
		})
	}
}

func TestPcccReadBatchGrouping(t *testing.T) {
	// Verify that the Read method correctly identifies bulkable vs non-bulkable addresses.
	// We can't do a full integration test without a PLC, but we can test the address
	// classification logic by checking which addresses would be grouped.

	// Bulkable: full-element reads (SubElement=0, BitNumber<0)
	bulkable := []string{"N7:0", "N7:1", "N7:2", "F8:0", "F8:1"}
	for _, addr := range bulkable {
		parsed, err := pccc.ParseAddress(addr)
		if err != nil {
			t.Fatalf("ParseAddress(%q): %v", addr, err)
		}
		if parsed.SubElement != 0 || parsed.BitNumber >= 0 {
			t.Errorf("%q should be bulkable (sub=%d, bit=%d)", addr, parsed.SubElement, parsed.BitNumber)
		}
	}

	// Not bulkable: bit access or sub-element access
	notBulkable := []string{"B3:0/5", "T4:0.ACC", "T4:0.DN", "C5:0.PRE", "N7:0/3"}
	for _, addr := range notBulkable {
		parsed, err := pccc.ParseAddress(addr)
		if err != nil {
			t.Fatalf("ParseAddress(%q): %v", addr, err)
		}
		isBulkable := parsed.SubElement == 0 && parsed.BitNumber < 0
		if isBulkable {
			t.Errorf("%q should NOT be bulkable (sub=%d, bit=%d)", addr, parsed.SubElement, parsed.BitNumber)
		}
	}
}

func TestDecodeValueExported(t *testing.T) {
	// Verify that the exported DecodeValue works correctly for basic types.
	tests := []struct {
		name  string
		addr  string
		data  []byte
		check func(interface{}) bool
	}{
		{
			name: "INT positive",
			addr: "N7:0",
			data: []byte{0x2A, 0x00}, // 42 LE
			check: func(v interface{}) bool {
				i, ok := v.(int16)
				return ok && i == 42
			},
		},
		{
			name: "INT negative",
			addr: "N7:1",
			data: []byte{0xD6, 0xFF}, // -42 LE
			check: func(v interface{}) bool {
				i, ok := v.(int16)
				return ok && i == -42
			},
		},
		{
			name: "FLOAT",
			addr: "F8:0",
			data: []byte{0xC3, 0xF5, 0x48, 0x40}, // 3.14 IEEE 754
			check: func(v interface{}) bool {
				f, ok := v.(float32)
				return ok && f > 3.13 && f < 3.15
			},
		},
		{
			name: "LONG",
			addr: "L10:0",
			data: []byte{0xA0, 0x86, 0x01, 0x00}, // 100000 LE
			check: func(v interface{}) bool {
				i, ok := v.(int32)
				return ok && i == 100000
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, err := pccc.ParseAddress(tt.addr)
			if err != nil {
				t.Fatalf("ParseAddress(%q): %v", tt.addr, err)
			}
			got := pccc.DecodeValue(addr, tt.data)
			if !tt.check(got) {
				t.Errorf("DecodeValue(%q, %X) = %v (%T), unexpected", tt.addr, tt.data, got, got)
			}
		})
	}
}
