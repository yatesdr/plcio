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

The slot number depends on the CPU model:

| CPU Family | Rack | Slot |
|---|---|---|
| S7-300 | 0 | 2 |
| S7-400 | 0 | 2 (or 3, depending on config) |
| S7-1200 | 0 | 0 |
| S7-1500 | 0 | 0 |

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

### Data Block Addressing

```
DB<block_number>.<byte_offset>
```

Examples:
- `DB1.0` &mdash; Data Block 1, starting at byte 0
- `DB1.4` &mdash; Data Block 1, starting at byte 4
- `DB100.10` &mdash; Data Block 100, starting at byte 10

### Bit Addressing

For BOOL types within a byte:
```
DB<block>.<byte>.<bit>
```

Examples:
- `DB1.0.0` &mdash; DB1, byte 0, bit 0
- `DB1.0.7` &mdash; DB1, byte 0, bit 7
- `M100.0` &mdash; Merker byte 100, bit 0

### Merker/Flag Addressing

```
M<byte_offset>[.<bit>]
```

Examples:
- `M0` &mdash; Merker byte 0
- `M100.0` &mdash; Merker byte 100, bit 0
- `MW10` &mdash; Merker word at byte 10

## Reading Tags

S7 reads **require type hints** because the protocol works at the byte level and doesn't carry type information:

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

| Type Hint | S7 Type | Go Type | Size | Byte Order |
|---|---|---|---|---|
| `BOOL` | BOOL | `bool` | 1 bit | N/A |
| `BYTE` | BYTE | `uint8` | 1 byte | N/A |
| `SINT` | SINT | `int8` | 1 byte | N/A |
| `CHAR` | CHAR | `string` (1 char) | 1 byte | N/A |
| `WORD` | WORD | `uint16` | 2 bytes | Big-endian |
| `INT` | INT | `int16` | 2 bytes | Big-endian |
| `DWORD` | DWORD | `uint32` | 4 bytes | Big-endian |
| `DINT` | DINT | `int32` | 4 bytes | Big-endian |
| `REAL` | REAL | `float32` | 4 bytes | Big-endian |
| `LWORD` | LWORD | `uint64` | 8 bytes | Big-endian |
| `LINT` | LINT | `int64` | 8 bytes | Big-endian |
| `LREAL` | LREAL | `float64` | 8 bytes | Big-endian |
| `STRING` | STRING | `string` | Variable | N/A |
| `WSTRING` | WSTRING | `string` | Variable | Big-endian |

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
// Write a DINT value
err := drv.Write("DB1.0", 42)

// Write a REAL value
err = drv.Write("DB1.4", 3.14)

// Write a BOOL
err = drv.Write("DB1.12.0", true)

// Write a STRING
err = drv.Write("DB1.20", "Hello")
```

The S7 adapter looks up the configured `DataType` for the tag to determine the correct wire format. Make sure the tag is in your `PLCConfig.Tags` list with the correct `DataType` field for writes to work properly.

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

The device info is retrieved via the SZL (System Status List) mechanism and returns the CPU module type, firmware version, and serial number.

## Connection Behavior

- S7 uses a standard TCP connection on port 102
- No explicit keep-alive is needed (TCP keep-alive handles connection maintenance)
- The `Keepalive()` method is a no-op for S7
- Connection loss is detected on the next read/write attempt
- The S7 protocol negotiates a PDU size during connection setup, which determines the maximum read size per request

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Connection refused on port 102 | PLC not reachable or S7 access disabled | Check network, enable PUT/GET access in TIA Portal |
| Wrong values returned | Byte offset or type hint incorrect | Verify offsets in TIA Portal DB editor, check type hint matches |
| Access denied | Optimized block access enabled | Disable "Optimized block access" on the Data Block in TIA Portal |
| Timeout | Wrong rack/slot or network issue | Check rack/slot for your CPU model, verify network path |
| BOOL reads wrong bit | Bit index incorrect | S7 bits are numbered 0-7 within a byte, verify bit position |
| Connection drops periodically | Firewall or network equipment timeout | Check firewall rules, ensure TCP keep-alive is not being blocked |
