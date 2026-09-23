package pccc

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

// These vectors pin the SLC 500 / MicroLogix wire format (CIP Execute PCCC
// wrapper + FNC 0xA2/0xAA/0xAB) as it was before PLC-5 typed commands were
// added, so shared-code changes cannot alter the SLC/MicroLogix bytes.
// Layout: 4B 02 20 67 24 01 | 07 <vendor LE> <serial LE> | 0F 00 <TNS LE>
// <FNC> <byte size> <file> <type> <element> <sub-element> [data]
// (DF1 manual 1770-6.5.16 ch. 7 "protected typed logical read/write with
// three address fields"; pycomm3 SLCDriver; libplctag slc_encode_address).

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const slcPinPrefix = "4B 02 20 67 24 01 07 01 00 78 56 34 12 0F 00 34 12 "

var slcReadPins = []struct{ addr, pccc string }{
	{"N7:0", "A2 02 07 89 00 00"},
	{"N7:254", "A2 02 07 89 FE 00"},
	{"N7:255", "A2 02 07 89 FF FF 00 00"},
	{"F8:1", "A2 04 08 8A 01 00"},
	{"T4:2", "A2 06 04 86 02 00"},
	{"T4:2.ACC", "A2 02 04 86 02 02"},
	{"C5:0.DN", "A2 02 05 87 00 00"},
	{"R6:1.LEN", "A2 02 06 88 01 01"},
	{"ST9:0", "A2 54 09 8D 00 00"},
	{"L10:3", "A2 04 0A 91 03 00"},
	{"B3:300/4", "A2 02 03 85 FF 2C 01 00"},
	{"I:010/15", "A2 02 01 83 0A 00"}, // decimal on SLC/MicroLogix: word 10
	{"O:0", "A2 02 00 82 00 00"},
	{"S:1/5", "A2 02 02 84 01 00"},
	{"N300:2", "A2 02 FF 2C 01 89 02 00"},
	{"A11:0", "A2 02 0B 8E 00 00"},
	{"MG12:0", "A2 32 0C 92 00 00"},
	{"PD13:0", "A2 2E 0D 93 00 00"},
}

func TestSLCWirePinnedRequests(t *testing.T) {
	for _, pin := range slcReadPins {
		for _, pt := range []PLCType{TypeSLC500, TypeMicroLogix} {
			addr, err := ParseAddressFor(pin.addr, pt)
			if err != nil {
				t.Fatalf("%s: %v", pin.addr, err)
			}
			got, err := buildReadRequest(addr, 0x1234, 1, 0x12345678)
			if err != nil {
				t.Fatal(err)
			}
			if want := unhex(t, slcPinPrefix+pin.pccc); !bytes.Equal(got, want) {
				t.Errorf("%s (%s): % X\nwant % X", pin.addr, pt, got, want)
			}
		}
	}

	addr, _ := ParseAddress("N7:5")
	got, _ := buildReadRequestN(addr, 20, 0x1234, 1, 0x12345678)
	if want := unhex(t, slcPinPrefix+"A2 14 07 89 05 00"); !bytes.Equal(got, want) {
		t.Errorf("read N: % X", got)
	}
	got, _ = buildWriteRequest(addr, []byte{0x2A, 0}, 0x1234, 1, 0x12345678)
	if want := unhex(t, slcPinPrefix+"AA 02 07 89 05 00 2A 00"); !bytes.Equal(got, want) {
		t.Errorf("write: % X", got)
	}
	addr, _ = ParseAddress("T4:2.DN")
	got, _ = buildMaskedWriteRequest(addr, []byte{0, 0x20}, []byte{0, 0x20}, 0x1234, 1, 0x12345678)
	if want := unhex(t, slcPinPrefix+"AB 02 04 86 02 00 00 20 00 20"); !bytes.Equal(got, want) {
		t.Errorf("masked write: % X", got)
	}
}

// End to end through Client for both SLC and MicroLogix: the PCCC bytes after
// the TNS for each operation.
func TestSLCWirePinnedClientOps(t *testing.T) {
	for _, pt := range []PLCType{TypeSLC500, TypeMicroLogix} {
		client, peer := startPCCCPeer(t, pt, func(cmd []byte) []byte {
			if cmd[4] == FncProtectedTypedLogicalRead {
				return pcccReply(cmd, 0, make([]byte, cmd[5])...)
			}
			return pcccReply(cmd, 0)
		})
		ops := []struct {
			write bool
			addr  string
			value interface{}
			want  string
		}{
			{false, "N7:0", nil, "A2 02 07 89 00 00"},
			{false, "T4:2", nil, "A2 06 04 86 02 00"},
			{false, "ST9:1", nil, "A2 54 09 8D 01 00"},
			{true, "N7:5", 42, "AA 02 07 89 05 00 2A 00"},
			{true, "F8:1", float32(1.5), "AA 04 08 8A 01 00 00 00 C0 3F"},
			{true, "T4:2.PRE", 100, "AA 02 04 86 02 01 64 00"},
			{true, "L10:3", int32(-2), "AA 04 0A 91 03 00 FE FF FF FF"},
			{true, "B3:1/4", true, "AB 02 03 85 01 00 10 00 10 00"},
			{true, "T4:2.EN", false, "AB 02 04 86 02 00 00 80 00 00"},
			{true, "ST9:0", "AB", "AA 54 09 8D 00 00 02 00 42 41" + strings.Repeat(" 00", 80)},
		}
		for _, op := range ops {
			var err error
			if op.write {
				err = client.Write(op.addr, op.value)
			} else {
				var vals []*TagValue
				vals, err = client.Read(op.addr)
				if err == nil && vals[0].Error != nil {
					err = vals[0].Error
				}
			}
			if err != nil {
				t.Fatalf("%s %s: %v", pt, op.addr, err)
			}
			log := peer.log()
			last := log[len(log)-1]
			if want := unhex(t, "0F 00"); !bytes.Equal(last[:2], want) {
				t.Errorf("%s %s: header % X", pt, op.addr, last[:4])
			}
			if want := unhex(t, op.want); !bytes.Equal(last[4:], want) {
				t.Errorf("%s %s: % X\nwant % X", pt, op.addr, last[4:], want)
			}
		}
	}
}
