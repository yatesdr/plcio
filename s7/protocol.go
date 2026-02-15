package s7

import (
	"encoding/binary"
	"fmt"

	"github.com/yatesdr/plcio/logging"
)

const (
	s7ProtocolID = 0x32

	// Message Types
	s7MsgJob     = 0x01
	s7MsgAck     = 0x02 // Acknowledgement without data
	s7MsgAckData = 0x03 // Acknowledgement with data

	// Functions
	s7FuncSetupComm = 0xF0
	s7FuncRead      = 0x04
	s7FuncWrite     = 0x05

	// Area Codes (for S7ANY addressing)
	s7AreaSysInfo = 0x03 // System info
	s7AreaSysFlg  = 0x05 // System flags
	s7AreaAnaIn   = 0x06 // Analog inputs
	s7AreaAnaOut  = 0x07 // Analog outputs
	s7AreaC       = 0x1C // Counters (S7-200/300)
	s7AreaT       = 0x1D // Timers (S7-200/300)
	s7AreaC200    = 0x1E // IEC counters (S7-200)
	s7AreaT200    = 0x1F // IEC timers (S7-200)
	s7AreaI       = 0x81 // Inputs
	s7AreaQ       = 0x82 // Outputs
	s7AreaM       = 0x83 // Markers/Flags
	s7AreaDB      = 0x84 // Data blocks
	s7AreaDI      = 0x85 // Instance data blocks
	s7AreaLocal   = 0x86 // Local data
	s7AreaV       = 0x87 // V memory (S7-200)

	// Transport sizes for S7ANY
	tsNULL  = 0x00
	tsBIT   = 0x01
	tsBYTE  = 0x02
	tsCHAR  = 0x03
	tsWORD  = 0x04
	tsINT   = 0x05
	tsDWORD = 0x06
	tsDINT  = 0x07
	tsREAL  = 0x08

	// S7ANY constants
	s7AnySpecType = 0x12
	s7AnyLen      = 0x0A
	s7AnySyntaxID = 0x10
)

// buildSetupCommRequest creates an S7 Setup Communication request PDU.
func buildSetupCommRequest(pduSize uint16) []byte {
	// S7 Header (10 bytes for Job)
	header := []byte{
		s7ProtocolID, // Protocol ID
		s7MsgJob,     // Message type: Job
		0x00, 0x00,   // Reserved
		0x00, 0x00,   // PDU reference
		0x00, 0x08,   // Parameter length: 8 bytes
		0x00, 0x00,   // Data length: 0
	}

	// Setup Communication parameters (8 bytes)
	params := []byte{
		s7FuncSetupComm, // Function: Setup Communication
		0x00,            // Reserved
		0x00, 0x01,      // Max AMQ calling
		0x00, 0x01,      // Max AMQ called
		byte(pduSize >> 8), byte(pduSize), // PDU size
	}

	return append(header, params...)
}

// parseSetupCommResponse parses an S7 Setup Communication response.
func parseSetupCommResponse(data []byte) (uint16, error) {
	// Minimum: 10 byte header + 8 byte params = 18 bytes
	// Response header is 12 bytes (includes error class/code)
	if len(data) < 20 {
		return 0, fmt.Errorf("setup response too short: %d bytes", len(data))
	}

	// Check protocol ID
	if data[0] != s7ProtocolID {
		return 0, fmt.Errorf("invalid protocol ID: 0x%02X", data[0])
	}

	// Check message type
	if data[1] != s7MsgAckData {
		return 0, fmt.Errorf("unexpected message type: 0x%02X", data[1])
	}

	// Check error class/code (bytes 10-11 in response)
	if data[10] != 0 || data[11] != 0 {
		return 0, S7Error{Class: data[10], Code: data[11]}
	}

	// Check function code
	if data[12] != s7FuncSetupComm {
		return 0, fmt.Errorf("unexpected function: 0x%02X", data[12])
	}

	// Extract negotiated PDU size (last 2 bytes of params)
	pduSize := binary.BigEndian.Uint16(data[18:20])
	return pduSize, nil
}

// buildReadRequest creates an S7 Read Variable request PDU.
func buildReadRequest(addrs []*Address, pduRef uint16) []byte {
	itemCount := len(addrs)

	// S7 Header (10 bytes for Job)
	paramLen := 2 + itemCount*12 // function + count + items
	header := []byte{
		s7ProtocolID,              // Protocol ID
		s7MsgJob,                  // Message type: Job
		0x00, 0x00,                // Reserved
		byte(pduRef >> 8), byte(pduRef), // PDU reference
		byte(paramLen >> 8), byte(paramLen), // Parameter length
		0x00, 0x00, // Data length: 0 for read request
	}

	// Parameters: function + count + items
	params := []byte{
		s7FuncRead,      // Function: Read Variable
		byte(itemCount), // Item count
	}

	// Add S7ANY items
	for _, addr := range addrs {
		item := addressToS7Any(addr)
		params = append(params, item...)
	}

	return append(header, params...)
}

// parseReadResponse parses an S7 Read Variable response.
func parseReadResponse(data []byte, count int) ([][]byte, []error) {
	results := make([][]byte, count)
	errors := make([]error, count)

	// Minimum header size check
	if len(data) < 12 {
		logging.DebugLog("S7", "parseReadResponse: response too short (%d bytes, need 12)", len(data))
		for i := range errors {
			errors[i] = fmt.Errorf("response too short")
		}
		return results, errors
	}

	// Check protocol ID
	if data[0] != s7ProtocolID {
		logging.DebugLog("S7", "parseReadResponse: invalid protocol ID 0x%02X", data[0])
		for i := range errors {
			errors[i] = fmt.Errorf("invalid protocol ID: 0x%02X", data[0])
		}
		return results, errors
	}

	// Check message type
	if data[1] != s7MsgAckData {
		// Message type 0x02 is ACK (no data) - usually indicates an error
		// ACK format: [protocolID, msgType, reserved(2), pduRef(2), paramLen(2), dataLen(2), errClass, errCode]
		if data[1] == 0x02 && len(data) >= 12 {
			// Error class/code are at positions 10-11 (same as AckData)
			errClass := data[10]
			errCode := data[11]
			err := S7Error{Class: errClass, Code: errCode}
			logging.DebugLog("S7", "parseReadResponse: ACK error class=0x%02X code=0x%02X: %v", errClass, errCode, err)
			for i := range errors {
				errors[i] = err
			}
			return results, errors
		}
		logging.DebugLog("S7", "parseReadResponse: unexpected message type 0x%02X", data[1])
		for i := range errors {
			errors[i] = fmt.Errorf("unexpected message type: 0x%02X", data[1])
		}
		return results, errors
	}

	// Check error class/code
	if data[10] != 0 || data[11] != 0 {
		err := S7Error{Class: data[10], Code: data[11]}
		logging.DebugLog("S7", "parseReadResponse: S7 error class=0x%02X code=0x%02X: %v", data[10], data[11], err)
		for i := range errors {
			errors[i] = err
		}
		return results, errors
	}

	// Get parameter and data lengths
	paramLen := binary.BigEndian.Uint16(data[6:8])
	dataLen := binary.BigEndian.Uint16(data[8:10])

	logging.DebugLog("S7", "parseReadResponse: paramLen=%d dataLen=%d totalLen=%d", paramLen, dataLen, len(data))

	// Skip header (12 bytes) and parameters
	dataStart := 12 + int(paramLen)
	if dataStart > len(data) || int(dataLen) > len(data)-dataStart {
		logging.DebugLog("S7", "parseReadResponse: invalid lengths - dataStart=%d dataLen=%d totalLen=%d", dataStart, dataLen, len(data))
		for i := range errors {
			errors[i] = fmt.Errorf("invalid response lengths")
		}
		return results, errors
	}

	// Parse data items
	pos := dataStart
	for i := 0; i < count; i++ {
		if pos >= len(data) {
			// No more data - mark remaining items as failed
			logging.DebugLog("S7", "parseReadResponse item %d: unexpected end of data at pos=%d, failing remaining %d items",
				i, pos, count-i)
			for j := i; j < count; j++ {
				errors[j] = fmt.Errorf("unexpected end of data (item %d of %d)", j+1, count)
			}
			break
		}

		// Data item header: return code, transport size, length
		returnCode := data[pos]
		if returnCode != dataItemSuccess {
			errMsg := dataItemError(returnCode)
			logging.DebugLog("S7", "parseReadResponse item %d: error returnCode=0x%02X (%s)", i, returnCode, errMsg)
			errors[i] = fmt.Errorf("%s", errMsg)
			// Skip past this item - S7 error items are 1 byte (just the return code)
			pos++
			continue
		}

		// Success - parse data
		if pos+4 > len(data) {
			// Header incomplete - mark remaining items as failed
			logging.DebugLog("S7", "parseReadResponse item %d: header too short at pos=%d, need 4 bytes, have %d, failing remaining items",
				i, pos, len(data)-pos)
			for j := i; j < count; j++ {
				errors[j] = fmt.Errorf("data item header too short")
			}
			break
		}

		transportSize := data[pos+1]
		dataLen := binary.BigEndian.Uint16(data[pos+2 : pos+4])

		// Calculate byte length from response
		// S7 response transport sizes differ from request codes:
		//   0x03 = BIT access (length in bits)
		//   0x04 = BYTE/WORD/INT access (length in bits)
		//   0x05 = INTEGER (length in bits)
		//   0x06 = DINT (length in bits)
		//   0x07 = REAL (length in bits)
		//   0x09 = OCTET STRING (length in bytes)
		// Only 0x09 (octet string) has length in bytes; all others are in bits
		var byteLen int
		if transportSize == 0x09 {
			// Octet string: length is in bytes
			byteLen = int(dataLen)
		} else {
			// All other types: length is in bits
			byteLen = int((dataLen + 7) / 8)
		}

		logging.DebugLog("S7", "parseReadResponse item %d: transportSize=0x%02X dataLen=%d (bits) byteLen=%d pos=%d remaining=%d",
			i, transportSize, dataLen, byteLen, pos, len(data)-pos)

		pos += 4 // Skip header

		if pos+byteLen > len(data) {
			// Data truncated - mark remaining items as failed
			logging.DebugLog("S7", "parseReadResponse item %d: data truncated - need %d bytes at pos=%d, have %d remaining, failing remaining items",
				i, byteLen, pos, len(data)-pos)
			for j := i; j < count; j++ {
				errors[j] = fmt.Errorf("data truncated: need %d bytes, have %d", byteLen, len(data)-pos)
			}
			break
		}

		results[i] = make([]byte, byteLen)
		copy(results[i], data[pos:pos+byteLen])
		pos += byteLen

		// Items are padded to even bytes (except last)
		if i < count-1 && byteLen%2 == 1 {
			pos++
		}
	}

	return results, errors
}

// buildWriteRequest creates an S7 Write Variable request PDU.
func buildWriteRequest(addr *Address, writeData []byte, pduRef uint16) []byte {
	// S7 Header (10 bytes for Job)
	paramLen := 2 + 12 // function + count + 1 item
	// Data: return code (1) + transport size (1) + length (2) + data
	dataLen := 4 + len(writeData)
	// Pad to even length
	if len(writeData)%2 == 1 {
		dataLen++
	}

	header := []byte{
		s7ProtocolID,              // Protocol ID
		s7MsgJob,                  // Message type: Job
		0x00, 0x00,                // Reserved
		byte(pduRef >> 8), byte(pduRef), // PDU reference
		byte(paramLen >> 8), byte(paramLen), // Parameter length
		byte(dataLen >> 8), byte(dataLen), // Data length
	}

	// Parameters: function + count + item
	params := []byte{
		s7FuncWrite, // Function: Write Variable
		0x01,        // Item count: 1
	}
	params = append(params, addressToS7Any(addr)...)

	// Data section transport size differs from parameter section:
	// - 0x03 for BIT access (length in bits)
	// - 0x04 for BYTE/WORD/DWORD access (length in bits)
	// - 0x09 for OCTET STRING (length in bytes)
	var dataTransportSize byte
	var bitLen int
	if addr.BitNum >= 0 {
		dataTransportSize = 0x03 // BIT
		bitLen = 1
	} else {
		dataTransportSize = 0x04 // BYTE/WORD/DWORD
		bitLen = len(writeData) * 8
	}

	dataSection := []byte{
		0x00,                                // Return code placeholder
		dataTransportSize,                   // Transport size for data section
		byte(bitLen >> 8), byte(bitLen),     // Bit length
	}
	dataSection = append(dataSection, writeData...)

	// Pad to even length
	if len(writeData)%2 == 1 {
		dataSection = append(dataSection, 0x00)
	}

	result := append(header, params...)
	result = append(result, dataSection...)
	return result
}

// parseWriteResponse parses an S7 Write Variable response.
func parseWriteResponse(data []byte) error {
	// Minimum header size check
	if len(data) < 12 {
		return fmt.Errorf("response too short")
	}

	// Check protocol ID
	if data[0] != s7ProtocolID {
		return fmt.Errorf("invalid protocol ID: 0x%02X", data[0])
	}

	// Check message type - accept both Ack (0x02) and AckData (0x03)
	msgType := data[1]
	if msgType != s7MsgAckData && msgType != s7MsgAck {
		return fmt.Errorf("unexpected message type: 0x%02X", msgType)
	}

	// Check error class/code (same position for both Ack and AckData)
	if data[10] != 0 || data[11] != 0 {
		err := S7Error{Class: data[10], Code: data[11]}
		logging.DebugLog("S7", "parseWriteResponse: S7 error class=0x%02X code=0x%02X: %v", data[10], data[11], err)
		return err
	}

	// For Ack messages (0x02), no data section - success if no error
	if msgType == s7MsgAck {
		return nil
	}

	// For AckData messages (0x03), check the data item return code
	paramLen := binary.BigEndian.Uint16(data[6:8])
	dataStart := 12 + int(paramLen)

	if dataStart >= len(data) {
		return fmt.Errorf("no data in response")
	}

	// Check data item return code
	returnCode := data[dataStart]
	if returnCode != dataItemSuccess {
		return fmt.Errorf("%s", dataItemError(returnCode))
	}

	return nil
}

// addressToS7Any converts an Address to S7ANY item bytes.
func addressToS7Any(addr *Address) []byte {
	// Determine area code
	var areaCode byte
	switch addr.Area {
	case AreaI:
		areaCode = s7AreaI
	case AreaQ:
		areaCode = s7AreaQ
	case AreaM:
		areaCode = s7AreaM
	case AreaDB:
		areaCode = s7AreaDB
	case AreaT:
		areaCode = s7AreaT
	case AreaC:
		areaCode = s7AreaC
	default:
		areaCode = s7AreaDB
	}

	// Determine transport size
	transportSize := getTransportSize(addr.DataType, addr.BitNum >= 0)

	// Determine count - the number of transport-sized elements to read
	var count int
	if addr.BitNum >= 0 {
		count = 1 // Single bit
	} else if transportSize == tsBYTE || transportSize == tsCHAR {
		// For byte transport, count is in bytes
		count = addr.Size
	} else {
		// For larger transport sizes (WORD, DWORD, etc.), count is number of elements
		count = addr.Count
		if count < 1 {
			count = 1
		}
	}

	// Encode address: (byte_offset << 3) | bit_number
	// 24-bit big-endian
	bitAddr := addr.Offset * 8
	if addr.BitNum >= 0 {
		bitAddr += addr.BitNum
	}

	dbNumber := addr.DBNumber
	if addr.Area != AreaDB {
		dbNumber = 0
	}

	logging.DebugLog("S7", "addressToS7Any: area=%s db=%d offset=%d bitAddr=%d transportSize=0x%02X count=%d",
		addr.Area, dbNumber, addr.Offset, bitAddr, transportSize, count)

	return []byte{
		s7AnySpecType, // Specification type
		s7AnyLen,      // Length of this item
		s7AnySyntaxID, // Syntax ID: S7ANY
		transportSize, // Transport size
		byte(count >> 8), byte(count), // Count
		byte(dbNumber >> 8), byte(dbNumber), // DB number
		areaCode,                                                     // Area
		byte(bitAddr >> 16), byte(bitAddr >> 8), byte(bitAddr), // Address (24-bit)
	}
}

// getTransportSize returns the S7 transport size code for a data type.
func getTransportSize(dataType uint16, isBit bool) byte {
	if isBit {
		return tsBIT
	}

	baseType := BaseType(dataType)
	switch baseType {
	case TypeBool:
		return tsBIT
	case TypeByte, TypeSInt, TypeChar:
		return tsBYTE
	case TypeWord, TypeInt, TypeDate, TypeWChar:
		return tsWORD
	case TypeDWord, TypeDInt, TypeTime, TypeTimeOfDay:
		return tsDWORD
	case TypeReal:
		return tsREAL
	case TypeLWord, TypeLInt, TypeLReal:
		// 64-bit types - use BYTE transport
		return tsBYTE
	default:
		return tsBYTE
	}
}
