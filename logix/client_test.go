package logix

import (
	"errors"
	"testing"

	"github.com/yatesdr/plcio/cip"
	"github.com/yatesdr/plcio/eip"
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

// A Forward Open object can remain populated after the EIP client tears down
// its socket. The transport state is authoritative in that situation.
func TestIsConnectedRejectsStaleCIPConnection(t *testing.T) {
	plc := &PLC{
		Connection: eip.NewEipClient("127.0.0.1"),
		cipConn:    &cip.Connection{},
	}

	if plc.IsConnected() {
		t.Fatal("expected disconnected when CIP state exists but the EIP transport is down")
	}

	err := (&Client{plc: plc}).connErrorIfDown()
	if !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("expected ErrConnectionLost for stale CIP state, got %v", err)
	}
}
