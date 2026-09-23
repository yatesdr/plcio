package driver

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/yatesdr/plcio/ads"
	"github.com/yatesdr/plcio/eip"
	"github.com/yatesdr/plcio/internal/netutil"
	"github.com/yatesdr/plcio/logging"
	"github.com/yatesdr/plcio/omron"
	"github.com/yatesdr/plcio/s7"
)

// DiscoveredDevice represents a PLC discovered on the network.
type DiscoveredDevice struct {
	IP           net.IP            // Device IP address
	Port         uint16            // Protocol port
	Family       PLCFamily         // PLC family (logix, s7, beckhoff, omron)
	ProductName  string            // Product name or description
	Protocol     string            // Protocol used for discovery
	Vendor       string            // Vendor name
	Extra        map[string]string // Additional info (serial, revision, etc.)
	DiscoveredAt time.Time         // When this device was discovered
}

// Key returns a unique identifier for deduplication.
func (d *DiscoveredDevice) Key() string {
	return fmt.Sprintf("%s:%d:%s", d.IP.String(), d.Port, d.Protocol)
}

// DiscoverAll performs network discovery using all supported protocols.
// This is the synchronous version that waits for completion. Per-protocol
// failures are dropped; use DiscoverAllWithReport to receive them.
func DiscoverAll(broadcastIP string, scanCIDR string, timeout time.Duration, concurrency int) []DiscoveredDevice {
	devices, _ := DiscoverAllWithReport(broadcastIP, scanCIDR, timeout, concurrency)
	return devices
}

// DiscoverAllWithReport is DiscoverAll that also returns per-protocol failures,
// such as an invalid or oversized scan CIDR, a socket that cannot be opened, a
// broadcast the OS refuses (no broadcast permission / no route) or a protocol
// scan error. Each error names its protocol. Devices found by other protocols
// (or other destinations) are still returned. The error order is unspecified.
func DiscoverAllWithReport(broadcastIP string, scanCIDR string, timeout time.Duration, concurrency int) ([]DiscoveredDevice, []error) {
	if timeout <= 0 {
		timeout = 500 * time.Millisecond
	}
	if concurrency <= 0 {
		concurrency = 20
	}

	logging.DebugLog("discovery", "DiscoverAll: starting with broadcast=%s cidr=%s timeout=%v concurrency=%d",
		broadcastIP, scanCIDR, timeout, concurrency)

	var (
		results []DiscoveredDevice
		errs    []error
		mu      sync.Mutex
		wg      sync.WaitGroup
	)
	collect := func(protocol string, devices []DiscoveredDevice, failures []error) {
		logging.DebugLog("discovery", "DiscoverAll: %s done, found %d devices, %d errors", protocol, len(devices), len(failures))
		mu.Lock()
		results = append(results, devices...)
		errs = append(errs, failures...)
		mu.Unlock()
	}

	scan := scanCIDR != ""
	if scan {
		if _, err := netutil.ExpandIPv4(scanCIDR); err != nil {
			errs = append(errs, fmt.Errorf("scan CIDR %q: %w (S7, ADS and FINS scans skipped)", scanCIDR, err))
			scan = false
		}
	}

	// Run all discoveries in parallel
	// 1. EIP broadcast discovery (Allen-Bradley, Omron NJ/NX)
	wg.Add(1)
	go func() {
		defer wg.Done()
		devices, failures := eipDiscovery(broadcastIP, timeout)
		collect("EIP", devices, failures)
	}()

	if scan {
		wg.Add(4)
		// 1b. EIP unicast ListIdentity across the CIDR (reaches routed subnets)
		go func() {
			defer wg.Done()
			devices, failures := eipUnicastDiscovery(scanCIDR, timeout)
			collect("EIP unicast", devices, failures)
		}()
		// 2. S7 port scan (Siemens)
		go func() {
			defer wg.Done()
			devices, failures := discoverS7Report(scanCIDR, timeout, concurrency)
			collect("S7", devices, failures)
		}()
		// 3. ADS UDP Get Info + TCP fallback (Beckhoff)
		go func() {
			defer wg.Done()
			devices, failures := discoverADSReport(scanCIDR, timeout, concurrency)
			collect("ADS", devices, failures)
		}()
		// 4. FINS discovery (Omron)
		go func() {
			defer wg.Done()
			devices, failures := discoverFINSReport(scanCIDR, timeout, concurrency)
			collect("FINS", devices, failures)
		}()
	} else if scanCIDR == "" {
		logging.DebugLog("discovery", "DiscoverAll: S7/ADS/FINS skipped (no CIDR)")
	}

	wg.Wait()
	logging.DebugLog("discovery", "DiscoverAll: all done, total %d devices before dedup", len(results))

	// Deduplicate by IP (prefer more specific protocol match)
	deduped := deduplicateDevices(results)
	logging.DebugLog("discovery", "DiscoverAll: returning %d devices after dedup", len(deduped))
	return deduped, errs
}

// eipDiscovery is a seam so tests can exercise DiscoverAllWithReport without
// broadcasting onto the real network.
var eipDiscovery = discoverEIPReport

// DiscoverEIPOnly performs EIP broadcast discovery only.
// This is the most stable discovery method, working for Allen-Bradley and Omron NJ/NX PLCs.
func DiscoverEIPOnly(broadcastIP string, timeout time.Duration) []DiscoveredDevice {
	return discoverEIP(broadcastIP, timeout)
}

// discoverEIP performs EIP broadcast discovery.
func discoverEIP(broadcastIP string, timeout time.Duration) []DiscoveredDevice {
	devices, _ := discoverEIPReport(broadcastIP, timeout)
	return devices
}

func discoverEIPReport(broadcastIP string, timeout time.Duration) ([]DiscoveredDevice, []error) {
	var errs []error
	if broadcastIP == "" {
		broadcastIP = "255.255.255.255"
	}

	// Try multiple broadcast addresses for better coverage
	broadcastAddrs := []string{broadcastIP}
	for _, addr := range GetBroadcastAddresses() {
		if addr != broadcastIP {
			broadcastAddrs = append(broadcastAddrs, addr)
		}
	}

	logging.DebugLog("discovery", "EIP discovery: trying broadcast addresses: %v", broadcastAddrs)

	var allIdentities []eip.Identity
	client := eip.NewEipClient("")

	// Use longer timeout for UDP broadcast (devices may be slow to respond)
	udpTimeout := timeout * 3
	if udpTimeout < 2*time.Second {
		udpTimeout = 2 * time.Second
	}

	for _, addr := range broadcastAddrs {
		logging.DebugLog("discovery", "EIP discovery: sending ListIdentity to %s (timeout=%v)", addr, udpTimeout)
		identities, err := client.ListIdentityUDP(addr, udpTimeout)
		if err != nil {
			logging.DebugLog("discovery", "EIP discovery: broadcast to %s error: %v", addr, err)
			errs = append(errs, fmt.Errorf("EIP discovery via %s: %w", addr, err))
			continue
		}
		logging.DebugLog("discovery", "EIP discovery: %s returned %d identities", addr, len(identities))
		allIdentities = append(allIdentities, identities...)
	}

	results := eipIdentitiesToDevices(allIdentities)
	logging.DebugLog("discovery", "EIP discovery: total %d unique device(s)", len(results))
	return results, errs
}

// eipIdentitiesToDevices converts ListIdentity replies to discovered devices,
// one per IP.
func eipIdentitiesToDevices(allIdentities []eip.Identity) []DiscoveredDevice {
	// Deduplicate by IP
	seen := make(map[string]bool)
	var results []DiscoveredDevice

	for _, id := range allIdentities {
		ipStr := id.IP.String()
		if seen[ipStr] {
			continue
		}
		seen[ipStr] = true

		// Determine vendor and family based on vendor ID
		// CIP Vendor IDs: 1=Rockwell Automation, 47=Omron
		family := FamilyLogix
		vendor := "Rockwell Automation"

		switch id.VendorID {
		case 1: // Rockwell Automation
			vendor = "Rockwell Automation"
			// Check if it's Micro800 series (product names start with "2080-")
			if len(id.ProductName) >= 5 && id.ProductName[:5] == "2080-" {
				family = FamilyMicro800
			} else {
				family = FamilyLogix
			}
		case 47: // Omron
			family = FamilyOmron
			vendor = "Omron"
		default:
			// Unknown vendor - log it and default to Logix
			logging.DebugLog("discovery", "EIP discovery: unknown vendor ID %d for %s (%s)",
				id.VendorID, ipStr, id.ProductName)
		}

		logging.DebugLog("discovery", "EIP discovery: found %s at %s (VendorID=%d, Family=%s)",
			id.ProductName, ipStr, id.VendorID, family)

		results = append(results, DiscoveredDevice{
			IP:          id.IP,
			Port:        id.Port,
			Family:      family,
			ProductName: id.ProductName,
			Protocol:    "EIP",
			Vendor:      vendor,
			Extra: map[string]string{
				"serial":   fmt.Sprintf("%d", id.SerialNumber),
				"revision": fmt.Sprintf("%d.%d", id.RevisionMajor, id.RevisionMinor),
				"vendorId": fmt.Sprintf("%d", id.VendorID),
			},
		})
	}

	return results
}

// eipUnicastDiscovery is a seam for tests.
var eipUnicastDiscovery = discoverEIPUnicastReport

// discoverEIPUnicastReport sends ListIdentity to every address in cidr by
// unicast UDP, which (unlike broadcast) reaches devices on routed subnets.
func discoverEIPUnicastReport(cidr string, timeout time.Duration) ([]DiscoveredDevice, []error) {
	ips, err := netutil.ExpandIPv4(cidr)
	if err != nil {
		return nil, []error{fmt.Errorf("EIP unicast discovery: %w", err)}
	}
	udpTimeout := timeout * 3
	if udpTimeout < 2*time.Second {
		udpTimeout = 2 * time.Second
	}
	ids, err := eip.ListIdentityUnicast(ips, udpTimeout)
	var errs []error
	if err != nil {
		errs = append(errs, fmt.Errorf("EIP unicast discovery: %w", err))
	}
	return eipIdentitiesToDevices(ids), errs
}

// discoverS7Report scans for Siemens S7 PLCs.
func discoverS7Report(cidr string, timeout time.Duration, concurrency int) ([]DiscoveredDevice, []error) {
	devices, err := s7.DiscoverSubnet(cidr, timeout, concurrency)
	if err != nil {
		logging.DebugLog("Discovery", "S7 scan error: %v", err)
		return nil, []error{fmt.Errorf("S7 discovery: %w", err)}
	}

	var results []DiscoveredDevice
	for _, dev := range devices {
		results = append(results, DiscoveredDevice{
			IP:          dev.IP,
			Port:        dev.Port,
			Family:      FamilyS7,
			ProductName: dev.ProductName,
			Protocol:    "S7",
			Vendor:      "Siemens",
			Extra: map[string]string{
				"rack": fmt.Sprintf("%d", dev.Rack),
				"slot": fmt.Sprintf("%d", dev.Slot),
			},
		})
	}

	logging.DebugLog("Discovery", "S7 found %d device(s)", len(results))
	return results, nil
}

// discoverADSReport finds Beckhoff TwinCAT devices. One UDP pass sends the TwinCAT
// Get Info request (port 48899) to every address in cidr (unicast, which also
// crosses routed subnets) and to every local broadcast address; replies carry
// the device's real AMS NetID, hostname and TwinCAT version. Only addresses
// that do not answer UDP are probed over TCP 48898, where the NetID has to be
// guessed as IP+".1.1". Extra["hasRoute"] is "true" only for a TCP identity.
func discoverADSReport(cidr string, timeout time.Duration, concurrency int) ([]DiscoveredDevice, []error) {
	ips, err := netutil.ExpandIPv4(cidr)
	if err != nil {
		logging.DebugLog("Discovery", "ADS scan rejected: %v", err)
		return nil, []error{fmt.Errorf("ADS discovery: %w", err)}
	}
	broadcastAddrs := GetBroadcastAddresses()
	logging.DebugLog("discovery", "discoverADS: UDP Get Info to %d address(es) and broadcasts %v", len(ips), broadcastAddrs)
	devices, failures := ads.DiscoverWithReport(ips, broadcastAddrs, timeout, concurrency)
	errs := make([]error, 0, len(failures))
	for _, failure := range failures {
		errs = append(errs, fmt.Errorf("ADS discovery: %w", failure))
	}
	results := make([]DiscoveredDevice, 0, len(devices))
	for _, dev := range devices {
		results = append(results, adsDiscoveredDevice(dev))
	}
	logging.DebugLog("Discovery", "ADS found %d device(s) total", len(results))
	return results, errs
}

func adsDiscoveredDevice(dev ads.DiscoveredDevice) DiscoveredDevice {
	hasRoute := "false"
	if dev.HasRoute {
		hasRoute = "true"
	}
	return DiscoveredDevice{
		IP:          dev.IP,
		Port:        dev.Port,
		Family:      FamilyBeckhoff,
		ProductName: dev.ProductName,
		Protocol:    "ADS",
		Vendor:      "Beckhoff",
		Extra: map[string]string{
			"amsNetId":  dev.AmsNetId,
			"hostname":  dev.Hostname,
			"tcVersion": dev.TwinCATVersion,
			"hasRoute":  hasRoute,
		},
	}
}

// discoverFINS scans for Omron FINS PLCs.
func discoverFINS(cidr string, timeout time.Duration, concurrency int) []DiscoveredDevice {
	devices, _ := discoverFINSReport(cidr, timeout, concurrency)
	return devices
}

func discoverFINSReport(cidr string, timeout time.Duration, concurrency int) ([]DiscoveredDevice, []error) {
	logging.DebugLog("discovery", "discoverFINS: calling omron.NetworkDiscoverSubnet with cidr=%s", cidr)
	devices, err := omron.NetworkDiscoverSubnet(cidr, timeout, concurrency)
	logging.DebugLog("discovery", "discoverFINS: NetworkDiscoverSubnet returned err=%v devices=%d", err, len(devices))
	if err != nil {
		logging.DebugLog("Discovery", "FINS scan error: %v", err)
		return nil, []error{fmt.Errorf("FINS discovery: %w", err)}
	}

	var results []DiscoveredDevice
	for i, dev := range devices {
		logging.DebugLog("discovery", "discoverFINS: device %d: IP=%s Protocol=%s ProductName=%q Node=%d",
			i, dev.IP.String(), dev.Protocol, dev.ProductName, dev.Node)
		results = append(results, DiscoveredDevice{
			IP:          dev.IP,
			Port:        dev.Port,
			Family:      FamilyOmron,
			ProductName: dev.ProductName,
			Protocol:    dev.Protocol,
			Vendor:      "Omron",
			Extra: map[string]string{
				"node": fmt.Sprintf("%d", dev.Node),
			},
		})
	}

	logging.DebugLog("discovery", "discoverFINS: returning %d devices", len(results))
	return results, nil
}

// deduplicateDevices removes duplicate devices, preferring confirmed connections.
func deduplicateDevices(devices []DiscoveredDevice) []DiscoveredDevice {
	seen := make(map[string]int)
	var results []DiscoveredDevice

	for _, dev := range devices {
		key := dev.IP.String()
		if idx, ok := seen[key]; ok {
			existing := results[idx]
			if dev.Protocol == "EIP" && existing.Protocol != "EIP" {
				results[idx] = dev
			}
		} else {
			seen[key] = len(results)
			results = append(results, dev)
		}
	}

	return results
}

// GetLocalSubnets returns the CIDR notations for all local network interfaces.
func GetLocalSubnets() []string {
	var subnets []string

	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}

			if ipnet.IP.To4() == nil {
				continue
			}

			if ipnet.IP.IsLinkLocalUnicast() {
				continue
			}

			subnets = append(subnets, ipnet.String())
		}
	}

	return subnets
}

// GetBroadcastAddresses returns broadcast addresses for all local interfaces.
func GetBroadcastAddresses() []string {
	var broadcasts []string

	ifaces, err := net.Interfaces()
	if err != nil {
		return []string{"255.255.255.255"}
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagBroadcast == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}

			ip := ipnet.IP.To4()
			if ip == nil {
				continue
			}

			if ip.IsLinkLocalUnicast() {
				continue
			}

			broadcast := make(net.IP, len(ip))
			for i := range ip {
				broadcast[i] = ip[i] | ^ipnet.Mask[i]
			}
			broadcasts = append(broadcasts, broadcast.String())
		}
	}

	if len(broadcasts) == 0 {
		return []string{"255.255.255.255"}
	}

	return broadcasts
}
