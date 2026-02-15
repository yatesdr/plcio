package ads

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"
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
	HasRoute       bool   // True if route is configured (device responds to ADS requests)
	Connected      bool   // True if successfully identified
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

	// TwinCAT discovery packet (32 bytes)
	// Magic: 03 66 14 71, followed by request parameters
	packet := make([]byte, 32)
	binary.LittleEndian.PutUint32(packet[0:4], 0x71146603)  // Magic (little-endian)
	binary.LittleEndian.PutUint32(packet[4:8], 0x00000000)  // Request ID
	binary.LittleEndian.PutUint32(packet[8:12], 0x00000001) // Service: discovery

	for _, broadcastAddr := range broadcastAddrs {
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

		conn.WriteTo(packet, destAddr)
		conn.SetReadDeadline(time.Now().Add(timeout))

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

// parseDiscoveryResponse parses a TwinCAT UDP discovery response.
// Response format:
//   - Bytes 0-3: Magic (03 66 14 71)
//   - Bytes 4-7: Request ID echo
//   - Bytes 8-11: Service response (01 00 00 80 - 0x80 indicates response)
//   - Bytes 12-17: AMS Net ID (6 bytes)
//   - Bytes 18-19: Port (little-endian, typically 10000)
//   - Bytes 20-23: Flags
//   - Bytes 24-25: Unknown
//   - Byte 26: Hostname length
//   - Byte 27: Unknown
//   - Bytes 28+: Hostname (null-terminated)
func parseDiscoveryResponse(data []byte, sourceIP net.IP) *DiscoveredDevice {
	if len(data) < 18 {
		return nil
	}

	// Verify magic bytes
	if data[0] != 0x03 || data[1] != 0x66 || data[2] != 0x14 || data[3] != 0x71 {
		return nil
	}

	device := &DiscoveredDevice{
		IP:        sourceIP,
		Port:      DefaultTCPPort,
		Connected: true,
		HasRoute:  true,
	}

	// AMS Net ID at offset 12 (6 bytes)
	amsBytes := data[12:18]
	device.AmsNetId = fmt.Sprintf("%d.%d.%d.%d.%d.%d",
		amsBytes[0], amsBytes[1], amsBytes[2], amsBytes[3], amsBytes[4], amsBytes[5])

	// Validate AMS Net ID (not all zeros)
	allZero := true
	for _, b := range amsBytes {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		device.AmsNetId = ""
	}

	// Hostname at offset 28 (length at offset 26)
	if len(data) > 28 {
		hostnameLen := int(data[26])
		if hostnameLen > 0 && 28+hostnameLen <= len(data) {
			device.Hostname = extractPrintableString(data[28 : 28+hostnameLen])
		}
	}

	// Build product name
	if device.Hostname != "" {
		device.ProductName = fmt.Sprintf("TwinCAT on %s", device.Hostname)
	} else {
		device.ProductName = "Beckhoff TwinCAT"
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
	if len(ips) == 0 {
		return nil
	}
	if timeout <= 0 {
		timeout = 500 * time.Millisecond
	}
	if concurrency <= 0 {
		concurrency = 20
	}

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

// probeADS attempts to connect to a TwinCAT device via TCP.
func probeADS(ip net.IP, timeout time.Duration) *DiscoveredDevice {
	addr := fmt.Sprintf("%s:%d", ip.String(), DefaultTCPPort)

	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))

	// Try to get device info via ADS protocol
	if device := tryADSDeviceInfo(conn, ip); device != nil {
		return device
	}

	// TCP connected but no ADS response - device exists but no route configured
	return &DiscoveredDevice{
		IP:          ip,
		Port:        DefaultTCPPort,
		ProductName: "Beckhoff TwinCAT (no route)",
		Connected:   false,
		HasRoute:    false,
	}
}

// tryADSDeviceInfo attempts to read device info via ADS ReadDeviceInfo command.
func tryADSDeviceInfo(conn net.Conn, ip net.IP) *DiscoveredDevice {
	// Build AMS Net ID from IP (convention: ip.ip.ip.ip.1.1)
	var netId [6]byte
	ip4 := ip.To4()
	if ip4 != nil {
		netId = [6]byte{ip4[0], ip4[1], ip4[2], ip4[3], 1, 1}
	} else if len(ip) >= 16 {
		netId = [6]byte{ip[12], ip[13], ip[14], ip[15], 1, 1}
	} else {
		return nil
	}

	// Build ADS ReadDeviceInfo request
	packet := make([]byte, 38)
	binary.LittleEndian.PutUint16(packet[0:2], 0)  // Reserved
	binary.LittleEndian.PutUint32(packet[2:6], 32) // AMS header length

	copy(packet[6:12], netId[:])                              // Target Net ID
	binary.LittleEndian.PutUint16(packet[12:14], PortTC3PLC1) // Target Port
	copy(packet[14:20], netId[:])                             // Source Net ID
	binary.LittleEndian.PutUint16(packet[20:22], 32768)       // Source Port
	binary.LittleEndian.PutUint16(packet[22:24], CmdReadDeviceInfo)
	binary.LittleEndian.PutUint16(packet[24:26], StateFlagRequest)
	binary.LittleEndian.PutUint32(packet[26:30], 0) // Data length
	binary.LittleEndian.PutUint32(packet[30:34], 0) // Error code
	binary.LittleEndian.PutUint32(packet[34:38], 1) // Invoke ID

	if _, err := conn.Write(packet); err != nil {
		return nil
	}

	// Read response header
	respHeader := make([]byte, 6)
	if _, err := conn.Read(respHeader); err != nil {
		return nil
	}

	respLen := binary.LittleEndian.Uint32(respHeader[2:6])
	if respLen < 32 || respLen > 1024 {
		return nil
	}

	respData := make([]byte, respLen)
	if _, err := conn.Read(respData); err != nil {
		return nil
	}

	if len(respData) < 32 {
		return nil
	}

	// Verify response
	cmdId := binary.LittleEndian.Uint16(respData[16:18])
	if cmdId != CmdReadDeviceInfo {
		return nil
	}

	stateFlags := binary.LittleEndian.Uint16(respData[18:20])
	if stateFlags&0x0001 == 0 {
		return nil
	}

	amsNetIdStr := fmt.Sprintf("%d.%d.%d.%d.%d.%d", netId[0], netId[1], netId[2], netId[3], netId[4], netId[5])

	errorCode := binary.LittleEndian.Uint32(respData[24:28])
	if errorCode != 0 || len(respData) < 56 {
		return &DiscoveredDevice{
			IP:          ip,
			Port:        DefaultTCPPort,
			AmsNetId:    amsNetIdStr,
			ProductName: "Beckhoff TwinCAT",
			Connected:   true,
			HasRoute:    true,
		}
	}

	// Parse device info
	deviceData := respData[32:]
	major := deviceData[0]
	minor := deviceData[1]
	build := binary.LittleEndian.Uint16(deviceData[2:4])
	deviceName := extractPrintableString(deviceData[4:20])

	productName := fmt.Sprintf("%s v%d.%d.%d", deviceName, major, minor, build)
	if deviceName == "" {
		productName = fmt.Sprintf("TwinCAT v%d.%d.%d", major, minor, build)
	}

	return &DiscoveredDevice{
		IP:             ip,
		Port:           DefaultTCPPort,
		AmsNetId:       amsNetIdStr,
		ProductName:    productName,
		TwinCATVersion: fmt.Sprintf("%d.%d.%d", major, minor, build),
		Connected:      true,
		HasRoute:       true,
	}
}

// expandCIDR expands a CIDR notation to a list of IP addresses.
func expandCIDR(cidr string) ([]net.IP, error) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR: %w", err)
	}

	var ips []net.IP
	for ip := ip.Mask(ipnet.Mask); ipnet.Contains(ip); inc(ip) {
		ones, bits := ipnet.Mask.Size()
		if bits-ones >= 8 {
			if ip[len(ip)-1] == 0 || ip[len(ip)-1] == 255 {
				continue
			}
		}
		ipCopy := make(net.IP, len(ip))
		copy(ipCopy, ip)
		ips = append(ips, ipCopy)
	}

	return ips, nil
}

// inc increments an IP address.
func inc(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}
