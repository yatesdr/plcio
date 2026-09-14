package ads

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestFrameValidation(t *testing.T) {
	for name, mutate := range map[string]func([]byte){
		"reserved":    func(b []byte) { b[0] = 1 },
		"short":       func(b []byte) { binary.LittleEndian.PutUint32(b[2:6], 31) },
		"oversized":   func(b []byte) { binary.LittleEndian.PutUint32(b[2:6], 0xffffffff) },
		"data length": func(b []byte) { b[26]++ },
		"invoke":      func(b []byte) { b[34]++ },
		"command":     func(b []byte) { b[22]++ },
		"flags":       func(b []byte) { b[24] = 4 },
		"target ID":   func(b []byte) { b[6]++ },
		"target port": func(b []byte) { b[12]++ },
		"source ID":   func(b []byte) { b[14]++ },
		"source port": func(b []byte) { b[20]++ },
	} {
		t.Run(name, func(t *testing.T) {
			local, peer := net.Pipe()
			defer local.Close()
			defer peer.Close()
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				request, err := testRequest(peer)
				if err != nil {
					return
				}
				response := testResponse(request, testReadReply([]byte{25, 0, 0, 0}))
				mutate(response)
				peer.Write(response)
				peer.Close()
			}()
			conn := newAdsConnection(local, AmsNetId{1, 2, 3, 4, 1, 1}, 32900)
			_, err := conn.sendRequest(AmsNetId{5, 45, 219, 226, 1, 1}, 851, CmdRead, nil)
			if !errors.Is(err, ErrConnectionLost) || !conn.dead.Load() {
				t.Fatalf("accepted malformed %s: %v", name, err)
			}
			if _, nextErr := conn.sendRequest(AmsNetId{5, 45, 219, 226, 1, 1}, 851, CmdRead, nil); !errors.Is(nextErr, net.ErrClosed) {
				t.Fatalf("reused unusable stream: %v", nextErr)
			}
			peer.Close()
			<-finished
		})
	}
}

type shortWriteConn struct{ net.Conn }

func (c shortWriteConn) Write(b []byte) (int, error) {
	if len(b) > 3 {
		b = b[:3]
	}
	return c.Conn.Write(b)
}

func TestFragmentedResponseAndShortWrites(t *testing.T) {
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		request, err := testRequest(peer)
		if err != nil {
			return
		}
		for _, value := range testResponse(request, testReadReply([]byte{42, 0})) {
			if _, err := peer.Write([]byte{value}); err != nil {
				return
			}
		}
	}()
	conn := newAdsConnection(shortWriteConn{local}, AmsNetId{1, 2, 3, 4, 1, 1}, 32900)
	response, err := conn.sendRequest(AmsNetId{5, 45, 219, 226, 1, 1}, 851, CmdRead, []byte{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	data, err := readCommandData(response)
	if err != nil || len(data) != 2 || data[0] != 42 {
		t.Fatalf("fragmented response: %x %v", data, err)
	}
	<-finished
}

func TestRouterErrorEnvelope(t *testing.T) {
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()
	go func() {
		request, err := testRequest(peer)
		if err != nil {
			return
		}
		response := testResponse(request, nil)
		binary.LittleEndian.PutUint32(response[30:34], 6)
		binary.LittleEndian.PutUint16(response[20:22], 0)
		peer.Write(response)
	}()
	conn := newAdsConnection(local, AmsNetId{1, 2, 3, 4, 1, 1}, 32900)
	_, err := conn.sendRequest(AmsNetId{5, 45, 219, 226, 1, 1}, 851, CmdRead, nil)
	var device *AdsError
	if !errors.As(err, &device) || device.Code != 6 || conn.dead.Load() {
		t.Fatalf("router error: %v", err)
	}
}

func TestStalledExchangesBounded(t *testing.T) {
	for _, part := range []string{"write", "header", "body"} {
		t.Run(part, func(t *testing.T) {
			local, peer := net.Pipe()
			defer local.Close()
			defer peer.Close()
			stop := make(chan struct{})
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				if part != "write" {
					request, err := testRequest(peer)
					if err != nil {
						return
					}
					if part == "body" {
						peer.Write(testResponse(request, testReadReply([]byte{1}))[:6])
					}
				}
				<-stop
			}()
			conn := newAdsConnection(local, AmsNetId{1, 2, 3, 4, 1, 1}, 32900)
			conn.timeout = 20 * time.Millisecond
			start := time.Now()
			_, err := conn.sendRequest(AmsNetId{5, 45, 219, 226, 1, 1}, 851, CmdRead, nil)
			var netErr net.Error
			if !errors.Is(err, ErrConnectionLost) || !errors.As(err, &netErr) || !netErr.Timeout() || time.Since(start) > time.Second {
				t.Fatalf("stall %s: %v", part, err)
			}
			close(stop)
			peer.Close()
			<-finished
		})
	}
}

func TestPartialFrameEOF(t *testing.T) {
	for _, size := range []int{1, 5, 6, 10, 37, 39} {
		t.Run(string(rune('A'+size)), func(t *testing.T) {
			local, peer := net.Pipe()
			defer local.Close()
			defer peer.Close()
			go func() {
				request, err := testRequest(peer)
				if err != nil {
					return
				}
				peer.Write(testResponse(request, testReadReply([]byte{1}))[:size])
				peer.Close()
			}()
			conn := newAdsConnection(local, AmsNetId{1, 2, 3, 4, 1, 1}, 32900)
			_, err := conn.sendRequest(AmsNetId{5, 45, 219, 226, 1, 1}, 851, CmdRead, nil)
			if !errors.Is(err, ErrConnectionLost) || !(errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
				t.Fatalf("partial frame error: %v", err)
			}
		})
	}
}

func TestCloseHandleCleanupBudget(t *testing.T) {
	for _, count := range []int{0, 1, 1000} {
		t.Run(string(rune('A'+count)), func(t *testing.T) {
			var calls atomic.Int32
			c := testClient(t, func(request []byte) []byte {
				calls.Add(1)
				time.Sleep(40 * time.Millisecond)
				return []byte{0, 0, 0, 0}
			})
			c.cfg.timeout = 20 * time.Millisecond
			for i := range count {
				c.symbols[string(rune(i+1))] = &SymbolEntry{Handle: uint32(i + 1)}
			}
			start := time.Now()
			c.Close()
			c.Close()
			if time.Since(start) > time.Second || c.IsConnected() {
				t.Fatal("Close unbounded or still connected")
			}
			if count > 0 && calls.Load() != 1 {
				t.Fatalf("continued cleanup after failed stream: %d", calls.Load())
			}
		})
	}
}

func TestCloseAbortsActiveRead(t *testing.T) {
	local, peer := net.Pipe()
	defer peer.Close()
	c := &Client{conn: newAdsConnection(local, AmsNetId{1, 2, 3, 4, 1, 1}, 32900), targetNetId: AmsNetId{5, 45, 219, 226, 1, 1}, targetPort: 851, connected: true, cfg: defaultOptions(), symbols: make(map[string]*SymbolEntry)}
	c.versionCapability, c.catalogUnavailable = 2, true
	seedDINT(c, "MAIN.n", 42)
	requestReceived := make(chan struct{})
	go func() { testRequest(peer); close(requestReceived) }()
	finished := make(chan error, 1)
	go func() { _, err := c.Read("MAIN.n"); finished <- err }()
	<-requestReceived
	start := time.Now()
	c.Close()
	select {
	case err := <-finished:
		if !errors.Is(err, ErrConnectionLost) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close cannot abort read")
	}
	if time.Since(start) > time.Second {
		t.Fatal("Close waited on active operation")
	}
}

func FuzzFrameParsers(f *testing.F) {
	f.Add([]byte{0, 0, 32, 0, 0, 0})
	f.Add([]byte{0, 0, 255, 255, 255, 255})
	f.Add(make([]byte, 32))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 6 {
			validateTCPHeader(data, 1<<20)
		}
		validateAMSResponse(data, AmsNetId{1, 2, 3, 4, 1, 1}, 32900, AmsNetId{5, 45, 219, 226, 1, 1}, 851, 2, 1)
		readCommandData(data)
		decodeDeviceInfo(data)
	})
}
