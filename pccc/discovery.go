package pccc

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// Sys0Info describes the binary layout of the file directory (system file 0)
// for a specific processor family. Different processors store the directory
// in different formats.
type Sys0Info struct {
	FileType     byte // Offset within a row for the file type byte
	SizeElement  byte // Offset within a row for the element size/count byte
	FilePosition int  // Byte offset where file directory entries begin
	RowSize      int  // Size of each directory entry row in bytes
	SizeConst    int  // Constant subtracted from the raw size value (MicroLogix 1100+ only)
}

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
	if p == nil || p.Connection == nil {
		return "", fmt.Errorf("GetProcessorType: nil PLC or connection")
	}

	tns := p.nextTNS()

	// CMD 0x06 has no FNC byte — the header is just [CMD] [STS] [TNS lo] [TNS hi]
	pcccCmd := make([]byte, 0, 4)
	pcccCmd = append(pcccCmd, CmdDiagnosticStatus)
	pcccCmd = append(pcccCmd, 0x00) // STS = 0 in request
	pcccCmd = binary.LittleEndian.AppendUint16(pcccCmd, tns)

	cipReq, err := wrapInCipExecutePCCC(pcccCmd, p.vendorID, p.serialNum)
	if err != nil {
		return "", fmt.Errorf("GetProcessorType: %w", err)
	}

	cipResp, err := p.sendCipRequest(cipReq)
	if err != nil {
		return "", fmt.Errorf("GetProcessorType: %w", err)
	}

	pcccResp, err := parseCipExecutePCCCResponse(cipResp)
	if err != nil {
		return "", fmt.Errorf("GetProcessorType: %w", err)
	}

	// Response: [CMD 0x46] [STS] [TNS lo] [TNS hi] [data...]
	if len(pcccResp) < 4 {
		return "", fmt.Errorf("GetProcessorType: response too short: %d bytes", len(pcccResp))
	}

	cmd := pcccResp[0]
	sts := pcccResp[1]

	if cmd != CmdDiagnosticReply {
		return "", fmt.Errorf("GetProcessorType: unexpected reply command 0x%02X", cmd)
	}
	if sts != StsSuccess {
		return "", PCCCStatusError(sts, 0)
	}

	// The catalog string is in the data portion after the 4-byte header.
	// It's typically a null-terminated ASCII string starting at a known offset.
	// For SLC/MicroLogix, the catalog string is at bytes 12-21 (0-indexed from data start).
	data := pcccResp[4:]
	if len(data) < 22 {
		return "", fmt.Errorf("GetProcessorType: diagnostic data too short: %d bytes", len(data))
	}

	// Extract catalog: starts at byte 12, up to 10 chars, null/space terminated
	catalog := extractCatalog(data[12:22])
	debugLog("GetProcessorType: catalog=%q", catalog)
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

// extractCatalogPrefix returns the first 4 characters of a catalog string,
// which identify the processor family (e.g., "1747", "1762").
func extractCatalogPrefix(catalog string) string {
	if len(catalog) < 4 {
		return catalog
	}
	return catalog[:4]
}

// readSection reads a chunk of data from a data file using the
// Protected Typed Logical Read (CMD 0x0F, FNC 0xA1) command.
// This is used to read the system file directory (file 0).
func (p *PLC) readSection(fileNum uint16, fileType byte, offset uint16, size uint16) ([]byte, error) {
	tns := p.nextTNS()

	pcccCmd := buildPCCCHeader(CmdTypedCommand, tns, FncReadSection)
	pcccCmd = appendCompactValue(pcccCmd, size)
	pcccCmd = appendCompactValue(pcccCmd, fileNum)
	pcccCmd = append(pcccCmd, fileType)
	pcccCmd = appendCompactValue(pcccCmd, offset)
	pcccCmd = appendCompactValue(pcccCmd, 0) // sub-element

	cipReq, err := wrapInCipExecutePCCC(pcccCmd, p.vendorID, p.serialNum)
	if err != nil {
		return nil, fmt.Errorf("readSection: %w", err)
	}

	cipResp, err := p.sendCipRequest(cipReq)
	if err != nil {
		return nil, fmt.Errorf("readSection file %d offset %d: %w", fileNum, offset, err)
	}

	pcccResp, err := parseCipExecutePCCCResponse(cipResp)
	if err != nil {
		return nil, fmt.Errorf("readSection file %d offset %d: %w", fileNum, offset, err)
	}

	data, err := parsePCCCReadResponse(pcccResp)
	if err != nil {
		return nil, fmt.Errorf("readSection file %d offset %d: %w", fileNum, offset, err)
	}

	return data, nil
}

// GetFileDirectory discovers all data files by reading the file directory (system file 0).
// This works for SLC 500 and MicroLogix processors (not PLC-5).
func (p *PLC) GetFileDirectory() ([]FileDirectoryEntry, error) {
	// Step 1: Get processor type
	catalog, err := p.GetProcessorType()
	if err != nil {
		return nil, fmt.Errorf("GetFileDirectory: %w", err)
	}

	// Step 2: Lookup sys0 layout for this processor
	prefix := extractCatalogPrefix(catalog)
	sys0, err := lookupSys0Info(prefix)
	if err != nil {
		return nil, fmt.Errorf("GetFileDirectory: %w", err)
	}

	debugLog("GetFileDirectory: catalog=%q prefix=%q sys0=%+v", catalog, prefix, *sys0)

	// Step 3: Read the size of the file directory from the system file header.
	// The first 2 bytes at offset 0 of sys file 0 give the total size in bytes.
	sizeData, err := p.readSection(0, FileTypeStatus, 0, 2)
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

	// Step 4: Read the file directory data in chunks
	dirSize := totalSize - sys0.FilePosition
	const maxChunk = 80
	dirData := make([]byte, 0, dirSize)

	for offset := 0; offset < dirSize; offset += maxChunk {
		chunk := maxChunk
		if offset+chunk > dirSize {
			chunk = dirSize - offset
		}
		data, err := p.readSection(0, FileTypeStatus, uint16(sys0.FilePosition+offset), uint16(chunk))
		if err != nil {
			return nil, fmt.Errorf("GetFileDirectory: read offset %d: %w", offset, err)
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

// lookupSys0Info returns the file directory layout for the given catalog prefix.
func lookupSys0Info(prefix string) (*Sys0Info, error) {
	switch prefix {
	case "1747": // SLC 5/03, 5/04, 5/05
		return &Sys0Info{
			FileType:     0x01,
			SizeElement:  0x23,
			FilePosition: 79,
			RowSize:      10,
			SizeConst:    0,
		}, nil
	case "1761": // MicroLogix 1000
		return &Sys0Info{
			FileType:     0x00,
			SizeElement:  0x23,
			FilePosition: 93,
			RowSize:      8,
			SizeConst:    0,
		}, nil
	case "1762", "1763", "1764": // MicroLogix 1100, 1200, 1500
		return &Sys0Info{
			FileType:     0x02,
			SizeElement:  0x28,
			FilePosition: 233,
			RowSize:      10,
			SizeConst:    19968,
		}, nil
	case "1766": // MicroLogix 1400
		return &Sys0Info{
			FileType:     0x03,
			SizeElement:  0x2b,
			FilePosition: 233,
			RowSize:      10,
			SizeConst:    19968,
		}, nil
	default:
		return nil, fmt.Errorf("unknown processor catalog prefix %q", prefix)
	}
}

// parseFileDirectory walks the raw file directory data and extracts data file entries.
func parseFileDirectory(data []byte, sys0 *Sys0Info) ([]FileDirectoryEntry, error) {
	var entries []FileDirectoryEntry

	fileNumber := 0
	for offset := 0; offset+sys0.RowSize <= len(data); offset += sys0.RowSize {
		row := data[offset : offset+sys0.RowSize]

		// Extract file type from the row at the configured offset
		if int(sys0.FileType) >= len(row) {
			fileNumber++
			continue
		}
		ft := row[sys0.FileType]

		// Skip placeholder/deleted files
		if ft == FileTypePlaceholder || ft == 0x00 {
			fileNumber++
			continue
		}

		// Extract element count from the row at the configured offset
		var elemCount int
		if int(sys0.SizeElement) < len(row) {
			elemCount = int(row[sys0.SizeElement])
		} else if int(sys0.SizeElement) < len(row)+1 {
			// For some layouts, the size is a 16-bit value
			elemCount = int(row[sys0.SizeElement])
		}

		// Handle 16-bit element count for larger layouts
		sizeOffset := int(sys0.SizeElement)
		if sizeOffset+1 < len(row) {
			elemCount = int(binary.LittleEndian.Uint16(row[sizeOffset : sizeOffset+2]))
		} else if sizeOffset < len(row) {
			elemCount = int(row[sizeOffset])
		}

		prefix := FileTypePrefix(ft)
		entries = append(entries, FileDirectoryEntry{
			FileNumber:   fileNumber,
			FileType:     ft,
			FileTypeName: FileTypeName(ft),
			TypePrefix:   prefix,
			ElementCount: elemCount,
		})

		fileNumber++
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
