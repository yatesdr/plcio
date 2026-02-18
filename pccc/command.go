package pccc

import (
	"encoding/binary"
	"fmt"

	"github.com/yatesdr/plcio/cip"
	"github.com/yatesdr/plcio/eip"
)

// buildReadRequest builds a PCCC "Protected Typed Logical Read with 3 Address Fields"
// command (CMD=0x0F, FNC=0xA2) wrapped in CIP Execute PCCC service (0x4B).
//
// PCCC command format:
//
//	[CMD:1] [STS:1] [TNS:2 LE] [FNC:1] [ByteSize] [FileNumber] [FileType] [Element] [SubElement]
//
// Each address field uses compact encoding: values 0-254 as a single byte,
// values 255+ as 0xFF followed by 2-byte little-endian value.
func buildReadRequest(addr *FileAddress, tns uint16, vendorID uint16, serialNum uint32) ([]byte, error) {
	return buildReadRequestN(addr, addr.ReadSize(), tns, vendorID, serialNum)
}

// buildReadRequestN builds a PCCC typed logical read with an explicit byte count.
// This is used for bulk reads where multiple contiguous elements are requested
// in a single PCCC command by specifying byteCount = count * ElementSize.
func buildReadRequestN(addr *FileAddress, byteCount int, tns uint16, vendorID uint16, serialNum uint32) ([]byte, error) {
	// Build the PCCC command payload
	pcccCmd := buildPCCCHeader(CmdTypedCommand, tns, FncProtectedTypedLogicalRead)
	pcccCmd = appendCompactValue(pcccCmd, uint16(byteCount))
	pcccCmd = appendCompactValue(pcccCmd, addr.FileNumber)
	pcccCmd = append(pcccCmd, addr.FileType)
	pcccCmd = appendCompactValue(pcccCmd, addr.Element)
	pcccCmd = appendCompactValue(pcccCmd, addr.SubElement)

	// Wrap in CIP Execute PCCC
	return wrapInCipExecutePCCC(pcccCmd, vendorID, serialNum)
}

// buildWriteRequest builds a PCCC "Protected Typed Logical Write with 3 Address Fields"
// command (CMD=0x0F, FNC=0xAA) wrapped in CIP Execute PCCC service (0x4B).
//
// PCCC command format:
//
//	[CMD:1] [STS:1] [TNS:2 LE] [FNC:1] [ByteSize] [FileNumber] [FileType] [Element] [SubElement] [Data...]
func buildWriteRequest(addr *FileAddress, data []byte, tns uint16, vendorID uint16, serialNum uint32) ([]byte, error) {
	// Build the PCCC command payload
	pcccCmd := buildPCCCHeader(CmdTypedCommand, tns, FncProtectedTypedLogicalWrite)
	pcccCmd = appendCompactValue(pcccCmd, uint16(len(data)))
	pcccCmd = appendCompactValue(pcccCmd, addr.FileNumber)
	pcccCmd = append(pcccCmd, addr.FileType)
	pcccCmd = appendCompactValue(pcccCmd, addr.Element)
	pcccCmd = appendCompactValue(pcccCmd, addr.SubElement)
	pcccCmd = append(pcccCmd, data...)

	// Wrap in CIP Execute PCCC
	return wrapInCipExecutePCCC(pcccCmd, vendorID, serialNum)
}

// buildPCCCHeader creates the common PCCC command header.
//
//	[CMD:1] [STS:1=0x00] [TNS:2 LE] [FNC:1]
func buildPCCCHeader(cmd byte, tns uint16, fnc byte) []byte {
	header := make([]byte, 0, 5)
	header = append(header, cmd)
	header = append(header, 0x00) // STS = 0 in request
	header = binary.LittleEndian.AppendUint16(header, tns)
	header = append(header, fnc)
	return header
}

// appendCompactValue appends a value using PCCC compact encoding:
// values 0-254 as a single byte, values 255+ as 0xFF + 2-byte LE.
func appendCompactValue(buf []byte, value uint16) []byte {
	if value < 255 {
		return append(buf, byte(value))
	}
	buf = append(buf, 0xFF)
	return binary.LittleEndian.AppendUint16(buf, value)
}

// wrapInCipExecutePCCC wraps a PCCC command in a CIP Execute PCCC request.
//
// CIP request format:
//
//	[Service:0x4B] [PathSize] [Path: class 0x67, instance 1]
//	[RequesterIDLen:7] [VendorID:2 LE] [SerialNum:4 LE]
//	[PCCC command bytes...]
func wrapInCipExecutePCCC(pcccPayload []byte, vendorID uint16, serialNum uint32) ([]byte, error) {
	// Build CIP path to PCCC Object (class 0x67, instance 1)
	path, err := cip.EPath().Class(CipClassPCCC).Instance(1).Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build PCCC Object path: %w", err)
	}

	// Build the CIP request
	req := make([]byte, 0, 2+len(path)+7+len(pcccPayload))
	req = append(req, CipSvcExecutePCCC) // Service code
	req = append(req, path.WordLen())     // Path size in words
	req = append(req, path...)            // Path bytes

	// Requester ID (7 bytes: length + vendor ID + serial number)
	req = append(req, RequesterIDLength)
	req = binary.LittleEndian.AppendUint16(req, vendorID)
	req = binary.LittleEndian.AppendUint32(req, serialNum)

	// PCCC command data
	req = append(req, pcccPayload...)

	return req, nil
}

// buildDirectCpf wraps a CIP request in a CPF packet for direct messaging (no routing).
func buildDirectCpf(cipRequest []byte) *eip.EipCommonPacket {
	return &eip.EipCommonPacket{
		Items: []eip.EipCommonPacketItem{
			{TypeId: eip.CpfAddressNullId, Length: 0, Data: nil},
			{TypeId: eip.CpfUnconnectedMessageId, Length: uint16(len(cipRequest)), Data: cipRequest},
		},
	}
}

// buildRoutedCpf wraps a CIP request in a CPF packet with routing via Connection Manager.
func buildRoutedCpf(cipRequest []byte, routePath []byte) *eip.EipCommonPacket {
	// Unconnected Send wraps the CIP request for routing through Connection Manager.
	ucmm := make([]byte, 0, 4+len(cipRequest)+1+2+len(routePath))
	ucmm = append(ucmm, 0x0A) // Priority/time tick
	ucmm = append(ucmm, 0x05) // Timeout ticks
	ucmm = binary.LittleEndian.AppendUint16(ucmm, uint16(len(cipRequest)))
	ucmm = append(ucmm, cipRequest...)
	if len(cipRequest)%2 != 0 {
		ucmm = append(ucmm, 0x00) // Pad to word boundary
	}
	ucmm = append(ucmm, byte(len(routePath)/2)) // Route path size in words
	ucmm = append(ucmm, 0x00)                   // Reserved
	ucmm = append(ucmm, routePath...)

	// Build UCMM request: service 0x52 to Connection Manager (class 0x06, instance 1)
	cmPath, _ := cip.EPath().Class(0x06).Instance(1).Build()
	fullReq := make([]byte, 0, 2+len(cmPath)+len(ucmm))
	fullReq = append(fullReq, 0x52)             // Unconnected_Send service
	fullReq = append(fullReq, cmPath.WordLen()) // Path size in words
	fullReq = append(fullReq, cmPath...)        // Connection Manager path
	fullReq = append(fullReq, ucmm...)

	return &eip.EipCommonPacket{
		Items: []eip.EipCommonPacketItem{
			{TypeId: eip.CpfAddressNullId, Length: 0, Data: nil},
			{TypeId: eip.CpfUnconnectedMessageId, Length: uint16(len(fullReq)), Data: fullReq},
		},
	}
}

// parseCipExecutePCCCResponse parses the CIP response to extract the PCCC response payload.
//
// CIP response format:
//
//	[ReplyService:0xCB] [Reserved:1] [Status:1] [AddlStatusSize:1] [AddlStatus...]
//	[RequesterIDLen:7] [VendorID:2] [SerialNum:4]
//	[PCCC response bytes...]
func parseCipExecutePCCCResponse(data []byte) ([]byte, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("CIP response too short: %d bytes", len(data))
	}

	replyService := data[0]
	status := data[2]
	addlStatusSize := data[3]

	// Check if this is a UCMM response (0xD2 = Unconnected_Send reply)
	if replyService == 0xD2 {
		if status != 0 {
			return nil, fmt.Errorf("CIP Unconnected_Send error: status=0x%02X", status)
		}
		// Strip UCMM wrapper to get embedded response
		embeddedStart := 4 + int(addlStatusSize)*2
		if embeddedStart >= len(data) {
			return nil, fmt.Errorf("UCMM response has no embedded data")
		}
		return parseCipExecutePCCCResponse(data[embeddedStart:])
	}

	// Verify it's an Execute PCCC reply (0x4B | 0x80 = 0xCB)
	if replyService != CipSvcExecutePCCCReply {
		return nil, fmt.Errorf("unexpected CIP reply service: 0x%02X (expected 0x%02X)", replyService, CipSvcExecutePCCCReply)
	}

	// Check CIP status
	if status != 0 {
		if addlStatusSize >= 1 && len(data) >= 6 {
			extStatus := binary.LittleEndian.Uint16(data[4:6])
			return nil, fmt.Errorf("CIP Execute PCCC error: status=0x%02X, extended=0x%04X", status, extStatus)
		}
		return nil, fmt.Errorf("CIP Execute PCCC error: status=0x%02X", status)
	}

	// Skip CIP header (4 bytes + additional status words)
	payloadStart := 4 + int(addlStatusSize)*2
	if payloadStart >= len(data) {
		return nil, fmt.Errorf("CIP response has no PCCC payload")
	}
	payload := data[payloadStart:]

	// Skip requester ID (1-byte length + vendor + serial = 7 bytes)
	if len(payload) < 7 {
		return nil, fmt.Errorf("CIP response missing requester ID")
	}
	idLen := int(payload[0])
	if len(payload) < idLen {
		return nil, fmt.Errorf("CIP response requester ID truncated")
	}
	pcccData := payload[idLen:]

	return pcccData, nil
}

// parsePCCCReadResponse parses the PCCC response to a typed read command.
//
// PCCC response format (success):
//
//	[CMD:1 = 0x4F] [STS:1 = 0x00] [TNS:2 LE] [Data...]
//
// PCCC response format (error with extended status):
//
//	[CMD:1 = 0x4F] [STS:1 with 0xF0] [TNS:2 LE] [EXT_STS:1]
func parsePCCCReadResponse(data []byte) ([]byte, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("PCCC response too short: %d bytes", len(data))
	}

	cmd := data[0]
	sts := data[1]
	// tns := binary.LittleEndian.Uint16(data[2:4])

	// Verify it's a reply to our typed command
	if cmd != CmdTypedReply {
		return nil, fmt.Errorf("unexpected PCCC reply command: 0x%02X (expected 0x%02X)", cmd, CmdTypedReply)
	}

	// Check status
	if sts != StsSuccess {
		var extSts byte
		if sts&0xF0 == 0xF0 && len(data) >= 5 {
			extSts = data[4]
		}
		return nil, PCCCStatusError(sts, extSts)
	}

	// Return data after the 4-byte header
	return data[4:], nil
}

// parsePCCCWriteResponse parses the PCCC response to a typed write command.
// The response has no data payload on success, just the 4-byte header.
func parsePCCCWriteResponse(data []byte) error {
	if len(data) < 4 {
		return fmt.Errorf("PCCC response too short: %d bytes", len(data))
	}

	cmd := data[0]
	sts := data[1]

	if cmd != CmdTypedReply {
		return fmt.Errorf("unexpected PCCC reply command: 0x%02X (expected 0x%02X)", cmd, CmdTypedReply)
	}

	if sts != StsSuccess {
		var extSts byte
		if sts&0xF0 == 0xF0 && len(data) >= 5 {
			extSts = data[4]
		}
		return PCCCStatusError(sts, extSts)
	}

	return nil
}
