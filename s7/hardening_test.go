package s7

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---- A. over-long strings are rejected, never truncated --------------------

func TestStringWriteTooLongIsRejected(t *testing.T) {
	f := newFakePLC(t)
	db := ownerDB1()
	copy(db[5360:], []byte{0, 6, 0, 0}) // DB1.5360 WSTRING[6]
	f.setDB(1, db)
	c := f.connect()
	before := f.db(1)

	long := strings.Repeat("x", 300)
	if err := c.WriteWithType("DB1.22", long, "STRING"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("300-char STRING write: %v", err)
	}
	if err := c.WriteWithType("DB1.326[4]", []string{"ok", strings.Repeat("y", 255)}, "STRING"); err == nil ||
		!strings.Contains(err.Error(), "element 1") {
		t.Fatalf("STRING array element too long: %v", err)
	}
	// "abcde" + U+1F600 is 7 UTF-16 code units for a WSTRING[6].
	if err := c.WriteWithType("DB1.5360", "abcde\U0001F600", "WSTRING"); err == nil || !strings.Contains(err.Error(), "UTF-16") {
		t.Fatalf("7-unit WSTRING write: %v", err)
	}
	if n := len(f.writeLog()); n != 0 || !bytes.Equal(f.db(1), before) {
		t.Fatalf("%d writes reached the PLC for over-long strings", n)
	}

	// Exactly the maximum still works.
	exact := strings.Repeat("z", 254)
	if err := c.WriteWithType("DB1.22", exact, "STRING"); err != nil {
		t.Fatal(err)
	}
	if got := f.db(1)[22 : 22+256]; got[0] != 254 || got[1] != 254 || string(got[2:]) != exact {
		t.Fatalf("254-char STRING bytes % X", got[:4])
	}
	if err := c.WriteWithType("DB1.5360", "abcd\U0001F600", "WSTRING"); err != nil {
		t.Fatal(err)
	}
	want := []byte{0, 6, 0, 6, 0, 'a', 0, 'b', 0, 'c', 0, 'd', 0xD8, 0x3D, 0xDE, 0x00}
	if got := f.db(1)[5360 : 5360+16]; !bytes.Equal(got, want) {
		t.Fatalf("WSTRING bytes % X, want % X", got, want)
	}
}

// ---- H. WSTRING is UTF-16BE with surrogate pairs ---------------------------

func TestWStringUTF16RoundTrip(t *testing.T) {
	text := "Ä€\U0001F600z"
	enc, err := encodeWStringWithMaxLen(text, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0, 10, 0, 5, 0x00, 0xC4, 0x20, 0xAC, 0xD8, 0x3D, 0xDE, 0x00, 0x00, 'z'}
	if !bytes.Equal(enc[:len(want)], want) || len(enc) != 4+20 {
		t.Fatalf("encoded % X", enc)
	}
	v := &TagValue{DataType: TypeWString, Bytes: enc, BitNum: -1, Count: 1}
	if got := v.GoValue(); got != text {
		t.Fatalf("decoded %q, want %q", got, text)
	}
	arr := append(make([]byte, 0, 1024), make([]byte, 1024)...)
	copy(arr, enc)
	copy(arr[512:], []byte{0, 254, 0, 1, 0x00, 0xE9})
	v = &TagValue{DataType: TypeWString, Bytes: arr, BitNum: -1, Count: 2}
	if got := v.GoValue(); !reflect.DeepEqual(got, []string{text, "é"}) {
		t.Fatalf("array decoded %q", got)
	}
}

// ---- B. TIME writes and signed reads ---------------------------------------

func TestTimeWriteAndSignedRead(t *testing.T) {
	f := newFakePLC(t)
	f.setDB(1, ownerDB1())
	c := f.connect()

	cases := []struct {
		v    interface{}
		want uint32
	}{
		{int64(100), 100},
		{100 * time.Millisecond, 100},
		{int64(-100), 0xFFFFFF9C},
		{-2 * time.Second, uint32(0xFFFFF830)},
		{float64(250), 250},
		{int32(math.MaxInt32), math.MaxInt32},
		{int64(math.MinInt32), 0x80000000},
	}
	for _, tc := range cases {
		if err := c.WriteWithType("DB1.8", tc.v, "TIME"); err != nil {
			t.Fatalf("TIME <- %T(%v): %v", tc.v, tc.v, err)
		}
		if got := binary.BigEndian.Uint32(f.db(1)[8:]); got != tc.want {
			t.Fatalf("TIME <- %v wrote %08X, want %08X", tc.v, got, tc.want)
		}
		vals, err := c.ReadWithTypes([]TagRequest{{Address: "DB1.8", TypeHint: "TIME"}})
		if err != nil || vals[0].GoValue() != int64(int32(tc.want)) {
			t.Fatalf("TIME read %v (%v), want %d", vals[0].GoValue(), err, int32(tc.want))
		}
	}
	writes := len(f.writeLog())
	for _, bad := range []interface{}{int64(math.MaxInt32) + 1, int64(math.MinInt32) - 1, 1500 * time.Microsecond, 2.5, "100", uint64(math.MaxUint64)} {
		if err := c.WriteWithType("DB1.8", bad, "TIME"); err == nil {
			t.Errorf("TIME <- %T(%v) accepted", bad, bad)
		}
	}
	if n := len(f.writeLog()); n != writes {
		t.Fatalf("rejected TIME values reached the PLC")
	}
	// TIME on a D address and TIME arrays.
	if err := c.WriteWithType("DB1.DBD8", -5*time.Millisecond, "TIME"); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteWithType("DB1.302[2]", []int64{-1, 7}, "TIME"); err != nil {
		t.Fatal(err)
	}
	vals, _ := c.ReadWithTypes([]TagRequest{{Address: "DB1.DBD8", TypeHint: "TIME"}, {Address: "DB1.302[2]", TypeHint: "TIME"}})
	if vals[0].GoValue() != int64(-5) || !reflect.DeepEqual(vals[1].GoValue(), []int64{-1, 7}) {
		t.Fatalf("TIME reads %v %v", vals[0].GoValue(), vals[1].GoValue())
	}
}

// ---- B. S5TIME / DATE / TIME_OF_DAY / DATE_AND_TIME / DTL ------------------

func TestS5TimeCodec(t *testing.T) {
	for _, tc := range []struct {
		ms   int64
		word uint16
	}{
		{0, 0x0000}, {10, 0x0001}, {100, 0x0010}, {1000, 0x0100}, {9990, 0x0999},
		{10000, 0x1100}, {99900, 0x1999}, {127000, 0x2127}, {999000, 0x2999},
		{1000000, 0x3100}, {9990000, 0x3999}, // S5T#2H46M30S
	} {
		b, err := encodeS5Time(tc.ms)
		if err != nil || binary.BigEndian.Uint16(b) != tc.word {
			t.Errorf("encode %d ms = % X (%v), want %04X", tc.ms, b, err, tc.word)
		}
		if ms, err := decodeS5Time([]byte{byte(tc.word >> 8), byte(tc.word)}); err != nil || ms != tc.ms {
			t.Errorf("decode %04X = %d (%v), want %d", tc.word, ms, err, tc.ms)
		}
	}
	for _, bad := range []interface{}{int64(5), int64(10005), int64(-10), int64(9990001), int64(10000000), 15 * time.Microsecond} {
		if b, err := encodeS5Time(bad); err == nil {
			t.Errorf("S5TIME %v encoded as % X", bad, b)
		}
	}
	for _, bad := range []uint16{0x000A, 0x00A0, 0x0A00, 0x4000} {
		if _, err := decodeS5Time([]byte{byte(bad >> 8), byte(bad)}); err == nil {
			t.Errorf("invalid S5TIME %04X decoded", bad)
		}
	}
}

func TestDateTimeOfDayCodecs(t *testing.T) {
	check := func(name string, got []byte, err error, want []byte) {
		t.Helper()
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s = % X (%v), want % X", name, got, err, want)
		}
	}
	b, err := encodeDate(int64(0))
	check("DATE 0", b, err, []byte{0, 0})
	b, err = encodeDate(time.Date(2168, 12, 31, 0, 0, 0, 0, time.UTC))
	check("DATE 2168-12-31", b, err, []byte{0xFF, 0x62}) // 65378
	b, err = encodeDate(time.Date(2024, 2, 29, 0, 0, 0, 0, time.FixedZone("x", 3600)))
	check("DATE 2024-02-29", b, err, []byte{0x30, 0xBD}) // 12477
	for _, bad := range []interface{}{int64(-1), int64(65379), time.Date(2024, 2, 29, 1, 0, 0, 0, time.UTC), time.Date(1989, 12, 31, 0, 0, 0, 0, time.UTC)} {
		if _, err := encodeDate(bad); err == nil {
			t.Errorf("DATE %v accepted", bad)
		}
	}
	b, err = encodeTimeOfDay(int64(86399999))
	check("TOD max", b, err, []byte{0x05, 0x26, 0x5B, 0xFF})
	b, err = encodeTimeOfDay(13*time.Hour + 5*time.Millisecond)
	check("TOD 13h", b, err, []byte{0x02, 0xCA, 0x1C, 0x85})
	for _, bad := range []interface{}{int64(86400000), int64(-1), time.Microsecond} {
		if _, err := encodeTimeOfDay(bad); err == nil {
			t.Errorf("TOD %v accepted", bad)
		}
	}
}

func TestDateAndTimeAndDTLCodecs(t *testing.T) {
	ts := time.Date(2024, 2, 29, 13, 45, 7, 123_000_000, time.UTC) // Thursday
	dt, err := encodeDateAndTime(ts)
	wantDT := []byte{0x24, 0x02, 0x29, 0x13, 0x45, 0x07, 0x12, 0x35}
	if err != nil || !bytes.Equal(dt, wantDT) {
		t.Fatalf("DT = % X (%v), want % X", dt, err, wantDT)
	}
	if got, err := decodeDateAndTime(dt); err != nil || !got.Equal(ts) {
		t.Fatalf("DT decode %v %v", got, err)
	}
	old, _ := encodeDateAndTime(time.Date(1995, 12, 31, 23, 59, 59, 999_000_000, time.UTC)) // Sunday
	if !bytes.Equal(old, []byte{0x95, 0x12, 0x31, 0x23, 0x59, 0x59, 0x99, 0x91}) {
		t.Fatalf("DT 1995 = % X", old)
	}
	if got, _ := decodeDateAndTime(old); got.Year() != 1995 {
		t.Fatalf("DT 95 decoded as %v", got)
	}
	for _, bad := range []interface{}{time.Date(1989, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2090, 1, 1, 0, 0, 0, 0, time.UTC),
		ts.Add(time.Microsecond), int64(5)} {
		if _, err := encodeDateAndTime(bad); err == nil {
			t.Errorf("DT %v accepted", bad)
		}
	}
	for _, bad := range [][]byte{{0x24, 0x02, 0x30, 0, 0, 0, 0, 0x01}, {0x24, 0x1A, 0x01, 0, 0, 0, 0, 0x01}, make([]byte, 8)} {
		if _, err := decodeDateAndTime(bad); err == nil {
			t.Errorf("invalid DT % X decoded", bad)
		}
	}

	ns := time.Date(2024, 2, 29, 13, 45, 7, 123456789, time.UTC)
	dtl, err := encodeDTL(ns)
	wantDTL := []byte{0x07, 0xE8, 0x02, 0x1D, 0x05, 0x0D, 0x2D, 0x07, 0x07, 0x5B, 0xCD, 0x15}
	if err != nil || !bytes.Equal(dtl, wantDTL) {
		t.Fatalf("DTL = % X (%v), want % X", dtl, err, wantDTL)
	}
	if got, err := decodeDTL(dtl); err != nil || !got.Equal(ns) {
		t.Fatalf("DTL decode %v %v", got, err)
	}
	for _, bad := range []interface{}{time.Date(1969, 12, 31, 23, 59, 59, 0, time.UTC), time.Date(2262, 4, 12, 0, 0, 0, 0, time.UTC), "x"} {
		if _, err := encodeDTL(bad); err == nil {
			t.Errorf("DTL %v accepted", bad)
		}
	}
	if _, err := decodeDTL(make([]byte, 12)); err == nil {
		t.Error("all-zero DTL decoded")
	}
}

// End to end: the new types resolve, travel over the wire and fail reads of
// invalid stored values instead of returning a plausible wrong value.
func TestTimeTypesEndToEnd(t *testing.T) {
	f := newFakePLC(t)
	f.setDB(1, make([]byte, 64))
	c := f.connect()
	ts := time.Date(2031, 7, 4, 6, 7, 8, 9_000_000, time.UTC)
	writes := []struct {
		addr, hint string
		v          interface{}
		want       interface{}
	}{
		{"DB1.0", "S5TIME", 2 * time.Second, int64(2000)},
		{"DB1.2", "DATE", int64(12477), int64(12477)},
		{"DB1.4", "TOD", int64(3600000), int64(3600000)},
		{"DB1.8", "DT", ts, ts},
		{"DB1.16", "DTL", ts, ts},
		{"DB1.DBL32", "DATE_AND_TIME", ts, ts},
	}
	for _, w := range writes {
		if err := c.WriteWithType(w.addr, w.v, w.hint); err != nil {
			t.Fatalf("%s %s: %v", w.addr, w.hint, err)
		}
	}
	reqs := make([]TagRequest, len(writes))
	for i, w := range writes {
		reqs[i] = TagRequest{Address: w.addr, TypeHint: w.hint}
	}
	vals, err := c.ReadWithTypes(reqs)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range vals {
		if v.Error != nil || !reflect.DeepEqual(v.GoValue(), writes[i].want) {
			t.Errorf("%s = %#v (%v), want %#v", v.Name, v.GoValue(), v.Error, writes[i].want)
		}
	}
	if db := f.db(1); db[0] != 0x02 || db[1] != 0x00 || db[16+2] != 7 || db[16+4] != byte(time.Friday+1) {
		t.Fatalf("stored bytes % X", db[:28])
	}
	// Garbage in the PLC fails the tag.
	db := f.db(1)
	db[40], db[41] = 0x0A, 0xBC
	f.setDB(1, db)
	vals, _ = c.ReadWithTypes([]TagRequest{{Address: "DB1.40", TypeHint: "S5TIME"}, {Address: "DB1.44", TypeHint: "DTL"}})
	if vals[0].Error == nil || vals[1].Error == nil || vals[0].GoValue() != nil {
		t.Fatalf("invalid stored values: %v %v", vals[0].Error, vals[1].Error)
	}
	for _, name := range []string{"S5TIME", "DATE_AND_TIME", "DT", "DTL", "dtl"} {
		if _, ok := TypeCodeFromName(name); !ok {
			t.Errorf("%s not recognized", name)
		}
	}
}

// ---- D. type hints on sized addresses --------------------------------------

func TestSizedAddressUsesSameWidthHint(t *testing.T) {
	f := newFakePLC(t)
	db := ownerDB1()
	binary.BigEndian.PutUint32(db[16:], math.Float32bits(2.5))
	f.setDB(1, db)
	c := f.connect()

	vals, err := c.ReadWithTypes([]TagRequest{
		{Address: "DB1.DBD16", TypeHint: "REAL"},
		{Address: "DB1.DBW4", TypeHint: "INT"},
		{Address: "DB1.DBD0", TypeHint: "dint"},
		{Address: "DB1.DBB12", TypeHint: "SINT"},
		{Address: "DB1.DBX6.0", TypeHint: "BOOL"},
		{Address: "DB1.DBD16"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []interface{}{2.5, int64(-10), int64(35), int64(4), true, uint64(math.Float32bits(2.5))}
	for i, v := range vals {
		if v.Error != nil || v.GoValue() != want[i] {
			t.Errorf("%s = %#v (%v), want %#v", v.Name, v.GoValue(), v.Error, want[i])
		}
	}
	// The REAL read goes out with REAL transport.
	if item := f.readRequests()[0]; item[3] != tsREAL {
		t.Fatalf("DBD16 REAL transport 0x%02X", item[3])
	}

	if err := c.WriteWithType("DB1.DBD16", 3.25, "REAL"); err != nil {
		t.Fatal(err)
	}
	if got := math.Float32frombits(binary.BigEndian.Uint32(f.db(1)[16:])); got != 3.25 {
		t.Fatalf("REAL write stored %v", got)
	}
	if err := c.WriteWithType("DB1.DBD0", 3.25, "DINT"); err == nil {
		t.Fatal("fractional float to DINT-hinted DBD accepted")
	}

	writes := len(f.writeLog())
	reads := len(f.readRequests())
	for _, tc := range []struct{ addr, hint string }{
		{"DB1.DBW20", "DINT"}, {"DB1.DBD16", "LREAL"}, {"DB1.DBB12", "INT"}, {"DB1.DBB12", "BOOL"},
		{"DB1.DBX6.0", "INT"}, {"DB1.DBD16", "STRING"}, {"DB1.DBD16", "FLOAT"},
	} {
		vals, _ := c.ReadWithTypes([]TagRequest{{Address: tc.addr, TypeHint: tc.hint}})
		if vals[0].Error == nil {
			t.Errorf("read %s as %s accepted", tc.addr, tc.hint)
		}
		if err := c.WriteWithType(tc.addr, int64(1), tc.hint); err == nil {
			t.Errorf("write %s as %s accepted", tc.addr, tc.hint)
		}
	}
	if len(f.writeLog()) != writes || len(f.readRequests()) != reads {
		t.Fatal("width-mismatched requests reached the PLC")
	}
}

// ---- E. Keepalive ----------------------------------------------------------

func TestSZLRequestMatchesSnap7Template(t *testing.T) {
	// Snap7 S7_SZL_FIRST (s7_micro_client.cpp) without TPKT/COTP, PDU ref 0x0500.
	snap7 := []byte{0x32, 0x07, 0x00, 0x00, 0x05, 0x00, 0x00, 0x08, 0x00, 0x08,
		0x00, 0x01, 0x12, 0x04, 0x11, 0x44, 0x01, 0x00, 0xff, 0x09, 0x00, 0x04, 0x04, 0x24, 0x00, 0x00}
	if got := buildSZLRequest(0x0424, 0, 0x0500); !bytes.Equal(got, snap7) {
		t.Fatalf("SZL request % X\nwant        % X", got, snap7)
	}
}

func TestKeepalive(t *testing.T) {
	f := newFakePLC(t)
	f.setDB(1, ownerDB1())
	c := f.connect()
	if err := c.Keepalive(); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	seen := f.userData
	f.szlRefuse = true
	f.mu.Unlock()
	if seen != 1 {
		t.Fatalf("keepalive sent %d UserData requests", seen)
	}
	// A CPU that refuses the SZL still answered: the link is alive.
	if err := c.Keepalive(); err != nil || !c.IsConnected() {
		t.Fatalf("refused SZL: %v connected=%v", err, c.IsConnected())
	}
	// A response to someone else's request breaks the link.
	f.mu.Lock()
	f.tamper = func(req, resp []byte) []byte { resp[5]++; return resp }
	f.mu.Unlock()
	err := c.Keepalive()
	if !errors.Is(err, ErrConnectionLost) || !errors.Is(err, ErrProtocol) || c.IsConnected() {
		t.Fatalf("mismatched keepalive: %v connected=%v", err, c.IsConnected())
	}
	// Not connected: an error, not nil.
	if err := c.Keepalive(); !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("keepalive while down: %v", err)
	}
	c.Close()
	if err := c.Keepalive(); err == nil {
		t.Fatal("keepalive after Close returned nil")
	}
}

func TestKeepaliveDetectsDroppedLink(t *testing.T) {
	f := newFakePLC(t)
	c := f.connect()
	f.mu.Lock()
	for conn := range f.conns {
		conn.Close()
	}
	f.mu.Unlock()
	if err := c.Keepalive(); !errors.Is(err, ErrConnectionLost) || c.IsConnected() {
		t.Fatalf("keepalive on dropped link: %v connected=%v", err, c.IsConnected())
	}
}

// ---- F. rack/slot validation -----------------------------------------------

func TestRackSlotValidation(t *testing.T) {
	f := newFakePLC(t)
	for _, rs := range [][2]int{{8, 0}, {-1, 0}, {0, 32}, {0, -1}} {
		if c, err := Connect(f.addr(), WithRackSlot(rs[0], rs[1]), WithTimeout(time.Second)); err == nil {
			c.Close()
			t.Errorf("rack %d slot %d accepted", rs[0], rs[1])
		}
	}
	f.mu.Lock()
	accepted := f.accepted
	f.mu.Unlock()
	if accepted != 0 {
		t.Fatalf("%d connections dialled for invalid rack/slot", accepted)
	}
	c, err := Connect(f.addr(), WithRackSlot(7, 31), WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

// ---- G. S5 timers and counters (Snap7 addressing) --------------------------

func TestTimerCounterAddressing(t *testing.T) {
	tAddr, _ := ParseAddress("T5")
	cAddr, _ := ParseAddress("C300")
	// Snap7 ReadArea(S7AreaTM, 0, 5, 1, S7WLTimer): area 0x1D, transport 0x1D,
	// count 1, DB 0, address = timer number (no <<3).
	if got := addressToS7Any(tAddr); !bytes.Equal(got, []byte{0x12, 0x0A, 0x10, 0x1D, 0x00, 0x01, 0x00, 0x00, 0x1D, 0x00, 0x00, 0x05}) {
		t.Fatalf("T5 item % X", got)
	}
	if got := addressToS7Any(cAddr); !bytes.Equal(got, []byte{0x12, 0x0A, 0x10, 0x1C, 0x00, 0x01, 0x00, 0x00, 0x1C, 0x00, 0x01, 0x2C}) {
		t.Fatalf("C300 item % X", got)
	}
	// Snap7 WriteArea: data section transport OCTET STRING (0x09), length in bytes.
	req := buildWriteRequest(tAddr, []byte{0x12, 0x34}, 1)
	if got := req[len(req)-6:]; !bytes.Equal(got, []byte{0x00, 0x09, 0x00, 0x02, 0x12, 0x34}) {
		t.Fatalf("T5 write data % X", got)
	}
	if got := req[12:24]; !bytes.Equal(got, addressToS7Any(tAddr)) {
		t.Fatalf("T5 write item % X", got)
	}
	if _, err := ParseAddress("T70000"); err == nil {
		t.Fatal("T70000 accepted")
	}

	f := newFakePLC(t)
	f.mu.Lock()
	f.areas[s7AreaT] = make([]byte, 32)
	f.areas[s7AreaC] = make([]byte, 32)
	f.areas[s7AreaT][10], f.areas[s7AreaT][11] = 0x21, 0x27 // T5 = S5T#127S
	f.mu.Unlock()
	c := f.connect()
	vals, err := c.ReadWithTypes([]TagRequest{{Address: "T5"}, {Address: "T5", TypeHint: "S5TIME"}, {Address: "C3"}})
	if err != nil || vals[0].GoValue() != uint64(0x2127) || vals[1].GoValue() != int64(127000) || vals[2].Error != nil {
		t.Fatalf("T/C reads: %v %v %v %v", err, vals[0].GoValue(), vals[1].GoValue(), vals[2].Error)
	}
	if err := c.Write("C3", 0x0042); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	got := append([]byte(nil), f.areas[s7AreaC][6:8]...)
	f.mu.Unlock()
	if !bytes.Equal(got, []byte{0x00, 0x42}) {
		t.Fatalf("C3 stored % X", got)
	}
}

// ---- I. responses must answer the request ----------------------------------

func TestResponseMismatchBreaksConnection(t *testing.T) {
	cases := []struct {
		name   string
		tamper func(req, resp []byte) []byte
		write  bool
	}{
		{"pdu ref", func(req, resp []byte) []byte { resp[5] ^= 0xFF; return resp }, false},
		{"function", func(req, resp []byte) []byte { resp[12] = s7FuncWrite; return resp }, false},
		{"item count", func(req, resp []byte) []byte { resp[13]++; return resp }, false},
		{"message type", func(req, resp []byte) []byte { resp[1] = s7MsgUserData; return resp }, false},
		{"write pdu ref", func(req, resp []byte) []byte { resp[4]++; return resp }, true},
		{"write function", func(req, resp []byte) []byte { resp[12] = s7FuncRead; return resp }, true},
		{"write item count", func(req, resp []byte) []byte { resp[13] = 2; return resp }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakePLC(t)
			f.setDB(1, ownerDB1())
			c := f.connect()
			f.mu.Lock()
			f.tamper = tc.tamper
			f.mu.Unlock()
			var err error
			if tc.write {
				err = c.WriteWithType("DB1.0", int64(1), "DINT")
			} else {
				var vals []*TagValue
				vals, err = c.ReadWithTypes([]TagRequest{{Address: "DB1.0", TypeHint: "DINT"}, {Address: "DB1.4", TypeHint: "INT"}})
				if vals[0].Error == nil || !errors.Is(vals[0].Error, ErrProtocol) {
					t.Fatalf("item error %v", vals[0].Error)
				}
			}
			if !errors.Is(err, ErrConnectionLost) || c.IsConnected() {
				t.Fatalf("err %v connected=%v", err, c.IsConnected())
			}
		})
	}
}

func TestCheckResponseAcceptsErrorAcks(t *testing.T) {
	req := buildReadRequest([]*Address{{Area: AreaDB, DBNumber: 1, BitNum: -1, DataType: TypeByte, Size: 1, Count: 1}}, 0x1234)
	// Ack with error class and no parameters (e.g. PDU too large) is valid.
	errAck := []byte{0x32, 0x02, 0, 0, 0x12, 0x34, 0, 0, 0, 0, 0x85, 0x00}
	if err := checkResponse(req, errAck); err != nil {
		t.Fatal(err)
	}
	okResp := []byte{0x32, 0x03, 0, 0, 0x12, 0x34, 0, 2, 0, 5, 0, 0, 0x04, 1, 0xFF, 0x04, 0, 8, 7}
	if err := checkResponse(req, okResp); err != nil {
		t.Fatal(err)
	}
	noParams := []byte{0x32, 0x03, 0, 0, 0x12, 0x34, 0, 0, 0, 0, 0, 0}
	if err := checkResponse(req, noParams); !errors.Is(err, ErrProtocol) {
		t.Fatalf("successful read without parameters: %v", err)
	}
}

// ---- J. chunked writes report partial progress -----------------------------

func TestChunkedWriteFailureReportsBytesWritten(t *testing.T) {
	f := newFakePLC(t)
	f.setDB(1, make([]byte, 500)) // second chunk falls off the end of the DB
	c := f.connect()
	err := c.WriteWithType("DB1.0[100]", make([]float64, 100), "LREAL")
	if err == nil {
		t.Fatal("write past the DB end succeeded")
	}
	first := len(f.writeLog()[0].data)
	if !strings.Contains(err.Error(), "not atomic") || !strings.Contains(err.Error(), "after "+strconv.Itoa(first)+" of 800 bytes") {
		t.Fatalf("error does not report progress: %v", err)
	}
}

// REAL writes must use data transport 0x07 with a byte length (Snap7
// TS_ResReal); an S7-1200 rejects 0x04/bit-length with "data type/size
// mismatch" (observed on hardware).
func TestBuildWriteRequestRealDataTransport(t *testing.T) {
	addr, err := ParseAddress("DB1.DBD16")
	if err != nil {
		t.Fatal(err)
	}
	addr.DataType = TypeReal
	addr.Size = 4
	req := buildWriteRequest(addr, []byte{0x3F, 0xC0, 0, 0}, 1)
	data := req[10+14:]
	if data[1] != 0x07 || data[2] != 0 || data[3] != 4 {
		t.Fatalf("REAL data header %x, want 00 07 00 04", data[:4])
	}
	addr.DataType = TypeDWord
	req = buildWriteRequest(addr, []byte{0, 0, 0, 14}, 1)
	data = req[10+14:]
	if data[1] != 0x04 || data[2] != 0 || data[3] != 32 {
		t.Fatalf("DWORD data header %x, want 00 04 00 20", data[:4])
	}
}
