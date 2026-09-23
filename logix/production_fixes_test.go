package logix

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yatesdr/plcio/cip"
	"github.com/yatesdr/plcio/eip"
)

// fakeTemplateAttrs answers Get Attribute List (0x03) on a Template object.
func fakeTemplateAttrs(req []byte, values map[uint16][]byte) []byte {
	start := 2 + int(req[1])*2
	body := req[start:]
	n := int(binary.LittleEndian.Uint16(body))
	out := []byte{0x83, 0, 0, 0}
	out = binary.LittleEndian.AppendUint16(out, uint16(n))
	for i := 0; i < n; i++ {
		id := binary.LittleEndian.Uint16(body[2+2*i:])
		out = binary.LittleEndian.AppendUint16(out, id)
		out = append(out, 0, 0)
		out = append(out, values[id]...)
	}
	return out
}

func u16(v uint16) []byte { return binary.LittleEndian.AppendUint16(nil, v) }
func u32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

// pairTemplate is a two-member UDT {X DINT @0, Y REAL @4}, 8 bytes.
var pairDefinition = append([]byte{
	0, 0, 0xc4, 0, 0, 0, 0, 0,
	0, 0, 0xca, 0, 4, 0, 0, 0,
}, []byte("Pair;n\x00X\x00Y\x00")...)

func pairTemplateAttrs() map[uint16][]byte {
	return map[uint16][]byte{
		5: u32(8), 4: u32(uint32((len(pairDefinition) + 23 + 3) / 4)), 3: u16(8), 2: u16(2), 1: u16(0xbeef),
	}
}

// Item 1: Forward Close must carry the connection triple actually sent in
// the Forward Open, otherwise the controller cannot match and release it.
func TestForwardCloseEchoesForwardOpenTriple(t *testing.T) {
	var mu sync.Mutex
	var openReq, closeReq []byte
	closeStatus := []byte{0xce, 0, 0, 0}
	peer := newFakePeer(t, func(req []byte) []byte {
		mu.Lock()
		defer mu.Unlock()
		switch req[0] {
		case 0x5b:
			openReq = append([]byte(nil), req...)
			return fakeForwardOpenReply(req)
		case 0x4e:
			closeReq = append([]byte(nil), req...)
			return closeStatus
		}
		t.Errorf("unexpected service %x", req[0])
		return nil
	})
	for _, reject := range []bool{false, true} {
		t.Run(fmt.Sprint("reject=", reject), func(t *testing.T) {
			if reject {
				mu.Lock()
				closeStatus = []byte{0xce, 0, 0x01, 0x01, 0x07, 0x01} // Connection not found
				mu.Unlock()
			}
			c := peer.newClient(t, true)
			if active, size := c.ConnectionInfo(); !active || size != ConnectionSizeLarge {
				t.Fatalf("connection not open: %v %d", active, size)
			}
			if err := c.PLC().CloseConnection(); err != nil {
				t.Fatalf("CloseConnection returned %v for a best-effort close", err)
			}
			if conn, _ := c.PLC().activeConn(); conn != nil {
				t.Fatal("connection state retained after close")
			}
			mu.Lock()
			defer mu.Unlock()
			if len(openReq) < 46 || len(closeReq) < 18 {
				t.Fatalf("missing open/close: %x / %x", openReq, closeReq)
			}
			// Forward Open: serial/vendor/originator serial at 16..24.
			// Forward Close: the same triple at 8..16.
			if !bytes.Equal(openReq[16:24], closeReq[8:16]) {
				t.Fatalf("Forward Close triple % x does not match Forward Open % x", closeReq[8:16], openReq[16:24])
			}
			if !bytes.Equal(openReq[46:], closeReq[18:]) {
				t.Fatalf("Forward Close path % x differs from Forward Open path % x", closeReq[18:], openReq[46:])
			}
		})
	}
	if err := checkForwardCloseReply(&eip.EipCommonPacket{Items: []eip.EipCommonPacketItem{{}, {Data: []byte{0xce, 0, 0x01, 0x01, 0x07, 0x01}}}}); err == nil {
		t.Fatal("rejected Forward Close reported as success")
	}
	if err := checkForwardCloseReply(&eip.EipCommonPacket{Items: []eip.EipCommonPacketItem{{}, {Data: []byte{0xce, 0, 0, 0}}}}); err != nil {
		t.Fatalf("accepted Forward Close reported as failure: %v", err)
	}
}

// Item 10: a failed Forward Open must not be described as connected.
func TestConnectionModeAfterFailedForwardOpen(t *testing.T) {
	peer := newFakePeer(t, func(req []byte) []byte {
		return []byte{req[0] | 0x80, 0, 0x01, 0x01, 0x00, 0x01}
	})
	c := peer.newClient(t, false)
	if err := c.PLC().OpenConnection(); err == nil {
		t.Fatal("rejected Forward Open succeeded")
	}
	if mode := c.ConnectionMode(); mode != "Unconnected messaging" {
		t.Fatalf("mode %q", mode)
	}
	if active, size := c.ConnectionInfo(); active || size != 0 {
		t.Fatalf("connection info %v %d", active, size)
	}
}

// Item 2: an additional-status size larger than the reply must be an error,
// not a slice-bounds panic.
func TestMalformedAdditionalStatusIsRejected(t *testing.T) {
	peer := newFakePeer(t, func(req []byte) []byte {
		switch req[0] {
		case 0x0a:
			return []byte{0x8a, 0, 0, 5}
		case 0x0e:
			return []byte{0x8e, 0, 0, 5}
		}
		t.Errorf("unexpected service %x", req[0])
		return nil
	})
	c := peer.newClient(t, false)
	if tags, err := c.PLC().ReadMultiple([]string{"A", "B"}); err == nil {
		t.Fatalf("malformed MSP reply accepted: %v", tags)
	}
	if dims, err := c.PLC().getSymbolDimensions(5, 1); err == nil {
		t.Fatalf("malformed dimension reply accepted: %v", dims)
	}
}

func bigStructData(n int) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = byte(i*7 + 3)
	}
	return data
}

// Item 3: every fragment of a structure carries the type and handle; only
// the first handle is kept and no data bytes are lost at fragment boundaries.
func TestFragmentedStructureReassemblesExactly(t *testing.T) {
	data := bigStructData(1000)
	want := append(u16(0x1234), data...)
	for _, open := range []bool{false, true} {
		t.Run(fmt.Sprint("connected=", open), func(t *testing.T) {
			peer := newFakePeer(t, func(req []byte) []byte {
				switch req[0] {
				case 0x5b:
					return fakeForwardOpenReply(req)
				case 0x4e:
					return []byte{0xce, 0, 0, 0}
				case 0x0a:
					return fakeMSP(req, func(sub []byte) []byte {
						_, rest := fakeSymbol(sub)
						return fakeFragment(sub, rest, CIPStructType, 0x1234, data, 100)
					})
				case 0x4c, 0x52:
					name, rest := fakeSymbol(req)
					if name != "Big" {
						return []byte{req[0] | 0x80, 0, 0x05, 0} // e.g. Big[0] on a non-array
					}
					max := 150
					if req[0] == 0x4c {
						max = 100
					}
					return fakeFragment(req, rest, CIPStructType, 0x1234, data, max)
				}
				t.Errorf("unexpected service %x", req[0])
				return nil
			})
			c := peer.newClient(t, open)
			tag, err := c.PLC().ReadTagFragmented("Big", 1000)
			if err != nil || !bytes.Equal(tag.Bytes, want) {
				t.Fatalf("fragmented read: %v", err)
			}
			// Known structure: size from the template drives the fragmented read.
			c.templates = map[uint16]*Template{9: {ID: 9, Name: "Big", Size: 1000}}
			c.tagInfo = map[string]TagInfo{"Big": {Name: "Big", TypeCode: 0x8009, Instance: 1}}
			values, err := c.Read("Big")
			if err != nil || len(values) != 1 || values[0].Error != nil || !bytes.Equal(values[0].Bytes, want) {
				t.Fatalf("known structure read: %v %+v", err, values)
			}
			// Unknown type (no metadata): a partial reply, single or batched,
			// must not be returned as a truncated success.
			c.tagInfo = nil
			value, err := c.ReadWithCount("Big", 1)
			if err != nil || !bytes.Equal(value.Bytes, want) {
				t.Fatalf("unknown structure single read: %v %d bytes", err, len(value.Bytes))
			}
			values, err = c.Read("Big")
			if err != nil || len(values) != 1 || values[0].Error != nil || !bytes.Equal(values[0].Bytes, want) {
				t.Fatalf("unknown structure batched read: %v %d bytes", err, len(values[0].Bytes))
			}
		})
	}
}

// Item 4: chunked atomic array reads either return the whole array or fail.
func TestChunkedArrayReadNeverReturnsTruncatedData(t *testing.T) {
	const total = 300
	data := make([]byte, 0, total*4)
	for i := 0; i < total; i++ {
		data = binary.LittleEndian.AppendUint32(data, uint32(i*3+1))
	}
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint("fail=", fail), func(t *testing.T) {
			peer := newFakePeer(t, func(req []byte) []byte {
				if req[0] != 0x4c {
					t.Errorf("unexpected service %x", req[0])
					return nil
				}
				name, rest := fakeSymbol(req)
				count := int(binary.LittleEndian.Uint16(rest))
				first := 0
				if name != "Arr" {
					if _, err := fmt.Sscanf(name, "Arr[%d]", &first); err != nil {
						return []byte{0xcc, 0, 0x05, 0}
					}
				}
				if fail && first >= 200 {
					return []byte{0xcc, 0, 0x05, 0}
				}
				n, status := count, byte(0)
				if n > 100 {
					n, status = 100, 0x06
				}
				out := []byte{0xcc, 0, status, 0, 0xc4, 0}
				return append(out, data[first*4:(first+n)*4]...)
			})
			c := peer.newClient(t, false)
			c.tagInfo = map[string]TagInfo{"Arr": {Name: "Arr", TypeCode: 0x2000 | TypeDINT, Instance: 1, Dimensions: []int{total}}}
			values, err := c.Read("Arr")
			if err != nil || len(values) != 1 {
				t.Fatalf("read: %v", err)
			}
			if fail {
				if values[0].Error == nil {
					t.Fatalf("truncated array (%d of %d bytes) returned as success", len(values[0].Bytes), len(data))
				}
				return
			}
			if values[0].Error != nil || !bytes.Equal(values[0].Bytes, data) {
				t.Fatalf("complete array read changed: %v", values[0].Error)
			}
		})
	}
}

// Item 4: a template definition that stops before the final chunk must not
// be parsed and cached as a (truncated) success.
func TestIncompleteTemplateDefinitionIsNotCached(t *testing.T) {
	for _, fault := range []string{"cip-error", "disconnect"} {
		t.Run(fault, func(t *testing.T) {
			peer := newFakePeer(t, func(req []byte) []byte {
				switch req[0] {
				case 0x03:
					return fakeTemplateAttrs(req, pairTemplateAttrs())
				case 0x4c:
					start := 2 + int(req[1])*2
					offset := binary.LittleEndian.Uint32(req[start:])
					if offset == 0 {
						return append([]byte{0xcc, 0, 0x06, 0}, pairDefinition[:16]...)
					}
					if fault == "disconnect" {
						return nil
					}
					return []byte{0xcc, 0, 0xff, 0x01, 0x09, 0x21}
				}
				t.Errorf("unexpected service %x", req[0])
				return nil
			})
			c := peer.newClient(t, false)
			if tmpl, err := c.GetTemplate(0x8001); err == nil {
				t.Fatalf("truncated template returned: %+v", tmpl)
			}
			if _, ok := c.GetCachedTemplates()[1]; ok {
				t.Fatal("truncated template cached")
			}
		})
	}
}

// Item 4: a Multiple Service Packet member with status 0x06 holds only a
// prefix; it must be re-read in full rather than returned truncated.
func TestReadMultiplePartialMemberIsReadInFull(t *testing.T) {
	data := bigStructData(300)
	peer := newFakePeer(t, func(req []byte) []byte {
		switch req[0] {
		case 0x0a:
			return fakeMSP(req, func(sub []byte) []byte {
				name, rest := fakeSymbol(sub)
				switch name {
				case "A":
					return []byte{0xcc, 0, 0, 0, 0xc4, 0, 7, 0, 0, 0}
				case "S":
					return fakeFragment(sub, rest, CIPStructType, 0x4321, data, 50)
				}
				return []byte{0xcc, 0, 0x05, 0}
			})
		case 0x52:
			name, rest := fakeSymbol(req)
			if name != "S" {
				return []byte{0xd2, 0, 0x05, 0}
			}
			return fakeFragment(req, rest, CIPStructType, 0x4321, data, 120)
		}
		t.Errorf("unexpected service %x", req[0])
		return nil
	})
	c := peer.newClient(t, false)
	tags, err := c.PLC().ReadMultiple([]string{"A", "S"})
	if err != nil || len(tags) != 2 || tags[0] == nil || tags[1] == nil {
		t.Fatalf("ReadMultiple: %v %v", tags, err)
	}
	if !bytes.Equal(tags[0].Bytes, []byte{7, 0, 0, 0}) {
		t.Fatalf("complete member changed: % x", tags[0].Bytes)
	}
	if want := append(u16(0x4321), data...); tags[1].DataType != CIPStructType || !bytes.Equal(tags[1].Bytes, want) {
		t.Fatalf("partial member returned %d bytes, want %d", len(tags[1].Bytes), len(want))
	}
}

// Item 7: the general status is byte 2 of the reply (byte 1 is reserved).
func TestKeepaliveChecksGeneralStatus(t *testing.T) {
	var mu sync.Mutex
	var nop []byte
	peer := newFakePeer(t, func(req []byte) []byte {
		switch req[0] {
		case 0x5b:
			return fakeForwardOpenReply(req)
		case 0x4e:
			return []byte{0xce, 0, 0, 0}
		case 0x17:
			mu.Lock()
			defer mu.Unlock()
			return nop
		}
		t.Errorf("unexpected service %x", req[0])
		return nil
	})
	c := peer.newClient(t, true)
	for _, tc := range []struct {
		reply []byte
		ok    bool
	}{
		{[]byte{0x97, 0, 0x00, 0}, true},
		{[]byte{0x97, 0, 0x08, 0}, true},                 // service not supported: still alive
		{[]byte{0x97, 0, 0x16, 0}, false},                // object does not exist
		{[]byte{0x97, 0, 0x01, 0x01, 0x07, 0x01}, false}, // connection failure
		{[]byte{0x97, 0}, false},                         // truncated
	} {
		mu.Lock()
		nop = tc.reply
		mu.Unlock()
		if err := c.Keepalive(); (err == nil) != tc.ok {
			t.Fatalf("reply % x: err=%v", tc.reply, err)
		}
	}
}

// Item 8: only controller rejections are cached as permanent template
// failures; transport errors are retried.
func TestTemplateFailureCacheOnlyCachesControllerRejections(t *testing.T) {
	offline := &Client{plc: &PLC{Connection: eip.NewEipClient("127.0.0.1")}}
	if _, err := offline.GetTemplate(0x8005); err == nil {
		t.Fatal("template fetched without a connection")
	}
	if offline.failedTemplates[5] {
		t.Fatal("transport failure cached as permanent")
	}

	var attrRequests atomic.Int32
	peer := newFakePeer(t, func(req []byte) []byte {
		if req[0] != 0x03 {
			t.Errorf("unexpected service %x", req[0])
			return nil
		}
		attrRequests.Add(1)
		switch req[5] {
		case 6:
			return []byte{0x83, 0, 0x16, 0} // object does not exist
		case 7:
			return []byte{0x83, 0, 0x01, 0x01, 0x04, 0x02} // connection failure
		}
		return nil
	})
	c := peer.newClient(t, false)
	for i := 0; i < 2; i++ {
		if _, err := c.GetTemplate(0x8006); err == nil {
			t.Fatal("rejected template succeeded")
		}
	}
	if attrRequests.Load() != 1 || !c.failedTemplates[6] {
		t.Fatalf("controller rejection not cached (%d requests)", attrRequests.Load())
	}
	for i := 0; i < 2; i++ {
		if _, err := c.GetTemplate(0x8007); err == nil {
			t.Fatal("failed template succeeded")
		}
	}
	if attrRequests.Load() != 3 || c.failedTemplates[7] {
		t.Fatalf("transient CIP failure cached (%d requests)", attrRequests.Load())
	}
}

// Item 5: a Client is used concurrently for reads, writes, keepalives, cache
// operations, and Close. Run with -race.
func TestClientConcurrentUseIsRaceFree(t *testing.T) {
	record := append(u16(0xbeef), u32(7)...)
	record = append(record, u32(0x3fc00000)...)
	peer := newFakePeer(t, func(req []byte) []byte {
		switch req[0] {
		case 0x5b:
			return fakeForwardOpenReply(req)
		case 0x4e:
			return []byte{0xce, 0, 0, 0}
		case 0x17:
			return []byte{0x97, 0, 0, 0}
		case 0x03:
			return fakeTemplateAttrs(req, pairTemplateAttrs())
		case 0x0a:
			return fakeMSP(req, func(sub []byte) []byte {
				return []byte{0xcc, 0, 0, 0, 0xc4, 0, 1, 0, 0, 0}
			})
		case 0x4c:
			if len(req) > 3 && req[2] == 0x20 && req[3] == 0x6c {
				return append([]byte{0xcc, 0, 0, 0}, pairDefinition...)
			}
			if name, _ := fakeSymbol(req); name == "S" {
				return append([]byte{0xcc, 0, 0, 0, 0xa0, 0x02}, record...)
			}
			return []byte{0xcc, 0, 0, 0, 0xc4, 0, 1, 0, 0, 0}
		case 0x4d:
			return []byte{0xcd, 0, 0, 0}
		}
		t.Errorf("unexpected service %x", req[0])
		return nil
	})
	c := peer.newClient(t, true)
	tags := []TagInfo{
		{Name: "A", TypeCode: TypeDINT, Instance: 1},
		{Name: "B", TypeCode: TypeDINT, Instance: 2},
		{Name: "S", TypeCode: 0x8001, Instance: 3},
	}
	c.SetTags(tags)

	var wg sync.WaitGroup
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				switch (g + i) % 6 {
				case 0:
					if values, err := c.Read("A", "S", "B"); err == nil {
						for _, v := range values {
							_ = v.GoValueDecoded(c)
						}
					}
				case 1:
					_ = c.Write("A", int32(i))
				case 2:
					_ = c.Keepalive()
				case 3:
					_, _ = c.GetTemplate(0x8001)
					_ = c.GetElementSize(0x8001)
				case 4:
					c.ClearTemplateCache()
					_ = c.GetCachedTemplates()
					_, _ = c.ConnectionInfo()
					_ = c.ConnectionMode()
				case 5:
					c.SetTags(tags)
					_, _ = c.ResolveTagType("S.X")
				}
				if g == 0 && i == 15 {
					c.Close()
				}
			}
		}(g)
	}
	wg.Wait()
	if conn, _ := c.PLC().activeConn(); conn != nil {
		t.Fatal("connection remained open after Close")
	}
	if !strings.Contains(c.ConnectionMode(), "Not connected") {
		t.Fatalf("mode after close: %s", c.ConnectionMode())
	}
}

// The Forward Open request layout assumed by forwardOpenTriple must match
// the builder.
func TestForwardOpenTripleMatchesBuilder(t *testing.T) {
	cfg := cip.DefaultForwardOpenConfig()
	cfg.ConnectionPath = []byte{1, 0, 0x20, 2, 0x24, 1}
	for _, build := range []func(cip.ForwardOpenConfig) ([]byte, uint16, error){cip.BuildForwardOpenRequest, cip.BuildForwardOpenRequestSmall} {
		req, serial, err := build(cfg)
		if err != nil {
			t.Fatal(err)
		}
		got, _, _, err := forwardOpenTriple(req)
		if err != nil || got != serial {
			t.Fatalf("triple serial %04x want %04x: %v", got, serial, err)
		}
	}
}
