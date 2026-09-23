package logix

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"sync"
	"testing"
)

// hardeningTemplates models the hardware fixtures: a record UDT with a DINT,
// a built-in STRING member (template 0xFCE, handle 0x0FCE), a SINT and a
// custom 20-character string member.
func hardeningTemplates() map[uint16]*Template {
	stringType := func(id uint16, name string, capacity int, handle uint16) *Template {
		return &Template{ID: id, Name: name, Size: uint32((4 + capacity + 3) &^ 3), RawHandle: handle,
			Members:   []TemplateMember{{Name: "LEN", Type: TypeDINT}, {Name: "DATA", Type: TypeSINT | TypeArrayMask, Offset: 4, ArrayDims: []int{capacity}}},
			MemberMap: map[string]int{"LEN": 0, "DATA": 1}}
	}
	record := &Template{ID: 1, Name: "Employee", Size: 124, RawHandle: 0x7777,
		Members: []TemplateMember{{Name: "Card_Number", Type: TypeDINT}, {Name: "First_Name", Type: 0x8FCE, Offset: 4},
			{Name: "Flags", Type: TypeSINT, Offset: 92}, {Name: "Code", Type: 0x8123, Offset: 96}},
		MemberMap: map[string]int{"Card_Number": 0, "First_Name": 1, "Flags": 2, "Code": 3}}
	return map[uint16]*Template{1: record, 0xFCE: stringType(0xFCE, "STRING", 82, 0x0FCE), 0x123: stringType(0x123, "STR_20", 20, 0x5a5a)}
}

func hardeningTags() map[string]TagInfo {
	return map[string]TagInfo{
		"Employee_Data":     {Name: "Employee_Data", TypeCode: 0xA001, Instance: 1, Dimensions: []int{1000}},
		"test_string":       {Name: "test_string", TypeCode: TypeShortSTRING, Instance: 2},
		"test_string_array": {Name: "test_string_array", TypeCode: TypeArrayMask | TypeShortSTRING, Instance: 3, Dimensions: []int{5}},
		"Counter":           {Name: "Counter", TypeCode: TypeINT, Instance: 4},
		"Big":               {Name: "Big", TypeCode: TypeULINT, Instance: 5},
		"Flags":             {Name: "Flags", TypeCode: TypeArrayMask | TypeDWORD, Instance: 6, Dimensions: []int{2}},
		"Names":             {Name: "Names", TypeCode: TypeArrayMask | 0x8FCE, Instance: 7, Dimensions: []int{20}},
	}
}

// hardeningPeer answers writes, read-modify-writes and reads of the fixture
// tags; rmwStatus is the general status returned for Read Modify Write.
func hardeningPeer(t *testing.T, rmwStatus byte) (*Client, *fakePeer) {
	peer := newFakePeer(t, func(req []byte) []byte {
		switch req[0] {
		case 0x4d:
			return []byte{0xcd, 0, 0, 0}
		case 0x4e:
			return []byte{0xce, 0, rmwStatus, 0}
		case 0x53:
			return []byte{0xd3, 0, 0, 0}
		case 0x4c:
			name, _ := fakeSymbol(req)
			switch name {
			case "Employee_Data[999].Card_Number":
				return []byte{0xcc, 0, 0, 0, 0xc4, 0, 0x20, 0, 0, 0x80}
			case "Big":
				return []byte{0xcc, 0, 0, 0, 0xc9, 0, 0, 0, 0, 0, 0, 0, 0, 0x80}
			}
			return []byte{0xcc, 0, 0x05, 0}
		}
		t.Errorf("unexpected service %x", req[0])
		return nil
	})
	c := peer.newClient(t, false)
	c.tagInfo, c.templates = hardeningTags(), hardeningTemplates()
	return c, peer
}

// splitWrite returns the symbolic path, data type bytes, count and data of a
// Write Tag request.
func splitWrite(t *testing.T, req []byte) (string, []byte, uint16, []byte) {
	t.Helper()
	if req[0] != 0x4d {
		t.Fatalf("not a Write Tag request: % x", req)
	}
	name, rest := fakeSymbol(req)
	typeLen := 2
	if binary.LittleEndian.Uint16(rest) == CIPStructType {
		typeLen = 4
	}
	return name, rest[:typeLen], binary.LittleEndian.Uint16(rest[typeLen:]), rest[typeLen+2:]
}

func TestStringWritesUseNativeLayout(t *testing.T) {
	c, peer := hardeningPeer(t, 0)
	standard := make([]byte, 88)
	standard[0] = 5
	copy(standard[4:], "PLCIO")
	custom := make([]byte, 24)
	custom[0] = 2
	copy(custom[4:], "ok")
	for _, tc := range []struct {
		tag, text string
		typ       []byte
		data      []byte
	}{
		{"Employee_Data[999].First_Name", "PLCIO", []byte{0xa0, 0x02, 0xce, 0x0f}, standard},
		{"Employee_Data[3].Code", "ok", []byte{0xa0, 0x02, 0x5a, 0x5a}, custom},
		{"test_string", "hello", []byte{0xda, 0}, []byte("\x05hello")},
		{"test_string_array[2]", "", []byte{0xda, 0}, []byte{0}},
	} {
		before := len(peer.requestLog())
		if err := c.Write(tc.tag, tc.text); err != nil {
			t.Fatalf("%s: %v", tc.tag, err)
		}
		log := peer.requestLog()
		if len(log) != before+1 {
			t.Fatalf("%s: %d requests", tc.tag, len(log)-before)
		}
		name, typ, count, data := splitWrite(t, log[before])
		if name != tc.tag || !bytes.Equal(typ, tc.typ) || count != 1 || !bytes.Equal(data, tc.data) {
			t.Fatalf("%s: wrote %s type % x count %d data % x", tc.tag, name, typ, count, data)
		}
	}
	// WriteString takes the same native path.
	if err := c.WriteString("test_string", "abc"); err != nil {
		t.Fatal(err)
	}
	if _, typ, _, data := splitWrite(t, peer.requestLog()[len(peer.requestLog())-1]); typ[0] != 0xda || string(data) != "\x03abc" {
		t.Fatalf("WriteString sent type % x data % x", typ, data)
	}
	// Over-capacity strings are rejected without truncation or traffic.
	before := len(peer.requestLog())
	for tag, text := range map[string]string{
		"Employee_Data[1].First_Name": strings.Repeat("x", 83),
		"Employee_Data[1].Code":       strings.Repeat("x", 21),
		"test_string":                 strings.Repeat("x", 256),
	} {
		if err := c.Write(tag, text); err == nil {
			t.Fatalf("%s: %d-byte string accepted", tag, len(text))
		}
	}
	if err := c.Write("Employee_Data[1].First_Name", strings.Repeat("x", 82)); err != nil {
		t.Fatalf("full-capacity STRING rejected: %v", err)
	}
	if n := len(peer.requestLog()) - before; n != 1 {
		t.Fatalf("%d requests for rejected strings", n)
	}
	// LEN/DATA members keep their atomic encodings.
	if err := c.Write("Employee_Data[999].First_Name.LEN", 3); err != nil {
		t.Fatal(err)
	}
	if name, typ, count, data := splitWrite(t, peer.requestLog()[len(peer.requestLog())-1]); name != "Employee_Data[999].First_Name.LEN" ||
		!bytes.Equal(typ, []byte{0xc4, 0}) || count != 1 || !bytes.Equal(data, []byte{3, 0, 0, 0}) {
		t.Fatalf("LEN write: %s % x %d % x", name, typ, count, data)
	}
}

func TestIntegerBitReadAndAtomicWrite(t *testing.T) {
	c, peer := hardeningPeer(t, 0)
	values, err := c.Read("Employee_Data[999].Card_Number.5", "Employee_Data[999].Card_Number.4",
		"Employee_Data[999].Card_Number.31", "Big.63", "Big.0")
	if err != nil || len(values) != 5 {
		t.Fatalf("read: %v %d", err, len(values))
	}
	want := map[string]bool{"Employee_Data[999].Card_Number.5": true, "Employee_Data[999].Card_Number.4": false,
		"Employee_Data[999].Card_Number.31": true, "Big.63": true, "Big.0": false}
	for _, v := range values {
		if got, err := v.Bool(); err != nil || got != want[v.Name] {
			t.Fatalf("%s: %v %v", v.Name, got, err)
		}
	}
	if code, ok := c.ResolveTagType("Employee_Data[999].Card_Number.5"); !ok || code != TypeBOOL {
		t.Fatalf("bit type %x %v", code, ok)
	}
	// Not bit references: out-of-range bit, non-integer parent, array parent.
	for _, name := range []string{"Employee_Data[999].Card_Number.32", "Employee_Data[999].First_Name.1", "Flags.1", "Employee_Data.1"} {
		if _, _, _, ok := c.bitReference(name); ok {
			t.Fatalf("%s treated as a bit", name)
		}
	}

	for _, tc := range []struct {
		tag       string
		value     any
		or, and   []byte
		parentTag string
	}{
		{"Employee_Data[999].Card_Number.5", true, []byte{0x20, 0, 0, 0}, []byte{0xff, 0xff, 0xff, 0xff}, "Employee_Data[999].Card_Number"},
		{"Employee_Data[999].Card_Number.5", int64(0), []byte{0, 0, 0, 0}, []byte{0xdf, 0xff, 0xff, 0xff}, "Employee_Data[999].Card_Number"},
		{"Employee_Data[7].Flags.7", 1.0, []byte{0x80}, []byte{0xff}, "Employee_Data[7].Flags"},
		{"Counter.9", false, []byte{0, 0}, []byte{0xff, 0xfd}, "Counter"},
	} {
		before := len(peer.requestLog())
		if err := c.Write(tc.tag, tc.value); err != nil {
			t.Fatalf("%s: %v", tc.tag, err)
		}
		log := peer.requestLog()[before:]
		if len(log) != 1 || log[0][0] != 0x4e {
			t.Fatalf("%s: expected one Read Modify Write, got % x", tc.tag, log)
		}
		name, rest := fakeSymbol(log[0])
		size := len(tc.or)
		wantBody := append(binary.LittleEndian.AppendUint16(nil, uint16(size)), append(tc.or, tc.and...)...)
		if name != tc.parentTag || !bytes.Equal(rest, wantBody) {
			t.Fatalf("%s: RMW %s % x, want % x", tc.tag, name, rest, wantBody)
		}
	}
	if err := c.Write("Counter.1", 2); err == nil {
		t.Fatal("non-boolean bit value accepted")
	}
}

func TestBitWriteUnsupportedServiceHasNoFallback(t *testing.T) {
	c, peer := hardeningPeer(t, StatusServiceNotSupport)
	err := c.Write("Counter.3", true)
	if err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("unsupported RMW error: %v", err)
	}
	log := peer.requestLog()
	if len(log) != 1 || log[0][0] != 0x4e {
		t.Fatalf("expected only the Read Modify Write request, got % x", log)
	}
}

func TestMicro800ForwardOpenOmitsBackplane(t *testing.T) {
	for _, tc := range []struct {
		plc  *PLC
		want []byte
	}{
		{&PLC{micro800: true, RoutePath: []byte{}}, []byte{0x20, 0x02, 0x24, 0x01}},
		{&PLC{}, []byte{0x01, 0x00, 0x20, 0x02, 0x24, 0x01}},
		{&PLC{Slot: 3}, []byte{0x01, 0x03, 0x20, 0x02, 0x24, 0x01}},
		{&PLC{micro800: true, RoutePath: []byte{0x01, 0x02}}, []byte{0x01, 0x02, 0x20, 0x02, 0x24, 0x01}},
	} {
		if got := tc.plc.buildConnectionPath(); !bytes.Equal(got, tc.want) {
			t.Fatalf("%+v: path % x, want % x", tc.plc.RoutePath, got, tc.want)
		}
	}
	cfg := &options{}
	WithMicro800()(cfg)
	if !cfg.micro800 || cfg.skipForwardOpen || cfg.routePath == nil || len(cfg.routePath) != 0 {
		t.Fatalf("WithMicro800 options %+v", cfg)
	}
	// On the wire: path size 2 words, Message Router only.
	var mu sync.Mutex
	var open []byte
	peer := newFakePeer(t, func(req []byte) []byte {
		if req[0] == 0x5b || req[0] == 0x54 {
			mu.Lock()
			open = append([]byte(nil), req...)
			mu.Unlock()
			return fakeForwardOpenReply(req)
		}
		return []byte{req[0] | 0x80, 0, 0, 0}
	})
	c := peer.newClient(t, false)
	c.plc.micro800, c.plc.RoutePath = true, []byte{}
	if err := c.plc.OpenConnection(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !bytes.HasSuffix(open, []byte{0xa3, 0x02, 0x20, 0x02, 0x24, 0x01}) {
		t.Fatalf("Micro800 Forward Open path: % x", open)
	}
}

func TestForwardOpenOriginatorSerialIsRandomPerPLC(t *testing.T) {
	var mu sync.Mutex
	var opens [][]byte
	peer := newFakePeer(t, func(req []byte) []byte {
		switch req[0] {
		case 0x5b:
			mu.Lock()
			opens = append(opens, append([]byte(nil), req...))
			mu.Unlock()
			return fakeForwardOpenReply(req)
		case 0x4e:
			return []byte{0xce, 0, 0, 0}
		}
		return []byte{req[0] | 0x80, 0, 0, 0}
	})
	a, b := peer.newClient(t, true), peer.newClient(t, true)
	// Reopening on the same PLC keeps its serial.
	if err := a.plc.CloseConnection(); err != nil {
		t.Fatal(err)
	}
	if err := a.plc.OpenConnection(); err != nil {
		t.Fatal(err)
	}
	conn, _ := a.plc.activeConn()
	mu.Lock()
	defer mu.Unlock()
	if len(opens) != 3 {
		t.Fatalf("%d Forward Opens", len(opens))
	}
	serial := func(req []byte) uint32 { return binary.LittleEndian.Uint32(req[20:24]) }
	if serial(opens[0]) == serial(opens[1]) || serial(opens[0]) == 42 || serial(opens[0]) == 0 {
		t.Fatalf("originator serials %d/%d", serial(opens[0]), serial(opens[1]))
	}
	if serial(opens[2]) != serial(opens[0]) || conn.OrigSerial != serial(opens[2]) {
		t.Fatalf("reopen serial %d, first %d, stored %d", serial(opens[2]), serial(opens[0]), conn.OrigSerial)
	}
	if b.plc.origSerial != serial(opens[1]) {
		t.Fatal("second client's serial not retained")
	}
}

func TestConnectedReplyMismatchDropsConnection(t *testing.T) {
	for _, fault := range []string{"sequence", "connection-id", "none"} {
		t.Run(fault, func(t *testing.T) {
			peer := newFakePeer(t, func(req []byte) []byte {
				switch req[0] {
				case 0x5b:
					return fakeForwardOpenReply(req)
				case 0x4e:
					return []byte{0xce, 0, 0, 0}
				}
				return []byte{0xcc, 0, 0, 0, 0xc4, 0, 7, 0, 0, 0}
			})
			peer.mangle = func(address, data []byte) ([]byte, []byte) {
				switch fault {
				case "sequence":
					data[0]++
				case "connection-id":
					address[0]++
				}
				return address, data
			}
			c := peer.newClient(t, true)
			value, err := c.ReadWithCount("Scalar", 1)
			if fault == "none" {
				if err != nil || !c.IsConnected() {
					t.Fatalf("matching reply rejected: %v", err)
				}
				if n, _ := value.Int(); n != 7 {
					t.Fatalf("value %d", n)
				}
				return
			}
			if !errors.Is(err, ErrConnectionLost) {
				t.Fatalf("mismatched reply accepted: %v %v", value, err)
			}
			if c.IsConnected() {
				t.Fatal("transport kept after protocol error")
			}
			if conn, _ := c.plc.activeConn(); conn != nil {
				t.Fatal("CIP connection kept after protocol error")
			}
		})
	}
}

// symbolEntry encodes one Get Instance Attribute List entry for attributes
// 1 (name), 2 (type) and 8 (three UDINT dimensions).
func symbolEntry(instance uint32, name string, typeCode uint16, dims [3]uint32) []byte {
	out := binary.LittleEndian.AppendUint32(nil, instance)
	out = binary.LittleEndian.AppendUint16(out, uint16(len(name)))
	out = append(out, name...)
	out = binary.LittleEndian.AppendUint16(out, typeCode)
	for _, d := range dims {
		out = binary.LittleEndian.AppendUint32(out, d)
	}
	return out
}

func TestSymbolListKeepsAllArrayDimensions(t *testing.T) {
	var data []byte
	data = append(data, symbolEntry(1, "Matrix", 0x4000|TypeDINT, [3]uint32{2, 3, 0})...)
	data = append(data, symbolEntry(2, "Cube", 0x6000|TypeREAL, [3]uint32{2, 3, 4})...)
	data = append(data, symbolEntry(3, "Long", 0x2000|TypeSINT, [3]uint32{70000, 0, 0})...)
	data = append(data, symbolEntry(4, "Scalar", TypeDINT, [3]uint32{9, 9, 9})...)
	data = append(data, symbolEntry(5, "test_dint_array", 0x2000|TypeDINT, [3]uint32{3, 0, 0})...)
	tags, last := parseSymbolListResponse(data)
	want := map[string][]int{"Matrix": {2, 3}, "Cube": {2, 3, 4}, "Long": {70000}, "Scalar": nil, "test_dint_array": {3}}
	if len(tags) != len(want) || last != 5 {
		t.Fatalf("%d tags, last %d", len(tags), last)
	}
	for _, tag := range tags {
		if w := want[tag.Name]; len(w) != len(tag.Dimensions) || (len(w) > 0 && !equalInts(w, tag.Dimensions)) {
			t.Fatalf("%s dims %v, want %v", tag.Name, tag.Dimensions, w)
		}
	}
	c := &Client{tagInfo: map[string]TagInfo{"Matrix": tags[0]}}
	if c.getElementCount("Matrix") != 6 {
		t.Fatalf("Matrix element count %d", c.getElementCount("Matrix"))
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestResolvedTagTypesAreCachedUntilTagDisappears(t *testing.T) {
	var mu sync.Mutex
	pages := 0
	missing := false
	peer := newFakePeer(t, func(req []byte) []byte {
		mu.Lock()
		defer mu.Unlock()
		switch req[0] {
		case 0x55:
			pages++
			out := []byte{0xd5, 0, 0, 0}
			out = append(out, symbolEntry(10, "Other", TypeREAL, [3]uint32{})...)
			return append(out, symbolEntry(11, "Speed", TypeDINT, [3]uint32{})...)
		case 0x4d:
			if missing {
				return []byte{0xcd, 0, 0x05, 0}
			}
			return []byte{0xcd, 0, 0, 0}
		}
		t.Errorf("unexpected service %x", req[0])
		return nil
	})
	c := peer.newClient(t, false)
	pageCount := func() int { mu.Lock(); defer mu.Unlock(); return pages }
	for i := 0; i < 3; i++ {
		if code, ok := c.ResolveTagType("Speed"); !ok || code != TypeDINT {
			t.Fatalf("resolve %x %v", code, ok)
		}
		if err := c.WriteTagCount("Speed", TypeDINT, []byte{1, 0, 0, 0}, 1); err != nil {
			t.Fatal(err)
		}
	}
	if pageCount() != 1 {
		t.Fatalf("symbol table paged %d times for repeated writes", pageCount())
	}
	mu.Lock()
	missing = true
	mu.Unlock()
	if err := c.WriteTagCount("Speed", TypeDINT, []byte{1, 0, 0, 0}, 1); err == nil {
		t.Fatal("missing tag write succeeded")
	}
	if _, ok := c.lookupResolvedType("Speed"); ok {
		t.Fatal("cached type kept after the controller reported the tag missing")
	}
	c.ResolveTagType("Speed")
	if pageCount() != 2 {
		t.Fatalf("symbol table paged %d times after invalidation", pageCount())
	}
	// Catalog entries supplied by SetTags are not dropped by a failed write.
	c.SetTags([]TagInfo{{Name: "Speed", TypeCode: TypeDINT}})
	if err := c.Write("Speed", int32(1)); err == nil {
		t.Fatal("missing tag write succeeded")
	}
	if _, ok := c.lookupTagInfo("Speed"); !ok {
		t.Fatal("catalog entry dropped")
	}
}

func TestStringArrayWritesUseNativeLayout(t *testing.T) {
	c, peer := hardeningPeer(t, 0)
	element := func(text string) []byte {
		out := make([]byte, 88)
		binary.LittleEndian.PutUint32(out, uint32(len(text)))
		copy(out[4:], text)
		return out
	}
	// Fits one request: Write Tag, count 3, 3 x 88 bytes.
	before := len(peer.requestLog())
	if err := c.Write("Names", []string{"a", "", "PLCIO"}); err != nil {
		t.Fatal(err)
	}
	log := peer.requestLog()[before:]
	want := append(append(element("a"), element("")...), element("PLCIO")...)
	if len(log) != 1 {
		t.Fatalf("%d requests", len(log))
	}
	if name, typ, count, data := splitWrite(t, log[0]); name != "Names" || !bytes.Equal(typ, []byte{0xa0, 0x02, 0xce, 0x0f}) || count != 3 || !bytes.Equal(data, want) {
		t.Fatalf("wrote %s % x count %d, %d bytes", name, typ, count, len(data))
	}
	// Too large for one unconnected request: Write Tag Fragmented on
	// element boundaries, every fragment carrying type, count and offset.
	texts := make([]string, 12)
	want = nil
	for i := range texts {
		texts[i] = strings.Repeat(string(rune('a'+i)), i+1)
		want = append(want, element(texts[i])...)
	}
	before = len(peer.requestLog())
	if err := c.Write("Names[4]", texts); err != nil {
		t.Fatal(err)
	}
	var got []byte
	for _, req := range peer.requestLog()[before:] {
		name, rest := fakeSymbol(req)
		if req[0] != 0x53 || name != "Names[4]" || !bytes.Equal(rest[:4], []byte{0xa0, 0x02, 0xce, 0x0f}) ||
			binary.LittleEndian.Uint16(rest[4:]) != 12 || int(binary.LittleEndian.Uint32(rest[6:])) != len(got) {
			t.Fatalf("fragment % x", req[:min(len(req), 40)])
		}
		if chunk := rest[10:]; len(chunk)%88 != 0 && len(got)+len(chunk) != len(want) {
			t.Fatalf("fragment of %d bytes splits an element", len(chunk))
		}
		got = append(got, rest[10:]...)
	}
	if !bytes.Equal(got, want) || len(peer.requestLog())-before < 2 {
		t.Fatalf("fragmented data %d bytes in %d requests", len(got), len(peer.requestLog())-before)
	}
	// Micro800 STRING array: packed length+chars elements, count N.
	before = len(peer.requestLog())
	if err := c.Write("test_string_array", []string{"a", "bc", ""}); err != nil {
		t.Fatal(err)
	}
	if name, typ, count, data := splitWrite(t, peer.requestLog()[before]); name != "test_string_array" ||
		!bytes.Equal(typ, []byte{0xda, 0}) || count != 3 || !bytes.Equal(data, []byte{1, 'a', 2, 'b', 'c', 0}) {
		t.Fatalf("Micro800 array: %s % x %d % x", name, typ, count, data)
	}
	// Rejections happen before anything is sent.
	before = len(peer.requestLog())
	for tag, value := range map[string][]string{
		"Names":             {"ok", strings.Repeat("x", 83)},
		"test_string_array": {"ok", strings.Repeat("x", 256)},
		"test_string":       {},
		"Employee_Data":     {"x"}, // Not a string type: not handled here.
	} {
		if err := c.Write(tag, value); err == nil {
			t.Fatalf("%s: accepted %d strings", tag, len(value))
		}
	}
	if err := c.Write("test_string_array", []string{"1", "2", "3", "4", "5", "6"}); err == nil {
		t.Fatal("more strings than elements accepted")
	}
	if err := c.Write("test_string_array", []string{strings.Repeat("x", 255), strings.Repeat("y", 255)}); err == nil {
		t.Fatal("oversized Micro800 string array accepted")
	}
	for _, req := range peer.requestLog()[before:] {
		if name, _ := fakeSymbol(req); name != "Employee_Data" {
			t.Fatalf("request sent for a rejected write: % x", req[:min(len(req), 24)])
		}
	}
}

// Type lookups made for writes must not change how reads size requests.
func TestReadsDoNotDependOnWriteTypeCache(t *testing.T) {
	var mu sync.Mutex
	var counts []uint16
	var handle func(req []byte) []byte
	handle = func(req []byte) []byte {
		switch req[0] {
		case 0x0a: // Unknown-size scalars are batched.
			return fakeMSP(req, handle)
		case 0x55:
			return append([]byte{0xd5, 0, 0, 0}, symbolEntry(1, "Arr", 0x2000|TypeDINT, [3]uint32{4, 0, 0})...)
		case 0x4d:
			return []byte{0xcd, 0, 0, 0}
		case 0x4e:
			return []byte{0xce, 0, 0, 0}
		case 0x4c:
			_, rest := fakeSymbol(req)
			n := binary.LittleEndian.Uint16(rest)
			mu.Lock()
			counts = append(counts, n)
			mu.Unlock()
			out := []byte{0xcc, 0, 0, 0, 0xc4, 0}
			for i := uint16(0); i < n; i++ {
				out = binary.LittleEndian.AppendUint32(out, uint32(10+i))
			}
			return out
		}
		t.Errorf("unexpected service %x", req[0])
		return nil
	}
	peer := newFakePeer(t, handle)
	c := peer.newClient(t, false)
	read := func() *TagValue {
		values, err := c.Read("Arr")
		if err != nil || len(values) != 1 || values[0].Error != nil {
			t.Fatalf("read %v %v", values, err)
		}
		return values[0]
	}
	first := read()
	if code, ok := c.ResolveTagType("Arr[2]"); !ok || code != TypeDINT {
		t.Fatalf("resolve %x %v", code, ok)
	}
	if err := c.Write("Arr[2].3", true); err != nil {
		t.Fatal(err)
	}
	if err := c.Write("Arr[2]", int32(7)); err != nil {
		t.Fatal(err)
	}
	second := read()
	if first.DataType != second.DataType || !bytes.Equal(first.Bytes, second.Bytes) || first.Count != second.Count ||
		c.getElementCount("Arr") != 1 || c.isArrayTag("Arr") {
		t.Fatalf("read changed after writes: %+v vs %+v", first, second)
	}
	// A catalog supplied with SetTags still sizes reads.
	c.SetTags([]TagInfo{{Name: "Arr", TypeCode: 0x2000 | TypeDINT, Dimensions: []int{4}}})
	if third := read(); third.Count != 4 || len(third.Bytes) != 16 {
		t.Fatalf("catalog read %+v", third)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(counts) != 3 || counts[0] != 1 || counts[1] != 1 || counts[2] != 4 {
		t.Fatalf("read element counts %v", counts)
	}
}

func TestGetArrayDimensionsReadsAttribute8AsDimensions(t *testing.T) {
	dims := map[byte][]uint32{1: {10, 0, 0}, 2: {2, 3, 0}, 3: {2, 3, 4}}
	peer := newFakePeer(t, func(req []byte) []byte {
		// Get Attribute Single: class 0x6B, 8-bit instance, attribute 8.
		if req[0] != 0x0e || !bytes.Equal(req[2:4], []byte{0x20, 0x6b}) || req[6] != 0x30 || req[7] != 8 {
			t.Errorf("unexpected request % x", req)
			return []byte{req[0] | 0x80, 0, 0x08, 0}
		}
		out := []byte{0x8e, 0, 0, 0}
		for _, d := range dims[req[5]] {
			out = binary.LittleEndian.AppendUint32(out, d)
		}
		return out
	})
	c := peer.newClient(t, false)
	for _, tc := range []struct {
		instance uint32
		code     uint16
		want     []int
	}{
		{1, 0x2000 | TypeDINT, []int{10}},
		{2, 0x4000 | TypeREAL, []int{2, 3}},
		{3, 0x6000 | TypeINT, []int{2, 3, 4}},
	} {
		got, err := c.plc.GetArrayDimensions(tc.instance, tc.code)
		if err != nil || !equalInts(got, tc.want) {
			t.Fatalf("instance %d: %v %v, want %v", tc.instance, got, err, tc.want)
		}
	}
	if got, err := c.plc.GetArrayDimensions(1, TypeDINT); got != nil || err != nil {
		t.Fatalf("scalar: %v %v", got, err)
	}
}

// A Go int written to an INT tag with no SetTags info must be encoded as INT
// (resolved from the controller), not guessed as DINT from the Go value.
func TestWriteGoIntUsesResolvedType(t *testing.T) {
	var mu sync.Mutex
	var wrote []byte
	peer := newFakePeer(t, func(req []byte) []byte {
		switch req[0] {
		case 0x55:
			out := []byte{0xd5, 0, 0, 0}
			return append(out, symbolEntry(11, "test_int", TypeINT, [3]uint32{})...)
		case 0x4d:
			mu.Lock()
			wrote = append([]byte(nil), req...)
			mu.Unlock()
			return []byte{0xcd, 0, 0, 0}
		}
		t.Errorf("unexpected service %x", req[0])
		return nil
	})
	c := peer.newClient(t, false)
	if err := c.Write("test_int", 777); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(wrote) < 2 {
		t.Fatal("no write sent")
	}
	off := 2 + 2*int(wrote[1])
	typ := binary.LittleEndian.Uint16(wrote[off:])
	data := wrote[off+4:]
	if typ != TypeINT || len(data) != 2 || binary.LittleEndian.Uint16(data) != 777 {
		t.Fatalf("wrote type 0x%04X data %x, want INT 777", typ, data)
	}
}
