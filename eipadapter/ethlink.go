package eipadapter

import "github.com/yatesdr/plcio/cip"

// EthernetLinkObject implements CIP Class 0xF6 Instance 1. We report a
// minimal, plausible link configuration. Real values would require pulling
// from the OS netlink — out of scope for this adapter, and almost no scanner
// makes decisions based on them.
type EthernetLinkObject struct{}

func NewEthernetLinkObject() *EthernetLinkObject { return &EthernetLinkObject{} }

func (o *EthernetLinkObject) Class() uint32    { return 0xF6 }
func (o *EthernetLinkObject) Instance() uint32 { return 1 }

func (o *EthernetLinkObject) Handle(req *ObjectRequest) ObjectResponse {
	if req.Service != 0x0E {
		return ObjectResponse{Status: cip.StatusServiceNotSupported}
	}
	switch req.Path.Attribute {
	case 1: // Interface Speed (UDINT, Mbps)
		return ObjectResponse{Status: cip.StatusSuccess, Data: le32(1000)}
	case 2: // Interface Flags (UDINT)
		// bit0=link up, bit1=full duplex, bit2=negotiation status (3=success)
		return ObjectResponse{Status: cip.StatusSuccess, Data: le32(0x0F)}
	case 3: // Physical Address (6 bytes) — not derived; report zeros.
		return ObjectResponse{Status: cip.StatusSuccess, Data: make([]byte, 6)}
	default:
		return ObjectResponse{Status: cip.StatusAttrNotSupported}
	}
}
