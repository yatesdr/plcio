package netutil

import (
	"encoding/binary"
	"fmt"
	"net"
)

const MaxScanAddresses = 4096
const MaxScanWorkers = 128

// ExpandIPv4 checks cardinality before enumeration and excludes only the actual
// network/broadcast pair. RFC 3021 /31 links and /32 hosts retain all addresses.
func ExpandIPv4(cidr string) ([]net.IP, error) {
	ip, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR: %w", err)
	}
	if ip.To4() == nil {
		return nil, fmt.Errorf("IPv6 subnet scans are unsupported")
	}
	ones, bits := network.Mask.Size()
	if bits != 32 {
		return nil, fmt.Errorf("IPv6 subnet scans are unsupported")
	}
	count := uint64(1) << uint(32-ones)
	if count > MaxScanAddresses {
		return nil, fmt.Errorf("subnet has %d addresses; scan limit is %d", count, MaxScanAddresses)
	}
	first, last := uint64(0), count
	if ones < 31 {
		first, last = 1, count-1
	}
	base := uint64(binary.BigEndian.Uint32(network.IP.To4()))
	result := make([]net.IP, 0, int(last-first))
	for index := first; index < last; index++ {
		address := make(net.IP, 4)
		binary.BigEndian.PutUint32(address, uint32(base+index))
		result = append(result, address)
	}
	return result, nil
}

func ScanWorkers(requested, addresses int) int {
	if requested <= 0 {
		requested = 20
	}
	if requested > MaxScanWorkers {
		requested = MaxScanWorkers
	}
	if requested > addresses {
		requested = addresses
	}
	return requested
}

func ValidScan(ips []net.IP) bool {
	if len(ips) == 0 || len(ips) > MaxScanAddresses {
		return false
	}
	for _, ip := range ips {
		if ip.To4() == nil {
			return false
		}
	}
	return true
}
