package pccc

import (
	"encoding/binary"
	"testing"
)

func TestLookupSys0Info(t *testing.T) {
	tests := []struct {
		prefix  string
		wantErr bool
		rowSize int
	}{
		{"1747", false, 10},
		{"1761", false, 8},
		{"1762", false, 10},
		{"1763", false, 10},
		{"1764", false, 10},
		{"1766", false, 10},
		{"9999", true, 0},
		{"", true, 0},
	}

	for _, tt := range tests {
		t.Run(tt.prefix, func(t *testing.T) {
			info, err := lookupSys0Info(tt.prefix)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error for prefix %q, got nil", tt.prefix)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for prefix %q: %v", tt.prefix, err)
			}
			if info.RowSize != tt.rowSize {
				t.Errorf("prefix %q: RowSize = %d, want %d", tt.prefix, info.RowSize, tt.rowSize)
			}
		})
	}
}

func TestExtractCatalogPrefix(t *testing.T) {
	tests := []struct {
		catalog string
		want    string
	}{
		{"1747-L552", "1747"},
		{"1762-L32BWA", "1762"},
		{"1766-L32BWAA", "1766"},
		{"ABC", "ABC"},      // Short string
		{"", ""},             // Empty
		{"1761-L16BWA", "1761"},
	}

	for _, tt := range tests {
		t.Run(tt.catalog, func(t *testing.T) {
			got := extractCatalogPrefix(tt.catalog)
			if got != tt.want {
				t.Errorf("extractCatalogPrefix(%q) = %q, want %q", tt.catalog, got, tt.want)
			}
		})
	}
}

func TestParseFileDirectory(t *testing.T) {
	// Build a test directory using SLC layout (RowSize=10, FileType offset=0x01, SizeElement offset=0x23 is too large for 10-byte row)
	// For SLC (1747): FileType=0x01, SizeElement=0x23, FilePosition=79, RowSize=10
	// But SizeElement=0x23=35 which is > RowSize=10, so we need to understand the actual layout.
	// Let's use a simpler scenario: SizeElement is at byte offset within the row.
	// Actually, looking at the code, for RowSize=10 and SizeElement=0x23, the offset is too large
	// so elemCount will be 0. Let's test with MicroLogix 1000 layout which has RowSize=8.

	// Use a custom Sys0Info for testing to verify the parsing logic
	sys0 := &Sys0Info{
		FileType:    0, // file type at byte 0 of each row
		SizeElement: 2, // element count at bytes 2-3 (16-bit LE)
		RowSize:     4, // 4 bytes per row for simplicity
		SizeConst:   0,
	}

	// Build 4 rows: file 0 = integer (50 elements), file 1 = placeholder, file 2 = float (10 elements), file 3 = timer (5 elements)
	data := make([]byte, 4*4)

	// Row 0: Integer file, 50 elements
	data[0] = FileTypeInteger
	data[1] = 0x00
	binary.LittleEndian.PutUint16(data[2:4], 50)

	// Row 1: Placeholder (deleted)
	data[4] = FileTypePlaceholder
	data[5] = 0x00
	binary.LittleEndian.PutUint16(data[6:8], 0)

	// Row 2: Float file, 10 elements
	data[8] = FileTypeFloat
	data[9] = 0x00
	binary.LittleEndian.PutUint16(data[10:12], 10)

	// Row 3: Timer file, 5 elements
	data[12] = FileTypeTimer
	data[13] = 0x00
	binary.LittleEndian.PutUint16(data[14:16], 5)

	entries, err := parseFileDirectory(data, sys0)
	if err != nil {
		t.Fatalf("parseFileDirectory: %v", err)
	}

	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	// Check entry 0: Integer at file 0
	if entries[0].FileNumber != 0 {
		t.Errorf("entry 0: FileNumber = %d, want 0", entries[0].FileNumber)
	}
	if entries[0].FileType != FileTypeInteger {
		t.Errorf("entry 0: FileType = 0x%02X, want 0x%02X", entries[0].FileType, FileTypeInteger)
	}
	if entries[0].TypePrefix != "N" {
		t.Errorf("entry 0: TypePrefix = %q, want %q", entries[0].TypePrefix, "N")
	}
	if entries[0].ElementCount != 50 {
		t.Errorf("entry 0: ElementCount = %d, want 50", entries[0].ElementCount)
	}

	// Check entry 1: Float at file 2 (file 1 was skipped)
	if entries[1].FileNumber != 2 {
		t.Errorf("entry 1: FileNumber = %d, want 2", entries[1].FileNumber)
	}
	if entries[1].FileType != FileTypeFloat {
		t.Errorf("entry 1: FileType = 0x%02X, want 0x%02X", entries[1].FileType, FileTypeFloat)
	}
	if entries[1].TypePrefix != "F" {
		t.Errorf("entry 1: TypePrefix = %q, want %q", entries[1].TypePrefix, "F")
	}
	if entries[1].ElementCount != 10 {
		t.Errorf("entry 1: ElementCount = %d, want 10", entries[1].ElementCount)
	}

	// Check entry 2: Timer at file 3
	if entries[2].FileNumber != 3 {
		t.Errorf("entry 2: FileNumber = %d, want 3", entries[2].FileNumber)
	}
	if entries[2].FileType != FileTypeTimer {
		t.Errorf("entry 2: FileType = 0x%02X, want 0x%02X", entries[2].FileType, FileTypeTimer)
	}
	if entries[2].TypePrefix != "T" {
		t.Errorf("entry 2: TypePrefix = %q, want %q", entries[2].TypePrefix, "T")
	}
	if entries[2].ElementCount != 5 {
		t.Errorf("entry 2: ElementCount = %d, want 5", entries[2].ElementCount)
	}
}

func TestParseFileDirectoryEmpty(t *testing.T) {
	sys0 := &Sys0Info{
		FileType:    0,
		SizeElement: 2,
		RowSize:     4,
	}

	entries, err := parseFileDirectory(nil, sys0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestParseFileDirectoryAllPlaceholders(t *testing.T) {
	sys0 := &Sys0Info{
		FileType:    0,
		SizeElement: 2,
		RowSize:     4,
	}

	data := make([]byte, 8)
	data[0] = FileTypePlaceholder
	data[4] = 0x00 // null type

	entries, err := parseFileDirectory(data, sys0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestFileTypePrefix(t *testing.T) {
	tests := []struct {
		fileType byte
		want     string
	}{
		{FileTypeOutput, "O"},
		{FileTypeInput, "I"},
		{FileTypeStatus, "S"},
		{FileTypeBinary, "B"},
		{FileTypeTimer, "T"},
		{FileTypeCounter, "C"},
		{FileTypeControl, "R"},
		{FileTypeInteger, "N"},
		{FileTypeFloat, "F"},
		{FileTypeString, "ST"},
		{FileTypeASCII, "A"},
		{FileTypeLong, "L"},
		{FileTypeMessage, "MG"},
		{FileTypePID, "PD"},
		{0x00, ""},  // Unknown
		{0xFF, ""},  // Unknown
	}

	for _, tt := range tests {
		got := FileTypePrefix(tt.fileType)
		if got != tt.want {
			t.Errorf("FileTypePrefix(0x%02X) = %q, want %q", tt.fileType, got, tt.want)
		}
	}
}

func TestExtractCatalog(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{"normal", []byte("1747-L552\x00"), "1747-L552"},
		{"with spaces", []byte("1747-L552 \x00"), "1747-L552"},
		{"all null", []byte{0, 0, 0, 0, 0}, ""},
		{"no null", []byte("1762-L32BW"), "1762-L32BW"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCatalog(tt.raw)
			if got != tt.want {
				t.Errorf("extractCatalog(%v) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
