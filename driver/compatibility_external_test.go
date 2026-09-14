package driver_test

import (
	"testing"
	"time"

	"github.com/yatesdr/plcio/ads"
	"github.com/yatesdr/plcio/driver"
)

// Compile published function types and unkeyed caller literals. Adding even a
// private field to these exported result/configuration types would break callers.
// Local defined types retain the exact imported layouts for positional checks
// without vet's warning about unkeyed literals from other packages.
type positionalADSValue ads.TagValue
type positionalADSInfo ads.TagInfo
type positionalADSSymbol ads.SymbolEntry

var (
	_ func(*driver.PLCConfig) (*driver.ADSAdapter, error) = driver.NewADSAdapter
	_ func(*driver.PLCConfig) (driver.Driver, error)      = driver.Create
	_ func(string, ...ads.Option) (*ads.Client, error)    = ads.Connect
	_ func(*ads.Client)                                   = (*ads.Client).Close
	_ func(*ads.Client, string, interface{}) error        = (*ads.Client).Write
	_ driver.Driver                                       = (*driver.ADSAdapter)(nil)
	_                                                     = driver.PLCConfig{"", "", 0, "", false, nil, nil, time.Duration(0), time.Duration(0), nil, "", "", 0, "", 0, 0, 0, 0}
	_                                                     = driver.TagSelection{"", "", "", false, false, nil, false, false, false, false}
	_                                                     = driver.TagValue{"", 0, "", nil, nil, nil, 0, nil}
	_                                                     = driver.TagInfo{"", 0, 0, nil, "", false}
	_                                                     = ads.TagValue(positionalADSValue{"", 0, nil, 0, nil})
	_                                                     = ads.TagInfo(positionalADSInfo{"", 0, "", 0, "", 0, 0, 0})
	_                                                     = ads.SymbolEntry(positionalADSSymbol{ads.TagInfo{}, 0})
)

func TestRawADSCaller(t *testing.T) {
	v := ads.TagValue(positionalADSValue{"MAIN.n", ads.TypeWord, []byte{42, 0}, 1, nil})
	got, ok := v.GoValue().(uint64)
	if !ok || got != 42 {
		t.Fatalf("raw UINT: %T %v", v.GoValue(), v.GoValue())
	}
}
