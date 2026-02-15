package logix

import (
	"fmt"
	"net"
	"time"

	"plcio/eip"
)

// DeviceInfo contains identity information about a discovered PLC or EtherNet/IP device.
type DeviceInfo struct {
	IP          net.IP // Device IP address
	Port        uint16 // EtherNet/IP port (usually 44818)
	VendorID    uint16 // Vendor ID (1 = Rockwell Automation)
	DeviceType  uint16 // Device type code
	ProductCode uint16 // Product code
	Revision    string // Firmware revision (e.g., "32.11")
	Serial      uint32 // Serial number
	ProductName string // Product name (e.g., "1756-L83E/B")
	Status      uint16 // Device status word
}

// VendorName returns a human-readable vendor name for common vendors.
func (d *DeviceInfo) VendorName() string {
	switch d.VendorID {
	case 1:
		return "Rockwell Automation"
	case 2:
		return "Schneider Electric"
	case 5:
		return "Omron"
	case 26:
		return "Turck"
	case 40:
		return "Molex"
	case 50:
		return "SICK"
	case 88:
		return "Cognex"
	default:
		return fmt.Sprintf("Vendor %d", d.VendorID)
	}
}

// DeviceTypeName returns a human-readable device type name.
func (d *DeviceInfo) DeviceTypeName() string {
	switch d.DeviceType {
	case 0x00:
		return "Generic Device"
	case 0x02:
		return "AC Drive"
	case 0x03:
		return "Motor Overload"
	case 0x04:
		return "Limit Switch"
	case 0x05:
		return "Inductive Proximity Switch"
	case 0x06:
		return "Photoelectric Sensor"
	case 0x07:
		return "General Purpose Discrete I/O"
	case 0x0C:
		return "Communications Adapter"
	case 0x0E:
		return "Programmable Logic Controller"
	case 0x10:
		return "Position Controller"
	case 0x13:
		return "DC Drive"
	case 0x15:
		return "Contactor"
	case 0x1B:
		return "Mass Flow Controller"
	case 0x1D:
		return "Pneumatic Valve"
	case 0x21:
		return "Residual Gas Analyzer"
	case 0x22:
		return "DC Power Generator"
	case 0x23:
		return "RF Power Generator"
	case 0x24:
		return "Turbomolecular Vacuum Pump"
	case 0x25:
		return "Encoder"
	case 0x26:
		return "Safety Discrete I/O Device"
	case 0x28:
		return "Fluid Flow Controller"
	case 0x29:
		return "CIP Motion Drive"
	case 0x2A:
		return "CompoNet Repeater"
	case 0x2B:
		return "Mass Flow Controller Enhanced"
	case 0x2C:
		return "CIP Modbus Device"
	case 0x2D:
		return "CIP Modbus Translator"
	case 0x2E:
		return "Safety Analog I/O Device"
	case 0x2F:
		return "Generic Device (keyable)"
	case 0x30:
		return "Managed Ethernet Switch"
	case 0x31:
		return "CIP Motion Safety Drive"
	case 0x32:
		return "Safety Drive"
	case 0x33:
		return "CIP Motion Encoder"
	case 0x34:
		return "CIP Motion Converter"
	case 0x35:
		return "CIP Motion I/O"
	default:
		return fmt.Sprintf("Device Type 0x%02X", d.DeviceType)
	}
}

// String returns a human-readable summary of the device.
func (d *DeviceInfo) String() string {
	return fmt.Sprintf("%s (%s) at %s - %s v%s [SN: %d]",
		d.ProductName, d.DeviceTypeName(), d.IP, d.VendorName(), d.Revision, d.Serial)
}

// Discover broadcasts a ListIdentity request on the local network to find
// all EtherNet/IP devices (PLCs, drives, I/O modules, etc.) on the subnet.
//
// broadcastAddr can be:
//   - "255.255.255.255" for global broadcast (may be blocked by routers/switches)
//   - A directed broadcast like "192.168.1.255" for a specific subnet
//   - Empty string "" to use "255.255.255.255" as default
//
// timeout specifies how long to wait for responses (e.g., 1*time.Second).
// Longer timeouts may find more devices on busy networks.
//
// Returns a slice of discovered devices. Devices behind switches/routers
// that don't forward broadcasts won't be found - use GetIdentity() for those.
func Discover(broadcastAddr string, timeout time.Duration) ([]DeviceInfo, error) {
	if broadcastAddr == "" {
		broadcastAddr = "255.255.255.255"
	}
	if timeout <= 0 {
		timeout = 1 * time.Second
	}

	// Create a temporary EIP client just for discovery
	// (ListIdentityUDP doesn't need a TCP connection)
	client := eip.NewEipClient("")

	identities, err := client.ListIdentityUDP(broadcastAddr, timeout)
	if err != nil {
		return nil, fmt.Errorf("Discover: %w", err)
	}

	devices := make([]DeviceInfo, len(identities))
	for i, id := range identities {
		devices[i] = identityToDeviceInfo(id)
	}

	return devices, nil
}

// DiscoverSubnet discovers devices on a specific subnet by calculating
// the broadcast address from the given CIDR notation (e.g., "192.168.1.0/24").
func DiscoverSubnet(cidr string, timeout time.Duration) ([]DeviceInfo, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("DiscoverSubnet: invalid CIDR %q: %w", cidr, err)
	}

	// Calculate broadcast address
	broadcast := make(net.IP, len(ipnet.IP))
	for i := range ipnet.IP {
		broadcast[i] = ipnet.IP[i] | ^ipnet.Mask[i]
	}

	return Discover(broadcast.String(), timeout)
}

// GetIdentity queries a specific IP address for its device identity over TCP.
// This works even when UDP broadcast doesn't reach the device (e.g., across
// subnets, through firewalls, or behind switches that don't forward broadcasts).
//
// Use this to verify if a specific IP address is an EtherNet/IP device.
func GetIdentity(ipAddr string) (*DeviceInfo, error) {
	if ipAddr == "" {
		return nil, fmt.Errorf("GetIdentity: empty IP address")
	}

	// Create EIP client and connect
	client := eip.NewEipClient(ipAddr)
	err := client.Connect()
	if err != nil {
		return nil, fmt.Errorf("GetIdentity: connection failed: %w", err)
	}
	defer client.Disconnect()

	// Query identity
	identities, err := client.ListIdentityTCP()
	if err != nil {
		return nil, fmt.Errorf("GetIdentity: %w", err)
	}

	if len(identities) == 0 {
		return nil, fmt.Errorf("GetIdentity: no identity returned")
	}

	// Use the first identity (typically only one)
	device := identityToDeviceInfo(identities[0])

	// If the identity didn't include an IP (common for TCP), use the address we connected to
	if device.IP == nil || device.IP.Equal(net.IPv4zero) {
		device.IP = net.ParseIP(ipAddr)
	}

	return &device, nil
}

// Identity returns the device identity of the connected PLC.
// Returns nil if not connected or identity query fails.
func (c *Client) Identity() (*DeviceInfo, error) {
	if c == nil || c.plc == nil || c.plc.Connection == nil {
		return nil, fmt.Errorf("Identity: not connected")
	}

	identities, err := c.plc.Connection.ListIdentityTCP()
	if err != nil {
		return nil, fmt.Errorf("Identity: %w", err)
	}

	if len(identities) == 0 {
		return nil, fmt.Errorf("Identity: no identity returned")
	}

	device := identityToDeviceInfo(identities[0])

	// Fill in IP from connection if not in identity
	if device.IP == nil || device.IP.Equal(net.IPv4zero) {
		device.IP = net.ParseIP(c.plc.IpAddress)
	}

	return &device, nil
}

// identityToDeviceInfo converts an eip.Identity to a DeviceInfo.
func identityToDeviceInfo(id eip.Identity) DeviceInfo {
	return DeviceInfo{
		IP:          id.IP,
		Port:        id.Port,
		VendorID:    id.VendorID,
		DeviceType:  id.DeviceType,
		ProductCode: id.ProductCode,
		Revision:    fmt.Sprintf("%d.%d", id.RevisionMajor, id.RevisionMinor),
		Serial:      id.SerialNumber,
		ProductName: id.ProductName,
		Status:      id.Status,
	}
}
