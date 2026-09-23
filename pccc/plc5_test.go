package pccc

import (
	"bytes"
	"encoding/binary"
	"math"
	"strconv"
	"sync"
	"testing"
)

// ---- Pure encoders/decoders against the DF1 manual (1770-6.5.16) ----

// PLC-5 logical binary addresses (p. 13-11/13-12). B3:300 is the manual's own
// example (mask 06: level 1 defaulted; 03; FF 2C 01). The manual's N7:30 and
// T4:2.ACC examples also encode level 1 (= 0) explicitly (mask 07/0F); we
// leave it at its default like libplctag's plc5_encode_address (mask 06/0E),
// which the manual's mask rules make equivalent.
func TestPLC5LogicalBinaryAddress(t *testing.T) {
	cases := []struct{ addr, want string }{
		{"B3:300", "06 03 FF 2C 01"},
		{"N7:30", "06 07 1E"},
		{"T4:2.ACC", "0E 04 02 02"},
		{"PD12:10.2", "0E 0C 0A 02"},
		{"T4:2.DN", "0E 04 02 00"},
		{"T4:2/13", "0E 04 02 00"}, // bit of a T element: the control word
		{"C5:0.PRE", "0E 05 00 01"},
		{"N300:2", "06 FF 2C 01 02"},
		{"N7:254", "06 07 FE"},
		{"N7:255", "06 07 FF FF 00"},
		{"I:010", "06 01 08"},    // octal word 010 = 8
		{"O:017/17", "06 00 0F"}, // octal
		{"S:1/5", "06 02 01"},
		{"ST9:0", "06 09 00"},
	}
	for _, tc := range cases {
		addr, err := ParseAddressFor(tc.addr, TypePLC5)
		if err != nil {
			t.Fatalf("%s: %v", tc.addr, err)
		}
		if got := appendPLC5Address(nil, addr); !bytes.Equal(got, unhex(t, tc.want)) {
			t.Errorf("%s: % X, want %s", tc.addr, got, tc.want)
		}
	}
}

// Type/data parameter (p. 7-28/7-29, examples p. 7-36/7-37).
func TestTypeDataParameter(t *testing.T) {
	enc := []struct {
		id, size uint32
		want     string
	}{
		{TypeIDInteger, 2, "42"},      // example 1: integer, 2 bytes
		{TypeIDInteger, 3, "43"},      // case 1
		{TypeIDFloat, 4, "94 08"},     // ID 8 does not fit 3 bits: 1 ID byte follows
		{TypeIDArray, 7, "97 09"},     // example 2 flag + ID
		{TypeIDArray, 21, "99 09 15"}, // example 3 value, shortest size form
		{TypeIDByteString, 84, "39 54"},
		{TypeIDTimer, 6, "56"},
		{TypeIDBCD, 300, "9A 10 2C 01"},
	}
	for _, tc := range enc {
		got := appendTypeData(nil, tc.id, tc.size)
		if !bytes.Equal(got, unhex(t, tc.want)) {
			t.Errorf("encode(%d,%d) = % X, want %s", tc.id, tc.size, got, tc.want)
		}
		id, size, n, err := parseTypeData(got)
		if err != nil || id != tc.id || size != tc.size || n != len(got) {
			t.Errorf("round trip %s: %d %d %d %v", tc.want, id, size, n, err)
		}
	}
	// The manual's three equivalent encodings of ID 4, size 3 (p. 7-36).
	for _, s := range []string{"43", "49 03", "4A 03 00"} {
		id, size, n, err := parseTypeData(unhex(t, s))
		if err != nil || id != 4 || size != 3 || n != len(unhex(t, s)) {
			t.Errorf("%s: id %d size %d n %d err %v", s, id, size, n, err)
		}
	}
	for _, bad := range []string{"", "49", "9A 09 15", "8F", "F8 00 00 00 00 00 00 00 00"} {
		if _, _, _, err := parseTypeData(unhex(t, bad)); err == nil {
			t.Errorf("%q: truncated/oversized parameter accepted", bad)
		}
	}
}

func TestDecodePLC5TypedData(t *testing.T) {
	// Example 2 (p. 7-37): array, descriptor 0x42, integers 0, -2, 255.
	td, err := decodePLC5TypedData(unhex(t, "97 09 42 00 00 FE FF FF 00"))
	if err != nil {
		t.Fatal(err)
	}
	if td.elemType != TypeIDInteger || td.elemSize != 2 || !bytes.Equal(td.data, unhex(t, "00 00 FE FF FF 00")) || !bytes.Equal(td.param, unhex(t, "97 09 42")) {
		t.Fatalf("example 2: %+v", td)
	}
	// Example 3 (p. 7-37): 2-byte extended size 0x0015 = descriptor + 20 bytes.
	ex3 := append(unhex(t, "9A 09 15 00 42"), make([]byte, 20)...)
	if td, err = decodePLC5TypedData(ex3); err != nil || len(td.data) != 20 || td.elemSize != 2 {
		t.Fatalf("example 3: %+v %v", td, err)
	}
	// A single scalar (example 1 parameter).
	if td, err = decodePLC5TypedData(unhex(t, "42 2A 00")); err != nil || !bytes.Equal(td.data, []byte{0x2A, 0}) {
		t.Fatalf("scalar: %+v %v", td, err)
	}
	for _, bad := range []string{
		"97 09 42 00 00 FE FF FF",       // array one byte short
		"97 09 42 00 00 FE FF FF 00 00", // trailing byte
		"42 2A",                         // scalar short
		"42 2A 00 00",                   // scalar long
		"93 09 40 00 00",                // zero-size elements
		"93 09 92 09 00",                // nested array
	} {
		if td, err := decodePLC5TypedData(unhex(t, bad)); err == nil {
			t.Errorf("%s accepted: %+v", bad, td)
		}
	}
}

func TestPLC5AddressParsing(t *testing.T) {
	ok := []struct {
		addr     string
		pt       PLCType
		elem     uint16
		bit      int
		fileType byte
	}{
		{"I:010/17", TypePLC5, 8, 15, FileTypeInput},
		{"I:000/00", TypePLC5, 0, 0, FileTypeInput},
		{"O:277/7", TypePLC5, 0277, 7, FileTypeOutput},
		{"I:010/15", TypeSLC500, 10, 15, FileTypeInput},
		{"I:010/15", TypeMicroLogix, 10, 15, FileTypeInput},
		{"N7:10/15", TypePLC5, 10, 15, FileTypeInteger}, // only I/O is octal
		{"B3:19/9", TypePLC5, 19, 9, FileTypeBinary},
		{"S:18/9", TypePLC5, 18, 9, FileTypeStatus},
	}
	for _, tc := range ok {
		a, err := ParseAddressFor(tc.addr, tc.pt)
		if err != nil {
			t.Fatalf("%s (%s): %v", tc.addr, tc.pt, err)
		}
		if a.Element != tc.elem || a.BitNumber != tc.bit || a.FileType != tc.fileType {
			t.Errorf("%s (%s): elem %d bit %d type %02X", tc.addr, tc.pt, a.Element, a.BitNumber, a.FileType)
		}
	}
	bad := []struct {
		addr string
		pt   PLCType
	}{
		{"I:018", TypePLC5},    // 8 is not octal
		{"I:009/1", TypePLC5},  // 9 is not octal
		{"I:010/18", TypePLC5}, // bit digit 8
		{"I:010/20", TypePLC5}, // octal 20 = 16: out of range
		{"L9:0", TypePLC5},     // no L files on PLC-5
		{"I:010/16", TypeSLC500},
		{"N70000:0", TypeSLC500}, // used to wrap to N4464
		{"O-1:0", TypeSLC500},    // used to fall back to the default file
		{"N+7:0", TypeSLC500},
		{"N7:+1", TypeSLC500},
		{"N7:1/+3", TypeSLC500},
		{"N7:1/-0", TypeSLC500},
	}
	for _, tc := range bad {
		if a, err := ParseAddressFor(tc.addr, tc.pt); err == nil {
			t.Errorf("%s (%s) accepted: %+v", tc.addr, tc.pt, a)
		}
	}
	if a, err := ParseAddressFor("L9:0", TypeSLC500); err != nil || a.FileType != FileTypeLong {
		t.Fatalf("L9:0 on SLC: %v", err)
	}
	if a, _ := ParseAddress("T4:0.DN"); !a.HasSubElement {
		t.Fatal("T4:0.DN must record a sub-element")
	}
	if a, _ := ParseAddress("N7:0"); a.HasSubElement {
		t.Fatal("N7:0 has no sub-element")
	}
}

// ---- Simulated PLC-5 ----

// plc5SimFile is one data file of the simulated PLC-5: its element
// descriptor as the processor would report it and its memory exactly as sent
// on the wire (floats high word first, strings byte-swapped).
type plc5SimFile struct {
	desc     []byte
	elemSize int
	mem      []byte
}

type plc5Sim struct {
	t          *testing.T
	mu         sync.Mutex
	files      map[int]*plc5SimFile
	scalar     bool // answer single-element reads with a scalar parameter
	twoByteLen bool // use the manual's 2-byte extended array size form
}

func newPLC5Sim(t *testing.T) *plc5Sim {
	f := func(desc string, elemSize, elems int) *plc5SimFile {
		return &plc5SimFile{desc: unhex(t, desc), elemSize: elemSize, mem: make([]byte, elemSize*elems)}
	}
	return &plc5Sim{t: t, files: map[int]*plc5SimFile{
		0: f("42", 2, 64),    // O
		1: f("42", 2, 64),    // I
		2: f("42", 2, 64),    // S
		3: f("42", 2, 400),   // B3
		4: f("56", 6, 10),    // T4
		5: f("66", 6, 10),    // C5
		6: f("76", 6, 10),    // R6
		7: f("42", 2, 400),   // N7
		8: f("94 08", 4, 10), // F8
		9: f("39 54", 84, 2), // ST9 (descriptor unknown on real PLC-5; any is echoed)
	}}
}

func lbDecode(b []byte) (levels [4]int, present [4]bool, n int) {
	mask := b[0]
	n = 1
	for i := 0; i < 4; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		present[i] = true
		if b[n] == 0xFF {
			levels[i] = int(binary.LittleEndian.Uint16(b[n+1:]))
			n += 3
		} else {
			levels[i] = int(b[n])
			n++
		}
	}
	return
}

// locate returns the file, the byte offset and the item size addressed.
func (s *plc5Sim) locate(b []byte) (*plc5SimFile, int, int, bool, int) {
	levels, present, n := lbDecode(b)
	f := s.files[levels[1]]
	if f == nil {
		return nil, 0, 0, false, n
	}
	off := levels[2] * f.elemSize
	if present[3] {
		return f, off + 2*levels[3], 2, true, n
	}
	return f, off, f.elemSize, false, n
}

func (s *plc5Sim) param(f *plc5SimFile, word bool, count int) []byte {
	desc := f.desc
	if word {
		desc = []byte{0x42}
	}
	size := len(desc) + count*f.elemSize
	if word {
		size = len(desc) + count*2
	}
	if s.scalar && count == 1 {
		return append([]byte(nil), desc...)
	}
	switch {
	case s.twoByteLen:
		return append([]byte{0x9A, 0x09, byte(size), byte(size >> 8)}, desc...)
	case size <= 7:
		return append([]byte{0x90 | byte(size), 0x09}, desc...)
	default:
		return append([]byte{0x99, 0x09, byte(size)}, desc...)
	}
}

func (s *plc5Sim) handle(cmd []byte) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cmd[0] != CmdTypedCommand {
		s.t.Errorf("unexpected CMD % X", cmd)
		return pcccReply(cmd, 0x10)
	}
	body := cmd[5:]
	switch cmd[4] {
	case FncTypedRead, FncTypedWrite:
		offset := binary.LittleEndian.Uint16(body[0:])
		total := int(binary.LittleEndian.Uint16(body[2:]))
		f, off, itemSize, word, n := s.locate(body[4:])
		rest := body[4+n:]
		if offset != 0 || f == nil {
			return pcccReply(cmd, 0xF0, 0x06)
		}
		if cmd[4] == FncTypedRead {
			size := int(binary.LittleEndian.Uint16(rest))
			if size != total || len(rest) != 2 {
				s.t.Errorf("typed read: total %d size %d rest % X", total, size, rest)
			}
			if off+size*itemSize > len(f.mem) {
				return pcccReply(cmd, 0xF0, 0x07)
			}
			out := s.param(f, word, size)
			return pcccReply(cmd, 0, append(out, f.mem[off:off+size*itemSize]...)...)
		}
		want := s.param(f, word, total)
		if !bytes.HasPrefix(rest, want) || len(rest) != len(want)+total*itemSize {
			return pcccReply(cmd, 0xF0, 0x17) // type mismatch
		}
		copy(f.mem[off:], rest[len(want):])
		return pcccReply(cmd, 0)
	case FncReadModifyWrite:
		f, off, itemSize, _, n := s.locate(body)
		if f == nil || itemSize != 2 || len(body) != n+4 {
			return pcccReply(cmd, 0xF0, 0x06)
		}
		and := binary.LittleEndian.Uint16(body[n:])
		or := binary.LittleEndian.Uint16(body[n+2:])
		w := binary.LittleEndian.Uint16(f.mem[off:])
		binary.LittleEndian.PutUint16(f.mem[off:], w&and|or)
		return pcccReply(cmd, 0)
	default:
		s.t.Errorf("PLC-5 sent SLC/unknown function 0x%02X", cmd[4])
		return pcccReply(cmd, 0x10)
	}
}

func (s *plc5Sim) word(file, elem int) uint16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return binary.LittleEndian.Uint16(s.files[file].mem[elem*s.files[file].elemSize:])
}

func (s *plc5Sim) setWord(file, off int, v uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	binary.LittleEndian.PutUint16(s.files[file].mem[off:], v)
}

func (s *plc5Sim) setBytes(file, off int, b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy(s.files[file].mem[off:], b)
}

func (s *plc5Sim) bytesAt(file, off, n int) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.files[file].mem[off:off+n]...)
}

func startPLC5(t *testing.T) (*Client, *pcccPeer, *plc5Sim) {
	sim := newPLC5Sim(t)
	client, peer := startPCCCPeer(t, TypePLC5, sim.handle)
	sim.setWord(7, 0, 0x1234)                              // N7:0
	sim.setWord(7, 300*2, 0xFFFE)                          // N7:300 = -2
	sim.setBytes(8, 4, []byte{0xC0, 0x3F, 0x00, 0x00})     // F8:1 = 1.5, high word first
	sim.setWord(3, 2*2, 0x0010)                            // B3:2/4
	sim.setBytes(4, 2*6, []byte{0x00, 0xA0, 100, 0, 7, 0}) // T4:2 EN+DN, PRE 100, ACC 7
	sim.setWord(5, 0, 0x2000)                              // C5:0.DN
	sim.setWord(6, 6+4, 3)                                 // R6:1.POS
	sim.setBytes(9, 0, []byte{5, 0, 'E', 'H', 'L', 'L', 0, 'O'})
	sim.setWord(1, 8*2, 0x8000) // I:010/17
	sim.setWord(0, 15*2, 0x0102)
	sim.setWord(2, 1*2, 0x0020)
	return client, peer, sim
}

// ---- End to end ----

func TestPLC5TypedReads(t *testing.T) {
	for _, mode := range []string{"array", "scalar", "two-byte size"} {
		client, peer, sim := startPLC5(t)
		sim.scalar = mode == "scalar"
		sim.twoByteLen = mode == "two-byte size"
		cases := []struct {
			addr string
			req  string // PCCC bytes after TNS
			want interface{}
		}{
			{"N7:0", "68 00 00 01 00 06 07 00 01 00", int16(0x1234)},
			{"N7:300", "68 00 00 01 00 06 07 FF 2C 01 01 00", int16(-2)},
			{"F8:1", "68 00 00 01 00 06 08 01 01 00", float32(1.5)},
			{"B3:2/4", "68 00 00 01 00 06 03 02 01 00", true},
			{"T4:2.ACC", "68 00 00 01 00 0E 04 02 02 01 00", int16(7)},
			{"T4:2.DN", "68 00 00 01 00 0E 04 02 00 01 00", true},
			{"C5:0.DN", "68 00 00 01 00 0E 05 00 00 01 00", true},
			{"R6:1.POS", "68 00 00 01 00 0E 06 01 02 01 00", int16(3)},
			{"ST9:0", "68 00 00 01 00 06 09 00 01 00", "HELLO"},
			{"I:010/17", "68 00 00 01 00 06 01 08 01 00", true},
			{"I:010/16", "68 00 00 01 00 06 01 08 01 00", false},
			{"O:017", "68 00 00 01 00 06 00 0F 01 00", int16(0x0102)},
			{"S:1/5", "68 00 00 01 00 06 02 01 01 00", true},
		}
		for _, tc := range cases {
			vals, err := client.Read(tc.addr)
			if err != nil || vals[0].Error != nil {
				t.Fatalf("%s %s: %v %v", mode, tc.addr, err, vals[0].Error)
			}
			if vals[0].Value != tc.want {
				t.Errorf("%s %s = %#v, want %#v", mode, tc.addr, vals[0].Value, tc.want)
			}
			log := peer.log()
			if got := log[len(log)-1][4:]; !bytes.Equal(got, unhex(t, tc.req)) {
				t.Errorf("%s %s request % X, want %s", mode, tc.addr, got, tc.req)
			}
		}
		vals, _ := client.Read("T4:2")
		m, ok := vals[0].Value.(map[string]interface{})
		if !ok || m["EN"] != true || m["DN"] != true || m["TT"] != false || m["PRE"] != int16(100) || m["ACC"] != int16(7) {
			t.Errorf("%s T4:2 = %#v (%v)", mode, vals[0].Value, vals[0].Error)
		}
	}
}

func TestPLC5ReadAddressNBatch(t *testing.T) {
	client, peer, sim := startPLC5(t)
	sim.setBytes(8, 0, []byte{0x80, 0x3F, 0, 0, 0xC0, 0x3F, 0, 0}) // F8:0=1.0 F8:1=1.5
	addr, _ := ParseAddressFor("N7:0", TypePLC5)
	tag, err := client.PLC().ReadAddressN(addr, 3)
	if err != nil || !bytes.Equal(tag.Bytes, []byte{0x34, 0x12, 0, 0, 0, 0}) {
		t.Fatalf("N7:0 x3: % X %v", tag, err)
	}
	if got := peer.log()[0][4:]; !bytes.Equal(got, unhex(t, "68 00 00 03 00 06 07 00 03 00")) {
		t.Fatalf("request % X", got)
	}
	addr, _ = ParseAddressFor("F8:0", TypePLC5)
	tag, err = client.PLC().ReadAddressN(addr, 2)
	if err != nil {
		t.Fatal(err)
	}
	if f0, f1 := math.Float32frombits(binary.LittleEndian.Uint32(tag.Bytes)), math.Float32frombits(binary.LittleEndian.Uint32(tag.Bytes[4:])); f0 != 1.0 || f1 != 1.5 {
		t.Fatalf("floats %v %v (% X)", f0, f1, tag.Bytes)
	}
}

// PLC-5 logical binary addresses carry no file type, so the typed reply is
// the only check that the file is what the address letter claims.
func TestPLC5TypeMismatchRejected(t *testing.T) {
	client, _, _ := startPLC5(t)
	for _, a := range []string{"F7:0", "N8:0", "N4:0", "T5:0", "C4:0", "R4:0", "ST7:0", "N9:0", "T7:0"} {
		vals, _ := client.Read(a)
		if vals[0].Error == nil {
			t.Errorf("%s decoded from the wrong file type: %#v", a, vals[0].Value)
		}
	}
	addr, _ := ParseAddressFor("N7:0", TypePLC5)
	if _, err := client.PLC().ReadAddressN(addr, 0); err == nil {
		t.Fatal("count 0 accepted")
	}
}

func TestPLC5TypedWrites(t *testing.T) {
	client, peer, sim := startPLC5(t)
	cases := []struct {
		addr  string
		value interface{}
		write string // the FNC 67 request after TNS
		file  int
		off   int
		mem   string
	}{
		{"N7:5", 42, "67 00 00 01 00 06 07 05 93 09 42 2A 00", 7, 10, "2A 00"},
		{"F8:1", float32(2.5), "67 00 00 01 00 06 08 01 96 09 94 08 20 40 00 00", 8, 4, "20 40 00 00"},
		{"T4:2.PRE", 250, "67 00 00 01 00 0E 04 02 01 93 09 42 FA 00", 4, 14, "FA 00"},
		{"O:010", 0x55, "67 00 00 01 00 06 00 08 93 09 42 55 00", 0, 16, "55 00"},
	}
	for _, tc := range cases {
		n := len(peer.log())
		if err := client.Write(tc.addr, tc.value); err != nil {
			t.Fatalf("%s: %v", tc.addr, err)
		}
		log := peer.log()[n:]
		if len(log) != 2 || log[0][4] != FncTypedRead {
			t.Fatalf("%s: requests % X, want type pre-read then write", tc.addr, log)
		}
		if got := log[1][4:]; !bytes.Equal(got, unhex(t, tc.write)) {
			t.Errorf("%s: write % X\nwant %s", tc.addr, got, tc.write)
		}
		want := unhex(t, tc.mem)
		if got := sim.bytesAt(tc.file, tc.off, len(want)); !bytes.Equal(got, want) {
			t.Errorf("%s: memory % X, want % X", tc.addr, got, want)
		}
	}
	vals, _ := client.Read("F8:1")
	if vals[0].Value != float32(2.5) {
		t.Fatalf("F8:1 read back %v", vals[0].Value)
	}

	// ST: the parameter is echoed from the pre-read; LEN + byte-swapped chars.
	if err := client.Write("ST9:1", "AB"); err != nil {
		t.Fatal(err)
	}
	log := peer.log()
	w := log[len(log)-1][4:]
	if !bytes.HasPrefix(w, unhex(t, "67 00 00 01 00 06 09 01 99 09 56 39 54 02 00 42 41 00")) || len(w) != 13+84 {
		t.Fatalf("ST write % X", w)
	}
	if vals, _ := client.Read("ST9:1"); vals[0].Value != "AB" {
		t.Fatalf("ST9:1 = %#v", vals[0].Value)
	}

	// Writing a float to an integer file: the pre-read shows a 2-byte integer,
	// the write is refused locally and never sent.
	n := len(peer.log())
	if err := client.Write("F7:0", 1.0); err == nil {
		t.Fatal("float written into an integer file")
	}
	for _, req := range peer.log()[n:] {
		if req[4] == FncTypedWrite {
			t.Fatal("mismatched typed write was sent")
		}
	}
	if sim.word(7, 0) != 0x1234 {
		t.Fatal("N7:0 changed")
	}
}

func TestPLC5BitWriteReadModifyWrite(t *testing.T) {
	client, peer, sim := startPLC5(t)
	sim.setWord(7, 0, 0xA5A5)
	cases := []struct {
		addr  string
		value bool
		req   string
	}{
		{"N7:0/0", false, "26 06 07 00 FE FF 00 00"},
		{"N7:0/1", true, "26 06 07 00 FF FF 02 00"},
		{"B3:300/15", true, "26 06 03 FF 2C 01 FF FF 00 80"},
		{"T4:2.DN", false, "26 0E 04 02 00 FF DF 00 00"},
		{"C5:0.CU", true, "26 0E 05 00 00 FF FF 00 80"},
		{"O:010/17", true, "26 06 00 08 FF FF 00 80"},
	}
	for i, tc := range cases {
		if err := client.Write(tc.addr, tc.value); err != nil {
			t.Fatalf("%s: %v", tc.addr, err)
		}
		log := peer.log()
		if len(log) != i+1 {
			t.Fatalf("%s: %d requests, want exactly one RMW per bit write", tc.addr, len(log)-i)
		}
		if got := log[i][4:]; !bytes.Equal(got, unhex(t, tc.req)) {
			t.Errorf("%s: % X, want %s", tc.addr, got, tc.req)
		}
	}
	// Neighbouring bits keep their values: 0xA5A5 -bit0 +bit1 = 0xA5A6.
	if w := sim.word(7, 0); w != 0xA5A6 {
		t.Fatalf("N7:0 = 0x%04X, want 0xA5A6", w)
	}
	if w := binary.LittleEndian.Uint16(sim.bytesAt(4, 12, 2)); w != 0x8000 {
		t.Fatalf("T4:2 control word = 0x%04X, want EN only", w)
	}

	// The processor applies each mask to its current word, so concurrent bit
	// writes cannot lose each other's updates.
	sim.setWord(7, 2, 0)
	var wg sync.WaitGroup
	for bit := 0; bit < 16; bit++ {
		wg.Add(1)
		go func(bit int) {
			defer wg.Done()
			if err := client.Write("N7:1/"+strconv.Itoa(bit), true); err != nil {
				t.Error(err)
			}
		}(bit)
	}
	wg.Wait()
	if w := sim.word(7, 1); w != 0xFFFF {
		t.Fatalf("N7:1 = 0x%04X after 16 concurrent bit sets", w)
	}
	for _, a := range []string{"F8:0/1", "ST9:0/1"} {
		if err := client.Write(a, true); err == nil {
			t.Errorf("%s: bit write accepted", a)
		}
	}
}
