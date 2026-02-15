package s7

import (
	"fmt"
	"net"
	"sync"
	"time"
)

// DiscoveredDevice contains identity information about a discovered S7 PLC.
type DiscoveredDevice struct {
	IP          net.IP // Device IP address
	Port        uint16 // S7 port (102)
	Rack        int    // Configured rack (default 0)
	Slot        int    // Configured slot (default 0 for S7-1200/1500, 2 for S7-300/400)
	ProductName string // Product name if available
	Connected   bool   // True if successfully connected and identified
}

// Discover scans a list of IP addresses for S7 PLCs by attempting to connect
// to TCP port 102 and perform COTP/S7 handshake.
//
// ips is a list of IP addresses to probe.
// timeout is the connection timeout per device (e.g., 500ms).
// concurrency is the number of parallel probes (e.g., 20).
//
// Returns discovered devices that responded to S7 protocol.
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

			device := probeS7(ip, timeout)
			if device != nil {
				mu.Lock()
				results = append(results, *device)
				mu.Unlock()
			}
		}(ip)
	}

	wg.Wait()
	return results
}

// DiscoverSubnet scans a subnet for S7 PLCs.
// cidr is in the format "192.168.1.0/24".
func DiscoverSubnet(cidr string, timeout time.Duration, concurrency int) ([]DiscoveredDevice, error) {
	ips, err := expandCIDR(cidr)
	if err != nil {
		return nil, err
	}
	return Discover(ips, timeout, concurrency), nil
}

// probeS7 attempts to connect to an S7 PLC and identify it.
func probeS7(ip net.IP, timeout time.Duration) *DiscoveredDevice {
	addr := fmt.Sprintf("%s:%d", ip.String(), defaultS7Port)

	// Try S7-1200/1500 first (rack 0, slot 0)
	if device := tryS7Connect(ip, addr, 0, 0, timeout); device != nil {
		return device
	}

	// Try S7-300/400 (rack 0, slot 2)
	if device := tryS7Connect(ip, addr, 0, 2, timeout); device != nil {
		return device
	}

	return nil
}

// tryS7Connect attempts a full S7 connection with specific rack/slot.
func tryS7Connect(ip net.IP, addr string, rack, slot int, timeout time.Duration) *DiscoveredDevice {
	// Attempt TCP connection first
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil
	}
	defer conn.Close()

	// Set read/write deadline
	conn.SetDeadline(time.Now().Add(timeout))

	// Build COTP Connection Request
	srcTSAP := []byte{0x01, 0x00}
	dstTSAP := []byte{0x01, byte((rack << 5) | slot)}

	cr := []byte{
		0x11,       // Length (will be adjusted)
		0xE0,       // COTP CR (Connection Request)
		0x00, 0x00, // Destination reference
		0x00, 0x01, // Source reference
		0x00, // Class/options
	}
	cr = append(cr, cotpParamSrcTSAP, byte(len(srcTSAP)))
	cr = append(cr, srcTSAP...)
	cr = append(cr, cotpParamDstTSAP, byte(len(dstTSAP)))
	cr = append(cr, dstTSAP...)
	cr = append(cr, cotpParamTPDUSize, 0x01, 0x0A) // 1024 bytes
	cr[0] = byte(len(cr) - 1)

	// Wrap in TPKT
	tpkt := make([]byte, 4+len(cr))
	tpkt[0] = 0x03 // Version
	tpkt[1] = 0x00 // Reserved
	tpkt[2] = byte((len(tpkt) >> 8) & 0xFF)
	tpkt[3] = byte(len(tpkt) & 0xFF)
	copy(tpkt[4:], cr)

	if _, err := conn.Write(tpkt); err != nil {
		return nil
	}

	// Read TPKT response
	header := make([]byte, 4)
	if _, err := conn.Read(header); err != nil {
		return nil
	}

	if header[0] != 0x03 {
		return nil
	}

	length := int(header[2])<<8 | int(header[3])
	if length < 7 || length > 1024 {
		return nil
	}

	payload := make([]byte, length-4)
	if _, err := conn.Read(payload); err != nil {
		return nil
	}

	// Check for COTP CC (Connection Confirm)
	if len(payload) < 2 || payload[1] != 0xD0 {
		return nil
	}

	// S7 responded - it's an S7 device
	return &DiscoveredDevice{
		IP:          ip,
		Port:        defaultS7Port,
		Rack:        rack,
		Slot:        slot,
		ProductName: "Siemens S7 PLC",
		Connected:   true,
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
		// Skip network and broadcast addresses for /24 and larger
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
