package eipadapter

import (
	"encoding/binary"
	"net"

	"github.com/yatesdr/plcio/cip"
)

// TCPIPInterfaceObject implements CIP Class 0xF5 Instance 1. Returns the
// device's IP configuration. Many scanners query attribute 5 (Interface
// Configuration) to verify the device is properly addressed.
type TCPIPInterfaceObject struct{}

func NewTCPIPInterfaceObject() *TCPIPInterfaceObject { return &TCPIPInterfaceObject{} }

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
		return ObjectResponse{Status: cip.StatusSuccess, Data: le32(0x44)} // DHCP + DNS-supported
	case 3: // Configuration Control
		return ObjectResponse{Status: cip.StatusSuccess, Data: le32(0)} // statically configured
	case 4: // Physical Link Object — path to Ethernet Link object
		path := []byte{0x20, 0xF6, 0x24, 0x01}
		out := append([]byte{byte(len(path) / 2)}, 0x00)
		out = append(out, path...)
		return ObjectResponse{Status: cip.StatusSuccess, Data: out}
	case 5: // Interface Configuration
		ip := preferredLocalIPv4()
		return ObjectResponse{Status: cip.StatusSuccess, Data: ifaceConfig(ip)}
	case 6: // Host Name (SHORT_STRING)
		name, _ := net.LookupAddr(preferredLocalIPv4().String())
		host := ""
		if len(name) > 0 {
			host = name[0]
		}
		return ObjectResponse{Status: cip.StatusSuccess, Data: shortString(host)}
	default:
		return ObjectResponse{Status: cip.StatusAttrNotSupported}
	}
}

// ifaceConfig serialises the Interface Configuration attribute:
// ip, subnet, gateway, name_server, name_server2, domain_name (SHORT_STRING).
func ifaceConfig(ip net.IP) []byte {
	out := make([]byte, 0, 24)
	v4 := ip.To4()
	if v4 == nil {
		v4 = net.IPv4zero.To4()
	}
	out = append(out, v4...)
	out = binary.LittleEndian.AppendUint32(out, 0xFFFFFF00) // /24
	out = append(out, 0, 0, 0, 0)                            // gateway
	out = append(out, 0, 0, 0, 0)                            // ns1
	out = append(out, 0, 0, 0, 0)                            // ns2
	out = append(out, 0x00)                                  // domain length
	return out
}
