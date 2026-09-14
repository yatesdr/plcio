package ads

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yatesdr/plcio/metadata"
)

func TestSumUpUnsupportedFallbackKeepsPartialResults(t *testing.T) {
	for _, code := range []byte{1, 2} {
		t.Run(string(rune('0'+code)), func(t *testing.T) {
			var sums, reads atomic.Int32
			c := testClient(t, func(req []byte) []byte {
				switch binary.LittleEndian.Uint32(req[38:]) {
				case 0xf080:
					sums.Add(1)
					return []byte{code, 7, 0, 0, 0, 0, 0, 0}
				case 0xf005:
					if reads.Add(1) > 1 {
						return nil
					}
					return testReadReply([]byte{25, 0, 0, 0})
				case 0xf006:
					return []byte{0, 0, 0, 0}
				}
				t.Errorf("unexpected request %x", req)
				return nil
			})
			for i, name := range []string{"a", "b", "c"} {
				seedDINT(c, name, uint32(i+1))
			}
			values, err := c.ReadDecoded("a", "b", "c")
			if !errors.Is(err, ErrConnectionLost) || !errors.Is(err, io.EOF) || len(values) != 3 || values[0].Value != int64(25) || values[1].Raw.Error == nil || values[2].Raw.Error == nil || sums.Load() != 1 || reads.Load() != 2 || c.IsConnected() {
				t.Fatalf("%v %v sums=%d reads=%d", values, err, sums.Load(), reads.Load())
			}
		})
	}
}

func TestExplicitStringEncodingOverridesAttributes(t *testing.T) {
	for _, tc := range []struct {
		encoding, attribute string
		expected            []byte
	}{
		{"UTF-8", "Latin-1", []byte{'c', 'a', 'f', 0xc3, 0xa9, 0}},
		{"Latin-1", "UTF-8", []byte{'c', 'a', 'f', 0xe9, 0, 0}},
	} {
		cfg := defaultOptions()
		WithStringEncoding(tc.encoding)(&cfg)
		r := newResolver(map[string]*typeEntry{"Alias": {name: "Alias", typeName: "STRING(5)", size: 6, flags: 1, version: 1, attributes: map[string]string{"TcEncoding": tc.attribute}}}, cfg)
		for _, name := range []string{"Alias", "ARRAY [-1..-1] OF Alias"} {
			schema := r.resolveName(name, 1)
			var input any = "café"
			if len(schema.dimensions) > 0 {
				input = []string{"café"}
			}
			data, err := encodeValue(schema, input, cfg)
			if err != nil || !bytes.Equal(data, tc.expected) {
				t.Fatalf("%s %s %x %v", tc.encoding, name, data, err)
			}
		}
	}
	if _, err := Connect("192.168.5.212", WithStringEncoding("UTF-16")); err == nil {
		t.Fatal("invalid encoding accepted")
	}
}

func TestRecordArraysAndKnownReadOnlyStorage(t *testing.T) {
	cfg := defaultOptions()
	r := newResolver(capturedTypes(t), cfg)
	schema := r.resolveName("ARRAY [-2..-1] OF PACKED_STRUCT", 1)
	input := []any{map[string]any{"my_bit1": false, "my_bit2": true, "my_bit3": false, "my_bit4": true}, map[string]any{"my_bit1": true, "my_bit2": false, "my_bit3": true, "my_bit4": false}}
	data, err := encodeValue(schema, input, cfg)
	if err != nil || !bytes.Equal(data, []byte{10, 5}) {
		t.Fatalf("%x %v", data, err)
	}
	value, err := decodeValue(schema, []byte{10, 5}, cfg)
	if err != nil || len(value.([]any)) != 2 {
		t.Fatal(value, err)
	}
	copy := *schema.element
	copy.members = append([]schemaMember(nil), copy.members...)
	copy.members[1].readOnly = true
	for _, value := range []any{input[0], []byte{10}} {
		if _, err := encodeValue(&copy, value, cfg); err == nil {
			t.Fatal("read-only member accepted")
		}
	}
}

func TestConcurrentReadWriteDescribeCatalogAndClose(t *testing.T) {
	var handles, reads, writes atomic.Int32
	c := testClient(t, func(req []byte) []byte {
		cmd := binary.LittleEndian.Uint16(req[22:])
		group := binary.LittleEndian.Uint32(req[38:])
		switch {
		case group == 0xf003:
			handles.Add(1)
			return testReadReply([]byte{42, 0, 0, 0})
		case group == 0xf005 && cmd == 2:
			reads.Add(1)
			return testReadReply([]byte{25, 0, 0, 0})
		case group == 0xf005 && cmd == 3:
			writes.Add(1)
			if !bytes.Equal(req[50:], []byte{25, 0, 0, 0}) {
				t.Errorf("write %x", req)
			}
			return []byte{0, 0, 0, 0}
		case group == 0xf006:
			return []byte{0, 0, 0, 0}
		}
		t.Errorf("unexpected %x", req)
		return nil
	})
	seedDINT(c, "MAIN.n", 0)
	c.catalogUnavailable = false
	c.symbolsLoaded = true
	c.catalog = []TagInfo{c.symbols["MAIN.n"].Info}
	c.snapshot = &schemaSnapshot{catalog: c.catalog, resolver: newResolver(nil, c.cfg), symbols: map[string]*symbolRecord{}}
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			switch i % 4 {
			case 0:
				var v []*DecodedTagValue
				v, err = c.ReadDecoded("MAIN.n")
				if err == nil && (len(v) != 1 || v[0].Value != int64(25)) {
					t.Errorf("read %v", v)
				}
			case 1:
				err = c.Write("MAIN.n", int64(25))
			case 2:
				var d *metadata.Symbol
				d, err = c.Describe("MAIN.n")
				if err == nil && (d.Type.Bits != 32 || d.Type.Kind != metadata.KindInt) {
					t.Errorf("describe %v", d)
				}
			case 3:
				var tags []TagInfo
				tags, err = c.AllTags()
				if err == nil && len(tags) != 1 {
					t.Errorf("catalog %v", tags)
				}
			}
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if handles.Load() != 1 || reads.Load() != 16 || writes.Load() != 16 {
		t.Fatalf("handle=%d read=%d write=%d", handles.Load(), reads.Load(), writes.Load())
	}
	for range 16 {
		wg.Add(1)
		go func() { defer wg.Done(); c.Close() }()
	}
	wg.Wait()
	if c.IsConnected() {
		t.Fatal("Close left connected")
	}
}
