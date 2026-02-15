package eip

// Code related to the CommonPacket Format for EIP per ODVA v1.4

import (
	"encoding/binary"
	"fmt"
)

const (
	CpfAddressNullId              uint16 = 0x00
	CpfTypeListIdentityResponseId uint16 = 0x0C
	CpfAddressConnectionId        uint16 = 0xA1
	CpfConnectedTransportPacketId uint16 = 0xB1
	CpfUnconnectedMessageId       uint16 = 0xB2
	CpfListServicesResponseId     uint16 = 0x100
	CpfSockAddrInfoOtoTId         uint16 = 0x8000
	CpfSockAddrInfoTtoOId         uint16 = 0x8001
	CpfSequencedAddressId         uint16 = 0x8002
)

// Cpf consists of a wrapper for data items.
type EipCommonPacket struct {
	Items []EipCommonPacketItem
}

// Common Packet Item format used for Data and Address items.
type EipCommonPacketItem struct {
	TypeId uint16
	Length uint16
	Data   []byte
}

type EipCpfNullAddressItem struct {
	TypeId uint16
	Length uint16
}

type EipCpfConnectedAddressItem struct {
	TypeId               uint16
	Length               uint16
	ConnectionIdentifier uint32
}

type EipCpfSequencedAddressItem struct {
	TypeId               uint16
	Length               uint16
	ConnectionIdentifier uint32
	SequenceNumber       uint32
}

type EipCpfUnconnectedDataItem struct {
	TypeId uint16
	Length uint16
	Data   []byte
}

type EipCpfConnectedDataItem struct {
	TypeId uint16
	Length uint16
	Data   []byte
}

type EipCpfSockaddrInfoItem struct {
	TypeId    uint16
	Length    uint16
	SinFamily int16
	SinPort   uint16
	SinAddr   uint32
	SinZero   [8]byte
}

// Generate a Little-Endian Encoded byte representation of the CommonPacket.
func (p *EipCommonPacket) Bytes() []byte {
	raw := binary.LittleEndian.AppendUint16(nil, uint16(len(p.Items)))
	for _, value := range p.Items {
		raw = append(raw, value.Bytes()...)
	}
	return raw
}

// Generate a Little-Endian encoded byte representation of the CommonPacketItem.
func (item *EipCommonPacketItem) Bytes() []byte {
	raw := binary.LittleEndian.AppendUint16(nil, item.TypeId)
	raw = binary.LittleEndian.AppendUint16(raw, item.Length)
	raw = append(raw, item.Data...)
	return raw
}

// Parses and returns a list of CommonPacketItems from a raw byte stream.
func ParseEipCommonPacket(raw []byte) (*EipCommonPacket, error) {

	if len(raw) < 2 {
		return nil, fmt.Errorf("ParseEipCommonPacket:  Raw bytes too short: Minimum 8, got %d", len(raw))
	}

	// Get the number of items and advance the slice.
	item_count := binary.LittleEndian.Uint16(raw[:2])
	raw = raw[2:]

	if (item_count > 0) && len(raw) == 0 {
		return nil, fmt.Errorf("ParseEipCommonPacket: Item count is nonzero but no bytes remain.")
	}

	// Parse the items
	var cp_items []EipCommonPacketItem

	var i uint16 = 0
	for i = 0; i < item_count; i += 1 {

		if len(raw) < 4 {
			return nil, fmt.Errorf("ParseEipCommonPacket: truncated item header at item %d: have %d bytes", i, len(raw))
		}

		type_id := binary.LittleEndian.Uint16(raw[:2])
		length := binary.LittleEndian.Uint16(raw[2:4])

		need := int(4 + length)
		if len(raw) < need {
			return nil, fmt.Errorf("ParseEipCommonPacket: insufficient data for item %d: need %d bytes, have %d", i, need, len(raw))
		}

		data := raw[4 : 4+length]
		cp_items = append(cp_items, EipCommonPacketItem{TypeId: type_id, Length: length, Data: data})

		// advance
		raw = raw[4+length:]
	}

	return &EipCommonPacket{Items: cp_items}, nil
}

