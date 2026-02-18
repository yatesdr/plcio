# Troubleshooting

Common issues, error patterns, and solutions when using plcio.

## Connection Issues

### Connection Refused

**Symptom:** `connect: connection refused` or `dial tcp ... : connection refused`

**Causes and fixes:**

| PLC Family | Likely Cause | Fix |
|---|---|---|
| All | Wrong IP address or PLC powered off | Verify IP with ping, check PLC power |
| All | Firewall blocking the port | Open required port (44818, 102, 48898, 9600) |
| Logix | Too many concurrent connections | Close unused connections, check PLC connection limit |
| SLC/PLC-5/MicroLogix | PLC has no Ethernet port | Only Ethernet-equipped models supported (SLC 5/03+, MicroLogix 1100+). Use a gateway for non-Ethernet models. |
| S7 | PUT/GET access not enabled | Enable in TIA Portal: Protection & Security > Connection mechanisms |
| Beckhoff | No ADS route configured | Add static route on TwinCAT device |
| Omron FINS | FINS ethernet unit not configured | Configure FINS settings in CX-Programmer |

### Connection Timeout

**Symptom:** `i/o timeout` or `connection timed out`

**Causes:**
- Network latency or routing issues
- Wrong port number
- PLC in a different VLAN/subnet
- AMS Net ID incorrect (Beckhoff)

**Fix:**
```go
// Increase timeout
cfg := &driver.PLCConfig{
    Timeout: 10 * time.Second, // Default is typically 5s
    // ...
}
```

### Connection Drops / Resets

**Symptom:** `connection reset by peer`, `broken pipe`, or `EOF` during reads

**Causes:**
- PLC mode change (Run → Stop → Run)
- PLC firmware update or restart
- Network switch reconfiguration
- CIP Forward Open connection timeout (Logix)

**Fix:**
- Check `IsConnectionError()` and implement reconnection logic:

```go
results, err := drv.Read(requests)
if err != nil && drv.IsConnectionError(err) {
    drv.Close()
    time.Sleep(2 * time.Second)
    if err := drv.Connect(); err != nil {
        log.Printf("Reconnect failed: %v", err)
    }
}
```

- For Logix PLCs, call `Keepalive()` periodically if the connection is idle

---

## Read Issues

### Tag Not Found

**Symptom:** Per-tag error indicating tag doesn't exist

**Fixes by family:**

| Family | Fix |
|---|---|
| Logix | Check exact tag name spelling (case-sensitive). Verify tag exists in PLC program. Use `AllTags()` to list available tags. |
| Micro800 | Same as Logix. Ensure PLC is in Run mode. |
| SLC/PLC-5/MicroLogix | Verify file number and element exist in RSLogix. Check address format (e.g., `N7:0`, `F8:5`). Confirm file type prefix matches the data table type. |
| S7 | Verify DB number exists and byte offset is within the DB size. |
| Beckhoff | Check exact symbol name. Ensure PLC is in RUN mode with valid program. |
| Omron FINS | Verify memory area and address are valid for your PLC model. |
| Omron EIP | Check exact tag name (case-sensitive). |

### Wrong Values Returned

**Symptom:** Values don't match what you see in the PLC programming software

**Common causes:**

1. **Wrong type hint (S7, FINS):** A DINT read as INT will give wrong values
   ```go
   // Wrong: reading 4-byte value as 2-byte type
   {Name: "DB1.0", TypeHint: "INT"}   // Should be "DINT" if the variable is DINT
   ```

2. **Wrong byte offset (S7):** Off by even one byte will shift all values
   ```go
   // Verify offset in TIA Portal DB editor
   {Name: "DB1.0", TypeHint: "DINT"}   // Byte 0, 4 bytes
   {Name: "DB1.4", TypeHint: "REAL"}   // Byte 4, not byte 2!
   ```

3. **Byte order confusion:** S7 and FINS use big-endian; ADS uses little-endian. plcio handles conversion automatically, but if you're working with raw bytes, be aware of the order.

### Empty Discovery Results

**Symptom:** `AllTags()` returns an empty list

**Fixes:**

| Family | Fix |
|---|---|
| Logix | Put PLC in Run or Remote Run mode. Check that your connection slot is correct. |
| Micro800 | Put PLC in Run mode. May need a power cycle. |
| Beckhoff | PLC must be in RUN mode with a valid downloaded program. |
| Omron EIP | Tags exist but UDT members won't be listed (known limitation). |
| SLC/PLC-5/MicroLogix | These families do not support discovery. Configure addresses manually from RSLogix. |
| S7 / Omron FINS | These families do not support discovery. Configure tags manually. |

### Structure/UDT Values

**Symptom:** Structure tags return raw bytes instead of decoded values

**Logix:** On first read, the driver fetches and caches the structure template. Subsequent reads decode correctly. If still seeing raw bytes:
- The UDT may have changed since the template was cached &mdash; reconnect
- The structure may be too complex &mdash; read individual members instead

**Omron EIP:** Structure member unpacking is not supported. Structures appear as opaque types.

**S7:** S7 does not return type information. Use the correct type hint for each address.

---

## Write Issues

### Write Has No Effect

**Symptom:** Write returns no error, but PLC value doesn't change

**Causes:**
1. **PLC logic overwrites the value:** PLC program writes to the same tag every scan cycle, overwriting your value immediately
   - **Fix:** Use dedicated write-back tags that PLC logic only reads
2. **Wrong data type:** Wrote a float where PLC expects integer
   - **Fix:** Match the exact PLC data type
3. **Tag is read-only:** Some tags/areas cannot be written
   - **Fix:** Check write permissions in PLC programming software

### Write Type Mismatch (S7, FINS)

**Symptom:** Write returns an error or PLC receives garbage data

**Fix:** Ensure the tag's `DataType` is correctly configured in `PLCConfig.Tags`:

```go
cfg := &driver.PLCConfig{
    Tags: []driver.TagSelection{
        {Name: "DB1.16", DataType: "DWORD", Enabled: true, Writable: true},
    },
}
```

The S7 and Omron adapters look up the `DataType` from the tag configuration to determine the wire format for writes.

---

## Network Discovery Issues

### No Devices Found

**Causes and fixes:**

1. **UDP broadcast blocked:** Many networks/switches block UDP broadcasts
   - Try using a subnet-specific broadcast address instead of `255.255.255.255`
   - Use `GetBroadcastAddresses()` to find the right address

2. **Wrong CIDR for port scan:** The CIDR must include the PLCs' subnet
   - Use `GetLocalSubnets()` to auto-detect

3. **Firewall rules:** Industrial firewalls may block discovery ports
   - EIP: UDP 44818
   - ADS: UDP + TCP 48898
   - S7: TCP 102
   - FINS: TCP 9600

4. **PLCs on different VLAN:** Discovery doesn't cross VLAN boundaries
   - Configure routing or discover within each VLAN separately

### Duplicate Devices in Results

This shouldn't happen &mdash; plcio deduplicates by IP address. If you see duplicates, you may be running multiple discoveries with overlapping subnets. The deduplication prefers EIP results over other protocols.

---

## Performance Issues

### Slow Read Cycles

**Causes:**
1. **Too many individual reads:** Pass all tags in a single `Read()` call to enable batching
   ```go
   // Slow: N network round-trips
   for _, tag := range tags {
       drv.Read([]driver.TagRequest{{Name: tag}})
   }

   // Fast: 1-2 round-trips (batched)
   requests := make([]driver.TagRequest, len(tags))
   for i, tag := range tags {
       requests[i] = driver.TagRequest{Name: tag}
   }
   drv.Read(requests)
   ```

2. **Unconnected messaging (Logix):** Forward Open connection wasn't established, limiting payload size
   - Check `ConnectionMode()` output
   - Verify slot number is correct

3. **Network latency:** PLCs on remote subnets or through VPNs
   - Place the plcio host as close to PLCs as possible on the network

### High CPU Usage

**Cause:** Poll rate too aggressive for the number of tags

**Fix:** Increase `PollRate` in configuration. For most monitoring use cases, 500ms to 5s is sufficient.

---

## Connection Error Detection

plcio provides `IsLikelyConnectionError()` to help distinguish transient errors from connection loss. Errors classified as connection errors:

- `io.EOF` (connection closed by remote)
- Any `net.Error` (network-level errors)
- `ECONNRESET`, `ECONNREFUSED`, `EPIPE`, `ECONNABORTED`
- Messages containing: "connection refused", "broken pipe", "i/o timeout", "no route to host", "forcibly closed", etc.

Use this for reconnection logic:

```go
if drv.IsConnectionError(err) {
    // Connection is dead - need to reconnect
    drv.Close()
    // ... reconnect with backoff ...
} else {
    // Transient error - can retry the operation
}
```

---

## Debug Logging

plcio includes a debug logging system that writes protocol-level details to `debug.log`:

```go
import "github.com/yatesdr/plcio/logging"

// Logging is protocol-filtered
// Available filters: "omron", "fins", "eip", "ads", "logix", "pccc", "s7"
```

When reporting issues, include the relevant debug.log output for the protocol in question.

---

## Getting Help

If you've worked through this guide and still have issues:

1. Check the PLC-specific documentation: [Allen-Bradley Logix](allen-bradley.md), [Allen-Bradley PCCC](allen-bradley-pccc.md), [Siemens S7](siemens-s7.md), [Beckhoff](beckhoff.md), [Omron](omron.md)
2. Enable debug logging and review the protocol-level output
3. Test connectivity with the PLC vendor's own software first (RSLogix, TIA Portal, TwinCAT XAE, CX-Programmer) to rule out PLC-side configuration issues
4. File an issue on GitHub with:
   - PLC family and firmware version
   - plcio version
   - Minimal reproduction code
   - Debug log output
   - Network topology (VLANs, firewalls between host and PLC)
