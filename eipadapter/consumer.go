package eipadapter

import (
	"context"
	"encoding/binary"
	"net"
	"time"

	"github.com/yatesdr/plcio/eip"
	"github.com/yatesdr/plcio/logging"
)

// serveIOUDP handles inbound Class 1 cyclic I/O (O->T) on UDP port 2222.
// Standard I/O datagrams contain raw CPF, without an encapsulation header.
// Legacy encapsulated packets are accepted for compatibility.
func (a *Adapter) serveIOUDP(ctx context.Context) {
	defer a.wg.Done()
	buf := make([]byte, 1500)
	for {
		if ctx.Err() != nil {
			return
		}
		_ = a.udpIO.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, src, err := a.udpIO.ReadFromUDP(buf)
		if err != nil {
			if isClosedNetErr(err) || ctx.Err() != nil {
				return
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			logging.DebugError("eipadapter", "UDP IO read", err)
			continue
		}
		a.handleIOPacket(buf[:n], src)
	}
}

func (a *Adapter) handleIOPacket(data []byte, src *net.UDPAddr) {
	var cpfBytes []byte
	if len(data) >= int(eip.EncapHeaderLen) {
		f, err := eip.ParseFrame(data)
		if err == nil && f.Command == eip.SendUnitData {
			rr, err := eip.ParseRRData(f.Data)
			if err != nil {
				return
			}
			cpfBytes = rr
		}
	}
	if cpfBytes == nil {
		// Try as raw CPF
		cpfBytes = data
	}

	pkt, err := eip.ParseEipCommonPacket(cpfBytes)
	if err != nil {
		return
	}

	var connID uint32
	var payload []byte
	var hasDataSeq bool
	for _, it := range pkt.Items {
		switch it.TypeId {
		case 0x8002: // Sequenced Address Item
			if len(it.Data) >= 8 {
				connID = binary.LittleEndian.Uint32(it.Data[0:4])
				hasDataSeq = true
			}
		case 0x00A1: // Connected Address Item (Class 3)
			if len(it.Data) >= 4 {
				connID = binary.LittleEndian.Uint32(it.Data[0:4])
			}
		case 0x00B1: // Connected Data Item
			payload = it.Data
		}
	}
	if connID == 0 || payload == nil {
		return
	}

	c := a.connMgr.lookupByOT(connID)
	if c == nil {
		return
	}

	// Class 1 connected data has a 16-bit sequence count at the start. Some
	// scanners also prepend a 32-bit Run/Idle header (bit0=Run); we accept
	// either layout by checking the payload length against the consume
	// assembly size.
	var dataBytes []byte
	if hasDataSeq && len(payload) >= 2 {
		dataBytes = payload[2:]
	} else {
		dataBytes = payload
	}

	if c.Consume == nil {
		c.acceptPeer(src, a.cfg.Now())
		return
	}

	// If the payload begins with a 4-byte Run/Idle header (data is 4 bytes
	// longer than consume size), strip it. AB scanners include this when the
	// connection parameters request it.
	if len(dataBytes) == c.Consume.Size+4 {
		dataBytes = dataBytes[4:]
	}
	if len(dataBytes) != c.Consume.Size || !c.acceptPeer(src, a.cfg.Now()) {
		return
	}
	c.Consume.receiveFromScanner(dataBytes)
}

func (c *Connection) acceptPeer(src *net.UDPAddr, now time.Time) bool {
	v4 := src.IP.To4()
	if v4 == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	if c.peerAddr.port != 0 && !net.IP(c.peerAddr.ip[:]).Equal(v4) {
		return false
	}
	if c.peerAddr.port == 0 {
		copy(c.peerAddr.ip[:], v4)
		c.peerAddr.port = uint16(src.Port)
	}
	c.lastInboundAt = now
	return true
}
