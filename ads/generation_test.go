package ads

import (
	"encoding/binary"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// Independent SDK entry layout: 42-byte datatype header, three terminated
// strings, then subitem entries. No client serializer constructs these inputs.
func wireDatatype(name, declaration string, size, offset, flags, adsType uint32, members ...[]byte) []byte {
	data := make([]byte, 42)
	binary.LittleEndian.PutUint32(data[4:8], 1)
	binary.LittleEndian.PutUint32(data[16:20], size)
	binary.LittleEndian.PutUint32(data[20:24], offset)
	binary.LittleEndian.PutUint32(data[24:28], adsType)
	binary.LittleEndian.PutUint32(data[28:32], flags)
	binary.LittleEndian.PutUint16(data[32:34], uint16(len(name)))
	binary.LittleEndian.PutUint16(data[34:36], uint16(len(declaration)))
	binary.LittleEndian.PutUint16(data[40:42], uint16(len(members)))
	data = append(data, []byte(name)...)
	data = append(data, 0)
	data = append(data, []byte(declaration)...)
	data = append(data, 0, 0)
	for _, member := range members {
		data = append(data, member...)
	}
	binary.LittleEndian.PutUint32(data[:4], uint32(len(data)))
	return data
}

func TestReadRecoversChangedLayoutsAndHandlesOnce(t *testing.T) {
	for _, mode := range []string{"equal-size", "different-size", "stale-handle", "persistent-stale"} {
		t.Run(mode, func(t *testing.T) {
			var version, trigger, reads, uploads, acquisitions atomic.Uint32
			version.Store(1)
			newSize := uint32(8)
			if mode == "different-size" {
				newSize = 12
			}
			layout := func(generation uint32) ([]byte, []byte, []byte) {
				size, name, offset, number, other := uint32(8), "before", uint32(0), byte(1), "MAIN.obsolete"
				if generation == 2 {
					size, name, offset, number, other = newSize, "after", 4, 25, "MAIN.added"
				}
				symbol := testSymbol("MAIN.record", "Record")
				binary.LittleEndian.PutUint32(symbol[8:12], generation*100)
				binary.LittleEndian.PutUint32(symbol[12:16], size)
				symbols := append(symbol, testSymbol(other, "DINT")...)
				types := wireDatatype("Record", "", size, 0, 1, 65, wireDatatype(name, "DINT", 4, offset, 2, 3))
				value := make([]byte, size)
				value[offset] = number
				return symbols, types, value
			}
			c := testClient(t, func(request []byte) []byte {
				group := binary.LittleEndian.Uint32(request[38:42])
				symbols, types, value := layout(version.Load())
				switch group {
				case 0xf008:
					return testReadReply([]byte{byte(version.Load()), 0, 0, 0})
				case 0xf00f:
					info := make([]byte, 24)
					binary.LittleEndian.PutUint32(info[:4], 2)
					binary.LittleEndian.PutUint32(info[4:8], uint32(len(symbols)))
					binary.LittleEndian.PutUint32(info[8:12], 1)
					binary.LittleEndian.PutUint32(info[12:16], uint32(len(types)))
					return testReadReply(info)
				case 0xf00b:
					uploads.Add(1)
					return testReadReply(symbols)
				case 0xf00e:
					return testReadReply(types)
				case 0xf003:
					acquisitions.Add(1)
					return testReadReply([]byte{byte(version.Load()), 0, 0, 0})
				case 0xf005:
					reads.Add(1)
					if trigger.CompareAndSwap(1, 2) {
						version.Store(2)
						if mode == "stale-handle" || mode == "persistent-stale" {
							return []byte{0x11, 7, 0, 0}
						}
						return testReadReply(value)
					}
					if mode == "persistent-stale" && trigger.Load() == 2 {
						return []byte{0x11, 7, 0, 0}
					}
					if binary.LittleEndian.Uint32(request[42:46]) != version.Load() {
						t.Error("old handle reused")
					}
					return testReadReply(value)
				case 0xf006:
					return []byte{0, 0, 0, 0}
				default:
					t.Errorf("unexpected service %x", group)
					return nil
				}
			})
			c.versionCapability, c.catalogUnavailable = 0, false
			warm, err := c.ReadDecoded("MAIN.record")
			if err != nil || !reflect.DeepEqual(warm[0].Value, map[string]any{"before": int64(1)}) {
				t.Fatal(warm, err)
			}
			trigger.Store(1)
			values, err := c.ReadDecoded("MAIN.record")
			if mode == "persistent-stale" {
				if !staleSymbolError(err) || values[0].Value != nil || values[0].Raw.Error == nil {
					t.Fatalf("stale successful decode: %v %v", values, err)
				}
			} else {
				if err != nil || values[0].Raw.Error != nil || !reflect.DeepEqual(values[0].Value, map[string]any{"after": int64(25)}) {
					t.Fatalf("new generation: %+v %v", values[0], err)
				}
				tags, err := c.AllTags()
				if err != nil || len(tags) != 2 || tags[0].Name != "MAIN.added" || tags[1].IndexOffset != 200 {
					t.Fatalf("new catalog: %v %v", tags, err)
				}
			}
			if reads.Load() != 3 || uploads.Load() != 2 || acquisitions.Load() != 2 {
				t.Fatalf("unbounded recovery: reads=%d uploads=%d handles=%d", reads.Load(), uploads.Load(), acquisitions.Load())
			}
		})
	}
}

func TestCachedMissingCatalogDoesNotHideVersionFailure(t *testing.T) {
	for _, operation := range []string{"Read", "ReadDecoded", "Write", "Describe"} {
		t.Run(operation, func(t *testing.T) {
			c := testClient(t, func([]byte) []byte { return nil })
			c.versionCapability = 0
			seedDINT(c, "MAIN.n", 42)
			var err error
			switch operation {
			case "Read":
				_, err = c.Read("MAIN.n")
			case "ReadDecoded":
				_, err = c.ReadDecoded("MAIN.n")
			case "Write":
				err = c.Write("MAIN.n", int64(1))
			case "Describe":
				_, err = c.Describe("MAIN.n")
			}
			if !errors.Is(err, ErrConnectionLost) || c.IsConnected() {
				t.Fatalf("version EOF hidden: %v", err)
			}
		})
	}
}

func TestWriteNeverReplaysValueAfterStaleReply(t *testing.T) {
	var writes atomic.Int32
	c := testClient(t, func(request []byte) []byte {
		if binary.LittleEndian.Uint32(request[38:42]) == 0xf005 {
			writes.Add(1)
			return []byte{0x11, 7, 0, 0}
		}
		return []byte{0, 0, 0, 0}
	})
	seedDINT(c, "MAIN.n", 42)
	err := c.Write("MAIN.n", int64(1))
	if !staleSymbolError(err) || writes.Load() != 1 || len(c.symbols) != 0 {
		t.Fatalf("write replay/stale cache: %v writes=%d", err, writes.Load())
	}
}

func TestRecoveryKeepsOriginalDeadline(t *testing.T) {
	var versions atomic.Int32
	c := testClient(t, func(request []byte) []byte {
		if binary.LittleEndian.Uint32(request[38:42]) == 0xf008 {
			versions.Add(1)
			return testReadReply([]byte{1})
		}
		time.Sleep(30 * time.Millisecond)
		return []byte{0x11, 7, 0, 0}
	})
	c.versionCapability, c.catalogUnavailable, c.cfg.timeout = 0, false, 40*time.Millisecond
	start := time.Now()
	_, err := c.ReadDecoded("MAIN.n")
	if err == nil || time.Since(start) > 120*time.Millisecond || versions.Load() != 2 {
		t.Fatalf("recovery budget: %v %s versions=%d", err, time.Since(start), versions.Load())
	}
}
