package pccc

import "testing"

func TestParseAddress(t *testing.T) {
	tests := []struct {
		addr       string
		fileType   byte
		fileNum    uint16
		element    uint16
		subElem    uint16
		bitNum     int
		typeLetter string
		readSize   int
		wantErr    bool
	}{
		// Integer files
		{"N7:0", FileTypeInteger, 7, 0, 0, -1, "N", 2, false},
		{"N7:255", FileTypeInteger, 7, 255, 0, -1, "N", 2, false},
		{"N10:42", FileTypeInteger, 10, 42, 0, -1, "N", 2, false},

		// Float files
		{"F8:0", FileTypeFloat, 8, 0, 0, -1, "F", 4, false},
		{"F8:5", FileTypeFloat, 8, 5, 0, -1, "F", 4, false},

		// Binary files with bit access
		{"B3:0/5", FileTypeBinary, 3, 0, 0, 5, "B", 2, false},
		{"B3:0/0", FileTypeBinary, 3, 0, 0, 0, "B", 2, false},
		{"B3:0/15", FileTypeBinary, 3, 0, 0, 15, "B", 2, false},

		// Default file number types
		{"O:0", FileTypeOutput, 0, 0, 0, -1, "O", 2, false},
		{"O:0/3", FileTypeOutput, 0, 0, 0, 3, "O", 2, false},
		{"I:0", FileTypeInput, 1, 0, 0, -1, "I", 2, false},
		{"I:0/3", FileTypeInput, 1, 0, 0, 3, "I", 2, false},
		{"S:1", FileTypeStatus, 2, 1, 0, -1, "S", 2, false},
		{"S:1/5", FileTypeStatus, 2, 1, 0, 5, "S", 2, false},

		// Timer sub-elements
		{"T4:0", FileTypeTimer, 4, 0, 0, -1, "T", 6, false},
		{"T4:0.PRE", FileTypeTimer, 4, 0, 1, -1, "T", 2, false},
		{"T4:0.ACC", FileTypeTimer, 4, 0, 2, -1, "T", 2, false},
		{"T4:0.DN", FileTypeTimer, 4, 0, 0, 13, "T", 2, false},
		{"T4:0.EN", FileTypeTimer, 4, 0, 0, 15, "T", 2, false},
		{"T4:0.TT", FileTypeTimer, 4, 0, 0, 14, "T", 2, false},

		// Counter sub-elements
		{"C5:2.PRE", FileTypeCounter, 5, 2, 1, -1, "C", 2, false},
		{"C5:2.ACC", FileTypeCounter, 5, 2, 2, -1, "C", 2, false},
		{"C5:2.DN", FileTypeCounter, 5, 2, 0, 13, "C", 2, false},
		{"C5:2.CU", FileTypeCounter, 5, 2, 0, 15, "C", 2, false},
		{"C5:2.CD", FileTypeCounter, 5, 2, 0, 14, "C", 2, false},
		{"C5:2.OV", FileTypeCounter, 5, 2, 0, 12, "C", 2, false},
		{"C5:2.UN", FileTypeCounter, 5, 2, 0, 11, "C", 2, false},

		// Control sub-elements
		{"R6:0.LEN", FileTypeControl, 6, 0, 1, -1, "R", 2, false},
		{"R6:0.POS", FileTypeControl, 6, 0, 2, -1, "R", 2, false},
		{"R6:0.DN", FileTypeControl, 6, 0, 0, 13, "R", 2, false},
		{"R6:0.EN", FileTypeControl, 6, 0, 0, 15, "R", 2, false},

		// Long integer
		{"L10:0", FileTypeLong, 10, 0, 0, -1, "L", 4, false},

		// String
		{"ST9:0", FileTypeString, 9, 0, 0, -1, "ST", 84, false},

		// ASCII
		{"A9:0", FileTypeASCII, 9, 0, 0, -1, "A", 2, false},

		// Message (MicroLogix)
		{"MG10:0", FileTypeMessage, 10, 0, 0, -1, "MG", 50, false},

		// PID
		{"PD11:0", FileTypePID, 11, 0, 0, -1, "PD", 46, false},

		// Error cases
		{"", 0, 0, 0, 0, 0, "", 0, true},       // Empty
		{"X7:0", 0, 0, 0, 0, 0, "", 0, true},   // Unknown type
		{"N:0", 0, 0, 0, 0, 0, "", 0, true},     // Missing file number for N
		{"N7", 0, 0, 0, 0, 0, "", 0, true},      // Missing colon
		{"N7:", 0, 0, 0, 0, 0, "", 0, true},      // Missing element
		{"B3:0/16", 0, 0, 0, 0, 0, "", 0, true}, // Bit out of range
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			addr, err := ParseAddress(tt.addr)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseAddress(%q) expected error, got nil", tt.addr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseAddress(%q) unexpected error: %v", tt.addr, err)
			}
			if addr.FileType != tt.fileType {
				t.Errorf("FileType = 0x%02X, want 0x%02X", addr.FileType, tt.fileType)
			}
			if addr.FileNumber != tt.fileNum {
				t.Errorf("FileNumber = %d, want %d", addr.FileNumber, tt.fileNum)
			}
			if addr.Element != tt.element {
				t.Errorf("Element = %d, want %d", addr.Element, tt.element)
			}
			if addr.SubElement != tt.subElem {
				t.Errorf("SubElement = %d, want %d", addr.SubElement, tt.subElem)
			}
			if addr.BitNumber != tt.bitNum {
				t.Errorf("BitNumber = %d, want %d", addr.BitNumber, tt.bitNum)
			}
			if addr.TypeLetter != tt.typeLetter {
				t.Errorf("TypeLetter = %q, want %q", addr.TypeLetter, tt.typeLetter)
			}
			if addr.ReadSize() != tt.readSize {
				t.Errorf("ReadSize() = %d, want %d", addr.ReadSize(), tt.readSize)
			}
		})
	}
}

func TestParseAddressRoundTrip(t *testing.T) {
	// Verify that RawAddress is preserved
	addrs := []string{"N7:0", "F8:5", "B3:0/5", "T4:0.ACC", "S:1/5", "O:0/3", "ST9:0"}
	for _, addr := range addrs {
		parsed, err := ParseAddress(addr)
		if err != nil {
			t.Fatalf("ParseAddress(%q) failed: %v", addr, err)
		}
		if parsed.RawAddress != addr {
			t.Errorf("RawAddress = %q, want %q", parsed.RawAddress, addr)
		}
	}
}

func TestCompactValueEncoding(t *testing.T) {
	// Test the compact encoding helper
	tests := []struct {
		value    uint16
		expected []byte
	}{
		{0, []byte{0x00}},
		{1, []byte{0x01}},
		{254, []byte{0xFE}},
		{255, []byte{0xFF, 0xFF, 0x00}},
		{256, []byte{0xFF, 0x00, 0x01}},
		{1000, []byte{0xFF, 0xE8, 0x03}},
	}

	for _, tt := range tests {
		result := appendCompactValue(nil, tt.value)
		if len(result) != len(tt.expected) {
			t.Errorf("appendCompactValue(%d) len = %d, want %d", tt.value, len(result), len(tt.expected))
			continue
		}
		for i := range result {
			if result[i] != tt.expected[i] {
				t.Errorf("appendCompactValue(%d) byte[%d] = 0x%02X, want 0x%02X", tt.value, i, result[i], tt.expected[i])
			}
		}
	}
}
