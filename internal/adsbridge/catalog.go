// Package adsbridge carries the ADS catalog projection between ads and driver
// without adding a public client method or changing the legacy TagInfo layout.
package adsbridge

import "time"

type Tag struct {
	Name       string
	TypeCode   uint16
	TypeName   string
	Writable   bool
	Dimensions []uint32
}

// Catalog is bound once by ads.init before any driver can call it. It retains
// no client, schema, registry, or cache. ads pins and projects the schema; driver
// checks the returned operation deadline while copying its caller-owned result.
var Catalog func(any) ([]Tag, time.Time, error)
