package ads

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/yatesdr/plcio/internal/netutil"
)

// UDP port for TwinCAT discovery broadcasts
const DiscoveryUDPPort = 48899

// DiscoveredDevice contains identity information about a discovered Beckhoff/TwinCAT device.
type DiscoveredDevice struct {
	IP             net.IP // Device IP address
	Port           uint16 // ADS port (48898)
	AmsNetId       string // AMS Net ID (e.g., "5.45.219.226.1.1")
	ProductName    string // Product name or description
	Hostname       string // Device hostname
	TwinCATVersion string // TwinCAT version (e.g., "3.1.4024")
	HasRoute       bool   // True only after a successful ADS runtime identity exchange
	Connected      bool   // True for a validated protocol identity; UDP does not verify a route
}

// DiscoverBroadcast performs UDP broadcast discovery for TwinCAT devices.
// This finds devices and retrieves their AMS Net ID without requiring a route.
func DiscoverBroadcast(broadcastAddrs []string, timeout time.Duration) []DiscoveredDevice {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}

	var (
		results []DiscoveredDevice
		mu      sync.Mutex
		seen    = make(map[string]bool)
	)

	// SDK SERVERINFO: 12-byte header, empty AMS address and zero tag count.
	// Magic: 03 66 14 71, followed by request parameters
	packet := make([]byte, 24)
	binary.LittleEndian.PutUint32(packet[0:4], 0x71146603)  // Magic (little-endian)
	binary.LittleEndian.PutUint32(packet[4:8], 0x00000000)  // Request ID
	binary.LittleEndian.PutUint32(packet[8:12], 0x00000001) // Service: discovery

	deadline := time.Now().Add(timeout)
	for _, broadcastAddr := range broadcastAddrs {
		if !time.Now().Before(deadline) {
			break
		}
		addr := fmt.Sprintf("%s:%d", broadcastAddr, DiscoveryUDPPort)

		conn, err := net.ListenPacket("udp4", ":0")
		if err != nil {
			continue
		}

		destAddr, err := net.ResolveUDPAddr("udp4", addr)
		if err != nil {
			conn.Close()
			continue
		}

		if err := conn.SetDeadline(deadline); err != nil {
			conn.Close()
			continue
		}
		if _, err := conn.WriteTo(packet, destAddr); err != nil {
			conn.Close()
			continue
		}

		buf := make([]byte, 512)
		for {
			n, srcAddr, err := conn.ReadFrom(buf)
			if err != nil {
				break // Timeout
			}

			udpAddr, ok := srcAddr.(*net.UDPAddr)
			if !ok || n < 18 {
				continue
			}

			ipStr := udpAddr.IP.String()
			if seen[ipStr] {
				continue
			}

			device := parseDiscoveryResponse(buf[:n], udpAddr.IP)
			if device != nil {
				mu.Lock()
				seen[ipStr] = true
				results = append(results, *device)
				mu.Unlock()
			}
		}
		conn.Close()
	}

	return results
}

// SERVERINFO identity and TLV fields follow the official Beckhoff AdsLib UDP
// implementation. Visibility verifies identity, not an ADS runtime route.
func parseDiscoveryResponse(data []byte, sourceIP net.IP) *DiscoveredDevice {
	if sourceIP.To4() == nil || len(data) < 18 || binary.LittleEndian.Uint32(data[:4]) != 0x71146603 || binary.LittleEndian.Uint32(data[4:8]) != 0 || binary.LittleEndian.Uint32(data[8:12]) != 0x80000001 {
		return nil
	}
	id := AmsNetId(data[12:18])
	if id.IsZero() {
		return nil
	}
	device := &DiscoveredDevice{IP: append(net.IP(nil), sourceIP...), Port: DefaultTCPPort, AmsNetId: id.String(), Connected: true, HasRoute: false, ProductName: "Beckhoff TwinCAT"}
	// The SDK accepts the minimal six-byte identity. Optional TLVs require
	// complete AMS address/count fields and complete bounded tag payloads.
	if len(data) == 18 || len(data) == 20 {
		return device
	}
	if len(data) < 24 {
		return nil
	}
	count := uint64(binary.LittleEndian.Uint32(data[20:24]))
	if count > uint64(len(data)-24)/4 {
		return nil
	}
	offset := 24
	seen := make(map[uint16]bool)
	for index := uint64(0); index < count; index++ {
		if len(data)-offset < 4 {
			return nil
		}
		tag, size := binary.LittleEndian.Uint16(data[offset:offset+2]), int(binary.LittleEndian.Uint16(data[offset+2:offset+4]))
		offset += 4
		if size > len(data)-offset || seen[tag] {
			return nil
		}
		seen[tag] = true
		value := data[offset : offset+size]
		offset += size
		if tag == 5 {
			if size == 0 || value[size-1] != 0 {
				return nil
			}
			device.Hostname = extractPrintableString(value)
		}
	}
	if offset != len(data) {
		return nil
	}
	if device.Hostname != "" {
		device.ProductName = "TwinCAT on " + device.Hostname
	}
	return device
}

// extractPrintableString extracts printable ASCII characters from data.
func extractPrintableString(data []byte) string {
	var result []byte
	for _, b := range data {
		if b == 0 {
			break
		}
		if b >= 32 && b < 127 {
			result = append(result, b)
		}
	}
	return string(result)
}

// Discover scans a list of IP addresses for TwinCAT devices via TCP port 48898.
func Discover(ips []net.IP, timeout time.Duration, concurrency int) []DiscoveredDevice {
	if !netutil.ValidScan(ips) {
		return nil
	}
	if timeout <= 0 {
		timeout = 500 * time.Millisecond
	}
	concurrency = netutil.ScanWorkers(concurrency, len(ips))

	var (
		results []DiscoveredDevice
		mu      sync.Mutex
		wg      sync.WaitGroup
		sem     = make(chan struct{}, concurrency)
	)

	for _, ip := range ips {
		wg.Add(1)
		sem <- struct{}{}

		go func(ip net.IP) {
			defer wg.Done()
			defer func() { <-sem }()

			if device := probeADS(ip, timeout); device != nil {
				mu.Lock()
				results = append(results, *device)
				mu.Unlock()
			}
		}(ip)
	}

	wg.Wait()
	return results
}

// DiscoverSubnet scans a subnet for TwinCAT devices.
func DiscoverSubnet(cidr string, timeout time.Duration, concurrency int) ([]DiscoveredDevice, error) {
	ips, err := expandCIDR(cidr)
	if err != nil {
		return nil, err
	}
	return Discover(ips, timeout, concurrency), nil
}

// probeADS returns only a verified ADS identity, never an arbitrary open port.
func probeADS(ip net.IP, timeout time.Duration) *DiscoveredDevice {
	if ip.To4() == nil {
		return nil
	}
	deadline := time.Now().Add(timeout)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp4", net.JoinHostPort(ip.String(), fmt.Sprint(DefaultTCPPort)))
	if err != nil {
		return nil
	}
	defer conn.Close()
	return tryADSDeviceInfoUntil(conn, ip, deadline)
}

func tryADSDeviceInfo(conn net.Conn, ip net.IP) *DiscoveredDevice {
	return tryADSDeviceInfoUntil(conn, ip, time.Now().Add(500*time.Millisecond))
}

func tryADSDeviceInfoUntil(conn net.Conn, ip net.IP, deadline time.Time) *DiscoveredDevice {
	target, err := AmsNetIdFromIP(ip.String())
	if err != nil {
		return nil
	}
	local := AmsNetId{127, 0, 0, 1, 1, 1}
	if address, ok := conn.LocalAddr().(*net.TCPAddr); ok {
		local, err = AmsNetIdFromIP(address.IP.String())
		if err != nil {
			return nil
		}
	}
	stream := newAdsConnection(conn, local, 32768)
	stream.maxPayload = 1024
	data, err := stream.sendRequestUntil(target, PortTC3PLC1, CmdReadDeviceInfo, nil, deadline)
	if err != nil {
		return nil
	}
	info, err := decodeDeviceInfo(data)
	if err != nil {
		return nil
	}
	return &DiscoveredDevice{IP: append(net.IP(nil), ip...), Port: DefaultTCPPort, AmsNetId: target.String(), ProductName: info.String(), TwinCATVersion: fmt.Sprintf("%d.%d.%d", info.MajorVersion, info.MinorVersion, info.BuildVersion), Connected: true, HasRoute: true}
}

// DiscoverSubnet reports broad/IPv6 CIDRs as errors before any network traffic.
func expandCIDR(cidr string) ([]net.IP, error) { return netutil.ExpandIPv4(cidr) }
