package driver

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/yatesdr/plcio/ads"
	"github.com/yatesdr/plcio/logix"
	"github.com/yatesdr/plcio/omron"
	"github.com/yatesdr/plcio/pccc"
	"github.com/yatesdr/plcio/s7"
)

func TestConnectionErrorClassification(t *testing.T) {
	sentinels := []error{ErrConnectionLost, logix.ErrConnectionLost, s7.ErrConnectionLost,
		omron.ErrConnectionLost, pccc.ErrConnectionLost, ads.ErrConnectionLost}
	for _, sentinel := range sentinels {
		wrapped := fmt.Errorf("read incomplete: %w", sentinel)
		if !IsConnectionLost(wrapped) || !IsLikelyConnectionError(wrapped) {
			t.Errorf("sentinel %v not classified", sentinel)
		}
	}
	for _, err := range []error{
		io.EOF, io.ErrUnexpectedEOF, net.ErrClosed,
		fmt.Errorf("x: %w", syscall.ECONNRESET), fmt.Errorf("x: %w", syscall.EPIPE),
		fmt.Errorf("x: %w", syscall.ECONNREFUSED),
		&net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded},
		errors.New("read tcp 10.0.0.1:44818: EOF"), // flattened to text
		errors.New("unexpected EOF"),
		errors.New("wsarecv: An existing connection was forcibly closed by the remote host."),
	} {
		if !IsLikelyConnectionError(err) {
			t.Errorf("not a connection error: %v", err)
		}
	}
	for _, err := range []error{
		errors.New(`tag "MAIN.Geofence" not found`),
		errors.New("symbol EOF_Count is read-only"),
		errors.New("tag Program:Main.eofFlag: path segment error"),
		errors.New("value 70000 out of range for INT"),
		&ads.AdsError{Code: 0x710},
	} {
		if IsLikelyConnectionError(err) || IsConnectionLost(err) {
			t.Errorf("misclassified as connection error: %v", err)
		}
	}
	if IsConnectionLost(io.EOF) || IsConnectionLost(nil) || IsLikelyConnectionError(nil) {
		t.Error("IsConnectionLost matches only sentinels")
	}
	if !containsWord("read: eof", "eof") || containsWord("geofence", "eof") || containsWord("eof_x", "eof") || !containsWord("eof", "eof") {
		t.Error("containsWord boundaries")
	}
}

func TestADSKeepaliveNotConnected(t *testing.T) {
	adapter, err := NewADSAdapter(&PLCConfig{Family: FamilyBeckhoff, Address: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	err = adapter.Keepalive()
	if err == nil || !errors.Is(err, ErrConnectionLost) || !adapter.IsConnectionError(err) {
		t.Fatalf("keepalive while disconnected: %v", err)
	}
}

// Keepalive performs ADS ReadState (command 4) on the configured AMS port and
// surfaces a dropped link as ErrConnectionLost.
func TestADSKeepaliveReadsState(t *testing.T) {
	var states atomic.Int32
	var targetPort atomic.Uint32
	var mode atomic.Int32 // 0 answer, 1 device error, 2 drop the link
	endpoint := adapterPeer(t, func(conn net.Conn) {
		for {
			req, err := adsPeerRequest(conn)
			if err != nil {
				return
			}
			switch binary.LittleEndian.Uint16(req[22:]) {
			case 1:
				body := make([]byte, 24)
				body[4] = 3
				copy(body[8:], "Fixture")
				adsPeerReply(conn, req, body)
			case 4:
				states.Add(1)
				targetPort.Store(uint32(binary.LittleEndian.Uint16(req[12:14])))
				if len(req) != 38 {
					t.Errorf("ReadState carries data: %x", req)
				}
				switch mode.Load() {
				case 0:
					adsPeerReply(conn, req, []byte{0, 0, 0, 0, 5, 0, 0, 0})
				case 1:
					adsPeerReply(conn, req, []byte{0x06, 0x07, 0, 0})
				default:
					return
				}
			default:
				t.Errorf("unexpected command %x", req)
				return
			}
		}
	})
	adapter, err := NewADSAdapterWithOptions(&PLCConfig{Family: FamilyBeckhoff, Address: endpoint,
		AmsNetId: "5.45.219.226.1.1", AmsPort: 851, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Connect(); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if err := adapter.Keepalive(); err != nil || states.Load() != 1 || targetPort.Load() != 851 {
		t.Fatalf("keepalive: %v states=%d port=%d", err, states.Load(), targetPort.Load())
	}
	mode.Store(1)
	err = adapter.Keepalive()
	var device *ads.AdsError
	if err == nil || !errors.As(err, &device) || adapter.IsConnectionError(err) || !adapter.IsConnected() {
		t.Fatalf("device rejection: %v", err)
	}
	mode.Store(2)
	err = adapter.Keepalive()
	if !errors.Is(err, ErrConnectionLost) || !errors.Is(err, ads.ErrConnectionLost) || !adapter.IsConnectionError(err) {
		t.Fatalf("dropped link: %v", err)
	}
	if adapter.IsConnected() {
		t.Fatal("dropped link still reported connected")
	}
	if err := adapter.Keepalive(); !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("keepalive after drop: %v", err)
	}
}

// Per-protocol failures reach the caller instead of being swallowed. The EIP
// phase is stubbed so the test sends no broadcast onto the real network, and
// the oversized CIDR is rejected before any scan traffic.
func TestDiscoverAllWithReportSurfacesErrors(t *testing.T) {
	previous := eipDiscovery
	defer func() { eipDiscovery = previous }()
	eipDiscovery = func(string, time.Duration) ([]DiscoveredDevice, []error) {
		return []DiscoveredDevice{{IP: net.IPv4(192, 0, 2, 7), Protocol: "EIP"}},
			[]error{fmt.Errorf("EIP discovery via 192.0.2.255: %w", syscall.EACCES)}
	}
	devices, errs := DiscoverAllWithReport("192.0.2.255", "10.0.0.0/8", 50*time.Millisecond, 1)
	if len(devices) != 1 || len(errs) != 2 {
		t.Fatalf("devices=%v errs=%v", devices, errs)
	}
	var sawCIDR, sawEIP bool
	for _, err := range errs {
		sawCIDR = sawCIDR || containsWord(err.Error(), "scan cidr \"10.0.0.0/8\"") || containsWord(err.Error(), "scan CIDR \"10.0.0.0/8\"")
		sawEIP = sawEIP || errors.Is(err, syscall.EACCES)
	}
	if !sawCIDR || !sawEIP {
		t.Fatalf("errors: %v", errs)
	}
	if got := DiscoverAll("192.0.2.255", "10.0.0.0/8", 50*time.Millisecond, 1); len(got) != 1 {
		t.Fatalf("DiscoverAll: %v", got)
	}
}
