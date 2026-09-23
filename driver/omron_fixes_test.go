package driver

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/yatesdr/plcio/omron"
)

// omronFixFINSServer is a minimal multi-connection FINS/TCP responder that
// answers every command with zero data words. Loopback only.
func omronFixFINSServer(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var conns []net.Conn
	frame := func(cmd uint32, payload []byte) []byte {
		out := []byte("FINS")
		out = binary.BigEndian.AppendUint32(out, uint32(8+len(payload)))
		out = binary.BigEndian.AppendUint32(out, cmd)
		out = binary.BigEndian.AppendUint32(out, 0)
		return append(out, payload...)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				for {
					hdr := make([]byte, 16)
					if _, err := io.ReadFull(conn, hdr); err != nil {
						return
					}
					n := binary.BigEndian.Uint32(hdr[4:8])
					if n < 8 || n > 4096 {
						return
					}
					body := make([]byte, n-8)
					if _, err := io.ReadFull(conn, body); err != nil {
						return
					}
					if binary.BigEndian.Uint32(hdr[8:12]) == 0 {
						conn.Write(frame(1, []byte{0, 0, 0, 10, 0, 0, 0, 2}))
						continue
					}
					if len(body) < 12 {
						return
					}
					resp := []byte{0xC0, 0, 2, body[6], body[7], body[8], body[3], body[4], body[5], body[9], body[10], body[11], 0, 0}
					if binary.BigEndian.Uint16(body[10:12]) == 0x0101 && len(body) >= 18 {
						resp = append(resp, make([]byte, 2*int(binary.BigEndian.Uint16(body[16:18])))...)
					}
					if _, err := conn.Write(frame(2, resp)); err != nil {
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return ln.Addr().(*net.TCPAddr).Port
}

// Item 10: the adapter's client pointer is shared between Close/Connect and
// Read/Write/IsConnected; run with -race.
func TestOmronAdapterConcurrentCloseAndRead(t *testing.T) {
	port := omronFixFINSServer(t)
	a, _ := NewOmronAdapter(&PLCConfig{Address: "127.0.0.1", FinsPort: port, Family: FamilyOmron, Protocol: "fins", Timeout: time.Second})
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				a.Read([]TagRequest{{Name: "DM0"}})
				a.IsConnected()
				a.ConnectionMode()
				a.Write("DM1", int64(1))
			}
		}()
	}
	for i := 0; i < 10; i++ {
		a.Close()
		if err := a.Connect(); err != nil {
			t.Error(err)
		}
	}
	close(stop)
	wg.Wait()
	a.Close()
}

func TestOmronAdapterConnectClosesPreviousClient(t *testing.T) {
	port := omronFixFINSServer(t)
	a, _ := NewOmronAdapter(&PLCConfig{Address: "127.0.0.1", FinsPort: port, Family: FamilyOmron, Protocol: "fins", Timeout: time.Second})
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	first := a.Client()
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if first == a.Client() {
		t.Fatal("client not replaced")
	}
	if first.IsConnected() {
		t.Fatal("previous client left open by Connect")
	}
	if !a.IsConnected() {
		t.Fatal("new client not connected")
	}
}

// Item 11: Omron BYTE/WORD/DWORD/LWORD (0xD1-0xD4) normalize like the CIP
// unsigned types.
func TestOmronBitStringTypesNormalize(t *testing.T) {
	cases := []struct {
		code uint16
		data []byte
		want uint64
	}{
		{omron.TypeOmronByte, []byte{0xFF}, 0xFF},
		{omron.TypeOmronWord, []byte{0xFF, 0xFF}, 0xFFFF},
		{omron.TypeOmronDWord, []byte{0x78, 0x56, 0x34, 0x12}, 0x12345678},
		{omron.TypeOmronLWord, []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, ^uint64(0)},
	}
	for _, tc := range cases {
		if !omronPrimitive(tc.code) {
			t.Errorf("0x%02X not treated as primitive", tc.code)
		}
		if got := normalizePrimitive(omron.DecodeValue(tc.code, tc.data, false)); got != tc.want {
			t.Errorf("0x%02X = %#v, want %#v", tc.code, got, tc.want)
		}
	}
}
