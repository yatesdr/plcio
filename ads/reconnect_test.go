package ads

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// This test uses only a loopback peer, never the supplied PLC. It establishes
// TCP endpoint preservation and configured AMS identities on verified sessions.
func TestReconnectOriginalEndpoint(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var sessions atomic.Int32
	var peers sync.WaitGroup
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			peer, err := listener.Accept()
			if err != nil {
				return
			}
			sessions.Add(1)
			peers.Add(1)
			go func() {
				defer peers.Done()
				defer peer.Close()
				for {
					req, err := testRequest(peer)
					if err != nil {
						return
					}
					if binary.LittleEndian.Uint16(req[22:24]) != 1 {
						t.Errorf("unexpected request %x", req)
						return
					}
					if AmsNetId(req[6:12]) != (AmsNetId{5, 45, 219, 226, 1, 1}) || binary.LittleEndian.Uint16(req[12:14]) != 852 || AmsNetId(req[14:20]) != (AmsNetId{10, 20, 30, 40, 1, 1}) || binary.LittleEndian.Uint16(req[20:22]) != 32999 {
						t.Errorf("wrong AMS addressing %x", req)
					}
					body := make([]byte, 24)
					body[4] = 3
					copy(body[8:], "Fake TwinCAT")
					if _, err := peer.Write(testResponse(req, body)); err != nil {
						return
					}
				}
			}()
		}
	}()
	c, err := Connect(listener.Addr().String(), WithAmsNetId("5.45.219.226.1.1"), WithAmsPort(852), WithLocalAmsNetId("10.20.30.40.1.1"), WithLocalAmsPort(32999), WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.Reconnect(); err != nil {
				t.Errorf("reconnect %v", err)
			}
		}()
	}
	wg.Wait()
	if sessions.Load() != 2 || !c.IsConnected() || c.cfg.timeout != time.Second {
		t.Fatalf("sessions=%d connected=%v timeout=%v", sessions.Load(), c.IsConnected(), c.cfg.timeout)
	}
	info, err := c.GetDeviceInfo()
	if err != nil {
		t.Fatal(err)
	}
	info.DeviceName = "mutated"
	again, _ := c.GetDeviceInfo()
	if again.DeviceName != "Fake TwinCAT" {
		t.Fatal("device info cache exposed")
	}
	c.Close()
	listener.Close()
	<-acceptDone
	peers.Wait()
}

func TestInitialVerificationBounded(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		peer, err := listener.Accept()
		if err != nil {
			return
		}
		defer peer.Close()
		testRequest(peer)
		io.Copy(io.Discard, peer)
	}()
	start := time.Now()
	_, err = Connect(listener.Addr().String(), WithAmsNetId("5.45.219.226.1.1"), WithTimeout(20*time.Millisecond))
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() || time.Since(start) > time.Second {
		t.Fatalf("unbounded verification: %v", err)
	}
	<-finished
}

func TestCloseRejectsLateReconnectPublication(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	requestSeen := make(chan struct{})
	respond := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		peer, err := listener.Accept()
		if err != nil {
			return
		}
		defer peer.Close()
		req, err := testRequest(peer)
		if err != nil {
			return
		}
		close(requestSeen)
		<-respond
		body := make([]byte, 24)
		body[4] = 3
		peer.Write(testResponse(req, body))
	}()
	cfg := defaultOptions()
	cfg.targetNetId = AmsNetId{5, 45, 219, 226, 1, 1}
	c := &Client{endpoint: listener.Addr().String(), cfg: cfg, targetNetId: cfg.targetNetId, targetPort: 851}
	result := make(chan error, 1)
	go func() { result <- c.Reconnect() }()
	<-requestSeen
	c.Close()
	close(respond)
	if err := <-result; !errors.Is(err, net.ErrClosed) {
		t.Fatalf("obsolete reconnect: %v", err)
	}
	if c.IsConnected() {
		t.Fatal("reconnect published after Close")
	}
	<-finished
}
