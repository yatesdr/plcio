# EtherNet/IP Adapter (`plcio/eipadapter`)

The `eipadapter` package implements the **adapter** (server) side of EtherNet/IP. In CIP terms, the scanner is the originator (typically a PLC) that opens connections and reads/writes data; the adapter is the target — the device being scanned. Where the rest of plcio lets your Go program *talk to* a PLC, `eipadapter` lets your Go program *be talked to by* a PLC.

> **Not safety-rated.** Standard EtherNet/IP is not CIP Safety. Do not use this package as a permissive output in a safety function. See [Safety and Intended Use](safety-and-intended-use.md).

## When to use it

- Smart sensors, cameras, and vision systems that need to feed data into a PLC's I/O scan.
- Bench fixtures that simulate field devices so you can develop ladder before the real hardware exists.
- Edge processes that present derived data to the control system in a familiar I/O-tree form.

If you need to *read* tags from a PLC, use the scanner-side drivers (`logix`, `s7`, `ads`, etc.). They're a different shape.

## Quick start

```go
package main

import (
    "context"
    "log"
    "os"
    "os/signal"
    "time"

    "github.com/yatesdr/plcio/eipadapter"
)

func main() {
    input := eipadapter.NewAssembly(101, eipadapter.AssemblyInput, 16)
    output := eipadapter.NewAssembly(102, eipadapter.AssemblyOutput, 4)
    config := eipadapter.NewAssembly(103, eipadapter.AssemblyConfig, 0)

    adp, err := eipadapter.New(eipadapter.Config{
        Identity: eipadapter.Identity{
            VendorID:     0x1337,
            DeviceType:   0x000C, // Generic Device
            ProductCode:  1,
            RevMajor:     1, RevMinor: 0,
            SerialNumber: 0xC0FFEE01,
            ProductName:  "MyDevice",
            State:        0x03, // Operational
        },
        Assemblies: []*eipadapter.Assembly{input, output, config},
    })
    if err != nil {
        log.Fatal(err)
    }

    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
    defer cancel()
    go adp.Serve(ctx)

    output.OnChange(func(old, new []byte) {
        log.Printf("scanner wrote: %x", new)
    })

    tick := time.NewTicker(50 * time.Millisecond)
    defer tick.Stop()
    var heartbeat byte
    for {
        select {
        case <-ctx.Done():
            return
        case <-tick.C:
            heartbeat++
            input.SetByte(0, heartbeat)
        }
    }
}
```

## Identity

The Identity Object (Class 0x01) is how PLCs and discovery tools identify your device. The fields you set here appear in RSLinx Who Active and in Studio 5000's "Add Device" dialog.

```go
Identity{
    VendorID:     0x1337,  // ODVA-assigned in production; any value works for dev
    DeviceType:   0x000C,  // Generic Device — accepted by AB scanners
    ProductCode:  1,
    RevMajor:     1, RevMinor: 0,
    SerialNumber: 0xC0FFEE01,
    ProductName:  "MyDevice",
    State:        0x03,    // 0x03 = Operational, 0xFF = Default
}
```

`IP` and `Port` are auto-populated at `Serve` time from your machine's outbound IPv4. If you need to override (multi-homed hosts), set them on the Identity before calling `New`.

## Assemblies

Assemblies are fixed-size byte buffers exposed as CIP Class 0x04 objects. Scanners reference them in their connection path during Forward_Open and then read/write the bytes cyclically.

```go
input  := eipadapter.NewAssembly(101, eipadapter.AssemblyInput,  16) // T->O
output := eipadapter.NewAssembly(102, eipadapter.AssemblyOutput, 4)  // O->T
config := eipadapter.NewAssembly(103, eipadapter.AssemblyConfig, 0)  // explicit-only
```

| Direction | Meaning | Polled by |
|---|---|---|
| `AssemblyInput`  | T→O — your application produces these bytes | Scanner cyclically reads |
| `AssemblyOutput` | O→T — scanner sends these bytes to you | Your `OnChange` callback fires |
| `AssemblyConfig` | Delivered during Forward_Open in the connection path | Explicit messaging only |

Instance numbering is up to you. The Allen-Bradley generic Ethernet device defaults (`Input=101, Output=102, Config=103`) are a reasonable convention.

### Updating input data

```go
input.SetBytes(0, []byte{statusByte, heartbeat, 0, 0})
input.SetByte(5, someFlag)
b := input.GetByte(5)
copy := input.Bytes()
```

`SetBytes` / `SetByte` are concurrent-safe. The producer goroutine takes a fresh snapshot at every RPI tick, so concurrent writes from your application can happen at any time without coordinating.

### Reacting to output writes

```go
output.OnChange(func(old, new []byte) {
    if old[0] != new[0] {
        log.Printf("command byte changed: %02X -> %02X", old[0], new[0])
    }
})
```

The callback runs in the same goroutine that handled the scanner write. Don't block — copy the data and post to a channel if you need significant work.

## Forward_Open behaviour

By default, the adapter accepts any well-formed Forward_Open whose connection path references existing assemblies. If you need to gate connections (e.g., reject when not ready), supply an `OnForwardOpen` callback:

```go
Config{
    // ...
    OnForwardOpen: func(c *eipadapter.ForwardOpenContext) error {
        if !systemReady() {
            return fmt.Errorf("not ready")
        }
        log.Printf("scanner opening: consume=%d produce=%d RPI=%dus",
            c.ConsumeInstance, c.ProduceInstance, c.Request.TORPI)
        return nil
    },
}
```

Returning an error rejects the connection with status `0x01` (Connection Failure). The PLC will see this as a connection fault.

### Connection paths

The adapter understands the two common scanner conventions:

```
20 04 24 80 2C 65 2C 66   = Class 4, Config=none (0x80), O->T=101 (0x65), T->O=102 (0x66)
20 04 24 67 2C 65 2C 66   = Class 4, Config=103 (0x67), O->T=101, T->O=102
```

Single-direction connections are supported (omit one of the connection-point segments). Input-only adapters like a vision sensor naturally produce a single connection point.

## Wiring up to a PLC

### Allen-Bradley (Studio 5000)

1. Add an Ethernet/IP module under your CIP scanner.
2. Type: **Generic Ethernet Module** (or ETHERNET-MODULE in some firmware).
3. Configure:
   - **Input Assembly Instance**: matches your `AssemblyInput` instance ID
   - **Input Size**: matches your input assembly size, in *32-bit ints* (so a 16-byte assembly = 4 INTs of size 4)
   - **Output Assembly Instance / Size**: same pattern for output (or set size 0 for input-only)
   - **Configuration Assembly Instance**: matches `AssemblyConfig` (or use a placeholder if you have no config)
   - **Comm Format**: `Data - INT` or `Data - SINT` — pick to match how you want to address it in ladder
   - **IP Address**: the host running your adapter
4. Set the RPI (50 ms is typical for vision/sensor data). Don't go below 5 ms.

After download, the module status should turn green. `Cam:I.Data[0..N]` will contain the bytes from your input assembly.

### Siemens (S7-1500)

S7 supports being a scanner of EtherNet/IP via the CM 1xxx-1 EtherNet/IP communication module, configured in TIA Portal under HW Config. The connection-path semantics are the same; refer to the CM module manual for the GSD configuration steps.

## Diagnostics

The adapter uses `plcio/logging` for protocol traces. Enable it the same way as the scanner drivers:

```go
import "github.com/yatesdr/plcio/logging"

l, _ := logging.NewDebugLogger("eipadapter.log")
logging.SetGlobalDebugLogger(l)
```

Watch for:

- `RegisterSession granted 0x... to ...` — scanner connected over TCP
- `Forward_Open accepted: O->T=... T->O=... ...` — Class 1 connection opened
- `producer start conn ... RPI=...` — cyclic I/O producer started
- `producer conn ... timed out (no inbound)` — the scanner stopped sending O→T packets within the connection timeout

## Limitations

- **Class 0 (broadcast) connections** are not implemented. Class 1 (cyclic) and Class 3 (server) are.
- **Multicast T→O** is not implemented; the producer unicasts to the peer learned from the first O→T packet. This is the common case for AB-style point-to-point connections.
- **Listen-only connections** are not specifically optimised but should work as a degenerate Class 1.
- **CIP Safety** is not implemented and is out of scope.
- **EDS files** are not generated. Most modern scanners do not require an EDS for generic device modules.
- **Run/Idle headers** on T→O are not added; the assembly bytes are sent as-is. O→T Run/Idle headers (4 bytes prefix) are auto-detected on inbound packets and stripped.
- **Single-host binding** — the adapter binds one TCP and two UDP sockets. Run multiple instances on different ports if you need to simulate multiple devices.
