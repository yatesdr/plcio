package eip

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// cpfItem builds a raw CPF item header + data for tests.
func cpfItem(typeID, length uint16, data []byte) []byte {
	b := binary.LittleEndian.AppendUint16(nil, typeID)
	b = binary.LittleEndian.AppendUint16(b, length)
	return append(b, data...)
}

// A declared item length of 0xFFFC..0xFFFF used to wrap in uint16 arithmetic
// (4+length) and panic with a slice-bounds error instead of returning an error.
func TestParseEipCommonPacketHugeItemLength(t *testing.T) {
	for _, l := range []uint16{0xFFFB, 0xFFFC, 0xFFFD, 0xFFFE, 0xFFFF} {
		raw := binary.LittleEndian.AppendUint16(nil, 1)
		raw = append(raw, cpfItem(CpfUnconnectedMessageId, l, []byte{1, 2, 3, 4, 5, 6})...)
		cp, err := ParseEipCommonPacket(raw)
		if err == nil {
			t.Fatalf("length 0x%04X: expected error, got %+v", l, cp)
		}
	}
}

func TestParseEipCommonPacketValid(t *testing.T) {
	cip := []byte{0xCC, 0x00, 0x00, 0x00, 0xC4, 0x00, 0x2A, 0x00, 0x00, 0x00}
	in := EipCommonPacket{Items: []EipCommonPacketItem{
		{TypeId: CpfAddressNullId, Length: 0},
		{TypeId: CpfUnconnectedMessageId, Length: uint16(len(cip)), Data: cip},
	}}
	cp, err := ParseEipCommonPacket(in.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(cp.Items) != 2 || cp.Items[0].TypeId != CpfAddressNullId || len(cp.Items[0].Data) != 0 ||
		cp.Items[1].TypeId != CpfUnconnectedMessageId || !bytes.Equal(cp.Items[1].Data, cip) {
		t.Fatalf("unexpected parse: %+v", cp)
	}
}

func FuzzParseEipCommonPacket(f *testing.F) {
	cip := []byte{0xCC, 0x00, 0x00, 0x00, 0xC4, 0x00, 0x2A, 0x00, 0x00, 0x00}
	unconnected := EipCommonPacket{Items: []EipCommonPacketItem{
		{TypeId: CpfAddressNullId, Length: 0},
		{TypeId: CpfUnconnectedMessageId, Length: uint16(len(cip)), Data: cip},
	}}
	connected := EipCommonPacket{Items: []EipCommonPacketItem{
		{TypeId: CpfAddressConnectionId, Length: 4, Data: []byte{1, 2, 3, 4}},
		{TypeId: CpfConnectedTransportPacketId, Length: uint16(2 + len(cip)), Data: append([]byte{0x01, 0x00}, cip...)},
	}}
	f.Add(unconnected.Bytes())
	f.Add(connected.Bytes())
	f.Add([]byte{0x00, 0x00})
	f.Add([]byte{0x01, 0x00, 0xB2, 0x00, 0xFF, 0xFF, 0x00})

	f.Fuzz(func(t *testing.T, raw []byte) {
		cp, err := ParseEipCommonPacket(raw)
		if err != nil {
			return
		}
		for i, it := range cp.Items {
			if int(it.Length) != len(it.Data) {
				t.Fatalf("item %d: Length %d but len(Data) %d", i, it.Length, len(it.Data))
			}
		}
	})
}
