# Allen-Bradley (Logix & Micro800)

plcio supports Allen-Bradley ControlLogix, CompactLogix, and Micro800 series PLCs over EtherNet/IP using the Common Industrial Protocol (CIP).

## Supported Hardware

| Series | Models Tested | Protocol | Connection Mode |
|---|---|---|---|
| ControlLogix | L7, L8 | EtherNet/IP (CIP) | Connected (Forward Open) or Unconnected |
| CompactLogix | L3x | EtherNet/IP (CIP) | Connected (Forward Open) or Unconnected |
| Micro800 | Micro820 | EtherNet/IP (CIP) | Unconnected only |

**Default port:** TCP 44818

## Connection Setup

### ControlLogix / CompactLogix

```go
cfg := &driver.PLCConfig{
    Name:    "logix_plc",
    Address: "192.168.1.10",    // IP address or hostname
    Family:  driver.FamilyLogix,
    Slot:    0,                  // CPU slot number
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

**Slot configuration:**
- **CompactLogix:** Always slot 0
- **ControlLogix:** Depends on chassis configuration. The CPU is typically in slot 0, but check your chassis layout. If the Ethernet module is separate from the CPU, you need the CPU's slot number, not the Ethernet module's.

### Micro800

```go
cfg := &driver.PLCConfig{
    Name:    "micro820",
    Address: "192.168.1.20",
    Family:  driver.FamilyMicro800,
    Enabled: true,
}
```

Micro800 uses unconnected messaging only (no Forward Open). Batch reads are not supported; each tag is read individually.

## Reading Tags

Allen-Bradley PLCs use symbolic tag names. Tags are read by name:

```go
// Read a single tag
results, err := drv.Read([]driver.TagRequest{
    {Name: "MyDINT"},
})
if err != nil {
    log.Fatal(err)
}
fmt.Printf("%s = %v\n", results[0].Name, results[0].Value)

// Read multiple tags (batched automatically on Logix)
results, err = drv.Read([]driver.TagRequest{
    {Name: "Counter"},
    {Name: "Temperature"},
    {Name: "StatusWord"},
    {Name: "Program:MainProgram.LocalVar"},
})
```

### Supported Data Types

| CIP Type | Code | Go Type | Size |
|---|---|---|---|
| BOOL | 0x00C1 | `bool` | 1 byte |
| SINT | 0x00C2 | `int8` | 1 byte |
| INT | 0x00C3 | `int16` | 2 bytes |
| DINT | 0x00C4 | `int32` | 4 bytes |
| LINT | 0x00C5 | `int64` | 8 bytes |
| USINT | 0x00C6 | `uint8` | 1 byte |
| UINT | 0x00C7 | `uint16` | 2 bytes |
| UDINT | 0x00C8 | `uint32` | 4 bytes |
| ULINT | 0x00C9 | `uint64` | 8 bytes |
| REAL | 0x00CA | `float32` | 4 bytes |
| LREAL | 0x00CB | `float64` | 8 bytes |
| STRING | 0x00D0 | `string` | 82 bytes |
| DWORD | 0x00D3 | `uint32` | 4 bytes |

### Reading Structures (UDTs)

When reading a structure/UDT tag, plcio automatically decodes member values using the PLC's template definitions:

```go
results, err := drv.Read([]driver.TagRequest{
    {Name: "MyUDT_Instance"},
})

// Value is returned as map[string]interface{}
if m, ok := results[0].Value.(map[string]interface{}); ok {
    fmt.Printf("Member1 = %v\n", m["Member1"])
    fmt.Printf("Member2 = %v\n", m["Member2"])
}
```

### Reading Arrays

```go
// Read an array element
results, _ := drv.Read([]driver.TagRequest{
    {Name: "MyArray[0]"},
    {Name: "MyArray[5]"},
})

// Read a range of array elements (Logix returns contiguous elements)
results, _ = drv.Read([]driver.TagRequest{
    {Name: "MyArray[0]"}, // Count is set based on discovered array dimensions
})
```

### Reading Program-Scoped Tags

```go
results, _ := drv.Read([]driver.TagRequest{
    {Name: "Program:MainProgram.LocalCounter"},
    {Name: "Program:Comms.HeartbeatFlag"},
})
```

## Writing Tags

```go
// Write an integer
err := drv.Write("MyDINT", 42)

// Write a float
err = drv.Write("Temperature_SP", 72.5)

// Write a boolean
err = drv.Write("StartCommand", true)

// Write a string
err = drv.Write("MessageTag", "Hello PLC")
```

**Write limitations:**
- Writes are single-tag, single-value operations
- Not optimized for high-throughput writing
- Intended for acknowledgments, status codes, and occasional parameter updates
- No transactional/atomic multi-tag writes

**Recommended write types:** DINT is the most reliable type for status codes and acknowledgments across all PLC families.

## Tag Discovery

Logix and Micro800 PLCs support automatic tag discovery:

```go
if drv.SupportsDiscovery() {
    tags, err := drv.AllTags()
    if err != nil {
        log.Fatal(err)
    }

    for _, tag := range tags {
        fmt.Printf("%-40s  Type: %-10s  Writable: %v\n",
            tag.Name, tag.TypeName, tag.Writable)
        if len(tag.Dimensions) > 0 {
            fmt.Printf("  Array dimensions: %v\n", tag.Dimensions)
        }
    }
}

// List program names
programs, _ := drv.Programs()
for _, p := range programs {
    fmt.Println("Program:", p)
}
```

**Requirements for discovery:**
- PLC must be in **Run** or **Remote Run** mode
- Network path to PLC must be open on TCP 44818
- Discovery returns controller-scoped and program-scoped tags

## Batch Read Optimization

On ControlLogix/CompactLogix, plcio uses CIP Multiple Service Packet requests to batch multiple tag reads into a single network round-trip. This is automatic and transparent.

**Connection modes:**
- **Connected messaging (Forward Open):** Preferred mode. Supports larger payloads (up to 4002 bytes) and CIP batching.
- **Unconnected messaging:** Fallback mode if Forward Open fails. Smaller payload limit (504 bytes).

Micro800 does **not** support Forward Open or batch reads.

## Connection Keep-alive

Logix connections using Forward Open require periodic keep-alive messages. The driver handles this via the `Keepalive()` method. Your application should call this periodically (every 10-30 seconds) if you maintain long-lived connections without regular reads.

```go
// In a monitoring loop, reads serve as implicit keep-alive.
// If idle, call Keepalive() explicitly:
if err := drv.Keepalive(); err != nil {
    log.Printf("Keepalive failed: %v", err)
    // Consider reconnecting
}
```

## Advanced: Direct Client Access

For operations not exposed through the `Driver` interface, you can access the underlying client:

```go
adapter := drv.(*driver.LogixAdapter)
client := adapter.Client()

// Resolve a tag's type code from the symbol table
typeCode, found := adapter.ResolveTagType("MyTag")

// Get member types for a structure
memberTypes := adapter.GetMemberTypes(typeCode)
for name, typeName := range memberTypes {
    fmt.Printf("  %s: %s\n", name, typeName)
}
```

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Connection refused | PLC not reachable or wrong IP | Verify network connectivity, check firewall |
| Forward Open rejected | Slot number wrong or too many connections | Check slot number, reduce concurrent connections |
| Tag not found | Misspelled name or tag doesn't exist | Check spelling (case-sensitive), verify tag exists in PLC program |
| Empty discovery results | PLC not in Run mode | Put PLC in Run or Remote Run mode |
| Timeout on read | Network congestion or PLC overloaded | Increase timeout, reduce poll rate |
| Structure returns raw bytes | Template not cached | Read the tag once to cache the template, then subsequent reads will decode |
