package eipadapter

import (
	"encoding/binary"
	"net"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/yatesdr/plcio/logging"
)

// startConnection runs c's goroutine (watchdog + producer), tracked by the
// adapter's WaitGroup so Serve waits for it on shutdown. A panic inside it
// is recovered and logged, and only this connection is dropped.
func (a *Adapter) startConnection(c *Connection) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				logging.DebugLog("eipadapter", "recovered panic in connection 0x%08X: %v\n%s", c.OTConnID, r, debug.Stack())
				a.connMgr.remove(c, 0)
			}
		}()
		a.runProducer(c)
	}()
}

// runProducer is the per-connection goroutine. It enforces the O->T
// receive watchdog for every connection — from Forward_Open time, so a
// connection that never receives traffic still times out — and, when the
// connection has a produce assembly, pushes its current contents to the
// scanner at the negotiated T->O RPI. It exits when the connection is
// closed, the adapter stops, or the connection times out.
//
// Sent format per packet:
//
//	CPF item count = 2
//	  Sequenced Address Item (0x8002, len=8): T->O connID + 32-bit network seq
//	  Connected Data Item    (0xB1, len=2+N): 16-bit data seq + N data bytes
//
// We don't attempt multicast — the producer unicasts to the originator's
// address fixed at Forward_Open.
func (a *Adapter) runProducer(c *Connection) {
	timeout := connectionTimeout(c.OTRPI, c.TimeoutMultiplier)
	check := timeout / 4
	if check < time.Millisecond {
		check = time.Millisecond
	}
	watchdog := time.NewTicker(check)
	defer watchdog.Stop()

	var produce <-chan time.Time
	var rpi time.Duration
	if c.Produce != nil {
		rpi = time.Duration(c.TORPI) * time.Microsecond
		if rpi < minRPI*time.Microsecond {
			// Forward_Open rejects smaller RPIs; guard hand-built
			// connections (and NewTicker) anyway.
			rpi = minRPI * time.Microsecond
		}
		tick := time.NewTicker(rpi)
		defer tick.Stop()
		produce = tick.C
	}

	logging.DebugLog("eipadapter", "producer start conn 0x%08X RPI=%v timeout=%v", c.TOConnID, rpi, timeout)

	var dataSeq atomic.Uint32
	sendNow := c.Produce != nil
	for {
		c.mu.RLock()
		closed := c.closed
		last := c.lastInboundAt
		if last.IsZero() {
			last = c.createdAt
		}
		peer := c.peerAddr
		c.mu.RUnlock()
		if closed {
			return
		}

		// Bound the wait so a stalled scanner gets the connection torn
		// down even if we have no other signal.
		if a.cfg.Now().Sub(last) > timeout {
			logging.DebugLog("eipadapter", "producer conn 0x%08X timed out (no inbound)", c.TOConnID)
			a.connMgr.expire(c)
			return
		}

		// Without an originator address (only possible for hand-built
		// connections) there is nowhere to send.
		if sendNow && peer.port != 0 {
			seq := dataSeq.Add(1)
			packet := buildProducerPacket(c.TOConnID, c.nextSeq(), uint16(seq), c.Produce.Bytes())

			dst := &net.UDPAddr{IP: net.IPv4(peer.ip[0], peer.ip[1], peer.ip[2], peer.ip[3]), Port: int(peer.port)}
			_ = a.udpIO.SetWriteDeadline(time.Now().Add(rpi))
			if _, err := a.udpIO.WriteToUDP(packet, dst); err != nil {
				logging.DebugError("eipadapter", "producer write", err)
			}
		}
		sendNow = false

		select {
		case <-produce:
			sendNow = true
		case <-watchdog.C:
		case <-c.done:
			return
		case <-a.stopCh:
			return
		}
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

	return cpf
}

// connectionTimeout returns the maximum allowed gap between inbound O->T
// packets before we declare the connection dead. CIP defines this as
// RPI * 2^(multiplier+2), with multiplier in {0..7}.
func connectionTimeout(rpiUS uint32, mult byte) time.Duration {
	// Map multiplier index to multiplier per CIP Vol 1, table 3-5.4:
	// 0=4, 1=8, 2=16, 3=32, 4=64, 5=128, 6=256, 7=512.
	powers := []uint32{4, 8, 16, 32, 64, 128, 256, 512}
	if int(mult) >= len(powers) {
		mult = 7
	}
	return time.Duration(rpiUS) * time.Duration(powers[mult]) * time.Microsecond
}

// expire removes a connection whose watchdog fired. Only the map entries
// that still point at c are deleted, so a stale watchdog can't remove a
// newer connection that reuses the same IDs.
func (m *ConnectionManager) expire(c *Connection) {
	m.remove(c, ConnectionTimedOut)
}
