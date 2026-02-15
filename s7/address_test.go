package s7

import "testing"

func TestParseAddress(t *testing.T) {
	tests := []struct {
		input    string
		wantErr  bool
		wantArea Area
		wantDB   int
		wantOff  int
		wantBit  int
		wantType uint16
	}{
		// DB addresses
		{"DB1.DBX0.0", false, AreaDB, 1, 0, 0, TypeBool},
		{"DB1.DBX0.7", false, AreaDB, 1, 0, 7, TypeBool},
		{"DB1.DBB0", false, AreaDB, 1, 0, -1, TypeByte},
		{"DB1.DBW2", false, AreaDB, 1, 2, -1, TypeWord},
		{"DB1.DBD4", false, AreaDB, 1, 4, -1, TypeDWord},
		{"DB100.DBW10", false, AreaDB, 100, 10, -1, TypeWord},
		{"db1.dbx0.0", false, AreaDB, 1, 0, 0, TypeBool}, // lowercase

		// M addresses
		{"M0.0", false, AreaM, 0, 0, 0, TypeBool},
		{"M0.7", false, AreaM, 0, 0, 7, TypeBool},
		{"MB0", false, AreaM, 0, 0, -1, TypeByte},
		{"MW2", false, AreaM, 0, 2, -1, TypeWord},
		{"MD4", false, AreaM, 0, 4, -1, TypeDWord},

		// I addresses (inputs)
		{"I0.0", false, AreaI, 0, 0, 0, TypeBool},
		{"IB0", false, AreaI, 0, 0, -1, TypeByte},
		{"IW0", false, AreaI, 0, 0, -1, TypeWord},
		{"ID0", false, AreaI, 0, 0, -1, TypeDWord},

		// Q addresses (outputs)
		{"Q0.0", false, AreaQ, 0, 0, 0, TypeBool},
		{"QB0", false, AreaQ, 0, 0, -1, TypeByte},
		{"QW0", false, AreaQ, 0, 0, -1, TypeWord},
		{"QD0", false, AreaQ, 0, 0, -1, TypeDWord},

		// Timers and counters
		{"T0", false, AreaT, 0, 0, -1, TypeWord},
		{"T100", false, AreaT, 0, 100, -1, TypeWord},
		{"C0", false, AreaC, 0, 0, -1, TypeWord},
		{"C50", false, AreaC, 0, 50, -1, TypeWord},

		// Invalid addresses
		{"", true, 0, 0, 0, 0, 0},
		{"invalid", true, 0, 0, 0, 0, 0},
		{"DB1.DBX0.8", true, 0, 0, 0, 0, 0}, // Bit > 7
		{"DB1.DBX0", true, 0, 0, 0, 0, 0},   // DBX without bit
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			addr, err := ParseAddress(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseAddress(%q) expected error, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Errorf("ParseAddress(%q) unexpected error: %v", tt.input, err)
				return
			}
			if addr.Area != tt.wantArea {
				t.Errorf("ParseAddress(%q) Area = %v, want %v", tt.input, addr.Area, tt.wantArea)
			}
			if addr.DBNumber != tt.wantDB {
				t.Errorf("ParseAddress(%q) DBNumber = %v, want %v", tt.input, addr.DBNumber, tt.wantDB)
			}
			if addr.Offset != tt.wantOff {
				t.Errorf("ParseAddress(%q) Offset = %v, want %v", tt.input, addr.Offset, tt.wantOff)
			}
			if addr.BitNum != tt.wantBit {
				t.Errorf("ParseAddress(%q) BitNum = %v, want %v", tt.input, addr.BitNum, tt.wantBit)
			}
			if addr.DataType != tt.wantType {
				t.Errorf("ParseAddress(%q) DataType = %v, want %v", tt.input, addr.DataType, tt.wantType)
			}
		})
	}
}

