package logix

// CIP common services
const (
	// Get Attribute Single - read single attribute from object instance
	SvcGetAttributeSingle byte = 0x0E

	// NOP (No Operation) - used for keepalive without state change
	SvcNop byte = 0x17
)

// Logix-specific CIP services (Allen-Bradley extensions to CIP).
// These are not part of the standard CIP specification.
const (
	// Read Tag Service - reads tag data by symbolic name
	SvcReadTag byte = 0x4C

	// Write Tag Service - writes tag data by symbolic name
	SvcWriteTag byte = 0x4D

	// Read Tag Fragmented - for large data transfers
	SvcReadTagFragmented byte = 0x52

	// Write Tag Fragmented - for large data transfers
	SvcWriteTagFragmented byte = 0x53

	// Read Modify Write Tag - atomic read-modify-write
	SvcReadModifyWriteTag byte = 0x4E

	// Multiple Service Packet - batch multiple requests
	SvcMultipleServicePacket byte = 0x0A

	// Get Instance Attribute List - used for tag browsing
	SvcGetInstanceAttributeList byte = 0x55
)

// CIP General Status codes
const (
	StatusSuccess           byte = 0x00
	StatusPathSegmentError  byte = 0x04
	StatusPathUnknown       byte = 0x05
	StatusPartialTransfer   byte = 0x06 // More data available (pagination)
	StatusServiceNotSupport byte = 0x08
	StatusInvalidAttrValue  byte = 0x09
	StatusAlreadyInState    byte = 0x0A
	StatusObjectStateConfl  byte = 0x0C
	StatusAttrNotSettable   byte = 0x0E
	StatusPrivilegeViolat   byte = 0x0F
	StatusDeviceStateConfl  byte = 0x10
	StatusReplyDataTooLarge byte = 0x11
	StatusNotEnoughData     byte = 0x13
	StatusAttrNotSupported  byte = 0x14
	StatusTooMuchData       byte = 0x15
	StatusObjectNotExist    byte = 0x16
	StatusFragNotSupported  byte = 0x17
	StatusNotSaved          byte = 0x18
	StatusAttrNotSavable    byte = 0x19
	StatusInvalidRequest    byte = 0x1A
	StatusMemberNotSettable byte = 0x1F
	StatusGeneralError      byte = 0xFF
)

// Logix extended status codes (when general status is 0xFF)
const (
	ExtStatusSuccess      uint16 = 0x0000
	ExtStatusExtendedErr  uint16 = 0x00FF
	ExtStatusIllegalType  uint16 = 0x2101 // Wrong data type for tag
	ExtStatusTagNotFound  uint16 = 0x2104 // Tag does not exist
	ExtStatusTagReadOnly  uint16 = 0x2105 // Cannot write to tag
	ExtStatusSizeTooSmall uint16 = 0x2107 // Data too small
	ExtStatusSizeTooLarge uint16 = 0x2108 // Data too large
	ExtStatusOffsetError  uint16 = 0x2109 // Offset out of range
)
