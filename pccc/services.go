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

	// CmdDiagnosticStatus is the Diagnostic Status command (no FNC byte).
	// Returns processor catalog string and status information.
	CmdDiagnosticStatus byte = 0x06

	// CmdDiagnosticReply is the reply to Diagnostic Status.
	CmdDiagnosticReply byte = 0x46 // 0x06 | 0x40
)

// PCCC function codes for typed commands (CMD=0x0F).
const (
	// FncProtectedTypedLogicalRead reads data using 3-address-field format.
	// Used by SLC500, MicroLogix, and PLC-5.
	FncProtectedTypedLogicalRead byte = 0xA2

	// FncProtectedTypedLogicalWrite writes data using 3-address-field format.
	// Used by SLC500, MicroLogix, and PLC-5.
	FncProtectedTypedLogicalWrite byte = 0xAA

	// FncTypedRead is the PLC-5 typed read function.
	FncTypedRead byte = 0x68

	// FncTypedWrite is the PLC-5 typed write function.
	FncTypedWrite byte = 0x67

	// FncReadSection reads a section of a data file (used for file directory discovery).
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

// PCCCStatusError returns a descriptive error for a PCCC status byte.
func PCCCStatusError(sts byte, extSts byte) error {
	statusName := pcccStatusName(sts)
	if sts&0xF0 == 0xF0 && extSts != 0 {
		return fmt.Errorf("PCCC error: %s (STS=0x%02X), extended: %s (EXT_STS=0x%02X)",
			statusName, sts, pcccExtStatusName(extSts), extSts)
	}
	return fmt.Errorf("PCCC error: %s (STS=0x%02X)", statusName, sts)
}

func pcccStatusName(sts byte) string {
	// The low nibble is the specific error code
	switch sts & 0xF0 {
	case 0x00:
		return "Success"
	case 0x10:
		return "Illegal Command or Format"
	case 0x20:
		return "Host has a Problem"
	case 0x30:
		return "Remote Node has a Problem"
	case 0x40:
		return "Hardware Fault"
	case 0x50:
		return "Address Problem"
	case 0x60:
		return "Function Not Allowed"
	case 0x70:
		return "Target Node Problem"
	case 0x80:
		return "Command Parameter Types Mismatch"
	case 0x90:
		return "Data Field Error"
	case 0xA0:
		return "Access Denied"
	case 0xB0:
		return "No Function Error"
	case 0xC0:
		return "Data Conversion Error"
	case 0xD0:
		return "Scanner Suspended Error"
	case 0xE0:
		return "Not Compatible"
	case 0xF0:
		return "Extended Status"
	default:
		return fmt.Sprintf("Unknown Status 0x%02X", sts)
	}
}

func pcccExtStatusName(extSts byte) string {
	switch extSts {
	case ExtStsNotAllowed:
		return "Not Allowed"
	case ExtStsPrivilegeViolation:
		return "Privilege Violation"
	case ExtStsNotExecuted:
		return "Not Executed"
	case ExtStsBadIOSAddress:
		return "Bad IOS Address"
	case ExtStsParamOutOfRange:
		return "Parameter Out of Range"
	case ExtStsAddressFieldShort:
		return "Address Field Too Short"
	case ExtStsAddressNotExist:
		return "Address Does Not Exist"
	case ExtStsDataFieldShort:
		return "Data Field Too Short"
	case ExtStsInsufficientDataField:
		return "Insufficient Data Field"
	case ExtStsFileNumberNotExist:
		return "File Number Does Not Exist"
	case ExtStsWrongFileType:
		return "Wrong File Type"
	case ExtStsElementOutOfRange:
		return "Element Out of Range"
	case ExtStsSubElementOutOfRange:
		return "Sub-Element Out of Range"
	case ExtStsFileAccessDenied:
		return "File Access Denied"
	case ExtStsAccessDenied:
		return "Access Denied"
	default:
		return fmt.Sprintf("Extended Status 0x%02X", extSts)
	}
}
