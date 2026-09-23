package pccc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yatesdr/plcio/eip"
)

// pcccPeer is an in-process EtherNet/IP peer on a loopback ephemeral port. It
// unwraps each CIP Execute PCCC request and hands the PCCC command bytes
// ([CMD] [STS] [TNS] [FNC] ...) to handle, which returns the PCCC reply
// ([CMD|0x40] [STS] [TNS] [data...]). A nil reply drops the connection.
type pcccPeer struct {
	mu       sync.Mutex
	requests [][]byte
}

func (p *pcccPeer) log() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]byte(nil), p.requests...)
}

func startPCCCPeer(t *testing.T, plcType PLCType, handle func(cmd []byte) []byte) (*Client, *pcccPeer) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	peer := &pcccPeer{}
	var wg sync.WaitGroup
	var connsMu sync.Mutex
	var conns []net.Conn
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connsMu.Lock()
			conns = append(conns, conn)
			connsMu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				servePCCC(t, conn, peer, handle)
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		connsMu.Lock()
		for _, c := range conns {
			c.Close()
		}
		connsMu.Unlock()
		wg.Wait()
	})

	port := listener.Addr().(*net.TCPAddr).Port
	conn := eip.NewEipClientWithPort("127.0.0.1", uint16(port))
	conn.SetTimeout(2 * time.Second)
	if err := conn.Connect(); err != nil {
		t.Fatal(err)
	}
	client := &Client{plc: &PLC{IpAddress: "127.0.0.1", Connection: conn, PLCType: plcType, vendorID: 1, serialNum: 0x12345678}}
	t.Cleanup(client.Close)
	return client, peer
}

func servePCCC(t *testing.T, conn net.Conn, peer *pcccPeer, handle func([]byte) []byte) {
	for {
		frame, err := eip.ReadFrame(conn)
		if err != nil {
			return
		}
		var reply *eip.Frame
		switch frame.Command {
		case 0x65: // RegisterSession
			reply = frame.Reply(0, []byte{1, 0, 0, 0})
			reply.SessionHandle = 0x1234
		case 0x66: // UnRegisterSession
			return
		case 0x6f: // SendRRData
			cpf, err := eip.ParseRRData(frame.Data)
			if err != nil {
				t.Error(err)
				return
			}
			packet, err := eip.ParseEipCommonPacket(cpf)
			if err != nil || len(packet.Items) != 2 {
				t.Errorf("CPF %v", err)
				return
			}
			req := packet.Items[1].Data
			if len(req) < 2 || req[0] != CipSvcExecutePCCC {
				t.Errorf("not an Execute PCCC request: % x", req)
				return
			}
			start := 2 + int(req[1])*2
			requester := req[start : start+int(RequesterIDLength)]
			cmd := append([]byte(nil), req[start+int(RequesterIDLength):]...)
			peer.mu.Lock()
			peer.requests = append(peer.requests, cmd)
			peer.mu.Unlock()
			pcccReply := handle(cmd)
			if pcccReply == nil {
				return
			}
			cip := append([]byte{CipSvcExecutePCCCReply, 0, 0, 0}, requester...)
			cip = append(cip, pcccReply...)
			data := []byte{0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 0, 0, 0xb2, 0, 0, 0}
			binary.LittleEndian.PutUint16(data[14:16], uint16(len(cip)))
			reply = frame.Reply(0, append(data, cip...))
		default:
			t.Errorf("unexpected encapsulation command 0x%x", frame.Command)
			return
		}
		if _, err := conn.Write(reply.Bytes()); err != nil {
			return
		}
	}
}

// pcccReply builds [CMD|0x40] [STS] [TNS echoed from cmd] [data...].
func pcccReply(cmd []byte, sts byte, data ...byte) []byte {
	return append([]byte{cmd[0] | 0x40, sts, cmd[2], cmd[3]}, data...)
}

// An SLC bit write must be one Protected Typed Logical Write with Mask
// (FNC 0xAB) carrying a one-bit mask and the value, with no preceding read:
// [0F 00 TNS AB size=02 file type elem sub mask:2 value:2] (pycomm3
// SLC_FNC_WRITE/writeable_value, libplctag slc_tag_write_bit_start).
func TestBitWriteUsesMaskedWrite(t *testing.T) {
	client, peer := startPCCCPeer(t, TypeSLC500, func(cmd []byte) []byte {
		return pcccReply(cmd, 0)
	})
	cases := []struct {
		addr  string
		value interface{}
		want  []byte // bytes after the TNS
	}{
		{"B3:0/5", true, []byte{0xAB, 0x02, 0x03, 0x85, 0x00, 0x00, 0x20, 0x00, 0x20, 0x00}},
		{"B3:0/5", false, []byte{0xAB, 0x02, 0x03, 0x85, 0x00, 0x00, 0x20, 0x00, 0x00, 0x00}},
		{"N7:300/15", 1, []byte{0xAB, 0x02, 0x07, 0x89, 0xFF, 0x2C, 0x01, 0x00, 0x00, 0x80, 0x00, 0x80}},
		{"C5:2.CU", true, []byte{0xAB, 0x02, 0x05, 0x87, 0x02, 0x00, 0x00, 0x80, 0x00, 0x80}},
		{"S:1/5", true, []byte{0xAB, 0x02, 0x02, 0x84, 0x01, 0x00, 0x20, 0x00, 0x20, 0x00}},
	}
	for i, tc := range cases {
		if err := client.Write(tc.addr, tc.value); err != nil {
			t.Fatalf("%s: %v", tc.addr, err)
		}
		reqs := peer.log()
		if len(reqs) != i+1 {
			t.Fatalf("%s: %d requests sent, want exactly one per bit write (no read-back)", tc.addr, len(reqs)-i)
		}
		got := reqs[i]
		if got[0] != CmdTypedCommand || got[1] != 0 || !bytes.Equal(got[4:], tc.want) {
			t.Errorf("%s: request % X, want 0F 00 <tns> % X", tc.addr, got, tc.want)
		}
	}
}

// The 0xAB mask is 16 bits (libplctag rejects other sizes), so a bit write
// to a 32-bit L file must fail rather than write 2 of 4 bytes, and a bit
// write to an F file is meaningless.
func TestBitWriteRejectsLongAndFloat(t *testing.T) {
	client, peer := startPCCCPeer(t, TypeSLC500, func(cmd []byte) []byte {
		return pcccReply(cmd, 0, 0, 0, 0, 0)
	})
	for _, addr := range []string{"L9:0/3", "F8:0/1", "ST10:0/1"} {
		if err := client.Write(addr, true); err == nil {
			t.Errorf("%s: bit write succeeded, want error", addr)
		}
	}
	if n := len(peer.log()); n != 0 {
		t.Fatalf("%d requests sent for rejected bit writes", n)
	}
}

// SLC ST elements are LEN + 82 chars stored byte-swapped per 16-bit word
// (pycomm3 PCCC_STRING / _slc_string_swap).
func TestSTStringByteSwap(t *testing.T) {
	plc := make([]byte, ElementSizeString)
	copy(plc, []byte{0x05, 0x00, 'E', 'H', 'L', 'L', 0x00, 'O'})

	addr, err := ParseAddress("ST10:0")
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeValue(addr, plc); got != "HELLO" {
		t.Fatalf("decode = %q, want %q", got, "HELLO")
	}

	enc, err := encodeValue(addr, "HELLO")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(enc, plc) {
		t.Fatalf("encode = % X, want % X", enc, plc)
	}

	even, _ := encodeString("AB")
	if !bytes.Equal(even[:4], []byte{0x02, 0x00, 'B', 'A'}) {
		t.Fatalf("encode AB = % X", even[:4])
	}

	full := strings.Repeat("x", 82)
	if enc, err := encodeString(full); err != nil || len(enc) != ElementSizeString {
		t.Fatalf("82 chars: len %d err %v", len(enc), err)
	}
	if _, err := encodeString(full + "y"); err == nil {
		t.Fatal("83-char string accepted, want error (no silent truncation)")
	}
}

func TestEncodeRangeChecks(t *testing.T) {
	cases := []struct {
		addr  string
		value interface{}
		want  []byte // nil means an error is expected
	}{
		{"N7:0", 70000, nil},
		{"N7:0", int64(-32769), nil},
		{"N7:0", 65536, nil},
		{"N7:0", int64(-32768), []byte{0x00, 0x80}},
		{"B3:0", 65535, []byte{0xFF, 0xFF}},
		{"S:1", uint16(0xFFFF), []byte{0xFF, 0xFF}},
		{"N7:0", 1.5, nil},
		{"N7:0", 42.0, []byte{42, 0}},
		{"T4:0.PRE", -1, nil},
		{"T4:0.ACC", 32768, nil},
		{"T4:0.PRE", 32767, []byte{0xFF, 0x7F}},
		{"C5:0.PRE", -5, []byte{0xFB, 0xFF}},
		{"C5:0.ACC", 40000, nil},
		{"L9:0", int64(1) << 31, nil},
		{"L9:0", int64(-1) << 31, []byte{0, 0, 0, 0x80}},
		{"L9:0", uint32(0xFFFFFFFF), []byte{0xFF, 0xFF, 0xFF, 0xFF}},
	}
	for _, tc := range cases {
		addr, err := ParseAddress(tc.addr)
		if err != nil {
			t.Fatal(err)
		}
		got, err := encodeValue(addr, tc.value)
		if tc.want == nil {
			if err == nil {
				t.Errorf("%s <- %v: encoded % X, want range error", tc.addr, tc.value, got)
			}
			continue
		}
		if err != nil || !bytes.Equal(got, tc.want) {
			t.Errorf("%s <- %v: % X, %v; want % X", tc.addr, tc.value, got, err, tc.want)
		}
	}
}

func TestReplyTNSMustMatch(t *testing.T) {
	if _, err := parsePCCCReadResponse([]byte{0x4F, 0x00, 0x02, 0x00, 0x2A, 0x00}, 0x0001); err == nil {
		t.Fatal("read reply with TNS 0x0002 accepted for request TNS 0x0001")
	}
	if err := parsePCCCWriteResponse([]byte{0x4F, 0x00, 0x02, 0x00}, 0x0001); err == nil {
		t.Fatal("write reply with wrong TNS accepted")
	}
	if data, err := parsePCCCReadResponse([]byte{0x4F, 0x00, 0x01, 0x00, 0x2A, 0x00}, 0x0001); err != nil || !bytes.Equal(data, []byte{0x2A, 0x00}) {
		t.Fatalf("matching reply: % X %v", data, err)
	}

	client, _ := startPCCCPeer(t, TypeSLC500, func(cmd []byte) []byte {
		stale := append([]byte(nil), cmd...)
		stale[2]++ // reply to some other transaction
		return pcccReply(stale, 0, 0x2A, 0x00)
	})
	values, _ := client.Read("N7:0")
	if len(values) != 1 || values[0].Error == nil {
		t.Fatalf("stale reply decoded as value: %+v", values[0])
	}
}

func TestStatusErrorLocalAndExtended(t *testing.T) {
	err := PCCCStatusError(0x02, 0)
	if strings.Contains(err.Error(), "Success") || !strings.HasPrefix(err.Error(), "PCCC error: ") || !strings.HasSuffix(err.Error(), "(STS=0x02)") {
		t.Fatalf("local STS 0x02: %q", err)
	}
	if got := PCCCStatusError(0x10, 0).Error(); got != "PCCC error: Illegal Command or Format (STS=0x10)" {
		t.Fatalf("STS 0x10: %q", got)
	}
	if got := PCCCStatusError(0xF0, 0x07).Error(); got != "PCCC error: Extended Status (STS=0xF0), extended: File is wrong size (EXT_STS=0x07)" {
		t.Fatalf("EXT STS: %q", got)
	}

	client, _ := startPCCCPeer(t, TypeSLC500, func(cmd []byte) []byte {
		return pcccReply(cmd, 0xF0, 0x06)
	})
	values, _ := client.Read("N7:0")
	var se *StatusError
	if len(values) != 1 || !errors.As(values[0].Error, &se) || se.STS != 0xF0 || se.EXTSTS != 0x06 {
		t.Fatalf("errors.As(*StatusError) failed: %v", values[0].Error)
	}
}

func TestShortReadIsError(t *testing.T) {
	client, _ := startPCCCPeer(t, TypeSLC500, func(cmd []byte) []byte {
		switch cmd[5] {
		case 2: // N7:0 -> nothing
			return pcccReply(cmd, 0)
		case 4: // F8:0 -> 2 of 4 bytes
			return pcccReply(cmd, 0, 0x00, 0x00)
		default: // batch of 3 N words -> 2 words
			return pcccReply(cmd, 0, 1, 0, 2, 0)
		}
	})
	values, _ := client.Read("N7:0", "F8:0")
	for _, v := range values {
		if v.Error == nil {
			t.Errorf("%s: short reply returned value %#v, want error", v.Name, v.Value)
		}
	}
	addr, _ := ParseAddress("N7:0")
	if _, err := client.PLC().ReadAddressN(addr, 3); err == nil {
		t.Fatal("ReadAddressN accepted a short reply")
	}
}

// slcFile0 builds a system file 0 image laid out as pycomm3's _parse_file0
// reads it for an SLC 5/05 ("1747" / default branch): rows of row_size=10
// bytes start at file_position=79; each row is [file type][size in bytes LE].
func slcFile0(rows [][3]byte) []byte {
	file0 := make([]byte, 79+10*len(rows))
	for i, r := range rows {
		copy(file0[79+10*i:], r[:])
	}
	return file0
}

var slcDirectoryRows = [][3]byte{
	{0x82, 2, 0},       // O0: 1 word
	{0x83, 4, 0},       // I1: 2 words
	{0x84, 66, 0},      // S2: 33 words
	{0x85, 2, 0},       // B3: 1 word
	{0x86, 18, 0},      // T4: 3 timers
	{0x87, 18, 0},      // C5: 3 counters
	{0x88, 6, 0},       // R6: 1 control
	{0x89, 100, 0},     // N7: 50 ints
	{0x8A, 40, 0},      // F8: 10 floats
	{0x81, 0, 0},       // file 9 skipped (placeholder)
	{0x00, 0, 0},       // not a data file; does not use a number
	{0x8D, 168, 0},     // ST10: 2 strings
	{0x91, 8, 0},       // L11: 2 longs
	{0x89, 0x20, 0x03}, // N12: 800 bytes = 400 ints
}

var slcDirectoryWant = []struct {
	num   int
	ft    byte
	count int
}{
	{0, FileTypeOutput, 1}, {1, FileTypeInput, 2}, {2, FileTypeStatus, 33}, {3, FileTypeBinary, 1},
	{4, FileTypeTimer, 3}, {5, FileTypeCounter, 3}, {6, FileTypeControl, 1}, {7, FileTypeInteger, 50},
	{8, FileTypeFloat, 10}, {10, FileTypeString, 2}, {11, FileTypeLong, 2}, {12, FileTypeInteger, 400},
}

func checkDirectory(t *testing.T, entries []FileDirectoryEntry) {
	t.Helper()
	if len(entries) != len(slcDirectoryWant) {
		t.Fatalf("%d entries, want %d: %+v", len(entries), len(slcDirectoryWant), entries)
	}
	for i, w := range slcDirectoryWant {
		e := entries[i]
		if e.FileNumber != w.num || e.FileType != w.ft || e.ElementCount != w.count || e.TypePrefix != FileTypePrefix(w.ft) {
			t.Errorf("entry %d = %+v, want file %d type 0x%02X count %d", i, e, w.num, w.ft, w.count)
		}
	}
}

func TestParseFileDirectorySLCLayout(t *testing.T) {
	sys0, err := lookupSys0Info("1747")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := parseFileDirectory(slcFile0(slcDirectoryRows), sys0)
	if err != nil {
		t.Fatal(err)
	}
	checkDirectory(t, entries)
}

// End to end against the request sequence of pycomm3's get_file_directory:
// Diagnostic Status (06 00 TNS 03), the size read (0F 00 TNS A1 04 00 01 23),
// then whole-file reads (0F 00 TNS A1 <=50 00 01 <word offset>).
func TestGetFileDirectorySLCRequests(t *testing.T) {
	file0 := slcFile0(slcDirectoryRows)
	var mu sync.Mutex
	var reads [][]byte
	client, _ := startPCCCPeer(t, TypeSLC500, func(cmd []byte) []byte {
		if cmd[0] == CmdDiagnosticStatus {
			if len(cmd) != 5 || cmd[4] != FncDiagnosticStatus {
				t.Errorf("diagnostic status request % X, want 06 00 <tns> 03", cmd)
			}
			diag := make([]byte, 24)
			copy(diag[5:], "1747-L552  ")
			return pcccReply(cmd, 0, diag...)
		}
		if cmd[0] != CmdTypedCommand || cmd[4] != FncReadSection {
			t.Errorf("unexpected request % X", cmd)
			return pcccReply(cmd, 0x10)
		}
		mu.Lock()
		reads = append(reads, cmd[5:])
		mu.Unlock()
		if len(cmd) != 9 {
			t.Errorf("FNC A1 request % X: want 4 fields (size, file, type, element)", cmd)
			return pcccReply(cmd, 0x10)
		}
		size, file, ft, elem := int(cmd[5]), cmd[6], cmd[7], int(cmd[8])
		if file != 0 || ft != 0x01 {
			t.Errorf("read of file %d type 0x%02X, want file 0 type 0x01", file, ft)
		}
		if elem == 0x23 && size == 4 {
			return pcccReply(cmd, 0, byte(len(file0)), byte(len(file0)>>8), 0, 0)
		}
		off := elem * 2
		end := off + size
		if end > len(file0) {
			end = len(file0)
		}
		return pcccReply(cmd, 0, file0[off:end]...)
	})
	entries, err := client.DiscoverDataFiles()
	if err != nil {
		t.Fatal(err)
	}
	checkDirectory(t, entries)
	mu.Lock()
	defer mu.Unlock()
	want := [][]byte{{0x04, 0x00, 0x01, 0x23}, {0x50, 0x00, 0x01, 0x00}, {0x50, 0x00, 0x01, 0x28}, {byte(len(file0) - 160), 0x00, 0x01, 0x50}}
	if len(reads) != len(want) {
		t.Fatalf("reads % X, want % X", reads, want)
	}
	for i := range want {
		if !bytes.Equal(reads[i], want[i]) {
			t.Errorf("read %d = % X, want % X", i, reads[i], want[i])
		}
	}
}

func TestDiscoveryUnverifiedLayoutsNotSupported(t *testing.T) {
	for _, catalog := range []string{"1761-L16BWA", "1762-L24BWA", "1764-LSP", "1747-L524", "1747-L511", "9999-X"} {
		if _, err := sys0InfoForCatalog(catalog); !errors.Is(err, ErrDiscoveryNotSupported) {
			t.Errorf("%s: err = %v, want ErrDiscoveryNotSupported", catalog, err)
		}
	}
	for _, catalog := range []string{"1747-L552", "1747-L532", "1747-L543", "1763-L16BWA", "1766-L32BWAA"} {
		if _, err := sys0InfoForCatalog(catalog); err != nil {
			t.Errorf("%s: %v", catalog, err)
		}
	}
}
