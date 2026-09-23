# Allen-Bradley SLC 500, PLC-5 & MicroLogix (PCCC)

plcio supports Allen-Bradley SLC 500, PLC-5, and MicroLogix processors using the PCCC (Programmable Controller Communication Commands) protocol tunneled over EtherNet/IP.

## Supported Hardware

| Series | Models | Protocol | Connection Mode | Tested |
|---|---|---|---|---|
| SLC 500 | SLC 5/03, 5/04, 5/05 | PCCC over EtherNet/IP | Unconnected | SLC 5/05 |
| PLC-5 | PLC-5/20E, 5/40E, 5/80E | PCCC over EtherNet/IP | Unconnected | No (experimental) |
| MicroLogix | 1100, 1200, 1400, 1500 | PCCC over EtherNet/IP | Unconnected | MicroLogix 1400 |

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

**PLC-5 support is experimental: it has not been tested against hardware.** PLC-5 uses its own command set, as given in the DF1 Protocol and Command Set Reference Manual (1770-6.5.16):

- **Reads** use Typed Read (CMD 0x0F, FNC 0x68). **Writes** use Typed Write (FNC 0x67). Addresses are sent in PLC-5 logical binary form (for example, `B3:300` is sent as `06 03 FF 2C 01`). Data is preceded by the DF1 type/data parameter.
- A logical binary address does not carry a file type. plcio checks each reply's type/data parameter against the address letter instead. Reading `F7:0` when file 7 is an integer file is an error. So is reading `N4:0` from a timer file. The value is never mis-decoded.
- Each write is preceded by a Typed Read of the same address. That read supplies the exact type/data parameter the processor reported, and plcio sends it back unchanged, as libplctag does. As a result, a PLC-5 write takes two round trips. A write whose size does not match the file (for example, a float into an integer file) is refused before anything is sent.
- **Bit writes** use Read-Modify-Write (FNC 0x26). The processor applies the AND/OR masks to the word itself (see [Bit Writes](#bit-writes)).
- **I/O addresses are octal**, as in RSLogix 5: `I:010/17` is input word 8, bit 15. Digits 8 and 9 are rejected in `I:` and `O:` word and bit numbers. All other numbers stay decimal (`N7:10` is element 10). SLC 500 and MicroLogix addresses are always decimal.
- **Floats:** the PLC-5 sends the high 16-bit word of a float first. libplctag verified this against hardware. plcio swaps the two words so values decode as normal IEEE 754.
- `L` (long) files do not exist on PLC-5 and are rejected. `MG` and `PD` elements are a different size on PLC-5 and are not supported. File directory discovery is not available.

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

The table describes unified driver values. Native protocol APIs retain their
existing representations and wire widths. Primitive arrays use typed slices;
record members follow the same widened categories. Canonical numeric Write inputs
are checked against the target width without routing integers through floating
point; existing time and text semantics are preserved. See
[compatibility changes](plcio-compatibility.md).

| File Type | Go Type | Notes |
|---|---|---|
| N (Integer) | `int64` | 16-bit signed |
| F (Float) | `float64` | IEEE 754 |
| L (Long) | `int64` | 32-bit signed |
| B, O, I, S, A (16-bit word) | `int64` | When reading the full word |
| Bit access (`/N`) | `bool` | Single bit extracted from word |
| T (Timer, full) | `map[string]interface{}` | Keys: `PRE`, `ACC`, `EN`, `TT`, `DN` |
| C (Counter, full) | `map[string]interface{}` | Keys: `PRE`, `ACC`, `CU`, `CD`, `DN`, `OV`, `UN` |
| R (Control, full) | `map[string]interface{}` | Keys: `LEN`, `POS`, `EN`, `EU`, `DN`, `EM`, `ER`, `UL`, `IN`, `FD` |
| T/C/R sub-element (PRE, ACC, LEN, POS) | `int64` | Individual 16-bit sub-element |
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

### Batch Reads

When the `Driver.Read()` method receives multiple addresses from the same data file with consecutive element numbers, plcio automatically batches them into a single PCCC round-trip. For example, reading `N7:0`, `N7:1`, `N7:2` issues one PCCC command requesting 6 bytes instead of three separate commands.

**What gets batched:**
- Full-element reads in the same data file with consecutive element numbers
- All simple types (N, F, L, B, O, I, S, A) and complex types (T, C, R, ST)

**What is always read individually:**
- Sub-element access (e.g., `T4:0.ACC`, `C5:2.PRE`)
- Bit access (e.g., `B3:0/5`, `N7:0/3`)
- Addresses from different data files

Each batch is capped at 236 data bytes on SLC 500 and MicroLogix. This is the DF1 manual's limit for SLC 5/03 and 5/04 protected typed logical reads, and no reference gives a smaller limit for the 5/03. For 16-bit integer files, that means up to 118 elements per batch. On PLC-5 the cap is 234 bytes (117 words). A Typed Read carries at most 240 bytes, and the type/data parameter counts toward that limit. If a batch read fails, the affected elements automatically fall back to individual reads.

Batching is transparent &mdash; you use the same `Read()` API and the driver handles grouping internally. The order of results always matches the order of requests.

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

On **SLC 500 and MicroLogix**, writing a single bit (e.g., `B3:0/5`, `T4:0.EN`) sends one Protected Typed Logical Write with Mask (FNC 0xAB) with only that bit set in the mask. The processor changes only that bit, so bits it updates itself (counter CU/CD, one-shot storage bits) are never overwritten. Nothing is read first.

Bit writes are accepted for 16-bit word files (O, I, S, B, N, A) and Timer/Counter/Control status bits. Bit writes to L (long) files return an error because the masked write is only 16 bits wide; write the whole long instead. Bit writes to F (float) and ST (string) files return an error.

On **PLC-5** (which does not support FNC 0xAB), a bit write sends one Read-Modify-Write (CMD 0x0F, FNC 0x26). To set a bit, the AND mask is `0xFFFF` and the OR mask is the bit. To clear a bit, the AND mask is the inverted bit and the OR mask is `0`. The processor applies the masks to the current word, so plcio never writes back a stale copy of the other bits. Timer, counter and control bits (for example `T4:0.DN` or `T4:0/13`) address sub-element 0, the control word.

### Write Value Ranges

Out-of-range values return an error and are never truncated:

- **16-bit words** (N, B, S, A, O, I): -32768..65535. Values 32768..65535 are stored as their 16-bit two's-complement pattern, so `0xFFFF` can be written to a B or S word (it reads back from N files as -1).
- **Timer `.PRE` / `.ACC`**: 0..32767. A negative timer preset or accumulator faults SLC processors.
- **Counter `.PRE` / `.ACC`, Control `.LEN` / `.POS`**: -32768..32767.
- **L (long)**: -2147483648..2147483647 (a `uint32` argument is written as its bit pattern).
- Floating-point input for integer files must be a whole number.
- **ST (string)**: at most 82 characters. The full 84-byte element is written: length, characters stored byte-swapped within each 16-bit word the way SLC processors hold them, and zero fill.

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
| SLC 500 (5/03, 5/04, 5/05: 1747-L53x/L54x/L55x) | Supported | File directory read |
| MicroLogix 1100 (1763), 1400 (1766) | Supported | File directory read |
| MicroLogix 1000, 1200, 1500; SLC 5/01, 5/02 | Not supported | Directory layout not verified; returns `pccc.ErrDiscoveryNotSupported` |
| PLC-5 | Not supported | PLC-5 does not expose a file directory |

### How It Works

Discovery follows the procedure pycomm3's `SLCDriver.get_file_directory` uses:

1. **Diagnostic Status** (CMD 0x06, FNC 0x03) &mdash; retrieves the processor catalog string (e.g., "1747-L552") to select the file directory layout. If that fails, the catalog is taken from the ListIdentity product name.
2. **Directory size** (CMD 0x0F, FNC 0xA1, Protected Typed Logical Read with 2 address fields) &mdash; reads the size in bytes of system file 0.
3. **Directory contents** (FNC 0xA1) &mdash; reads all of system file 0 in 80-byte chunks, then parses each row: the file type code, and the file size in bytes, divided by the element size to give the element count.

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
| Batch reads | CIP Multi-Service Packet | Contiguous element batching (automatic) |
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
| PCCC status error (STS=0xF0 with an EXT_STS code) | Processor-specific failure, such as an element beyond the end of the file | Read the extended status text. Check the address against RSLogix |
| Wrong values returned | Reading wrong file number or element | Double-check address against RSLogix data table configuration |
| "typed reply ... check the file type letter" (PLC-5) | The address letter does not match the file's actual type. PLC-5 addresses carry no file type, so the processor answers with whatever the file holds | Use the letter RSLogix 5 shows for that file number |
| "reply has N data bytes, expected M" error | The processor returned a different number of data bytes than requested | Check the address and element size. No value is decoded from a reply of the wrong length |
| "reply TNS ... does not match" error | A reply belonged to a different (e.g. timed-out) request | Retry. Frequent occurrences point to a network or gateway problem |

PCCC status failures can be classified in code with `errors.As(err, &se)` where `se` is a `*pccc.StatusError` (fields `STS` and `EXTSTS`).
| Routing errors | Bad connection path or gateway unreachable | Test direct connection first, verify path in RSLinx |
| "nil response" error | PLC didn't respond to the request | Check PLC mode (should be Run or Remote Run), verify connectivity |
