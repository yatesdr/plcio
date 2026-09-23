# Siemens S7

plcio supports Siemens S7-300, S7-400, S7-1200, and S7-1500 PLCs using the S7comm protocol over TCP port 102.

## Supported Hardware

| Series | Slot | Protocol | Tested |
|---|---|---|---|
| S7-300 | Rack 0, Slot 2 | S7comm | No |
| S7-400 | Rack 0, Slot 2 | S7comm | No |
| S7-1200 | Rack 0, Slot 0 | S7comm | Yes |
| S7-1500 | Rack 0, Slot 0 | S7comm | No |

**Default port:** TCP 102

## Connection Setup

```go
cfg := &driver.PLCConfig{
    Name:    "s7_plc",
    Address: "192.168.1.30",
    Family:  driver.FamilyS7,
    Slot:    0,     // Slot 0 for S7-1200/1500, Slot 2 for S7-300/400
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

### Slot Configuration

The slot number depends on the CPU model (slot must be 0-31; values outside
the range fail `Connect`):

| CPU Family | Rack | Slot |
|---|---|---|
| S7-300 | 0 | 2 |
| S7-400 | 0 | 2 (or 3, depending on config) |
| S7-1200 | 0 | 0 |
| S7-1500 | 0 | 0 |

The unified driver always uses **rack 0**: `PLCConfig` has no rack field
because its layout is frozen by the v0.3.0 compatibility contract (unkeyed
literals). For a CPU in another rack use the native client:
`s7.Connect(addr, s7.WithRackSlot(rack, slot))` (rack 0-7, slot 0-31; anything
else is rejected before dialling).

### S7-1200/1500 Access Requirements

For S7-1200 and S7-1500 PLCs, you must configure access permissions in the TIA Portal project:

1. Open the PLC properties in TIA Portal
2. Navigate to **Protection & Security > Connection mechanisms**
3. Enable **Permit access with PUT/GET communication from remote partner**
4. For Data Blocks you want to read, disable **Optimized block access** (use standard/S7-300/400 compatible access)

Without these settings, connections will be rejected or reads will fail.

## Tag Addressing

S7 PLCs use **address-based** tag names. There is no symbolic tag browsing; you must know the memory addresses of the data you want to read.

### Address Format

```
<area><offset>[.<bit>]
```

**Memory areas:**

| Area | Prefix | Description |
|---|---|---|
| Data Block | `DB<n>.` | Data blocks (most common) |
| Merker/Flags | `M` | Flag memory |
| Input | `I` | Process inputs |
| Output | `Q` | Process outputs |
| S5 timer | `T<n>` | S7-300/400 timers (not on S7-1200/1500) |
| S5 counter | `C<n>` | S7-300/400 counters (not on S7-1200/1500) |

### Data Block Addressing

```
DB<block_number>.<byte_offset>
```

Examples:
- `DB1.0` &mdash; Data Block 1, starting at byte 0
- `DB1.4` &mdash; Data Block 1, starting at byte 4
- `DB100.10` &mdash; Data Block 100, starting at byte 10
- `DB1.302[6]` &mdash; array of 6 elements of the configured type starting at byte 302

These offset-only addresses carry no size, so the type always comes from the
type hint / configured `DataType` (reads default to `DINT` when there is
neither; writes require a type).

Sized forms encode the width in the address itself. Without a hint they use
the type shown below; a hint (or configured `DataType`) of **the same width**
selects how the bytes are interpreted, e.g. `DB1.DBD16` with `REAL` is a REAL
(sent with REAL transport), with `TIME` a TIME. A hint of a different width
(`DB1.DBW20` + `DINT`, `DB1.DBB12` + `BOOL`, a bit address + anything but
`BOOL`) or an unknown hint fails the read/write instead of being ignored.
Same-width types: B = BYTE/USINT/SINT/CHAR; W = WORD/UINT/INT/WCHAR/DATE/
S5TIME; D = DWORD/UDINT/DINT/REAL/TIME/TIME_OF_DAY; L = LWORD/ULINT/LINT/
LREAL/DATE_AND_TIME.

| Form | Meaning |
|---|---|
| `DB1.DBX12.0` | bit 0 of byte 12 (BOOL) |
| `DB1.DBB12` | byte 12 (BYTE) |
| `DB1.DBW12` | word at byte 12 (WORD) |
| `DB1.DBD12` | double word at byte 12 (DWORD) |
| `DB1.DBL12` | 8 bytes at byte 12 (LINT) |

DB numbers must be 0-65535 and byte offsets 0-2097151 (the limits of the
S7ANY address); larger values are rejected when the address is parsed.

### Bit Addressing

For BOOL types within a byte:
```
DB<block>.<byte>.<bit>
```

Examples:
- `DB1.0.0` &mdash; DB1, byte 0, bit 0 (same as `DB1.DBX0.0`)
- `DB1.0.7` &mdash; DB1, byte 0, bit 7
- `M100.0` &mdash; Merker byte 100, bit 0

A bit address reads the single addressed bit, so no type hint is needed.

### Merker/Flag Addressing

```
M<byte_offset>[.<bit>]
M{B|W|D|L}<byte_offset>
```

Examples:
- `M0` &mdash; Merker **bit** M0.0 (a bare `M<n>` is bit 0 of byte n, not the byte)
- `M100.0` &mdash; Merker byte 100, bit 0
- `MB0` &mdash; Merker byte 0
- `MW10` &mdash; Merker word at byte 10
- `MD20` &mdash; Merker double word at byte 20

Inputs and outputs use the same forms with `I` / `Q` (`I0.0`, `IB0`, `IW0`,
`ID0`, `Q0.0`, `QB0`, `QW0`, `QD0`).

### Timers and Counters (S7-300/400)

`T<n>` / `C<n>` (n = 0-65535) address one S5 timer / counter word. As in
Snap7 (`Cli_TMRead`/`Cli_CTRead`), the request uses area 0x1D / 0x1C with
transport size TIMER (0x1D) / COUNTER (0x1C), the element count, and the
timer/counter number itself as the address; writes send the data as an
OCTET STRING. The raw 16-bit word is returned as `uint64`; use the hint
`S5TIME` to decode a timer word to milliseconds. S7-1200/1500 CPUs have no S5
timers/counters and answer with an error. (Not yet verified on S7-300/400
hardware.)

## Reading Tags

S7 reads **need a type** because the protocol works at the byte level and
doesn't carry type information. An explicit `TypeHint` wins; when it is empty
the adapter uses the tag's configured `DataType` from `PLCConfig.Tags` (same
case-insensitive name match as `Write`), so reads and writes of a configured
tag always agree on its width:

```go
results, err := drv.Read([]driver.TagRequest{
    {Name: "DB1.0",   TypeHint: "DINT"},   // 4-byte signed integer at DB1, byte 0
    {Name: "DB1.4",   TypeHint: "REAL"},   // 4-byte float at DB1, byte 4
    {Name: "DB1.8",   TypeHint: "INT"},    // 2-byte signed integer at DB1, byte 8
    {Name: "DB1.10",  TypeHint: "WORD"},   // 2-byte unsigned at DB1, byte 10
    {Name: "DB1.12.0", TypeHint: "BOOL"},  // Single bit at DB1, byte 12, bit 0
    {Name: "M100.0",  TypeHint: "BOOL"},   // Merker bit
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

### Supported Data Types

The table describes unified driver values. Native protocol APIs retain their
existing representations and wire widths. Primitive arrays use typed slices;
record members follow the same widened categories. Canonical numeric Write inputs
are checked against the target width without routing integers through floating
point; existing time and text semantics are preserved. See
[compatibility changes](plcio-compatibility.md).

| Type Hint | S7 Type | Go Type | Size | Byte Order |
|---|---|---|---|---|
| `BOOL` | BOOL | `bool` | 1 bit | N/A |
| `BYTE` | BYTE | `uint64` | 1 byte | N/A |
| `SINT` | SINT | `int64` | 1 byte | N/A |
| `CHAR` | CHAR | `uint64` character code | 1 byte | N/A |
| `WORD` | WORD | `uint64` | 2 bytes | Big-endian |
| `INT` | INT | `int64` | 2 bytes | Big-endian |
| `DWORD` | DWORD | `uint64` | 4 bytes | Big-endian |
| `DINT` | DINT | `int64` | 4 bytes | Big-endian |
| `REAL` | REAL | `float64` | 4 bytes | Big-endian |
| `LWORD` | LWORD | `uint64` | 8 bytes | Big-endian |
| `LINT` | LINT | `int64` | 8 bytes | Big-endian |
| `LREAL` | LREAL | `float64` | 8 bytes | Big-endian |
| `STRING` | STRING | `string` | Variable | N/A |
| `WSTRING` | WSTRING | `string` (UTF-16, surrogate pairs) | Variable | Big-endian |
| `TIME` | TIME | `int64` signed milliseconds | 4 bytes | Big-endian |
| `TIME_OF_DAY` / `TOD` | TIME_OF_DAY | `int64` ms since midnight | 4 bytes | Big-endian |
| `DATE` | DATE | `int64` days since 1990-01-01 | 2 bytes | Big-endian |
| `S5TIME` | S5TIME | `int64` milliseconds | 2 bytes | BCD + time base |
| `DATE_AND_TIME` / `DT` | DATE_AND_TIME | `time.Time` (UTC) | 8 bytes | BCD |
| `DTL` | DTL | `time.Time` (UTC) | 12 bytes | Big-endian |

Also accepted: `USINT` (= BYTE), `UINT` (= WORD), `UDINT` (= DWORD), `ULINT`
(= LWORD), `WCHAR`, and any name with a `[]` suffix. Type names are
case-insensitive. An unrecognised type hint (for example `FLOAT`, `LTIME`,
`LDT`) fails that tag with an "unknown S7 data type" error instead of silently
reading a DINT.

**Date/time types.** TIME arrays decode to `[]int64`. DATE_AND_TIME and DTL
carry no time zone: reads return the PLC's wall clock as a UTC `time.Time`;
writes store the wall-clock fields of the given `time.Time` in its own
location (and the correct weekday byte). A stored S5TIME / DATE_AND_TIME / DTL
that is not valid (bad BCD, impossible date) fails the tag instead of
returning a plausible wrong value. Writes are strict and never round:

| Type | Accepted values | Rejected |
|---|---|---|
| TIME | integer ms, integral float ms, `time.Duration` | outside int32 ms, sub-ms durations, fractions |
| TIME_OF_DAY | integer ms, `time.Duration` | outside 0..86399999 ms, sub-ms |
| DATE | integer days, `time.Time` at midnight | outside 1990-01-01..2168-12-31, a time-of-day part |
| S5TIME | integer ms, `time.Duration` (finest exact time base is chosen) | negative, > 2h46m30s, not a multiple of the base (e.g. 10005 ms) |
| DATE_AND_TIME | `time.Time` | outside 1990..2089, sub-ms precision |
| DTL | `time.Time` (ns precision) | outside 1970-01-01..2262-04-11 23:47:16.854775807 |

**BOOL arrays** (`DB1.6[16]` with type `BOOL`) are read as packed bits, 8 per
byte starting at bit 0 of the offset, and returned as `[]bool`. Writing a BOOL
array is not supported; write individual bits by bit address instead.

**Important:** S7 uses **big-endian** byte order for all multi-byte types. plcio handles the conversion automatically.

### Byte Offset Planning

When reading from Data Blocks, you need to know the byte offset of each variable. In TIA Portal, you can view the offset in the Data Block editor:

```
DB1 Layout Example:
  Offset 0:  MyDINT    (DINT, 4 bytes)    → DB1.0  TypeHint: DINT
  Offset 4:  MyREAL    (REAL, 4 bytes)    → DB1.4  TypeHint: REAL
  Offset 8:  MyINT     (INT, 2 bytes)     → DB1.8  TypeHint: INT
  Offset 10: MyWORD    (WORD, 2 bytes)    → DB1.10 TypeHint: WORD
  Offset 12: MyBOOL1   (BOOL, bit 0)     → DB1.12.0 TypeHint: BOOL
  Offset 12: MyBOOL2   (BOOL, bit 1)     → DB1.12.1 TypeHint: BOOL
```

## Writing Tags

```go
// With PLCConfig.Tags containing DB1.0 = DINT, DB1.4 = REAL, DB1.20 = STRING:
err := drv.Write("DB1.0", 42)       // DINT, 4 bytes
err = drv.Write("DB1.4", 3.14)      // REAL, 4 bytes
err = drv.Write("DB1.20", "Hello")  // STRING, sized from the DB header

// Addresses that encode their own size need no configuration:
err = drv.Write("DB1.12.0", true)   // single bit
err = drv.Write("DB1.DBW30", 7)     // WORD, 2 bytes
err = drv.Write("DB1.DBD8", 7)      // integer to an unconfigured D address is written as DWORD
// A fractional float to an unconfigured D address is rejected; configure the
// tag as REAL (DataType "REAL" for "DB1.DBD8") to write it as a REAL.
```

The S7 adapter looks up the configured `DataType` for the tag to determine the
correct wire format. An offset-only address such as `DB1.4` has no size, so a
write to it **fails** unless the tag is in `PLCConfig.Tags` with a recognised
`DataType` (with the native `s7.Client`, use `WriteWithType`). The value is
never used to guess the width, because guessing 8 bytes for a Go `int64` or
`float64` would overwrite neighbouring variables.

Writes whose encoded size exceeds the target (element size x array count) are
rejected before anything is sent. STRING and WSTRING writes first read the
string header from the PLC to learn the declared maximum length; the write
fails if that header cannot be read or declares more than 254 (STRING) or
16382 (WSTRING) characters, which usually means the offset is wrong. A string
longer than the declared maximum is **rejected, not truncated** (for STRING
arrays, per element). WSTRING lengths count UTF-16 code units, so a character
outside the Basic Multilingual Plane (e.g. an emoji) counts as two.

**Multi-PDU writes are not atomic.** A value larger than one PDU (long
strings, large arrays) is written in several S7 requests. If a later request
fails, the earlier chunks stay written in the PLC; the error states how many
bytes (and which byte range) were already written, e.g. `chunk write at
offset 445 failed after 445 of 800 bytes were already written (multi-PDU
writes are not atomic; ...)`. Re-write the whole value after fixing the cause.

**Write limitations:**
- Single-tag operations only
- Not optimized for high-throughput writing
- Intended for acknowledgments and status codes
- Large strings are automatically chunked into multiple S7 protocol writes

## Tag Discovery

S7 PLCs **do not support** tag discovery or symbol table browsing over the S7comm protocol. You must configure tags manually with their addresses and type hints.

## Device Information

```go
info, err := drv.GetDeviceInfo()
if err != nil {
    log.Fatal(err)
}

fmt.Printf("Model: %s\n", info.Model)
fmt.Printf("Version: %s\n", info.Version)
fmt.Printf("Serial: %s\n", info.SerialNumber)
```

Reading CPU identity via SZL is not implemented yet: `GetDeviceInfo` currently returns a placeholder model (`S7 PLC`) with empty version and serial number.

## Connection Behavior

- S7 uses a standard TCP connection on port 102
- `Keepalive()` performs one cheap round trip (UserData read of SZL 0x0424,
  CPU status, one 20-byte record). Any well-formed answer counts as alive,
  including a CPU refusing that SZL. It returns an error when not connected and
  an error matching `s7.ErrConnectionLost` when the round trip fails
- Connection loss is otherwise detected on the next read/write attempt
- Every response must answer the request that was sent (same PDU reference,
  message type, function code and item count). A mismatch is a protocol error
  (`s7.ErrProtocol`, also matching `s7.ErrConnectionLost`) and marks the
  connection lost, like an I/O error, so the caller reconnects
- The S7 protocol negotiates a PDU size during connection setup, which determines the maximum read size per request. A negotiated size below 240 bytes is rejected as a broken peer
- Several tags are packed into one read request when they fit the PDU; a failing tag (for example a non-existent DB) only fails that tag, and the other tags in the batch still get their own values
- `Reconnect` uses the timeout configured at connect time; `Close` during a reconnect leaves the client closed

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Connection refused on port 102 | PLC not reachable or S7 access disabled | Check network, enable PUT/GET access in TIA Portal |
| Wrong values returned | Byte offset or type hint incorrect | Verify offsets in TIA Portal DB editor, check type hint matches |
| Access denied | Optimized block access enabled | Disable "Optimized block access" on the Data Block in TIA Portal |
| Timeout | Wrong rack/slot or network issue | Check rack/slot for your CPU model, verify network path |
| BOOL reads wrong bit | Bit index incorrect | S7 bits are numbered 0-7 within a byte, verify bit position |
| Connection drops periodically | Firewall or network equipment timeout | Check firewall rules, ensure TCP keep-alive is not being blocked |
