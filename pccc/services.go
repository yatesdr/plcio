package pccc

import "fmt"

// CIP service code for PCCC encapsulation.
const (
	// CipSvcExecutePCCC is the CIP service that encapsulates PCCC commands.
	// Sent to the PCCC Object (class 0x67, instance 1).
	CipSvcExecutePCCC byte = 0x4B

	// CipSvcExecutePCCCReply is the reply service code (0x4B | 0x80).
	CipSvcExecutePCCCReply byte = 0xCB

	// CipClassPCCC is the CIP class for the PCCC Object.
	CipClassPCCC byte = 0x67
)

// PCCC command codes.
const (
	// CmdTypedCommand is the command code for typed read/write operations.
	CmdTypedCommand byte = 0x0F

	// CmdTypedReply is the reply bit ORed with the command code.
	CmdTypedReply byte = 0x4F // 0x0F | 0x40

	// CmdDiagnosticStatus is the Diagnostic Status command (CMD 06, FNC 03 per
	// the DF1 manual 1770-6.5.16; pycomm3's get_processor_type sends the same).
	// Returns processor catalog string and status information.
	CmdDiagnosticStatus byte = 0x06

	// FncDiagnosticStatus is the function code that accompanies CMD 0x06.
	FncDiagnosticStatus byte = 0x03

	// CmdDiagnosticReply is the reply to Diagnostic Status.
	CmdDiagnosticReply byte = 0x46 // 0x06 | 0x40
)

// PCCC function codes for typed commands (CMD=0x0F).
const (
	// FncProtectedTypedLogicalRead reads data using 3-address-field format.
	// SLC 500 and MicroLogix (1770-6.5.16 p. 7-17); not a PLC-5 command.
	FncProtectedTypedLogicalRead byte = 0xA2

	// FncProtectedTypedLogicalWrite writes data using 3-address-field format.
	// SLC 500 and MicroLogix (1770-6.5.16 p. 7-18); not a PLC-5 command.
	FncProtectedTypedLogicalWrite byte = 0xAA

	// FncProtectedTypedLogicalMaskedWrite is the Protected Typed Logical Write
	// with Mask, 3 address fields (SLC 500 / MicroLogix). Only bits set in the
	// mask change, so the processor applies a bit write atomically.
	FncProtectedTypedLogicalMaskedWrite byte = 0xAB

	// FncTypedRead is the PLC-5 Typed Read (read block) function
	// (1770-6.5.16 p. 7-28). See plc5.go.
	FncTypedRead byte = 0x68

	// FncTypedWrite is the PLC-5 Typed Write (write block) function
	// (1770-6.5.16 p. 7-30). See plc5.go.
	FncTypedWrite byte = 0x67

	// FncReadSection is the Protected Typed Logical Read with 2 address fields
	// (file number, file type, element; no sub-element). It is used to read
	// system file 0 for file directory discovery.
	FncReadSection byte = 0xA1
)

// PCCC status codes (STS byte in response).
// The low nibble contains the error code, the high nibble contains flags.
const (
	StsSuccess         byte = 0x00
	StsIllegalCommand  byte = 0x10
	StsHostProblem     byte = 0x20
	StsRemoteProblem   byte = 0x30
	StsHardwareFault   byte = 0x40
	StsAddressProblem  byte = 0x50
	StsFunctionNA      byte = 0x60
	StsTargetProblem   byte = 0x70
	StsTypesMismatch   byte = 0x80
	StsDataFieldError  byte = 0x90
	StsAccessDenied    byte = 0xA0
	StsNoFunctionErr   byte = 0xB0
	StsDataConvErr     byte = 0xC0
	StsScnrSuspError   byte = 0xD0
	StsNotCompatible   byte = 0xE0
	StsExtStatusFlag   byte = 0xF0 // Extended status follows TNS
)

// PCCC extended status codes (EXT_STS byte, when STS has 0xF0 flag).
//
// Deprecated: these names do not match the DF1 manual's EXT STS table for
// CMD 0x0F (e.g. 0x0C is "condition cannot be generated", not "file number
// does not exist"). The values are kept for compatibility; error text comes
// from the DF1 table in pcccExtStatusName.
const (
	ExtStsNotAllowed            byte = 0x01
	ExtStsPrivilegeViolation    byte = 0x02
	ExtStsNotExecuted           byte = 0x03
	ExtStsBadIOSAddress         byte = 0x04
	ExtStsParamOutOfRange       byte = 0x05
	ExtStsAddressFieldShort     byte = 0x06
	ExtStsAddressNotExist       byte = 0x07
	ExtStsDataFieldShort        byte = 0x08
	ExtStsInsufficientDataField byte = 0x09
	ExtStsFileNumberNotExist    byte = 0x0C
	ExtStsWrongFileType         byte = 0x0F
	ExtStsElementOutOfRange     byte = 0x10
	ExtStsSubElementOutOfRange  byte = 0x11
	ExtStsFileAccessDenied      byte = 0x12
	ExtStsAccessDenied          byte = 0x13
)

// RequesterIDLength is the fixed requester ID length for PCCC-over-CIP.
// Format: 1-byte length + 2-byte vendor ID + 4-byte serial number = 7 bytes.
const RequesterIDLength byte = 7

// StatusError is a PCCC status failure reported in a reply. STS is the reply
// status byte; EXTSTS is the extended status byte, present only when the STS
// high nibble is 0xF (zero otherwise). Classify with errors.As.
type StatusError struct {
	STS    byte
	EXTSTS byte
}

// Error returns the text format PCCCStatusError has always produced.
func (e *StatusError) Error() string {
	statusName := pcccStatusName(e.STS)
	if e.STS&0xF0 == 0xF0 && e.EXTSTS != 0 {
		return fmt.Sprintf("PCCC error: %s (STS=0x%02X), extended: %s (EXT_STS=0x%02X)",
			statusName, e.STS, pcccExtStatusName(e.EXTSTS), e.EXTSTS)
	}
	return fmt.Sprintf("PCCC error: %s (STS=0x%02X)", statusName, e.STS)
}

// PCCCStatusError returns a descriptive error for a PCCC status byte.
// The returned error is a *StatusError.
func PCCCStatusError(sts byte, extSts byte) error {
	return &StatusError{STS: sts, EXTSTS: extSts}
}

// pcccStatusName describes an STS byte using the DF1 manual (1770-6.5.16)
// status tables. The high nibble carries remote errors; when it is zero the
// low nibble carries local (link/port) errors. The remote texts match
// pycomm3's PCCC_ERROR_CODE and libplctag's pccc_decode_error.
func pcccStatusName(sts byte) string {
	if sts&0xF0 == 0 {
		switch sts {
		case 0x00:
			return "Success"
		case 0x01:
			return "Destination node is out of buffer space"
		case 0x02:
			return "Cannot guarantee delivery, remote node did not ACK command"
		case 0x03:
			return "Duplicate token holder detected"
		case 0x04:
			return "Local port is disconnected"
		case 0x05:
			return "Application layer timed out waiting for a response"
		case 0x06:
			return "Duplicate node detected"
		case 0x07:
			return "Station is offline"
		case 0x08:
			return "Hardware fault"
		default:
			return fmt.Sprintf("Unknown Local Status 0x%02X", sts)
		}
	}
	switch sts & 0xF0 {
	case 0x10:
		return "Illegal Command or Format"
	case 0x20:
		return "Host has a problem and will not communicate"
	case 0x30:
		return "Remote node host is missing, disconnected, or shut down"
	case 0x40:
		return "Host could not complete function due to hardware fault"
	case 0x50:
		return "Addressing problem or memory protect rungs"
	case 0x60:
		return "Function not allowed due to command protection selection"
	case 0x70:
		return "Processor is in Program mode"
	case 0x80:
		return "Compatibility mode file missing or communication zone problem"
	case 0x90:
		return "Remote node cannot buffer command"
	case 0xA0, 0xC0:
		return "Wait ACK (1775-KA buffer full)"
	case 0xB0:
		return "Remote node problem due to download"
	case 0xF0:
		return "Extended Status"
	default:
		return fmt.Sprintf("Unknown Status 0x%02X", sts)
	}
}

// pcccExtStatusName describes an EXT STS byte of a CMD 0x0F reply using the
// DF1 manual (1770-6.5.16) table; codes 0x02-0x0E agree with libplctag's
// pccc_decode_error.
func pcccExtStatusName(extSts byte) string {
	switch extSts {
	case 0x01:
		return "A field has an illegal value"
	case 0x02:
		return "Fewer levels specified in address than minimum for any address"
	case 0x03:
		return "More levels specified in address than system supports"
	case 0x04:
		return "Symbol not found"
	case 0x05:
		return "Symbol is of improper format"
	case 0x06:
		return "Address doesn't point to something usable"
	case 0x07:
		return "File is wrong size"
	case 0x08:
		return "Cannot complete request, situation has changed since the start of the command"
	case 0x09:
		return "Data or file is too large"
	case 0x0A:
		return "Transaction size plus word address is too large"
	case 0x0B:
		return "Access denied, improper privilege"
	case 0x0C:
		return "Condition cannot be generated, resource is not available"
	case 0x0D:
		return "Condition already exists, resource is already available"
	case 0x0E:
		return "Command cannot be executed"
	case 0x0F:
		return "Histogram overflow"
	case 0x10:
		return "No access"
	case 0x11:
		return "Illegal data type"
	case 0x12:
		return "Invalid parameter or invalid data"
	case 0x13:
		return "Address reference exists to deleted area"
	case 0x14:
		return "Command execution failure for unknown reason"
	case 0x15:
		return "Data conversion error"
	case 0x16:
		return "Scanner not able to communicate with 1771 rack adapter"
	case 0x17:
		return "Type mismatch"
	case 0x18:
		return "1771 module response was not valid"
	case 0x19:
		return "Duplicated label"
	case 0x1A:
		return "File is open, another node owns it"
	case 0x1B:
		return "Another node is the program owner"
	case 0x1E:
		return "Data table element protection violation"
	case 0x1F:
		return "Temporary internal problem"
	case 0x22:
		return "Remote rack fault"
	case 0x23:
		return "Timeout"
	case 0x24:
		return "Unknown error"
	default:
		return fmt.Sprintf("Extended Status 0x%02X", extSts)
	}
}
