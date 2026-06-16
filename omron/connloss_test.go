package omron

import (
	"errors"
	"testing"
)

// With no live transport the FINS read paths must surface ErrConnectionLost
// rather than hiding the drop behind per-address errors with a nil top-level
// error. (The EIP path escalates connection errors on its own.)
func TestConnErrorIfDownSurfacesLostConnection(t *testing.T) {
	c := &Client{} // fins == nil && eipClient == nil => down
	err := c.connErrorIfDownLocked()
	if err == nil || !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("expected errors.Is(err, ErrConnectionLost), got %v", err)
	}
}
