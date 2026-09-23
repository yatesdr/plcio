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
		a.handleIOPacketSafe(buf[:n], src)
	}
}

// handleIOPacketSafe processes one datagram; a panic is logged and only
// that datagram is dropped, so the I/O loop keeps serving other connections.
func (a *Adapter) handleIOPacketSafe(data []byte, src *net.UDPAddr) {
	defer recoverPanic("UDP I/O packet from " + src.String())
	a.handleIOPacket(data, src)
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

	var connID, netSeq uint32
	var payload []byte
	var hasDataSeq bool
	for _, it := range pkt.Items {
		switch it.TypeId {
		case 0x8002: // Sequenced Address Item
			if len(it.Data) >= 8 {
				connID = binary.LittleEndian.Uint32(it.Data[0:4])
				netSeq = binary.LittleEndian.Uint32(it.Data[4:8])
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
	// scanners also prepend a 32-bit Run/Idle header (bit0=Run); the
	// Forward_Open connection size tells us which (c.otFormat), otherwise
	// we detect it by checking the payload length against the consume
	// assembly size.
	var dataBytes []byte
	if hasDataSeq && len(payload) >= 2 {
		dataBytes = payload[2:]
	} else {
		dataBytes = payload
	}

	if c.Consume == nil {
		c.acceptPeer(src, a.cfg.Now(), netSeq, hasDataSeq)
		return
	}

	var header []byte
	switch {
	case c.otFormat == otFormatRunIdle && len(dataBytes) >= 4:
		header, dataBytes = dataBytes[:4], dataBytes[4:]
		if len(dataBytes) == 0 && header[0]&1 == 0 {
			// Idle packets may carry the header only: heartbeat, no data.
			dataBytes = nil
		}
	case c.otFormat == otFormatAuto && len(dataBytes) == c.Consume.Size+4:
		// A 4-byte Run/Idle header precedes the data (data is 4 bytes
		// longer than consume size). AB scanners include this when the
		// connection parameters request it.
		header, dataBytes = dataBytes[:4], dataBytes[4:]
	}
	if (dataBytes != nil && len(dataBytes) != c.Consume.Size) || (c.otFormat == otFormatRunIdle && header == nil) {
		return
	}
	if !c.acceptPeer(src, a.cfg.Now(), netSeq, hasDataSeq) {
		return
	}
	if header != nil {
		a.noteRunIdle(c, header[0]&1 != 0)
	}
	if dataBytes != nil {
		c.Consume.receiveFromScanner(dataBytes, c)
	}
}

// acceptPeer validates an O->T packet's source and sequence number and, if
// acceptable, feeds the connection watchdog. Packets are accepted only from
// the IP of the originator that opened the connection (a connection with no
// known originator accepts nothing), and only if their 32-bit sequence
// number is newer than the last accepted one (serial-number arithmetic, so
// wraparound is handled); duplicates and stale reordered packets are
// dropped.
func (c *Connection) acceptPeer(src *net.UDPAddr, now time.Time, seq uint32, hasSeq bool) bool {
	v4 := src.IP.To4()
	if v4 == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	if c.peerAddr.port == 0 || !net.IP(c.peerAddr.ip[:]).Equal(v4) {
		return false
	}
	if hasSeq {
		if c.seqValid && int32(seq-c.lastSeq) <= 0 {
			return false
		}
		c.lastSeq, c.seqValid = seq, true
	}
	c.lastInboundAt = now
	return true
}

// noteRunIdle records the O->T Run/Idle state and reports transitions.
func (a *Adapter) noteRunIdle(c *Connection, run bool) {
	c.mu.Lock()
	changed := !c.runKnown || c.run != run
	c.run, c.runKnown = run, true
	c.mu.Unlock()
	c.Consume.setRunIdle(c, run)
	if changed {
		ev := ConnectionIdle
		if run {
			ev = ConnectionRun
		}
		a.emitConnEvent(c, ev)
	}
}
