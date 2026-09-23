package eipadapter

import (
	"encoding/binary"
	"net"
	"os"

	"github.com/yatesdr/plcio/cip"
)

// TCPIPInterfaceObject implements CIP Class 0xF5 Instance 1. Returns the
// device's IP configuration. Many scanners query attribute 5 (Interface
// Configuration) to verify the device is properly addressed.
type TCPIPInterfaceObject struct {
	// Interface values, captured once by setInterface (Adapter.New). A
	// zero-value object falls back to looking them up per request.
	configured bool
	ip         net.IP
	mask       net.IPMask
	hostname   string
}

func NewTCPIPInterfaceObject() *TCPIPInterfaceObject { return &TCPIPInterfaceObject{} }

// setInterface caches the advertised IP, its interface's netmask and the
// host name, so requests never block on the network (e.g. reverse DNS).
func (o *TCPIPInterfaceObject) setInterface(ip net.IP) {
	o.ip = ip.To4()
	o.mask = interfaceMask(o.ip)
	o.hostname, _ = os.Hostname()
	o.configured = true
}

// interfaceMask returns the netmask of the local interface that holds ip,
// or nil if none does (e.g. 0.0.0.0).
func interfaceMask(ip net.IP) net.IPMask {
	v4 := ip.To4()
	if v4 == nil || v4.IsUnspecified() {
		return nil
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(v4) {
			if m := n.Mask; len(m) == net.IPv6len {
				return m[12:]
			} else if len(m) == net.IPv4len {
				return m
			}
		}
	}
	return nil
}

func (o *TCPIPInterfaceObject) Class() uint32    { return 0xF5 }
func (o *TCPIPInterfaceObject) Instance() uint32 { return 1 }

func (o *TCPIPInterfaceObject) Handle(req *ObjectRequest) ObjectResponse {
	if req.Service != 0x0E {
		return ObjectResponse{Status: cip.StatusServiceNotSupported}
	}
	switch req.Path.Attribute {
	case 1: // Status
		return ObjectResponse{Status: cip.StatusSuccess, Data: le32(1)}
	case 2: // Configuration Capability
		// No capability bits (CIP Vol 2, 5-4.3.2.2): the host OS owns the
		// IP configuration, so the adapter is neither a BOOTP/DHCP/DNS
		// client nor settable over CIP. (Earlier releases reported 0x44,
		// "DHCP client" + "change requires reset", which was untrue.)
		return ObjectResponse{Status: cip.StatusSuccess, Data: le32(0)}
	case 3: // Configuration Control
		return ObjectResponse{Status: cip.StatusSuccess, Data: le32(0)} // statically configured
	case 4: // Physical Link Object — path to Ethernet Link object
		path := []byte{0x20, 0xF6, 0x24, 0x01}
		out := append([]byte{byte(len(path) / 2)}, 0x00)
		out = append(out, path...)
		return ObjectResponse{Status: cip.StatusSuccess, Data: out}
	case 5: // Interface Configuration
		ip, mask := o.ip, o.mask
		if !o.configured {
			ip = preferredLocalIPv4()
			mask = interfaceMask(ip)
		}
		return ObjectResponse{Status: cip.StatusSuccess, Data: ifaceConfig(ip, mask)}
	case 6: // Host Name (STRING, max 64 characters)
		host := o.hostname
		if !o.configured {
			host, _ = os.Hostname()
		}
		return ObjectResponse{Status: cip.StatusSuccess, Data: cipString(host, maxHostNameLen)}
	default:
		return ObjectResponse{Status: cip.StatusAttrNotSupported}
	}
}

// Maximum lengths of the TCP/IP Interface object's STRING attributes (CIP
// Vol 2, Table 5-4.3: domain name 48, host name 64 characters).
const (
	maxDomainNameLen = 48
	maxHostNameLen   = 64
)

// ifaceConfig serialises the Interface Configuration attribute (CIP Vol 2,
// 5-4.3.2.5): IP address, network mask, gateway, name server, name server 2
// (each a UDINT, so little-endian: 192.168.1.10 is sent as 0A 01 A8 C0, as
// OpENer's EncodeCipTcpIpInterfaceConfiguration and Wireshark's ENIP
// dissector do), then the domain name as a STRING. The mask is the real mask
// of the advertised IP's interface, or 0.0.0.0 when unknown.
func ifaceConfig(ip net.IP, mask net.IPMask) []byte {
	out := make([]byte, 0, 22)
	out = appendIPv4UDINT(out, ip)
	out = appendIPv4UDINT(out, net.IP(mask))
	out = append(out, 0, 0, 0, 0) // gateway
	out = append(out, 0, 0, 0, 0) // name server
	out = append(out, 0, 0, 0, 0) // name server 2
	return append(out, cipString("", maxDomainNameLen)...)
}

// appendIPv4UDINT appends an IPv4 address (or mask) as a CIP UDINT
// (little-endian); anything that is not IPv4 is written as 0.0.0.0.
func appendIPv4UDINT(out []byte, ip net.IP) []byte {
	var v uint32
	if v4 := ip.To4(); v4 != nil {
		v = binary.BigEndian.Uint32(v4)
	}
	return binary.LittleEndian.AppendUint32(out, v)
}

// cipString encodes s as a CIP STRING: UINT character count, the
// characters, and a pad byte if the count is odd (the pad is not counted),
// as the TCP/IP Interface object requires (CIP Vol 2, 5-4.3.2.5/6). s is
// truncated to max characters.
func cipString(s string, max int) []byte {
	if len(s) > max {
		s = s[:max]
	}
	out := binary.LittleEndian.AppendUint16(make([]byte, 0, 3+len(s)), uint16(len(s)))
	out = append(out, s...)
	if len(s)%2 != 0 {
		out = append(out, 0)
	}
	return out
}
