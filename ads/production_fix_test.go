package ads

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TwinCAT resolves names case-insensitively and F009 reports the canonical
// spelling. Every spelling shares one entry/handle; results keep the request.
func TestCaseInsensitiveSymbolNames(t *testing.T) {
	var lookups, handles, writes atomic.Int32
	c := testClient(t, func(req []byte) []byte {
		switch binary.LittleEndian.Uint32(req[38:42]) {
		case 0xf009:
			lookups.Add(1)
			if !strings.EqualFold(strings.TrimRight(string(req[54:]), "\x00"), "MAIN.n") {
				return []byte{0x10, 7, 0, 0}
			}
			return testReadReply(testSymbol("MAIN.n", "DINT"))
		case 0xf003:
			handles.Add(1)
			return testReadReply([]byte{7, 0, 0, 0})
		case 0xf005:
			if binary.LittleEndian.Uint32(req[42:46]) != 7 {
				t.Errorf("value access through handle %x", req[42:46])
			}
			if binary.LittleEndian.Uint16(req[22:24]) == 3 {
				writes.Add(1)
				return []byte{0, 0, 0, 0}
			}
			return testReadReply([]byte{25, 0, 0, 0})
		case 0xf006:
			return []byte{0, 0, 0, 0}
		}
		t.Errorf("unexpected service %x", req)
		return nil
	})
	for _, name := range []string{"main.n", "MAIN.n", "Main.N", "main.n"} {
		values, err := c.Read(name)
		if err != nil || len(values) != 1 || values[0].Error != nil || values[0].Name != name || values[0].GoValue() != int64(25) {
			t.Fatalf("read %q: %+v %v", name, values, err)
		}
	}
	desc, err := c.Describe("main.n")
	if err != nil || desc.Name != "main.n" || !desc.Writable {
		t.Fatalf("describe: %+v %v", desc, err)
	}
	if err := c.Write("MAIN.N", int64(1)); err != nil || writes.Load() != 1 {
		t.Fatalf("write: %v writes=%d", err, writes.Load())
	}
	// "MAIN.n" hits the canonical entry; "main.n" and "Main.N" are cached aliases.
	if lookups.Load() != 3 || handles.Load() != 1 {
		t.Fatalf("lookups=%d handle acquisitions=%d", lookups.Load(), handles.Load())
	}
	values, err := c.Read("MAIN.other")
	var device *AdsError
	if err != nil || !errors.As(values[0].Error, &device) || device.Code != ErrDeviceSymbolNotFound {
		t.Fatalf("unrelated name resolved: %+v %v", values, err)
	}
}

func TestCaseInsensitiveCatalogLookupKeepsAccessRules(t *testing.T) {
	record := testSymbol("MAIN.rec", "Record")
	binary.LittleEndian.PutUint32(record[16:20], 65)
	locked := testSymbol("MAIN.rec.locked", "DINT")
	binary.LittleEndian.PutUint32(locked[20:24], SymFlagReadOnly)
	symbols := append(append(testSymbol("MAIN.n", "DINT"), record...), locked...)
	types := wireDatatype("Record", "", 4, 0, 1, 65, wireDatatype("locked", "DINT", 4, 0, 2, 3))
	var lookups, writes atomic.Int32
	c := testClient(t, func(req []byte) []byte {
		switch binary.LittleEndian.Uint32(req[38:42]) {
		case 0xf00f:
			info := make([]byte, 24)
			binary.LittleEndian.PutUint32(info[:4], 3)
			binary.LittleEndian.PutUint32(info[4:8], uint32(len(symbols)))
			binary.LittleEndian.PutUint32(info[8:12], 1)
			binary.LittleEndian.PutUint32(info[12:16], uint32(len(types)))
			return testReadReply(info)
		case 0xf00b:
			return testReadReply(symbols)
		case 0xf00e:
			return testReadReply(types)
		case 0xf009:
			lookups.Add(1)
			return []byte{0x10, 7, 0, 0}
		case 0xf003:
			return testReadReply([]byte{9, 0, 0, 0})
		case 0xf005:
			if binary.LittleEndian.Uint16(req[22:24]) == 3 {
				writes.Add(1)
				return []byte{0, 0, 0, 0}
			}
			return testReadReply([]byte{25, 0, 0, 0})
		case 0xf006:
			return []byte{0, 0, 0, 0}
		}
		t.Errorf("unexpected service %x", req)
		return nil
	})
	c.catalogUnavailable = false
	values, err := c.ReadDecoded("main.N")
	if err != nil || values[0].Raw.Error != nil || values[0].Raw.Name != "main.N" || values[0].Value != int64(25) {
		t.Fatalf("catalog read: %+v %v", values, err)
	}
	for _, input := range []any{map[string]any{"locked": int64(1)}, []byte{1, 0, 0, 0}} {
		if err := c.Write("main.REC", input); err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Fatalf("differently cased write bypassed read-only member: %v", err)
		}
	}
	if err := c.Write("Main.Rec.Locked", int64(1)); err == nil {
		t.Fatal("differently cased read-only symbol written")
	}
	desc, err := c.Describe("main.rec")
	if err != nil || desc.Name != "main.rec" || !desc.Type.Members[0].ReadOnly {
		t.Fatalf("describe access: %+v %v", desc, err)
	}
	if lookups.Load() != 0 || writes.Load() != 0 {
		t.Fatalf("catalog names not resolved locally: lookups=%d writes=%d", lookups.Load(), writes.Load())
	}
}

func TestCaseInsensitivePackedMemberLookup(t *testing.T) {
	lookup, err := os.ReadFile("testdata/beckhoff-bit-lookups-2026-09-14/MAIN.test_bitpacked_struct.my_bit2.f009.bin")
	if err != nil {
		t.Fatal(err)
	}
	symbols, err := os.ReadFile("testdata/beckhoff/symbols.bin")
	if err != nil {
		t.Fatal(err)
	}
	_, records, err := parseSymbolRecords(symbols, 40, defaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	entries := capturedTypes(t)
	var writes atomic.Int32
	c := testClient(t, func(req []byte) []byte {
		switch binary.LittleEndian.Uint32(req[38:42]) {
		case 0xf009:
			return testReadReply(lookup)
		case 0xf003:
			return testReadReply([]byte{42, 0, 0, 0})
		case 0xf005:
			if binary.LittleEndian.Uint16(req[22:24]) == 3 {
				if !bytes.Equal(req[50:], []byte{1}) {
					t.Errorf("bit write %x", req)
				}
				writes.Add(1)
				return []byte{0, 0, 0, 0}
			}
			return testReadReply([]byte{1})
		case 0xf006:
			return []byte{0, 0, 0, 0}
		}
		t.Errorf("unexpected service %x", req)
		return nil
	})
	c.catalogUnavailable = false
	c.snapshot = &schemaSnapshot{entries: entries, resolver: newResolver(entries, c.cfg), symbols: records}
	name := "main.TEST_BITPACKED_STRUCT.My_Bit2"
	values, err := c.ReadDecoded(name)
	if err != nil || values[0].Raw.Error != nil || values[0].Value != true || values[0].Raw.Name != name {
		t.Fatalf("bit member read: %+v %v", values, err)
	}
	if record := c.lookupSymbols["MAIN.test_bitpacked_struct.my_bit2"]; record == nil {
		t.Fatal("bit lookup not recorded under its published spelling")
	}
	if err := c.Write(name, true); err != nil || writes.Load() != 1 {
		t.Fatalf("bit member write: %v writes=%d", err, writes.Load())
	}
	values, _ = c.ReadDecoded("MAIN.test_bitpacked_struct.nope")
	if values[0].Raw.Error == nil || !strings.Contains(values[0].Raw.Error.Error(), "identity mismatch") {
		t.Fatalf("unknown member accepted: %+v", values[0].Raw)
	}
}

// handlePLC models a runtime whose handles can be invalidated by a download
// that leaves the symbol version unchanged.
type handlePLC struct {
	mu        sync.Mutex
	next      uint32
	valid     map[uint32]string
	missing   map[string]bool
	released  []uint32
	sumUpRel  int
	noSumUp   bool
	lookups   map[string]int
	reads     int
	writes    int
	acquired  int
	version   uint32
	versioned bool
}

func (p *handlePLC) serve(t *testing.T) func([]byte) []byte {
	return func(req []byte) []byte {
		p.mu.Lock()
		defer p.mu.Unlock()
		cmd := binary.LittleEndian.Uint16(req[22:24])
		group, offset := binary.LittleEndian.Uint32(req[38:42]), binary.LittleEndian.Uint32(req[42:46])
		switch group {
		case 0xf008:
			if !p.versioned {
				return []byte{1, 7, 0, 0}
			}
			return testReadReply(binary.LittleEndian.AppendUint32(nil, p.version))
		case 0xf009:
			name := strings.TrimRight(string(req[54:]), "\x00")
			p.lookups[name]++
			if p.missing[name] {
				return []byte{0x10, 7, 0, 0}
			}
			canonical := "MAIN." + strings.ToLower(name[5:]) // published spelling
			return testReadReply(testSymbol(canonical, "DINT"))
		case 0xf003:
			name := strings.TrimRight(string(req[54:]), "\x00")
			if p.missing[name] {
				return []byte{0x10, 7, 0, 0}
			}
			p.next++
			p.acquired++
			p.valid[p.next] = name
			return testReadReply(binary.LittleEndian.AppendUint32(nil, p.next))
		case 0xf005:
			if p.valid[offset] == "" {
				return []byte{0x10, 7, 0, 0}
			}
			if cmd == 3 {
				p.writes++
				return []byte{0, 0, 0, 0}
			}
			p.reads++
			return testReadReply([]byte{byte(offset), 0, 0, 0})
		case 0xf080:
			p.reads++
			data := make([]byte, 4*offset)
			for i := range offset {
				handle := binary.LittleEndian.Uint32(req[54+12*i+4:])
				if p.valid[handle] == "" {
					binary.LittleEndian.PutUint32(data[4*i:], 0x710)
				}
			}
			for i := range offset {
				data = append(data, byte(binary.LittleEndian.Uint32(req[54+12*i+4:])), 0, 0, 0)
			}
			return testReadReply(data)
		case 0xf081:
			if p.noSumUp {
				return []byte{1, 7, 0, 0}
			}
			p.sumUpRel++
			for i := range offset {
				if binary.LittleEndian.Uint32(req[54+12*i:]) != 0xf006 || binary.LittleEndian.Uint32(req[54+12*i+8:]) != 4 {
					t.Errorf("SumUp release header %x", req[54:])
				}
				handle := binary.LittleEndian.Uint32(req[54+12*offset+4*i:])
				p.released = append(p.released, handle)
				delete(p.valid, handle)
			}
			return testReadReply(make([]byte, 4*offset))
		case 0xf006:
			handle := binary.LittleEndian.Uint32(req[50:54])
			p.released = append(p.released, handle)
			delete(p.valid, handle)
			return []byte{0, 0, 0, 0}
		}
		t.Errorf("unexpected service %x", req)
		return nil
	}
}

func newHandlePLC() *handlePLC {
	return &handlePLC{valid: map[uint32]string{}, missing: map[string]bool{}, lookups: map[string]int{}}
}

// A download that leaves the version counter unchanged makes cached handles
// report 0x0710. One re-resolution recovers; a genuinely removed symbol reports
// not-found after that single retry without poisoning other slots.
func TestCachedHandleNotFoundReResolvesOnce(t *testing.T) {
	for _, batch := range []uint32{1, 500} {
		plc := newHandlePLC()
		c := testClient(t, plc.serve(t))
		c.cfg.maxBatch = batch
		names := []string{"MAIN.a", "MAIN.gone", "MAIN.b"}
		values, err := c.Read(names...)
		if err != nil {
			t.Fatal(values, err)
		}
		plc.mu.Lock()
		clear(plc.valid) // runtime download: every old handle is now unknown
		plc.missing["MAIN.gone"] = true
		plc.mu.Unlock()
		values, err = c.Read(names...)
		if err != nil {
			t.Fatalf("batch %d: %v", batch, err)
		}
		for i, value := range values {
			if value.Name != names[i] {
				t.Fatalf("slot name %q", value.Name)
			}
			var device *AdsError
			if names[i] == "MAIN.gone" {
				if !errors.As(value.Error, &device) || device.Code != ErrDeviceSymbolNotFound {
					t.Fatalf("batch %d removed symbol: %+v", batch, value)
				}
			} else if value.Error != nil || value.GoValue() == nil {
				t.Fatalf("batch %d slot %s not recovered: %+v", batch, names[i], value)
			}
		}
		plc.mu.Lock()
		if plc.lookups["MAIN.gone"] != 2 || plc.lookups["MAIN.a"] != 2 || plc.acquired != 5 {
			t.Fatalf("batch %d: unbounded or missing re-resolution: lookups=%v acquired=%d", batch, plc.lookups, plc.acquired)
		}
		plc.mu.Unlock()
		// An uncached not-found lookup is final: nothing is invalidated.
		if values, err := c.Read("MAIN.gone", "MAIN.a"); err != nil || values[0].Error == nil || values[1].Error != nil {
			t.Fatalf("batch %d missing lookup: %+v %v", batch, values, err)
		}
		plc.mu.Lock()
		if plc.lookups["MAIN.gone"] != 3 || plc.lookups["MAIN.a"] != 2 || plc.acquired != 5 {
			t.Fatalf("batch %d: not-found lookup invalidated caches: lookups=%v acquired=%d", batch, plc.lookups, plc.acquired)
		}
		plc.mu.Unlock()
	}
}

func TestCachedHandleNotFoundWriteIsNotReplayed(t *testing.T) {
	plc := newHandlePLC()
	c := testClient(t, plc.serve(t))
	if err := c.Write("MAIN.a", int64(1)); err != nil {
		t.Fatal(err)
	}
	plc.mu.Lock()
	clear(plc.valid)
	plc.mu.Unlock()
	err := c.Write("MAIN.a", int64(2))
	var device *AdsError
	if !errors.As(err, &device) || device.Code != ErrDeviceSymbolNotFound || len(c.symbols) != 0 {
		t.Fatalf("stale write: %v cached=%d", err, len(c.symbols))
	}
	if err := c.Write("MAIN.a", int64(3)); err != nil {
		t.Fatalf("write after re-resolution: %v", err)
	}
	plc.mu.Lock()
	defer plc.mu.Unlock()
	if plc.writes != 2 || plc.acquired != 2 {
		t.Fatalf("write replayed or handle not re-resolved: writes=%d acquired=%d", plc.writes, plc.acquired)
	}
}

// Live invalidation releases the discarded handles before new acquisitions,
// batched through SumUp write, and release failures never fail the read.
func TestLiveInvalidationReleasesHandles(t *testing.T) {
	for _, mode := range []string{"sumup", "individual", "rejected"} {
		t.Run(mode, func(t *testing.T) {
			plc := newHandlePLC()
			plc.versioned, plc.version = true, 1
			plc.noSumUp = mode == "individual"
			serve := plc.serve(t)
			c := testClient(t, func(req []byte) []byte {
				if mode == "rejected" && (binary.LittleEndian.Uint32(req[38:42]) == 0xf081) {
					return []byte{6, 7, 0, 0}
				}
				return serve(req)
			})
			c.versionCapability = 0
			names := []string{"MAIN.a", "MAIN.b", "MAIN.c"}
			if values, err := c.Read(names...); err != nil || values[2].Error != nil {
				t.Fatal(values, err)
			}
			if _, err := c.Read("main.A"); err != nil { // alias shares handle 1
				t.Fatal(err)
			}
			plc.mu.Lock()
			plc.version = 2
			plc.mu.Unlock()
			values, err := c.Read(names...)
			if err != nil || values[0].Error != nil || values[2].Error != nil {
				t.Fatalf("read after version change: %+v %v", values, err)
			}
			plc.mu.Lock()
			defer plc.mu.Unlock()
			want := []uint32{1, 2, 3}
			if mode == "rejected" {
				want = nil
			}
			released := append([]uint32(nil), plc.released...)
			if len(released) != len(want) {
				t.Fatalf("released %v, want %v", released, want)
			}
			seen := map[uint32]bool{}
			for _, handle := range released {
				if seen[handle] || handle > 3 {
					t.Fatalf("released %v: duplicate or new handle", released)
				}
				seen[handle] = true
			}
			if mode == "sumup" && plc.sumUpRel != 1 {
				t.Fatalf("SumUp releases %d", plc.sumUpRel)
			}
			if plc.acquired != 6 {
				t.Fatalf("acquisitions %d", plc.acquired)
			}
		})
	}
}

func TestCatalogUploadMayExceedCommandPayload(t *testing.T) {
	var symbols []byte
	for _, name := range []string{"MAIN.first_long_symbol_name", "MAIN.second_long_symbol_name", "MAIN.third_long_symbol_name"} {
		symbols = append(symbols, testSymbol(name, "DINT")...)
	}
	c := testClient(t, func(req []byte) []byte {
		switch binary.LittleEndian.Uint32(req[38:42]) {
		case 0xf00f:
			info := make([]byte, 24)
			binary.LittleEndian.PutUint32(info[:4], 3)
			binary.LittleEndian.PutUint32(info[4:8], uint32(len(symbols)))
			return testReadReply(info)
		case 0xf00b:
			if binary.LittleEndian.Uint32(req[46:50]) != uint32(len(symbols)) {
				t.Errorf("upload request size %x", req)
			}
			return testReadReply(symbols)
		}
		t.Errorf("unexpected service %x", req)
		return nil
	})
	c.catalogUnavailable = false
	c.conn.maxPayload = 64 // far below the 193-byte advertised symbol table
	tags, err := c.AllTags()
	if err != nil || len(tags) != 3 {
		t.Fatalf("upload bounded by value payload: %v %v", tags, err)
	}
	// The aggregate metadata budget still bounds the advertised upload.
	c.invalidateCaches()
	c.cfg.maxMetadata = uint32(len(symbols)) - 1
	if _, err := c.AllTags(); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("metadata limit ignored: %v", err)
	}
}

// Every broadcast destination receives its probe; a silent first destination
// no longer consumes the whole window.
func TestBroadcastProbesEveryDestination(t *testing.T) {
	silent, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer silent.Close()
	responder, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer responder.Close()
	go func() {
		buf := make([]byte, 64)
		for {
			n, from, err := responder.ReadFrom(buf)
			if err != nil {
				return
			}
			if n == 24 && binary.LittleEndian.Uint32(buf[:4]) == 0x71146603 {
				responder.WriteTo(udpIdentity("second"), from)
			}
		}
	}()
	start := time.Now()
	devices := discoverUDP([]string{silent.LocalAddr().String(), responder.LocalAddr().String()}, 300*time.Millisecond)
	if len(devices) != 1 || devices[0].Hostname != "second" {
		t.Fatalf("second destination not probed: %+v", devices)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("discovery exceeded its window: %s", elapsed)
	}
}
