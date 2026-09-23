package driver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yatesdr/plcio/logging"
)

func TestGetProtocolCaseInsensitive(t *testing.T) {
	for _, tc := range []struct {
		protocol                string
		want                    string
		eip, fins, addressBased bool
	}{
		{"", "fins", false, true, true},
		{"fins", "fins", false, true, true},
		{"FINS", "fins", false, true, true},
		{"eip", "eip", true, false, false},
		{"EIP", "eip", true, false, false},
		{"Eip", "eip", true, false, false},
	} {
		cfg := &PLCConfig{Family: FamilyOmron, Protocol: tc.protocol}
		if got := cfg.GetProtocol(); got != tc.want {
			t.Errorf("GetProtocol(%q) = %q, want %q", tc.protocol, got, tc.want)
		}
		if cfg.IsOmronEIP() != tc.eip || cfg.IsOmronFINS() != tc.fins || cfg.IsAddressBased() != tc.addressBased {
			t.Errorf("%q: eip=%v fins=%v addressBased=%v", tc.protocol, cfg.IsOmronEIP(), cfg.IsOmronFINS(), cfg.IsAddressBased())
		}
		adapter, err := NewOmronAdapter(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.SupportsDiscovery() != adapter.SupportsDiscovery() {
			t.Errorf("%q: config SupportsDiscovery=%v, adapter=%v", tc.protocol, cfg.SupportsDiscovery(), adapter.SupportsDiscovery())
		}
	}
	if got := (&PLCConfig{Family: FamilyLogix, Protocol: "EIP"}).GetProtocol(); got != "" {
		t.Errorf("non-Omron GetProtocol = %q", got)
	}
}

func TestCreateRejectsUnknownFamily(t *testing.T) {
	for _, family := range []PLCFamily{"", FamilyLogix, FamilyMicro800} {
		drv, err := Create(&PLCConfig{Family: family, Address: "127.0.0.1"})
		if err != nil {
			t.Fatalf("%q: %v", family, err)
		}
		if _, ok := drv.(*LogixAdapter); !ok {
			t.Errorf("%q: got %T", family, drv)
		}
	}
	for _, family := range []PLCFamily{FamilySLC500, FamilyPLC5, FamilyMicroLogix, FamilyS7, FamilyBeckhoff, FamilyOmron} {
		if _, err := Create(&PLCConfig{Family: family, Address: "127.0.0.1"}); err != nil {
			t.Errorf("%q: %v", family, err)
		}
	}
	// Family names are case-insensitive and whitespace-tolerant.
	for family, want := range map[PLCFamily]string{"S7": "*driver.S7Adapter", " Logix ": "*driver.LogixAdapter",
		"BECKHOFF": "*driver.ADSAdapter", "\tmicro800\n": "*driver.LogixAdapter", "  ": "*driver.LogixAdapter", "Omron": "*driver.OmronAdapter"} {
		cfg := &PLCConfig{Family: family, Address: "127.0.0.1"}
		drv, err := Create(cfg)
		if err != nil || fmt.Sprintf("%T", drv) != want {
			t.Errorf("%q: %T, %v", family, drv, err)
		}
		if cfg.Family != family {
			t.Errorf("caller's config mutated: %q", cfg.Family)
		}
	}
	for _, family := range []PLCFamily{"siemens", "controllogix", "s 7", "logix5000"} {
		drv, err := Create(&PLCConfig{Family: family, Address: "127.0.0.1"})
		if err == nil || drv != nil || !strings.Contains(err.Error(), "unknown PLC family") {
			t.Errorf("%q: %T, %v", family, drv, err)
		}
	}
}

func TestDiscoveryLogTag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.log")
	logger, err := logging.NewDebugLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	previous := logging.GetGlobalDebugLogger()
	logging.SetGlobalDebugLogger(logger)
	defer logging.SetGlobalDebugLogger(previous)

	// An invalid CIDR fails before any network I/O.
	if devices := discoverFINS("not-a-cidr", time.Millisecond, 1); len(devices) != 0 {
		t.Fatal(devices)
	}
	logging.SetGlobalDebugLogger(previous)
	logger.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "[discovery] discoverFINS") || strings.Contains(string(data), "[tui]") {
		t.Fatalf("discovery log tag:\n%s", data)
	}
}

func TestGetProtocolFINSTransportsAndSpaces(t *testing.T) {
	for _, p := range []string{"", "fins", " FINS ", "fins-tcp", "FINS-UDP"} {
		c := &PLCConfig{Family: FamilyOmron, Protocol: p}
		if c.GetProtocol() != "fins" || !c.IsOmronFINS() || c.IsOmronEIP() {
			t.Fatalf("protocol %q: got %q", p, c.GetProtocol())
		}
	}
	if c := (&PLCConfig{Family: FamilyOmron, Protocol: " EIP "}); !c.IsOmronEIP() {
		t.Fatal("\" EIP \" not recognised as EIP")
	}
}
