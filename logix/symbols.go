package logix

// Symbol type flags for decoding TagInfo.TypeCode
const (
	// SymbolTypeAtomic indicates an atomic (scalar) type when this bit is NOT set
	// and the type code is in the 0x00C0-0x00CF range
	SymbolTypeAtomic uint16 = 0x00C0

	// SymbolTypeStruct indicates a structure/UDT when bit 15 is set
	SymbolTypeStruct uint16 = 0x8000

	// SymbolTypeArray indicates an array (1D, 2D, or 3D) - check bits 13-14
	SymbolTypeArray1D uint16 = 0x2000
	SymbolTypeArray2D uint16 = 0x4000
	SymbolTypeArray3D uint16 = 0x6000
	SymbolTypeArrayMask uint16 = 0x6000

	// SymbolTypeSystem indicates a system type
	SymbolTypeSystem uint16 = 0x1000
)

// IsArrayType returns true if the type code indicates an array.
func IsArrayType(typeCode uint16) bool {
	return (typeCode & SymbolTypeArrayMask) != 0
}

// ArrayDimensions returns the number of array dimensions (0, 1, 2, or 3).
func ArrayDimensions(typeCode uint16) int {
	switch typeCode & SymbolTypeArrayMask {
	case SymbolTypeArray1D:
		return 1
	case SymbolTypeArray2D:
		return 2
	case SymbolTypeArray3D:
		return 3
	default:
		return 0
	}
}

// BaseType extracts the base type code, stripping array/struct flags.
func BaseType(typeCode uint16) uint16 {
	return typeCode & 0x0FFF
}
