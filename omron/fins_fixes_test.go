package omron

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// --- Item 1: multi-word values are stored low word first -------------------

func TestFINSWordOrderDecode(t *testing.T) {
	// REAL 1.0 in D100/D101: D100=0x0000, D101=0x3F80.
	if got := DecodeValue(TypeReal, []byte{0x00, 0x00, 0x3F, 0x80}, true); got != float32(1.0) {
		t.Fatalf("REAL = %v, want 1", got)
	}
	// DINT 0x12345678: D100=0x5678, D101=0x1234.
	if got := DecodeValue(TypeInt32, []byte{0x56, 0x78, 0x12, 0x34}, true); got != int32(0x12345678) {
		t.Fatalf("DINT = %#x", got)
	}
	if got := DecodeValue(TypeDWord, []byte{0x56, 0x78, 0x12, 0x34}, true); got != uint32(0x12345678) {
		t.Fatalf("DWORD = %#x", got)
	}
	// 64-bit: words from lowest address are least significant.
	b64 := []byte{0xCD, 0xEF, 0x89, 0xAB, 0x45, 0x67, 0x01, 0x23}
	if got := DecodeValue(TypeLWord, b64, true); got != uint64(0x0123456789ABCDEF) {
		t.Fatalf("LWORD = %#x", got)
	}
	if got := DecodeValue(TypeInt64, b64, true); got != int64(0x0123456789ABCDEF) {
		t.Fatalf("LINT = %#x", got)
	}
	// LREAL 1.0 = 0x3FF0000000000000 -> words 0000 0000 0000 3FF0.
	if got := DecodeValue(TypeLReal, []byte{0, 0, 0, 0, 0, 0, 0x3F, 0xF0}, true); got != float64(1.0) {
		t.Fatalf("LREAL = %v", got)
	}
	// 16-bit values are unchanged (big-endian word).
	if got := DecodeValue(TypeInt16, []byte{0xFF, 0xFE}, true); got != int16(-2) {
		t.Fatalf("INT = %v", got)
	}
	// CIP (little-endian) paths are unaffected.
	if got := DecodeValue(TypeCIPREAL, []byte{0x00, 0x00, 0x80, 0x3F}, false); got != float32(1.0) {
		t.Fatalf("CIP REAL = %v", got)
	}
	if got := DecodeValue(TypeCIPDINT, []byte{0x78, 0x56, 0x34, 0x12}, false); got != int32(0x12345678) {
		t.Fatalf("CIP DINT = %#x", got)
	}
	// Arrays decode each element in FINS word order.
	tv := &TagValue{DataType: MakeArrayType(TypeReal), Count: 2, bigEndian: true,
		Bytes: []byte{0, 0, 0x3F, 0x80, 0, 0, 0xC0, 0x00}}
	if got, ok := tv.GoValue().([]float32); !ok || got[0] != 1 || got[1] != -2 {
		t.Fatalf("REAL[2] = %v", tv.GoValue())
	}
}

func TestFINSWordOrderEncode(t *testing.T) {
	cases := []struct {
		value any
		code  uint16
		want  []byte
	}{
		{1.0, TypeReal, []byte{0x00, 0x00, 0x3F, 0x80}},
		{int64(0x12345678), TypeInt32, []byte{0x56, 0x78, 0x12, 0x34}},
		{uint32(0x12345678), TypeDWord, []byte{0x56, 0x78, 0x12, 0x34}},
		{int64(0x0123456789ABCDEF), TypeInt64, []byte{0xCD, 0xEF, 0x89, 0xAB, 0x45, 0x67, 0x01, 0x23}},
		{uint64(0x0123456789ABCDEF), TypeLWord, []byte{0xCD, 0xEF, 0x89, 0xAB, 0x45, 0x67, 0x01, 0x23}},
		{1.0, TypeLReal, []byte{0, 0, 0, 0, 0, 0, 0x3F, 0xF0}},
		{int64(-2), TypeInt16, []byte{0xFF, 0xFE}},
	}
	for _, tc := range cases {
		got, err := EncodeValue(tc.value, tc.code, true)
		if err != nil || !bytes.Equal(got, tc.want) {
			t.Errorf("EncodeValue(%v, %s) = %x %v, want %x", tc.value, TypeName(tc.code), got, err, tc.want)
		}
	}
	// CIP little-endian encoding unchanged.
	if got, _ := EncodeValue(1.0, TypeCIPREAL, false); !bytes.Equal(got, []byte{0, 0, 0x80, 0x3F}) {
		t.Fatalf("CIP REAL %x", got)
	}
}

func TestFINSWordOrderEndToEnd(t *testing.T) {
	f := newFakeFINS()
	f.set(AreaDMWord, 100, 0x0000, 0x3F80) // REAL 1.0
	f.set(AreaDMWord, 110, 0x5678, 0x1234) // DINT 0x12345678
	c := connectFINSTCPTest(t, f)

	vals, err := c.ReadWithTypes([]TagRequest{{Address: "D100", TypeHint: "REAL"}, {Address: "D110", TypeHint: "DINT"}})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := vals[0].Float(); v != 1.0 {
		t.Fatalf("D100 REAL = %v", vals[0].GoValue())
	}
	if v, _ := vals[1].Int(); v != 0x12345678 {
		t.Fatalf("D110 DINT = %#x", vals[1].GoValue())
	}

	if err := c.WriteWithType("D200", int64(0x12345678), "DINT"); err != nil {
		t.Fatal(err)
	}
	if lo, hi := f.get(AreaDMWord, 200), f.get(AreaDMWord, 201); lo != 0x5678 || hi != 0x1234 {
		t.Fatalf("D200/D201 = %#04x/%#04x, want 0x5678/0x1234", lo, hi)
	}
	if err := c.WriteWithType("D300", 1.0, "REAL"); err != nil {
		t.Fatal(err)
	}
	if lo, hi := f.get(AreaDMWord, 300), f.get(AreaDMWord, 301); lo != 0x0000 || hi != 0x3F80 {
		t.Fatalf("D300/D301 = %#04x/%#04x, want 0x0000/0x3F80", lo, hi)
	}
}

// --- Item 2: end-code status flags ------------------------------------------

func TestFINSEndCodeMasking(t *testing.T) {
	for _, code := range []uint16{0x0000, 0x0040, 0x0080, 0x00C0} {
		if err := FINSEndCodeError(code); err != nil {
			t.Errorf("end code 0x%04X: %v, want success", code, err)
		}
	}
	// W342 5-1-3: bit 15 = network relay error, always a failure.
	if err := FINSEndCodeError(0x8000); err == nil {
		t.Error("end code 0x8000 (relay error flag) reported success")
	}
	err := FINSEndCodeError(0x1103 | 0x0040)
	var fe *FINSEndError
	if !errors.As(err, &fe) || fe.Code != 0x1103 || fe.EndCode != 0x1143 || fe.RelayError {
		t.Fatalf("masked error %#v", err)
	}
	if !strings.Contains(err.Error(), "FINS error 0x1103") || !strings.Contains(err.Error(), "address range error") {
		t.Fatalf("message %q", err)
	}
	err = FINSEndCodeError(0x8000 | 0x0205)
	if !errors.As(err, &fe) || !fe.RelayError || fe.Code != 0x0205 || !strings.Contains(err.Error(), "relay") {
		t.Fatalf("relay error %v", err)
	}
	if relay, fatal, nonFatal := FINSEndCodeFlags(0x80C0); !relay || !fatal || !nonFatal {
		t.Fatal("flags not reported")
	}
}

func TestFINSNonFatalCPUFlagDoesNotFailReads(t *testing.T) {
	f := newFakeFINS()
	f.flags = 0x0040 // e.g. CJ2 battery warning
	f.set(AreaDMWord, 0, 0x1234)
	c := connectFINSTCPTest(t, f)
	vals, err := c.ReadWithTypes([]TagRequest{{Address: "D0", TypeHint: "WORD"}})
	if err != nil || vals[0].Error != nil {
		t.Fatalf("read with non-fatal flag failed: %v %v", err, vals[0].Error)
	}
	if v, _ := vals[0].Uint(); v != 0x1234 {
		t.Fatalf("value %#x", v)
	}
	if err := c.Write("D1", 7); err != nil {
		t.Fatalf("write with non-fatal flag failed: %v", err)
	}

	// Same over UDP.
	uf := newFakeFINS()
	uf.flags = 0x0080
	uf.set(AreaDMWord, 5, 42)
	port := startFINSUDP(t, uf)
	uc, err := Connect("127.0.0.1", WithTransport(TransportFINSUDP), WithPort(port), WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer uc.Close()
	vals, err = uc.Read("D5")
	if err != nil || vals[0].Error != nil {
		t.Fatalf("UDP read with fatal-CPU flag failed: %v %v", err, vals[0].Error)
	}
}

// --- Item 3: Multiple Memory Area Read (0x0104) -----------------------------

func TestMultiMemoryReadRequestFormat(t *testing.T) {
	groups := []readGroup{
		{area: AreaDMWord, startAddr: 0x0102, wordCount: 1},
		{area: AreaCIOWord, startAddr: 0x0010, wordCount: 1},
	}
	want := []byte{0x82, 0x01, 0x02, 0x00, 0xB0, 0x00, 0x10, 0x00} // no leading count
	if got := BuildMultiMemoryReadRequest(groups); !bytes.Equal(got, want) {
		t.Fatalf("request %x, want %x", got, want)
	}
}

func TestMultiMemoryReadResponseValidation(t *testing.T) {
	mk := func() ([]readGroup, []*TagValue) {
		p1, _ := ParseAddress("D1")
		p2, _ := ParseAddress("CIO2")
		return []readGroup{
			{area: AreaDMWord, startAddr: 1, wordCount: 1, requests: []finsReadRequest{{originalIndex: 0, address: "D1", parsed: p1, wordCount: 1}}},
			{area: AreaCIOWord, startAddr: 2, wordCount: 1, requests: []finsReadRequest{{originalIndex: 1, address: "CIO2", parsed: p2, wordCount: 1}}},
		}, make([]*TagValue, 2)
	}
	groups, results := mk()
	if err := ParseMultiMemoryReadResponse([]byte{0x82, 0x12, 0x34, 0xB0, 0xAB, 0xCD}, groups, results); err != nil {
		t.Fatal(err)
	}
	if v, _ := results[0].Uint(); v != 0x1234 {
		t.Fatalf("D1 %#x", v)
	}
	if v, _ := results[1].Uint(); v != 0xABCD {
		t.Fatalf("CIO2 %#x", v)
	}
	bad := [][]byte{
		{0x82, 0x12, 0x34, 0xB1, 0xAB, 0xCD},       // wrong echoed area
		{0x12, 0x34, 0xAB, 0xCD},                   // old (area-less) layout
		{0x82, 0x12, 0x34, 0xB0, 0xAB, 0xCD, 0x00}, // trailing garbage
	}
	for _, resp := range bad {
		groups, results := mk()
		if err := ParseMultiMemoryReadResponse(resp, groups, results); err == nil {
			t.Errorf("response %x accepted", resp)
		}
		if results[0] != nil || results[1] != nil {
			t.Errorf("response %x wrote partial results", resp)
		}
	}
}

func TestMultiMemoryReadEndToEnd(t *testing.T) {
	f := newFakeFINS()
	addrs := []string{"D10", "D20", "D30", "D40", "CIO5"}
	f.set(AreaDMWord, 10, 1)
	f.set(AreaDMWord, 20, 2)
	f.set(AreaDMWord, 30, 3)
	f.set(AreaDMWord, 40, 4)
	f.set(AreaCIOWord, 5, 5)
	c := connectFINSTCPTest(t, f)
	vals, err := c.Read(addrs...)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range vals {
		if n, _ := v.Uint(); v.Error != nil || int(n) != i+1 {
			t.Fatalf("%s = %v %v", addrs[i], v.GoValue(), v.Error)
		}
	}
	multi := f.commands(FINSCmdMultiMemoryRead)
	if len(multi) != 1 || len(multi[0].Data) != 5*4 || len(f.commands(FINSCmdMemoryRead)) != 0 {
		t.Fatalf("expected one 0x0104 with 5 items, got %d (data %x) and %d single reads",
			len(multi), multi, len(f.commands(FINSCmdMemoryRead)))
	}

	// A misaligned response falls back to single reads with correct values.
	f2 := newFakeFINS()
	f2.set(AreaDMWord, 10, 1)
	f2.set(AreaDMWord, 20, 2)
	f2.set(AreaDMWord, 30, 3)
	f2.set(AreaDMWord, 40, 4)
	f2.set(AreaCIOWord, 5, 5)
	f2.setTCPHook(func(req finsReq) []byte {
		if req.Command != FINSCmdMultiMemoryRead {
			return nil
		}
		// Old, area-less layout: 2 bytes per item.
		return finsTCPFrame(cmdFINSFrameSend, 0, finsResponseFrame(req, 0, []byte{0, 1, 0, 2, 0, 3, 0, 4, 0, 5}))
	})
	c2 := connectFINSTCPTest(t, f2)
	vals, err = c2.Read(addrs...)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range vals {
		if n, _ := v.Uint(); v.Error != nil || int(n) != i+1 {
			t.Fatalf("fallback %s = %v %v", addrs[i], v.GoValue(), v.Error)
		}
	}
	if len(f2.commands(FINSCmdMemoryRead)) != 5 {
		t.Fatalf("expected fallback single reads")
	}
}

// --- Item 4: writes bounded by the declared extent --------------------------

func TestWriteFINSRejectsOversizedWrites(t *testing.T) {
	f := newFakeFINS()
	c := connectFINSTCPTest(t, f)

	if err := c.Write("D100[5]", []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}); err == nil {
		t.Fatal("10 elements written to D100[5]")
	}
	if err := c.Write("D100", []int{1, 2}); err == nil {
		t.Fatal("2 elements written to scalar D100")
	}
	if err := c.WriteWithType("D100[20]", strings.Repeat("x", 50), "STRING"); err == nil {
		t.Fatal("50-char string written to 20-byte STRING")
	}
	if err := c.WriteWithType("D100", int64(1), "DINT"); err != nil {
		t.Fatal(err)
	}
	if err := c.Write("D100.0[2]", []bool{true, false, true}); err == nil {
		t.Fatal("3 bits written to D100.0[2]")
	}
	if n := len(f.commands(FINSCmdMemoryWrite)); n != 1 {
		t.Fatalf("expected only the valid DINT write on the wire, got %d writes", n)
	}

	// In-bounds writes still work, including a string that exactly fills its
	// buffer (terminator omitted) and a partial array write.
	if err := c.Write("D200[5]", []int{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteWithType("D300[4]", "abcd", "STRING"); err != nil {
		t.Fatal(err)
	}
	if f.get(AreaDMWord, 300) != 0x6162 || f.get(AreaDMWord, 301) != 0x6364 || f.get(AreaDMWord, 302) != 0 {
		t.Fatal("exact-fit string not written as expected")
	}
	if err := c.WriteWithType("D400[10]", "hi", "STRING"); err != nil {
		t.Fatal(err)
	}
	vals, _ := c.ReadWithTypes([]TagRequest{{Address: "D400[10]", TypeHint: "STRING"}})
	if vals[0].String() != "hi" {
		t.Fatalf("string round trip %q", vals[0].String())
	}
}

// --- Item 5: C / T prefixes, TIM / CNT --------------------------------------

func TestAddressPrefixAmbiguity(t *testing.T) {
	for _, addr := range []string{"C100", "c100", "C100.1", "T5", "T5[2]", "t5.0"} {
		_, err := ParseAddress(addr)
		if err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Errorf("%s: %v, want ambiguity error", addr, err)
		}
	}
	if _, ok := AreaFromName("C"); ok {
		t.Error("AreaFromName(C) still maps")
	}
	if _, ok := AreaFromName("T"); ok {
		t.Error("AreaFromName(T) still maps")
	}
	cases := []struct {
		addr string
		area byte
		word uint16
	}{
		{"CIO100", AreaCIOWord, 100},
		{"TK1", AreaTaskBit, 1},
		{"TIM5", AreaTimerCounterPV, 5},
		{"TIM4095", AreaTimerCounterPV, 4095},
		{"CNT5", AreaTimerCounterPV, 0x8005},
		{"CNT0[3]", AreaTimerCounterPV, 0x8000},
		{"D100", AreaDMWord, 100},
	}
	for _, tc := range cases {
		p, err := ParseAddress(tc.addr)
		if err != nil || p.MemoryArea != tc.area || p.Address != tc.word {
			t.Errorf("%s = %+v %v, want area 0x%02X addr 0x%04X", tc.addr, p, err, tc.area, tc.word)
		}
	}
	for _, addr := range []string{"TIM4096", "CNT4095[2]", "TIM1.0", "CNT1.2"} {
		if _, err := ParseAddress(addr); err == nil {
			t.Errorf("%s accepted", addr)
		}
	}
	// On the wire: CNT7 reads area 0x89 address 0x8007.
	f := newFakeFINS()
	f.set(AreaTimerCounterPV, 0x8007, 0x0042)
	c := connectFINSTCPTest(t, f)
	vals, err := c.Read("CNT7")
	if err != nil || vals[0].Error != nil {
		t.Fatal(err, vals[0].Error)
	}
	if v, _ := vals[0].Uint(); v != 0x42 {
		t.Fatalf("CNT7 = %#x", v)
	}
	if r := f.commands(FINSCmdMemoryRead); len(r) != 1 || !bytes.Equal(r[0].Data, []byte{0x89, 0x80, 0x07, 0, 0, 1}) {
		t.Fatalf("CNT7 request %x", r)
	}
}

// --- Item 7: FINS/TCP framing errors break the connection -------------------

func TestFINSTCPFramingErrorsMarkDisconnected(t *testing.T) {
	good := func(req finsReq) []byte {
		return finsTCPFrame(cmdFINSFrameSend, 0, finsResponseFrame(req, 0, []byte{0, 1}))
	}
	cases := map[string]func(req finsReq) []byte{
		"huge length": func(req finsReq) []byte {
			b := good(req)
			binary.BigEndian.PutUint32(b[4:8], 0xFFFFFFF0)
			return b
		},
		"bad magic": func(req finsReq) []byte {
			b := good(req)
			copy(b, "FIXS")
			return b
		},
		"unexpected command": func(req finsReq) []byte {
			b := good(req)
			binary.BigEndian.PutUint32(b[8:12], 7)
			return b
		},
		"short length": func(req finsReq) []byte { return finsTCPFrame(cmdFINSFrameSend, 0, nil)[:16] },
		"frame error": func(req finsReq) []byte {
			return finsTCPFrame(cmdFINSFrameSendError, 1, nil)
		},
		"truncated FINS": func(req finsReq) []byte {
			return finsTCPFrame(cmdFINSFrameSend, 0, []byte{0xC0, 0, 2})
		},
		"wrong SID": func(req finsReq) []byte {
			req.Header.SID++
			return finsTCPFrame(cmdFINSFrameSend, 0, finsResponseFrame(req, 0, []byte{0, 1}))
		},
	}
	for name, hook := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeFINS()
			f.setTCPHook(hook)
			c := connectFINSTCPTest(t, f)
			vals, err := c.Read("D0")
			if !errors.Is(err, ErrConnectionLost) {
				t.Fatalf("err = %v, want ErrConnectionLost", err)
			}
			if len(vals) != 1 || vals[0].Error == nil {
				t.Fatalf("values %v", vals)
			}
			if c.IsConnected() {
				t.Fatal("still connected after framing error")
			}
		})
	}
}

// --- Item 8: FINS/UDP response validation -----------------------------------

func TestFINSUDPDiscardsMismatchedDatagrams(t *testing.T) {
	f := newFakeFINS()
	f.set(AreaDMWord, 3, 0x0BEE)
	port := startFINSUDP(t, f)
	c, err := Connect("127.0.0.1", WithTransport(TransportFINSUDP), WithPort(port), WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	f.setUDPHook(func(req finsReq) [][]byte {
		if req.Command != FINSCmdMemoryRead {
			return nil
		}
		code, data := f.handle(req)
		valid := finsResponseFrame(req, code, data)
		staleSID := req
		staleSID.Header.SID--
		otherCmd := req
		otherCmd.Command = FINSCmdMemoryWrite
		notResp := append([]byte(nil), valid...)
		notResp[0] = 0x80
		otherNode := append([]byte(nil), valid...)
		otherNode[4]++
		return [][]byte{
			{0x01, 0x02, 0x03}, // garbage
			finsResponseFrame(staleSID, 0, []byte{0xDE, 0xAD}),
			finsResponseFrame(otherCmd, 0, []byte{0xDE, 0xAD}),
			notResp,
			otherNode,
			valid,
		}
	})
	vals, err := c.Read("D3")
	if err != nil || vals[0].Error != nil {
		t.Fatalf("%v %v", err, vals[0].Error)
	}
	if v, _ := vals[0].Uint(); v != 0x0BEE {
		t.Fatalf("accepted wrong datagram: %#x", v)
	}

	// Only garbage until the deadline: an error, never garbage as data.
	f.setUDPHook(func(req finsReq) [][]byte {
		stale := req
		stale.Header.SID += 7
		return [][]byte{finsResponseFrame(stale, 0, []byte{0xDE, 0xAD})}
	})
	vals, _ = c.Read("D3")
	if vals[0].Error == nil {
		t.Fatalf("mismatched-only response accepted: %v", vals[0].GoValue())
	}
}

// --- Item 9: FINS/UDP connect verifies reachability -------------------------

// silentUDP binds a UDP port that never answers.
func silentUDP(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn.LocalAddr().(*net.UDPAddr).Port
}

func TestFINSUDPConnectFailsWithoutResponder(t *testing.T) {
	port := silentUDP(t)
	start := time.Now()
	c, err := Connect("127.0.0.1", WithTransport(TransportFINSUDP), WithPort(port), WithTimeout(200*time.Millisecond))
	if err == nil {
		c.Close()
		t.Fatal("UDP connect succeeded with no FINS responder")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("verification did not honour the timeout")
	}

	// Auto mode: TCP refused, UDP silent -> Connect must fail.
	c, err = Connect("127.0.0.1", WithTransport(TransportFINS), WithPort(port), WithTimeout(200*time.Millisecond))
	if err == nil {
		c.Close()
		t.Fatal("auto FINS connect succeeded against a non-existent PLC")
	}

	// A responder that answers 0x0501 is accepted and the probe is 0x0501.
	f := newFakeFINS()
	up := startFINSUDP(t, f)
	c, err = Connect("127.0.0.1", WithTransport(TransportFINSUDP), WithPort(up), WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if r := f.requests(); len(r) != 1 || r[0].Command != FINSCmdCPURead {
		t.Fatalf("probe requests %+v", r)
	}

	// A destination-node error means the node was not reached.
	f2 := newFakeFINS()
	f2.setUDPHook(func(req finsReq) [][]byte { return [][]byte{finsResponseFrame(req, 0x0201, nil)} })
	up2 := startFINSUDP(t, f2)
	if c, err = Connect("127.0.0.1", WithTransport(TransportFINSUDP), WithPort(up2), WithTimeout(time.Second)); err == nil {
		c.Close()
		t.Fatal("connect accepted a destination-node error")
	}
	// ...while "command not supported" still proves a live FINS node.
	f3 := newFakeFINS()
	f3.setUDPHook(func(req finsReq) [][]byte {
		if req.Command == FINSCmdCPURead {
			return [][]byte{finsResponseFrame(req, 0x0401, nil)}
		}
		return nil
	})
	up3 := startFINSUDP(t, f3)
	if c, err = Connect("127.0.0.1", WithTransport(TransportFINSUDP), WithPort(up3), WithTimeout(time.Second)); err != nil {
		t.Fatal(err)
	}
	c.Close()
}

// --- Item 13: address range, chunking, SNA, discovery, handshake ------------

func TestAddressOutOfRangeRejected(t *testing.T) {
	for _, addr := range []string{"D99999", "D65536", "CIO70000.1", "D100[0]", "D100[99999999999]"} {
		if _, err := ParseAddress(addr); err == nil {
			t.Errorf("%s accepted", addr)
		}
	}
	if _, err := ParseAddress("D65535"); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAddressWithType("D65535", "DINT"); err == nil {
		t.Fatal("DINT at D65535 runs past the end of the area")
	}
	if _, err := ParseAddressWithType("D65534", "DINT"); err != nil {
		t.Fatal(err)
	}
}

func TestLargeReadIsChunkedAndLargeWriteRejected(t *testing.T) {
	f := newFakeFINS()
	for i := 0; i < 1500; i++ {
		f.set(AreaDMWord, uint16(i), uint16(i))
	}
	c := connectFINSTCPTest(t, f)
	vals, err := c.Read("D0[1500]")
	if err != nil || vals[0].Error != nil {
		t.Fatal(err, vals[0].Error)
	}
	words, ok := vals[0].GoValue().([]uint16)
	if !ok || len(words) != 1500 || words[0] != 0 || words[999] != 999 || words[1499] != 1499 {
		t.Fatalf("chunked read wrong: len=%d", len(words))
	}
	// W342 5-2-2: 999 words per Memory Area Read over Ethernet.
	reads := f.commands(FINSCmdMemoryRead)
	if len(reads) != 2 || !bytes.Equal(reads[0].Data, []byte{0x82, 0, 0, 0, 0x03, 0xE7}) ||
		!bytes.Equal(reads[1].Data, []byte{0x82, 0x03, 0xE7, 0, 0x01, 0xF5}) {
		t.Fatalf("chunk requests %x", reads)
	}

	big := make([]int, 1000)
	if err := c.Write("D0[1000]", big); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("1000-word write: %v", err)
	}
	if len(f.commands(FINSCmdMemoryWrite)) != 0 {
		t.Fatal("oversized write reached the wire")
	}
}

func TestFINSSourceNetworkIsLocal(t *testing.T) {
	f := newFakeFINS()
	port := startFINSTCP(t, f)
	c, err := Connect("127.0.0.1", WithTransport(TransportFINSTCP), WithPort(port), WithNetwork(3), WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Read("D0")
	r := f.requests()
	if len(r) == 0 || r[0].Header.DNA != 3 || r[0].Header.SNA != 0 {
		t.Fatalf("header %+v, want DNA=3 SNA=0", r)
	}

	uf := newFakeFINS()
	up := startFINSUDP(t, uf)
	uc, err := Connect("127.0.0.1", WithTransport(TransportFINSUDP), WithPort(up), WithNetwork(3), WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer uc.Close()
	if r := uf.requests(); len(r) == 0 || r[0].Header.DNA != 3 || r[0].Header.SNA != 0 {
		t.Fatalf("UDP header %+v, want DNA=3 SNA=0", r)
	}
}

func TestProbeFINSTCPReportsServerNode(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 20)
		if _, err := conn.Read(buf); err != nil {
			return
		}
		payload := binary.BigEndian.AppendUint32(nil, 0x05)    // client node
		payload = binary.BigEndian.AppendUint32(payload, 0x2A) // server node
		frame := finsTCPFrame(cmdNodeAddressResponse, 0, payload)
		conn.Write(frame[:10]) // deliver in pieces
		time.Sleep(20 * time.Millisecond)
		conn.Write(frame[10:])
		time.Sleep(50 * time.Millisecond)
	}()
	dev := probeFINSTCPAddr(net.IPv4(127, 0, 0, 1), ln.Addr().String(), time.Second)
	if dev == nil || dev.Node != 0x2A {
		t.Fatalf("probe = %+v, want server node 42", dev)
	}
}

func TestNodeAddressErrorFrameKeepsCode(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 20)
		conn.Read(buf)
		conn.Write(finsTCPFrame(cmdFINSFrameSendError, 0x21, nil)) // 16 bytes, no node fields
		time.Sleep(2 * time.Second)
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	start := time.Now()
	_, err = Connect("127.0.0.1", WithTransport(TransportFINSTCP), WithPort(port), WithTimeout(5*time.Second))
	if err == nil || !strings.Contains(err.Error(), "0x00000021") {
		t.Fatalf("err = %v, want PLC error code 0x21", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("waited for a timeout instead of reporting the error frame")
	}
}
