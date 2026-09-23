package ads

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"syscall"
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

// TwinCAT UDP discovery ("Broadcast Search" / Get Info) on UDP 48899. Wire
// format (little-endian): magic 0x71146603, invoke ID, service, source AMS
// NetID (6 bytes) + AMS port, tag count, then tag/length/value fields. A Get
// Info request is service 1 with a zero NetID, AMS port 10000 and no tags; the
// reply is service 0x80000001 carrying the device's real AMS NetID at bytes
// 12..18, then tags such as 0x0005 hostname and 0x0003 TwinCAT version.
// References: pyads adsGetNetIdForPLC (pyads/pyads_ex.py) sends exactly this
// 24-byte request to UDP 48899 and reads the NetID from reply bytes 12..18;
// Beckhoff's AdsLib (github.com/Beckhoff/ADS, `AdsTool <ip> netid`) and the
// TwinCAT engineering "Broadcast Search" use the same service. The reply
// proves identity only; it does not prove an ADS route exists.
const (
	udpDiscoveryMagic   uint32 = 0x71146603
	udpServiceGetInfo   uint32 = 0x00000001
	udpServiceResponse  uint32 = 0x80000000
	udpTagTwinCATVer    uint16 = 0x0003
	udpTagHostname      uint16 = 0x0005
	udpRequestAmsPort   uint16 = 10000
	udpResponseMaxBytes        = 8192
)

// Ports are variables only so loopback tests can substitute fake peers.
var (
	discoveryUDPPort = DiscoveryUDPPort
	discoveryTCPPort = DefaultTCPPort
)

// DiscoverBroadcast performs UDP broadcast discovery for TwinCAT devices.
// This finds devices and retrieves their AMS Net ID without requiring a route.
func DiscoverBroadcast(broadcastAddrs []string, timeout time.Duration) []DiscoveredDevice {
	destinations := make([]string, len(broadcastAddrs))
	for i, broadcastAddr := range broadcastAddrs {
		destinations[i] = net.JoinHostPort(broadcastAddr, fmt.Sprint(discoveryUDPPort))
	}
	return discoverUDP(destinations, timeout)
}

// discoverUDP sends every probe from one socket before collecting replies, so
// each destination has the whole timeout window rather than only the first.
func discoverUDP(destinations []string, timeout time.Duration) []DiscoveredDevice {
	devices, _ := udpGetInfo(destinations, nil, timeout)
	return devices
}

func getInfoRequest() []byte {
	packet := make([]byte, 24)
	binary.LittleEndian.PutUint32(packet[0:4], udpDiscoveryMagic)
	binary.LittleEndian.PutUint32(packet[4:8], 0) // invoke ID, echoed by the device
	binary.LittleEndian.PutUint32(packet[8:12], udpServiceGetInfo)
	// packet[12:18]: zero source AMS NetID (as pyads sends).
	binary.LittleEndian.PutUint16(packet[18:20], udpRequestAmsPort)
	binary.LittleEndian.PutUint32(packet[20:24], 0) // no tags
	return packet
}

// udpGetInfo sends one Get Info datagram to each "host:port" destination from a
// single socket, then collects replies until the window closes. With a non-nil
// unicast set and no other destinations, replies from other hosts are ignored
// and collection ends early once every target has answered. Send failures are
// returned (summarized for unicast floods); replies are never retransmitted.
func udpGetInfo(destinations []string, unicast map[string]bool, timeout time.Duration) ([]DiscoveredDevice, []error) {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	var errs []error
	deadline := time.Now().Add(timeout)
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, []error{fmt.Errorf("ADS UDP discovery socket: %w", err)}
	}
	defer conn.Close()
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, []error{fmt.Errorf("ADS UDP discovery deadline: %w", err)}
	}
	packet := getInfoRequest()
	sent, unicastFailures := 0, 0
	var firstUnicastErr error
	onlyUnicast := unicast != nil
	for _, destination := range destinations {
		destAddr, err := net.ResolveUDPAddr("udp4", destination)
		if err == nil {
			_, err = conn.WriteTo(packet, destAddr)
			if err != nil && errors.Is(err, syscall.ENOBUFS) {
				time.Sleep(2 * time.Millisecond) // send queue full during a subnet sweep
				_, err = conn.WriteTo(packet, destAddr)
			}
		}
		if err == nil {
			sent++
			continue
		}
		host, _, _ := net.SplitHostPort(destination)
		if unicast[host] {
			unicastFailures++
			if firstUnicastErr == nil {
				firstUnicastErr = err
			}
			continue
		}
		onlyUnicast = false
		errs = append(errs, fmt.Errorf("ADS UDP discovery send to %s: %w", destination, err))
	}
	if unicastFailures > 0 {
		errs = append(errs, fmt.Errorf("ADS UDP discovery: %d unicast probe(s) not sent (first: %w)", unicastFailures, firstUnicastErr))
	}
	if sent == 0 {
		return nil, errs
	}
	if onlyUnicast && len(destinations) != len(unicast) {
		onlyUnicast = false // some destination was not a unicast target
	}

	var results []DiscoveredDevice
	seen := make(map[string]bool)
	answered, readErrors := 0, 0
	buf := make([]byte, udpResponseMaxBytes)
	for time.Now().Before(deadline) {
		n, srcAddr, err := conn.ReadFrom(buf)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				break
			}
			// e.g. an ICMP-induced reset on some platforms; bounded by the
			// deadline and by a small error budget so it never spins.
			if readErrors++; readErrors > 32 {
				errs = append(errs, fmt.Errorf("ADS UDP discovery receive: %w", err))
				break
			}
			continue
		}
		udpAddr, ok := srcAddr.(*net.UDPAddr)
		if !ok {
			continue
		}
		ipStr := udpAddr.IP.String()
		if seen[ipStr] || (onlyUnicast && !unicast[ipStr]) {
			continue
		}
		device := parseDiscoveryResponse(buf[:n], udpAddr.IP)
		if device == nil {
			continue
		}
		seen[ipStr] = true
		results = append(results, *device)
		if unicast[ipStr] {
			answered++
		}
		if onlyUnicast && answered == len(unicast) {
			break
		}
	}
	return results, errs
}

// parseDiscoveryResponse validates the fixed header strictly (magic, echoed
// invoke ID 0, Get Info response service, non-zero NetID) and parses the tag
// section defensively: unknown tags are skipped, a duplicate tag keeps its
// first value, and a truncated tag or trailing bytes end parsing while keeping
// the identity already proven by the header. It never panics.
func parseDiscoveryResponse(data []byte, sourceIP net.IP) *DiscoveredDevice {
	if sourceIP.To4() == nil || len(data) < 18 || binary.LittleEndian.Uint32(data[:4]) != udpDiscoveryMagic ||
		binary.LittleEndian.Uint32(data[4:8]) != 0 || binary.LittleEndian.Uint32(data[8:12]) != udpServiceResponse|udpServiceGetInfo {
		return nil
	}
	id := AmsNetId(data[12:18])
	if id.IsZero() {
		return nil
	}
	device := &DiscoveredDevice{IP: append(net.IP(nil), sourceIP.To4()...), Port: DefaultTCPPort, AmsNetId: id.String(), Connected: true, HasRoute: false, ProductName: "Beckhoff TwinCAT"}
	if len(data) >= 24 {
		count := uint64(binary.LittleEndian.Uint32(data[20:24]))
		offset := 24
		seen := make(map[uint16]bool)
		for index := uint64(0); index < count && len(data)-offset >= 4; index++ {
			tag, size := binary.LittleEndian.Uint16(data[offset:offset+2]), int(binary.LittleEndian.Uint16(data[offset+2:offset+4]))
			offset += 4
			if size > len(data)-offset {
				break // truncated tag: keep the identity, drop the partial value
			}
			value := data[offset : offset+size]
			offset += size
			if seen[tag] {
				continue
			}
			seen[tag] = true
			switch tag {
			case udpTagHostname:
				device.Hostname = extractPrintableString(value)
			case udpTagTwinCATVer:
				if size >= 4 {
					device.TwinCATVersion = fmt.Sprintf("%d.%d.%d", value[0], value[1], binary.LittleEndian.Uint16(value[2:4]))
				}
			}
		}
	}
	if device.Hostname != "" {
		device.ProductName = "TwinCAT on " + device.Hostname
	}
	return device
}

// extractPrintableString extracts printable ASCII characters up to a NUL.
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

// Discover identifies TwinCAT devices at the given IPv4 addresses. Each address
// first receives a unicast UDP Get Info (port 48899), which also works across
// routed subnets and reports the device's real AMS NetID, hostname and TwinCAT
// version. Only addresses that do not answer are probed over TCP 48898, where
// the target NetID must be guessed as IP+".1.1".
func Discover(ips []net.IP, timeout time.Duration, concurrency int) []DiscoveredDevice {
	devices, _ := DiscoverWithReport(ips, nil, timeout, concurrency)
	return devices
}

// DiscoverWithReport is Discover plus optional UDP broadcast destinations
// (addresses such as "192.168.5.255", sent in the same UDP pass) and a report
// of failures: socket errors, rejected broadcasts (for example no broadcast
// permission) and unsent unicast probes. Devices answering a broadcast from
// outside ips are included. Each phase is bounded by timeout.
func DiscoverWithReport(ips []net.IP, broadcastAddrs []string, timeout time.Duration, concurrency int) ([]DiscoveredDevice, []error) {
	if len(ips) != 0 && !netutil.ValidScan(ips) {
		return nil, []error{fmt.Errorf("ADS discovery: scan list must be at most %d IPv4 addresses", netutil.MaxScanAddresses)}
	}
	if len(ips) == 0 && len(broadcastAddrs) == 0 {
		return nil, nil
	}
	if timeout <= 0 {
		timeout = 500 * time.Millisecond
	}
	destinations := make([]string, 0, len(ips)+len(broadcastAddrs))
	unicast := make(map[string]bool, len(ips))
	for _, ip := range ips {
		key := ip.To4().String()
		if !unicast[key] {
			unicast[key] = true
			destinations = append(destinations, net.JoinHostPort(key, fmt.Sprint(discoveryUDPPort)))
		}
	}
	for _, address := range broadcastAddrs {
		destinations = append(destinations, net.JoinHostPort(address, fmt.Sprint(discoveryUDPPort)))
	}
	results, errs := udpGetInfo(destinations, unicast, timeout)
	answered := make(map[string]bool, len(results))
	for _, device := range results {
		answered[device.IP.String()] = true
	}
	var fallback []net.IP
	for _, ip := range ips {
		if key := ip.To4().String(); !answered[key] {
			answered[key] = true // also deduplicates the TCP list
			fallback = append(fallback, ip)
		}
	}
	return append(results, discoverTCP(fallback, timeout, concurrency)...), errs
}

// discoverTCP probes each address over TCP 48898 with an identity request.
func discoverTCP(ips []net.IP, timeout time.Duration, concurrency int) []DiscoveredDevice {
	if len(ips) == 0 {
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
// It is the fallback for hosts that do not answer UDP Get Info; the target
// NetID is guessed as IP+".1.1", so devices with a different NetID (for
// example 5.45.219.226.1.1 at 192.168.5.212) are only found through UDP.
func probeADS(ip net.IP, timeout time.Duration) *DiscoveredDevice {
	if ip.To4() == nil {
		return nil
	}
	deadline := time.Now().Add(timeout)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp4", net.JoinHostPort(ip.String(), fmt.Sprint(discoveryTCPPort)))
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
