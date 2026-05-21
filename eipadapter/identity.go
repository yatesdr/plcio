package eipadapter

import (
	"encoding/binary"
	"net"

	"github.com/yatesdr/plcio/cip"
	"github.com/yatesdr/plcio/eip"
)

// Identity carries the values reported by the Identity Object (Class 0x01)
// and in ListIdentity responses. None of these have safety meaning; they
// identify the device to the scanner for routing and display purposes.
type Identity struct {
	VendorID     uint16
	DeviceType   uint16
	ProductCode  uint16
	RevMajor     byte
	RevMinor     byte
	Status       uint16
	SerialNumber uint32
	ProductName  string
	State        byte // 0x00=Nonexistent, 0x03=Operational, 0xFF=Default
	IP           net.IP
	Port         uint16 // 0xAF12 = 44818
}

// IdentityObject implements CIP Class 0x01 Instance 1.
type IdentityObject struct {
	info *Identity
}

func NewIdentityObject(info *Identity) *IdentityObject { return &IdentityObject{info: info} }

func (o *IdentityObject) Class() uint32    { return 0x01 }
func (o *IdentityObject) Instance() uint32 { return 1 }

const idGetAttributesAll byte = 0x01
const idGetAttributeSingle byte = 0x0E
const idReset byte = 0x05

func (o *IdentityObject) Handle(req *ObjectRequest) ObjectResponse {
	switch req.Service {
	case idGetAttributesAll:
		return ObjectResponse{Status: cip.StatusSuccess, Data: o.attributesAll()}
	case idGetAttributeSingle:
		data, ok := o.attribute(req.Path.Attribute)
		if !ok {
			return ObjectResponse{Status: cip.StatusAttrNotSupported}
		}
		return ObjectResponse{Status: cip.StatusSuccess, Data: data}
	case idReset:
		// Spec allows Reset; we treat as no-op success since the adapter
		// process can't safely self-restart.
		return ObjectResponse{Status: cip.StatusSuccess}
	default:
		return ObjectResponse{Status: cip.StatusServiceNotSupported}
	}
}

func (o *IdentityObject) attribute(id uint32) ([]byte, bool) {
	switch id {
	case 1:
		return le16(o.info.VendorID), true
	case 2:
		return le16(o.info.DeviceType), true
	case 3:
		return le16(o.info.ProductCode), true
	case 4:
		return []byte{o.info.RevMajor, o.info.RevMinor}, true
	case 5:
		return le16(o.info.Status), true
	case 6:
		return le32(o.info.SerialNumber), true
	case 7:
		return shortString(o.info.ProductName), true
	case 8:
		return []byte{o.info.State}, true
	}
	return nil, false
}

func (o *IdentityObject) attributesAll() []byte {
	out := make([]byte, 0, 64)
	out = append(out, le16(o.info.VendorID)...)
	out = append(out, le16(o.info.DeviceType)...)
	out = append(out, le16(o.info.ProductCode)...)
	out = append(out, o.info.RevMajor, o.info.RevMinor)
	out = append(out, le16(o.info.Status)...)
	out = append(out, le32(o.info.SerialNumber)...)
	out = append(out, shortString(o.info.ProductName)...)
	out = append(out, o.info.State)
	return out
}

// buildListIdentityReply builds the encap frame for a ListIdentity (0x63)
// response. Used for both UDP discovery and explicit TCP queries.
func buildListIdentityReply(req *eip.Frame, info *Identity) *eip.Frame {
	body := make([]byte, 0, 64)
	body = binary.LittleEndian.AppendUint16(body, 1) // encap protocol version

	// Socket address: family(BE) + port(BE) + addr(BE) + 8 zero bytes
	body = append(body, 0x00, 0x02) // AF_INET in big-endian
	body = binary.BigEndian.AppendUint16(body, info.Port)
	ip := net.IPv4zero.To4()
	if info.IP != nil {
		if v4 := info.IP.To4(); v4 != nil {
			ip = v4
		}
	}
	body = append(body, ip...)
	body = append(body, make([]byte, 8)...)

	body = binary.LittleEndian.AppendUint16(body, info.VendorID)
	body = binary.LittleEndian.AppendUint16(body, info.DeviceType)
	body = binary.LittleEndian.AppendUint16(body, info.ProductCode)
	body = append(body, info.RevMajor, info.RevMinor)
	body = binary.LittleEndian.AppendUint16(body, info.Status)
	body = binary.LittleEndian.AppendUint32(body, info.SerialNumber)
	body = append(body, shortString(info.ProductName)...)
	body = append(body, info.State)

	cpf := make([]byte, 0, 6+len(body))
	cpf = binary.LittleEndian.AppendUint16(cpf, 1) // item count
	cpf = binary.LittleEndian.AppendUint16(cpf, 0x000C)
	cpf = binary.LittleEndian.AppendUint16(cpf, uint16(len(body)))
	cpf = append(cpf, body...)

	return req.Reply(eip.EncapStatusSuccess, cpf)
}

func le16(v uint16) []byte { return binary.LittleEndian.AppendUint16(nil, v) }
func le32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

func shortString(s string) []byte {
	if len(s) > 32 {
		s = s[:32]
	}
	return append([]byte{byte(len(s))}, []byte(s)...)
}
