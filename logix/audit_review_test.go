package logix

import "testing"

// Rockwell 1756-PM020I-EN-P, p.55: the first member UINT is the BOOL
// bit location, rather than a sequential position to infer from member order.
func TestAuditBOOLUsesPublishedBitLocation(t *testing.T) {
	definition := []byte{
		0, 0, 0xc2, 0, 0, 0, 0, 0, // host SINT at byte zero
		3, 0, 0xc1, 0, 0, 0, 0, 0, // visible BOOL at bit three
	}
	definition = append(definition, []byte("Flags;n\x00ZZZZZZZZZZFlags0\x00Enabled\x00")...)
	tmpl := &Template{ID: 1, Size: 1, RawHandle: 0x1234, MemberMap: make(map[string]int)}
	if err := tmpl.parseDefinition(definition, 2); err != nil {
		t.Fatal(err)
	}
	client := &Client{plc: &PLC{}, templates: map[uint16]*Template{1: tmpl}}
	value, err := client.DecodeUDT(0x8001, []byte{0x34, 0x12, 0x08})
	if err != nil {
		t.Fatal(err)
	}
	if value["Enabled"] != true {
		t.Errorf("published bit 3 is set, decoded Enabled=%v; inferred BitOffset=%d", value["Enabled"], tmpl.Members[1].BitOffset)
	}
}
