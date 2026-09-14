package logix

import (
	"encoding/binary"
	"fmt"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yatesdr/plcio/eip"
)

func readPathTemplates() map[uint16]*Template {
	text := &Template{ID: 2, Name: "STRING", Size: 88, RawHandle: 0xbeef,
		Members:   []TemplateMember{{Name: "LEN", Type: TypeDINT}, {Name: "DATA", Type: TypeSINT | TypeArrayMask, Offset: 4, ArrayDims: []int{82}}},
		MemberMap: map[string]int{"LEN": 0, "DATA": 1}}
	record := &Template{ID: 1, Name: "SyntheticRecord", Size: 8196, RawHandle: 0xdef0,
		Members: []TemplateMember{{Name: "Card", Type: TypeDINT}, {Name: "First", Type: 0x8002, Offset: 4},
			{Name: "Last", Type: 0x8002, Offset: 92}, {Name: "Tail", Type: TypeDINT, Offset: 8192}},
		MemberMap: map[string]int{"Card": 0, "First": 1, "Last": 2, "Tail": 3}}
	nested := &Template{ID: 3, Name: "NestedRecord", Size: 16392, RawHandle: 0xabcd,
		Members: []TemplateMember{{Name: "Rows", Type: 0xa001, ArrayDims: []int{2}}}, MemberMap: map[string]int{"Rows": 0}}
	return map[uint16]*Template{1: record, 2: text, 3: nested}
}

func syntheticText(text string) []byte {
	data := make([]byte, 88)
	binary.LittleEndian.PutUint32(data, uint32(len(text)))
	copy(data[4:], text)
	return data
}

func syntheticRecord() []byte {
	data := make([]byte, 8196)
	binary.LittleEndian.PutUint32(data, 1234)
	copy(data[4:], syntheticText("Sample"))
	copy(data[92:], syntheticText("Test"))
	binary.LittleEndian.PutUint32(data[8192:], 9876)
	return data
}

func TestResolveIndexedAndProgramMemberTypes(t *testing.T) {
	// A template instance can share its low bits with an atomic CIP code.
	collision := &Client{templates: map[uint16]*Template{TypeDINT: {Size: 8196}}}
	if collision.GetElementSize(0x8000|TypeDINT) != 8196 || collision.GetElementSize(TypeDINT) != 4 {
		t.Fatal("structure template ID was mistaken for an atomic type")
	}
	c := &Client{plc: &PLC{}, templates: readPathTemplates(), tagInfo: map[string]TagInfo{
		"Records":                 {Name: "Records", TypeCode: 0xa001, Instance: 42, Dimensions: []int{2}},
		"Program:Fixture.Records": {Name: "Program:Fixture.Records", TypeCode: 0xa001, Instance: 43, Dimensions: []int{2}},
		"Nested":                  {Name: "Nested", TypeCode: 0x8003, Instance: 44},
		"Matrix":                  {Name: "Matrix", TypeCode: 0x4000 | TypeDINT, Instance: 45, Dimensions: []int{2, 3}},
	}}
	for _, tc := range []struct {
		name  string
		code  uint16
		count uint16
	}{
		{"Records", 0xa001, 2}, {"Records[1]", 0x8001, 1}, {"Records[1].First", 0x8002, 1},
		{"Program:Fixture.Records[1].Last", 0x8002, 1}, {"Nested.Rows[1].First", 0x8002, 1},
		{"Records[1].First.DATA[3]", TypeSINT, 1}, {"Matrix[1,2]", TypeDINT, 1},
		{"Matrix[1][2]", TypeDINT, 1}, {"Matrix[1]", TypeArrayMask | TypeDINT, 3},
	} {
		info, ok := c.resolveTagInfo(tc.name)
		if !ok || info.TypeCode != tc.code || c.getElementCount(tc.name) != tc.count {
			t.Fatalf("%s: resolved %+v ok=%v count=%d", tc.name, info, ok, c.getElementCount(tc.name))
		}
		if tc.name != rootTagName(tc.name) && info.Instance != 0 {
			t.Fatalf("derived path inherited root instance: %+v", info)
		}
	}
	for _, name := range []string{"Records[2]", "Records[-1]", "Records[1].Missing", "Records.First", "Records[1].First.DATA[82]", "Records[1", "Matrix[1,3]"} {
		if _, ok := c.ResolveTagType(name); ok {
			t.Fatalf("invalid path resolved: %s", name)
		}
	}
	if len(c.tagInfo) != 4 || c.tagInfo["Records"].Dimensions[0] != 2 {
		t.Fatal("derived/invalid paths polluted or replaced the catalog")
	}
}

func TestStandardStringDecodingTopLevelNestedAndArray(t *testing.T) {
	for _, name := range []string{"STRING", "ASCIISTRING82"} {
		t.Run(name, func(t *testing.T) {
			c := &Client{plc: &PLC{}, templates: readPathTemplates()}
			c.templates[2].Name = name
			for _, text := range []string{"", "Sample", strings.Repeat("x", 82)} {
				data := append([]byte{0xef, 0xbe}, syntheticText(text)...)
				v := &TagValue{DataType: 0x8002, Bytes: data, Count: 1}
				if got := v.GoValueDecoded(c); got != text {
					t.Fatalf("STRING %d bytes: %T", len(text), got)
				}
			}
			for _, length := range []uint32{83, 0xffffffff} {
				data := append([]byte{0xef, 0xbe}, syntheticText("Sample")...)
				binary.LittleEndian.PutUint32(data[2:], length)
				if _, text := (&TagValue{DataType: 0x8002, Bytes: data}).GoValueDecoded(c).(string); text {
					t.Fatal("invalid LEN decoded as text")
				}
			}
			short := []byte{0xef, 0xbe, 6, 0, 0, 0, 'x'}
			if _, text := (&TagValue{DataType: 0x8002, Bytes: short}).GoValueDecoded(c).(string); text {
				t.Fatal("short data decoded as text")
			}
			want := map[string]any{"Card": int64(1234), "First": "Sample", "Last": "Test", "Tail": int64(9876)}
			got := (&TagValue{DataType: 0x8001, Bytes: append([]byte{0xf0, 0xde}, syntheticRecord()...)}).GoValueDecoded(c)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("nested text: %#v", got)
			}
			array := append([]byte{0xef, 0xbe}, syntheticText("Sample")...)
			array = append(array, syntheticText("Test")...)
			got = (&TagValue{DataType: 0xa002, Bytes: array, Count: 2}).GoValueDecoded(c)
			if !reflect.DeepEqual(got, []any{"Sample", "Test"}) {
				t.Fatalf("STRING array: %#v", got)
			}
			custom := *c.templates[2]
			custom.Name = "CustomRecord"
			c.templates[2] = &custom
			if _, text := (&TagValue{DataType: 0x8002, Bytes: append([]byte{0xef, 0xbe}, syntheticText("Sample")...)}).GoValueDecoded(c).(string); text {
				t.Fatal("custom structure was silently collapsed to STRING")
			}
		})
	}
}

type readPathPeer struct{ fragments, batches, single, connected atomic.Int32 }

func newReadPathPeer(t *testing.T, connectionSize, payload int, routed, individual bool, faults ...string) (*Client, *readPathPeer) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	stats := &readPathPeer{}
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		var handle func([]byte) []byte
		handle = func(req []byte) []byte {
			if len(req) < 2 {
				t.Error("short CIP request")
				return nil
			}
			start := 2 + int(req[1])*2
			if start > len(req) {
				t.Error("short path")
				return nil
			}
			switch req[0] {
			case 0x5b, 0x54:
				if (req[0] == 0x5b && connectionSize != 4002) || (req[0] == 0x54 && connectionSize != 504) {
					return []byte{req[0] | 0x80, 0, 1, 0}
				}
				return append([]byte{req[0] | 0x80, 0, 0, 0}, make([]byte, 26)...)
			case 0x4e:
				return []byte{0xce, 0, 0, 0}
			case 0x0a:
				stats.batches.Add(1)
				limit := 480
				if connectionSize > 0 {
					limit = connectionSize - 2
				}
				if len(req) > limit {
					t.Errorf("MSP request %d exceeds %d-byte connection budget", len(req), limit)
					return []byte{0x8a, 0, 0x11, 0}
				}
				body := req[start:]
				n := int(binary.LittleEndian.Uint16(body))
				out := make([]byte, 2+2*n)
				binary.LittleEndian.PutUint16(out, uint16(n))
				failed := false
				for i := 0; i < n; i++ {
					begin := int(binary.LittleEndian.Uint16(body[2+2*i:]))
					end := len(body)
					if i+1 < n {
						end = int(binary.LittleEndian.Uint16(body[4+2*i:]))
					}
					binary.LittleEndian.PutUint16(out[2+2*i:], uint16(len(out)))
					reply := handle(body[begin:end])
					failed = failed || reply[2] != 0
					out = append(out, reply...)
				}
				status := byte(0)
				if failed {
					status = 0x1e
				}
				if len(out)+4 > limit {
					t.Errorf("MSP reply %d exceeds %d-byte connection budget", len(out)+4, limit)
				}
				return append([]byte{0x8a, 0, status, 0}, out...)
			case 0x4c, 0x52:
				name := readPathName(t, req[2:start])
				if name == "Missing" {
					return []byte{req[0] | 0x80, 0, 5, 0}
				}
				count := int(binary.LittleEndian.Uint16(req[start:]))
				var data []byte
				var structure uint16
				switch {
				case strings.HasSuffix(name, ".First"):
					data = syntheticText("Sample")
					structure = 0xbeef
				case strings.HasSuffix(name, ".Last"):
					data = syntheticText("Test")
					structure = 0xbeef
				case name == "Records" || strings.HasSuffix(name, "Records[1]"):
					data = syntheticRecord()
					structure = 0xdef0
					if name == "Records" && count == 2 {
						data = append(data, syntheticRecord()...)
					} else if count != 1 {
						t.Errorf("indexed record count %d", count)
					}
				case strings.HasPrefix(name, "Scalar") || strings.HasSuffix(name, ".Card"):
					data = []byte{0xd2, 4, 0, 0}
				case strings.HasPrefix(name, "L"):
					data = binary.LittleEndian.AppendUint64(nil, 1234)
				default:
					t.Errorf("unexpected selected path %s", name)
					return []byte{req[0] | 0x80, 0, 5, 0}
				}
				offset := 0
				if req[0] == 0x52 {
					stats.fragments.Add(1)
					offset = int(binary.LittleEndian.Uint32(req[start+2:]))
				} else {
					stats.single.Add(1)
				}
				if offset >= len(data) {
					t.Errorf("fragment offset %d beyond data %d", offset, len(data))
					return []byte{req[0] | 0x80, 0, 5, 0}
				}
				end := offset + payload
				status := byte(0x06)
				if end >= len(data) {
					end = len(data)
					status = 0
				}
				reply := []byte{req[0] | 0x80, 0, status, 0, 0xc4, 0}
				if structure == 0 && len(data) == 8 {
					reply[4] = byte(TypeLINT)
				}
				if structure != 0 {
					reply = []byte{req[0] | 0x80, 0, status, 0, 0xa0, 2, byte(structure), byte(structure >> 8)}
				}
				if req[0] == 0x52 && len(faults) > 0 && offset > 0 {
					switch faults[0] {
					case "handle":
						reply[6] ^= 1
					case "type":
						reply[4], reply[5] = 0xc4, 0
					case "empty":
						return reply
					case "missing-handle":
						return reply[:6]
					case "device-error":
						return []byte{0xd2, 0, 5, 0}
					case "early-final":
						reply[2] = 0
					case "extra-partial":
						reply[2] = 6
					case "oversized":
						return append(reply, make([]byte, 100)...)
					case "disconnect":
						return nil
					}
				}
				return append(reply, data[offset:end]...)
			default:
				t.Errorf("unexpected service %x", req[0])
				return nil
			}
		}
		for {
			frame, err := eip.ReadFrame(conn)
			if err != nil {
				return
			}
			var reply *eip.Frame
			switch frame.Command {
			case 0x65:
				reply = frame.Reply(0, []byte{1, 0, 0, 0})
				reply.SessionHandle = 1234
			case 0x66:
				return
			case 0x6f, 0x70:
				body, err := eip.ParseRRData(frame.Data)
				if err != nil {
					t.Error(err)
					return
				}
				packet, err := eip.ParseEipCommonPacket(body)
				if err != nil || len(packet.Items) != 2 {
					t.Errorf("CPF %v", err)
					return
				}
				req := packet.Items[1].Data
				var seq []byte
				if frame.Command == 0x70 {
					stats.connected.Add(1)
					seq = req[:2]
					req = req[2:]
				}
				wrapped := req[0] == 0x52 && req[2] == 0x20
				if wrapped {
					start := 2 + int(req[1])*2
					n := int(binary.LittleEndian.Uint16(req[start+2:]))
					req = req[start+4 : start+4+n]
				}
				response := handle(req)
				if response == nil {
					return
				}
				if wrapped {
					if len(faults) == 0 || faults[0] != "direct-routed-reply" {
						response = append([]byte{0xd2, 0, 0, 0}, response...)
					}
				}
				address, transport := uint16(0), uint16(0xb2)
				var addressData []byte
				if seq != nil {
					address, transport = 0xa1, 0xb1
					addressData = make([]byte, 4)
					response = append(append([]byte(nil), seq...), response...)
				}
				cpf := &eip.EipCommonPacket{Items: []eip.EipCommonPacketItem{{TypeId: address, Length: uint16(len(addressData)), Data: addressData}, {TypeId: transport, Length: uint16(len(response)), Data: response}}}
				reply = frame.Reply(0, eip.BuildRRData(cpf.Bytes()))
			default:
				t.Errorf("unexpected encapsulation %x", frame.Command)
				return
			}
			if _, err = conn.Write(reply.Bytes()); err != nil {
				return
			}
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	n, _ := strconv.Atoi(port)
	transport := eip.NewEipClientWithPort(host, uint16(n))
	if err := transport.Connect(); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	c := &Client{plc: &PLC{Connection: transport}, micro800: individual, templates: readPathTemplates(), tagInfo: map[string]TagInfo{
		"Records":                 {Name: "Records", TypeCode: 0xa001, Instance: 42, Dimensions: []int{2}},
		"Program:Fixture.Records": {Name: "Program:Fixture.Records", TypeCode: 0xa001, Instance: 43, Dimensions: []int{2}},
	}}
	if routed {
		c.plc.RoutePath = []byte{1, 0}
	}
	if connectionSize > 0 {
		if err := c.plc.OpenConnection(); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		c.Close()
		listener.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("read peer did not close")
		}
	})
	return c, stats
}

func TestRoutedFragmentedReadAcceptsDirectAndWrappedReplies(t *testing.T) {
	for _, mode := range []string{"wrapped", "direct-routed-reply"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := newReadPathPeer(t, 0, 32, true, false, mode)
			v, err := c.ReadWithCount("Records[1]", 1)
			if err != nil || v.GoValueDecoded(c).(map[string]any)["Tail"] != int64(9876) {
				t.Fatalf("routed record incomplete: %v", err)
			}
		})
	}
}

func TestFragmentedReadRejectsIncompleteAndInconsistentResponses(t *testing.T) {
	for _, fault := range []string{"handle", "type", "empty", "missing-handle", "device-error", "early-final", "extra-partial", "oversized", "disconnect"} {
		t.Run(fault, func(t *testing.T) {
			c, _ := newReadPathPeer(t, 504, 32, false, false, fault)
			value, err := c.PLC().ReadTagFragmented("Records[1].First", 88)
			if err == nil || value != nil {
				t.Fatalf("fault %s returned successful/partial data", fault)
			}
		})
	}
}

func readPathName(t *testing.T, path []byte) string {
	t.Helper()
	name := ""
	for len(path) > 0 {
		switch path[0] {
		case 0x91:
			n := int(path[1])
			if name != "" {
				name += "."
			}
			name += string(path[2 : 2+n])
			size := 2 + n
			if size%2 != 0 {
				size++
			}
			path = path[size:]
		case 0x28:
			name += fmt.Sprintf("[%d]", path[1])
			path = path[2:]
		default:
			t.Fatalf("unexpected test path %x", path)
		}
	}
	return name
}

func TestReadStringsAndRecordsAcrossMessagingAndBuffers(t *testing.T) {
	for _, mode := range []struct {
		name               string
		size, payload      int
		routed, individual bool
	}{
		{"unconnected", 0, 480, false, false}, {"unconnected-tiny", 0, 32, false, false},
		{"routed", 0, 480, true, false}, {"connected-504", 504, 400, false, false},
		{"connected-504-tiny", 504, 32, false, false}, {"connected-4002", 4002, 3900, false, false},
		{"connected-4002-tiny", 4002, 64, false, false}, {"individual", 0, 32, false, true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			c, stats := newReadPathPeer(t, mode.size, mode.payload, mode.routed, mode.individual)
			active, size := c.ConnectionInfo()
			if active != (mode.size > 0) || int(size) != mode.size ||
				(mode.size == 0 && c.ConnectionMode() != "Unconnected messaging") {
				t.Fatalf("wrong connection mode: %s, active=%v size=%d", c.ConnectionMode(), active, size)
			}
			for _, names := range [][]string{{"Records[1].First"}, {"Records[1]"}, {"Records[1].First", "Records[1].Last", "Records[1].Card", "Missing"}, {"Program:Fixture.Records[1].First"}, {"Records"}} {
				values, err := c.Read(names...)
				if err != nil || len(values) != len(names) {
					t.Fatalf("read %v: %d results %v", names, len(values), err)
				}
				for _, v := range values {
					if v.Name == "Missing" {
						if v.Error == nil {
							t.Fatal("failed slot lost")
						}
						continue
					}
					if v.Error != nil {
						t.Fatalf("%s: %v", v.Name, v.Error)
					}
					decoded := v.GoValueDecoded(c)
					switch {
					case strings.HasSuffix(v.Name, ".First"):
						if decoded != "Sample" || v.Count != 1 {
							t.Fatalf("first STRING %T count %d", decoded, v.Count)
						}
					case strings.HasSuffix(v.Name, ".Last"):
						if decoded != "Test" {
							t.Fatalf("last STRING %T", decoded)
						}
					case strings.HasSuffix(v.Name, ".Card"):
						if decoded != int64(1234) {
							t.Fatalf("scalar %v", decoded)
						}
					case v.Name == "Records":
						a, ok := decoded.([]any)
						if !ok || len(a) != 2 || a[1].(map[string]any)["Tail"] != int64(9876) {
							t.Fatal("record array incomplete/misaligned")
						}
					default:
						m, ok := decoded.(map[string]any)
						if !ok || m["Card"] != int64(1234) || m["First"] != "Sample" || m["Tail"] != int64(9876) {
							t.Fatal("record incomplete/misaligned")
						}
					}
				}
			}
			v, err := c.ReadWithCount("Records[1].First", 1)
			if err != nil || v.GoValueDecoded(c) != "Sample" {
				t.Fatalf("explicit single count: %v", err)
			}
			if stats.fragments.Load() == 0 || (mode.size > 0 && stats.connected.Load() == 0) {
				t.Fatal("intended fragmentation/connected path was not exercised")
			}
		})
	}
}

func TestScalarBatchFitsSmallAndLargeConnectionBudgets(t *testing.T) {
	for _, size := range []int{0, 504, 4002} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			c, stats := newReadPathPeer(t, size, 480, false, false)
			var names []string
			for i := 0; i < 50; i++ {
				name := fmt.Sprintf("Scalar_%02d_with_a_long_symbol_name", i)
				names = append(names, name)
			}
			values, err := c.Read(names...)
			if err != nil || len(values) != 50 {
				t.Fatalf("batch %d values err=%v", len(values), err)
			}
			for _, v := range values {
				if v.Error != nil || v.GoValue() != int64(1234) {
					t.Fatalf("scalar batch error: %v", v.Error)
				}
			}
			if size < 4002 && stats.batches.Load() < 2 {
				t.Fatal("small buffer did not split batch")
			}
		})
	}
}

func TestNativeSingleAndMSPReadsPreserveStringDescriptors(t *testing.T) {
	for _, size := range []int{0, 504, 4002} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			c, _ := newReadPathPeer(t, size, 400, false, false)
			var single *Tag
			var err error
			if size > 0 {
				single, err = c.PLC().ReadTagConnected("Records[1].First")
			} else {
				single, err = c.PLC().ReadTag("Records[1].First")
			}
			if err != nil || single.DataType != CIPStructType || len(single.Bytes) != 90 || binary.LittleEndian.Uint16(single.Bytes) != 0xbeef {
				t.Fatalf("native single descriptor/storage: %v", err)
			}
			batch, err := c.PLC().ReadMultiple([]string{"Records[1].First", "Records[1].Last", "Missing"})
			if err != nil || len(batch) != 3 || batch[2] != nil || !reflect.DeepEqual(batch[0].Bytes, single.Bytes) || batch[0].DataType != CIPStructType {
				t.Fatalf("native MSP descriptor/partial slot: %v", err)
			}
			decoded, err := c.DecodeUDT(0x8002, single.Bytes)
			if err != nil || decoded["LEN"] != int64(6) {
				t.Fatalf("public DecodeUDT top-level map contract: %v", err)
			}
		})
	}
}

func TestScalarBatchFitsReplyBudget(t *testing.T) {
	for _, size := range []int{0, 504, 4002} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			c, _ := newReadPathPeer(t, size, 400, false, false)
			names := make([]string, 50)
			for i := range names {
				names[i] = fmt.Sprintf("L%02d", i)
			}
			budget := 480
			if size > 0 {
				budget = size - 2
			}
			expected := (budget - 6) / 16
			if expected > 50 {
				expected = 50
			}
			if n := c.readBatchSize(names); n != expected {
				t.Fatalf("reply sizing selected %d instead of %d", n, expected)
			}
			values, err := c.Read(names...)
			if err != nil || len(values) != 50 {
				t.Fatalf("LINT batch: %v", err)
			}
			for _, v := range values {
				if v.Error != nil || v.GoValue() != int64(1234) {
					t.Fatal("LINT batch overflow or value mismatch")
				}
			}
		})
	}
}
