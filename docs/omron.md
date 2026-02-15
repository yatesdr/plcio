# Omron PLCs (FINS & EIP)

plcio supports Omron PLCs via two protocols:

- **FINS** (Factory Interface Network Service) &mdash; For CS1, CJ1/2, CP1, and CV series
- **EIP** (EtherNet/IP with CIP) &mdash; For NJ and NX series

## Supported Hardware

| Series | Protocol | Tag Discovery | Tested | Status |
|---|---|---|---|---|
| CS1 | FINS TCP/UDP | Manual | No | Functional |
| CJ1/CJ2 | FINS TCP/UDP | Manual | No | Functional |
| CP1 | FINS TCP/UDP | Manual | Yes (CP1) | Functional |
| CV | FINS TCP/UDP | Manual | No | Functional |
| NJ | EtherNet/IP | Automatic (no UDT members) | No | **Experimental** |
| NX | EtherNet/IP | Automatic (no UDT members) | No | **Experimental** |

## Omron FINS

### Connection Setup

```go
cfg := &driver.PLCConfig{
    Name:        "omron_cp1",
    Address:     "192.168.1.50",
    Family:      driver.FamilyOmron,
    Protocol:    "fins",
    FinsPort:    9600,   // Default FINS port
    FinsNetwork: 0,      // FINS network number
    FinsNode:    0,      // Destination node (often last octet of PLC IP)
    FinsUnit:    0,      // CPU unit number
    Enabled:     true,
}

drv, err := driver.Create(cfg)
if err != nil {
    log.Fatal(err)
}

if err := drv.Connect(); err != nil {
    log.Fatal(err)
}
defer drv.Close()
```

### FINS Addressing Parameters

| Parameter | Description | Default |
|---|---|---|
| `FinsPort` | TCP/UDP port for FINS communication | 9600 |
| `FinsNetwork` | FINS network number (0 = local) | 0 |
| `FinsNode` | Destination node number. Usually the last octet of the PLC's IP address (e.g., IP 192.168.1.50 → node 50) | 0 |
| `FinsUnit` | CPU unit number (0 = CPU unit) | 0 |

### Memory Areas

FINS uses address-based tag names. Tags are specified as a memory area prefix followed by a word address:

| Area | Prefix | Description | Access |
|---|---|---|---|
| CIO | `CIO` | Core I/O | Read/Write |
| WR | `WR` | Work area | Read/Write |
| HR | `HR` | Holding area | Read/Write |
| AR | `AR` | Auxiliary area | Read only (varies) |
| DM | `DM` | Data Memory | Read/Write |
| EM | `EM0`-`EM9`, `EMA`-`EMC` | Extended Memory banks | Read/Write |
| TASK | `TASK` | Task flags | Read only |

### Reading Tags (FINS)

FINS reads **require type hints** because the protocol operates on raw memory addresses:

```go
results, err := drv.Read([]driver.TagRequest{
    {Name: "DM100",  TypeHint: "INT"},    // DM area, word 100, as INT
    {Name: "DM200",  TypeHint: "DINT"},   // DM area, word 200, as DINT (2 words)
    {Name: "DM300",  TypeHint: "REAL"},   // DM area, word 300, as REAL (2 words)
    {Name: "CIO50",  TypeHint: "WORD"},   // CIO area, word 50
    {Name: "HR0",    TypeHint: "INT"},    // Holding area, word 0
    {Name: "WR10",   TypeHint: "WORD"},   // Work area, word 10
})

if err != nil {
    log.Fatal(err)
}

for _, r := range results {
    if r.Error != nil {
        fmt.Printf("%s: ERROR %v\n", r.Name, r.Error)
    } else {
        fmt.Printf("%s = %v\n", r.Name, r.Value)
    }
}
```

### Bit-Level Access (FINS)

For reading individual bits:

```go
results, _ := drv.Read([]driver.TagRequest{
    {Name: "CIO50.0",  TypeHint: "BOOL"},   // CIO word 50, bit 0
    {Name: "CIO50.15", TypeHint: "BOOL"},   // CIO word 50, bit 15
    {Name: "DM100.0",  TypeHint: "BOOL"},   // DM word 100, bit 0
})
```

### Supported Data Types (FINS)

| Type Hint | Go Type | Words | Byte Order |
|---|---|---|---|
| `BOOL` | `bool` | 1 bit | N/A |
| `BYTE` | `uint8` | 1 (partial) | N/A |
| `WORD` | `uint16` | 1 | Big-endian |
| `INT` / `INT16` | `int16` | 1 | Big-endian |
| `DWORD` | `uint32` | 2 | Big-endian |
| `DINT` / `INT32` | `int32` | 2 | Big-endian |
| `LWORD` | `uint64` | 4 | Big-endian |
| `INT64` | `int64` | 4 | Big-endian |
| `REAL` | `float32` | 2 | Big-endian |
| `LREAL` | `float64` | 4 | Big-endian |
| `STRING` | `string` | Variable | N/A |

### FINS Batch Optimization

plcio automatically optimizes FINS reads with multiple strategies:

1. **Contiguous address grouping** &mdash; Adjacent addresses in the same memory area are combined into a single FINS read (up to 998 words per read)
2. **Multi-memory area read** &mdash; Scattered addresses across different areas use FINS command 0x0104 for a single round-trip
3. **Fallback hierarchy** &mdash; If batch reads fail, the driver falls back to individual reads

This is transparent to the caller; just pass all your tag requests in a single `Read()` call.

### Writing Tags (FINS)

```go
// Write an INT value
err := drv.Write("DM100", 42)

// Write a DINT value
err = drv.Write("DM200", int32(100000))

// Write a BOOL
err = drv.Write("CIO50.0", true)
```

The FINS adapter looks up the configured `DataType` for the tag to determine the correct wire format. Ensure the tag is in your `PLCConfig.Tags` with the correct `DataType`.

### FINS Transport

FINS supports both TCP and UDP transport:
- **TCP** (preferred): More reliable, handles larger payloads
- **UDP**: Lower latency, but limited payload size

plcio defaults to TCP. The driver handles transport-level details automatically.

---

## Omron EIP (NJ/NX Series)

> **Experimental** &mdash; This support is under active development. Structure/UDT member unpacking is not yet implemented. Use with caution.

### Connection Setup

```go
cfg := &driver.PLCConfig{
    Name:     "omron_nj",
    Address:  "192.168.1.60",
    Family:   driver.FamilyOmron,
    Protocol: "eip",
    Enabled:  true,
}

drv, err := driver.Create(cfg)
if err != nil {
    log.Fatal(err)
}

if err := drv.Connect(); err != nil {
    log.Fatal(err)
}
defer drv.Close()
```

### Reading Tags (EIP)

NJ/NX PLCs use symbolic tag names (case-sensitive):

```go
results, err := drv.Read([]driver.TagRequest{
    {Name: "MyVariable"},
    {Name: "Counter1"},
    {Name: "Temperature"},
})
```

No type hints are needed for EIP &mdash; the CIP protocol carries type information.

### Tag Discovery (EIP)

```go
if drv.SupportsDiscovery() {
    tags, err := drv.AllTags()
    if err != nil {
        log.Fatal(err)
    }

    for _, tag := range tags {
        fmt.Printf("%-40s  Type: %-10s\n", tag.Name, tag.TypeName)
    }
}
```

**Current limitation:** UDT/structure members are **not unpacked**. Structures appear as opaque types (e.g., `STRUCT_XX`). Individual member access is not yet supported through discovery.

### EIP Batch Optimization

Omron EIP supports CIP Multiple Service Packet batching:
- Up to 50 tags per batch in connected mode
- Up to 20 tags per batch in unconnected mode
- Automatic fallback to individual reads on batch failure

### Writing Tags (EIP)

```go
err := drv.Write("MyVariable", 42)
err = drv.Write("Temperature_SP", 72.5)
```

### Known Limitations (EIP)

- Structure/UDT members cannot be browsed or read individually
- No Forward Open connection negotiation for larger payloads (planned)
- Less tested than FINS support

---

## Network Discovery

### FINS Discovery

FINS PLCs can be discovered via network scanning:

```go
devices := driver.DiscoverAll("255.255.255.255", "192.168.1.0/24", 500*time.Millisecond, 20)

for _, dev := range devices {
    if dev.Family == driver.FamilyOmron {
        fmt.Printf("Omron at %s (Protocol: %s, Node: %s)\n",
            dev.IP, dev.Protocol, dev.Extra["node"])
    }
}
```

### EIP Discovery

NJ/NX PLCs respond to standard EIP ListIdentity broadcasts and are automatically detected by `DiscoverAll()` or `DiscoverEIPOnly()`. They are identified by CIP Vendor ID 47 (Omron).

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| FINS connection refused | Wrong port or PLC not configured for FINS | Verify FINS port (default 9600), check PLC FINS settings |
| FINS timeout | Wrong node number | Set `FinsNode` to the last octet of the PLC's IP address |
| Wrong values (FINS) | Type hint incorrect | Verify data type and word count for the memory address |
| EIP tag not found | Case-sensitive name mismatch | Check exact tag name (NJ/NX tags are case-sensitive) |
| EIP structure unreadable | UDT member unpacking not supported | Read primitive members individually, avoid structure tags |
| Discovery finds no Omron PLCs | FINS port blocked or EIP not enabled | Check firewall rules for port 9600 (FINS) and 44818 (EIP) |
| Multi-memory read fails | PLC doesn't support command 0x0104 | Driver automatically falls back to individual reads |
