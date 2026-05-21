package eipadapter

import "github.com/yatesdr/plcio/cip"

// MessageRouterObject implements CIP Class 0x02 Instance 1. It is required by
// the spec for any device that processes explicit messages, but typically has
// no application logic beyond reporting attributes. Most scanners never query
// it; we include it for spec compliance.
type MessageRouterObject struct{}

func NewMessageRouterObject() *MessageRouterObject { return &MessageRouterObject{} }

func (o *MessageRouterObject) Class() uint32    { return 0x02 }
func (o *MessageRouterObject) Instance() uint32 { return 1 }

func (o *MessageRouterObject) Handle(req *ObjectRequest) ObjectResponse {
	// Attribute 1: object list. Attribute 2: max connections. Attribute 3:
	// active connections count. Attribute 4: active connection list.
	// We return modest defaults — this is fine for all common scanners.
	switch req.Service {
	case 0x0E: // Get_Attribute_Single
		switch req.Path.Attribute {
		case 1:
			// Object list: count(uint16) + class IDs (uint16 each). We omit
			// the optional list and just return count=0 (allowed).
			return ObjectResponse{Status: cip.StatusSuccess, Data: []byte{0x00, 0x00}}
		case 2:
			return ObjectResponse{Status: cip.StatusSuccess, Data: []byte{0x20, 0x00}} // 32 max
		case 3:
			return ObjectResponse{Status: cip.StatusSuccess, Data: []byte{0x00, 0x00}}
		default:
			return ObjectResponse{Status: cip.StatusAttrNotSupported}
		}
	default:
		return ObjectResponse{Status: cip.StatusServiceNotSupported}
	}
}
