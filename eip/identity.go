package eip

import (
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

// Implement the ListIdentity operation.

// Identity is the parsed ListIdentity identity item.
type Identity struct {
	EncapsulationVersion uint16
	VendorID             uint16
	DeviceType           uint16
	ProductCode          uint16
	RevisionMajor        byte
	RevisionMinor        byte
	Status               uint16
	SerialNumber         uint32
	ProductName          string
	State                byte

	IP   net.IP
	Port uint16
}

// ListIdentity over TCP (encapsulation command 0x63), reusing the same parsers
// as the UDP version (parseListIdentityPayloadToIdentities + parseIdentityItemData).
//
// This is not broadcast discovery. It asks the connected target to identify itself.
// Returns zero or more Identity records (usually 1).
func (e *EipClient) ListIdentityTCP() ([]Identity, error) {
	if e == nil {
		return nil, fmt.Errorf("ListIdentityTCP: received nil client")
	}
	// Force atomic transaction
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.conn == nil {
		return nil, fmt.Errorf("ListIdentityTCP: not connected")
	}

	// ListIdentity (0x63) over an established TCP session.
	// Conventionally uses session_handle = 0 for ListIdentity.
	req := EipEncap{
		command:       0x63,
		length:        0,
		sessionHandle: 0,
		status:        0,
		context:       [8]byte{},
		options:       0,
		data:          nil,
	}

	err := e.sendEncap(req)
	if err != nil {
		return nil, fmt.Errorf("ListIdentityTCP: failed to transmit message.  %w", err)
	}
	resp, err := e.recvEncap()
	if err != nil {
		return nil, fmt.Errorf("ListIdentityTCP: failed to read response: %w", err)
	}
	if resp.status != 0 {
		return nil, fmt.Errorf("ListIdentityTCP: encapsulation status=0x%08x", resp.status)
	}

	// Reuse the UDP parser. TCP responses often have 0.0.0.0 in the embedded socket addr,
	// but the UDP parser already supports a fallback IP. For TCP we don't have a UDP src IP,
	// so pass nil; you'll still get Vendor/Type/Product/Name/etc.
	idents, err := parseListIdentityPayloadToIdentities(resp.data, nil)
	if err != nil {
		return nil, fmt.Errorf("ListIdentityTCP: parse payload: %w", err)
	}

	return idents, nil
}

// ListIdentityUDP broadcasts a ListIdentity (0x63) request over UDP/44818 and
// collects replies until the timeout expires.
//
// broadcastIP can be "255.255.255.255" or a directed broadcast like "192.168.1.255".
// timeout is how long to listen for responses (e.g., 750*time.Millisecond).
func (e *EipClient) ListIdentityUDP(broadcastIP string, timeout time.Duration) ([]Identity, error) {

	if e == nil {
		return nil, fmt.Errorf("ListIdentityUdp: received nil client.")
	}

	// Force atomic transaction
	e.mu.Lock()
	defer e.mu.Unlock()

	// Parse broadcast IP
	ip := net.ParseIP(broadcastIP)
	if ip == nil {
		return nil, fmt.Errorf("invalid broadcast IP: %q", broadcastIP)
	}
	ip = ip.To4()
	if ip == nil {
		return nil, fmt.Errorf("broadcast IP must be IPv4: %q", broadcastIP)
	}

	// Listen on an ephemeral UDP port on all interfaces
	laddr := &net.UDPAddr{IP: net.IPv4zero, Port: 0}
	uc, err := net.ListenUDP("udp4", laddr)
	if err != nil {
		return nil, fmt.Errorf("ListenUDP: %w", err)
	}
	defer uc.Close()

	// Enable broadcast (best-effort; some OSes allow without explicit setting)
	_ = uc.SetWriteBuffer(1 << 20)
	_ = uc.SetReadBuffer(1 << 20)

	// Send ListIdentity encapsulation request (0x63) over UDP to 44818
	// Encapsulation header is always 24 bytes:
	// Command(2) Length(2) Session(4) Status(4) Context(8) Options(4)
	req := make([]byte, 0, 24)
	req = binary.LittleEndian.AppendUint16(req, 0x63) // ListIdentity
	req = binary.LittleEndian.AppendUint16(req, 0)    // length = 0
	req = binary.LittleEndian.AppendUint32(req, 0)    // session handle = 0 for discovery
	req = binary.LittleEndian.AppendUint32(req, 0)    // status = 0
	req = append(req, make([]byte, 8)...)             // sender context
	req = binary.LittleEndian.AppendUint32(req, 0)    // options = 0

	raddr := &net.UDPAddr{IP: ip, Port: 44818}
	if _, err := uc.WriteToUDP(req, raddr); err != nil {
		return nil, fmt.Errorf("WriteToUDP(ListIdentity): %w", err)
	}

	// Read replies until timeout
	deadline := time.Now().Add(timeout)
	if err := uc.SetReadDeadline(deadline); err != nil {
		return nil, fmt.Errorf("SetReadDeadline: %w", err)
	}

	// Collect devices; dedupe by (IP, Serial)
	type key struct {
		ip     string
		serial uint32
	}
	seen := make(map[key]struct{})
	out := make([]Identity, 0, 8)

	buf := make([]byte, 4096)
	for {
		n, src, err := uc.ReadFromUDP(buf)
		if err != nil {
			// Timeout is expected; stop collecting
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				break
			}
			return nil, fmt.Errorf("ReadFromUDP: %w", err)
		}
		if n < 24 {
			// Too short to be an encapsulation packet
			continue
		}

		// Parse encapsulation header
		cmd := binary.LittleEndian.Uint16(buf[0:2])
		if cmd != 0x63 {
			// Some devices may respond with other things; ignore
			continue
		}
		length := int(binary.LittleEndian.Uint16(buf[2:4]))
		status := binary.LittleEndian.Uint32(buf[8:12])
		if status != 0 {
			// Encapsulation-level error; ignore or record if you want
			continue
		}
		if 24+length > n {
			// Truncated packet
			continue
		}

		payload := buf[24 : 24+length]

		idents, err := parseListIdentityPayloadToIdentities(payload, src.IP)
		if err != nil {
			// Ignore malformed replies rather than failing discovery
			continue
		}

		for _, id := range idents {
			k := key{ip: id.IP.String(), serial: id.SerialNumber}
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
			out = append(out, id)
		}
	}

	return out, nil
}

// --- Helpers (kept private) ---

func parseListIdentityPayloadToIdentities(p []byte, fallbackIP net.IP) ([]Identity, error) {
	if len(p) < 2 {
		return nil, fmt.Errorf("payload too short: %d", len(p))
	}

	count := int(binary.LittleEndian.Uint16(p[0:2]))
	off := 2

	idents := make([]Identity, 0, count)
	for i := 0; i < count; i++ {
		if off+4 > len(p) {
			return nil, fmt.Errorf("truncated item header at item %d", i)
		}
		itemType := binary.LittleEndian.Uint16(p[off : off+2])
		itemLen := int(binary.LittleEndian.Uint16(p[off+2 : off+4]))
		off += 4

		if off+itemLen > len(p) {
			return nil, fmt.Errorf("truncated item data at item %d", i)
		}
		itemData := p[off : off+itemLen]
		off += itemLen

		// Identity Item is commonly type 0x000C
		if itemType == 0x000C {
			id, err := parseIdentityItemData(itemData)
			if err != nil {
				return nil, err
			}
			// If identity item didn't yield a valid IP, fall back to UDP src IP
			if id.IP == nil || id.IP.To4() == nil || id.IP.Equal(net.IPv4zero) {
				id.IP = fallbackIP
			}
			idents = append(idents, id)
		}
	}

	return idents, nil
}

func parseIdentityItemData(b []byte) (Identity, error) {
	// Minimum length up to ProductNameLength is 33 bytes (see earlier notes).
	if len(b) < 33 {
		return Identity{}, fmt.Errorf("identity item too short: %d", len(b))
	}
	off := 0

	encapVer := binary.LittleEndian.Uint16(b[off : off+2])
	off += 2

	// Socket Address (16 bytes): family(2), port(2), addr(4), zero(8)
	if off+16 > len(b) {
		return Identity{}, fmt.Errorf("socket address truncated")
	}
	sock := b[off : off+16]
	off += 16

	port := binary.BigEndian.Uint16(sock[2:4]) // network byte order
	ip := net.IPv4(sock[4], sock[5], sock[6], sock[7])

	vendor := binary.LittleEndian.Uint16(b[off : off+2])
	off += 2
	devType := binary.LittleEndian.Uint16(b[off : off+2])
	off += 2
	prodCode := binary.LittleEndian.Uint16(b[off : off+2])
	off += 2

	revMaj := b[off]
	revMin := b[off+1]
	off += 2

	status := binary.LittleEndian.Uint16(b[off : off+2])
	off += 2

	serial := binary.LittleEndian.Uint32(b[off : off+4])
	off += 4

	nameLen := int(b[off])
	off++

	if off+nameLen > len(b) {
		return Identity{}, fmt.Errorf("product name truncated: need %d bytes, have %d", nameLen, len(b)-off)
	}
	name := string(b[off : off+nameLen])
	off += nameLen

	if off >= len(b) {
		return Identity{}, fmt.Errorf("missing state byte")
	}
	state := b[off]

	return Identity{
		EncapsulationVersion: encapVer,
		VendorID:             vendor,
		DeviceType:           devType,
		ProductCode:          prodCode,
		RevisionMajor:        revMaj,
		RevisionMinor:        revMin,
		Status:               status,
		SerialNumber:         serial,
		ProductName:          name,
		State:                state,
		IP:                   ip,
		Port:                 port,
	}, nil
}
