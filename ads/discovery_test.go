package ads

import (
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"
)

func TestDiscoveryUsesCompleteValidatedIdentity(t *testing.T) {
	for _, mode := range []string{"fragmented", "ADS-error", "AMS-error", "truncated", "wrong-invoke", "wrong-flags"} {
		t.Run(mode, func(t *testing.T) {
			local, peer := net.Pipe()
			defer local.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer peer.Close()
				request, err := testRequest(peer)
				if err != nil {
					return
				}
				if binary.LittleEndian.Uint16(request[22:24]) != 1 || len(request) != 38 {
					t.Error("incorrect identity request")
				}
				body := make([]byte, 24)
				body[4], body[5] = 3, 1
				binary.LittleEndian.PutUint16(body[6:8], 4024)
				copy(body[8:24], "Identity Test")
				if mode == "ADS-error" {
					body = []byte{0x23, 7, 0, 0}
				}
				response := testResponse(request, body)
				switch mode {
				case "AMS-error":
					response = testResponse(request, nil)
					binary.LittleEndian.PutUint32(response[30:34], 7)
				case "truncated":
					response = response[:len(response)-1]
				case "wrong-invoke":
					response[34]++
				case "wrong-flags":
					response[24] = 1
				}
				for _, value := range response {
					if _, err := peer.Write([]byte{value}); err != nil {
						return
					}
				}
			}()
			device := tryADSDeviceInfoUntil(local, net.IPv4(192, 168, 5, 212), time.Now().Add(100*time.Millisecond))
			local.Close()
			<-done
			if mode == "fragmented" {
				if device == nil || device.ProductName != "Identity Test v3.1.4024" || device.TwinCATVersion != "3.1.4024" || !device.HasRoute || !device.Connected {
					t.Fatalf("identity offset/fragments: %+v", device)
				}
			} else if device != nil {
				t.Fatalf("unverified reply identified device: %+v", device)
			}
		})
	}
}

func udpIdentity(hostname string) []byte {
	data := make([]byte, 24)
	binary.LittleEndian.PutUint32(data[:4], 0x71146603)
	binary.LittleEndian.PutUint32(data[8:12], 0x80000001)
	copy(data[12:18], []byte{5, 45, 219, 226, 1, 1})
	binary.LittleEndian.PutUint16(data[18:20], 10000)
	if hostname != "" {
		binary.LittleEndian.PutUint32(data[20:24], 1)
		data = binary.LittleEndian.AppendUint16(data, 5)
		data = binary.LittleEndian.AppendUint16(data, uint16(len(hostname)+1))
		data = append(data, []byte(hostname)...)
		data = append(data, 0)
	}
	return data
}

func TestBroadcastIdentityDoesNotVerifyRuntimeRoute(t *testing.T) {
	data := udpIdentity(strings.Repeat("x", 300))
	device := parseDiscoveryResponse(data, net.IPv4(192, 168, 5, 212))
	if device == nil || len(device.Hostname) != 300 || !device.Connected || device.HasRoute || device.AmsNetId != "5.45.219.226.1.1" {
		t.Fatalf("UDP identity/route/length: %+v", device)
	}
	for _, mutate := range []func([]byte) []byte{
		func(b []byte) []byte { b[8] = 6; return b },
		func(b []byte) []byte { b[4] = 1; return b },
		func(b []byte) []byte { clear(b[12:18]); return b },
		func(b []byte) []byte { return b[:len(b)-1] },
		func(b []byte) []byte { b[20] = 2; return b },
		func(b []byte) []byte { return append(b, 0) },
	} {
		if parseDiscoveryResponse(mutate(append([]byte(nil), data...)), net.IPv4(1, 2, 3, 4)) != nil {
			t.Fatal("malformed UDP identity accepted")
		}
	}
}

func FuzzDiscoveryResponse(f *testing.F) {
	f.Add(udpIdentity("PLC"))
	f.Add(udpIdentity("")[:18])
	f.Add([]byte{3, 0x66, 0x14, 0x71})
	f.Fuzz(func(t *testing.T, data []byte) { _ = parseDiscoveryResponse(data, net.IPv4(192, 168, 5, 212)) })
}
