package pccc

import (
	"encoding/binary"
	"fmt"
)

// PLC-5 command set.
//
// PLC-5 processors do not take the SLC "protected typed logical" commands
// (FNC 0xA1/0xA2/0xAA/0xAB); the DF1 manual (1770-6.5.16 ch. 7) lists those
// for SLC 500 / 5/03 / 5/04 only. PLC-5 data table access uses:
//
//	Typed Read  (read block)  CMD 0F FNC 68  (1770-6.5.16 p. 7-28)
//	  [PACKET OFFSET:2] [TOTAL TRANS:2] [PLC-5 system address] [SIZE:2]
//	  reply: [type/data parameter] [data]
//	Typed Write (write block) CMD 0F FNC 67  (p. 7-30)
//	  [PACKET OFFSET:2] [TOTAL TRANS:2] [PLC-5 system address]
//	  [type/data parameter] [data]
//	Read-Modify-Write (write bit) CMD 0F FNC 26  (p. 7-20)
//	  [PLC-5 system address] [AND mask:2] [OR mask:2]
//
// SIZE, PACKET OFFSET and TOTAL TRANS are 2 bytes, low byte first, and count
// elements, not bytes (ch. 6 "SIZE": "For PLC-5 ... typed read and typed
// write commands, the SIZE field specifies the number of elements"). One
// command carries the whole transaction here, so PACKET OFFSET is 0 and
// TOTAL TRANS equals SIZE, exactly as libplctag's typed read/write for
// PCCC-mapped Logix (ab/eip_lgx_pccc.c: pccc_offset = 0, pccc_transfer_size
// = elem_count, then the address, then elem_count).
//
// The PLC-5 system address is sent in logical binary form (p. 13-11): a mask
// byte whose bits 0..3 flag which of levels 1..4 follow, then each flagged
// level (0..254 as one byte, else FF + 2 bytes LE). Level 1 (data table
// area, default 0) is left at its default, level 2 is the file number,
// level 3 the element (word) and level 4 the sub-element. This is libplctag's
// plc5_encode_address (mask 0x06, or 0x0E with a sub-element) and matches the
// manual's example for B3:300 (06 03 FF 2C 01).

// PLC-5 function codes (CMD 0x0F).
const (
	// FncReadModifyWrite is the PLC-5 read-modify-write (write bit)
	// function: the processor applies AND then OR masks to one word.
	FncReadModifyWrite byte = 0x26
)

// Type/data parameter data type IDs (1770-6.5.16 p. 7-29).
const (
	TypeIDBit        = 1
	TypeIDBitString  = 2
	TypeIDByteString = 3
	TypeIDInteger    = 4
	TypeIDTimer      = 5
	TypeIDCounter    = 6
	TypeIDControl    = 7
	TypeIDFloat      = 8
	TypeIDArray      = 9
	TypeIDAddress    = 15
	TypeIDBCD        = 16
)

// plc5MaxTypedData is the data limit of one typed read/write: "up to 240
// bytes minus the number of bytes used in the type/data parameter"
// (1770-6.5.16 p. 7-28/7-30).
const plc5MaxTypedData = 240

// appendPLC5Address appends the PLC-5 logical binary system address for addr.
// The sub-element level is included when the address names a sub-element,
// and for bit access to a T/C/R element (T4:0/13 is a bit of the control
// word, sub-element 0): read-modify-write "must point to a word" (p. 7-20).
func appendPLC5Address(buf []byte, addr *FileAddress) []byte {
	mask := byte(0x06) // levels 2 (file) and 3 (element); level 1 defaults to 0
	withSub := addr.HasSubElement || addr.SubElement > 0 ||
		(IsComplexType(addr.FileType) && addr.BitNumber >= 0)
	if withSub {
		mask |= 0x08
	}
	buf = append(buf, mask)
	buf = appendCompactValue(buf, addr.FileNumber)
	buf = appendCompactValue(buf, addr.Element)
	if withSub {
		buf = appendCompactValue(buf, addr.SubElement)
	}
	return buf
}

// appendTypeData appends a type/data parameter (1770-6.5.16 p. 7-28/7-29,
// examples p. 7-36): a flag byte whose high nibble holds the type ID (bit 7
// clear, ID in bits 6..4 when ID <= 7; bit 7 set and bits 6..4 = number of ID
// bytes that follow otherwise) and whose low nibble does the same for the
// size. Extension bytes are least significant first, ID bytes before size
// bytes. The shortest form is always produced.
func appendTypeData(buf []byte, typeID, size uint32) []byte {
	flagAt := len(buf)
	buf = append(buf, 0)
	var flag byte
	if typeID <= 7 {
		flag = byte(typeID) << 4
	} else {
		n := 0
		for v := typeID; v != 0; v >>= 8 {
			buf = append(buf, byte(v))
			n++
		}
		flag = 0x80 | byte(n)<<4
	}
	if size <= 7 {
		flag |= byte(size)
	} else {
		n := 0
		for v := size; v != 0; v >>= 8 {
			buf = append(buf, byte(v))
			n++
		}
		flag |= 0x08 | byte(n)
	}
	buf[flagAt] = flag
	return buf
}

// parseTypeData decodes one type/data parameter from b and returns the type
// ID, the size value and the number of bytes consumed. All three equivalent
// encodings in the manual (p. 7-36: 0x43 / 0x49 0x03 / 0x4A 0x03 0x00) decode
// the same. Extensions longer than 4 bytes are rejected (the manual allows
// up to 7, but no PLC-5 value needs more than 2).
func parseTypeData(b []byte) (typeID, size uint32, n int, err error) {
	if len(b) < 1 {
		return 0, 0, 0, fmt.Errorf("type/data parameter missing")
	}
	flag := b[0]
	n = 1
	ext := func(count int, what string) (uint32, error) {
		if count > 4 {
			return 0, fmt.Errorf("type/data parameter %s extension of %d bytes not supported", what, count)
		}
		if n+count > len(b) {
			return 0, fmt.Errorf("type/data parameter truncated: need %d %s bytes, have %d", count, what, len(b)-n)
		}
		var v uint32
		for i := 0; i < count; i++ {
			v |= uint32(b[n+i]) << (8 * uint(i))
		}
		n += count
		return v, nil
	}
	if flag&0x80 != 0 {
		if typeID, err = ext(int(flag>>4&0x07), "type ID"); err != nil {
			return 0, 0, 0, err
		}
	} else {
		typeID = uint32(flag >> 4 & 0x07)
	}
	if flag&0x08 != 0 {
		if size, err = ext(int(flag&0x07), "size"); err != nil {
			return 0, 0, 0, err
		}
	} else {
		size = uint32(flag & 0x07)
	}
	return typeID, size, n, nil
}

// plc5TypedData is a decoded typed read reply (or the parameter to echo in a
// typed write).
type plc5TypedData struct {
	param    []byte // the raw type/data parameter bytes (outer + element descriptor)
	elemType uint32 // element type ID
	elemSize int    // bytes per element
	data     []byte // element data
}

// decodePLC5TypedData splits a typed read reply payload into its type/data
// parameter and data. An array (ID 9) carries a second parameter, the element
// descriptor, whose bytes count toward the array's size value (p. 7-37,
// examples 2 and 3). The declared size must match the bytes present exactly:
// a mismatch means the reply cannot be trusted, and returning a partial or
// padded element would decode a wrong value.
func decodePLC5TypedData(payload []byte) (*plc5TypedData, error) {
	typeID, size, n, err := parseTypeData(payload)
	if err != nil {
		return nil, err
	}
	rest := payload[n:]
	if typeID != TypeIDArray {
		if uint64(len(rest)) != uint64(size) {
			return nil, fmt.Errorf("typed reply: type %d declares %d data bytes, got %d", typeID, size, len(rest))
		}
		return &plc5TypedData{param: payload[:n], elemType: typeID, elemSize: int(size), data: rest}, nil
	}
	if uint64(len(rest)) != uint64(size) {
		return nil, fmt.Errorf("typed reply: array declares %d bytes (descriptor + data), got %d", size, len(rest))
	}
	elemType, elemSize, dn, err := parseTypeData(rest)
	if err != nil {
		return nil, fmt.Errorf("typed reply array descriptor: %w", err)
	}
	if elemType == TypeIDArray {
		return nil, fmt.Errorf("typed reply: nested arrays are not supported")
	}
	data := rest[dn:]
	if elemSize == 0 || uint64(len(data))%uint64(elemSize) != 0 {
		return nil, fmt.Errorf("typed reply: %d data bytes is not a whole number of %d-byte elements", len(data), elemSize)
	}
	return &plc5TypedData{param: payload[:n+dn], elemType: elemType, elemSize: int(elemSize), data: data}, nil
}

// plc5ExpectedElementSize is the size of one addressed item: a word for
// sub-element and bit access, otherwise the file's element size.
func plc5ExpectedElementSize(addr *FileAddress) int {
	if addr.BitNumber >= 0 || addr.SubElement > 0 || addr.HasSubElement {
		return SubElementSize
	}
	return ElementSize(addr.FileType)
}

// checkPLC5Type verifies that a typed reply describes the kind of data the
// address expects. PLC-5 logical binary addresses carry no file type, so this
// is what stops "F8:0" being decoded from an integer file (or "T4:0" from a
// counter file) when the letter in the address does not match the file.
func checkPLC5Type(addr *FileAddress, td *plc5TypedData, count int) error {
	want := plc5ExpectedElementSize(addr)
	if len(td.data) != count*want {
		return fmt.Errorf("typed reply has %d data bytes, expected %d (%d x %d-byte %s element); check the file type letter",
			len(td.data), count*want, count, want, FileTypeName(addr.FileType))
	}
	wordAccess := addr.BitNumber >= 0 || addr.SubElement > 0 || addr.HasSubElement
	structural := func(id uint32) bool {
		return id == TypeIDTimer || id == TypeIDCounter || id == TypeIDControl || id == TypeIDFloat
	}
	switch {
	case addr.FileType == FileTypeFloat && !wordAccess:
		if td.elemType != TypeIDFloat {
			return fmt.Errorf("typed reply element type %d is not IEEE float (8); %s is not a float file", td.elemType, addr.RawAddress)
		}
	case IsComplexType(addr.FileType) && !wordAccess:
		wantID := uint32(TypeIDTimer)
		switch addr.FileType {
		case FileTypeCounter:
			wantID = TypeIDCounter
		case FileTypeControl:
			wantID = TypeIDControl
		}
		if td.elemType != wantID && td.elemType != TypeIDInteger {
			return fmt.Errorf("typed reply element type %d, expected %d (%s); check the file type letter", td.elemType, wantID, FileTypeName(addr.FileType))
		}
	case want == SubElementSize:
		// A data table word: N, B, I, O, S, A, or a T/C/R sub-element.
		if structural(td.elemType) || td.elemSize != SubElementSize {
			return fmt.Errorf("typed reply element (type %d, %d bytes) is not a 16-bit word; check the file type letter of %s", td.elemType, td.elemSize, addr.RawAddress)
		}
	default:
		if structural(td.elemType) {
			return fmt.Errorf("typed reply element type %d does not match %s (%s file)", td.elemType, addr.RawAddress, FileTypeName(addr.FileType))
		}
	}
	return nil
}

// plc5SwapFloatWords exchanges the two 16-bit words of every 4-byte float in
// b, in place. libplctag's plc5_tag_byte_order (ab/eip_plc5_pccc.c) records,
// "verified against hardware", that the PLC-5 sends a 32-bit float high word
// first with the bytes inside each word unchanged; Tag.Bytes and the float
// encoder use standard little-endian IEEE 754, so PLC-5 float data is swapped
// on the way in and out. (libplctag observed this with word range reads; the
// same data table words are returned by typed reads. LAB: confirm with a
// known F value.)
func plc5SwapFloatWords(b []byte) {
	for i := 0; i+3 < len(b); i += 4 {
		b[i], b[i+1], b[i+2], b[i+3] = b[i+2], b[i+3], b[i], b[i+1]
	}
}

// buildPLC5TypedReadRequest builds CMD 0F FNC 68 for count elements.
func buildPLC5TypedReadRequest(addr *FileAddress, count int, tns uint16, vendorID uint16, serialNum uint32) ([]byte, error) {
	if count <= 0 || count > 0xFFFF {
		return nil, fmt.Errorf("PLC-5 typed read: element count %d out of range (1..65535)", count)
	}
	cmd := buildPCCCHeader(CmdTypedCommand, tns, FncTypedRead)
	cmd = binary.LittleEndian.AppendUint16(cmd, 0)             // packet offset
	cmd = binary.LittleEndian.AppendUint16(cmd, uint16(count)) // total trans
	cmd = appendPLC5Address(cmd, addr)
	cmd = binary.LittleEndian.AppendUint16(cmd, uint16(count)) // size (elements)
	return wrapInCipExecutePCCC(cmd, vendorID, serialNum)
}

// buildPLC5TypedWriteRequest builds CMD 0F FNC 67. param is the type/data
// parameter (as returned by a typed read of the same address and count).
func buildPLC5TypedWriteRequest(addr *FileAddress, count int, param, data []byte, tns uint16, vendorID uint16, serialNum uint32) ([]byte, error) {
	if count <= 0 || count > 0xFFFF {
		return nil, fmt.Errorf("PLC-5 typed write: element count %d out of range (1..65535)", count)
	}
	if len(param)+len(data) > plc5MaxTypedData {
		return nil, fmt.Errorf("PLC-5 typed write: %d parameter + %d data bytes exceeds %d", len(param), len(data), plc5MaxTypedData)
	}
	cmd := buildPCCCHeader(CmdTypedCommand, tns, FncTypedWrite)
	cmd = binary.LittleEndian.AppendUint16(cmd, 0)             // packet offset
	cmd = binary.LittleEndian.AppendUint16(cmd, uint16(count)) // total trans
	cmd = appendPLC5Address(cmd, addr)
	cmd = append(cmd, param...)
	cmd = append(cmd, data...)
	return wrapInCipExecutePCCC(cmd, vendorID, serialNum)
}

// buildPLC5ReadModifyWriteRequest builds CMD 0F FNC 26 for one word: the
// processor resets the bits that are 0 in andMask, then sets the bits that
// are 1 in orMask (1770-6.5.16 p. 7-20; libplctag plc5_tag_write_bit_start:
// encoded address, AND/reset mask, OR/set mask, each 2 bytes LE).
func buildPLC5ReadModifyWriteRequest(addr *FileAddress, andMask, orMask uint16, tns uint16, vendorID uint16, serialNum uint32) ([]byte, error) {
	cmd := buildPCCCHeader(CmdTypedCommand, tns, FncReadModifyWrite)
	cmd = appendPLC5Address(cmd, addr)
	cmd = binary.LittleEndian.AppendUint16(cmd, andMask)
	cmd = binary.LittleEndian.AppendUint16(cmd, orMask)
	return wrapInCipExecutePCCC(cmd, vendorID, serialNum)
}

// plc5TypedRead reads count elements starting at addr with Typed Read and
// returns the validated element data (floats converted to little-endian).
func (p *PLC) plc5TypedRead(addr *FileAddress, count int) (*plc5TypedData, error) {
	tns := p.nextTNS()
	cipReq, err := buildPLC5TypedReadRequest(addr, count, tns, p.vendorID, p.serialNum)
	if err != nil {
		return nil, err
	}
	cipResp, err := p.sendCipRequest(cipReq)
	if err != nil {
		return nil, err
	}
	pcccResp, err := parseCipExecutePCCCResponse(cipResp)
	if err != nil {
		return nil, err
	}
	payload, err := parsePCCCReadResponse(pcccResp, tns)
	if err != nil {
		return nil, err
	}
	td, err := decodePLC5TypedData(payload)
	if err != nil {
		return nil, err
	}
	debugLog("plc5TypedRead %s: param=% X elemType=%d elemSize=%d data=%d bytes",
		addr.RawAddress, td.param, td.elemType, td.elemSize, len(td.data))
	if err := checkPLC5Type(addr, td, count); err != nil {
		return nil, err
	}
	td.data = append([]byte(nil), td.data...)
	if td.elemType == TypeIDFloat {
		plc5SwapFloatWords(td.data)
	}
	return td, nil
}

// plc5TypedWrite writes data (count elements, standard little-endian) to addr
// with Typed Write. The type/data parameter is taken from a Typed Read of the
// same address and count, as libplctag's PCCC typed write does (ab/
// eip_lgx_pccc.c pre-reads to capture encoded_type_info and sends it back
// unchanged): the manual requires the written type to match the file's type
// (p. 7-30), and echoing what the processor reported is the only way to know
// its exact descriptor for B, I/O, ST and structure sub-elements. The read
// also rejects a write whose size or type does not match the file.
func (p *PLC) plc5TypedWrite(addr *FileAddress, count int, data []byte) error {
	td, err := p.plc5TypedRead(addr, count)
	if err != nil {
		return fmt.Errorf("type pre-read: %w", err)
	}
	if len(data) != len(td.data) {
		return fmt.Errorf("write of %d bytes does not match the %d-byte %s data at this address", len(data), len(td.data), FileTypeName(addr.FileType))
	}
	out := append([]byte(nil), data...)
	if td.elemType == TypeIDFloat {
		plc5SwapFloatWords(out)
	}

	tns := p.nextTNS()
	cipReq, err := buildPLC5TypedWriteRequest(addr, count, td.param, out, tns, p.vendorID, p.serialNum)
	if err != nil {
		return err
	}
	cipResp, err := p.sendCipRequest(cipReq)
	if err != nil {
		return err
	}
	pcccResp, err := parseCipExecutePCCCResponse(cipResp)
	if err != nil {
		return err
	}
	return parsePCCCWriteResponse(pcccResp, tns)
}

// readModifyWrite changes one word with PLC-5 Read-Modify-Write (FNC 0x26).
// The processor applies the masks itself, so no other bit of the word is
// written back from a stale copy by this client.
func (p *PLC) readModifyWrite(addr *FileAddress, andMask, orMask uint16) error {
	if p == nil || p.Connection == nil {
		return fmt.Errorf("readModifyWrite: nil PLC or connection")
	}
	debugLog("readModifyWrite %s: file=%d elem=%d sub=%d and=%04X or=%04X",
		addr.RawAddress, addr.FileNumber, addr.Element, addr.SubElement, andMask, orMask)
	tns := p.nextTNS()
	cipReq, err := buildPLC5ReadModifyWriteRequest(addr, andMask, orMask, tns, p.vendorID, p.serialNum)
	if err != nil {
		return fmt.Errorf("WriteAddress %s: %w", addr.RawAddress, err)
	}
	cipResp, err := p.sendCipRequest(cipReq)
	if err != nil {
		return fmt.Errorf("WriteAddress %s: %w", addr.RawAddress, err)
	}
	pcccResp, err := parseCipExecutePCCCResponse(cipResp)
	if err != nil {
		return fmt.Errorf("WriteAddress %s: %w", addr.RawAddress, err)
	}
	if err := parsePCCCWriteResponse(pcccResp, tns); err != nil {
		return fmt.Errorf("WriteAddress %s: %w", addr.RawAddress, err)
	}
	return nil
}
