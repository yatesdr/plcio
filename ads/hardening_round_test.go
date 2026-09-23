package ads

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// ReadState (command 4) is the keepalive probe: it decodes ADS/device state,
// keeps the stream on a device rejection and fails it on malformed replies.
func TestReadStateKeepaliveProbe(t *testing.T) {
	var mu sync.Mutex
	reply := []byte{0, 0, 0, 0, 5, 0, 3, 0}
	c := testClient(t, func(req []byte) []byte {
		if binary.LittleEndian.Uint16(req[22:24]) != CmdReadState || len(req) != 38 {
			t.Errorf("not an empty ReadState request: %x", req)
		}
		mu.Lock()
		defer mu.Unlock()
		return reply
	})
	state, device, err := c.ReadState()
	if err != nil || state != AdsStateRun || device != 3 {
		t.Fatalf("ReadState: %d %d %v", state, device, err)
	}
	mu.Lock()
	reply = []byte{0x06, 0x07, 0, 0}
	mu.Unlock()
	_, _, err = c.ReadState()
	var ads *AdsError
	if !errors.As(err, &ads) || ads.Code != 0x706 || errors.Is(err, ErrConnectionLost) || !c.IsConnected() {
		t.Fatalf("device rejection: %v connected=%v", err, c.IsConnected())
	}
	mu.Lock()
	reply = []byte{0, 0, 0, 0, 5}
	mu.Unlock()
	if _, _, err = c.ReadState(); !errors.Is(err, ErrConnectionLost) || c.IsConnected() {
		t.Fatalf("malformed ReadState kept stream: %v", err)
	}
	if _, _, err = c.ReadState(); !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("ReadState on dead stream: %v", err)
	}
}

func TestReadStateEOFIsConnectionLost(t *testing.T) {
	c := testClient(t, func(req []byte) []byte { return nil })
	if _, _, err := c.ReadState(); !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("EOF: %v", err)
	}
}

// L5: an HMI reading many distinct element paths must not hit a permanent
// "limit exceeded". The lookup caches clear on full, releasing the evicted
// handles before any replacement handle is acquired.
func TestLookupCacheEvictsInsteadOfFailing(t *testing.T) {
	var (
		mu     sync.Mutex
		events []string
		next   uint32
	)
	record := func(event string) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	}
	c := testClient(t, func(req []byte) []byte {
		group := binary.LittleEndian.Uint32(req[38:42])
		switch {
		case group == IndexGroupSymbolInfoByNameEx:
			name := strings.TrimRight(string(req[54:]), "\x00")
			return testReadReply(testSymbol(name, "DINT"))
		case group == IndexGroupSymbolHandleByName:
			mu.Lock()
			next++
			handle := next
			mu.Unlock()
			record(fmt.Sprintf("acquire %d", handle))
			return testReadReply(binary.LittleEndian.AppendUint32(nil, handle))
		case group == IndexGroupSymbolValueByHandle:
			return testReadReply([]byte{byte(binary.LittleEndian.Uint32(req[42:46])), 0, 0, 0})
		case group == IndexGroupSymbolReleaseHandle:
			record(fmt.Sprintf("release %d", binary.LittleEndian.Uint32(req[50:54])))
			return []byte{0, 0, 0, 0}
		case group == IndexGroupSumUpRead:
			return testReadReply(make([]byte, 8*int(binary.LittleEndian.Uint32(req[42:46]))))
		case group == IndexGroupSumUpWrite:
			count := int(binary.LittleEndian.Uint32(req[42:46]))
			handles := req[54+12*count:]
			for i := 0; i < count; i++ {
				record(fmt.Sprintf("release %d", binary.LittleEndian.Uint32(handles[4*i:])))
			}
			return testReadReply(make([]byte, 4*count))
		}
		t.Errorf("unexpected request %x", req)
		return nil
	})
	c.cfg.maxSymbols = 3
	for i := 0; i < 8; i++ {
		name := fmt.Sprintf("MAIN.arr[%d]", i)
		values, err := c.Read(name)
		if err != nil || values[0].Error != nil {
			t.Fatalf("read %s: %+v %v", name, values, err)
		}
	}
	if len(c.symbols) > 3 || len(c.lookupSymbols) > 3 {
		t.Fatalf("caches exceed limit: %d %d", len(c.symbols), len(c.lookupSymbols))
	}
	mu.Lock()
	defer mu.Unlock()
	// Every release precedes the next acquisition; no handle is released twice.
	released := map[string]bool{}
	sawRelease := false
	for i, event := range events {
		if strings.HasPrefix(event, "release") {
			if released[event] {
				t.Fatalf("double release %s: %v", event, events)
			}
			released[event] = true
			sawRelease = true
			continue
		}
		if i > 0 && strings.HasPrefix(events[i-1], "release") && !strings.HasPrefix(event, "acquire") {
			t.Fatalf("unexpected order: %v", events)
		}
	}
	// The three evicted handles (in any order) are released before the next
	// acquisition.
	if !sawRelease || len(events) < 7 {
		t.Fatalf("evicted handles not released before re-acquire: %v", events)
	}
	firstBatch := map[string]bool{events[3]: true, events[4]: true, events[5]: true}
	if !firstBatch["release 1"] || !firstBatch["release 2"] || !firstBatch["release 3"] || events[6] != "acquire 4" {
		t.Fatalf("evicted handles not released before re-acquire: %v", events)
	}
	// A single request larger than the whole budget still reports the limit.
	mu.Unlock()
	values, err := c.Read("A.a", "A.b", "A.c", "A.d")
	mu.Lock()
	limited := err != nil && strings.Contains(err.Error(), "limit")
	for _, v := range values {
		limited = limited || (v.Error != nil && strings.Contains(v.Error.Error(), "limit"))
	}
	if !limited {
		t.Fatalf("oversized request did not report the limit: %+v %v", values, err)
	}
}

// Catalog entries (and their handles) survive eviction; aliases of a retained
// catalog entry are dropped without releasing the shared handle.
func TestEvictLookupsKeepsCatalogEntries(t *testing.T) {
	var released []uint32
	c := testClient(t, func(req []byte) []byte {
		if binary.LittleEndian.Uint32(req[38:42]) == IndexGroupSymbolReleaseHandle {
			released = append(released, binary.LittleEndian.Uint32(req[50:54]))
			return []byte{0, 0, 0, 0}
		}
		t.Errorf("unexpected request %x", req)
		return nil
	})
	catalog := &SymbolEntry{Info: TagInfo{Name: "MAIN.n", TypeCode: TypeInt32, TypeName: "DINT", Size: 4}, Handle: 10}
	dynamic := &SymbolEntry{Info: TagInfo{Name: "MAIN.arr[1]", TypeCode: TypeInt32, TypeName: "DINT", Size: 4}, Handle: 11}
	c.symbols["MAIN.n"], c.symbols["main.n"], c.symbols["MAIN.arr[1]"] = catalog, catalog, dynamic
	c.snapshot = &schemaSnapshot{catalog: []TagInfo{catalog.Info}, symbols: map[string]*symbolRecord{"MAIN.n": {info: catalog.Info}}}
	c.lookupSymbols = map[string]*symbolRecord{"MAIN.arr[1]": {info: dynamic.Info}}
	c.lookupBytes = 100
	_, done, err := c.begin(false)
	if err != nil {
		t.Fatal(err)
	}
	c.evictLookups()
	done()
	if len(released) != 1 || released[0] != 11 || dynamic.Handle != 0 || catalog.Handle != 10 {
		t.Fatalf("released=%v dynamic=%d catalog=%d", released, dynamic.Handle, catalog.Handle)
	}
	if len(c.symbols) != 1 || c.symbols["MAIN.n"] != catalog || len(c.lookupSymbols) != 0 || c.lookupBytes != 0 {
		t.Fatalf("caches after eviction: %v %v %d", c.symbols, c.lookupSymbols, c.lookupBytes)
	}
}
