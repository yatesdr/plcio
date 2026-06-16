package ads

import (
	"errors"
	"testing"
)

// A disconnected client must surface ErrConnectionLost rather than hiding the
// drop behind per-symbol errors with a nil top-level error.
func TestConnErrorIfDownSurfacesLostConnection(t *testing.T) {
	c := &Client{} // connected == false
	err := c.connErrorIfDown()
	if err == nil || !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("expected errors.Is(err, ErrConnectionLost), got %v", err)
	}
}
