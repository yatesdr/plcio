package ads

import (
	"github.com/yatesdr/plcio/metadata"
)

// TagValue holds the result of a tag read operation.
// This structure is designed to be compatible with the warlink plcman package.
type TagValue struct {
	Name     string // Symbol name
	DataType uint16 // ADS type code
	Bytes    []byte // Raw value bytes (little-endian, native x86/TwinCAT format)
	Count    int    // Number of elements (1 for scalar, >1 for array)
	Error    error  // Per-tag error (nil if successful)
}

// GoValue decodes native ADS primitive bytes without a connection. Numeric
// values are widened to int64, uint64 or float64; arrays remain typed slices,
// including a singleton marked with TypeArrayFlag. STRING defaults to Latin-1
// and WSTRING to UCS-2. TIME/TOD use unsigned milliseconds; DATE/DT use unsigned
// Unix seconds and LTIME unsigned nanoseconds. Unknown layouts retain []int
// opaque storage. Malformed known storage returns nil. Published record layouts
// and target encoding attributes require Client.ReadDecoded.
func (v *TagValue) GoValue() interface{} {
	if v == nil || v.Error != nil || len(v.Bytes) == 0 {
		return nil
	}
	base := BaseType(v.DataType)
	schema := primitiveSchemas[base]
	if schema == nil {
		return v.bytesToIntArray()
	}
	array := IsArray(v.DataType) || v.Count > 1
	width := TypeSize(base)
	if width != 0 && len(v.Bytes) > width {
		array = true
	}
	if !array {
		if width != 0 && len(v.Bytes) != width {
			return nil
		}
		value, err := decodeValueUnchecked(schema, v.Bytes, defaultOptions().deadline)
		if err != nil {
			return nil
		}
		return value
	}
	element := *schema
	count := v.Count
	if width != 0 {
		if len(v.Bytes)%width != 0 {
			return nil
		}
		actual := len(v.Bytes) / width
		if count > 1 && count != actual {
			return nil
		}
		count = actual
	} else {
		if count < 1 {
			count = 1
		}
		if len(v.Bytes)%count != 0 {
			return nil
		}
		element.size = uint64(len(v.Bytes) / count)
	}
	if count < 1 || uint64(count) >= uint64(defaultOptions().maxElements) || uint64(len(v.Bytes)) > uint64(defaultOptions().maxPayload) {
		return nil
	}
	result := element
	result.size = uint64(len(v.Bytes))
	result.element = &element
	result.dimensions = []metadata.Dimension{{Length: uint32(count)}}
	value, err := decodeValue(&result, v.Bytes, defaultOptions())
	if err != nil {
		return nil
	}
	return value
}

// bytesToIntArray converts the raw bytes to []int for JSON-friendly output.
func (v *TagValue) bytesToIntArray() []int {
	intBytes := make([]int, len(v.Bytes))
	for i, b := range v.Bytes {
		intBytes[i] = int(b)
	}
	return intBytes
}

// TypeName returns the human-readable type name for this tag.
func (v *TagValue) TypeName() string {
	if v == nil {
		return ""
	}
	return TypeName(v.DataType)
}
