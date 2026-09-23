package ads

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testReadReply(data []byte) []byte {
	reply := make([]byte, 8+len(data))
	binary.LittleEndian.PutUint32(reply[4:8], uint32(len(data)))
	copy(reply[8:], data)
	return reply
}

// Build envelopes independently of the client's serializer. The request is
// checked by individual tests, and the response swaps the exact wire endpoints.
func testResponse(request, body []byte) []byte {
	response := make([]byte, 38+len(body))
	binary.LittleEndian.PutUint32(response[2:6], uint32(32+len(body)))
	copy(response[6:14], request[14:22])
	copy(response[14:22], request[6:14])
	copy(response[22:24], request[22:24])
	response[24] = 5
	binary.LittleEndian.PutUint32(response[26:30], uint32(len(body)))
	copy(response[34:38], request[34:38])
	copy(response[38:], body)
	return response
}

func testRequest(conn net.Conn) ([]byte, error) {
	header := make([]byte, 6)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	size := binary.LittleEndian.Uint32(header[2:6])
	if size > 1<<20 || size < 32 {
		return nil, errors.New("bad test request size")
	}
	request := make([]byte, 6+int(size))
	copy(request, header)
	_, err := io.ReadFull(conn, request[6:])
	return request, err
}

func testClient(t *testing.T, handler func([]byte) []byte) *Client {
	t.Helper()
	local, peer := net.Pipe()
	c := &Client{conn: newAdsConnection(local, AmsNetId{127, 0, 0, 1, 1, 1}, 32900),
		targetNetId: AmsNetId{5, 45, 219, 226, 1, 1}, targetPort: 851, connected: true,
		cfg: defaultOptions(), symbols: make(map[string]*SymbolEntry)}
	c.cfg.timeout = 200 * time.Millisecond
	c.versionCapability = 2
	c.catalogUnavailable = true
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer peer.Close()
		for {
			request, err := testRequest(peer)
			if err != nil {
				return
			}
			body := handler(request)
			if body == nil {
				return
			} // inject EOF
			if _, err := peer.Write(testResponse(request, body)); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { c.Close(); local.Close(); peer.Close(); <-finished })
	return c
}

func seedDINT(c *Client, name string, handle uint32) {
	c.symbols[name] = &SymbolEntry{Info: TagInfo{Name: name, TypeCode: TypeInt32,
		TypeName: "DINT", Size: 4}, Handle: handle}
}

func TestReleaseHandleWireAndError(t *testing.T) {
	want := []byte{6, 0xf0, 0, 0, 0, 0, 0, 0, 4, 0, 0, 0, 0x78, 0x56, 0x34, 0x12}
	c := testClient(t, func(request []byte) []byte {
		if binary.LittleEndian.Uint16(request[22:24]) != 3 || !bytes.Equal(request[38:], want) {
			t.Errorf("release wire: %x", request)
		}
		return []byte{6, 7, 0, 0}
	})
	_, done, err := c.begin(false)
	if err != nil {
		t.Fatal(err)
	}
	err = c.releaseHandleUnsafe(0x12345678)
	done()
	var device *AdsError
	if !errors.As(err, &device) || device.Code != 0x706 {
		t.Fatalf("release lost ADS code: %v", err)
	}
	if !c.IsConnected() {
		t.Fatal("device rejection disconnected stream")
	}
}

func TestShortReadWriteErrorPreserved(t *testing.T) {
	c := testClient(t, func(request []byte) []byte { return []byte{0x10, 7, 0, 0, 0, 0, 0, 0} })
	seedDINT(c, "MAIN.n", 0)
	values, err := c.Read("MAIN.n")
	if err != nil || len(values) != 1 {
		t.Fatalf("read: %v %v", values, err)
	}
	var device *AdsError
	if !errors.As(values[0].Error, &device) || device.Code != 0x710 {
		t.Fatalf("handle error: %v", values[0].Error)
	}
}

func TestWriteEOFDisconnectsAndRetainsCause(t *testing.T) {
	c := testClient(t, func(request []byte) []byte { return nil })
	seedDINT(c, "MAIN.n", 42)
	err := c.Write("MAIN.n", int64(25))
	if !errors.Is(err, ErrConnectionLost) || !errors.Is(err, io.EOF) {
		t.Fatalf("lost write error identities: %v", err)
	}
	if c.IsConnected() {
		t.Fatal("EOF leaves client connected")
	}
}

func TestConcurrentHandleAcquisition(t *testing.T) {
	var handles, reads, releases atomic.Int32
	c := testClient(t, func(request []byte) []byte {
		cmd := binary.LittleEndian.Uint16(request[22:24])
		group := binary.LittleEndian.Uint32(request[38:42])
		switch {
		case cmd == 9 && group == 0xf003:
			handles.Add(1)
			return testReadReply([]byte{42, 0, 0, 0})
		case cmd == 2 && group == 0xf005:
			reads.Add(1)
			return testReadReply([]byte{25, 0, 0, 0})
		case cmd == 3 && group == 0xf006:
			releases.Add(1)
			return []byte{0, 0, 0, 0}
		default:
			t.Errorf("unexpected wire: %x", request)
			return nil
		}
	})
	seedDINT(c, "MAIN.n", 0)
	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			values, err := c.Read("MAIN.n")
			if err != nil || len(values) != 1 || values[0].Error != nil || values[0].GoValue() != int64(25) {
				t.Errorf("read %v: %v", values, err)
			}
		}()
	}
	wg.Wait()
	c.Close()
	if handles.Load() != 1 || reads.Load() != 64 || releases.Load() != 1 {
		t.Fatalf("handles=%d reads=%d releases=%d", handles.Load(), reads.Load(), releases.Load())
	}
}

func TestPartialReadEOF(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(request []byte) []byte {
		if calls.Add(1) == 1 {
			return testReadReply([]byte{25, 0, 0, 0})
		}
		return nil
	})
	c.cfg.maxBatch = 1
	seedDINT(c, "MAIN.a", 1)
	seedDINT(c, "MAIN.b", 2)
	seedDINT(c, "MAIN.c", 3)
	values, err := c.Read("MAIN.a", "MAIN.b", "MAIN.c")
	if !errors.Is(err, ErrConnectionLost) || !errors.Is(err, io.EOF) || len(values) != 3 {
		t.Fatalf("partial read: %v %v", values, err)
	}
	if values[0].GoValue() != int64(25) || values[1].Error == nil || values[2].Error == nil || calls.Load() != 2 {
		t.Fatalf("slots/calls: %v %d", values, calls.Load())
	}
}

func TestF080FailedSlotStillConsumesRequestedBytes(t *testing.T) {
	// Independent F080 fixture: two result words, then both requested 4-byte
	// slots. First fails, second must still decode from its own slot. The
	// failure is access-denied: not-found through a cached handle is stale and
	// correctly triggers one re-resolution (TestCachedHandleNotFoundReResolvesOnce).
	c := testClient(t, func(request []byte) []byte {
		group := binary.LittleEndian.Uint32(request[38:42])
		if group == 0xf006 {
			return []byte{0, 0, 0, 0}
		}
		if group != 0xf080 {
			t.Errorf("batch group: %x", group)
		}
		return testReadReply([]byte{0x23, 7, 0, 0, 0, 0, 0, 0, 0xaa, 0xbb, 0xcc, 0xdd, 25, 0, 0, 0})
	})
	seedDINT(c, "MAIN.a", 1)
	seedDINT(c, "MAIN.b", 2)
	values, err := c.Read("MAIN.a", "MAIN.b")
	if err != nil || values[0].Error == nil || values[1].GoValue() != int64(25) {
		t.Fatalf("failed-slot cursor: %v %v", values, err)
	}
}

func TestCapturedSymbolTable(t *testing.T) {
	data, err := os.ReadFile("testdata/beckhoff/symbols.bin")
	if err != nil {
		t.Fatal(err)
	}
	tags, err := parseSymbolTable(data, 40, 100000)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 40 || len(data) != 3440 {
		t.Fatalf("capture counts: %d %d", len(tags), len(data))
	}
	var found bool
	for _, tag := range tags {
		if tag.Name == "MAIN.test_struct" {
			found = true
			if tag.Size != 124 {
				t.Fatal(tag)
			}
		}
	}
	if !found {
		t.Fatal("record omitted from catalog")
	}
}

func TestSymbolFlagsIndependent(t *testing.T) {
	for _, flag := range []uint32{1, 2, 4, 8, 16, 32} {
		info := TagInfo{Flags: flag}
		if info.IsWritable() != (flag != 32) {
			t.Errorf("flag 0x%x writable=%v", flag, info.IsWritable())
		}
	}
	if SymFlagReadOnly != 32 || SymFlagTypeGUID != 8 || SymFlagReferenceTo != 4 || SymFlagInterfacePointer != 16 {
		t.Fatal("documented flags changed")
	}
	if IndexGroupSymbolValueByName != 0xf004 || IndexGroupDataTypeInfoByNameEx != 0xf011 {
		t.Fatal("incorrect service groups")
	}
}

func TestOptionsValidateBeforeDial(t *testing.T) {
	for _, opts := range [][]Option{
		{WithAmsNetId("bad")}, {WithTimeout(0)}, {WithTimeout(-time.Second)},
		{WithAmsPort(0)}, {WithLocalAmsNetId("bad")}, {WithLocalAmsPort(0)},
		{WithMaxPayload(0)}, {WithMaxBatchItems(0)}, {WithMetadataLimits(0, 1, 1)},
		{WithExpansionLimits(0, 1)}, {nil},
	} {
		if _, err := Connect("192.168.5.212", opts...); err == nil {
			t.Fatalf("accepted bad options %v", opts)
		}
	}
	endpoint, cfg, err := configure("192.168.5.212:49999", []Option{WithAmsNetId("5.45.219.226.1.1"), WithTimeout(time.Second)})
	if err != nil || endpoint != "192.168.5.212:49999" || cfg.targetNetId != (AmsNetId{5, 45, 219, 226, 1, 1}) || cfg.timeout != time.Second {
		t.Fatalf("endpoint/options: %s %v %v", endpoint, cfg, err)
	}
	if _, _, err := configure("plc.local", nil); err == nil {
		t.Fatal("manufactured hostname AMS identity")
	}
}

func TestOperationWaitBounded(t *testing.T) {
	c := testClient(t, func(request []byte) []byte { return []byte{0, 0, 0, 0} })
	c.cfg.timeout = 20 * time.Millisecond
	_, done, err := c.begin(false)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	start := time.Now()
	_, _, err = c.begin(false)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("unbounded gate wait: %v", err)
	}
}

func TestEveryCommandTransportFailure(t *testing.T) {
	for _, operation := range []string{"identity", "symbol", "handle", "catalog", "read", "write", "release"} {
		t.Run(operation, func(t *testing.T) {
			c := testClient(t, func(request []byte) []byte { return nil })
			var err error
			switch operation {
			case "identity":
				_, err = c.GetDeviceInfo()
			case "symbol":
				_, err = c.Read("MAIN.missing")
			case "handle":
				seedDINT(c, "MAIN.n", 0)
				_, err = c.Read("MAIN.n")
			case "catalog":
				c.catalogUnavailable = false
				_, err = c.AllTags()
			case "read":
				seedDINT(c, "MAIN.n", 42)
				_, err = c.Read("MAIN.n")
			case "write":
				seedDINT(c, "MAIN.n", 42)
				err = c.Write("MAIN.n", int64(25))
			case "release":
				_, done, beginErr := c.begin(false)
				if beginErr != nil {
					t.Fatal(beginErr)
				}
				err = c.releaseHandleUnsafe(42)
				done()
			}
			if !errors.Is(err, ErrConnectionLost) || !errors.Is(err, io.EOF) || c.IsConnected() {
				t.Fatalf("%s failure: %v, connected=%v", operation, err, c.IsConnected())
			}
		})
	}
}
