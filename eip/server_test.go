package eip

import (
	"bytes"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	in := &Frame{
		Command:       RegisterSession,
		SessionHandle: 0xDEADBEEF,
		Status:        0,
		Context:       [8]byte{1, 2, 3, 4, 5, 6, 7, 8},
		Options:       0,
		Data:          []byte{0x01, 0x00, 0x00, 0x00},
	}
	wire := in.Bytes()
	if len(wire) != int(EncapHeaderLen)+len(in.Data) {
		t.Fatalf("length mismatch: %d vs %d", len(wire), EncapHeaderLen+len(in.Data))
	}

	got, err := ParseFrame(wire)
	if err != nil {
		t.Fatalf("ParseFrame: %v", err)
	}
	if got.Command != in.Command {
		t.Errorf("Command: got %d want %d", got.Command, in.Command)
	}
	if got.SessionHandle != in.SessionHandle {
		t.Errorf("SessionHandle: got 0x%08X want 0x%08X", got.SessionHandle, in.SessionHandle)
	}
	if got.Context != in.Context {
		t.Errorf("Context mismatch")
	}
	if !bytes.Equal(got.Data, in.Data) {
		t.Errorf("Data mismatch: %x vs %x", got.Data, in.Data)
	}
}

func TestReadFrame(t *testing.T) {
	in := &Frame{
		Command:       SendRRData,
		SessionHandle: 0x1234,
		Data:          []byte("payload"),
	}
	r := bytes.NewReader(in.Bytes())
	got, err := ReadFrame(r)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if string(got.Data) != "payload" {
		t.Errorf("data: %q", got.Data)
	}
}

func TestParseFrameTruncated(t *testing.T) {
	short := make([]byte, EncapHeaderLen-1)
	if _, err := ParseFrame(short); err == nil {
		t.Error("expected error on truncated frame")
	}
}

func TestReply(t *testing.T) {
	in := &Frame{
		Command:       SendRRData,
		SessionHandle: 0xABCD1234,
		Context:       [8]byte{0xDE, 0xAD},
	}
	r := in.Reply(EncapStatusSuccess, []byte{0x01, 0x02})
	if r.Command != in.Command || r.SessionHandle != in.SessionHandle || r.Context != in.Context {
		t.Error("Reply did not echo Command/Session/Context")
	}
	if r.Status != EncapStatusSuccess || len(r.Data) != 2 {
		t.Error("Reply status/data wrong")
	}
}

func TestRRDataRoundTrip(t *testing.T) {
	payload := []byte("hello world")
	wrapped := BuildRRData(payload)
	got, err := ParseRRData(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("payload mismatch: %q vs %q", got, payload)
	}
}
