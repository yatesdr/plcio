package pccc

import "testing"

func TestTypeNameRoundTrip(t *testing.T) {
	codes := []uint16{
		uint16(FileTypeOutput), uint16(FileTypeInput), uint16(FileTypeStatus),
		uint16(FileTypeBinary), uint16(FileTypeTimer), uint16(FileTypeCounter),
		uint16(FileTypeControl), uint16(FileTypeInteger), uint16(FileTypeFloat),
		uint16(FileTypeString), uint16(FileTypeASCII), uint16(FileTypeLong),
		uint16(FileTypeMessage), uint16(FileTypePID),
	}
	for _, code := range codes {
		name := TypeName(code)
		if name == "UNKNOWN" {
			t.Errorf("TypeName(0x%02X) returned UNKNOWN", code)
			continue
		}
		got, ok := TypeCodeFromName(name)
		if !ok {
			t.Errorf("TypeCodeFromName(%q) returned ok=false", name)
			continue
		}
		if got != code {
			t.Errorf("round-trip failed: code 0x%02X -> name %q -> code 0x%02X", code, name, got)
		}
	}
}

func TestTypeCodeFromNameCaseInsensitive(t *testing.T) {
	cases := []string{"int", "Int", "INT", "float", "Float", "FLOAT", "timer", "Timer"}
	for _, name := range cases {
		code, ok := TypeCodeFromName(name)
		if !ok {
			t.Errorf("TypeCodeFromName(%q) returned ok=false", name)
			continue
		}
		upper := TypeName(code)
		code2, _ := TypeCodeFromName(upper)
		if code != code2 {
			t.Errorf("case mismatch: %q -> 0x%02X, %q -> 0x%02X", name, code, upper, code2)
		}
	}
}

func TestTypeCodeFromNameUnknown(t *testing.T) {
	code, ok := TypeCodeFromName("BOGUS")
	if ok {
		t.Errorf("expected ok=false for unknown name, got code=0x%02X", code)
	}
	if code != 0 {
		t.Errorf("expected code=0 for unknown name, got 0x%02X", code)
	}
}

func TestTypeNameUnknown(t *testing.T) {
	name := TypeName(0xFFFF)
	if name != "UNKNOWN" {
		t.Errorf("expected UNKNOWN for invalid code, got %q", name)
	}
}

func TestSupportedTypeNames(t *testing.T) {
	names := SupportedTypeNames()
	if len(names) != 8 {
		t.Errorf("expected 8 supported type names, got %d", len(names))
	}
	// Verify each supported name round-trips
	for _, name := range names {
		if _, ok := TypeCodeFromName(name); !ok {
			t.Errorf("SupportedTypeNames contains %q which TypeCodeFromName doesn't recognize", name)
		}
	}
}

func TestTypeSizeDelegatesToElementSize(t *testing.T) {
	if TypeSize(uint16(FileTypeInteger)) != ElementSizeInteger {
		t.Error("TypeSize(Integer) mismatch")
	}
	if TypeSize(uint16(FileTypeFloat)) != ElementSizeFloat {
		t.Error("TypeSize(Float) mismatch")
	}
	if TypeSize(uint16(FileTypeTimer)) != ElementSizeTimer {
		t.Error("TypeSize(Timer) mismatch")
	}
}

func TestTypeIntegerConstant(t *testing.T) {
	if TypeInteger != uint16(FileTypeInteger) {
		t.Errorf("TypeInteger = 0x%04X, want 0x%04X", TypeInteger, uint16(FileTypeInteger))
	}
}
