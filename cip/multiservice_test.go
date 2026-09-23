package cip

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// msReply builds a Multiple Service Packet reply body (service count, offset
// table, replies) with correctly computed offsets.
func msReply(replies ...[]byte) []byte {
	hdr := 2 + 2*len(replies)
	out := binary.LittleEndian.AppendUint16(nil, uint16(len(replies)))
	off := hdr
	for _, r := range replies {
		out = binary.LittleEndian.AppendUint16(out, uint16(off))
		off += len(r)
	}
	for _, r := range replies {
		out = append(out, r...)
	}
	return out
}

var (
	msOK   = []byte{0xCC, 0x00, 0x00, 0x00, 0xC4, 0x00, 0x2A, 0x00, 0x00, 0x00}
	msErr  = []byte{0xCC, 0x00, 0x05, 0x01, 0x34, 0x12}
	msOK16 = []byte{0xCC, 0x00, 0x00, 0x00, 0xC3, 0x00, 0x07, 0x00}
)

func TestParseMultipleServiceResponseValid(t *testing.T) {
	resps, err := ParseMultipleServiceResponse(msReply(msOK, msErr, msOK16))
	if err != nil {
		t.Fatal(err)
	}
	if len(resps) != 3 {
		t.Fatalf("got %d responses", len(resps))
	}
	if resps[0].Service != 0xCC || resps[0].Status != 0 || !bytes.Equal(resps[0].Data, msOK[4:]) || resps[0].ExtStatus != nil {
		t.Fatalf("resp 0: %+v", resps[0])
	}
	if resps[1].Status != 0x05 || !bytes.Equal(resps[1].ExtStatus, []byte{0x34, 0x12}) || resps[1].Data != nil {
		t.Fatalf("resp 1: %+v", resps[1])
	}
	if !bytes.Equal(resps[2].Data, msOK16[4:]) {
		t.Fatalf("resp 2: %+v", resps[2])
	}
}

// Short-but-in-range replies keep the existing lenient behaviour (zero-value
// entry) rather than failing the whole batch.
func TestParseMultipleServiceResponseShortEntryLenient(t *testing.T) {
	data := msReply(msOK, []byte{0xCC, 0x00}, msOK16)
	resps, err := ParseMultipleServiceResponse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(resps) != 3 || resps[1].Service != 0 || !bytes.Equal(resps[2].Data, msOK16[4:]) {
		t.Fatalf("unexpected: %+v", resps)
	}
}

// Offsets from the wire must be validated; previously an offset past the end
// of the buffer (e.g. 0xFFFF) panicked with a slice-bounds error.
func TestParseMultipleServiceResponseBadOffsets(t *testing.T) {
	good := msReply(msOK, msErr, msOK16)
	setOff := func(i int, v uint16) []byte {
		d := append([]byte(nil), good...)
		binary.LittleEndian.PutUint16(d[2+2*i:], v)
		return d
	}
	cases := map[string][]byte{
		"first offset 0xFFFF":        setOff(0, 0xFFFF),
		"middle offset 0xFFFF":       setOff(1, 0xFFFF),
		"last offset 0xFFFF":         setOff(2, 0xFFFF),
		"offset past end":            setOff(1, uint16(len(good)+1)),
		"offset inside header":       setOff(0, 2),
		"offset zero":                setOff(0, 0),
		"offsets decreasing":         setOff(2, binary.LittleEndian.Uint16(good[2:4])),
		"count exceeds offset table": append([]byte{0xFF, 0xFF}, good[2:]...),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			resps, err := ParseMultipleServiceResponse(data)
			if err == nil {
				t.Fatalf("expected error, got %+v", resps)
			}
		})
	}
}

func FuzzParseMultipleServiceResponse(f *testing.F) {
	f.Add(msReply(msOK))
	f.Add(msReply(msOK, msErr, msOK16))
	f.Add(msReply(msErr, []byte{0xCC, 0x00}))
	f.Add([]byte{0x00, 0x00})
	f.Add([]byte{0x02, 0x00, 0x06, 0x00, 0xFF, 0xFF, 0xCC, 0x00, 0x00, 0x00})

	f.Fuzz(func(t *testing.T, data []byte) {
		resps, err := ParseMultipleServiceResponse(data)
		if err != nil {
			return
		}
		if len(data) >= 2 && len(resps) != int(binary.LittleEndian.Uint16(data)) {
			t.Fatalf("got %d responses for count %d", len(resps), binary.LittleEndian.Uint16(data))
		}
	})
}
