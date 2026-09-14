package ads

import (
	"encoding/binary"
	"errors"
	"os"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/yatesdr/plcio/metadata"
)

func TestCatalogDescriptionMembershipAndVersions(t *testing.T) {
	symbols, err := os.ReadFile("testdata/beckhoff/symbols.bin")
	if err != nil {
		t.Fatal(err)
	}
	types, err := os.ReadFile("testdata/beckhoff/datatypes.bin")
	if err != nil {
		t.Fatal(err)
	}
	var symbolUploads, typeUploads, lookups atomic.Int32
	c := testClient(t, func(request []byte) []byte {
		group := binary.LittleEndian.Uint32(request[38:42])
		switch group {
		case 0xf008:
			return testReadReply([]byte{1, 0, 0, 0})
		case 0xf00f:
			info := make([]byte, 24)
			binary.LittleEndian.PutUint32(info[:4], 40)
			binary.LittleEndian.PutUint32(info[4:8], 3440)
			binary.LittleEndian.PutUint32(info[8:12], 75)
			binary.LittleEndian.PutUint32(info[12:16], 13608)
			return testReadReply(info)
		case 0xf00b:
			symbolUploads.Add(1)
			return testReadReply(symbols)
		case 0xf00e:
			typeUploads.Add(1)
			return testReadReply(types)
		case 0xf009:
			lookups.Add(1)
			return testReadReply(testSymbol("MAIN.test_struct.my_dint", "DINT"))
		default:
			t.Errorf("unexpected service %x", group)
			return nil
		}
	})
	c.versionCapability, c.catalogUnavailable = 0, false
	before, err := c.AllTags()
	if err != nil || len(before) != 40 {
		t.Fatalf("catalog: %d %v", len(before), err)
	}
	c.versionCapability, c.catalogUnavailable = 0, false
	programs, err := c.Programs()
	if err != nil || !reflect.DeepEqual(programs, []string{"GVL", "Global_Version", "MAIN", "TwinCAT_SystemInfoVarList"}) {
		t.Fatalf("namespaces: %v %v", programs, err)
	}
	desc, err := c.Describe("MAIN.test_struct")
	if err != nil || desc.Type.Kind != metadata.KindStruct || len(desc.Type.Members) != 5 {
		t.Fatalf("description: %+v %v", desc, err)
	}
	desc.Type.Members[0].Name = "corrupt"
	again, err := c.Describe("MAIN.test_struct")
	if err != nil || again.Type.Members[0].Name != "my_byte" {
		t.Fatal("description corrupts schema cache")
	}
	member, err := c.Describe("MAIN.test_struct.my_dint")
	if err != nil || member.Type.Kind != metadata.KindInt || member.Type.Bits != 32 {
		t.Fatalf("member: %+v %v", member, err)
	}
	after, err := c.AllTags()
	if err != nil || !reflect.DeepEqual(before, after) || symbolUploads.Load() != 1 || typeUploads.Load() != 1 || lookups.Load() != 1 {
		t.Fatalf("membership/reloads: %d %d %d %v", symbolUploads.Load(), typeUploads.Load(), lookups.Load(), err)
	}
}

func TestEmptyCatalogPrograms(t *testing.T) {
	c := testClient(t, func(request []byte) []byte {
		group := binary.LittleEndian.Uint32(request[38:42])
		if group == 0xf008 {
			return testReadReply([]byte{1})
		}
		if group == 0xf00f {
			return testReadReply(make([]byte, 24))
		}
		t.Errorf("unexpected service %x", group)
		return nil
	})
	c.versionCapability, c.catalogUnavailable = 0, false
	programs, err := c.Programs()
	if err != nil || len(programs) != 0 || !c.symbolsLoaded || c.snapshot == nil {
		t.Fatalf("empty catalog: %v %v", programs, err)
	}
}

func TestInterruptedAndChangedUploadNeverPublishes(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(map[bool]string{false: "EOF", true: "generation"}[change], func(t *testing.T) {
			var versionReads atomic.Int32
			c := testClient(t, func(request []byte) []byte {
				group := binary.LittleEndian.Uint32(request[38:42])
				switch group {
				case 0xf008:
					version := versionReads.Add(1)
					if !change {
						version = 1
					}
					return testReadReply([]byte{byte(version), 0, 0, 0})
				case 0xf00f:
					info := make([]byte, 24)
					entry := testSymbol("MAIN.n", "DINT")
					binary.LittleEndian.PutUint32(info[:4], 1)
					binary.LittleEndian.PutUint32(info[4:8], uint32(len(entry)))
					return testReadReply(info)
				case 0xf00b:
					if !change {
						return nil
					}
					return testReadReply(testSymbol("MAIN.n", "DINT"))
				default:
					return nil
				}
			})
			c.versionCapability, c.catalogUnavailable = 0, false
			_, err := c.AllTags()
			if err == nil || c.symbolsLoaded || c.snapshot != nil {
				t.Fatalf("published interrupted/changed upload: %v", err)
			}
			if !change && !errors.Is(err, ErrConnectionLost) {
				t.Fatal(err)
			}
		})
	}
}

func TestUnsupportedMetadataAndVersionCapabilities(t *testing.T) {
	var versions, uploads atomic.Int32
	c := testClient(t, func(request []byte) []byte {
		group := binary.LittleEndian.Uint32(request[38:42])
		if group == 0xf008 {
			versions.Add(1)
		}
		if group == 0xf00f || group == 0xf00c {
			uploads.Add(1)
		}
		if group == 0xf009 {
			return testReadReply(testSymbol("MAIN.n", "DINT"))
		}
		return []byte{1, 7, 0, 0}
	})
	c.versionCapability, c.catalogUnavailable = 0, false
	desc, err := c.Describe("MAIN.n")
	if err != nil || desc.Type.Kind != metadata.KindInt {
		t.Fatalf("primitive description without publication: %+v %v", desc, err)
	}
	if _, err := c.AllTags(); err == nil {
		t.Fatal("unavailable catalog falsely successful")
	}
	if versions.Load() != 1 || uploads.Load() != 2 {
		t.Fatalf("uncached capability: versions=%d uploads=%d", versions.Load(), uploads.Load())
	}
}
