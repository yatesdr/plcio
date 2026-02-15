# Beckhoff TwinCAT (ADS)

plcio supports Beckhoff TwinCAT 2 and TwinCAT 3 PLCs using the ADS (Automation Device Specification) protocol.

## Supported Hardware

| Platform | TwinCAT Version | AMS Port | Tested |
|---|---|---|---|
| CX series (CX9020) | TwinCAT 3 | 851 | Yes |
| CX series | TwinCAT 2 | 801 | No |
| IPC / Embedded PC | TwinCAT 3 | 851 | No |

**Status: Stable, but requires more real-world testing before production use.**

**Default port:** TCP 48898

## Connection Setup

```go
cfg := &driver.PLCConfig{
    Name:     "beckhoff_plc",
    Address:  "192.168.1.40",
    Family:   driver.FamilyBeckhoff,
    AmsNetId: "192.168.1.40.1.1",  // Target AMS Net ID
    AmsPort:  851,                  // 851 for TwinCAT 3, 801 for TwinCAT 2
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

### AMS Net ID

Every TwinCAT device has a unique AMS Net ID in the format `x.x.x.x.x.x`. By default, it is derived from the IP address with `.1.1` appended:

```
IP: 192.168.1.40  →  AMS Net ID: 192.168.1.40.1.1
```

You can find the actual AMS Net ID in:
- **TwinCAT XAE:** Right-click the PLC project > Properties > AMS Net Id
- **TwinCAT System Manager:** System > Routes
- **Command line:** On the TwinCAT device, run `AdsInfo`

### AMS Port

| TwinCAT Version | Default Port | Description |
|---|---|---|
| TwinCAT 2 | 801 | PLC runtime 1 |
| TwinCAT 3 | 851 | PLC runtime 1 |
| TwinCAT 3 | 852 | PLC runtime 2 (if configured) |

### ADS Route Configuration

For plcio to connect, the TwinCAT system must have an ADS route configured for your client machine. There are two approaches:

**Option 1: Static route (recommended)**

On the TwinCAT device, add a static route via TwinCAT System Manager or `TcAdsRouting`:
- Remote AMS Net ID: `<your_client_ip>.1.1`
- Remote IP: `<your_client_ip>`
- Transport: TCP

**Option 2: Auto-route discovery**

plcio's network discovery uses UDP broadcast which can locate Beckhoff devices even without pre-configured routes. However, for reliable ongoing communication, a static route is recommended.

## Reading Tags

Beckhoff PLCs use symbolic tag names matching the variable declarations in your TwinCAT project:

```go
results, err := drv.Read([]driver.TagRequest{
    {Name: "MAIN.counter"},
    {Name: "MAIN.temperature"},
    {Name: "MAIN.motor_running"},
    {Name: "GVL.system_status"},
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

### Tag Naming Convention

Tags follow the TwinCAT variable naming:

```
<POU_Name>.<Variable_Name>
```

Common patterns:
- `MAIN.myVar` &mdash; Variable in MAIN program
- `GVL.globalVar` &mdash; Global Variable List
- `MAIN.myStruct.member` &mdash; Structure member access
- `MAIN.myArray[0]` &mdash; Array element

### Supported Data Types

| ADS Type | Code | Go Type | Size | Byte Order |
|---|---|---|---|---|
| BOOL | 0x21 | `bool` | 1 byte | N/A |
| BYTE | 0x11 | `uint8` | 1 byte | N/A |
| SINT | 0x10 | `int8` | 1 byte | N/A |
| WORD | 0x12 | `uint16` | 2 bytes | Little-endian |
| INT | 0x02 | `int16` | 2 bytes | Little-endian |
| DWORD | 0x13 | `uint32` | 4 bytes | Little-endian |
| DINT | 0x03 | `int32` | 4 bytes | Little-endian |
| LWORD | 0x15 | `uint64` | 8 bytes | Little-endian |
| LINT | 0x14 | `int64` | 8 bytes | Little-endian |
| REAL | 0x04 | `float32` | 4 bytes | Little-endian |
| LREAL | 0x05 | `float64` | 8 bytes | Little-endian |
| STRING | 0x1E | `string` | Variable | N/A |
| WSTRING | 0x1F | `string` | Variable | Little-endian |
| TIME | - | `uint32` (ms) | 4 bytes | Little-endian |
| LTIME | - | `uint64` (ns) | 8 bytes | Little-endian |
| DATE | - | `uint32` | 4 bytes | Little-endian |
| DATE_AND_TIME | - | `uint32` | 4 bytes | Little-endian |

**Note:** Beckhoff uses **little-endian** byte order (native x86), unlike Siemens S7 which uses big-endian.

## Writing Tags

```go
// Write an integer
err := drv.Write("MAIN.counter", 100)

// Write a float
err = drv.Write("MAIN.setpoint", 72.5)

// Write a boolean
err = drv.Write("MAIN.start_cmd", true)

// Write a string
err = drv.Write("MAIN.message", "Hello TwinCAT")
```

**Write limitations:**
- Single-tag operations only
- Not optimized for high-throughput writing
- Intended for acknowledgments, status codes, and occasional parameter updates

## Tag Discovery

TwinCAT PLCs support full symbol discovery:

```go
if drv.SupportsDiscovery() {
    tags, err := drv.AllTags()
    if err != nil {
        log.Fatal(err)
    }

    for _, tag := range tags {
        fmt.Printf("%-40s  Type: %-10s  Writable: %v\n",
            tag.Name, tag.TypeName, tag.Writable)
    }
}

// List POU names (MAIN, GVL, etc.)
programs, _ := drv.Programs()
for _, p := range programs {
    fmt.Println("POU:", p)
}
```

Symbols are discovered from the TwinCAT symbol table. The driver caches symbol handles for efficient repeated reads.

## Connection Behavior

- ADS uses TCP on port 48898
- No explicit keep-alive is needed (TCP keep-alive handles it)
- The `Keepalive()` method is a no-op for ADS
- Symbol handles are cached after first resolution for efficient repeated reads
- Connection loss is detected on the next read/write attempt

## Device Information

```go
info, err := drv.GetDeviceInfo()
if err != nil {
    log.Fatal(err)
}

fmt.Printf("Model: %s\n", info.Model)           // e.g., "CX9020"
fmt.Printf("Version: %s\n", info.Version)        // TwinCAT version
fmt.Printf("Description: %s\n", info.Description) // "TwinCAT PLC"
```

## Network Discovery

Beckhoff devices can be discovered via two methods that run in parallel:

1. **UDP Broadcast** &mdash; Sends ADS discovery broadcast packets
2. **TCP Port Scan** &mdash; Scans for devices accepting TCP on port 48898

```go
// Discover all PLCs (includes ADS discovery)
devices := driver.DiscoverAll("255.255.255.255", "192.168.1.0/24", 500*time.Millisecond, 20)

for _, dev := range devices {
    if dev.Family == driver.FamilyBeckhoff {
        fmt.Printf("Beckhoff at %s (AMS: %s, TwinCAT: %s)\n",
            dev.IP, dev.Extra["amsNetId"], dev.Extra["tcVersion"])
    }
}
```

Discovery results include:
- AMS Net ID
- Hostname
- TwinCAT version
- Whether an ADS route exists (`hasRoute`)

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Connection refused | No ADS route or firewall | Add ADS route on TwinCAT device, check firewall port 48898 |
| ADS error 0x0706 | Target port not found | Check AMS Port (851 for TC3, 801 for TC2) |
| Symbol not found | Variable doesn't exist or wrong name | Check exact variable name in TwinCAT project (case-sensitive) |
| Timeout | AMS Net ID incorrect | Verify AMS Net ID matches the target device |
| Discovery finds device but can't connect | No ADS route configured | Add static route on TwinCAT device for your client |
| Values seem wrong | Byte order issue (unlikely) | ADS handles byte order natively; check data type in TwinCAT |
| PLC in Config mode | Runtime not started | Start TwinCAT runtime, put PLC in RUN mode |
