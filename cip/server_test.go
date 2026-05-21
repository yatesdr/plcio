package cip

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestForwardOpenRoundTripStandard(t *testing.T) {
	// Build a Forward_Open via the existing client-side builder, then parse
	// it via our new server-side parser to make sure they agree on the wire.
	cfg := DefaultForwardOpenConfig()
	cfg.ConnectionPath = []byte{0x20, 0x04, 0x24, 0x01, 0x2C, 0x65, 0x2C, 0x66}
	// "small" Forward_Open: 16-bit conn params
	reqBytes, _, err := BuildForwardOpenRequestSmall(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// reqBytes starts with [SvcForwardOpen, path_size, class_path_bytes..., request_data...]
	// Skip the service+path prefix: 1 byte service + 1 byte path size + path
	if reqBytes[0] != SvcForwardOpen {
		t.Fatalf("expected service 0x54, got 0x%02X", reqBytes[0])
	}
	pathBytes := int(reqBytes[1]) * 2
	body := reqBytes[2+pathBytes:]

	r, err := ParseForwardOpenRequest(body, false)
	if err != nil {
		t.Fatalf("ParseForwardOpenRequest: %v", err)
	}
	if r.Large {
		t.Error("expected Large=false")
	}
	if !bytes.Equal(r.ConnectionPath, cfg.ConnectionPath) {
		t.Errorf("connection path mismatch: %x vs %x", r.ConnectionPath, cfg.ConnectionPath)
	}
}

func TestForwardOpenRoundTripLarge(t *testing.T) {
	cfg := DefaultForwardOpenConfig()
	cfg.ConnectionPath = []byte{0x20, 0x04, 0x24, 0x01, 0x2C, 0x65, 0x2C, 0x66}
	reqBytes, _, err := BuildForwardOpenRequest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	pathBytes := int(reqBytes[1]) * 2
	body := reqBytes[2+pathBytes:]
	r, err := ParseForwardOpenRequest(body, true)
	if err != nil {
		t.Fatalf("ParseForwardOpenRequest large: %v", err)
	}
	if !r.Large {
		t.Error("expected Large=true")
	}
}

func TestForwardOpenSuccessResponse(t *testing.T) {
	s := ForwardOpenSuccess{
		OTConnectionID:   0x11111111,
		TOConnectionID:   0x22222222,
		ConnectionSerial: 0xAAAA,
		VendorID:         0xBBBB,
		OriginatorSerial: 0x33333333,
		OTAPI:            5000,
		TOAPI:            5000,
	}
	body := BuildForwardOpenSuccess(s)
	if len(body) < 26 {
		t.Fatalf("response body too short: %d", len(body))
	}
	got, err := ParseForwardOpenResponse(body)
	if err != nil {
		t.Fatalf("ParseForwardOpenResponse: %v", err)
	}
	if got.OTConnectionID != s.OTConnectionID {
		t.Errorf("OTConnectionID: %08X vs %08X", got.OTConnectionID, s.OTConnectionID)
	}
	if got.TOConnectionID != s.TOConnectionID {
		t.Errorf("TOConnectionID: %08X vs %08X", got.TOConnectionID, s.TOConnectionID)
	}
}

func TestForwardCloseRoundTrip(t *testing.T) {
	// Build a Forward_Close request manually and parse it.
	data := make([]byte, 0, 16)
	data = append(data, 0x0A, 0x01)
	data = binary.LittleEndian.AppendUint16(data, 0x1234)
	data = binary.LittleEndian.AppendUint16(data, 0x5678)
	data = binary.LittleEndian.AppendUint32(data, 0xCAFEBABE)
	data = append(data, 0x03, 0x00) // path size 3 words, reserved
	data = append(data, 0x20, 0x04, 0x24, 0x01, 0x2C, 0x65) // 6 bytes = 3 words
	r, err := ParseForwardCloseRequest(data)
	if err != nil {
		t.Fatalf("ParseForwardCloseRequest: %v", err)
	}
	if r.ConnectionSerial != 0x1234 || r.VendorID != 0x5678 || r.OriginatorSerial != 0xCAFEBABE {
		t.Errorf("close fields: %+v", r)
	}
	if !bytes.Equal(r.ConnectionPath, []byte{0x20, 0x04, 0x24, 0x01, 0x2C, 0x65}) {
		t.Errorf("close path: %x", r.ConnectionPath)
	}
}

func TestParsePathClassInstanceAttribute(t *testing.T) {
	// 0x20 0x01 0x24 0x02 0x30 0x07  → class 1, instance 2, attribute 7
	path := []byte{0x20, 0x01, 0x24, 0x02, 0x30, 0x07}
	p, err := ParsePath(path)
	if err != nil {
		t.Fatal(err)
	}
	if !p.HasClass || p.Class != 1 {
		t.Errorf("class: %v %d", p.HasClass, p.Class)
	}
	if !p.HasInstance || p.Instance != 2 {
		t.Errorf("instance: %v %d", p.HasInstance, p.Instance)
	}
	if !p.HasAttribute || p.Attribute != 7 {
		t.Errorf("attribute: %v %d", p.HasAttribute, p.Attribute)
	}
}

func TestParsePath16BitInstance(t *testing.T) {
	// 0x20 0x04 0x25 0x00 0x65 0x00 → class 4, instance 0x65 (16-bit form)
	path := []byte{0x20, 0x04, 0x25, 0x00, 0x65, 0x00}
	p, err := ParsePath(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Class != 4 || p.Instance != 0x65 {
		t.Errorf("class=%d instance=%d", p.Class, p.Instance)
	}
}
