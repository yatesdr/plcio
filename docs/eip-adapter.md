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

`IP` and `Port` are auto-populated by `New`. When `Config.BindAddr` is a specific IPv4 address (e.g. the OT-network NIC on a multi-homed PC) that address is advertised; otherwise the machine's default-route IPv4 is used. Set them on the Identity before calling `New` to override. The TCP/IP Interface object (Class 0xF5) reports the same IP with its interface's real netmask (0.0.0.0 if unknown) and `os.Hostname()` as the host name, all captured once at startup. Per CIP Vol 2 (TCP/IP Interface object), attribute 5 encodes the addresses as little-endian UDINTs and the (empty) domain name as a STRING (2-byte length, padded to even length); attribute 6 (host name, truncated to 64 characters) is a STRING too. Attribute 2 (Configuration Capability) is 0: the IP configuration belongs to the host OS and cannot be changed over CIP.

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

While a Class 1 connection owns an output assembly, explicit `Set_Attribute_Single` writes to it are rejected with status `0x0C` (Object State Conflict), and a second connection trying to consume it is rejected with extended status `0x0106` (ownership conflict). Zero-size assemblies (heartbeat connection points) are never owned.

### Connection loss and Run/Idle

The adapter does **not** clear or otherwise change an output assembly when its connection closes, times out, or the scanner switches to Idle — the last received bytes stay in place. Your application decides what "safe" means. To learn about these transitions, set `Config.OnConnectionEvent`:

```go
Config{
    // ...
    OnConnectionEvent: func(e eipadapter.ConnectionEvent) {
        switch e.Type {
        case eipadapter.ConnectionTimedOut, eipadapter.ConnectionClosed, eipadapter.ConnectionIdle:
            outputsToSafeState(e.ConsumeInstance)
        }
    },
}
```

Event types are `ConnectionOpened`, `ConnectionClosed` (Forward_Close or adapter shutdown), `ConnectionTimedOut` (no O→T packet within RPI × timeout multiplier, counted from the Forward_Open), and `ConnectionRun` / `ConnectionIdle` (the O→T 32-bit Run/Idle header's Run bit; reported for the first header and on every change). The callback runs synchronously on an adapter goroutine — don't block in it.

`output.RunIdle()` returns the current Run/Idle state `(run, ok)` for an output assembly; `ok` is false when no connection owns it or the owning connection hasn't delivered a Run/Idle header. Data in Idle packets is still written to the assembly, as before.

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

Before the callback runs, the adapter validates the request and rejects it with general status `0x01` and one of these extended statuses (CIP Vol 1, Connection Manager error codes):

| Extended status | Reason |
|---|---|
| `0x0100` | Duplicate Forward_Open (same connection serial + vendor ID + originator serial as an open connection) |
| `0x0103` | Transport class other than Class 1 |
| `0x0106` | Output assembly already owned by another connection |
| `0x0108` | O→T not point-to-point, or null T→O when the connection produces data |
| `0x0109` | Connection size doesn't match the assembly: T→O = size + 2 (sequence count); O→T = size + 2 (modeless) or size + 6 (with 32-bit Run/Idle header) |
| `0x0111` | RPI below 1 ms |
| `0x0113` | `Config.MaxConnections` (default 32) I/O connections already open |
| `0x0117` | Consume point is not an `AssemblyOutput`, or produce point is not an `AssemblyInput` |
| `0x0315` | Connection path unparseable or references an unknown assembly |

Connection IDs follow CIP Vol 1 3-5.4.1 (the consumer of a point-to-point connection chooses its ID; the producer of a multicast one): the reply keeps the originator's proposed O→T ID unless another connection already uses it, in which case the adapter picks a fresh one (the scanner must use the ID in the reply); a point-to-point T→O uses the originator's proposed T→O ID; a multicast T→O request (served unicast) gets an adapter-chosen T→O ID.

Accepted connections report the requested RPIs as the actual packet intervals — they are what the adapter uses. T→O data is sent to the IP address of the TCP peer that sent the Forward_Open, at UDP port 2222 (or the port from a T→O sockaddr item in the request). O→T packets are only accepted from that IP, and only if their 32-bit sequence number is newer than the last accepted one. Each connection's timeout watchdog starts at Forward_Open, so a connection that never receives O→T traffic times out.

A Forward_Close closes the connection with the same connection serial + vendor ID + originator serial. If there is none — including a Forward_Close naming another originator's connection — it is rejected with general status `0x01`, extended status `0x0107` (target connection not found). A matching Forward_Close from a different IP than the one that opened the connection is rejected with general status `0x0F` (privilege violation), as OpENer does.

Other limits: at most `Config.MaxTCPConnections` (default 64) concurrent TCP connections (extra connections are closed on accept), and one registered session per TCP connection (a second RegisterSession gets encapsulation status `0x0001`). `Close()` or cancelling the `Serve` context closes the listener, all TCP connections and UDP sockets and stops all I/O connections; `Serve` returns once everything has exited. A panic in a connection handler — including one in your callbacks — is recovered and logged (with stack) via `plcio/logging`, and only that connection is dropped.

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

- **Only Class 1 (cyclic) I/O connections** are implemented. Forward_Open must target the Assembly class (0x04) with transport class 1; Class 0 and Class 3 (connected explicit messaging) Forward_Opens are rejected. Explicit messaging works unconnected (SendRRData, including Unconnected_Send).
- **Multicast T→O** is not implemented. A Forward_Open requesting it is accepted and served unicast to the originator (see above), as in earlier releases. Point-to-point is the common case for AB-style connections.
- **Input-only / listen-only connections** name a heartbeat connection point for O→T (commonly 198 for input-only, 199 for listen-only). The adapter only accepts them if you register a zero-size `AssemblyOutput` at that instance (e.g. `NewAssembly(198, eipadapter.AssemblyOutput, 0)`); otherwise the Forward_Open is rejected with `0x0315`. Listen-only connections that ask for a multicast T→O are accepted and served unicast.
- **CIP Safety** is not implemented and is out of scope.
- **EDS files** are not generated. Most modern scanners do not require an EDS for generic device modules.
- **Run/Idle headers** on T→O are not added; the assembly bytes are sent as-is. O→T Run/Idle headers (4 bytes prefix) are expected when the O→T connection size is assembly size + 6, auto-detected for variable-size connections, and reported via `OnConnectionEvent` / `Assembly.RunIdle`.
- **Single-host binding** — the adapter binds one TCP and two UDP sockets. Run multiple instances on different ports if you need to simulate multiple devices.
