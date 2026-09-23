package eip

import (
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

// ListIdentityUnicast sends a UDP ListIdentity request to each address on
// port 44818 from one socket and collects the replies until timeout (or until
// every address has answered). Unlike a broadcast, unicast requests cross
// routers, so this finds devices on routed subnets.
func ListIdentityUnicast(ips []net.IP, timeout time.Duration) ([]Identity, error) {
	return listIdentityUnicast(ips, 44818, timeout)
}

func listIdentityUnicast(ips []net.IP, port int, timeout time.Duration) ([]Identity, error) {
	uc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, fmt.Errorf("ListenUDP: %w", err)
	}
	defer uc.Close()
	_ = uc.SetReadBuffer(1 << 20)

	req := make([]byte, 24)
	binary.LittleEndian.PutUint16(req[0:2], 0x63) // ListIdentity, all other fields zero

	var targets []*net.UDPAddr
	pending := make(map[string]bool, len(ips))
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil && !pending[ip4.String()] {
			pending[ip4.String()] = true
			targets = append(targets, &net.UDPAddr{IP: ip4, Port: port})
		}
	}
	if len(targets) == 0 {
		return nil, nil
	}

	// Two passes within the timeout: routers commonly drop part of a burst
	// of packets to unresolved (empty) addresses while they ARP, so requests
	// are paced and non-responders are asked once more.
	deadline := time.Now().Add(timeout)
	var out []Identity
	buf := make([]byte, 4096)
	for pass := 0; pass < 2 && len(pending) > 0; pass++ {
		for _, t := range targets {
			if pending[t.IP.String()] {
				_, _ = uc.WriteToUDP(req, t) // unreachable destinations are skipped
				time.Sleep(time.Millisecond)
			}
		}
		passEnd := deadline
		if pass == 0 {
			passEnd = time.Now().Add(time.Until(deadline) / 2)
		}
		if err := uc.SetReadDeadline(passEnd); err != nil {
			return out, fmt.Errorf("SetReadDeadline: %w", err)
		}
		for len(pending) > 0 {
			n, src, err := uc.ReadFromUDP(buf)
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					break
				}
				return out, fmt.Errorf("ReadFromUDP: %w", err)
			}
			if n < 24 || binary.LittleEndian.Uint16(buf[0:2]) != 0x63 || binary.LittleEndian.Uint32(buf[8:12]) != 0 {
				continue
			}
			length := int(binary.LittleEndian.Uint16(buf[2:4]))
			key := src.IP.To4().String()
			if 24+length > n || !pending[key] {
				continue
			}
			idents, err := parseListIdentityPayloadToIdentities(buf[24:24+length], src.IP)
			if err != nil {
				continue
			}
			delete(pending, key)
			out = append(out, idents...)
		}
	}
	return out, nil
}
