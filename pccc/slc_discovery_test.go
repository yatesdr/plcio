package pccc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDiagnosticSLCNames(t *testing.T) {
	// F2 diagnostic data; contains no directory or plant values.
	data := append([]byte{0, 0xEE, 0x4A, 0x79, 0x66}, []byte("5/05       ")...)
	data = append(data, 0, 0, 0x86, 0, 0x6c, 0x31, 0x40, 0xfe)
	catalog, processor, err := parseDiagnosticStatusData(data)
	if err != nil || catalog != "5/05" || !bytes.Equal(processor, data[1:5]) {
		t.Fatalf("%q % X %v", catalog, processor, err)
	}
	for _, name := range []string{"5/05", " 5/05 ", "5/04", "5/03", "1747-l553"} {
		// 5/03 and 5/04 names are extrapolated from pycomm3's default layout.
		info, err := sys0InfoForCatalog(name)
		if err != nil || info.FileType != 1 || info.FilePosition != 79 {
			t.Fatalf("%q: %+v %v", name, info, err)
		}
	}
	for _, name := range []string{"5/02", "5/01", "1747-L511", "1761-NET", "1756-EN2T", ""} {
		if _, err := sys0InfoForCatalog(name); !errors.Is(err, ErrDiscoveryNotSupported) {
			t.Errorf("%q: %v", name, err)
		}
	}
	for _, name := range []string{"1763-L16BWA", "1766-L32BWAA"} {
		info, err := sys0InfoForCatalog(name)
		if err != nil || info.FilePosition != 233 {
			t.Fatalf("%q: %+v %v", name, info, err)
		}
	}
}

func syntheticDirectory() []byte {
	rows := [][3]byte{{0x20, 1, 0}, {0x21, 1, 0}, {0x40, 0, 0}, {0x60, 0, 0}}
	rows = append(rows, slcDirectoryRows...)
	rows = append(rows, [3]byte{0x81, 0, 0}, [3]byte{0xA1, 0, 0}, [3]byte{0xA2, 0, 0}, [3]byte{0xA4, 0, 0})
	return slcFile0(rows)
}

func TestDiscoveryFallback(t *testing.T) {
	for _, tc := range []struct {
		name, catalog, identity   string
		diagErr                   error
		routed, wantErr, fallback bool
	}{
		{name: "diag", catalog: "5/05"},
		{name: "diag routed", catalog: "5/05", routed: true},
		{name: "unknown direct", catalog: "unknown", identity: "1747-L553", fallback: true},
		{name: "unknown routed", catalog: "unknown", routed: true, wantErr: true},
		{name: "error direct", diagErr: errors.New("diag refused"), identity: "1747-L553", fallback: true},
		{name: "error routed", diagErr: errors.New("diag refused"), routed: true, wantErr: true},
		{name: "neither", catalog: "unknown", identity: "1756-EN2T", fallback: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &PLC{}
			if tc.routed {
				p.RoutePath = []byte{1, 0}
			}
			file := syntheticDirectory()
			called := false
			diag := func() (string, []byte, error) { return tc.catalog, []byte{0xee, 0x4a, 0x79, 0x66}, tc.diagErr }
			identity := func() (string, error) { called = true; return tc.identity, nil }
			read := func(_ uint16, _ byte, elem, size uint16) ([]byte, error) {
				if elem == 0x23 && size == 4 {
					return []byte{byte(len(file)), byte(len(file) >> 8)}, nil
				}
				return file[int(elem)*2 : int(elem)*2+int(size)], nil
			}
			entries, err := p.getFileDirectory(diag, identity, read, time.Now)
			if called != tc.fallback || (err != nil) != tc.wantErr {
				t.Fatalf("fallback=%v entries=%v err=%v", called, entries, err)
			}
			if err != nil {
				if entries != nil {
					t.Fatal("partial entries")
				}
				for _, field := range []string{"diag=", "identity=", "processor=EE 4A 79 66", "totalSize=", "header52=", "parsedCount="} {
					if !strings.Contains(err.Error(), field) {
						t.Errorf("missing %s in %v", field, err)
					}
				}
			} else {
				checkDirectory(t, entries)
			}
		})
	}
}

func TestDirectoryGuards(t *testing.T) {
	for _, mode := range []string{"valid", "count", "divisibility", "short", "long", "oversize", "short size", "changed", "budget before request", "budget before chunk", "budget before success", "transport"} {
		t.Run(mode, func(t *testing.T) {
			p := &PLC{}
			file := syntheticDirectory()
			if mode == "count" {
				file[52]++
			}
			if mode == "divisibility" {
				file[79+10*(4+8)+1]++
			}
			clock := time.Unix(0, 0)
			sizeReads, reads := 0, 0
			transportErr := errors.New("transport")
			diag := func() (string, []byte, error) {
				if mode == "budget before request" {
					clock = clock.Add(61 * time.Second)
				}
				return "5/05", []byte{1, 2, 3, 4}, nil
			}
			read := func(_ uint16, _ byte, elem, size uint16) ([]byte, error) {
				reads++
				if mode == "transport" {
					return nil, transportErr
				}
				if elem == 0x23 && size == 4 {
					sizeReads++
					if mode == "budget before chunk" {
						clock = clock.Add(61 * time.Second)
					}
					n := len(file)
					if mode == "oversize" {
						n = 16385
					}
					if mode == "short size" {
						return []byte{0}, nil
					}
					if sizeReads == 2 {
						if mode == "changed" {
							n++
						}
						if mode == "budget before success" {
							clock = clock.Add(61 * time.Second)
						}
					}
					data := make([]byte, 2)
					binary.LittleEndian.PutUint16(data, uint16(n))
					return data, nil
				}
				data := file[int(elem)*2 : int(elem)*2+int(size)]
				if mode == "short" {
					data = data[:len(data)-1]
				}
				if mode == "long" {
					data = append(append([]byte{}, data...), 0)
				}
				return data, nil
			}
			entries, err := p.getFileDirectory(diag, func() (string, error) { t.Fatal("unexpected fallback"); return "", nil }, read, func() time.Time { return clock })
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				checkDirectory(t, entries)
				return
			}
			if err == nil || entries != nil {
				t.Fatalf("entries=%v err=%v", entries, err)
			}
			if strings.HasPrefix(mode, "budget") {
				if !strings.Contains(err.Error(), "budget exceeded") {
					t.Fatal(err)
				}
				if mode == "budget before request" && reads != 0 {
					t.Fatal("read after deadline")
				}
			} else if mode == "transport" {
				if !errors.Is(err, transportErr) {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrDirectoryInvalid) {
				t.Fatal(err)
			}
		})
	}
}

func TestDirectoryHeaderUsesBothBytes(t *testing.T) {
	rows := make([][3]byte, 256)
	for i := range rows {
		rows[i][0] = FileTypePlaceholder
	}
	file := slcFile0(rows)
	sys0, _ := lookupSys0Info("1747")
	if _, err := parseFileDirectory(file, sys0); err != nil {
		t.Fatal(err)
	}
	file[53] = 0
	if _, err := parseFileDirectory(file, sys0); !errors.Is(err, ErrDirectoryInvalid) {
		t.Fatal(err)
	}
}

func TestMicroLogixDirectoryChecksUnchanged(t *testing.T) {
	file := make([]byte, 244)
	file[233] = FileTypeInteger
	file[234] = 3 // Existing MicroLogix parsing truncates to one element.
	sizeReads := 0
	p := &PLC{}
	entries, err := p.getFileDirectory(
		func() (string, []byte, error) { return "1763-L16BWA", nil, nil },
		func() (string, error) { t.Fatal("identity fallback"); return "", nil },
		func(_ uint16, ft byte, elem, size uint16) ([]byte, error) {
			if ft != 2 {
				t.Fatalf("file type %d", ft)
			}
			if elem == 0x28 && size == 8 {
				sizeReads++
				n := len(file) + 19968
				return []byte{byte(n), byte(n >> 8)}, nil
			}
			// Oversized replies retain the original truncation behavior.
			return append(append([]byte{}, file[int(elem)*2:int(elem)*2+int(size)]...), 0), nil
		}, time.Now)
	if err != nil || len(entries) != 1 || entries[0].ElementCount != 1 || sizeReads != 1 {
		t.Fatalf("%+v %v sizeReads=%d", entries, err, sizeReads)
	}
}
