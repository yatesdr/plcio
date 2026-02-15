package s7

import "fmt"

// S7 Error Classes
const (
	errClassNoError     = 0x00
	errClassAppRelation = 0x81
	errClassObjDef      = 0x82
	errClassResource    = 0x83
	errClassService     = 0x84
	errClassNoResource  = 0x85 // No resource available (often PDU size exceeded)
	errClassAccess      = 0x87
)

// S7 Data Item Return Codes
const (
	dataItemSuccess         = 0xFF
	dataItemHardwareFault   = 0x01
	dataItemAccessDenied    = 0x03
	dataItemAddressError    = 0x05
	dataItemTypeError       = 0x06
	dataItemTypeInconsistent = 0x07 // Data type/size mismatch
	dataItemNotExist        = 0x0A
)

// S7Error represents an S7 protocol error.
type S7Error struct {
	Class byte
	Code  byte
}

// Error implements the error interface.
func (e S7Error) Error() string {
	return s7ErrorMessage(e.Class, e.Code)
}

// s7ErrorMessage returns a human-readable message for an S7 error.
func s7ErrorMessage(class, code byte) string {
	switch class {
	case errClassNoError:
		return "no error"
	case errClassAppRelation:
		return fmt.Sprintf("application relationship error (code %d)", code)
	case errClassObjDef:
		return fmt.Sprintf("object definition error (code %d)", code)
	case errClassResource:
		return fmt.Sprintf("resource error (code %d)", code)
	case errClassService:
		return fmt.Sprintf("service error (code %d)", code)
	case errClassNoResource:
		return fmt.Sprintf("no resource available - request may exceed PDU size (code %d)", code)
	case errClassAccess:
		return fmt.Sprintf("access error (code %d)", code)
	default:
		return fmt.Sprintf("S7 error class 0x%02X code %d", class, code)
	}
}

// dataItemError returns a human-readable message for a data item return code.
func dataItemError(code byte) string {
	switch code {
	case dataItemSuccess:
		return ""
	case dataItemHardwareFault:
		return "hardware fault"
	case dataItemAccessDenied:
		return "access denied"
	case dataItemAddressError:
		return "invalid address"
	case dataItemTypeError:
		return "data type not supported"
	case dataItemTypeInconsistent:
		return "data type/size mismatch"
	case dataItemNotExist:
		return "object does not exist"
	default:
		return fmt.Sprintf("data item error 0x%02X", code)
	}
}
