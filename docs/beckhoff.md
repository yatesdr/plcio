# Beckhoff TwinCAT (ADS)

The ordinary unified driver connects, browses symbols, reads decoded values and
writes values by their published symbol path. It resolves ADS metadata internally;
callers do not need a separate typed-read mode or an application type registry.

```go
cfg := &driver.PLCConfig{
    Address:  "192.168.5.212:48898", // TCP endpoint
    Family:   driver.FamilyBeckhoff,
    AmsNetId: "5.45.219.226.1.1",   // Independent target AMS identity
    AmsPort:  851,                  // TwinCAT 3 runtime
    Timeout:  5 * time.Second,
}
drv, err := driver.Create(cfg)
if err != nil { return err }
if err := drv.Connect(); err != nil { return err }
defer drv.Close()

tags, err := drv.AllTags()
if err != nil { return err }
values, readErr := drv.Read([]driver.TagRequest{
    {Name: "MAIN.test_struct"},
    {Name: "MAIN.test_2d_dint_array_style1"},
    {Name: "MAIN.test_struct.my_dint"},
})
for _, value := range values {
    if value.Error != nil {
        fmt.Printf("%s: %v\n", value.Name, value.Error)
        continue
    }
    fmt.Printf("%s = %#v\n", value.Name, value.Value)
}
if readErr != nil { return readErr } // Earlier successful slots remain available.
_ = tags
```

A reachable TCP host does not establish an ADS route. Configure the PLC's existing
route for the local AMS identity and source IP. plcio does not install routes,
control the runtime or deploy a PLC program. TCP defaults to port 48898; target
runtime AMS port defaults to 851. TwinCAT 2 commonly uses AMS port 801.
Without `AmsNetId` the target NetID is assumed to be the IP plus `.1.1`; when it
differs, set it explicitly. Discovery reports the real value in
`Extra["amsNetId"]` (see [Discovery](#discovery-and-validation-status)).

For a configured local route, use the additive adapter constructor:

```go
adapter, err := driver.NewADSAdapterWithOptions(cfg,
    ads.WithLocalAmsNetId("192.168.5.10.1.1"),
    ads.WithLocalAmsPort(32900),
    ads.WithTimeout(5*time.Second),
)
```

The original `NewADSAdapter(cfg)` signature is unchanged. With no explicit local
identity, the client derives it from the actual local IPv4 TCP address. A hostname
or IPv6 TCP endpoint requires an explicit target AMS identity; a local IPv6
connection also needs an explicit local identity. IPv6 subnet discovery is unsupported.
Reconnect dials the original TCP endpoint and retains all identity and resource
options. It verifies device identity before publishing the new connection.
Calling the adapter's `Connect` again first closes the previous client (releasing
its handles), because the TwinCAT router may reject a second connection from the
same AMS Net ID; until the new session is verified the adapter reports not connected.

## Values and metadata

Successful unified `TagValue.Value` and `StableValue` use these categories:

| PLC declaration | Go value | Storage |
|---|---|---|
| BOOL / BIT | `bool` | Native byte or published packed bit |
| SINT / INT / DINT / LINT | `int64` | Signed 8/16/32/64 bits |
| BYTE / WORD / DWORD / LWORD and unsigned integers | `uint64` | Unsigned 8/16/32/64 bits |
| REAL / LREAL | `float64` | REAL retains float32 precision |
| STRING / WSTRING | `string` | Transcoded to UTF-8 Go text |
| Ordinary record | `map[string]any` | Declared member names, recursively decoded |
| Primitive array | `[]bool`, `[]int64`, `[]uint64`, `[]float64`, `[]string` | Flat, last axis fastest |
| Record array | `[]any` containing maps | Flat, recursively decoded |
| Unsupported layout | No successful value | Per-tag error; native `Bytes` retained |

Both `ARRAY [1..2] OF ARRAY [1..3] OF INT` and
`ARRAY [1..2,1..3] OF INT` produce `[]int64{1,2,3,4,5,6}` with `Count == 6`.
Negative/nonzero lower bounds and singleton arrays are retained in descriptions;
a singleton array remains a slice. Native `DataType`, symbol `TypeCode`, declaration
`TypeName` and raw `Bytes` keep their protocol meaning.

Aliases, integer-backed enums and subranges retain their underlying category.
An unknown enum number is a valid integer value when it fits the underlying type.
Descriptions preserve the declared name rather than replacing it with the Go type.
Ordinary published records, byte padding and one-bit packed members are supported.
Pointers, references, interfaces, unions/overlap, hidden or incomplete function
block layouts, methods and unknown layout extensions produce an explicit
unsupported reason. There is no arbitrary Go struct binding.

`AllTags()` returns the complete validated advertised catalog, including symbols
whose layout cannot be decoded. Direct member lookups and handles do not add
catalog members. `TagInfo.Dimensions` contains effective axis lengths when resolved.
Names, declarations and dimensions come from one snapshot within one operation
timeout. A detected generation change discards the complete result and permits
one refresh within that same timeout.
Symbol names are case-insensitive, as in TwinCAT: `main.n` and `MAIN.n` resolve
to the same symbol and share one handle (ASCII letters only are folded). Read and
Describe results report the spelling the caller requested; catalog entries,
read-only checks and schemas use the PLC's published spelling.
`Programs()` loads the catalog and returns sorted published top-level namespaces,
e.g. `GVL`, `MAIN`; it is a namespace projection, not a PLC POU enumerator. An empty
catalog yields an empty list.

Descriptions are optional and caller-owned:

```go
if describer, ok := drv.(driver.Describer); ok {
    symbol, err := describer.Describe(driver.TagRequest{Name: "MAIN.test_struct"})
    if err != nil { return err }
    fmt.Printf("%s: %+v\n", symbol.Name, symbol.Type)
}
```

`metadata.Type` exposes kind, storage bits, declared name, signed lower bounds and
lengths, members, units, epoch and an unsupported reason. Mutating a returned
description cannot change the client's schema cache. The mandatory `Driver`
interface and existing configuration/result field layouts remain unchanged.

## Time and text

| Declaration | Go type | Unit / epoch |
|---|---|---|
| TIME | `uint64` | Unsigned milliseconds, 4-byte storage |
| TOD / TIME_OF_DAY | `uint64` | Milliseconds since midnight; writes must be less than 86,400,000 |
| DATE / DT / DATE_AND_TIME | `uint64` | Seconds from 1970-01-01, 4-byte storage |
| LTIME | `uint64` | Unsigned nanoseconds, 8-byte storage |
| LTOD / LTIME_OF_DAY | `uint64` | Nanoseconds since midnight; writes must be less than 86,400,000,000,000 |
| LDATE / LDT / LDATE_AND_TIME | `int64` | Library contract: signed nanoseconds from 1970-01-01 |

TOD/LTOD reads are not range-checked: a stored value of 24 hours or more (which
the PLC can hold, for example after arithmetic) is returned as the raw count
rather than turning a successful read into an error. Writes reject such values.

Integers are never routed through floating point. The captured LTIME is exactly
`8649040500600700`; `TIME` storage `ff ff ff ff` is `uint64(4294967295)`.
Other adapters retain their existing time semantics. Beckhoff documents
[TIME/date storage](https://infosys.beckhoff.com/content/1033/tc3_plc_intro/2529415819.html).

STRING defaults to Latin-1. Published `TcEncoding := UTF-8` is honored, or
`ads.WithStringEncoding("Latin-1")` / `ads.WithStringEncoding("UTF-8")` can override
STRING encoding for a connection. Capacity is measured in storage bytes.
Attributes apply to symbols, aliases and array elements. An explicit Latin-1
attribute clears inherited UTF-8; unsupported declared encodings fail unless a
connection override supplies the encoding.
WSTRING uses UCS-2 little endian, counts BMP characters and rejects surrogates and
non-BMP input. `63 61 66 e9 00` reads as `"café"`; WSTRING `e9 00 41 00 00 00`
reads as `"éA"`. Empty/full strings and fixed-stride string arrays preserve their
terminators. Writes reject embedded NUL, malformed Go UTF-8, unrepresentable
characters and capacity overflow; unused storage is zero padded. See Beckhoff's
[WSTRING definition](https://infosys.beckhoff.com/content/1033/tc3_plc_intro/2529437323.html).

## Writes

Ordinary `Write()` accepts in-range signed/unsigned Go integers, finite integral
floating input for integer targets, bool/numeric 0-or-1 for BOOL, floating values,
strings, full flat slices and complete record maps. `uint64` and `[]uint64` returned
by ordinary reads are accepted. REAL/LREAL permit IEEE NaN/Inf; finite REAL overflow
is rejected. All counts, subranges, text capacities and known access restrictions
are checked before sending a value-write request.

```go
// Examples require variables whose test/write purpose is confirmed.
err := drv.Write("MAIN.scratch_uint", uint64(65535))
err = drv.Write("MAIN.scratch_array", []int64{1, 2, 3, 4, 5, 6})
err = drv.Write("MAIN.scratch_record", map[string]any{
    "count": int64(25), "ready": true,
})
err = drv.Write("MAIN.scratch_record.count", int64(25))
```

A record map must contain exactly every declared member; arrays must contain the
full published element count. A whole record with known read-only member storage
is rejected, including when supplied as raw bytes.
Known access comes from uploaded symbols and validated direct lookups. Nested
restrictions appear on caller-owned member descriptions and stay specific to the
symbol instance, including indexed instances of a shared datatype. A direct
published member path writes only that member. Packed parent writes require a
complete parent value or a directly addressable bit symbol; there is no automatic
parent read-modify-write.
Advanced `[]byte` input must match the complete symbol size and is copied; it does
not establish semantic validation of opaque storage.

A value-write request is sent at most once. Metadata may be refreshed before that
request. If transport fails after sending, the outcome is uncertain and the error
says so; plcio does not replay the write or automatically read it back. A successful
reply is protocol acknowledgment, not an exactly-once or PLC-scan atomicity guarantee.

## Deadlines, generations and limits

The default five-second budget covers an entire client operation, including waiting
for serialized access, metadata, handles, split batches and one permitted read
recovery. Close has one total cleanup budget and can abort active I/O. Corrupt,
truncated, timed-out or disconnected streams become unusable; device rejections
retain `*ads.AdsError` and leave an otherwise healthy stream reusable. Detect
connection loss with `errors.Is(err, ads.ErrConnectionLost)` (or, across all
families, `driver.IsConnectionLost(err)`) and inspect original wrapped I/O errors;
check each result's `Error` as well as the top-level error.

`ADSAdapter.Keepalive()` (and `ads.Client.ReadState()`) performs one ADS
ReadState (command 4) on the target AMS port: a cheap, read-only exchange bounded
by the operation timeout, never retried. It returns an error when not connected.
A transport failure matches both `ads.ErrConnectionLost` and
`driver.ErrConnectionLost`; a device rejection (for example no runtime on the
configured port) is an `*ads.AdsError` and does not count as connection loss.

Metadata uploads and each value group are bracketed by the supported symbol-version
service. Reconnect and detected version/stale-handle changes invalidate handles,
catalog and schemas together. Reads pin one schema snapshot and may refresh/retry
once within the original budget; no bytes are successfully decoded with a newly
changed layout. Only explicit unsupported-service/invalid-group replies enable
primitive fallback. Other metadata/version failures remain failures.
A "symbol not found" (0x0710) reply through a handle cached by an earlier
operation is also treated as stale, because a download can invalidate handles
without changing the version counter: a read re-resolves once, while a write
returns the error (the value was not written) and the next operation re-resolves.
Not-found from a name lookup is final. Handles discarded by a detected change on
a healthy connection are released immediately (SumUp release where supported),
before replacements are acquired, and only while half the operation budget
remains; release failures are ignored. Handles of a lost connection are dropped.

Version checking is best effort: Beckhoff's SDK warns that minor online changes
can leave the symbol counter unchanged. It does not detect every PLC edit and does
not provide a PLC-wide atomic snapshot. Explicitly reconnect after program changes
when the target cannot signal them reliably. SDK evidence is recorded in
[fixture provenance](../ads/testdata/spec/README.md).

| Default limit | Value | Option |
|---|---|---|
| ADS command request/response payload | 1 MiB | `WithMaxPayload(bytes)` |
| SumUp items | 500, also bounded by bytes | `WithMaxBatchItems(count)` |
| Aggregate metadata wire bytes | 32 MiB | `WithMetadataLimits(bytes,symbols,types)` |
| Symbol / datatype count | 100,000 each | Same option |
| Schema/description depth | 64 | `WithExpansionLimits(depth,elements)` |
| Expanded members/elements per value operation | 1,000,000 | Same option |

Direct lookups (element/member paths such as `MAIN.arr[17]`, differently cased
aliases, names outside the catalog) share the symbol count and metadata byte
budgets with the catalog. When the next operation might not fit, the client
evicts all non-catalog lookup state at the start of that operation and releases
the evicted handles before acquiring new ones; catalog entries, their handles and
schemas are kept. An HMI reading many distinct paths therefore never hits a
permanent limit. Only one request that alone exceeds the budget fails.

The command payload limit bounds value, SumUp and lookup exchanges. A symbol or
datatype upload is one complete read of the size the PLC advertises beforehand,
bounded instead by the aggregate metadata budget, so large projects do not need a
larger value payload. Uploads beyond that budget fail explicitly; unverified
chunk/offset semantics are not guessed. The upload shares the operation timeout;
raise `PLCConfig.Timeout`/`WithTimeout` for very large catalogs on slow links.
Adapter users can pass these options through `driver.NewADSAdapterWithOptions`.
F080 SumUp results include each requested data slot, including failed slots, as
specified by the [vendor ADS definitions](https://github.com/Beckhoff/ADS/blob/master/AdsLib/standalone/AdsDef.h).

## Discovery and validation status

Discovery uses the TwinCAT UDP "Get Info" service on port 48899 (the Broadcast
Search used by TwinCAT engineering, Beckhoff's AdsLib `AdsTool <ip> netid`, and
pyads `adsGetNetIdForPLC`). `ads.Discover`/`DiscoverSubnet` send it unicast to
each address, which also works across routed subnets; `DiscoverBroadcast` sends it
to broadcast addresses; `ads.DiscoverWithReport` does both in one pass and returns
send/socket failures. The reply carries the device's real AMS NetID (it need not
be the IP plus `.1.1`: a CX at 192.168.5.212 can be `5.45.219.226.1.1`), hostname
and TwinCAT version. Only addresses that do not answer UDP fall back to a TCP
48898 identity probe, which has to guess the NetID as IP + `.1.1`. The reply
header is validated strictly; tags are parsed defensively (unknown tags skipped,
a truncated tag dropped without losing the identity).

UDP discovery establishes advertised identity. `HasRoute == false` means a working
route has not been verified. A validated TCP ADS device-info reply sets it true;
an open TCP port or broadcast reply alone does not. Invalid/error/truncated TCP
identity replies are rejected. Every probe is sent before replies are collected
for the whole timeout. IPv4 subnet expansion is capped at 4096 addresses,
workers at 128; /31 and /32 preserve usable boundary addresses. Discovery does not
change routes.

Beckhoff reads and writes are hardware-tested on a CX with TwinCAT 3, in addition
to offline wire/schema/conformance tests and parser fuzzing. Real online changes
remain unverified. See the [implementation report](plcio-implementation-report.md)
and [compatibility migration contract](plcio-compatibility.md).
