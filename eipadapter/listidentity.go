package eipadapter

import (
	"context"
	"net"
	"time"

	"github.com/yatesdr/plcio/eip"
	"github.com/yatesdr/plcio/logging"
)

// serveDiscoverUDP handles UDP ListIdentity (0x63) broadcasts on port 44818.
// We also respond to NOP (0x00) silently.
func (a *Adapter) serveDiscoverUDP(ctx context.Context) {
	defer a.wg.Done()
	buf := make([]byte, 1500)
	for {
		if ctx.Err() != nil {
			return
		}
		_ = a.udpDiscover.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, src, err := a.udpDiscover.ReadFromUDP(buf)
		if err != nil {
			if isClosedNetErr(err) || ctx.Err() != nil {
				return
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			logging.DebugError("eipadapter", "UDP discover read", err)
			continue
		}
		a.handleDiscoverPacket(buf[:n], src)
	}
}

// handleDiscoverPacket answers one discovery datagram. A panic is logged
// and only that datagram is dropped.
func (a *Adapter) handleDiscoverPacket(data []byte, src *net.UDPAddr) {
	defer recoverPanic("UDP discover packet from " + src.String())
	if len(data) < int(eip.EncapHeaderLen) {
		return
	}
	f, err := eip.ParseFrame(data)
	if err != nil {
		return
	}
	if f.Command != uint16(0x63) { // not ListIdentity
		return
	}
	reply := buildListIdentityReply(f, &a.cfg.Identity)
	_ = a.udpDiscover.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
	if _, err := a.udpDiscover.WriteToUDP(reply.Bytes(), src); err != nil {
		logging.DebugError("eipadapter", "UDP discover write", err)
	}
}
