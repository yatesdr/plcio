package logix

import (
	"errors"
	"testing"
)

// When the underlying transport is down, the batch read paths must surface
// ErrConnectionLost at the top level instead of silently returning a nil error
// (which previously hid link drops behind per-tag errors).
func TestConnErrorIfDownSurfacesLostConnection(t *testing.T) {
	// A PLC with no Connection reports IsConnected() == false.
	c := &Client{plc: &PLC{}}

	err := c.connErrorIfDown()
	if err == nil {
		t.Fatal("expected an error when the connection is down, got nil")
	}
	if !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("expected errors.Is(err, ErrConnectionLost), got %v", err)
	}
}

// connErrorIfDown must not panic and must return nil when there is no plc to
// query, so callers in degenerate states don't get a spurious connection error.
func TestConnErrorIfDownNilPLC(t *testing.T) {
	c := &Client{}
	if err := c.connErrorIfDown(); err != nil {
		t.Fatalf("expected nil error when plc is nil, got %v", err)
	}
}
