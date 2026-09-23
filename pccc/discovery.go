package pccc

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// Sys0Info describes how to read and parse the file directory held in system
// file 0 for a processor family. The fields and values follow pycomm3's
// SLCDriver (_get_sys0_info, _get_file_directory_size,
// _read_whole_file_directory and _parse_file0); the pycomm3 key is given for
// each field.
type Sys0Info struct {
	FileType     byte // File type code used to address system file 0 in FNC 0xA1 reads ("file_type")
	SizeElement  byte // Element (word) number in file 0 that holds the directory size in bytes ("size_element")
	FilePosition int  // Byte offset in file 0 of the first data-file row ("file_position")
	RowSize      int  // Size of each directory row in bytes ("row_size")
	SizeConst    int  // Constant subtracted from the raw size value (MicroLogix 1100+ only) ("size_const")
	SizeLen      byte // Byte count requested when reading the directory size ("size_len")
}

// ErrDiscoveryNotSupported is returned by GetFileDirectory for processors
// whose file directory layout is not established by a reference, rather than
// guessing and returning wrong data.
var ErrDiscoveryNotSupported = errors.New("pccc: file directory discovery not supported for this processor")

// FileDirectoryEntry describes a single data file discovered from the file directory.
type FileDirectoryEntry struct {
	FileNumber   int    // Data file number (e.g., 7 for N7)
	FileType     byte   // PCCC file type code (e.g., 0x89 for Integer)
	FileTypeName string // Human-readable type name (e.g., "Integer")
	TypePrefix   string // Address prefix letter (e.g., "N")
	ElementCount int    // Number of elements in the file
}

// FileTypePlaceholder marks a deleted or unused slot in the file directory.
const FileTypePlaceholder byte = 0x81

// GetProcessorType sends a Diagnostic Status command (CMD 0x06) and returns
// the processor catalog string (e.g., "1747-L552").
func (p *PLC) GetProcessorType() (string, error) {
	catalog, err := p.getProcessorTypeFromDiagnosticStatus()
	if err == nil {
		return catalog, nil
	}

	fallbackCatalog, fallbackErr := p.getProcessorTypeFromIdentity()
	if fallbackErr == nil {
		debugLog("GetProcessorType: diagnostic probe failed (%v), using ListIdentity catalog %q", err, fallbackCatalog)
		return fallbackCatalog, nil
	}

	return "", err
}

func (p *PLC) getProcessorTypeFromDiagnosticStatus() (string, error) {
	if p == nil || p.Connection == nil {
		return "", fmt.Errorf("nil PLC or connection")
	}

	tns := p.nextTNS()

	// Diagnostic Status is CMD 0x06, FNC 0x03 (DF1 manual 1770-6.5.16;
	// pycomm3 get_processor_type): [CMD] [STS] [TNS lo] [TNS hi] [FNC]
	pcccCmd := buildPCCCHeader(CmdDiagnosticStatus, tns, FncDiagnosticStatus)

	cipReq, err := wrapInCipExecutePCCC(pcccCmd, p.vendorID, p.serialNum)
	if err != nil {
		return "", err
	}

	cipResp, err := p.sendCipRequest(cipReq)
	if err != nil {
		return "", err
	}

	pcccResp, err := parseCipExecutePCCCResponse(cipResp)
	if err != nil {
		return "", err
	}

	// Response: [CMD 0x46] [STS] [TNS lo] [TNS hi] [data...]
	if len(pcccResp) < 4 {
		return "", fmt.Errorf("response too short: %d bytes", len(pcccResp))
	}

	cmd := pcccResp[0]
	sts := pcccResp[1]

	if cmd != CmdDiagnosticReply {
		return "", fmt.Errorf("unexpected reply command 0x%02X", cmd)
	}
	if err := checkReplyTNS(pcccResp, tns); err != nil {
		return "", err
	}
	if sts != StsSuccess {
		var extSts byte
		if sts&0xF0 == 0xF0 && len(pcccResp) >= 5 {
			extSts = pcccResp[4]
		}
		return "", PCCCStatusError(sts, extSts)
	}

	// The catalog number is 11 ASCII bytes at offset 5 of the reply data
	// (after the 4-byte header): mode/status, type extender, extended
	// interface type, extended processor type, series/revision, then the
	// catalog number. pycomm3 get_processor_type reads the same [5:16].
	data := pcccResp[4:]
	if len(data) < 16 {
		return "", fmt.Errorf("diagnostic data too short: %d bytes", len(data))
	}

	catalog := extractCatalog(data[5:16])
	if catalog == "" {
		return "", fmt.Errorf("diagnostic status reply has no catalog number")
	}
	debugLog("GetProcessorType: catalog=%q", catalog)
	return catalog, nil
}

func (p *PLC) getProcessorTypeFromIdentity() (string, error) {
	if p == nil || p.Connection == nil {
		return "", fmt.Errorf("nil PLC or connection")
	}

	identities, err := p.Connection.ListIdentityTCP()
	if err != nil {
		return "", err
	}
	if len(identities) == 0 {
		return "", fmt.Errorf("no identity response")
	}

	catalog := extractCatalogFromIdentityProductName(identities[0].ProductName)
	if catalog == "" {
		return "", fmt.Errorf("unable to extract catalog from product name %q", identities[0].ProductName)
	}

	return catalog, nil
}

// extractCatalog extracts a catalog string from a fixed-width byte field,
// trimming null bytes and trailing spaces.
func extractCatalog(raw []byte) string {
	// Find the end of the string (null terminator or end of slice)
	end := len(raw)
	for i, b := range raw {
		if b == 0 {
			end = i
			break
		}
	}
	return strings.TrimRight(string(raw[:end]), " ")
}

func extractCatalogFromIdentityProductName(productName string) string {
	productName = strings.TrimSpace(productName)
	if productName == "" {
		return ""
	}

	firstField := productName
	if idx := strings.IndexAny(productName, " \t"); idx >= 0 {
		firstField = productName[:idx]
	}
	if idx := strings.IndexByte(firstField, '/'); idx >= 0 {
		firstField = firstField[:idx]
	}

	return strings.TrimSpace(firstField)
}

// extractCatalogPrefix returns the first 4 characters of a catalog string,
// which identify the processor family (e.g., "1747", "1762").
func extractCatalogPrefix(catalog string) string {
	if len(catalog) < 4 {
		return catalog
	}
	return catalog[:4]
}

// readSection reads size bytes of system/data file fileNum starting at word
// element using Protected Typed Logical Read with 2 address fields
// (CMD 0x0F, FNC 0xA1):
//
//	[CMD] [STS] [TNS:2 LE] [FNC=0xA1] [ByteSize] [FileNumber] [FileType] [Element]
//
// There is no sub-element field in the 2-address-field form; this matches the
// requests pycomm3 builds in _get_file_directory_size and
// _read_whole_file_directory ("function code, from RSLinx capture").
func (p *PLC) readSection(fileNum uint16, fileType byte, element uint16, size uint16) ([]byte, error) {
	tns := p.nextTNS()

	pcccCmd := buildPCCCHeader(CmdTypedCommand, tns, FncReadSection)
	pcccCmd = appendCompactValue(pcccCmd, size)
	pcccCmd = appendCompactValue(pcccCmd, fileNum)
	pcccCmd = append(pcccCmd, fileType)
	pcccCmd = appendCompactValue(pcccCmd, element)

	cipReq, err := wrapInCipExecutePCCC(pcccCmd, p.vendorID, p.serialNum)
	if err != nil {
		return nil, fmt.Errorf("readSection: %w", err)
	}

	cipResp, err := p.sendCipRequest(cipReq)
	if err != nil {
		return nil, fmt.Errorf("readSection file %d element %d: %w", fileNum, element, err)
	}

	pcccResp, err := parseCipExecutePCCCResponse(cipResp)
	if err != nil {
		return nil, fmt.Errorf("readSection file %d element %d: %w", fileNum, element, err)
	}

	data, err := parsePCCCReadResponse(pcccResp, tns)
	if err != nil {
		return nil, fmt.Errorf("readSection file %d element %d: %w", fileNum, element, err)
	}

	return data, nil
}

// fileDirectoryChunk is the byte count per FNC 0xA1 read of file 0 (pycomm3
// reads 0x50 bytes at a time).
const fileDirectoryChunk = 0x50

// GetFileDirectory discovers all data files by reading the file directory
// (system file 0). It works for SLC 5/03, 5/04, 5/05 and MicroLogix 1100 and
// 1400; other processors return ErrDiscoveryNotSupported (PLC-5 has no such
// directory).
//
// The procedure follows pycomm3's SLCDriver.get_file_directory: read the
// directory size from word SizeElement of file 0 (addressed with file type
// FileType), read all of file 0 from element 0 in 0x50-byte chunks (element =
// words already read), then parse the rows starting at FilePosition.
func (p *PLC) GetFileDirectory() ([]FileDirectoryEntry, error) {
	// Step 1: Get processor type
	catalog, err := p.GetProcessorType()
	if err != nil {
		return nil, fmt.Errorf("GetFileDirectory: %w", err)
	}

	// Step 2: Lookup sys0 layout for this processor
	sys0, err := sys0InfoForCatalog(catalog)
	if err != nil {
		return nil, fmt.Errorf("GetFileDirectory: %w", err)
	}

	debugLog("GetFileDirectory: catalog=%q sys0=%+v", catalog, *sys0)

	// Step 3: Read the size in bytes of file 0.
	sizeData, err := p.readSection(0, sys0.FileType, uint16(sys0.SizeElement), uint16(sys0.SizeLen))
	if err != nil {
		return nil, fmt.Errorf("GetFileDirectory: read directory size: %w", err)
	}
	if len(sizeData) < 2 {
		return nil, fmt.Errorf("GetFileDirectory: directory size response too short")
	}
	totalSize := int(binary.LittleEndian.Uint16(sizeData[:2])) - sys0.SizeConst
	if totalSize <= sys0.FilePosition {
		return nil, fmt.Errorf("GetFileDirectory: directory size %d too small", totalSize)
	}

	debugLog("GetFileDirectory: totalSize=%d filePosition=%d", totalSize, sys0.FilePosition)

	// Step 4: Read all of file 0. The element field is a word offset.
	dirData := make([]byte, 0, totalSize)
	for len(dirData) < totalSize {
		if len(dirData)%2 != 0 {
			return nil, fmt.Errorf("GetFileDirectory: odd-length reply at offset %d", len(dirData)-1)
		}
		chunk := totalSize - len(dirData)
		if chunk > fileDirectoryChunk {
			chunk = fileDirectoryChunk
		}
		data, err := p.readSection(0, sys0.FileType, uint16(len(dirData)/2), uint16(chunk))
		if err != nil {
			return nil, fmt.Errorf("GetFileDirectory: read offset %d: %w", len(dirData), err)
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("GetFileDirectory: empty reply at offset %d", len(dirData))
		}
		if len(data) > chunk {
			data = data[:chunk]
		}
		dirData = append(dirData, data...)
	}

	// Step 5: Parse the directory entries
	entries, err := parseFileDirectory(dirData, sys0)
	if err != nil {
		return nil, fmt.Errorf("GetFileDirectory: %w", err)
	}

	debugLog("GetFileDirectory: found %d data files", len(entries))
	return entries, nil
}

// sys0InfoForCatalog returns the file directory layout for a full catalog
// number. SLC 500 (1747) support is limited to the Ethernet-capable modular
// processors (5/03 L53x, 5/04 L54x, 5/05 L55x); fixed and 5/01, 5/02
// processors share the 1747 prefix but not the layout.
func sys0InfoForCatalog(catalog string) (*Sys0Info, error) {
	prefix := extractCatalogPrefix(catalog)
	if prefix == "1747" {
		model := strings.ToUpper(catalog)
		if !strings.HasPrefix(model, "1747-L53") && !strings.HasPrefix(model, "1747-L54") && !strings.HasPrefix(model, "1747-L55") {
			return nil, fmt.Errorf("%w: %q", ErrDiscoveryNotSupported, catalog)
		}
	}
	return lookupSys0Info(prefix)
}

// lookupSys0Info returns the file directory layout for the given catalog
// prefix. Values are pycomm3's _get_sys0_info. Only layouts pycomm3 reports
// as taken from real processors are enabled: SLC 5/05 (its default branch)
// and MicroLogix 1100 / 1400 ("values from 1100 and 1400"). pycomm3 marks
// MicroLogix 1000 ("Not sure if these are correct, never tested") and
// 1200 / 1500 ("not tested on 1200/1500") as unverified, so those return
// ErrDiscoveryNotSupported rather than risk wrong data.
func lookupSys0Info(prefix string) (*Sys0Info, error) {
	switch prefix {
	case "1747": // SLC 5/03, 5/04, 5/05
		return &Sys0Info{
			FileType:     0x01,
			SizeElement:  0x23,
			FilePosition: 79,
			RowSize:      10,
			SizeConst:    0,
			SizeLen:      0x04,
		}, nil
	case "1763": // MicroLogix 1100
		return &Sys0Info{
			FileType:     0x02,
			SizeElement:  0x28,
			FilePosition: 233,
			RowSize:      10,
			SizeConst:    19968,
			SizeLen:      0x08,
		}, nil
	case "1766": // MicroLogix 1400
		return &Sys0Info{
			FileType:     0x03,
			SizeElement:  0x2b,
			FilePosition: 233,
			RowSize:      10,
			SizeConst:    19968,
			SizeLen:      0x08,
		}, nil
	case "1761", "1762", "1764": // MicroLogix 1000, 1200, 1500: layout unverified
		return nil, fmt.Errorf("%w: catalog prefix %q (layout unverified)", ErrDiscoveryNotSupported, prefix)
	default:
		return nil, fmt.Errorf("%w: unknown processor catalog prefix %q", ErrDiscoveryNotSupported, prefix)
	}
}

// fileTypePLS is the programmable limit switch file type code. It is not
// addressable through this package, but it occupies a file number.
const fileTypePLS byte = 0x94

// parseFileDirectory walks the rows of file 0 (the whole file, as read from
// element 0) and extracts the data file entries, following pycomm3's
// _parse_file0: rows start at FilePosition and are RowSize bytes apart; byte 0
// of a row is the file type code and bytes 1-2 are the file size in bytes
// (little-endian). The element count is size / element size. File numbers
// start at 0 and advance for each recognised data file type and for 0x81
// placeholder rows (skipped file numbers); other rows do not use a number.
func parseFileDirectory(data []byte, sys0 *Sys0Info) ([]FileDirectoryEntry, error) {
	if sys0 == nil || sys0.RowSize <= 0 || sys0.FilePosition < 0 {
		return nil, fmt.Errorf("invalid file directory layout")
	}

	var entries []FileDirectoryEntry

	fileNumber := 0
	for pos := sys0.FilePosition; pos+3 <= len(data); pos += sys0.RowSize {
		ft := data[pos]
		prefix := FileTypePrefix(ft)

		if prefix != "" {
			size := int(binary.LittleEndian.Uint16(data[pos+1 : pos+3]))
			entries = append(entries, FileDirectoryEntry{
				FileNumber:   fileNumber,
				FileType:     ft,
				FileTypeName: FileTypeName(ft),
				TypePrefix:   prefix,
				ElementCount: size / ElementSize(ft),
			})
		}

		if prefix != "" || ft == fileTypePLS || ft == FileTypePlaceholder {
			fileNumber++
		}
	}

	return entries, nil
}

// DiscoverDataFiles reads the file directory from the PLC and returns
// the list of data files. This is the high-level Client method.
func (c *Client) DiscoverDataFiles() ([]FileDirectoryEntry, error) {
	if c == nil || c.plc == nil {
		return nil, fmt.Errorf("DiscoverDataFiles: nil client")
	}
	return c.plc.GetFileDirectory()
}
