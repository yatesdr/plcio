package netutil

import (
	"net"
	"testing"
)

func TestIPv4ExpansionBoundsAndMasks(t *testing.T) {
	for _, test := range []struct {
		cidr, first, last string
		count             int
	}{
		{"10.0.0.0/23", "10.0.0.1", "10.0.1.254", 510},
		{"10.0.0.128/25", "10.0.0.129", "10.0.0.254", 126},
		{"10.0.0.0/20", "10.0.0.1", "10.0.15.254", 4094},
		{"10.0.0.254/31", "10.0.0.254", "10.0.0.255", 2},
		{"255.255.255.254/31", "255.255.255.254", "255.255.255.255", 2},
		{"255.255.255.255/32", "255.255.255.255", "255.255.255.255", 1},
	} {
		ips, err := ExpandIPv4(test.cidr)
		if err != nil || len(ips) != test.count || ips[0].String() != test.first || ips[len(ips)-1].String() != test.last {
			t.Fatalf("%s: %v %v", test.cidr, ips, err)
		}
		if test.cidr == "10.0.0.0/23" && (ips[254].String() != "10.0.0.255" || ips[255].String() != "10.0.1.0") {
			t.Fatal("filtered legitimate internal .0/.255 hosts")
		}
	}
	for _, cidr := range []string{"10.0.0.0/19", "0.0.0.0/0", "::/0", "::1/128", "invalid"} {
		if _, err := ExpandIPv4(cidr); err == nil {
			t.Fatalf("accepted %s", cidr)
		}
	}
	if ScanWorkers(1<<30, 4096) != 128 || ScanWorkers(0, 3) != 3 || ScanWorkers(2, 5) != 2 {
		t.Fatal("worker cap")
	}
	if ValidScan([]net.IP{net.ParseIP("::1")}) || ValidScan(make([]net.IP, 4097)) {
		t.Fatal("invalid direct scan accepted")
	}
}
