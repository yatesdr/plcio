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

### Protocol Setting

`Protocol` is case-insensitive and surrounding spaces are ignored:

| Value | Transport |
|---|---|
| `""` or `"fins"` | FINS/TCP, falling back to FINS/UDP |
| `"fins-tcp"` | FINS/TCP only |
| `"fins-udp"` | FINS/UDP only |
| `"eip"` | EtherNet/IP (NJ/NX) |

Any other value (for example `"tcp"`) makes `NewOmronAdapter` return an error;
earlier releases silently used FINS.

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
| TK | `TK` | Task flags `TK0`-`TK31` (area 0x06, address n, bit 0; BOOL only, no bit suffix) | Read only |
| Timer PV | `TIM` | Timer present value `TIM0`-`TIM4095` (area 0x89, address n) | Read/Write (word only) |
| Counter PV | `CNT` | Counter present value `CNT0`-`CNT4095` (area 0x89, address 0x8000+n) | Read/Write (word only) |

The one-letter aliases `W`, `H`, `A`, `D` and `E` are accepted for WR, HR, AR,
DM and the current EM bank. The bare prefixes **`C` and `T` are rejected** with
a parse error: in Omron notation they mean counter and timer, and earlier
releases silently mapped them to CIO (physical I/O) and the task-flag area. Use
`CIO`/`CNT` or `TK`/`TIM` explicitly. Timer/counter completion flags are not
supported; `TIM`/`CNT` addresses are word (present value) access only.

Word addresses must be 0-65535, and an element count (`[n]`) must be at least 1
and must not run past word 65535; out-of-range addresses are rejected rather
than clamped.

### Reading Tags (FINS)

FINS reads **require type hints** because the protocol operates on raw memory
addresses. Hints are case-insensitive. An unknown hint (e.g. `FLOAT32`) is an
error for that tag &mdash; earlier releases silently read it as `WORD` &mdash;
and a bit address (`DM100.5`, `TK3`) only accepts `BOOL`:

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

The table describes unified driver values. Native protocol APIs retain their
existing representations and wire widths. Primitive arrays use typed slices;
record members follow the same widened categories. Canonical numeric Write inputs
are checked against the target width without routing integers through floating
point; existing time and text semantics are preserved. See
[compatibility changes](plcio-compatibility.md).

| Type Hint | Go Type | Words | Byte Order |
|---|---|---|---|
| `BOOL` | `bool` | 1 bit | N/A |
| `BYTE` | `uint64` | 1 (partial) | N/A |
| `WORD` | `uint64` | 1 | Big-endian |
| `INT` / `INT16` | `int64` | 1 | Big-endian |
| `DWORD` | `uint64` | 2 | Big-endian words, low word first |
| `DINT` / `INT32` | `int64` | 2 | Big-endian words, low word first |
| `LWORD` | `uint64` | 4 | Big-endian words, low word first |
| `LINT` / `INT64` | `int64` | 4 | Big-endian words, low word first |
| `REAL` | `float64` | 2 | Big-endian words, low word first |
| `LREAL` | `float64` | 4 | Big-endian words, low word first |
| `STRING` | `string` | Variable | N/A |

Multi-word values follow the CS/CJ/CP memory layout: the least significant word
is at the lowest address. For example REAL `1.0` in `D100` is `D100=0x0000`,
`D101=0x3F80`, and DINT `0x12345678` is `D100=0x5678`, `D101=0x1234`.
(Releases before this fix swapped the words for all 32/64-bit types.)

### FINS Batch Optimization

plcio automatically optimizes FINS reads with multiple strategies:

1. **Contiguous address grouping** &mdash; Adjacent addresses in the same memory area are combined into a single FINS read (up to 999 words per read, the Ethernet limit in W342 section 5-2-2); a single tag larger than 999 words is read in several requests
2. **Multi-memory area read** &mdash; Scattered single-word addresses (4-64 of them) use FINS command 0x0104 for a single round-trip; every echoed area code is validated
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

Writes never extend past the extent the address declares (element size x `[count]`,
rounded up to whole words): writing a 10-element slice to `DM100[5]`, a 2-element
slice to `DM100`, or a 50-character string to a 20-byte `STRING` (`DM100[20]`)
returns an error instead of overwriting adjacent memory. A string that exactly
fills its buffer is written without a terminator. A single write is limited to
997 words, the MEMORY AREA WRITE (0102) limit over Ethernet (FINS/TCP and
FINS/UDP) in W342-E1-18 section 5-2-2: 6 bytes of area/address/count plus
1,994 data bytes fill the 2,000-byte command text, a 2,012-byte FINS frame.
Larger writes are rejected, not split (a split write is not atomic). Integer
values that do not fit the target type (e.g. `70000` for a `WORD`, `-1` for a
`DWORD`, `1.5` for a `DINT`) are rejected instead of being truncated.

### FINS End Codes

Per W342-E1-18 section 5-1-3, bit 7 (fatal CPU Unit error) and bit 6
(non-fatal CPU Unit error, e.g. a battery error: "the end code of a sent command
that is completed normally is 0040") are status flags, not failures. They are
masked off before the result is checked, so a PLC reporting a CPU error still
returns data. Bit 15 (network relay error) is always a failure: the response
then carries a relay-error word (error network, error node) instead of data,
reported in `FINSEndError.RelayNetwork`/`RelayNode`. Use
`omron.FINSEndCodeFlags` to inspect the flags; failures are returned as
`*omron.FINSEndError` with the masked code. The message table follows W342
5-1-3 (e.g. `0x3001` access right error, `0x4001` service aborted).

### FINS Transport

FINS supports both TCP and UDP transport:
- **TCP** (preferred): More reliable, handles larger payloads
- **UDP**: Lower latency, but limited payload size

plcio defaults to TCP and falls back to UDP. Because UDP is connectionless, a
UDP connect sends a read-only Controller Data Read (0x0501) and fails if no
valid FINS response arrives within the timeout, so `Connect` no longer succeeds
for a host that is not there. UDP responses are matched on SID, command code and
destination node; stray or stale datagrams are discarded. The UDP source node
defaults to the last octet of the local IPv4 address (the Ethernet Unit's
default automatic IP address conversion).

FINS/TCP follows W421 section 7-4-2: the client requests automatic node
allocation (client node 0), and the server's reply must carry client and
server nodes in 1..254. Header error codes (0x01 not 'FINS', 0x02 length too
long, 0x03 command not supported, 0x20 all connections in use, 0x21 node
already connected, 0x22 protected node, 0x23 client node out of range, 0x24
same node as server, 0x25 no node address available) are reported with their
W421 text. CONNECTION CONFIRMATION frames (command 6) are discarded as the
manual requires. Any framing error (bad magic, length outside 8..2,020,
unexpected command, mismatched response) marks the connection lost
(`omron.ErrConnectionLost`).

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

`STRING` variables use the NJ/NX CIP layout (2-byte little-endian length
followed by the characters); the length prefix is stripped on read and added on
write. The Omron `BYTE`/`WORD`/`DWORD`/`LWORD` type codes (0xD1-0xD4) decode to
unsigned integers and can be written like `USINT`/`UINT`/`UDINT`/`ULINT`.
Scalar `BOOL` writes send the W506 7-7-3 layout (status byte, forced byte 0);
`[]bool` writes send one status byte per element.

Time types (W506 7-7-1 codes; 8 bytes of little-endian nanoseconds, ranges
from W501 section 6-3):

| NJ/NX type | CIP code | Read value | Write accepts |
|---|---|---|---|
| `TIME` | 0xDB or 0x09 | `int64` nanoseconds (signed) | `time.Duration` or integer ns |
| `TIME_OF_DAY` | 0x0B | `int64` ns since midnight | `time.Duration` or integer ns in [0, 24h) |
| `DATE` | 0x08 | `time.Time` (UTC, midnight) | `time.Time` at midnight or integer ns, 1970-01-01..2106-02-06 |
| `DATE_AND_TIME` | 0x0A | `time.Time` (UTC) | `time.Time` or integer ns, 1970-01-01..2106-02-06 23:59:59.999999999 |

The controller clock has no time zone: `DATE`/`DATE_AND_TIME` count the
controller's wall clock, so they are returned as a UTC `time.Time` with that
wall clock, and a written `time.Time` stores its wall clock in its own location
(as the S7 driver does). Out-of-range values are rejected on write and
reported as a per-tag error on read. Enumerations (0x07) read as `int32`;
`UINT/UDINT/ULINT BCD` (0x04-0x06) and unions (0x0C) are returned as raw bytes.
The Omron vendor codes 0x04-0x0C are reported as `0x0100 | code`
(`omron.TypeOmronDate` etc.) so they cannot be confused with the FINS type codes.

Structure reads strip the 2-byte structure CRC (AddInfo) that precedes the data
(W506 7-6-1). A read answered with CIP status 0x06 (partial transfer: the value
did not fit in one reply), a reply shorter than its type, or a `STRING` whose
length prefix exceeds the data is a per-tag error, not a truncated value.

If the connection drops during `AllTags`, the error wraps
`omron.ErrConnectionLost` (any tags found so far are returned with it by the
`omron` client).

### Known Limitations (EIP)

- Structure/UDT members cannot be browsed or read individually
- Structures cannot be written (the write needs the structure CRC)
- Values larger than one CIP reply (large `STRING`s or arrays read whole) are
  reported as errors; fragmented reads are not implemented
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
