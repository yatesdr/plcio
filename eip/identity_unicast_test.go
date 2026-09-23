package eip

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestListIdentityUnicastCollectsRepliesAndStopsEarly(t *testing.T) {
	srv, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	port := srv.LocalAddr().(*net.UDPAddr).Port

	name := "2080-LC20-20QWB"
	item := binary.LittleEndian.AppendUint16(nil, 1)
	sock := make([]byte, 16)
	binary.BigEndian.PutUint16(sock[0:2], 2)
	binary.BigEndian.PutUint16(sock[2:4], 44818)
	copy(sock[4:8], []byte{127, 0, 0, 1})
	item = append(item, sock...)
	item = binary.LittleEndian.AppendUint16(item, 1)
	item = binary.LittleEndian.AppendUint16(item, 0x0E)
	item = binary.LittleEndian.AppendUint16(item, 0x00A6)
	item = append(item, 14, 11)
	item = binary.LittleEndian.AppendUint16(item, 0x3060)
	item = binary.LittleEndian.AppendUint32(item, 1889069414)
	item = append(item, byte(len(name)))
	item = append(item, name...)
	item = append(item, 0x03)
	payload := binary.LittleEndian.AppendUint16(nil, 1)
	payload = append(payload, cpfItem(CpfTypeListIdentityResponseId, uint16(len(item)), item)...)

	go func() {
		buf := make([]byte, 64)
		n, src, err := srv.ReadFromUDP(buf)
		if err != nil || n != 24 || binary.LittleEndian.Uint16(buf) != 0x63 {
			return
		}
		srv.WriteToUDP([]byte("junk"), src) // malformed datagram is ignored
		srv.WriteToUDP(append(encapHeader(0x63, uint16(len(payload)), 0, 0), payload...), src)
	}()
	start := time.Now()
	ids, err := listIdentityUnicast([]net.IP{net.IPv4(127, 0, 0, 1)}, port, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0].ProductName != name || ids[0].SerialNumber != 1889069414 {
		t.Fatalf("unexpected identities: %+v", ids)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("did not stop once every address had answered")
	}
}

// A device whose first request is lost is asked again in the second pass.
func TestListIdentityUnicastRetriesNonResponders(t *testing.T) {
	srv, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	port := srv.LocalAddr().(*net.UDPAddr).Port
	item := make([]byte, 34)
	binary.LittleEndian.PutUint16(item, 1)
	binary.BigEndian.PutUint16(item[2:], 2)
	item = append(item, 0, 0x03)
	item[33] = 0 // empty product name
	payload := binary.LittleEndian.AppendUint16(nil, 1)
	payload = append(payload, cpfItem(CpfTypeListIdentityResponseId, uint16(len(item)), item)...)
	go func() {
		buf := make([]byte, 64)
		srv.ReadFromUDP(buf) // first request dropped
		_, src, err := srv.ReadFromUDP(buf)
		if err != nil {
			return
		}
		srv.WriteToUDP(append(encapHeader(0x63, uint16(len(payload)), 0, 0), payload...), src)
	}()
	ids, err := listIdentityUnicast([]net.IP{net.IPv4(127, 0, 0, 1)}, port, 2*time.Second)
	if err != nil || len(ids) != 1 {
		t.Fatalf("ids=%+v err=%v", ids, err)
	}
}
