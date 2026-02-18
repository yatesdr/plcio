# Network Discovery

plcio can discover PLCs on your local network using protocol-specific broadcast and scan techniques. This is useful for auto-configuration, inventory, and commissioning tools.

## Discovery Methods

plcio runs four discovery methods in parallel:

| Method | Protocol | Targets | Technique |
|---|---|---|---|
| EIP Broadcast | EtherNet/IP | Allen-Bradley (Logix, SLC 500, PLC-5, MicroLogix), Omron NJ/NX | UDP broadcast on port 44818 |
| S7 Port Scan | S7comm | Siemens S7-* | TCP connect scan on port 102 |
| ADS Broadcast + Scan | ADS | Beckhoff TwinCAT | UDP broadcast + TCP scan on port 48898 |
| FINS Scan | FINS | Omron CS/CJ/CP | Network scan on port 9600 |

## Discover All PLCs

```go
import (
    "fmt"
    "time"

    "github.com/yatesdr/plcio/driver"
)

func main() {
    // Discover all PLC types on the local network
    devices := driver.DiscoverAll(
        "255.255.255.255",        // Broadcast address (or specific subnet broadcast)
        "192.168.1.0/24",         // CIDR for port scanning (S7, ADS, FINS)
        500*time.Millisecond,     // Timeout per device
        20,                        // Max concurrent scan workers
    )

    for _, dev := range devices {
        fmt.Printf("[%s] %s at %s:%d\n", dev.Family, dev.ProductName, dev.IP, dev.Port)
        fmt.Printf("  Vendor: %s  Protocol: %s\n", dev.Vendor, dev.Protocol)
        for k, v := range dev.Extra {
            fmt.Printf("  %s: %s\n", k, v)
        }
        fmt.Println()
    }
}
```

## EIP-Only Discovery

For environments where you only need Allen-Bradley and Omron NJ/NX devices:

```go
// Faster - only sends EIP ListIdentity broadcast
devices := driver.DiscoverEIPOnly("255.255.255.255", 2*time.Second)
```

EIP broadcast discovery is the fastest and most reliable method. It sends a single UDP broadcast and collects responses.

## DiscoveredDevice Structure

```go
type DiscoveredDevice struct {
    IP           net.IP            // Device IP address
    Port         uint16            // Protocol port
    Family       PLCFamily         // logix, micro800, s7, beckhoff, omron
    ProductName  string            // Product name or description
    Protocol     string            // Discovery protocol used (EIP, S7, ADS, FINS)
    Vendor       string            // Vendor name
    Extra        map[string]string // Protocol-specific metadata
    DiscoveredAt time.Time         // Discovery timestamp
}
```

### Extra Fields by Protocol

**EIP (Allen-Bradley, Omron):**
- `serial` &mdash; Device serial number
- `revision` &mdash; Firmware revision (major.minor)
- `vendorId` &mdash; CIP Vendor ID (1 = Rockwell, 47 = Omron)

**S7 (Siemens):**
- `rack` &mdash; Detected rack number
- `slot` &mdash; Detected slot number

**ADS (Beckhoff):**
- `amsNetId` &mdash; AMS Net ID
- `hostname` &mdash; Device hostname
- `tcVersion` &mdash; TwinCAT version
- `hasRoute` &mdash; Whether an ADS route exists to this device

**FINS (Omron):**
- `node` &mdash; FINS node number

## Automatic Vendor Detection

EIP discovery automatically identifies vendors by CIP Vendor ID:

| Vendor ID | Vendor | PLC Family |
|---|---|---|
| 1 | Rockwell Automation | `logix` or `micro800` |
| 47 | Omron | `omron` |

Micro800 devices are identified by product names starting with "2080-".

## Helper Functions

### Get Local Subnets

```go
// Returns CIDR notations for all active network interfaces
subnets := driver.GetLocalSubnets()
// e.g., ["192.168.1.0/24", "10.0.0.0/16"]
```

### Get Broadcast Addresses

```go
// Returns broadcast addresses for all active interfaces
broadcasts := driver.GetBroadcastAddresses()
// e.g., ["192.168.1.255", "10.0.255.255"]
```

These helpers are useful for building the parameters for `DiscoverAll()`:

```go
subnets := driver.GetLocalSubnets()
broadcasts := driver.GetBroadcastAddresses()

broadcastAddr := "255.255.255.255"
if len(broadcasts) > 0 {
    broadcastAddr = broadcasts[0]
}

scanCIDR := ""
if len(subnets) > 0 {
    scanCIDR = subnets[0]
}

devices := driver.DiscoverAll(broadcastAddr, scanCIDR, time.Second, 20)
```

## Deduplication

When multiple discovery methods find the same device (e.g., an Omron NJ responds to both EIP broadcast and FINS scan), results are deduplicated by IP address. EIP results are preferred over other protocols when duplicates are found.

## Performance Notes

- **EIP broadcast** completes in 2-3 seconds (UDP, waits for all responses)
- **Port scans** (S7, ADS, FINS) scale with subnet size and concurrency setting
- All four methods run in parallel, so total time equals the slowest method
- A `/24` subnet scan with 20 workers typically completes in 5-15 seconds
- Set timeout to at least 500ms; some PLCs are slow to respond

## Network Requirements

| Protocol | Port | Direction | Type |
|---|---|---|---|
| EIP | 44818 | Outbound UDP broadcast | Broadcast |
| S7 | 102 | Outbound TCP | Unicast scan |
| ADS | 48898 | Outbound UDP broadcast + TCP | Both |
| FINS | 9600 | Outbound TCP | Unicast scan |

Firewalls and VLANs may block discovery. UDP broadcasts do not cross router boundaries unless explicitly forwarded.
