package ads

import (
	"bytes"
	"encoding/binary"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func appendUDPTag(data []byte, tag uint16, value []byte) []byte {
	data = binary.LittleEndian.AppendUint16(data, tag)
	data = binary.LittleEndian.AppendUint16(data, uint16(len(value)))
	return append(data, value...)
}

// A TwinCAT 3 Get Info reply as captured shapes it: hostname, a large OS
// version block (unknown to us), TwinCAT version and a fingerprint. The whole
// reply exceeds the previous 512-byte receive buffer.
func getInfoReply(netID []byte, hostname string) []byte {
	data := make([]byte, 24)
	binary.LittleEndian.PutUint32(data[:4], 0x71146603)
	binary.LittleEndian.PutUint32(data[8:12], 0x80000001)
	copy(data[12:18], netID)
	binary.LittleEndian.PutUint16(data[18:20], 10000)
	binary.LittleEndian.PutUint32(data[20:24], 4)
	data = appendUDPTag(data, 0x0005, append([]byte(hostname), 0))
	data = appendUDPTag(data, 0x0004, bytes.Repeat([]byte{0xAA}, 276))
	data = appendUDPTag(data, 0x0003, []byte{3, 1, 0xB8, 0x0F}) // 3.1.4024
	data = appendUDPTag(data, 0x0012, bytes.Repeat([]byte{'f'}, 300))
	return data
}

func TestGetInfoRequestWireFormat(t *testing.T) {
	want := []byte{0x03, 0x66, 0x14, 0x71, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x10, 0x27, 0, 0, 0, 0}
	if got := getInfoRequest(); !bytes.Equal(got, want) {
		t.Fatalf("Get Info request %x, want %x", got, want)
	}
}

func TestParseGetInfoReplyTags(t *testing.T) {
	reply := getInfoReply([]byte{5, 45, 219, 226, 1, 1}, "CX-5DB8E2")
	if len(reply) <= 512 {
		t.Fatalf("fixture too small: %d", len(reply))
	}
	device := parseDiscoveryResponse(reply, net.IPv4(192, 168, 5, 212))
	if device == nil || device.AmsNetId != "5.45.219.226.1.1" || device.Hostname != "CX-5DB8E2" ||
		device.TwinCATVersion != "3.1.4024" || device.HasRoute || device.IP.String() != "192.168.5.212" {
		t.Fatalf("parsed %+v", device)
	}
	// A duplicate tag keeps its first value; a short version tag is ignored.
	dup := appendUDPTag(append([]byte(nil), reply...), 0x0005, []byte("other\x00"))
	binary.LittleEndian.PutUint32(dup[20:24], 5)
	if device := parseDiscoveryResponse(dup, net.IPv4(1, 2, 3, 4)); device == nil || device.Hostname != "CX-5DB8E2" {
		t.Fatalf("duplicate tag: %+v", device)
	}
	short := getInfoReply([]byte{5, 45, 219, 226, 1, 1}, "h")[:24]
	binary.LittleEndian.PutUint32(short[20:24], 1)
	short = appendUDPTag(short, 0x0003, []byte{3, 1})
	if device := parseDiscoveryResponse(short, net.IPv4(1, 2, 3, 4)); device == nil || device.TwinCATVersion != "" {
		t.Fatalf("short version tag: %+v", device)
	}
}

// fakeGetInfoPeer answers Get Info on loopback like a TwinCAT router and points
// the discovery ports at itself and at a counting TCP listener.
func fakeGetInfoPeer(t *testing.T, answer bool) (tcpAccepts *atomic.Int32, requests *atomic.Int32) {
	t.Helper()
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		udp.Close()
		t.Skip(err)
	}
	oldUDP, oldTCP := discoveryUDPPort, discoveryTCPPort
	discoveryUDPPort = udp.LocalAddr().(*net.UDPAddr).Port
	discoveryTCPPort = tcp.Addr().(*net.TCPAddr).Port
	tcpAccepts, requests = new(atomic.Int32), new(atomic.Int32)
	go func() {
		for {
			conn, err := tcp.Accept()
			if err != nil {
				return
			}
			tcpAccepts.Add(1)
			conn.Close()
		}
	}()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := udp.ReadFrom(buf)
			if err != nil {
				return
			}
			requests.Add(1)
			if answer && bytes.Equal(buf[:n], getInfoRequest()) {
				udp.WriteTo(getInfoReply([]byte{5, 45, 219, 226, 1, 1}, "CX-5DB8E2"), from)
			}
		}
	}()
	t.Cleanup(func() {
		udp.Close()
		tcp.Close()
		discoveryUDPPort, discoveryTCPPort = oldUDP, oldTCP
	})
	return tcpAccepts, requests
}

// The device's real NetID comes from UDP Get Info, not IP+".1.1"; a UDP answer
// suppresses the TCP fallback and ends collection early.
func TestDiscoverUsesUnicastGetInfoNetID(t *testing.T) {
	accepts, requests := fakeGetInfoPeer(t, true)
	start := time.Now()
	devices := Discover([]net.IP{net.IPv4(127, 0, 0, 1)}, 2*time.Second, 4)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("did not stop once every target answered: %s", elapsed)
	}
	if len(devices) != 1 || devices[0].AmsNetId != "5.45.219.226.1.1" || devices[0].Hostname != "CX-5DB8E2" ||
		devices[0].TwinCATVersion != "3.1.4024" || devices[0].HasRoute || !devices[0].Connected {
		t.Fatalf("devices %+v", devices)
	}
	if requests.Load() != 1 || accepts.Load() != 0 {
		t.Fatalf("udp requests=%d tcp probes=%d", requests.Load(), accepts.Load())
	}
}

func TestDiscoverFallsBackToTCPOnlyWithoutUDPAnswer(t *testing.T) {
	accepts, requests := fakeGetInfoPeer(t, false)
	start := time.Now()
	devices, errs := DiscoverWithReport([]net.IP{net.IPv4(127, 0, 0, 1)}, nil, 200*time.Millisecond, 4)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("unbounded discovery: %s", elapsed)
	}
	// The TCP peer closes without an identity, so nothing is reported found.
	if len(devices) != 0 || len(errs) != 0 || requests.Load() != 1 || accepts.Load() != 1 {
		t.Fatalf("devices=%+v errs=%v udp=%d tcp=%d", devices, errs, requests.Load(), accepts.Load())
	}
}

func TestDiscoverWithReportSurfacesFailures(t *testing.T) {
	fakeGetInfoPeer(t, true)
	// An IPv6 broadcast literal cannot be sent from the IPv4 socket; the failure
	// is reported while the unicast answer is still collected.
	devices, errs := DiscoverWithReport([]net.IP{net.IPv4(127, 0, 0, 1)}, []string{"::1"}, 300*time.Millisecond, 2)
	if len(devices) != 1 || len(errs) != 1 || !strings.Contains(errs[0].Error(), "::1") {
		t.Fatalf("devices=%+v errs=%v", devices, errs)
	}
	if _, errs := DiscoverWithReport([]net.IP{net.ParseIP("::1")}, nil, time.Millisecond, 1); len(errs) != 1 {
		t.Fatalf("IPv6 scan list not reported: %v", errs)
	}
}
