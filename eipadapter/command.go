package eipadapter

import (
	"encoding/binary"
	"net"

	"github.com/yatesdr/plcio/cip"
	"github.com/yatesdr/plcio/eip"
	"github.com/yatesdr/plcio/logging"
)

// handleSendRRData processes an unconnected explicit message.
//
// CPF expected: [Null Address (0x00)] + [Unconnected Data Item (0xB2)] +
// optionally sockaddr items the scanner sent. We respond with [Null Address]
// + [Unconnected Data Item] containing the CIP reply.
func (a *Adapter) handleSendRRData(conn net.Conn, f *eip.Frame) {
	cpfBytes, err := eip.ParseRRData(f.Data)
	if err != nil {
		a.sendFrame(conn, f.Reply(eip.EncapStatusInvalidData, nil))
		return
	}
	pkt, err := eip.ParseEipCommonPacket(cpfBytes)
	if err != nil {
		a.sendFrame(conn, f.Reply(eip.EncapStatusInvalidData, nil))
		return
	}

	var cipReq []byte
	for _, it := range pkt.Items {
		if it.TypeId == eip.CpfUnconnectedMessageId {
			cipReq = it.Data
			break
		}
	}
	if cipReq == nil {
		a.sendFrame(conn, f.Reply(eip.EncapStatusInvalidData, nil))
		return
	}

	cipResp := a.dispatch(cipReq, f.SessionHandle, 0)

	replyCPF := buildUnconnectedCPF(cipResp)
	a.sendFrame(conn, f.Reply(eip.EncapStatusSuccess, eip.BuildRRData(replyCPF)))
}

// handleSendUnitData processes a connected explicit message. The CPF should
// contain a Connected Address Item (0xA1) followed by a Connected Data Item
// (0xB1). The first two bytes of the connected data are a 16-bit sequence
// number which we echo in the reply.
func (a *Adapter) handleSendUnitData(conn net.Conn, f *eip.Frame) {
	cpfBytes, err := eip.ParseRRData(f.Data)
	if err != nil {
		return
	}
	pkt, err := eip.ParseEipCommonPacket(cpfBytes)
	if err != nil {
		return
	}
	var connID uint32
	var seq uint16
	var cipReq []byte
	for _, it := range pkt.Items {
		switch it.TypeId {
		case eip.CpfAddressConnectionId:
			if len(it.Data) >= 4 {
				connID = binary.LittleEndian.Uint32(it.Data[:4])
			}
		case eip.CpfConnectedTransportPacketId:
			if len(it.Data) >= 2 {
				seq = binary.LittleEndian.Uint16(it.Data[0:2])
				cipReq = it.Data[2:]
			}
		}
	}
	if cipReq == nil {
		return
	}
	if a.connMgr.lookupByOT(connID) == nil {
		logging.DebugLog("eipadapter", "SendUnitData on unknown conn 0x%08X", connID)
		return
	}

	cipResp := a.dispatch(cipReq, f.SessionHandle, connID)
	// Build connected reply: same connection ID for T->O direction in explicit
	// connected messaging is the originator's OTConnID echoed back.
	c := a.connMgr.lookupByOT(connID)
	replyCPF := buildConnectedCPF(c.TOConnID, seq, cipResp)
	a.sendFrame(conn, f.Reply(eip.EncapStatusSuccess, eip.BuildRRData(replyCPF)))
}

// dispatch decodes a CIP request and routes it to the correct object. The
// returned bytes are the full CIP response (including reply header).
func (a *Adapter) dispatch(req []byte, session, connID uint32) []byte {
	if len(req) < 2 {
		return errResponse(0, cip.StatusPathSegmentError)
	}
	svc := req[0]
	pathWords := int(req[1])
	pathBytes := pathWords * 2
	if len(req) < 2+pathBytes {
		return errResponse(svc, cip.StatusPathSegmentError)
	}
	path := req[2 : 2+pathBytes]
	data := req[2+pathBytes:]

	parsed, err := cip.ParsePath(path)
	if err != nil {
		return errResponse(svc, cip.StatusPathSegmentError)
	}
	if !parsed.HasClass {
		return errResponse(svc, cip.StatusPathSegmentError)
	}
	// Default to instance 1 when omitted (common for class-level services).
	instance := uint32(1)
	if parsed.HasInstance {
		instance = parsed.Instance
	}

	obj := a.registry.Lookup(parsed.Class, instance)
	if obj == nil {
		if a.registry.classExists(parsed.Class) {
			return errResponse(svc, cip.StatusPathDestUnknown)
		}
		return errResponse(svc, cip.StatusPathDestUnknown)
	}

	resp := obj.Handle(&ObjectRequest{
		Service: svc,
		Path:    parsed,
		Data:    data,
		Session: session,
		ConnID:  connID,
	})
	if resp.Status == cip.StatusSuccess {
		return okResponse(svc, resp.Data)
	}
	return errResponse(svc, resp.Status, resp.ExtData...)
}

// buildUnconnectedCPF wraps a CIP response into a CPF for SendRRData reply.
func buildUnconnectedCPF(cipResp []byte) []byte {
	cpf := eip.EipCommonPacket{
		Items: []eip.EipCommonPacketItem{
			{TypeId: eip.CpfAddressNullId, Length: 0},
			{TypeId: eip.CpfUnconnectedMessageId, Length: uint16(len(cipResp)), Data: cipResp},
		},
	}
	return cpf.Bytes()
}

// buildConnectedCPF wraps a CIP response for connected explicit messaging.
func buildConnectedCPF(connID uint32, seq uint16, cipResp []byte) []byte {
	addrData := binary.LittleEndian.AppendUint32(nil, connID)
	body := make([]byte, 0, 2+len(cipResp))
	body = binary.LittleEndian.AppendUint16(body, seq)
	body = append(body, cipResp...)

	cpf := eip.EipCommonPacket{
		Items: []eip.EipCommonPacketItem{
			{TypeId: eip.CpfAddressConnectionId, Length: 4, Data: addrData},
			{TypeId: eip.CpfConnectedTransportPacketId, Length: uint16(len(body)), Data: body},
		},
	}
	return cpf.Bytes()
}
