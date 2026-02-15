package omron

import (
	"fmt"
	"net"
	"sync"
	"time"

	"plcio/logging"
)

// NetworkDeviceInfo contains identity information about a discovered Omron PLC.
type NetworkDeviceInfo struct {
	IP          net.IP // Device IP address
	Port        uint16 // FINS port (9600)
	Node        byte   // FINS node number (usually last octet of IP)
	ProductName string // Product name if available
	Protocol    string // "FINS/UDP", "FINS/TCP", or "EIP"
	Connected   bool   // True if successfully connected and identified
}

// NetworkDiscover scans a list of IP addresses for Omron PLCs by attempting
// to connect via FINS (UDP port 9600) and EIP (TCP port 44818).
//
// ips is a list of IP addresses to probe.
// timeout is the connection timeout per device (e.g., 500ms).
// concurrency is the number of parallel probes (e.g., 20).
//
// Returns discovered devices that responded to Omron protocols.
func NetworkDiscover(ips []net.IP, timeout time.Duration, concurrency int) []NetworkDeviceInfo {
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
		results []NetworkDeviceInfo
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

			device := probeOmron(ip, timeout)
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

// NetworkDiscoverSubnet scans a subnet for Omron PLCs.
// cidr is in the format "192.168.1.0/24".
func NetworkDiscoverSubnet(cidr string, timeout time.Duration, concurrency int) ([]NetworkDeviceInfo, error) {
	ips, err := expandCIDRomron(cidr)
	if err != nil {
		return nil, err
	}
	return NetworkDiscover(ips, timeout, concurrency), nil
}

// probeOmron attempts to connect to an Omron PLC using FINS protocols.
// Note: EIP discovery is handled separately by the main EIP broadcast discovery
// to avoid misidentifying non-Omron EIP devices as Omron.
func probeOmron(ip net.IP, timeout time.Duration) *NetworkDeviceInfo {
	// Try FINS/UDP first (most common for older Omron PLCs)
	if device := probeFINSUDP(ip, timeout); device != nil {
		logging.DebugLog("tui", "FINS probeOmron: %s responded to FINS/UDP", ip.String())
		return device
	}

	// Try FINS/TCP
	if device := probeFINSTCP(ip, timeout); device != nil {
		logging.DebugLog("tui", "FINS probeOmron: %s responded to FINS/TCP", ip.String())
		return device
	}

	return nil
}

// probeFINSUDP attempts to connect via FINS/UDP.
func probeFINSUDP(ip net.IP, timeout time.Duration) *NetworkDeviceInfo {
	addr := fmt.Sprintf("%s:%d", ip.String(), defaultFINSPort)

	conn, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		return nil
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(timeout))

	// Derive node number from IP (common convention)
	node := ip[len(ip)-1]

	// Build a simple FINS status read command (Controller Status Read)
	// FINS Frame: ICF(1) RSV(1) GCT(1) DNA(1) DA1(1) DA2(1) SNA(1) SA1(1) SA2(1) SID(1) CMD(2) DATA...
	fins := []byte{
		0x80, // ICF: Command, needs response
		0x00, // RSV: Reserved
		0x02, // GCT: Gateway count
		0x00, // DNA: Destination network (local)
		node, // DA1: Destination node (PLC)
		0x00, // DA2: Destination unit
		0x00, // SNA: Source network (local)
		0x01, // SA1: Source node (us)
		0x00, // SA2: Source unit
		0x00, // SID: Service ID
		0x06, 0x01, // Command: Controller Status Read
	}

	if _, err := conn.Write(fins); err != nil {
		return nil
	}

	// Read response
	resp := make([]byte, 256)
	n, err := conn.Read(resp)
	if err != nil {
		return nil
	}

	// Check for valid FINS response
	if n < 14 {
		return nil
	}

	// Check ICF for response flag (bit 6 = 1 for response)
	if resp[0]&0x40 == 0 {
		return nil
	}

	// Check for FINS end codes
	endCode := uint16(resp[12])<<8 | uint16(resp[13])

	// End code 0x0000 = success, other codes indicate various statuses
	// Any response means it's a FINS device
	productName := "Omron PLC"
	if endCode == 0x0000 && n >= 16 {
		// Parse controller status
		productName = "Omron PLC (FINS/UDP)"
	}

	return &NetworkDeviceInfo{
		IP:          ip,
		Port:        defaultFINSPort,
		Node:        node,
		ProductName: productName,
		Protocol:    "FINS/UDP",
		Connected:   true,
	}
}

// probeFINSTCP attempts to connect via FINS/TCP.
func probeFINSTCP(ip net.IP, timeout time.Duration) *NetworkDeviceInfo {
	addr := fmt.Sprintf("%s:%d", ip.String(), defaultFINSPort)

	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(timeout))

	// FINS/TCP requires a handshake first
	// Send FINS/TCP node address request
	nodeReq := []byte{
		'F', 'I', 'N', 'S', // Magic
		0x00, 0x00, 0x00, 0x0C, // Length (12 bytes)
		0x00, 0x00, 0x00, 0x00, // Command: Node address request
		0x00, 0x00, 0x00, 0x00, // Error code
		0x00, 0x00, 0x00, 0x00, // Client node (0 = auto)
	}

	if _, err := conn.Write(nodeReq); err != nil {
		return nil
	}

	// Read response
	resp := make([]byte, 24)
	n, err := conn.Read(resp)
	if err != nil || n < 24 {
		return nil
	}

	// Check for FINS magic
	if string(resp[0:4]) != "FINS" {
		return nil
	}

	// Check command response (should be 0x00000001)
	cmd := uint32(resp[8])<<24 | uint32(resp[9])<<16 | uint32(resp[10])<<8 | uint32(resp[11])
	if cmd != 0x00000001 {
		return nil
	}

	// Parse server node from response
	serverNode := resp[19]

	return &NetworkDeviceInfo{
		IP:          ip,
		Port:        defaultFINSPort,
		Node:        serverNode,
		ProductName: "Omron PLC (FINS/TCP)",
		Protocol:    "FINS/TCP",
		Connected:   true,
	}
}

// expandCIDRomron expands a CIDR notation to a list of IP addresses.
func expandCIDRomron(cidr string) ([]net.IP, error) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR: %w", err)
	}

	var ips []net.IP
	for ip := ip.Mask(ipnet.Mask); ipnet.Contains(ip); incOmron(ip) {
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

// incOmron increments an IP address.
func incOmron(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}
