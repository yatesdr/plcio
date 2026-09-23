package s7

import (
	"encoding/binary"
	"math"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ownerDB1 reproduces the owner's S7-1200 DB1 test layout.
func ownerDB1() []byte {
	db := make([]byte, 6000)
	binary.BigEndian.PutUint32(db[0:], 35)             // DB1.0 DINT
	binary.BigEndian.PutUint16(db[4:], uint16(0xFFF6)) // DB1.4 INT -10
	db[6] = 0x01                                       // DB1.6 BOOL (bit 0)
	binary.BigEndian.PutUint32(db[8:], 0xDEADBEEF)     // DB1.8 DWORD
	db[12] = 0x04                                      // DB1.12 USINT / bits (DBX12.2 set)
	binary.BigEndian.PutUint16(db[14:], 65000)         // DB1.14 UINT
	binary.BigEndian.PutUint32(db[16:], 0x01020304)    // DB1.16 DWORD
	binary.BigEndian.PutUint16(db[20:], 0xBEEF)        // DB1.20 WORD
	copy(db[22:], append([]byte{254, 5}, "hello"...))  // DB1.22 STRING
	for i := 0; i < 6; i++ {                           // DB1.302[6] DINT
		binary.BigEndian.PutUint32(db[302+4*i:], uint32(int32(-i)))
	}
	for i := 0; i < 4; i++ { // DB1.326[4] STRING[254]
		copy(db[326+256*i:], append([]byte{254, 2}, byte('a'+i), byte('A'+i)))
	}
	copy(db[5360:], []byte{0, 254, 0, 2, 0, 'h', 0, 'i'}) // DB1.5360 WSTRING
	return db
}

// ---- 1. failed read items are 4 bytes on the wire -------------------------

// Byte-for-byte replay of the S7-1200 answer to the hardware-confirmed batch
// [DB9999.0 DINT, DB1.0 DINT, DB1.4 INT, DB1.20 WORD, DB1.8 DWORD].
func TestParseReadResponseErrorItemIsFourBytes(t *testing.T) {
	data := []byte{
		0x0A, 0x00, 0x00, 0x00, // item 0: object does not exist
		0xFF, 0x04, 0x00, 0x20, 0x00, 0x00, 0x00, 0x23, // DB1.0 = 35
		0xFF, 0x04, 0x00, 0x10, 0xFF, 0xF6, // DB1.4 = -10
		0xFF, 0x04, 0x00, 0x10, 0xBE, 0xEF, // DB1.20
		0xFF, 0x04, 0x00, 0x20, 0xDE, 0xAD, 0xBE, 0xEF, // DB1.8
	}
	resp := append([]byte{0x32, 3, 0, 0, 0, 1, 0, 2, 0, byte(len(data)), 0, 0, 0x04, 5}, data...)
	vals, errs := parseReadResponse(resp, 5)
	if errs[0] == nil || !strings.Contains(errs[0].Error(), "object does not exist") {
		t.Fatalf("item 0 error = %v", errs[0])
	}
	want := [][]byte{nil, {0, 0, 0, 0x23}, {0xFF, 0xF6}, {0xBE, 0xEF}, {0xDE, 0xAD, 0xBE, 0xEF}}
	for i := 1; i < 5; i++ {
		if errs[i] != nil || !reflect.DeepEqual(vals[i], want[i]) {
			t.Errorf("item %d = %x, %v; want %x", i, vals[i], errs[i], want[i])
		}
	}
}

// An error item that declares an odd data length is followed by a fill byte
// like any other non-last item; a truncated trailing error item is tolerated.
func TestParseReadResponseErrorItemPaddingAndTruncation(t *testing.T) {
	data := []byte{
		0x05, 0x00, 0x00, 0x08, 0x99, 0x00, // error declaring 1 byte + fill byte
		0xFF, 0x04, 0x00, 0x08, 0x42, 0x00, // BYTE 0x42 + fill
		0x0A, // truncated final error item (return code only)
	}
	resp := append([]byte{0x32, 3, 0, 0, 0, 1, 0, 2, 0, byte(len(data)), 0, 0, 0x04, 3}, data...)
	vals, errs := parseReadResponse(resp, 3)
	if errs[0] == nil || errs[1] != nil || !reflect.DeepEqual(vals[1], []byte{0x42}) {
		t.Fatalf("items 0/1: %v %v %x", errs[0], errs[1], vals[1])
	}
	if errs[2] == nil || !strings.Contains(errs[2].Error(), "object does not exist") {
		t.Fatalf("truncated final item: %v", errs[2])
	}
}

func TestReadBatchMissingDBDoesNotShiftValues(t *testing.T) {
	f := newFakePLC(t)
	f.setDB(1, ownerDB1())
	c := f.connect()
	vals, err := c.ReadWithTypes([]TagRequest{
		{Address: "DB9999.0", TypeHint: "DINT"},
		{Address: "DB1.0", TypeHint: "DINT"},
		{Address: "DB1.4", TypeHint: "INT"},
		{Address: "DB1.20", TypeHint: "WORD"},
		{Address: "DB1.8", TypeHint: "DWORD"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if vals[0].Error == nil {
		t.Fatal("DB9999 should fail")
	}
	want := []interface{}{nil, int64(35), int64(-10), uint64(0xBEEF), uint64(0xDEADBEEF)}
	for i := 1; i < 5; i++ {
		if vals[i].Error != nil || vals[i].GoValue() != want[i] {
			t.Errorf("%s = %v (%v), want %v", vals[i].Name, vals[i].GoValue(), vals[i].Error, want[i])
		}
	}
}

// The owner's hardware-verified layout must keep reading exactly as before.
func TestOwnerLayoutStillReads(t *testing.T) {
	f := newFakePLC(t)
	f.setDB(1, ownerDB1())
	c := f.connect()
	vals, err := c.ReadWithTypes([]TagRequest{
		{Address: "DB1.0", TypeHint: "DINT"},
		{Address: "DB1.4", TypeHint: "INT"},
		{Address: "DB1.6", TypeHint: "BOOL"},
		{Address: "DB1.8", TypeHint: "DWORD"},
		{Address: "DB1.12", TypeHint: "USINT"},
		{Address: "DB1.14", TypeHint: "UINT"},
		{Address: "DB1.16", TypeHint: "DWORD"},
		{Address: "DB1.20", TypeHint: "WORD"},
		{Address: "DB1.22", TypeHint: "STRING"},
		{Address: "DB1.302[6]", TypeHint: "DINT"},
		{Address: "DB1.326[4]", TypeHint: "STRING"},
		{Address: "DB1.5360", TypeHint: "WSTRING"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []interface{}{
		int64(35), int64(-10), true, uint64(0xDEADBEEF), uint64(4), uint64(65000),
		uint64(0x01020304), uint64(0xBEEF), "hello",
		[]int64{0, -1, -2, -3, -4, -5}, []string{"aA", "bB", "cC", "dD"}, "hi",
	}
	for i, v := range vals {
		if v.Error != nil || !reflect.DeepEqual(v.GoValue(), want[i]) {
			t.Errorf("%s = %#v (%v), want %#v", v.Name, v.GoValue(), v.Error, want[i])
		}
	}
}

// ---- 2. bit reads ----------------------------------------------------------

func TestBitReadsDecodeBitTransportValue(t *testing.T) {
	f := newFakePLC(t)
	f.setDB(1, ownerDB1())
	f.mu.Lock()
	f.areas[s7AreaM] = []byte{0x08}
	f.mu.Unlock()
	c := f.connect()
	cases := []struct {
		req  TagRequest
		want bool
	}{
		{TagRequest{Address: "DB1.DBX12.2"}, true},
		{TagRequest{Address: "DB1.DBX12.0"}, false},
		{TagRequest{Address: "DB1.DBX12.1"}, false},
		{TagRequest{Address: "DB1.DBX6.0"}, true},
		{TagRequest{Address: "DB1.6", TypeHint: "BOOL"}, true},
		{TagRequest{Address: "M0.3"}, true},
		{TagRequest{Address: "M0.2"}, false},
	}
	for _, tc := range cases {
		// Single-item path and batched path.
		for _, reqs := range [][]TagRequest{{tc.req}, {tc.req, {Address: "DB1.0", TypeHint: "DINT"}}} {
			vals, err := c.ReadWithTypes(reqs)
			if err != nil || vals[0].Error != nil {
				t.Fatalf("%s: %v %v", tc.req.Address, err, vals[0].Error)
			}
			b, err := vals[0].Bool()
			if err != nil || b != tc.want || vals[0].GoValue() != tc.want {
				t.Errorf("%s (batch=%v) = %v/%v, want %v", tc.req.Address, len(reqs) > 1, b, vals[0].GoValue(), tc.want)
			}
		}
	}
}

// ---- 3. untyped writes -----------------------------------------------------

func TestUntypedWritesUseAddressSizeOrFail(t *testing.T) {
	f := newFakePLC(t)
	f.setDB(1, make([]byte, 64))
	f.mu.Lock()
	f.areas[s7AreaM] = make([]byte, 16)
	f.mu.Unlock()
	c := f.connect()

	for _, v := range []interface{}{3.14, int64(5), 42} {
		if err := c.Write("DB1.4", v); err == nil || !strings.Contains(err.Error(), "data type") {
			t.Fatalf("untyped DB1.4 <- %T: err = %v", v, err)
		}
	}
	if n := len(f.writeLog()); n != 0 {
		t.Fatalf("%d writes reached the PLC for size-less addresses", n)
	}

	check := func(addr string, v interface{}, off int, want []byte) {
		t.Helper()
		if err := c.Write(addr, v); err != nil {
			t.Fatalf("%s: %v", addr, err)
		}
		mem := f.db(1)
		if strings.HasPrefix(addr, "M") {
			f.mu.Lock()
			mem = append([]byte(nil), f.areas[s7AreaM]...)
			f.mu.Unlock()
		}
		if got := mem[off : off+len(want)+1]; !reflect.DeepEqual(got[:len(want)], want) || got[len(want)] != 0 {
			t.Fatalf("%s <- %v wrote %x, want %x then untouched", addr, v, got, want)
		}
	}
	// Fractional floats to integer-sized addresses are refused (they would
	// truncate); integral floats keep the existing integer encoding.
	for _, a := range []string{"DB1.DBD4", "MD4", "DB1.DBW12"} {
		if err := c.Write(a, 3.14); err == nil {
			t.Fatalf("%s <- 3.14 accepted without a REAL type", a)
		}
	}
	check("DB1.DBD4", float64(3), 4, []byte{0, 0, 0, 3})
	real314 := make([]byte, 4)
	binary.BigEndian.PutUint32(real314, math.Float32bits(3.14))
	if err := c.WriteWithType("DB1.4", 3.14, "REAL"); err != nil || !reflect.DeepEqual(f.db(1)[4:8], real314) {
		t.Fatalf("typed REAL write: %v %x", err, f.db(1)[4:8])
	}
	check("DB1.DBW12", 7, 12, []byte{0, 7})
	check("DB1.DBB20", 9, 20, []byte{9})
	check("DB1.DBD24", int64(-2), 24, []byte{0xFF, 0xFF, 0xFF, 0xFE})
	if err := c.Write("DB1.DBL32", 2.5); err == nil {
		t.Fatal("DB1.DBL32 <- 2.5 accepted without an LREAL type")
	}
	if err := c.Write("DB1.DBX40.3", true); err != nil || f.db(1)[40] != 0x08 {
		t.Fatalf("bit write: %v %x", err, f.db(1)[40])
	}

	// Configured type still wins for size-less addresses.
	if err := c.WriteWithType("DB1.48", 3.14, "REAL"); err != nil {
		t.Fatal(err)
	}
	if got := f.db(1)[48:53]; !reflect.DeepEqual(got, append(real314, 0)) {
		t.Fatalf("typed REAL wrote %x", got)
	}
	if err := c.WriteWithType("DB1.48", 1, "FLOAT"); err == nil {
		t.Fatal("unknown write type accepted")
	}
}

// ---- 4. write length fields ------------------------------------------------

func TestWriteDeclaredLengthMatchesData(t *testing.T) {
	f := newFakePLC(t)
	db := make([]byte, 700)
	db[0] = 10  // DB1.0 STRING[10]
	db[100] = 0 // DB1.100 WSTRING[6]
	db[101] = 6 //
	f.setDB(1, db)
	c := f.connect()

	if err := c.WriteWithType("DB1.0", "abc", "STRING"); err != nil {
		t.Fatalf("STRING write: %v", err)
	}
	if err := c.WriteWithType("DB1.100", "xy", "WSTRING"); err != nil {
		t.Fatalf("WSTRING write: %v", err)
	}
	if err := c.WriteWithType("DB1.200[3]", []int64{1, -2, 3}, "LINT"); err != nil {
		t.Fatalf("LINT array write: %v", err)
	}
	if err := c.WriteWithType("DB1.300[4]", []int32{1, 2, 3, 4}, "BYTE"); err != nil {
		t.Fatalf("BYTE array write: %v", err)
	}
	if err := c.WriteWithType("DB1.310[2]", []int32{5, 6}, "INT"); err != nil {
		t.Fatalf("INT array write: %v", err)
	}
	for _, w := range f.writeLog() {
		if n := w.count * tsElemSize(w.transport); n != len(w.data) {
			t.Errorf("write at bit %d declares %d x ts 0x%02X = %d bytes but sends %d", w.bitAddr, w.count, w.transport, n, len(w.data))
		}
	}
	got := f.db(1)
	if !reflect.DeepEqual(got[0:5], []byte{10, 3, 'a', 'b', 'c'}) || got[12] != 0 {
		t.Errorf("STRING bytes %x", got[0:13])
	}
	if !reflect.DeepEqual(got[100:108], []byte{0, 6, 0, 2, 0, 'x', 0, 'y'}) {
		t.Errorf("WSTRING bytes %x", got[100:108])
	}
	if binary.BigEndian.Uint64(got[208:]) != uint64(0xFFFFFFFFFFFFFFFE) || got[216+7] != 3 {
		t.Errorf("LINT array bytes %x", got[200:224])
	}
	if !reflect.DeepEqual(got[300:305], []byte{1, 2, 3, 4, 0}) || !reflect.DeepEqual(got[310:314], []byte{0, 5, 0, 6}) {
		t.Errorf("array bytes %x %x", got[300:305], got[310:314])
	}
}

func TestWriteRejectsDataLargerThanTarget(t *testing.T) {
	f := newFakePLC(t)
	db := make([]byte, 2000)
	db[600] = 4 // DB1.600 STRING[4]
	f.setDB(1, db)
	c := f.connect()
	cases := []struct {
		addr, hint string
		v          interface{}
	}{
		{"DB1.0[2]", "DINT", []int32{1, 2, 3}},
		{"DB1.0", "INT", []int32{1, 2}},
		{"DB1.DBW0", "", []int32{1, 2}},
		{"DB1.0[2]", "LREAL", make([]float64, 200)}, // would be chunked past the target
		{"DB1.600", "STRING", []string{"a", "b"}},
	}
	for _, tc := range cases {
		if err := c.WriteWithType(tc.addr, tc.v, tc.hint); err == nil {
			t.Errorf("%s %s <- %T accepted", tc.addr, tc.hint, tc.v)
		}
	}
	if n := len(f.writeLog()); n != 0 {
		t.Fatalf("%d oversized writes reached the PLC", n)
	}
	// Exact and shorter-than-target writes still go through.
	if err := c.WriteWithType("DB1.0[2]", []int32{1, 2}, "DINT"); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteWithType("DB1.0[4]", []int32{1}, "DINT"); err != nil {
		t.Fatal(err)
	}
}

// ---- 5. address overflow ---------------------------------------------------

func TestParseAddressRejectsUnencodableNumbers(t *testing.T) {
	for _, a := range []string{
		"DB70000.0", "DB65536.DBW0", "DB70000.DBX0.0", "DB1.2097152", "DB1.DBW2097152",
		"MB2097152", "I2097152.0", "DB99999999999999999999.0", "DB1.0[99999999999999999999]",
	} {
		if addr, err := ParseAddress(a); err == nil {
			t.Errorf("%s accepted as DB%d offset %d", a, addr.DBNumber, addr.Offset)
		}
	}
	for _, a := range []string{"DB65535.0", "DB65535.DBW0", "DB1.2097151", "MB2097151"} {
		if _, err := ParseAddress(a); err != nil {
			t.Errorf("%s rejected: %v", a, err)
		}
	}
	// An array whose end would not fit the 24-bit address fails per tag.
	f := newFakePLC(t)
	c := f.connect()
	vals, _ := c.ReadWithTypes([]TagRequest{{Address: "DB1.2097150[4]", TypeHint: "BYTE"}})
	if vals[0].Error == nil || len(f.readRequests()) != 0 {
		t.Fatalf("out-of-range array read: %v, %d requests", vals[0].Error, len(f.readRequests()))
	}
}

// ---- 6. type hints ---------------------------------------------------------

func TestUnknownTypeHintsAreRejected(t *testing.T) {
	f := newFakePLC(t)
	f.setDB(1, make([]byte, 2048))
	c := f.connect()
	for _, hint := range []string{"FLOAT", "LTIME", "LDT", "DINTT", "STRING[20]"} {
		vals, err := c.ReadWithTypes([]TagRequest{{Address: "DB1.0", TypeHint: hint}})
		if err != nil || vals[0].Error == nil || !strings.Contains(vals[0].Error.Error(), "unknown") {
			t.Errorf("hint %q: %v %v", hint, err, vals[0].Error)
		}
	}
	if n := len(f.readRequests()); n != 0 {
		t.Fatalf("%d reads sent for unknown hints", n)
	}
	recognized := []string{
		"BOOL", "BYTE", "USINT", "CHAR", "SINT", "WORD", "UINT", "INT", "DWORD", "UDINT",
		"DINT", "REAL", "DATE", "TIME", "TIME_OF_DAY", "TOD", "LWORD", "ULINT", "LINT",
		"LREAL", "WCHAR", "STRING", "WSTRING", "dint", " Real ", "INT[]", "S5TIME",
	}
	for _, hint := range recognized {
		vals, err := c.ReadWithTypes([]TagRequest{{Address: "DB1.0", TypeHint: hint}})
		if err != nil || vals[0].Error != nil {
			t.Errorf("hint %q: %v %v", hint, err, vals[0].Error)
		}
	}
	// No hint keeps the historical DINT default.
	vals, _ := c.ReadWithTypes([]TagRequest{{Address: "DB1.0"}})
	if vals[0].Error != nil || vals[0].DataType != TypeDInt {
		t.Fatalf("default type: %v %v", vals[0].DataType, vals[0].Error)
	}
}

// ---- 7. BOOL arrays --------------------------------------------------------

func TestBoolArrayReadUnpacksBits(t *testing.T) {
	f := newFakePLC(t)
	db := make([]byte, 16)
	db[6], db[7] = 0xA5, 0x02 // 1010 0101, 0000 0010
	f.setDB(1, db)
	c := f.connect()
	want := []bool{true, false, true, false, false, true, false, true, false, true}
	for _, reqs := range [][]TagRequest{
		{{Address: "DB1.6[10]", TypeHint: "BOOL"}},
		{{Address: "DB1.6[10]", TypeHint: "BOOL"}, {Address: "DB1.0", TypeHint: "INT"}},
	} {
		vals, err := c.ReadWithTypes(reqs)
		if err != nil || vals[0].Error != nil || !reflect.DeepEqual(vals[0].GoValue(), want) {
			t.Fatalf("BOOL[10] = %#v (%v %v)", vals[0].GoValue(), err, vals[0].Error)
		}
	}
	if err := c.WriteWithType("DB1.6[10]", []bool{true, false}, "BOOL"); err == nil ||
		!strings.Contains(err.Error(), "not supported") {
		t.Fatalf("BOOL array write: %v", err)
	}
	if n := len(f.writeLog()); n != 0 {
		t.Fatalf("BOOL array write reached the PLC")
	}
}

// ---- 8. string header trust -----------------------------------------------

func TestStringWriteRejectsImplausibleHeader(t *testing.T) {
	f := newFakePLC(t)
	db := make([]byte, 64)
	db[0], db[1] = 0xFF, 0xFF // WSTRING header 0xFFFF
	db[10] = 0xFF             // STRING max 255
	f.setDB(1, db)
	c := f.connect()
	if err := c.WriteWithType("DB1.0", "x", "WSTRING"); err == nil {
		t.Error("WSTRING with 0xFFFF header accepted")
	}
	if err := c.WriteWithType("DB1.10", "x", "STRING"); err == nil {
		t.Error("STRING with 255 header accepted")
	}
	if err := c.WriteWithType("DB1.0[2]", []string{"x"}, "WSTRING"); err == nil {
		t.Error("WSTRING array with 0xFFFF header accepted")
	}
	// Unreadable header: refuse instead of guessing a 254-char buffer.
	if err := c.WriteWithType("DB7.0", "x", "STRING"); err == nil {
		t.Error("STRING write with unreadable header accepted")
	}
	if n := len(f.writeLog()); n != 0 {
		t.Fatalf("%d writes sent", n)
	}
}

// ---- 9. batch sizing with fill bytes ---------------------------------------

func TestBatchSizingAccountsForFillBytes(t *testing.T) {
	f := newFakePLC(t)
	f.setDB(1, make([]byte, 1024))
	c := f.connect()
	// 18 x 21-byte items at PDU 480: 14 + 18*25 + 17 fill = 481 bytes.
	var reqs []TagRequest
	for i := 0; i < 18; i++ {
		reqs = append(reqs, TagRequest{Address: "DB1." + strconv.Itoa(i*22) + "[21]", TypeHint: "BYTE"})
	}
	vals, err := c.ReadWithTypes(reqs)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vals {
		if v.Error != nil {
			t.Fatalf("%s: %v", v.Name, v.Error)
		}
	}
}

// Batches that fit today must be packed identically.
func TestBatchPackingUnchangedForFittingBatches(t *testing.T) {
	f := newFakePLC(t)
	f.setDB(1, make([]byte, 1024))
	c := f.connect()
	sizes := func(reqs []TagRequest) []int {
		before := len(f.readRequests())
		if _, err := c.ReadWithTypes(reqs); err != nil {
			t.Fatal(err)
		}
		var out []int
		for _, r := range f.readRequests()[before:] {
			out = append(out, len(r)/12)
		}
		return out
	}
	var ints, even, odd []TagRequest
	for i := 0; i < 25; i++ {
		ints = append(ints, TagRequest{Address: "DB1." + strconv.Itoa(2*i), TypeHint: "INT"})
	}
	for i := 0; i < 18; i++ {
		even = append(even, TagRequest{Address: "DB1." + strconv.Itoa(i*22) + "[20]", TypeHint: "BYTE"})
	}
	for i := 0; i < 19; i++ { // odd items that fit easily keep the 19-item batch
		odd = append(odd, TagRequest{Address: "DB1." + strconv.Itoa(i*22) + "[3]", TypeHint: "BYTE"})
	}
	if got := sizes(ints); !reflect.DeepEqual(got, []int{19, 6}) {
		t.Errorf("INT packing %v", got)
	}
	if got := sizes(even); !reflect.DeepEqual(got, []int{18}) {
		t.Errorf("even packing %v", got)
	}
	if got := sizes(odd); !reflect.DeepEqual(got, []int{19}) {
		t.Errorf("odd small packing %v", got)
	}
}

// ---- 10. REAL response transport 0x07 carries a byte length ----------------

func TestReadRealResponseByteLength(t *testing.T) {
	data := []byte{
		0xFF, 0x07, 0x00, 0x04, 0x3F, 0xC0, 0x00, 0x00, // REAL 1.5, length in bytes
		0xFF, 0x04, 0x00, 0x10, 0x00, 0x07, // INT 7
	}
	resp := append([]byte{0x32, 3, 0, 0, 0, 1, 0, 2, 0, byte(len(data)), 0, 0, 0x04, 2}, data...)
	vals, errs := parseReadResponse(resp, 2)
	if errs[0] != nil || errs[1] != nil || !reflect.DeepEqual(vals[0], []byte{0x3F, 0xC0, 0, 0}) || !reflect.DeepEqual(vals[1], []byte{0, 7}) {
		t.Fatalf("%x %v", vals, errs)
	}

	f := newFakePLC(t)
	db := make([]byte, 16)
	binary.BigEndian.PutUint32(db[4:], math.Float32bits(1.5))
	f.setDB(1, db)
	c := f.connect()
	got, err := c.ReadWithTypes([]TagRequest{{Address: "DB1.4", TypeHint: "REAL"}, {Address: "DB1.DBD4"}})
	if err != nil || got[0].Error != nil || got[0].GoValue() != 1.5 {
		t.Fatalf("REAL = %v (%v %v)", got[0].GoValue(), err, got[0].Error)
	}
}

// ---- 11. reconnect / close races --------------------------------------------

func TestReconnectUsesConfiguredTimeout(t *testing.T) {
	f := newFakePLC(t)
	c := f.connect(WithTimeout(200 * time.Millisecond))
	f.mu.Lock()
	f.stallCR = true
	f.mu.Unlock()
	c.SetDisconnected()
	start := time.Now()
	if err := c.Reconnect(); err == nil {
		t.Fatal("reconnect to stalled PLC succeeded")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("reconnect took %v; configured timeout ignored", d)
	}
}

func TestConcurrentReconnectDoesNotLeakTransports(t *testing.T) {
	f := newFakePLC(t)
	c := f.connect()
	c.SetDisconnected()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.Reconnect(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if !c.IsConnected() {
		t.Fatal("not connected after reconnect")
	}
	if !f.waitOpenConns(1) {
		t.Fatalf("%d connections open after concurrent reconnects, want 1", f.openConns())
	}
	c.Close()
	if !f.waitOpenConns(0) {
		t.Fatalf("%d connections open after Close", f.openConns())
	}
}

func TestCloseDuringReconnectStaysClosed(t *testing.T) {
	f := newFakePLC(t)
	c := f.connect()
	<-f.crSeen
	hold := make(chan struct{})
	f.mu.Lock()
	f.holdCR = hold
	f.mu.Unlock()
	c.SetDisconnected()
	done := make(chan error, 1)
	go func() { done <- c.Reconnect() }()
	<-f.crSeen // reconnect is now dialing
	c.Close()
	close(hold)
	if err := <-done; err == nil {
		t.Error("Reconnect succeeded after Close")
	}
	if c.IsConnected() {
		t.Fatal("Close during Reconnect left the client connected")
	}
	if !f.waitOpenConns(0) {
		t.Fatalf("%d connections still open", f.openConns())
	}
}

func TestClientConcurrentUseIsRaceFree(t *testing.T) {
	f := newFakePLC(t)
	f.setDB(1, make([]byte, 64))
	c := f.connect()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(4)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				c.ReadWithTypes([]TagRequest{{Address: "DB1.0", TypeHint: "INT"}})
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				c.WriteWithType("DB1.2", int32(j), "INT")
				c.GetCPUInfo()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				c.SetDisconnected()
				c.Reconnect()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				c.IsConnected()
				c.ConnectionMode()
			}
		}()
	}
	wg.Wait()
	c.Close()
	if !f.waitOpenConns(0) {
		t.Fatalf("%d connections leaked", f.openConns())
	}
}

// ---- 12. documented DB1.12.0 bit syntax -----------------------------------

func TestParseAddressSimpleDBBit(t *testing.T) {
	a, err := ParseAddress("DB1.12.2")
	if err != nil || a.Area != AreaDB || a.DBNumber != 1 || a.Offset != 12 || a.BitNum != 2 || a.DataType != TypeBool {
		t.Fatalf("%+v %v", a, err)
	}
	if _, err := ParseAddress("DB1.12.8"); err == nil {
		t.Fatal("bit 8 accepted")
	}
	m, err := ParseAddress("M0")
	if err != nil || m.BitNum != 0 || m.DataType != TypeBool {
		t.Fatalf("M0 = %+v %v", m, err)
	}
	f := newFakePLC(t)
	f.setDB(1, ownerDB1())
	c := f.connect()
	vals, err := c.ReadWithTypes([]TagRequest{{Address: "DB1.12.2", TypeHint: "BOOL"}, {Address: "DB1.12.1", TypeHint: "BOOL"}})
	if err != nil || vals[0].GoValue() != true || vals[1].GoValue() != false {
		t.Fatalf("%v %v %v", vals[0].GoValue(), vals[1].GoValue(), err)
	}
}

// ---- 13. discovery framing and PDU negotiation ------------------------------

func TestDiscoveryReadsFragmentedConfirm(t *testing.T) {
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
		buf := make([]byte, 64)
		conn.Read(buf)
		cc := []byte{3, 0, 0, 11, 6, cotpCC, 0, 1, 0, 1, 0}
		for _, part := range [][]byte{cc[:2], cc[2:4], cc[4:6], cc[6:]} {
			conn.Write(part)
			time.Sleep(20 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
	}()
	dev := tryS7Connect(net.IPv4(127, 0, 0, 1), ln.Addr().String(), 0, 0, 2*time.Second)
	if dev == nil {
		t.Fatal("fragmented COTP CC not recognised")
	}
}

func TestSetupCommRejectsImplausiblePDU(t *testing.T) {
	resp := func(pdu uint16) []byte {
		r := []byte{0x32, 3, 0, 0, 0, 0, 0, 8, 0, 0, 0, 0, 0xF0, 0, 0, 1, 0, 1, 0, 0}
		binary.BigEndian.PutUint16(r[18:], pdu)
		return r
	}
	for _, pdu := range []uint16{0, 1, 100, 239} {
		if _, err := parseSetupCommResponse(resp(pdu)); err == nil {
			t.Errorf("PDU %d accepted", pdu)
		}
	}
	for pdu, want := range map[uint16]uint16{240: 240, 480: 480, 960: 960, 1920: 960} {
		if got, err := parseSetupCommResponse(resp(pdu)); err != nil || got != want {
			t.Errorf("PDU %d -> %d %v, want %d", pdu, got, err, want)
		}
	}
	f := newFakePLC(t)
	f.pdu = 0
	if c, err := Connect(f.addr(), WithRackSlot(0, 0), WithTimeout(time.Second)); err == nil {
		c.Close()
		t.Fatal("connected with PDU 0")
	}
}
