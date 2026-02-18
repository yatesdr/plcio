# Allen-Bradley SLC 500, PLC-5 & MicroLogix (PCCC)

plcio supports Allen-Bradley SLC 500, PLC-5, and MicroLogix processors using the PCCC (Programmable Controller Communication Commands) protocol tunneled over EtherNet/IP.

## Supported Hardware

| Series | Models | Protocol | Connection Mode | Tested |
|---|---|---|---|---|
| SLC 500 | SLC 5/03, 5/04, 5/05 | PCCC over EtherNet/IP | Unconnected | No |
| PLC-5 | PLC-5/20E, 5/40E, 5/80E | PCCC over EtherNet/IP | Unconnected | No |
| MicroLogix | 1100, 1200, 1400, 1500 | PCCC over EtherNet/IP | Unconnected | No |

**Default port:** TCP 44818

**Important:** Only Ethernet-equipped models are supported. The SLC 5/01 and 5/02 do not have Ethernet ports and cannot be reached directly. The MicroLogix 1000 does not have a built-in Ethernet port but can be reached through a gateway (see [Routing Through a Gateway](#routing-through-a-gateway) below).

## Connection Setup

### SLC 500

```go
cfg := &driver.PLCConfig{
    Name:    "slc500",
    Address: "192.168.1.10",
    Family:  driver.FamilySLC500,
    Enabled: true,
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

### PLC-5

```go
cfg := &driver.PLCConfig{
    Name:    "plc5",
    Address: "192.168.1.11",
    Family:  driver.FamilyPLC5,
    Enabled: true,
}
```

### MicroLogix

```go
cfg := &driver.PLCConfig{
    Name:    "micrologix",
    Address: "192.168.1.12",
    Family:  driver.FamilyMicroLogix,
    Enabled: true,
}
```

### Timeout Configuration

```go
cfg := &driver.PLCConfig{
    Name:    "slc500",
    Address: "192.168.1.10",
    Family:  driver.FamilySLC500,
    Timeout: 10 * time.Second, // Default is 5s
    Enabled: true,
}
```

## Tag Addressing

PCCC PLCs use **file-based data table addresses**, not symbolic tag names. You must know the data file type, file number, and element number for each value you want to read or write.

### Address Format

```
[TypePrefix][FileNumber]:[Element][.SubElement][/Bit]
```

- **TypePrefix** &mdash; One or two letters identifying the file type (N, F, B, T, etc.)
- **FileNumber** &mdash; The data file number (some types have defaults and can be omitted)
- **Element** &mdash; The element index within the file
- **SubElement** &mdash; Named sub-element for complex types (Timer, Counter, Control)
- **Bit** &mdash; Bit position (0&ndash;15) within a 16-bit word

### File Types

| Prefix | File Type | Default File # | Element Size | Description |
|---|---|---|---|---|
| `O` | Output | 0 | 2 bytes | Digital output image |
| `I` | Input | 1 | 2 bytes | Digital input image |
| `S` | Status | 2 | 2 bytes | Processor status |
| `B` | Binary | &mdash; | 2 bytes | Bit storage (16 bits per element) |
| `T` | Timer | &mdash; | 6 bytes | Timer (3 sub-elements) |
| `C` | Counter | &mdash; | 6 bytes | Counter (3 sub-elements) |
| `R` | Control | &mdash; | 6 bytes | Control (3 sub-elements) |
| `N` | Integer | &mdash; | 2 bytes | 16-bit signed integer |
| `F` | Float | &mdash; | 4 bytes | 32-bit IEEE 754 float |
| `ST` | String | &mdash; | 84 bytes | 82-char string + 2-byte length |
| `A` | ASCII | &mdash; | 2 bytes | ASCII data |
| `L` | Long | &mdash; | 4 bytes | 32-bit signed integer |
| `MG` | Message | &mdash; | 50 bytes | Message control (MicroLogix) |
| `PD` | PID | &mdash; | 46 bytes | PID control |

### Address Examples

**Simple types:**

| Address | Meaning |
|---|---|
| `N7:0` | Integer file 7, element 0 |
| `N7:42` | Integer file 7, element 42 |
| `F8:5` | Float file 8, element 5 |
| `L10:0` | Long integer file 10, element 0 |
| `ST9:0` | String file 9, element 0 |

**I/O and status (default file numbers):**

| Address | Meaning |
|---|---|
| `O:0` | Output file 0, element 0 (entire 16-bit word) |
| `I:0` | Input file 1, element 0 |
| `S:1` | Status file 2, element 1 |

**Bit access (any 16-bit word):**

| Address | Meaning |
|---|---|
| `B3:0/5` | Binary file 3, element 0, bit 5 |
| `O:0/3` | Output word 0, bit 3 |
| `I:0/7` | Input word 0, bit 7 |
| `S:1/5` | Status word 1, bit 5 |
| `N7:0/0` | Integer file 7, element 0, bit 0 |

**Timer sub-elements:**

| Address | Meaning |
|---|---|
| `T4:0` | Timer file 4, element 0 (full 6-byte element) |
| `T4:0.PRE` | Timer 4:0 preset value |
| `T4:0.ACC` | Timer 4:0 accumulated value |
| `T4:0.EN` | Timer 4:0 enable bit |
| `T4:0.TT` | Timer 4:0 timing bit |
| `T4:0.DN` | Timer 4:0 done bit |

**Counter sub-elements:**

| Address | Meaning |
|---|---|
| `C5:2` | Counter file 5, element 2 (full element) |
| `C5:2.PRE` | Counter 5:2 preset value |
| `C5:2.ACC` | Counter 5:2 accumulated value |
| `C5:2.CU` | Counter 5:2 count up enable bit |
| `C5:2.CD` | Counter 5:2 count down enable bit |
| `C5:2.DN` | Counter 5:2 done bit |
| `C5:2.OV` | Counter 5:2 overflow bit |
| `C5:2.UN` | Counter 5:2 underflow bit |

**Control sub-elements:**

| Address | Meaning |
|---|---|
| `R6:0.LEN` | Control 6:0 length |
| `R6:0.POS` | Control 6:0 position |
| `R6:0.EN` | Control 6:0 enable bit |
| `R6:0.DN` | Control 6:0 done bit |

## Reading Tags

PCCC reads do **not** require type hints. The file type is determined from the address prefix, and plcio automatically decodes the value into the appropriate Go type.

```go
results, err := drv.Read([]driver.TagRequest{
    {Name: "N7:0"},       // Integer
    {Name: "F8:5"},       // Float
    {Name: "B3:0/5"},     // Single bit
    {Name: "T4:0.ACC"},   // Timer accumulated value
    {Name: "ST9:0"},      // String
    {Name: "O:0"},        // Output word
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

### Decoded Value Types

| File Type | Go Type | Notes |
|---|---|---|
| N (Integer) | `int16` | 16-bit signed |
| F (Float) | `float32` | IEEE 754 |
| L (Long) | `int32` | 32-bit signed |
| B, O, I, S, A (16-bit word) | `int16` | When reading the full word |
| Bit access (`/N`) | `bool` | Single bit extracted from word |
| T (Timer, full) | `map[string]interface{}` | Keys: `control`, `PRE`, `ACC`, `EN`, `TT`, `DN` |
| C (Counter, full) | `map[string]interface{}` | Keys: `control`, `PRE`, `ACC`, `CU`, `CD`, `DN`, `OV`, `UN` |
| R (Control, full) | `map[string]interface{}` | Keys: `control`, `LEN`, `POS`, `EN`, `EU`, `DN`, `EM`, `ER`, `UL`, `IN`, `FD` |
| T/C/R sub-element (PRE, ACC, LEN, POS) | `int16` | Individual 16-bit sub-element |
| T/C/R status bit (DN, EN, etc.) | `bool` | Individual bit from control word |
| ST (String) | `string` | Decoded from 84-byte element |

### Reading Complex Types

When you read a Timer, Counter, or Control element without a sub-element qualifier, plcio returns a map with all sub-elements decoded:

```go
results, _ := drv.Read([]driver.TagRequest{
    {Name: "T4:0"},  // Full timer element
})

if m, ok := results[0].Value.(map[string]interface{}); ok {
    fmt.Printf("Preset:      %v\n", m["PRE"])
    fmt.Printf("Accumulated: %v\n", m["ACC"])
    fmt.Printf("Done:        %v\n", m["DN"])
    fmt.Printf("Timing:      %v\n", m["TT"])
    fmt.Printf("Enabled:     %v\n", m["EN"])
}
```

## Writing Tags

```go
// Write an integer
err := drv.Write("N7:0", 42)

// Write a float
err = drv.Write("F8:5", 3.14)

// Write a boolean (single bit)
err = drv.Write("B3:0/5", true)

// Write a 32-bit long integer
err = drv.Write("L10:0", int32(100000))
```

### Bit Writes

Writing to a single bit (e.g., `B3:0/5`) performs a **read-modify-write** operation: plcio reads the containing 16-bit word, sets or clears the specified bit, and writes the word back. This is atomic at the protocol level but not at the PLC scan level &mdash; if the PLC modifies other bits in the same word between the read and write, those changes could be lost.

**Recommendation:** If you need to write individual bits frequently, use dedicated integer words as status/command registers instead of individual bit addresses.

### Write Limitations

- Writes are single-address operations (no batch writes)
- Writing to Timer/Counter/Control status words (sub-element 0) is blocked to prevent corrupting processor-managed control bits
- You can write to PRE, ACC, LEN, and POS sub-elements
- Not optimized for high-throughput writing
- No type hints needed &mdash; the wire format is determined from the address

## Tag Discovery

SLC 500 and MicroLogix processors support **automatic data table discovery** by reading the file directory (system file 0). This enumerates all configured data files with their type and element count.

```go
if drv.SupportsDiscovery() {
    tags, err := drv.AllTags()
    if err != nil {
        log.Fatal(err)
    }
    for _, t := range tags {
        fmt.Printf("  %s: %s (%d elements)\n", t.Name, t.TypeName, t.Dimensions[0])
    }
}
// Example output:
//   O0: OUTPUT (1 elements)
//   I1: INPUT (1 elements)
//   S2: STATUS (33 elements)
//   B3: BINARY (1 elements)
//   T4: TIMER (3 elements)
//   C5: COUNTER (3 elements)
//   R6: CONTROL (1 elements)
//   N7: INT (50 elements)
//   F8: FLOAT (10 elements)
```

### Supported Processors

| Family | Discovery | Method |
|---|---|---|
| SLC 500 (5/03, 5/04, 5/05) | Supported | File directory read |
| MicroLogix (1000, 1100, 1200, 1400, 1500) | Supported | File directory read |
| PLC-5 | Not supported | PLC-5 does not expose a file directory |

### How It Works

Discovery sends two PCCC commands:

1. **Diagnostic Status** (CMD 0x06) &mdash; retrieves the processor catalog string (e.g., "1747-L552") to identify the processor family and determine the file directory binary layout.
2. **Read Section** (CMD 0x0F, FNC 0xA1) &mdash; reads system file 0 (the file directory) in 80-byte chunks, then parses each row to extract the file type code and element count.

The discovered tag names use the format `PrefixFileNumber` (e.g., `N7`, `F8`, `T4`). These names correspond directly to the data table addresses used for reading and writing.

### PLC-5

PLC-5 does **not** support file directory discovery. You must configure addresses manually based on your PLC program.

```go
drv.SupportsDiscovery() // Returns false for PLC-5
```

To determine the data table layout, refer to **RSLogix 5** or your PLC program documentation.

## Routing Through a Gateway

PCCC PLCs without built-in Ethernet (or behind a ControlLogix backplane) can be reached through a gateway using a CIP connection path. Common scenarios:

- SLC 500 behind a 1756-DHRIO module in a ControlLogix chassis
- PLC-5 on DH+ reached via a 1756-DHRIO bridge
- MicroLogix 1000 behind an ENI adapter

```go
cfg := &driver.PLCConfig{
    Name:           "remote_slc",
    Address:        "192.168.1.10",       // Gateway IP (e.g., ControlLogix Ethernet module)
    Family:         driver.FamilySLC500,
    ConnectionPath: "1,0,2,192.168.2.50", // Route through gateway to target
    Enabled:        true,
}
```

The `ConnectionPath` uses the same Rockwell-style route format as Logix connections: comma-separated pairs of `port,address`. Each pair describes one hop in the route.

**Note:** Routing through gateways has not been extensively tested. If you encounter issues, try connecting directly to the PLC's Ethernet port first to rule out routing problems.

## Device Information

```go
info, err := drv.GetDeviceInfo()
if err != nil {
    log.Fatal(err)
}

fmt.Printf("Family:  %s\n", info.Family)       // "slc500", "plc5", or "micrologix"
fmt.Printf("Model:   %s\n", info.Model)         // Product name from ListIdentity
fmt.Printf("Version: %s\n", info.Version)       // Firmware revision (major.minor)
fmt.Printf("Serial:  %s\n", info.SerialNumber)  // 8-hex-digit serial
```

Device information is retrieved via an EtherNet/IP ListIdentity request, which all Ethernet-equipped PCCC PLCs support.

## Connection Behavior

- PCCC uses **unconnected messaging** only (EIP SendRRData). There is no Forward Open or CIP connected session.
- Each read or write is an independent request/response &mdash; there is no session state beyond the EIP registration.
- The `Keepalive()` method sends an EIP NOP packet to prevent TCP idle timeouts. Call it periodically (every 30&ndash;60 seconds) if you maintain long-lived connections without regular reads.
- Connection loss is detected on the next read/write attempt.

## Key Differences from Logix

If you're familiar with the Logix driver, these are the important differences when working with PCCC PLCs:

| Feature | Logix | PCCC (SLC/PLC-5/MicroLogix) |
|---|---|---|
| Tag names | Symbolic (e.g., `MyTag`) | Address-based (e.g., `N7:0`) |
| Type hints | Not needed | Not needed (type from address prefix) |
| Tag discovery | Automatic | SLC/MicroLogix: file directory; PLC-5: none |
| Connection mode | Connected (Forward Open) or Unconnected | Unconnected only |
| Batch reads | CIP Multi-Service Packet | Individual requests per address |
| UDT/structures | Automatic decode | Timer/Counter/Control maps |
| Slot configuration | Required | Not used |

## Advanced: Direct Client Access

For operations not exposed through the `Driver` interface, you can access the underlying PCCC client:

```go
adapter := drv.(*driver.PCCCAdapter)
client := adapter.Client() // *pccc.Client

// The client provides typed Read/Write with automatic value decoding
// and raw PLC-level access if needed
```

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Connection refused | PLC not reachable, wrong IP, or no Ethernet port | Verify IP with ping, confirm PLC has Ethernet capability |
| Timeout on connect | Wrong IP or port, firewall blocking TCP 44818 | Check network path, open port 44818 |
| PCCC status error (0x10, "Illegal command or format") | Address doesn't exist in PLC or wrong file type | Verify address exists in RSLogix, check file type and number |
| PCCC status error (0x0F, "Address out of range") | Element number exceeds file size | Check maximum element number for the file in RSLogix |
| Wrong values returned | Reading wrong file number or element | Double-check address against RSLogix data table configuration |
| Bit writes lost | Read-modify-write race with PLC scan | Use dedicated words for bit-level command/status instead of shared bit files |
| Routing errors | Bad connection path or gateway unreachable | Test direct connection first, verify path in RSLinx |
| "nil response" error | PLC didn't respond to the request | Check PLC mode (should be Run or Remote Run), verify connectivity |
