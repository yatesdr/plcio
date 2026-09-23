# Network Discovery

plcio can discover PLCs on your local network using protocol-specific broadcast and scan techniques. This is useful for auto-configuration, inventory, and commissioning tools.

## Discovery Methods

plcio runs four discovery methods in parallel:

| Method | Protocol | Targets | Technique |
|---|---|---|---|
| EIP Broadcast | EtherNet/IP | Allen-Bradley (Logix, SLC 500, PLC-5, MicroLogix), Omron NJ/NX | UDP broadcast on port 44818 |
| S7 Port Scan | S7comm | Siemens S7-* | TCP protocol probe on port 102 |
| ADS Get Info + Scan | ADS | Beckhoff TwinCAT | UDP Get Info (port 48899) unicast to every scanned address and broadcast; TCP 48898 probe only for addresses that do not answer |
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

## Discovery Errors

`DiscoverAll` drops per-protocol failures. `DiscoverAllWithReport` takes the same
arguments and returns them as well:

```go
devices, errs := driver.DiscoverAllWithReport("255.255.255.255", "192.168.1.0/24", 500*time.Millisecond, 20)
for _, err := range errs {
    log.Printf("discovery: %v", err) // e.g. "ADS discovery: ADS UDP discovery send to 255.255.255.255:48899: permission denied"
}
```

Each error names its protocol (EIP, S7, ADS, FINS). Typical entries: an invalid or
oversized scan CIDR (reported once; the S7, ADS and FINS scans are then skipped), a
UDP socket that cannot be opened, a broadcast the OS refuses (no broadcast
permission, no route), unsent unicast probes (summarized as a count plus the first
error) and scan errors. Devices from other protocols or destinations are still
returned, so an empty device list with errors means "could not look", not "nothing
there". Error order is unspecified. For ADS alone, `ads.DiscoverWithReport(ips,
broadcastAddrs, timeout, concurrency)` gives the same report.

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
- `amsNetId` &mdash; AMS Net ID reported by the device over UDP Get Info (use it as `PLCConfig.AmsNetId`; it is often not the IP plus `.1.1`). For a device found only by the TCP fallback it is the IP plus `.1.1` guess that the device accepted
- `hostname` &mdash; Device hostname
- `tcVersion` &mdash; TwinCAT version
- `hasRoute` &mdash; Whether a working ADS device-info exchange verified a route; false means unverified

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
| ADS | 48899 (UDP), 48898 (TCP) | Outbound UDP unicast + broadcast; TCP fallback | Both |
| FINS | 9600 | Outbound TCP | Unicast scan |

Firewalls and VLANs may block discovery. UDP broadcasts do not cross router boundaries unless explicitly forwarded. ADS unicast Get Info to each scanned address does cross routers, so pass the remote subnet as the scan CIDR.

## Scan and ADS identity limits

ADS, S7 and FINS subnet scans accept IPv4 only and reject unsupported IPv6 or
CIDRs larger than 4096 expanded addresses before opening scan sockets. Workers
are bounded at 128 and by the number of hosts. /31 and /32 retain usable boundary
addresses; network/broadcast exclusions use the actual mask rather than the last
octet. Invalid explicit scan IP lists are also rejected before I/O.

ADS discovery sends the TwinCAT UDP Get Info request (magic `0x71146603`, service
1, zero source NetID, AMS port 10000, no tags; the request pyads
`adsGetNetIdForPLC` and Beckhoff's `AdsTool <ip> netid` use) to UDP 48899 of every
scanned address and every local broadcast address from one socket, then collects
replies until the timeout (or until every scanned address answered when no
broadcast is sent). The reply's header (magic, invoke ID, service `0x80000001`,
non-zero NetID) is validated strictly; its tags (`0x0005` hostname, `0x0003`
TwinCAT version, others ignored) are parsed defensively, and a truncated tag drops
only that value. Addresses that answer are not probed over TCP; the rest get the
TCP 48898 device-info probe, which must guess the NetID as IP + `.1.1`.

ADS UDP replies validate advertised identity and do not prove an ADS route.
`HasRoute` is true only after a valid TCP ADS device-info exchange; a reachable
TCP port with an invalid, truncated or ADS-error response does not create a device.
Route installation is outside discovery. See [Beckhoff ADS](beckhoff.md).
