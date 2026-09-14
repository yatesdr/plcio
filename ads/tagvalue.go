package ads

import "fmt"

// TagInfo holds metadata about a discovered symbol.
// This structure is designed to be compatible with the warlink plcman package.
type TagInfo struct {
	Name        string // Full symbol name (e.g., "MAIN.Temperature")
	TypeCode    uint16 // ADS type code
	TypeName    string // Type name from TwinCAT (e.g., "REAL", "FB_MyBlock")
	Size        uint32 // Size in bytes
	Comment     string // Symbol comment/description
	IndexGroup  uint32 // Index group for direct access
	IndexOffset uint32 // Index offset for direct access
	Flags       uint32 // Symbol flags
}

// IsReadable returns true if the symbol can be read.
// Most symbols are readable unless they have specific access restrictions.
func (t *TagInfo) IsReadable() bool {
	// Check flags for read access
	// TwinCAT symbol flags: bit 0 = persistent, bit 1 = bit value, etc.
	// For now, assume all discovered symbols are readable
	return true
}

// IsWritable returns true if the symbol can be written.
// This checks the symbol flags for write access.
func (t *TagInfo) IsWritable() bool {
	// Check flags for write access
	// In TwinCAT, most variables are writable unless marked as CONSTANT
	// Documented READONLY is bit 5 (0x20).
	return (t.Flags & SymFlagReadOnly) == 0
}

// IsPrimitive returns true if the type is a primitive (not a struct/FB).
func (t *TagInfo) IsPrimitive() bool {
	switch t.TypeCode {
	case TypeBool, TypeByte, TypeSByte, TypeWord, TypeInt16,
		TypeDWord, TypeInt32, TypeLWord, TypeInt64,
		TypeReal, TypeLReal, TypeString, TypeWString,
		TypeTime, TypeDate, TypeTimeOfDay, TypeDateTime:
		return true
	default:
		// Check if size matches a primitive
		return t.Size <= 8
	}
}

// String returns a string representation of the tag info.
func (t *TagInfo) String() string {
	if t == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s (%s, %d bytes)", t.Name, t.TypeName, t.Size)
}

// SymbolFlags contains documented ADS symbol flags.
const (
	SymFlagPersistent uint32 = 0x0001
	SymFlagBitValue   uint32 = 0x0002
	// SymFlagReserved is retained for source compatibility.
	// Deprecated: this value means REFERENCETO; use SymFlagReferenceTo.
	SymFlagReserved uint32 = 0x0004
	// SymFlagReference is retained for source compatibility.
	// Deprecated: this value means TYPEGUID; use SymFlagTypeGUID or SymFlagReferenceTo.
	SymFlagReference uint32 = 0x0008
	SymFlagReadOnly  uint32 = 0x0020
	// Deprecated: this value is READONLY, not static storage.
	SymFlagStaticVar uint32 = 0x0020
	// Deprecated: this value is not a documented input direction flag.
	SymFlagInput uint32 = 0x0040
	// Deprecated: this value is not a documented output direction flag.
	SymFlagOutput uint32 = 0x0080
	// Deprecated: this value belongs to the context mask, not InOut direction.
	SymFlagInOut            uint32 = 0x0100
	SymFlagReferenceTo      uint32 = 0x0004
	SymFlagTypeGUID         uint32 = 0x0008
	SymFlagInterfacePointer uint32 = 0x0010
	SymFlagContextMask      uint32 = 0x0f00
	SymFlagItfMethodAccess  uint32 = 0x0040
	SymFlagMethodDeref      uint32 = 0x0080
	SymFlagAttributes       uint32 = 0x1000
	SymFlagStatic           uint32 = 0x2000
	SymFlagInitOnReset      uint32 = 0x4000
	SymFlagExtendedFlags    uint32 = 0x8000
)
