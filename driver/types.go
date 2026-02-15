package driver


// TagValue is a unified wrapper that holds tag data from any PLC family.
// It stores pre-computed Go values and type information for display.
type TagValue struct {
	Name        string      // Tag name
	DataType    uint16      // Native type code (family-specific)
	Family      string      // PLC family ("logix", "s7", "ads", "omron")
	Value       interface{} // Pre-computed Go value
	StableValue interface{} // Value with ignored members removed (for change detection)
	Bytes       []byte      // Original raw bytes (native byte order)
	Count       int         // Number of elements (1 for scalar, >1 for array)
	Error       error       // Per-tag error (nil if successful)
}

// SetIgnoreList computes and sets the StableValue based on the ignore list.
// This should be called after the TagValue is created to filter out volatile members.
func (v *TagValue) SetIgnoreList(ignoreList []string) {
	if v == nil {
		return
	}
	v.StableValue = ComputeStableValue(v.Value, ignoreList)
}

// TagRequest represents a read request with optional type hint.
// TypeHint is used for protocols that require type information (e.g., S7, Omron FINS).
type TagRequest struct {
	Name     string // Tag name or address
	TypeHint string // Optional type hint (e.g., "INT", "REAL", "DINT")
}

// TagInfo represents discovered tag metadata from PLCs that support tag browsing.
type TagInfo struct {
	Name       string   // Tag name
	TypeCode   uint16   // Native type code
	Instance   uint32   // CIP instance ID (Logix/Omron EIP)
	Dimensions []uint32 // Array dimensions (empty for scalars)
	TypeName   string   // Human-readable type name
	Writable   bool     // Whether the tag can be written
}

// DeviceInfo contains information about the connected PLC.
type DeviceInfo struct {
	Family       PLCFamily // PLC family
	Vendor       string           // Vendor name
	Model        string           // Device model
	Version      string           // Firmware version
	SerialNumber string           // Serial number
	Description  string           // Additional description
}

// ComputeStableValue returns a copy of the value with ignored members removed.
// For map values (decoded UDTs), this filters out keys in the ignore list.
// For other value types, returns the value unchanged.
func ComputeStableValue(value interface{}, ignoreList []string) interface{} {
	if len(ignoreList) == 0 {
		return value
	}

	// Check if value is a map (decoded UDT)
	mapVal, ok := value.(map[string]interface{})
	if !ok {
		return value
	}

	// Create ignore set for O(1) lookup
	ignoreSet := make(map[string]bool, len(ignoreList))
	for _, name := range ignoreList {
		ignoreSet[name] = true
	}

	// Create filtered copy
	filtered := make(map[string]interface{}, len(mapVal))
	for key, val := range mapVal {
		if !ignoreSet[key] {
			filtered[key] = val
		}
	}

	return filtered
}
