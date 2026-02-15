package ads

import (
	"testing"
)

func TestParseAmsNetId(t *testing.T) {
	tests := []struct {
		input   string
		want    AmsNetId
		wantErr bool
	}{
		{"192.168.1.100.1.1", AmsNetId{192, 168, 1, 100, 1, 1}, false},
		{"10.0.0.1.1.1", AmsNetId{10, 0, 0, 1, 1, 1}, false},
		{"0.0.0.0.0.0", AmsNetId{0, 0, 0, 0, 0, 0}, false},
		{"255.255.255.255.255.255", AmsNetId{255, 255, 255, 255, 255, 255}, false},
		{"192.168.1.100", AmsNetId{}, true},      // Too few parts
		{"192.168.1.100.1.1.1", AmsNetId{}, true}, // Too many parts
		{"", AmsNetId{}, true},                    // Empty
		{"abc.def.ghi.jkl.mno.pqr", AmsNetId{}, true}, // Non-numeric
		{"256.0.0.0.0.0", AmsNetId{}, true},       // Out of range
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseAmsNetId(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseAmsNetId(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ParseAmsNetId(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestAmsNetIdFromIP(t *testing.T) {
	tests := []struct {
		input   string
		want    AmsNetId
		wantErr bool
	}{
		{"192.168.1.100", AmsNetId{192, 168, 1, 100, 1, 1}, false},
		{"192.168.1.100:48898", AmsNetId{192, 168, 1, 100, 1, 1}, false}, // With port
		{"10.0.0.1", AmsNetId{10, 0, 0, 1, 1, 1}, false},
		{"invalid", AmsNetId{}, true},
		{"", AmsNetId{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := AmsNetIdFromIP(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("AmsNetIdFromIP(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("AmsNetIdFromIP(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestAmsNetIdString(t *testing.T) {
	netId := AmsNetId{192, 168, 1, 100, 1, 1}
	want := "192.168.1.100.1.1"
	got := netId.String()
	if got != want {
		t.Errorf("AmsNetId.String() = %q, want %q", got, want)
	}
}

func TestTypeCodeFromName(t *testing.T) {
	tests := []struct {
		name     string
		wantCode uint16
		wantOk   bool
	}{
		{"BOOL", TypeBool, true},
		{"BYTE", TypeByte, true},
		{"USINT", TypeByte, true},
		{"SINT", TypeSByte, true},
		{"WORD", TypeWord, true},
		{"UINT", TypeWord, true},
		{"INT", TypeInt16, true},
		{"DWORD", TypeDWord, true},
		{"UDINT", TypeDWord, true},
		{"DINT", TypeInt32, true},
		{"LWORD", TypeLWord, true},
		{"ULINT", TypeLWord, true},
		{"LINT", TypeInt64, true},
		{"REAL", TypeReal, true},
		{"LREAL", TypeLReal, true},
		{"STRING", TypeString, true},
		{"UNKNOWN", TypeUnknown, false},
		{"", TypeUnknown, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCode, gotOk := TypeCodeFromName(tt.name)
			if gotCode != tt.wantCode || gotOk != tt.wantOk {
				t.Errorf("TypeCodeFromName(%q) = (%d, %v), want (%d, %v)",
					tt.name, gotCode, gotOk, tt.wantCode, tt.wantOk)
			}
		})
	}
}

func TestTypeName(t *testing.T) {
	tests := []struct {
		code uint16
		want string
	}{
		{TypeBool, "BOOL"},
		{TypeByte, "BYTE"},
		{TypeSByte, "SINT"},
		{TypeWord, "WORD"},
		{TypeInt16, "INT"},
		{TypeDWord, "DWORD"},
		{TypeInt32, "DINT"},
		{TypeLWord, "LWORD"},
		{TypeInt64, "LINT"},
		{TypeReal, "REAL"},
		{TypeLReal, "LREAL"},
		{TypeString, "STRING"},
		{0x99, "TYPE_0099"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := TypeName(tt.code)
			if got != tt.want {
				t.Errorf("TypeName(%d) = %q, want %q", tt.code, got, tt.want)
			}
		})
	}
}

