package s7

import (
	"errors"
	"testing"
)

// With no live transport the read paths must surface ErrConnectionLost rather
// than hiding the drop behind per-address errors with a nil top-level error.
func TestConnErrorIfDownSurfacesLostConnection(t *testing.T) {
	c := &Client{} // transport == nil => down
	err := c.connErrorIfDownLocked()
	if err == nil || !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("expected errors.Is(err, ErrConnectionLost), got %v", err)
	}
}
