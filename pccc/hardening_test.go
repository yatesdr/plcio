package pccc

import (
	"bytes"
	"math"
	"testing"
)

// The FNC 0xA2/0xAA/0xAB Byte Size field is one byte with no FF escape
// (1770-6.5.16 p. 7-17/7-18; pycomm3 USINT, libplctag uint8_t). A size of
// 255+ used to be compact-encoded as FF lo hi, which the processor would parse
// as size 255, file "lo", type "hi": a request aimed at another file.
func TestSLCByteSizeIsOneByte(t *testing.T) {
	addr, _ := ParseAddress("N7:0")
	got, err := buildReadRequestN(addr, 255, 0x1234, 1, 0x12345678)
	if err != nil || !bytes.Equal(got, unhex(t, slcPinPrefix+"A2 FF 07 89 00 00")) {
		t.Fatalf("255: % X %v", got, err)
	}
	for _, n := range []int{0, 256, 1000} {
		if _, err := buildReadRequestN(addr, n, 1, 1, 1); err == nil {
			t.Errorf("read byte size %d accepted", n)
		}
	}
	if _, err := buildWriteRequest(addr, make([]byte, 256), 1, 1, 1); err == nil {
		t.Error("256-byte write accepted")
	}
	if _, err := buildMaskedWriteRequest(addr, make([]byte, 256), make([]byte, 256), 1, 1, 1); err == nil {
		t.Error("256-byte masked write accepted")
	}

	client, peer := startPCCCPeer(t, TypeSLC500, func(cmd []byte) []byte { return pcccReply(cmd, 0) })
	if _, err := client.PLC().ReadAddressN(addr, 128); err == nil {
		t.Error("ReadAddressN of 256 bytes accepted")
	}
	if err := client.PLC().WriteAddress(addr, make([]byte, 300)); err == nil {
		t.Error("300-byte WriteAddress accepted")
	}
	if n := len(peer.log()); n != 0 {
		t.Fatalf("%d malformed requests reached the wire", n)
	}
}

// A reply with more data than requested is not the answer to this request
// (libplctag pccc_check_read_status: "Too much data received").
func TestSLCLongReplyIsError(t *testing.T) {
	client, _ := startPCCCPeer(t, TypeSLC500, func(cmd []byte) []byte {
		return pcccReply(cmd, 0, make([]byte, int(cmd[5])+2)...)
	})
	vals, _ := client.Read("N7:0", "T4:0", "ST9:0")
	for _, v := range vals {
		if v.Error == nil {
			t.Errorf("%s: over-long reply decoded as %#v", v.Name, v.Value)
		}
	}
	addr, _ := ParseAddress("N7:0")
	if _, err := client.PLC().ReadAddressN(addr, 3); err == nil {
		t.Fatal("ReadAddressN accepted an over-long reply")
	}
}

// The requester ID in an Execute PCCC reply is echoed as sent (7 bytes). A
// zero length byte used to hand back the payload with that byte still in
// front, shifting CMD/STS/TNS/data by one.
func TestExecutePCCCRequesterIDLength(t *testing.T) {
	good := unhex(t, "CB 00 00 00 07 01 00 78 56 34 12 4F 00 01 00 2A 00")
	if pccc, err := parseCipExecutePCCCResponse(good); err != nil || !bytes.Equal(pccc, unhex(t, "4F 00 01 00 2A 00")) {
		t.Fatalf("good reply: % X %v", pccc, err)
	}
	for _, bad := range []string{
		"CB 00 00 00 00 01 00 78 56 34 12 4F 00 01 00",
		"CB 00 00 00 03 01 00 78 56 34 12 4F 00 01 00",
		"CB 00 00 00 FF 01 00 78 56 34 12 4F 00 01 00",
	} {
		if pccc, err := parseCipExecutePCCCResponse(unhex(t, bad)); err == nil {
			t.Errorf("%s accepted: % X", bad, pccc)
		}
	}
}

func TestEncodeFloat32NoSilentLoss(t *testing.T) {
	addr, _ := ParseAddress("F8:0")
	ok := []struct {
		v    interface{}
		want float32
	}{
		{float32(1.5), 1.5},
		{3.25, 3.25},
		{16777216, 16777216},
		{int16(-7), -7},
		{math.Inf(-1), float32(math.Inf(-1))},
		{math.MaxFloat32, math.MaxFloat32},
	}
	for _, tc := range ok {
		b, err := encodeValue(addr, tc.v)
		if err != nil || math.Float32frombits(uint32(b[0])|uint32(b[1])<<8|uint32(b[2])<<16|uint32(b[3])<<24) != tc.want {
			t.Errorf("%v: % X %v", tc.v, b, err)
		}
	}
	for _, v := range []interface{}{1e39, -1e39, 16777217, int64(1)<<40 + 1, "1.0"} {
		if b, err := encodeValue(addr, v); err == nil {
			t.Errorf("%v encoded as % X, want error", v, b)
		}
	}
}

func TestOddRoutePathRejected(t *testing.T) {
	client, peer := startPCCCPeer(t, TypeSLC500, func(cmd []byte) []byte { return pcccReply(cmd, 0, 0, 0) })
	client.plc.RoutePath = []byte{0x01, 0x00, 0x12}
	vals, _ := client.Read("N7:0")
	if vals[0].Error == nil {
		t.Fatal("odd route path accepted")
	}
	if len(peer.log()) != 0 {
		t.Fatal("request sent")
	}
}
