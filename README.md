# plcio

A pure Go library for communicating with industrial PLCs (Programmable Logic Controllers) across multiple vendors and protocols. plcio provides a unified `Driver` interface for reading tags, writing values, discovering devices, and browsing symbol tables across Allen-Bradley, Siemens, Beckhoff, and Omron PLCs.

> **BETA** &mdash; Allen-Bradley and Siemens support is well-tested. Beckhoff is stable but requires more testing. Omron FINS is functional; Omron EIP is experimental.

## Supported PLC Families

| Family | Models | Protocol | Tag Discovery | Tested On |
|---|---|---|---|---|
| **Allen-Bradley Logix** | ControlLogix, CompactLogix | EtherNet/IP (CIP) | Automatic | L7, L8 |
| **Allen-Bradley Micro800** | Micro820, Micro850 | EtherNet/IP (CIP) | Automatic | Micro820 |
| **Siemens S7** | S7-300, S7-400, S7-1200, S7-1500 | S7comm (port 102) | Manual (address-based) | S7-1200 |
| **Beckhoff TwinCAT** | CX series, TwinCAT 2/3 | ADS (port 48898) | Automatic | CX9020 |
| **Omron (FINS)** | CS1, CJ1/2, CP1, CV | FINS TCP/UDP (port 9600) | Manual (address-based) | CP1 |
| **Omron (EIP)** | NJ, NX Series | EtherNet/IP (CIP) | Automatic (no UDT members) | **Experimental** |

## Installation

```bash
go get github.com/yatesdr/plcio
```

Requires Go 1.24 or later. No external dependencies &mdash; plcio is implemented entirely in the Go standard library.

## Quick Start

### Unified Driver Interface

Every PLC uses the same `driver.Driver` interface, so your application code is vendor-agnostic:

```go
package main

import (
    "fmt"
    "log"

    "github.com/yatesdr/plcio/driver"
)

func main() {
    // Create a driver from configuration
    cfg := &driver.PLCConfig{
        Name:    "myPLC",
        Address: "192.168.1.10",
        Family:  driver.FamilyLogix,
        Enabled: true,
    }

    drv, err := driver.Create(cfg)
    if err != nil {
        log.Fatal(err)
    }

    // Connect
    if err := drv.Connect(); err != nil {
        log.Fatal(err)
    }
    defer drv.Close()

    // Read tags
    results, err := drv.Read([]driver.TagRequest{
        {Name: "MyTag"},
        {Name: "AnotherTag"},
    })
    if err != nil {
        log.Fatal(err)
    }

    for _, tv := range results {
        if tv.Error != nil {
            fmt.Printf("%s: ERROR %v\n", tv.Name, tv.Error)
        } else {
            fmt.Printf("%s = %v\n", tv.Name, tv.Value)
        }
    }

    // Write a value
    if err := drv.Write("MyTag", 42); err != nil {
        log.Fatal(err)
    }
}
```

### Allen-Bradley (ControlLogix / CompactLogix)

```go
cfg := &driver.PLCConfig{
    Name:    "logix",
    Address: "192.168.1.10",
    Family:  driver.FamilyLogix,
    Slot:    0, // CPU slot (0 for CompactLogix, varies for ControlLogix)
    Enabled: true,
}

drv, _ := driver.Create(cfg)
drv.Connect()
defer drv.Close()

// Read tags by symbolic name
results, _ := drv.Read([]driver.TagRequest{
    {Name: "Program:MainProgram.Counter"},
    {Name: "MyDINT"},
    {Name: "MyUDT"},
})

// Discover all tags
if drv.SupportsDiscovery() {
    tags, _ := drv.AllTags()
    for _, t := range tags {
        fmt.Printf("  %s (%s)\n", t.Name, t.TypeName)
    }
}
```

### Allen-Bradley Micro800

```go
cfg := &driver.PLCConfig{
    Name:    "micro820",
    Address: "192.168.1.20",
    Family:  driver.FamilyMicro800,
    Enabled: true,
}
```

### Siemens S7

```go
cfg := &driver.PLCConfig{
    Name:    "s7plc",
    Address: "192.168.1.30",
    Family:  driver.FamilyS7,
    Slot:    2, // Slot 2 for S7-300/400, Slot 0 for S7-1200/1500
    Enabled: true,
}

drv, _ := driver.Create(cfg)
drv.Connect()
defer drv.Close()

// S7 uses address-based tags with type hints
results, _ := drv.Read([]driver.TagRequest{
    {Name: "DB1.0", TypeHint: "DINT"},
    {Name: "DB1.4", TypeHint: "REAL"},
    {Name: "M100.0", TypeHint: "BOOL"},
})
```

### Beckhoff TwinCAT

```go
cfg := &driver.PLCConfig{
    Name:     "beckhoff",
    Address:  "192.168.1.40",
    Family:   driver.FamilyBeckhoff,
    AmsNetId: "192.168.1.40.1.1", // Target AMS Net ID
    AmsPort:  851,                 // TwinCAT 3 runtime (801 for TC2)
    Enabled:  true,
}

drv, _ := driver.Create(cfg)
drv.Connect()
defer drv.Close()

// Read by symbol name
results, _ := drv.Read([]driver.TagRequest{
    {Name: "MAIN.counter"},
    {Name: "MAIN.temperature"},
    {Name: "GVL.status_word"},
})
```

### Omron (FINS)

```go
cfg := &driver.PLCConfig{
    Name:        "omron_fins",
    Address:     "192.168.1.50",
    Family:      driver.FamilyOmron,
    Protocol:    "fins",
    FinsPort:    9600,
    FinsNetwork: 0,
    FinsNode:    0, // Usually last octet of PLC IP
    FinsUnit:    0,
    Enabled:     true,
}

drv, _ := driver.Create(cfg)
drv.Connect()
defer drv.Close()

// FINS uses memory area addresses with type hints
results, _ := drv.Read([]driver.TagRequest{
    {Name: "DM100", TypeHint: "INT"},
    {Name: "DM200", TypeHint: "DINT"},
    {Name: "CIO50", TypeHint: "WORD"},
    {Name: "HR0", TypeHint: "INT"},
})
```

### Omron (EIP) &mdash; Experimental

```go
cfg := &driver.PLCConfig{
    Name:     "omron_nj",
    Address:  "192.168.1.60",
    Family:   driver.FamilyOmron,
    Protocol: "eip",
    Enabled:  true,
}

drv, _ := driver.Create(cfg)
drv.Connect()
defer drv.Close()

// EIP uses symbolic tag names (case-sensitive)
results, _ := drv.Read([]driver.TagRequest{
    {Name: "MyVariable"},
    {Name: "Counter1"},
})
```

## Network Discovery

Discover PLCs on your network across all supported protocols:

```go
import "github.com/yatesdr/plcio/driver"

// Discover all PLC types on the local network
devices := driver.DiscoverAll(
    "255.255.255.255", // Broadcast address
    "192.168.1.0/24",  // Subnet to scan
    500*time.Millisecond, // Timeout per device
    20,                // Concurrent scan workers
)

for _, dev := range devices {
    fmt.Printf("[%s] %s at %s:%d (%s)\n",
        dev.Family, dev.ProductName, dev.IP, dev.Port, dev.Vendor)
}
```

## Key Features

- **Unified interface** &mdash; One `Driver` interface works across all PLC families
- **Zero dependencies** &mdash; Pure Go standard library, no CGO
- **Tag discovery** &mdash; Browse and enumerate tags on supported PLCs
- **Network discovery** &mdash; Find PLCs on your network via EIP broadcast, S7 port scan, ADS broadcast, and FINS scan
- **Batch reads** &mdash; Efficient multi-tag reads with automatic protocol-level batching
- **Structure decoding** &mdash; Automatic UDT/struct member unpacking (Logix, ADS)
- **Per-tag errors** &mdash; Individual tag failures don't fail the entire batch
- **Connection detection** &mdash; Built-in heuristics to detect connection loss
- **Keep-alive** &mdash; Automatic connection maintenance for protocols that need it

## Documentation

Detailed documentation for each PLC family and feature:

- [Allen-Bradley (Logix & Micro800)](docs/allen-bradley.md)
- [Siemens S7](docs/siemens-s7.md)
- [Beckhoff TwinCAT (ADS)](docs/beckhoff.md)
- [Omron (FINS & EIP)](docs/omron.md)
- [Network Discovery](docs/network-discovery.md)
- [API Reference](docs/api-reference.md)
- [Safety & Intended Use](docs/safety-and-intended-use.md)
- [Troubleshooting](docs/troubleshooting.md)

## Support Status

| Feature | Logix | Micro800 | S7 | Beckhoff | Omron FINS | Omron EIP |
|---|:---:|:---:|:---:|:---:|:---:|:---:|
| Connect/Disconnect | Stable | Stable | Stable | Stable | Stable | Experimental |
| Read Tags | Stable | Stable | Stable | Stable | Stable | Experimental |
| Write Tags | Stable | Stable | Stable | Stable | Stable | Experimental |
| Tag Discovery | Stable | Stable | N/A | Stable | N/A | Experimental |
| Network Discovery | Stable | Stable | Stable | Stable | Stable | Stable |
| Batch Reads | Stable | N/A | Stable | Stable | Stable | Experimental |
| UDT/Struct Decode | Stable | Stable | N/A | Partial | N/A | No |
| Device Info | Stable | Stable | Stable | Stable | Stable | Experimental |
| Keep-alive | Stable | Stable | N/A | N/A | Stable | Experimental |

## Acknowledgements

plcio was built from the ground up in pure Go, but the protocol implementations would not have been possible without the research, documentation, and reference code provided by several outstanding open-source projects:

- **[pylogix](https://github.com/dmroeder/pylogix)** &mdash; Invaluable reference for Allen-Bradley EtherNet/IP and CIP implementation details including Forward Open connection parameters, tag discovery, and template decoding. Many protocol constants and sequencing details were validated against pylogix's well-tested codebase.

- **[pycomm3](https://github.com/ottowayi/pycomm3)** &mdash; Reference for CIP structure attribute handling and template size computation. The approach to UDT member decoding was informed by pycomm3's implementation.

- **[libplctag](https://github.com/libplctag/libplctag)** &mdash; Essential resource for Omron EIP/CIP support. GitHub issues and source code provided critical insight into Omron NJ/NX symbol object attributes and CIP vendor-specific behavior. Also a valuable general reference for multi-vendor PLC protocol details.

- **[rust-eip](https://github.com/Joylei/eip-rs)** &mdash; Reference for EtherNet/IP session management and CIP message framing patterns, helpful for validating our EIP transport layer implementation.

- **[gos7](https://github.com/robinson/gos7)** &mdash; Go implementation of the S7comm protocol that served as a reference for S7 connection setup, PDU negotiation, and data block addressing.

- **[Wireshark](https://www.wireshark.org/)** &mdash; Protocol captures with Wireshark's EtherNet/IP, S7comm, and ADS dissectors were used extensively to validate packet structures and debug protocol-level issues across all PLC families.

- **Omron W506 Manual** &mdash; The *NJ/NX-series CPU Unit Built-in EtherNet/IP Port User's Manual* provided essential protocol details for Omron EIP tag discovery and symbol access.

Thank you to the maintainers and contributors of these projects for making industrial protocol communication more accessible.

## Disclaimer

**plcio is provided "AS IS" without warranty of any kind.** PLCs frequently control industrial equipment that can cause serious injury or death if operated improperly. This library is intended for **monitoring and data collection only**. See [Safety & Intended Use](docs/safety-and-intended-use.md) for critical safety information before using this library in any industrial environment.

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for the full text.
