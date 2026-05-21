package eipadapter

import (
	"encoding/binary"
	"net"
	"sync/atomic"
	"time"

	"github.com/yatesdr/plcio/eip"
	"github.com/yatesdr/plcio/logging"
)

// runProducer pushes the produce assembly's current contents to the scanner
// at the negotiated T->O RPI. It exits when the connection is closed or the
// timeout multiplier expires without any inbound O->T traffic.
//
// Sent format per packet:
//
//	Encap: command=0x70 SendUnitData, session=0
//	RRData prefix (4 + 2)
//	CPF item count = 2
//	  Sequenced Address Item (0x8002, len=8): T->O connID + 32-bit network seq
//	  Connected Data Item    (0xB1, len=2+N): 16-bit data seq + N data bytes
//
// We don't attempt multicast — the producer unicasts to the peer's address
// learned from the first inbound packet OR from sockaddr items in the
// Forward_Open response (TODO: parse sockaddr_info hints from FO).
func (a *Adapter) runProducer(c *Connection) {
	if c.Produce == nil {
		return
	}

	rpi := time.Duration(c.TORPI) * time.Microsecond
	if rpi < time.Millisecond {
		rpi = 10 * time.Millisecond
	}
	timeout := connectionTimeout(c.TORPI, c.TimeoutMultiplier)

	logging.DebugLog("eipadapter", "producer start conn 0x%08X RPI=%v timeout=%v", c.TOConnID, rpi, timeout)

	tick := time.NewTicker(rpi)
	defer tick.Stop()

	var dataSeq atomic.Uint32
	for {
		c.mu.RLock()
		closed := c.closed
		last := c.lastInboundAt
		peer := c.peerAddr
		c.mu.RUnlock()
		if closed {
			return
		}

		// Bound the wait so a stalled scanner gets the connection torn
		// down even if we have no other signal.
		if !last.IsZero() && time.Since(last) > timeout {
			logging.DebugLog("eipadapter", "producer conn 0x%08X timed out (no inbound)", c.TOConnID)
			a.connMgr.expire(c)
			return
		}

		// If we don't yet have a peer address (no inbound, no hint), skip
		// this tick. Standard scanners send their first O->T packet very
		// shortly after Forward_Open.
		if peer.port == 0 {
			<-tick.C
			continue
		}

		seq := dataSeq.Add(1)
		packet := buildProducerPacket(c.TOConnID, c.nextSeq(), uint16(seq), c.Produce.Bytes())

		dst := &net.UDPAddr{IP: net.IPv4(peer.ip[0], peer.ip[1], peer.ip[2], peer.ip[3]), Port: int(peer.port)}
		_ = a.udpIO.SetWriteDeadline(time.Now().Add(rpi))
		if _, err := a.udpIO.WriteToUDP(packet, dst); err != nil {
			logging.DebugError("eipadapter", "producer write", err)
		}

		<-tick.C
	}
}

func buildProducerPacket(toConnID, netSeq uint32, dataSeq uint16, data []byte) []byte {
	// Sequenced Address Item
	addr := make([]byte, 0, 12)
	addr = binary.LittleEndian.AppendUint16(addr, 0x8002)
	addr = binary.LittleEndian.AppendUint16(addr, 8)
	addr = binary.LittleEndian.AppendUint32(addr, toConnID)
	addr = binary.LittleEndian.AppendUint32(addr, netSeq)

	// Connected Data Item
	dataItem := make([]byte, 0, 4+2+len(data))
	dataItem = binary.LittleEndian.AppendUint16(dataItem, 0x00B1)
	dataItem = binary.LittleEndian.AppendUint16(dataItem, uint16(2+len(data)))
	dataItem = binary.LittleEndian.AppendUint16(dataItem, dataSeq)
	dataItem = append(dataItem, data...)

	// CPF
	cpf := make([]byte, 0, 2+len(addr)+len(dataItem))
	cpf = binary.LittleEndian.AppendUint16(cpf, 2)
	cpf = append(cpf, addr...)
	cpf = append(cpf, dataItem...)

	rrdata := eip.BuildRRData(cpf)

	f := &eip.Frame{
		Command: eip.SendUnitData,
		Data:    rrdata,
	}
	return f.Bytes()
}

// connectionTimeout returns the maximum allowed gap between inbound O->T
// packets before we declare the connection dead. CIP defines this as
// RPI * 2^multiplier (multiplier in {0..7}, encoded as the index into the
// powers-of-two list).
func connectionTimeout(rpiUS uint32, mult byte) time.Duration {
	// Map multiplier index to multiplier per CIP Vol 1, table 3-5.4:
	// 0=4, 1=8, 2=16, 3=32, 4=64, 5=128, 6=256, 7=512.
	powers := []uint32{4, 8, 16, 32, 64, 128, 256, 512}
	if int(mult) >= len(powers) {
		mult = 7
	}
	return time.Duration(rpiUS*powers[mult]) * time.Microsecond
}

func (m *ConnectionManager) expire(c *Connection) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	delete(m.byOT, c.OTConnID)
	delete(m.byTO, c.TOConnID)
}
