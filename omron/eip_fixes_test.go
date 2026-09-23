package omron

import (
	"bytes"
	"errors"
	"sync"
	"testing"

	"github.com/yatesdr/plcio/cip"
)

// cipPayload returns the service data following the request path.
func cipPayload(req []byte) []byte {
	return req[2+int(req[1])*2:]
}

// --- Item 6: NJ/NX CIP STRING length prefix ---------------------------------

func TestCIPStringLengthPrefix(t *testing.T) {
	raw := []byte{0x05, 0x00, 'h', 'e', 'l', 'l', 'o'}
	tv := &TagValue{DataType: TypeCIPSTRING, Count: 1, Bytes: raw}
	if got := tv.GoValue(); got != "hello" {
		t.Fatalf("GoValue = %q, want hello", got)
	}
	if got := DecodeValue(TypeCIPSTRING, raw, false); got != "hello" {
		t.Fatalf("DecodeValue = %q", got)
	}
	// Declared length shorter than the buffer (padding) and longer (truncated).
	if got := DecodeValue(TypeCIPSTRING, []byte{2, 0, 'h', 'i', 'x', 'x'}, false); got != "hi" {
		t.Fatalf("padded = %q", got)
	}
	if got := DecodeValue(TypeCIPSTRING, []byte{9, 0, 'h', 'i'}, false); got != "hi" {
		t.Fatalf("truncated = %q", got)
	}
	got, err := EncodeValue("hello", TypeCIPSTRING, false)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("EncodeValue = %x %v, want %x", got, err, raw)
	}
	// FINS STRING (no prefix, NUL terminated) unchanged.
	if got, _ := EncodeValue("hi", TypeString, true); !bytes.Equal(got, []byte{'h', 'i', 0}) {
		t.Fatalf("FINS string %x", got)
	}
}

func TestCIPStringReadWriteOverEIP(t *testing.T) {
	var mu sync.Mutex
	var written []byte
	c := connectEIPTest(t, func(req []byte) []byte {
		switch req[0] {
		case svcReadTag:
			return []byte{0xCC, 0, 0, 0, 0xD0, 0x00, 0x05, 0x00, 'h', 'e', 'l', 'l', 'o'}
		case svcWriteTag:
			mu.Lock()
			written = append([]byte(nil), cipPayload(req)...)
			mu.Unlock()
			return []byte{0xCD, 0, 0, 0}
		}
		return []byte{req[0] | 0x80, 0, 0x08, 0}
	})
	vals, err := c.Read("Msg")
	if err != nil || vals[0].Error != nil {
		t.Fatal(err, vals[0].Error)
	}
	if s := vals[0].String(); s != "hello" {
		t.Fatalf("read %q, want hello", s)
	}
	if err := c.Write("Msg", "world!"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []byte{0xD0, 0x00, 0x01, 0x00, 0x06, 0x00, 'w', 'o', 'r', 'l', 'd', '!'}
	if !bytes.Equal(written, want) {
		t.Fatalf("write payload %x, want %x", written, want)
	}
}

// --- Item 11: Omron BYTE/WORD/DWORD/LWORD (0xD1-0xD4) -----------------------

func TestOmronBitStringTypes(t *testing.T) {
	cases := []struct {
		code uint16
		data []byte
		want any
		u    uint64
	}{
		{TypeOmronByte, []byte{0xAB}, uint8(0xAB), 0xAB},
		{TypeOmronWord, []byte{0x34, 0x12}, uint16(0x1234), 0x1234},
		{TypeOmronDWord, []byte{0x78, 0x56, 0x34, 0x12}, uint32(0x12345678), 0x12345678},
		{TypeOmronLWord, []byte{0xEF, 0xCD, 0xAB, 0x89, 0x67, 0x45, 0x23, 0x01}, uint64(0x0123456789ABCDEF), 0x0123456789ABCDEF},
	}
	for _, tc := range cases {
		if got := DecodeValue(tc.code, tc.data, false); got != tc.want {
			t.Errorf("%s decode = %#v, want %#v", TypeName(tc.code), got, tc.want)
		}
		for _, in := range []any{tc.want, int64(tc.u), tc.u} {
			got, err := EncodeValue(in, tc.code, false)
			if err != nil || !bytes.Equal(got, tc.data) {
				t.Errorf("%s encode %T = %x %v, want %x", TypeName(tc.code), in, got, err, tc.data)
			}
		}
	}
	arr := &TagValue{DataType: MakeArrayType(TypeOmronWord), Count: 2, Bytes: []byte{1, 0, 2, 0}}
	if got, ok := arr.GoValue().([]uint16); !ok || got[0] != 1 || got[1] != 2 {
		t.Fatalf("WORD[2] = %#v", arr.GoValue())
	}
	if got, err := EncodeValue([]uint64{1, 2}, TypeOmronWord, false); err != nil || !bytes.Equal(got, []byte{1, 0, 2, 0}) {
		t.Fatalf("[]uint64 encode %x %v", got, err)
	}
}

func TestOmronDWordWriteOverEIP(t *testing.T) {
	var mu sync.Mutex
	var written []byte
	c := connectEIPTest(t, func(req []byte) []byte {
		switch req[0] {
		case svcReadTag:
			return []byte{0xCC, 0, 0, 0, 0xD3, 0x00, 0, 0, 0, 0}
		case svcWriteTag:
			mu.Lock()
			written = append([]byte(nil), cipPayload(req)...)
			mu.Unlock()
			return []byte{0xCD, 0, 0, 0}
		}
		return []byte{req[0] | 0x80, 0, 0x08, 0}
	})
	if err := c.Write("Flags", uint64(0x12345678)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []byte{0xD3, 0, 1, 0, 0x78, 0x56, 0x34, 0x12}; !bytes.Equal(written, want) {
		t.Fatalf("payload %x, want %x", written, want)
	}
}

// --- Item 12: discovery reports a dropped connection ------------------------

func TestAllTagsReportsConnectionDrop(t *testing.T) {
	// Drop immediately: previously AllTags returned (nil, nil).
	c := connectEIPTest(t, func(req []byte) []byte { return nil })
	tags, err := c.AllTags()
	if err == nil || !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("AllTags = %v, %v; want ErrConnectionLost", tags, err)
	}

	// Drop after one page: the partial list is returned with the error.
	var pages int
	c2 := connectEIPTest(t, func(req []byte) []byte {
		if req[0] != svcGetInstanceAttrList {
			return []byte{req[0] | 0x80, 0, 0x08, 0}
		}
		pages++
		if pages > 1 {
			return nil
		}
		entry := []byte{1, 0, 0, 0, 4, 0, 'T', 'a', 'g', '1', 0xC4, 0, 0, 0}
		entry = append(entry, make([]byte, 10)...)        // AB layout: nameLen+20 bytes per entry
		return append([]byte{0xD5, 0, 0x06, 0}, entry...) // partial transfer: more pages
	})
	tags, err = c2.AllTags()
	if err == nil || !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("AllTags after mid-scan drop = %v, %v; want error", tags, err)
	}
	if len(tags) != 1 || tags[0].Name != "Tag1" {
		t.Fatalf("partial tags %+v", tags)
	}
}

// --- Item 13: CIP status handling --------------------------------------------

// A partial transfer (0x06) is never a complete value: a read reports an
// error instead of decoding a fragment, and a write is not reported applied.
func TestCIPPartialTransferIsAnError(t *testing.T) {
	c := connectEIPTest(t, func(req []byte) []byte {
		switch req[0] {
		case svcReadTag:
			return []byte{0xCC, 0, 0x06, 0, 0xC4, 0, 1, 0, 0, 0}
		case svcWriteTag:
			return []byte{0xCD, 0, 0x06, 0}
		}
		return []byte{req[0] | 0x80, 0, 0x08, 0}
	})
	vals, err := c.Read("X")
	if err != nil || vals[0].Error == nil || vals[0].GoValue() != nil {
		t.Fatalf("read with partial transfer: %v %v %v", err, vals[0].Error, vals[0].GoValue())
	}
	if err := c.Write("X", int64(5)); err == nil {
		t.Fatal("write answered with partial transfer (0x06) reported success")
	}
}

func TestKeepaliveChecksCIPStatus(t *testing.T) {
	var mu sync.Mutex
	status := byte(0x08)
	c := connectEIPTest(t, func(req []byte) []byte {
		mu.Lock()
		defer mu.Unlock()
		return []byte{req[0] | 0x80, 0, status, 0}
	})
	c.mu.Lock()
	c.cipConn = &cip.Connection{OTConnID: 1, TOConnID: 2}
	c.mu.Unlock()
	if err := c.Keepalive(); err == nil {
		t.Fatal("Keepalive ignored CIP error status")
	}
	mu.Lock()
	status = 0
	mu.Unlock()
	if err := c.Keepalive(); err != nil {
		t.Fatal(err)
	}
}
