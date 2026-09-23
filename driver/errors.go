package driver

import (
	"errors"

	"github.com/yatesdr/plcio/ads"
	"github.com/yatesdr/plcio/logix"
	"github.com/yatesdr/plcio/omron"
	"github.com/yatesdr/plcio/pccc"
	"github.com/yatesdr/plcio/s7"
)

// ErrConnectionLost is the driver-level connection-loss sentinel. Errors that
// the driver package itself creates for a lost or absent link (for example
// ADSAdapter.Keepalive) wrap it.
//
// Each protocol package also has its own sentinel (logix, s7, omron, pccc and
// ads ErrConnectionLost). Those errors do not match errors.Is(err,
// driver.ErrConnectionLost); use IsConnectionLost to test for any of them.
var ErrConnectionLost = errors.New("driver: connection lost")

// connectionLostSentinels lists every package-level connection-loss sentinel.
var connectionLostSentinels = []error{
	ErrConnectionLost,
	logix.ErrConnectionLost,
	s7.ErrConnectionLost,
	omron.ErrConnectionLost,
	pccc.ErrConnectionLost,
	ads.ErrConnectionLost,
}

// IsConnectionLost reports whether err wraps driver.ErrConnectionLost or any
// protocol package's ErrConnectionLost sentinel (logix, s7, omron, pccc, ads).
// It matches by identity through errors.Is, never by message text. A true
// result means the link is unusable and the caller should reconnect; for a
// write it also means the outcome is uncertain (the value may have been sent).
func IsConnectionLost(err error) bool {
	if err == nil {
		return false
	}
	for _, sentinel := range connectionLostSentinels {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}
