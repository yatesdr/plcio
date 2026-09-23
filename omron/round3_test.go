package omron

// Byte-level tests for the third hardening round: NJ/NX time types and
// vendor type codes (W506 7-7-1), Read Tag AddInfo, strict encoding, FINS
// write/read limits (W342 5-2-2), end codes (W342 5-1-3), FINS/TCP framing
// (W421 7-4-2) and address/type-hint validation. Loopback fakes only.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// cipTagName extracts the symbolic tag name from a CIP request path.
func cipTagName(req []byte) string {
	path := req[2 : 2+int(req[1])*2]
	if len(path) >= 2 && path[0] == 0x91 {
		return string(path[2 : 2+int(path[1])])
	}
	return ""
}

// mspSubRequests splits a Multiple Service Packet request into sub-requests.
func mspSubRequests(req []byte) [][]byte {
	p := cipPayload(req)
	n := int(binary.LittleEndian.Uint16(p))
	var out [][]byte
	for i := 0; i < n; i++ {
		start := int(binary.LittleEndian.Uint16(p[2+2*i:]))
		end := len(p)
		if i+1 < n {
			end = int(binary.LittleEndian.Uint16(p[2+2*(i+1):]))
		}
		out = append(out, p[start:end])
	}
	return out
}

// mspReply builds a Multiple Service Packet reply from individual replies.
func mspReply(replies [][]byte) []byte {
	body := binary.LittleEndian.AppendUint16(nil, uint16(len(replies)))
	off := 2 + 2*len(replies)
	for _, r := range replies {
		body = binary.LittleEndian.AppendUint16(body, uint16(off))
		off += len(r)
	}
	for _, r := range replies {
		body = append(body, r...)
	}
	return append([]byte{0x8A, 0, 0, 0}, body...)
}

func le64(v int64) []byte { return binary.LittleEndian.AppendUint64(nil, uint64(v)) }

var (
	njTime  = -(1500*time.Millisecond + 7)                               // -1.500000007 s
	njDate  = time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC)               // D#2024-03-15
	njTOD   = 13*time.Hour + 45*time.Minute + 30*time.Second + 123456789 // TOD#13:45:30.123456789
	njDT    = time.Date(2024, 3, 15, 13, 45, 30, 123456789, time.UTC)    // DT#2024-03-15-13:45:30.123456789
	njReply = map[string][]byte{                                         // tag -> Read Tag reply data (type, AddInfo len, data)
		"T_CIP":  append([]byte{0xDB, 0x00}, le64(int64(njTime))...),
		"T_NSEC": append([]byte{0x09, 0x00}, le64(int64(njTime))...),
		"D":      append([]byte{0x08, 0x00}, le64(njDate.UnixNano())...),
		"TOD":    append([]byte{0x0B, 0x00}, le64(int64(njTOD))...),
		"DT":     append([]byte{0x0A, 0x00}, le64(njDT.UnixNano())...),
		"E":      {0x07, 0x00, 0xFE, 0xFF, 0xFF, 0xFF},
		"BCD":    {0x04, 0x00, 0x34, 0x12},
		"S":      {0xA0, 0x02, 0xCD, 0xAB, 0x11, 0x22, 0x33, 0x44},
		"BADTOD": append([]byte{0x0B, 0x00}, le64(int64(24*time.Hour))...),
		"SHORT":  {0x0A, 0x00, 1, 2, 3},
		"BADSTR": {0xD0, 0x00, 0x09, 0x00, 'h', 'i'},
	}
)

func njFake(t *testing.T, writes *[][]byte, mu *sync.Mutex) *Client {
	t.Helper()
	return connectEIPTest(t, func(req []byte) []byte {
		switch req[0] {
		case svcReadTag:
			if d, ok := njReply[cipTagName(req)]; ok {
				return append([]byte{0xCC, 0, 0, 0}, d...)
			}
			return []byte{0xCC, 0, 0x05, 0}
		case 0x0A:
			var replies [][]byte
			for _, sub := range mspSubRequests(req) {
				d := njReply[cipTagName(sub)]
				replies = append(replies, append([]byte{0xCC, 0, 0, 0}, d...))
			}
			return mspReply(replies)
		case svcWriteTag:
			mu.Lock()
			*writes = append(*writes, append([]byte(nil), cipPayload(req)...))
			mu.Unlock()
			return []byte{0xCD, 0, 0, 0}
		}
		return []byte{req[0] | 0x80, 0, 0x08, 0}
	})
}

func TestNJTimeTypesDecode(t *testing.T) {
	var mu sync.Mutex
	var writes [][]byte
	c := njFake(t, &writes, &mu)

	want := map[string]any{
		"T_CIP":  int64(njTime),
		"T_NSEC": int64(njTime),
		"D":      njDate,
		"TOD":    int64(njTOD),
		"DT":     njDT,
		"E":      int32(-2),
		"BCD":    []byte{0x34, 0x12},
		"S":      []byte{0x11, 0x22, 0x33, 0x44}, // structure CRC (AddInfo) stripped
	}
	names := map[string]string{"T_CIP": "TIME", "T_NSEC": "TIME", "D": "DATE", "TOD": "TIME_OF_DAY",
		"DT": "DATE_AND_TIME", "E": "ENUM", "BCD": "UINT_BCD", "S": "STRUCT_A0"}
	check := func(path string, vals []*TagValue, tags []string) {
		t.Helper()
		for i, tag := range tags {
			tv := vals[i]
			if tv.Error != nil {
				t.Errorf("%s %s: %v", path, tag, tv.Error)
				continue
			}
			got := tv.GoValue()
			switch w := want[tag].(type) {
			case time.Time:
				if g, ok := got.(time.Time); !ok || !g.Equal(w) || g.Location() != time.UTC {
					t.Errorf("%s %s = %#v, want %v", path, tag, got, w)
				}
			case []byte:
				if g, ok := got.([]byte); !ok || !bytes.Equal(g, w) {
					t.Errorf("%s %s = %#v, want %x", path, tag, got, w)
				}
			default:
				if got != w {
					t.Errorf("%s %s = %#v, want %#v", path, tag, got, w)
				}
			}
			if tv.TypeName() != names[tag] {
				t.Errorf("%s %s type %s, want %s", path, tag, tv.TypeName(), names[tag])
			}
		}
	}
	tags := []string{"T_CIP", "T_NSEC", "D", "TOD", "DT", "E", "BCD", "S"}
	for _, tag := range tags { // single Read Tag path
		vals, err := c.Read(tag)
		if err != nil {
			t.Fatal(err)
		}
		check("single", vals, []string{tag})
	}
	vals, err := c.Read(tags...) // Multiple Service Packet path
	if err != nil {
		t.Fatal(err)
	}
	check("MSP", vals, tags)

	// Invalid or short data is an error, not a zero/clipped value.
	for _, tag := range []string{"BADTOD", "SHORT", "BADSTR"} {
		vals, err := c.Read(tag)
		if err != nil || vals[0].Error == nil || vals[0].GoValue() != nil {
			t.Errorf("%s: err=%v tvErr=%v value=%v, want per-tag error", tag, err, vals[0].Error, vals[0].GoValue())
		}
	}

	for _, code := range []uint16{TypeOmronTime, TypeOmronTimeNSec, TypeOmronDate, TypeOmronTOD, TypeOmronDT} {
		if TypeSize(code) != 8 {
			t.Errorf("TypeSize(%s) = %d, want 8", TypeName(code), TypeSize(code))
		}
	}
}

func TestNJTimeTypesEncode(t *testing.T) {
	zone := time.FixedZone("UTC+9", 9*3600)
	ok := []struct {
		code  uint16
		value any
		want  int64
	}{
		{TypeOmronTime, njTime, int64(njTime)},
		{TypeOmronTime, int64(math.MinInt64), math.MinInt64},
		{TypeOmronTimeNSec, int64(42), 42},
		{TypeOmronTOD, njTOD, int64(njTOD)},
		{TypeOmronTOD, int64(0), 0},
		{TypeOmronTOD, int64(24*time.Hour - 1), int64(24*time.Hour - 1)},
		// The wall clock of the value's own location is stored.
		{TypeOmronDate, time.Date(2024, 3, 15, 0, 0, 0, 0, zone), njDate.UnixNano()},
		{TypeOmronDate, njDate.UnixNano(), njDate.UnixNano()},
		{TypeOmronDT, time.Date(2024, 3, 15, 13, 45, 30, 123456789, zone), njDT.UnixNano()},
		{TypeOmronDT, uint64(njDT.UnixNano()), njDT.UnixNano()},
		{TypeOmronDT, time.Date(2106, 2, 6, 23, 59, 59, 999999999, time.UTC), njMaxDateTimeNs},
	}
	for _, tc := range ok {
		got, err := EncodeValue(tc.value, tc.code, false)
		if err != nil || !bytes.Equal(got, le64(tc.want)) {
			t.Errorf("%s %T %v: %x %v, want %x", TypeName(tc.code), tc.value, tc.value, got, err, le64(tc.want))
		}
	}
	bad := []struct {
		code  uint16
		value any
	}{
		{TypeOmronTOD, 24 * time.Hour},
		{TypeOmronTOD, -time.Nanosecond},
		{TypeOmronTime, njDT},
		{TypeOmronTime, 1.5},
		{TypeOmronTime, uint64(math.MaxUint64)},
		{TypeOmronDate, time.Date(2024, 3, 15, 13, 0, 0, 0, time.UTC)}, // not midnight
		{TypeOmronDate, njDate.UnixNano() + 1},
		{TypeOmronDate, time.Date(1969, 12, 31, 0, 0, 0, 0, time.UTC)},
		{TypeOmronDate, time.Date(2106, 2, 7, 0, 0, 0, 0, time.UTC)},
		{TypeOmronDT, int64(-1)},
		{TypeOmronDT, time.Second},
		{TypeOmronDT, "2024-03-15"},
		{TypeOmronDT, time.Date(2106, 2, 7, 0, 0, 0, 0, time.UTC)},
	}
	for _, tc := range bad {
		if got, err := EncodeValue(tc.value, tc.code, false); err == nil {
			t.Errorf("%s accepted %T %v as %x", TypeName(tc.code), tc.value, tc.value, got)
		}
	}
	if _, err := EncodeValue(njTime, TypeOmronTime, true); err == nil {
		t.Error("TIME encoded for FINS")
	}
}

func TestNJTimeWriteOverEIP(t *testing.T) {
	var mu sync.Mutex
	var writes [][]byte
	c := njFake(t, &writes, &mu)
	if err := c.Write("DT", njDT); err != nil {
		t.Fatal(err)
	}
	if err := c.Write("T_CIP", njTime); err != nil {
		t.Fatal(err)
	}
	if err := c.Write("TOD", 25*time.Hour); err == nil {
		t.Fatal("TOD 25h written")
	}
	if err := c.Write("S", []byte{1, 2, 3, 4}); err == nil {
		t.Fatal("structure written without its CRC")
	}
	mu.Lock()
	defer mu.Unlock()
	// W506 7-7-2: vendor code 0x0A goes back on the wire as 0A 00.
	want := [][]byte{
		append([]byte{0x0A, 0x00, 0x01, 0x00}, le64(njDT.UnixNano())...),
		append([]byte{0xDB, 0x00, 0x01, 0x00}, le64(int64(njTime))...),
	}
	if len(writes) != 2 || !bytes.Equal(writes[0], want[0]) || !bytes.Equal(writes[1], want[1]) {
		t.Fatalf("write payloads %x, want %x", writes, want)
	}
}

// The FINS pseudo-type codes 0x04..0x0C no longer capture NJ vendor codes:
// DATE_AND_TIME (wire 0x0A) used to decode as a REAL.
func TestCIPVendorCodesDoNotAliasFINSTypes(t *testing.T) {
	code, data, err := parseCIPReadReply(njReply["DT"])
	if err != nil || code != TypeOmronDT || code == TypeReal || len(data) != 8 {
		t.Fatalf("code %#x data %x err %v", code, data, err)
	}
	if _, _, err := parseCIPReadReply([]byte{0xA0, 0x02, 0xCD}); err == nil {
		t.Fatal("truncated AddInfo accepted")
	}
	if !bytes.Equal(cipWireType(TypeOmronDate), []byte{0x08, 0x00}) || !bytes.Equal(cipWireType(TypeCIPDINT), []byte{0xC4, 0x00}) {
		t.Fatal("wire type mapping")
	}
	if got := discoveryTypeCode(MakeArrayType(0x0A)); got != MakeArrayType(TypeOmronDT) {
		t.Fatalf("discovery type %#x", got)
	}
}

func TestEncodeValueRejectsOutOfRange(t *testing.T) {
	bad := []struct {
		code  uint16
		value any
	}{
		{TypeWord, 70000}, {TypeCIPUINT, int64(-1)}, {TypeOmronWord, uint64(65536)},
		{TypeCIPUDINT, -1}, {TypeCIPDINT, 1.5}, {TypeCIPDINT, int64(math.MaxInt32) + 1},
		{TypeCIPSINT, 128}, {TypeCIPUSINT, 256}, {TypeCIPLINT, uint64(math.MaxUint64)},
		{TypeCIPULINT, int64(-1)}, {TypeLWord, math.NaN()}, {TypeCIPREAL, 1e39},
		{TypeCIPBool, 2}, {TypeCIPINT, []int{1, 40000}}, {TypeCIPINT, []int64{}},
	}
	for _, tc := range bad {
		if got, err := EncodeValue(tc.value, tc.code, false); err == nil {
			t.Errorf("%s accepted %T %v as %x", TypeName(tc.code), tc.value, tc.value, got)
		}
	}
	good := []struct {
		code  uint16
		value any
		want  []byte
	}{
		{TypeCIPUSINT, 255, []byte{0xFF}},
		{TypeCIPSINT, int8(-128), []byte{0x80}},
		{TypeCIPUINT, float64(65535), []byte{0xFF, 0xFF}},
		{TypeCIPULINT, uint64(math.MaxUint64), bytes.Repeat([]byte{0xFF}, 8)},
		{TypeCIPLINT, int64(math.MinInt64), []byte{0, 0, 0, 0, 0, 0, 0, 0x80}},
		{TypeCIPINT, []int32{-1, 2}, []byte{0xFF, 0xFF, 2, 0}},
		{TypeCIPUSINT, []byte{1, 2}, []byte{1, 2}},
		{TypeOmronEnum, int64(-2), []byte{0xFE, 0xFF, 0xFF, 0xFF}},
		// W506 7-7-3 Boolean Data: status byte plus forced byte 0; with a
		// Num of Element for an array, one status byte per element.
		{TypeCIPBool, true, []byte{1, 0}},
		{TypeCIPBool, []bool{true, false, true}, []byte{1, 0, 1}},
	}
	for _, tc := range good {
		got, err := EncodeValue(tc.value, tc.code, false)
		if err != nil || !bytes.Equal(got, tc.want) {
			t.Errorf("%s %T %v: %x %v, want %x", TypeName(tc.code), tc.value, tc.value, got, err, tc.want)
		}
	}
	// FINS: DWORD 0x12345678 at D100/D101 is D100=0x5678 D101=0x1234.
	if got, err := EncodeValue(uint32(0x12345678), TypeDWord, true); err != nil || !bytes.Equal(got, []byte{0x56, 0x78, 0x12, 0x34}) {
		t.Fatalf("FINS DWORD %x %v", got, err)
	}
	// Appending the FINS STRING terminator must not write into the
	// caller's backing array.
	backing := []byte("abcX")
	if _, err := EncodeValue(backing[:3], TypeString, true); err != nil || backing[3] != 'X' {
		t.Fatalf("caller buffer modified: %q %v", backing, err)
	}
}

func TestCIPBoolScalarWriteFormat(t *testing.T) {
	var mu sync.Mutex
	var written []byte
	c := connectEIPTest(t, func(req []byte) []byte {
		switch req[0] {
		case svcReadTag:
			return []byte{0xCC, 0, 0, 0, 0xC1, 0x00, 0x01, 0x00}
		case svcWriteTag:
			mu.Lock()
			written = append([]byte(nil), cipPayload(req)...)
			mu.Unlock()
			return []byte{0xCD, 0, 0, 0}
		}
		return []byte{req[0] | 0x80, 0, 0x08, 0}
	})
	if err := c.Write("Flag", true); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []byte{0xC1, 0x00, 0x01, 0x00, 0x01, 0x00}; !bytes.Equal(written, want) {
		t.Fatalf("BOOL write payload %x, want %x", written, want)
	}
}

func TestFINSEndCodeTableW342(t *testing.T) {
	for code, text := range map[uint16]string{
		0x0001: "Service canceled",
		0x0401: "Service unsupported",
		0x0501: "destination address setting error",
		0x2102: "protected",
		0x3001: "Access right error",
		0x4001: "aborted",
	} {
		if err := FINSEndCodeError(code); err == nil || !strings.Contains(err.Error(), text) {
			t.Errorf("0x%04X: %v, want %q", code, err, text)
		}
	}
	err := finsResponseError(0x8205, []byte{0x03, 0x07})
	var fe *FINSEndError
	if !errors.As(err, &fe) || !fe.RelayError || !fe.HasRelayAddress || fe.RelayNetwork != 3 || fe.RelayNode != 7 ||
		!strings.Contains(err.Error(), "network 3 node 7") {
		t.Fatalf("relay error %v", err)
	}
}

// W342 5-1-3: with bit 15 set the response carries a relay error word; a
// read must fail rather than decode that word as data.
func TestFINSRelayErrorFailsRead(t *testing.T) {
	f := newFakeFINS()
	c := connectFINSTCPTest(t, f)
	f.setTCPHook(func(req finsReq) []byte {
		return finsTCPFrame(cmdFINSFrameSend, 0, finsResponseFrame(req, 0x8000, []byte{0x01, 0x05, 0x12, 0x34}))
	})
	vals, err := c.ReadWithTypes([]TagRequest{{Address: "D0", TypeHint: "WORD"}})
	var fe *FINSEndError
	if err != nil || vals[0].Error == nil || !errors.As(vals[0].Error, &fe) || fe.RelayNode != 5 {
		t.Fatalf("read with relay error: %v %v", err, vals[0].Error)
	}
}

func TestFINSWriteLimitIs997Words(t *testing.T) {
	for _, transport := range []Transport{TransportFINSTCP, TransportFINSUDP} {
		f := newFakeFINS()
		var port int
		if transport == TransportFINSTCP {
			port = startFINSTCP(t, f)
		} else {
			port = startFINSUDP(t, f)
		}
		c, err := Connect("127.0.0.1", WithTransport(transport), WithPort(port), WithTimeout(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		words := make([]int, 997)
		for i := range words {
			words[i] = i
		}
		if err := c.Write("D0[997]", words); err != nil {
			t.Fatalf("%s: 997-word write: %v", transport, err)
		}
		if err := c.Write("D0[998]", make([]int, 998)); err == nil || !strings.Contains(err.Error(), "997") {
			t.Fatalf("%s: 998-word write: %v", transport, err)
		}
		w := f.commands(FINSCmdMemoryWrite)
		// 6 bytes of area/address/count + 1994 data bytes = the 2,000-byte
		// command text limit (W342 3-2); the whole frame is 2,012 bytes.
		if len(w) != 1 || len(w[0].Data) != 2000 || !bytes.Equal(w[0].Data[:6], []byte{0x82, 0, 0, 0, 0x03, 0xE5}) {
			t.Fatalf("%s: writes %d, first %x", transport, len(w), w[0].Data[:6])
		}
		if f.get(AreaDMWord, 996) != 996 {
			t.Fatalf("%s: D996 = %d", transport, f.get(AreaDMWord, 996))
		}
		// A 999-word read is one command whose response is 2,012 bytes.
		vals, err := c.Read("D0[999]")
		if err != nil || vals[0].Error != nil {
			t.Fatalf("%s: 999-word read %v %v", transport, err, vals[0].Error)
		}
		if r := f.commands(FINSCmdMemoryRead); len(r) != 1 || !bytes.Equal(r[0].Data, []byte{0x82, 0, 0, 0, 0x03, 0xE7}) {
			t.Fatalf("%s: reads %x", transport, r)
		}
		c.Close()
	}
}

func TestFINSTCPConnectionConfirmationDiscarded(t *testing.T) {
	f := newFakeFINS()
	f.set(AreaDMWord, 10, 0xBEEF)
	c := connectFINSTCPTest(t, f)
	f.setTCPHook(func(req finsReq) []byte {
		code, data := f.handle(req)
		// W421 7-4-2 CONNECTION CONFIRMATION: 'FINS', length 8, command 6.
		confirm := finsTCPFrame(cmdConnectionConfirm, 0, nil)
		return append(confirm, finsTCPFrame(cmdFINSFrameSend, 0, finsResponseFrame(req, code, data))...)
	})
	vals, err := c.Read("D10")
	if err != nil || vals[0].Error != nil {
		t.Fatalf("read after CONNECTION CONFIRMATION: %v %v", err, vals[0].Error)
	}
	if v, _ := vals[0].Uint(); v != 0xBEEF || !c.IsConnected() {
		t.Fatalf("value %#x connected=%v", v, c.IsConnected())
	}

	// A length above W421's 2,020-byte maximum is a framing error.
	f.setTCPHook(func(req finsReq) []byte {
		hdr := []byte("FINS")
		hdr = binary.BigEndian.AppendUint32(hdr, maxFINSTCPLength+1)
		return append(hdr, 0, 0, 0, 2, 0, 0, 0, 0)
	})
	vals, err = c.Read("D10")
	if err == nil && (vals[0].Error == nil) || c.IsConnected() {
		t.Fatalf("oversized length accepted: %v %v connected=%v", err, vals[0].Error, c.IsConnected())
	}
}

func TestFINSTCPNodeAddressValidation(t *testing.T) {
	for _, nodes := range [][2]uint32{{0, 2}, {255, 2}, {10, 0}, {10, 0x1FF}} {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		go func(client, server uint32) {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			req := make([]byte, 20)
			if _, err := conn.Read(req); err != nil {
				return
			}
			// W421 7-4-2 request: FINS, length 0x0C, command 0, error 0, client node 0.
			if !bytes.Equal(req, append([]byte("FINS"), 0, 0, 0, 0x0C, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)) {
				t.Errorf("node address request %x", req)
			}
			payload := binary.BigEndian.AppendUint32(nil, client)
			payload = binary.BigEndian.AppendUint32(payload, server)
			conn.Write(finsTCPFrame(cmdNodeAddressResponse, 0, payload))
			time.Sleep(200 * time.Millisecond)
		}(nodes[0], nodes[1])
		_, err = Connect("127.0.0.1", WithTransport(TransportFINSTCP), WithPort(ln.Addr().(*net.TCPAddr).Port), WithTimeout(time.Second))
		ln.Close()
		if err == nil || !strings.Contains(err.Error(), "invalid node addresses") {
			t.Errorf("client=%d server=%d: %v", nodes[0], nodes[1], err)
		}
	}
	if msg := finsNodeAddressErrorMsg(0x23); msg != "client FINS node address out of range" {
		t.Fatal(msg)
	}
}

func TestFINSTypeHintAndTaskFlagValidation(t *testing.T) {
	if p, err := ParseAddressWithType("D100", " dint "); err != nil || p.TypeCode != TypeInt32 {
		t.Fatalf("lower-case hint: %+v %v", p, err)
	}
	// Documented aliases (docs/omron.md) used to fall back to WORD silently.
	for hint, code := range map[string]uint16{"INT16": TypeInt16, "int32": TypeInt32, "INT64": TypeInt64} {
		if p, err := ParseAddressWithType("D100", hint); err != nil || p.TypeCode != code {
			t.Errorf("hint %s: %+v %v", hint, p, err)
		}
	}
	for _, tc := range [][2]string{{"D100", "DINT32"}, {"D100", "VOID"}, {"D100.5", "WORD"}, {"TK3", "INT"}} {
		if p, err := ParseAddressWithType(tc[0], tc[1]); err == nil {
			t.Errorf("%s with hint %s accepted: %+v", tc[0], tc[1], p)
		}
	}
	if p, err := ParseAddressWithType("D100.5", "bool"); err != nil || p.TypeCode != TypeBool || p.BitOffset != 5 {
		t.Fatalf("bit with BOOL hint: %+v %v", p, err)
	}
	// W342 5-2-2: TK0000..TK0031 = area 06, address nnnn, bit 00.
	p, err := ParseAddress("TK5")
	if err != nil || p.MemoryArea != AreaTaskBit || p.Address != 5 || p.BitOffset != 0 || p.TypeCode != TypeBool {
		t.Fatalf("TK5 = %+v %v", p, err)
	}
	for _, a := range []string{"TK32", "TK0.5", "TK0[2]"} {
		if _, err := ParseAddress(a); err == nil {
			t.Errorf("%s accepted", a)
		}
	}
	f := newFakeFINS()
	f.set(AreaTaskBit, 5, 1)
	c := connectFINSTCPTest(t, f)
	vals, err := c.Read("TK5")
	if err != nil || vals[0].Error != nil || vals[0].GoValue() != true {
		t.Fatalf("TK5 read %v %v %v", err, vals[0].Error, vals[0].GoValue())
	}
	if r := f.commands(FINSCmdMemoryRead); len(r) != 1 || !bytes.Equal(r[0].Data, []byte{0x06, 0, 5, 0, 0, 1}) {
		t.Fatalf("TK5 request %x", r)
	}
	// An unknown hint on a read is a per-tag error, not a WORD read.
	vals, err = c.ReadWithTypes([]TagRequest{{Address: "D0", TypeHint: "FLOAT"}})
	if err != nil || vals[0].Error == nil {
		t.Fatalf("unknown hint read %v %v", err, vals[0].Error)
	}
}

func TestTagValueAccessorsDoNotWrap(t *testing.T) {
	ulint := &TagValue{DataType: TypeCIPULINT, Count: 1, Bytes: bytes.Repeat([]byte{0xFF}, 8)}
	if _, err := ulint.Int(); err == nil {
		t.Error("ULINT max converted to int64")
	}
	if v, err := ulint.Uint(); err != nil || v != math.MaxUint64 {
		t.Errorf("Uint %d %v", v, err)
	}
	neg := &TagValue{DataType: TypeCIPINT, Count: 1, Bytes: []byte{0xFF, 0xFF}}
	if _, err := neg.Uint(); err == nil {
		t.Error("INT -1 converted to uint64")
	}
	nan := &TagValue{DataType: TypeCIPLREAL, Count: 1, Bytes: le64(int64(math.Float64bits(math.NaN())))}
	if _, err := nan.Int(); err == nil {
		t.Error("NaN converted to int64")
	}
}

func TestEIPDeviceInfoIdentityLayout(t *testing.T) {
	c := connectEIPTest(t, func(req []byte) []byte {
		if req[0] != 0x01 {
			return []byte{req[0] | 0x80, 0, 0x08, 0}
		}
		id := []byte{0x2F, 0x00, 0x0C, 0x00, 0x34, 0x06, 0x01, 0x28, 0x30, 0x00, 0x78, 0x56, 0x34, 0x12}
		id = append(id, 10)
		id = append(id, "NJ501-1300"...)
		return append([]byte{0x81, 0, 0, 0}, id...)
	})
	info, err := c.GetDeviceInfo()
	if err != nil || info.SerialNumber != 0x12345678 || info.Model != "NJ501-1300" || info.Version != "1.40" || info.VendorID != 47 {
		t.Fatalf("identity %+v %v", info, err)
	}
}

func TestConnectRejectsInvalidPort(t *testing.T) {
	if _, err := Connect("127.0.0.1", WithTransport(TransportEIP), WithPort(70000)); err == nil {
		t.Fatal("port 70000 accepted")
	}
}
