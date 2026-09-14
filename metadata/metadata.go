// Package metadata contains caller-owned descriptions of PLC symbols. It has no
// networking, registries, or vendor-specific storage layout rules.
package metadata

// Dimension is one effective array axis. Values are flat, last axis fastest.
type Dimension struct {
	LowerBound int64
	Length     uint32
}

// Kind identifies the ordinary Go value category of an array element or scalar.
type Kind string

const (
	KindBool         Kind = "bool"
	KindInt          Kind = "int"
	KindUint         Kind = "uint"
	KindFloat        Kind = "float"
	KindString       Kind = "string"
	KindStruct       Kind = "struct"
	KindOpaque       Kind = "opaque"
	UnitMilliseconds      = "milliseconds"
	UnitSeconds           = "seconds"
	UnitNanoseconds       = "nanoseconds"
	EpochUnix             = "1970-01-01"
)

type Type struct {
	Kind              Kind
	Bits              uint16
	DeclaredName      string
	Dimensions        []Dimension
	Members           []Member
	Unit              string
	Epoch             string
	UnsupportedReason string
}

type Member struct {
	Name     string
	Type     Type
	ReadOnly bool
}

type Symbol struct {
	Name     string
	Type     Type
	Readable bool
	Writable bool
}
