package driver

import (
	"encoding/binary"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yatesdr/plcio/eip"
)

// pcccSessionPeer is a loopback EtherNet/IP peer (the PCCC adapter always
// dials port 44818) that accepts any number of sessions, answers PCCC typed
// reads with zeroed data and writes with success, and counts sessions that
// ended (UnRegisterSession or socket close).
type pcccSessionPeer struct {
	opened atomic.Int32
	closed atomic.Int32
}

func startPCCCSessionPeer(t *testing.T) (string, *pcccSessionPeer) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:44818")
	if err != nil {
		t.Fatal(err)
	}
	peer := &pcccSessionPeer{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var conns []net.Conn
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
			peer.opened.Add(1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer peer.closed.Add(1)
				defer conn.Close()
				servePCCCSession(conn)
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return "127.0.0.1", peer
}

func servePCCCSession(conn net.Conn) {
	for {
		frame, err := eip.ReadFrame(conn)
		if err != nil {
			return
		}
		var reply *eip.Frame
		switch frame.Command {
		case 0x65:
			reply = frame.Reply(0, []byte{1, 0, 0, 0})
			reply.SessionHandle = 99
		case 0x6f:
			cpf, err := eip.ParseRRData(frame.Data)
			if err != nil {
				return
			}
			packet, err := eip.ParseEipCommonPacket(cpf)
			if err != nil || len(packet.Items) != 2 {
				return
			}
			req := packet.Items[1].Data
			start := 2 + int(req[1])*2
			requester := req[start : start+7]
			cmd := req[start+7:]
			resp := append([]byte{0xcb, 0, 0, 0}, requester...)
			resp = append(resp, cmd[0]|0x40, 0, cmd[2], cmd[3])
			if cmd[4] == 0xa2 {
				resp = append(resp, make([]byte, int(cmd[5]))...)
			}
			data := []byte{0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 0, 0, 0xb2, 0, 0, 0}
			binary.LittleEndian.PutUint16(data[14:16], uint16(len(resp)))
			reply = frame.Reply(0, append(data, resp...))
		default: // 0x66 UnRegisterSession or anything else ends the session
			return
		}
		if _, err := conn.Write(reply.Bytes()); err != nil {
			return
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Reconnecting must close the previous EtherNet/IP session instead of leaking
// it; SLC 5/05 and MicroLogix processors have very few sessions.
func TestPCCCAdapterReconnectClosesPreviousSession(t *testing.T) {
	host, peer := startPCCCSessionPeer(t)
	a, _ := NewPCCCAdapter(&PLCConfig{Address: host, Family: FamilySLC500, Timeout: time.Second})
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second session", func() bool { return peer.opened.Load() == 2 })
	waitFor(t, "first session to be closed", func() bool { return peer.closed.Load() == 1 })
	if !a.IsConnected() {
		t.Fatal("adapter not connected after reconnect")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "all sessions closed", func() bool { return peer.closed.Load() == 2 })
}

// Close racing with Read/Write/Keepalive must neither data-race (go test
// -race) nor dereference a nil client.
func TestPCCCAdapterCloseDuringOperations(t *testing.T) {
	host, _ := startPCCCSessionPeer(t)
	a, _ := NewPCCCAdapter(&PLCConfig{Address: host, Family: FamilySLC500, Timeout: time.Second})
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
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
				_, _ = a.Read([]TagRequest{{Name: "N7:0"}, {Name: "N7:1"}, {Name: "B3:0/1"}})
				_ = a.Write("N7:0", int64(1))
				_ = a.Keepalive()
				_ = a.IsConnected()
				_ = a.ConnectionMode()
				_ = a.Client()
			}
		}()
	}
	time.Sleep(20 * time.Millisecond)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	close(stop)
	wg.Wait()
	if a.IsConnected() {
		t.Fatal("adapter still connected after Close")
	}
}
