package driver

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// A repeated Connect must close the previous ADS session before dialing, since
// the TwinCAT router may reject a second TCP connection from the same AMS Net ID.
func TestADSConnectClosesPreviousBeforeDial(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	overlap := make(chan bool, 4)
	var wg sync.WaitGroup
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		for _, conn := range conns {
			conn.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	serve := func(conn net.Conn) {
		defer wg.Done()
		for {
			req, err := adsPeerRequest(conn)
			if err != nil {
				return
			}
			if binary.LittleEndian.Uint16(req[22:24]) != 1 {
				adsPeerReply(conn, req, []byte{1, 7, 0, 0})
				continue
			}
			body := make([]byte, 24)
			body[4] = 3
			copy(body[8:], "Connect")
			adsPeerReply(conn, req, body)
		}
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			if len(conns) > 0 {
				// Before answering the new session, the old one must be closed.
				previous := conns[len(conns)-1]
				previous.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
				_, err := previous.Read(make([]byte, 1))
				overlap <- !errors.Is(err, io.EOF)
			}
			conns = append(conns, conn)
			mu.Unlock()
			wg.Add(1)
			go serve(conn)
		}
	}()
	a, err := NewADSAdapter(&PLCConfig{Address: listener.Addr().String(), AmsNetId: "5.45.219.226.1.1", AmsPort: 851, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for i := 0; i < 2; i++ {
		if err := a.Connect(); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case open := <-overlap:
		if open {
			t.Fatal("new ADS session dialed while the previous one was still open")
		}
	case <-time.After(time.Second):
		t.Fatal("second session not observed")
	}
	if !a.IsConnected() {
		t.Fatal("replacement session not published")
	}
}
