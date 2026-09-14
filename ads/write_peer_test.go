package ads

import (
	"bytes"
	"encoding/binary"
	"os"
	"sync/atomic"
	"testing"
)

func TestOrdinaryRecordAndDirectMemberWritesUseOneValueRequest(t *testing.T) {
	var writes, lookups atomic.Int32
	record, err := os.ReadFile("testdata/beckhoff/MAIN.test_struct.bin")
	if err != nil {
		t.Fatal(err)
	}
	c := testClient(t, func(req []byte) []byte {
		group := binary.LittleEndian.Uint32(req[38:])
		switch group {
		case 0xf009:
			lookups.Add(1)
			if !bytes.Equal(req[54:], []byte("MAIN.test_struct.my_dint\x00")) {
				t.Errorf("lookup %x", req)
			}
			return testReadReply(testSymbol("MAIN.test_struct.my_dint", "DINT"))
		case 0xf003:
			return testReadReply([]byte{43, 0, 0, 0})
		case 0xf005:
			if binary.LittleEndian.Uint16(req[22:]) != 3 {
				t.Error("value read in ordinary write")
				return nil
			}
			expected := record
			if binary.LittleEndian.Uint32(req[42:]) == 43 {
				expected = []byte{25, 0, 0, 0}
			}
			if !bytes.Equal(req[50:], expected) {
				t.Errorf("write %x want %x", req[50:], expected)
			}
			writes.Add(1)
			return []byte{0, 0, 0, 0}
		case 0xf006:
			return []byte{0, 0, 0, 0}
		}
		t.Errorf("unexpected %x", req)
		return nil
	})
	c.catalogUnavailable = false
	c.symbolsLoaded = true
	c.snapshot = &schemaSnapshot{resolver: newResolver(capturedTypes(t), c.cfg), symbols: map[string]*symbolRecord{}}
	c.symbols["MAIN.test_struct"] = &SymbolEntry{Info: TagInfo{Name: "MAIN.test_struct", TypeName: "TEST_STRUCT", TypeCode: 0x41, Size: 124}, Handle: 42}
	input := map[string]any{"my_byte": uint64(15), "my_dint": int64(25), "my_sint": int64(5), "my_string": "Test structure string", "my_dint_array": []int64{1, 2, 3, 4, 5, 6, 7, 8}}
	if err := c.Write("MAIN.test_struct", input); err != nil {
		t.Fatal(err)
	}
	if err := c.Write("MAIN.test_struct.my_dint", int64(25)); err != nil {
		t.Fatal(err)
	}
	if writes.Load() != 2 || lookups.Load() != 1 {
		t.Fatalf("writes=%d lookups=%d", writes.Load(), lookups.Load())
	}
}
