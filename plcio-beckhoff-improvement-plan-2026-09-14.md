# plcio implementation plan: robust ADS and consistent Go values

Date: 2026-09-14. Status: implementation handoff; implementation has not started.

Baseline: `1431799176522451efdc205e09521007a0ab1d16` (published v0.2.11).
Recheck HEAD and working-tree changes before starting; preserve unrelated work.

This is the implementation handoff for plcio. It incorporates the code review and
captured Beckhoff evidence, with compatibility and acceptance decisions specified
below. The owner will assign implementation separately and audit the resulting work.

## 1. Product contract and scope

plcio is a Go hardware abstraction layer for supported PLC families. Applications
should connect, browse, read parsed Go values, and write Go values through a small,
consistent interface. Vendor byte orders, handles, layouts, encodings, and type
aliases belong inside the library. Preserve useful native declarations as metadata;
do not make callers implement vendor datatype rules.

Use the existing Logix family approach as the reference: ordinary reads resolve
available type information automatically and return familiar Go values, arrays,
and member maps. Align useful behavior across families without forcing identical
wire types or introducing a second object model.

Engineering priorities, in order:

1. Correct values and explicit errors; never plausible but incorrectly decoded data.
2. Robust, bounded connection and resource behavior.
3. Preserve existing API signatures and established valid behavior wherever possible.
4. Complete supported reads and writes through the ordinary API.
5. Simple implementation, small additions, and tests that establish real behavior.

The existing `driver.Driver` is the primary API. Keep `Connect`, `Close`, `Read`,
`Write`, `AllTags`, and the other current methods. Applications must not select
between ordinary and typed reading, upload schemas themselves, or understand ADS
service groups. This plan does not introduce `Driver.ReadTyped`, `WriteTyped`, a
second catalog API, a vendor-neutral transport framework, or an application policy engine.

Scope is ADS correctness and complete schema-backed symbolic I/O, plus the narrow
adapter work needed for common Go numeric representations and error propagation.
Keep protocol implementations for S7, CIP/Logix, PCCC, and Omron isolated from ADS
changes. Preserve their addressing, routing, UDT/AOI rules, byte order, and time semantics.

Notifications, automatic route creation, runtime control, program deployment,
polling/scheduling, analytics, preview limits, messaging integrations, and general
Go-struct reflection binding are outside this implementation. They are not needed
to make the requested I/O work. Do not expand existing application-oriented
`PLCConfig`/`TagSelection` fields such as publication preferences.

## 2. Compatibility decisions

### Source compatibility

- Keep `driver.Driver` unchanged; no new mandatory interface methods.
- Keep the field layouts of existing exported result/configuration structs,
  including `driver.TagValue`, `driver.TagInfo`, `driver.PLCConfig`, `ads.TagValue`,
  and `ads.TagInfo`. Adding private fields also breaks external unkeyed literals.
- Preserve existing constructors and method signatures, including their function
  types. Use the existing variadic ADS options or an additional constructor;
  do not turn a nonvariadic published function into a variadic one.
- Keep existing protocol-specific raw result APIs available. In particular,
  stateless `ads.TagValue.GoValue()` remains usable without a live client.
- Preserve native `DataType`/`TypeCode` meaning and `TagValue.Bytes`. Do not reuse
  these fields for a new universal enum, decoded value, or schema identifier.
- Keep Go 1.24 compatibility, pure Go, and standard-library-only runtime dependencies.

### Intentional behavior changes

The following changes are part of this plan. Record before/after examples and
affected consumers in `docs/plcio-compatibility.md`; do not label them behavior-neutral.

| Area | Existing behavior | Required behavior |
|---|---|---|
| Unified ADS structure read | Numeric raw-byte slice | Complete `map[string]any` for a supported published structure |
| Unsupported unified ADS value | Successful-looking opaque numeric slice | Per-tag decode/unsupported error, with raw `Bytes` retained |
| ADS catalog | Primitive filter; membership changes after direct reads | All validated advertised symbols; stable membership for a schema generation |
| ADS singleton arrays | Can become scalars | Remain typed slices of length one |
| ADS strings | Encoding bugs/raw Latin-1 in Go strings | Correct Go UTF-8 strings, with target encoding handled internally |
| Invalid ADS writes | Some wrapping, truncation, wrong sizes | Reject invalid input before sending the value write |
| Errors/lifecycle | Lost ADS codes, discarded partial results, stale connected state | Preserve causes and partial results; truthful connection state |
| Unified numeric values | Logix/S7/ADS mostly widened; Omron/PCCC often native widths | The common numeric contract below, implemented at adapter boundaries |
| Primitive arrays inside decoded Logix records | `[]any` even for homogeneous primitive members | Corresponding typed primitive slices at the driver boundary; record arrays remain `[]any` |
| Published constants | Some incorrect values/names | Correct documented constants; explicitly deprecate misleading old names |
| ADS `Programs()` | Guessed MAIN/GVL before upload | Published top-level namespaces from a successfully loaded catalog |

Do not change working scalar signedness, array order, or date/time units merely
to tidy code. The exception is a documented defect such as ADS TIME signedness.
Keep flat array values; dimensions belong in metadata. Use the same flattening
for equivalent native multidimensional and nested-array declarations.

Feature promotion is a deliberate minor release, not an unnoticed patch upgrade.
Do not choose or publish a tag during implementation. Small transport bug fixes
may be reviewed independently of the feature release. Use dependency pinning for
rollout/rollback; do not build permanent legacy/strict mode switches into every operation.
If an existing consumer depends on a changed representation, report the exact use
and migration needed in the handoff. Finish unaffected work; do not silently edit
other applications or invent additional public compatibility APIs.

## 3. Go value conformance

The following is the normal value contract at the unified driver boundary for
successfully decoded supported values. Apply it recursively to decoded records.
It follows the existing broad Logix convention. Do not introduce a distinct Go
type for every PLC declaration.

| PLC value category | `driver.TagValue.Value` |
|---|---|
| BOOL or schema-declared BIT | `bool` |
| Signed integer, including signed enum/subrange storage | `int64` |
| Unsigned integer or bit string, including unsigned enum storage | `uint64` |
| REAL/LREAL | `float64`; REAL is decoded at its actual 32-bit precision first |
| Supported text | Go `string` |
| Homogeneous primitive array | `[]bool`, `[]int64`, `[]uint64`, `[]float64`, or `[]string` |
| Structure | `map[string]any`, with declared member names and recursively conforming values |
| Array of structures | Flat `[]any`, each element a `map[string]any`, matching the existing Logix record-array container |

For arrays, `Count` is the total number of scalar/record elements after flattening;
for a scalar or one record it is 1. The last dimension varies fastest. Preserve
singletons. Example: both captured 2-by-3 INT declarations yield
`[]int64{1, 2, 3, 4, 5, 6}`, with bounds available through metadata.

Enums read as their underlying integer, including unknown enum values. Aliases
read as their resolved value category. Preserve declared names in descriptions,
and enum/subrange details privately for correct resolution and validation. Enum
writes enforce the underlying integer range; subrange writes also enforce their
declared bounds. Do not add an enum wrapper or substitute labels in ordinary reads.

DATE/TIME variants require an explicit semantic contract in addition to a Go
integer type. Preserve existing supported units/epochs and document them. ADS
TIME/TOD return `uint64` milliseconds; DATE/DT return `uint64` seconds since
1970-01-01; LTIME returns `uint64` nanoseconds. For new long date/time support,
LDATE/LDT return `int64` nanoseconds relative to 1970-01-01, interpreting the
64-bit representation as signed to preserve pre-epoch values. LTOD returns
`uint64` nanoseconds since midnight. This is the HAL interpretation of the
documented representation. Include negative/zero/positive epoch fixtures and
time-of-day bounds. Do not invent timezone information the PLC does not supply.
Do not funnel these through `float64` or universally coerce them to `time.Duration`,
whose range is smaller than unsigned LTIME. Include unit/epoch descriptions.
[Beckhoff time definitions](https://infosys.beckhoff.com/content/1033/tc3_plc_intro/12189021579.html),
[date/time definitions](https://infosys.beckhoff.com/content/1033/tc3_plc_intro/2529415819.html).

Implement cross-family numeric widening as a small adapter helper. It may widen
known decoded integers/floats and their arrays/record members, and normalize
homogeneous decoded primitive `[]any` members to their corresponding typed slices.
Keep record arrays as `[]any`; do not reshape existing member maps. It must not infer
that an opaque `[]byte`/`[]int` fallback is a numeric PLC array, convert integers
through floating point, reinterpret time units, or change raw protocol results.
Use the adapter's known type/decoding status to distinguish opaque buffers.
Preserve current opaque fallback behavior in unaffected families and document it
as an exception, rather than guessing a structure or normalizing its bytes.

For ADS, unknown/unpublished/unsupported layouts remain inspectable through raw
`Bytes`. A unified read that cannot completely decode the requested symbol returns
a per-tag unsupported/decode error and no successful partial record. Read exact
supported member paths independently when useful. Unknown types still appear in
`AllTags`. Stateless raw ADS APIs retain their existing opaque fallback.

For values an adapter supports writing, its ordinary `Write` must accept its
ordinary `Read` output type without narrowing or losing precision. Preserve
existing valid input forms too. Test this at the adapter boundary, particularly
Omron/PCCC after widening. This does not require implementing previously unsupported
record-write capabilities in other families.

## 4. Narrow public extensions and package boundaries

### Optional descriptions

Add one optional interface in `driver`; keep `Driver` unchanged:

```go
type Describer interface {
    Describe(request TagRequest) (*metadata.Symbol, error)
}
```

Put the small shared description structs in a new `metadata` package, imported
by protocol packages and `driver`. It contains data definitions, not networking,
registries, reflection marshalling, or a public ADS type graph. Use this shape:

```go
type Dimension struct {
    LowerBound int64
    Length     uint32
}

type Kind string // bool, int, uint, float, string, struct, opaque

type Type struct {
    Kind              Kind
    Bits              uint16 // numeric/boolean element width; 0 when inapplicable
    DeclaredName      string
    Dimensions        []Dimension // normalized effective axes; empty for scalar
    Members           []Member    // record members, described once for record arrays
    Unit              string      // empty when inapplicable; documented vocabulary
    Epoch             string      // empty when inapplicable
    UnsupportedReason string      // nonempty for opaque/unsupported layouts
}

type Member struct {
    Name     string // relative declared member name
    Type     Type
    ReadOnly bool
}

type Symbol struct {
    Name     string // exact requested/resolved symbol path
    Type     Type
    Readable bool
    Writable bool
}
```

Define constants for Kind and the units actually supported. `Dimensions` applies
to the element kind: no separate array-kind graph or public element-stride model
is needed. The original declaration retains distinctions normalized away by the
HAL. Storage offsets, bit positions, encodings, GUIDs, extension tails, and handle
identities remain in the private ADS schema. A description is a caller-owned
deep copy; changing it cannot corrupt a cache. Limit recursive expansion.

ADSAdapter implements `Describer` first; other adapters need not acquire methods
or pretend to discover unavailable schema. `Describe` is for inspection and rich
tools, never a prerequisite for `Read` or `Write`. `AllTags` continues carrying
names, declared type names, writability, and unsigned dimension lengths using its
existing fields. No second browse API or catalog-result wrapper is necessary:
`AllTags` returns success only after a complete validated upload.

### ADS integration without breaking exported result layouts

Keep schema parsing, I/O, and decoding together inside `ads`. A decoded value
must use the exact immutable schema snapshot captured for its read. Calling
`Read`, then looking up whichever schema is current in the adapter, is unsafe
across an online change.

The small protocol-level bridge allowed for this is:

```go
type DecodedTagValue struct {
    Raw   *TagValue // existing raw ADS result; its Error carries any per-tag failure
    Value any
}

func (c *Client) ReadDecoded(names ...string) ([]*DecodedTagValue, error)
func (c *Client) Describe(name string) (*metadata.Symbol, error)
```

ADSAdapter's existing `Read` invokes that bridge automatically. It is not a new
unified reading mode or an application opt-in requirement. Its purpose is to keep
`ads.TagValue` source-compatible while safely carrying a decoded result across
the Go package boundary. `Read` and `ReadDecoded` must use one internal read
engine; do not duplicate request construction, batching, caches, or error logic.
The raw API remains useful for existing low-level callers. Ordinary `ads.Client.Write`
uses the same private schema/codec for newly supported writes.
Return a nonnil wrapper and raw result for every requested slot, including failures.
The adapter uses the decoded value for `Value` and the initial `StableValue`,
preserving existing ignore-list behavior.

Use new `ads.Option` functions for transport identity and bounded resource settings.
Expose them to unified-driver callers through
`NewADSAdapterWithOptions(cfg, opts...)`, retaining `NewADSAdapter` and `Create`
unchanged. The returned adapter still implements the ordinary `Driver` interface.
Do not add Beckhoff controls to every family's shared config. Validate options
before dialing; retain `Option`'s existing signature by accumulating validation
errors in private options if needed.

Do not add a cross-driver context interface in this work. Existing timeout options
must first provide bounded whole operations. Internally use context/deadline-aware
I/O where helpful; a future general cancellation extension deserves a separate API
review. Avoid exporting new methods merely to make tests easier.

## 5. Required ADS defects and regression cases

IDs B01-B23 identify the required code-review findings. P0 means before expanded
ADS I/O can be considered ready. Offline reproductions do not establish that a
condition was exercised on hardware.

| ID | Priority | Required work and acceptance case |
|---|---|---|
| B01 | P0 | Correct READONLY to `0x20`, and audit neighboring symbol flags. Test TYPEGUID, REFERENCETO, interface pointer, BIT, and read-only independently. |
| B02 | P0 | Reconnect to the original TCP endpoint, retaining timeout, runtime port, and configured local/target identities. Test IP `192.168.5.212` with Net ID `5.45.219.226.1.1`; never dial the Net ID as an IP. |
| B03 | P0 | Apply operation deadlines, bound initial identity verification and Close, handle short writes, and discard unusable streams. Stall headers, bodies, writes, and handle cleanup in tests. |
| B04 | P0 | Replace unchecked symbol parsing; no uint16 offset wrap, missing terminator acceptance, skipped invalid entry followed by successful catalog, or unbounded count allocation. |
| B05 | P0 | Validate TCP/AMS lengths, frame budget, response flags, expected command, invoke ID, and endpoint semantics. Test fragmented, partial, oversized, mismatched, and late frames. |
| B06 | P0 | Invalidate handles/catalog/schema together on reconnect or detected online change. Test changed offsets, equal-size changed layouts, stale-version errors, and interrupted uploads. |
| B07 | P0 | Fix the confirmed concurrent handle race (`readSymbol` handle check versus assignment); cover Read/Write/Close/Reconnect/Describe/catalog interactions and deduplicated handle acquisition. |
| B08 | P0 | Correct value-by-name to F004 and datatype-by-name to F011. F00A is symbol download. Test exact service groups and commands; no symbol download request belongs here. |
| B09 | P1 | DT is four bytes, TIME/TOD storage is unsigned; scalar/array codecs and helpers must agree. Test DT width, TIME `0xffffffff`, and long time precision. |
| B10 | P1 | Correct STRING transcoding and WSTRING scalar/array encoding, character positions, capacities, terminators, and high bytes. Test `café`, `éA`, `Ā`, empty/full strings, and invalid input. |
| B11 | P1 | Correct dimensions/count/stride; preserve singleton and negative-bound arrays, nested/native dimensions, string arrays, and arrays of records. Reject inconsistent lengths. |
| B12 | P1 | Enforce writable schema, scalar ranges, full counts and padded buffers. No silent truncation, numeric wrapping, or partial writes disguised as complete writes. |
| B13 | P1 | Reuse validated device-info decoding in discovery. Use full reads, parse ADS Result before identity, and reject fragmented/truncated/error replies correctly. |
| B14 | P1 | Discovery must not equate TCP reachability or broadcast visibility with a working route. Bound scan expansion/workers; explicitly reject unsupported IPv6 scans. |
| B15 | P0 bounds; P1 splitting | Check all batch arithmetic and returned lengths; split by count and bytes, preserving order/errors. Verify F080 failed-slot layout independently before changing cursor handling. |
| B16 | P1 | `Programs` loads the catalog when needed and returns real published namespaces; document this abstraction. Empty catalogs produce empty lists, not guessed names. |
| B17 | P1 | Separate catalog membership from lookup/handle caches. Browse, read an omitted struct/member, browse again: only a new PLC symbol generation may change membership. |
| B18 | P0 | Fix handle-release request: ADS Write header is 12 bytes, followed by the four-byte handle. Parse the result; test failure propagation and cleanup. |
| B19 | P0 | A transport failure during Write or metadata operations marks the connection unusable. Reproduce EOF during Write followed by IsConnected/Reconnect. |
| B20 | P0 | Preserve short ADS error replies in every command decoder, including handle acquisition and device identity. An eight-byte ReadWrite error must retain its ADS code. |
| B21 | P1 | Preserve partial results plus top-level connection errors through adapters; fix the shared pattern in each affected adapter as a separate small change. Test consumers' `errors.Is`/`errors.As` behavior. |
| B22 | P1 | Ordinary ADS write accepts uint64 and []uint64 produced by reads, plus all documented scalar/slice inputs within range. Cross-adapter canonical outputs must also be writable where supported. |
| B23 | P1 | Audit exported error constants and names against the official table. Example: device exception is 0x072C, not the current 0x072D; adjacent codes also need correction. |

Correct clearly named but incorrect constants explicitly, with release notes.
For misleading legacy names with no sound semantic replacement, preserve their
numeric value, deprecate them, and introduce accurate names; never use the
misleading names in new internals. Do not leave a false READONLY definition active
just to make an old test pass. Verify values against
[Beckhoff ADS headers](https://github.com/Beckhoff/ADS/blob/master/AdsLib/standalone/AdsDef.h),
[symbol service groups](https://infosys.beckhoff.com/content/1031/tc3_ads.net/12209198859.html),
and [error definitions](https://infosys.beckhoff.com/content/1033/tc3_ads.net/9408165643.html).

## 6. Transport, lifecycle, and cache implementation rules

Start with a single in-flight ADS exchange per connection. Do not add a background
receive/demultiplexing system for synchronous reads. Separate ownership of a
connection's state from serialization of exchanges enough that shutdown can abort
blocked I/O. Write down the lock order and ownership beside the implementation.

- Retain the original host and TCP port separately from target AMS Net ID/runtime
  port. Honor a supplied TCP port instead of discarding it. Derive an ID from IPv4
  only when the user omitted one; an explicitly malformed Net ID is an error.
  Hostname resolution does not manufacture a hostname-shaped AMS ID.
- Support explicit local AMS Net ID/port options; retain the current derived
  local IPv4 identity as the default. Distinguish TCP port, target AMS port, and
  local AMS port in validation and documentation. Never mutate routes automatically.
- Default timeout remains five seconds. Define it as the budget for a complete
  public operation, including lock wait, metadata work, batching, and a permitted
  recovery. Each exchange uses the remaining budget. A batch fallback cannot
  restart an unlimited sequence of full timeouts. Reject nonpositive configured
  timeouts rather than accidentally creating unbounded operations.
- Close is idempotent, prevents new operations, aborts in-flight work as necessary,
  and spends at most one configured operation budget on cleanup in total. Release
  handles only while the stream is healthy; stop on transport failure. An explicit
  later Reconnect remains possible, but an earlier concurrent reconnect attempt
  must not publish a new connection after Close has invalidated it.
  Keep the existing void `ads.Client.Close` signature: cleanup remains best effort.
  Its release helper must parse and return ADS failures, and Close always closes
  the transport. Do not add another public Close variant for cleanup reporting.
- Publish a newly connected session only after identity verification. Dial or
  verify failures must not overwrite a healthy session. Deduplicate concurrent
  reconnect attempts and reject late publication from an obsolete attempt.
- The exchange layer owns transport-failure classification. Preserve wrapped Go
  I/O errors and `AdsError` codes. Device-level rejections do not necessarily mean
  TCP is broken. A desynchronized stream cannot be reused or used for fallback.
- Validate documented router-generated error envelopes without demanding fields
  that those envelopes legitimately omit/change. Include such fixtures; do not
  disable general endpoint/command validation to make one device pass.
- Keep catalogs, immutable schema snapshots, and mutable handles separate.
  Scope caches to connection generation and PLC symbol generation. Do not expose
  mutable internal descriptors or use raw type code FFFF as a structure identity.
- Default request/response payload budget: 1 MiB per ADS exchange, configurable.
  Maximum frame validation must also account for fixed AMS/TCP and command headers.
  Default SumUp limit: 500 items, also constrained by byte budget. These are client
  limits, not claims of universal device capacity. Split in original request order.
- Initial aggregate metadata budget: 32 MiB, 100,000 symbols, 100,000 types,
  depth 64, and 1,000,000 expanded value elements/members per operation. Check
  totals before allocation/expansion; raw byte limits alone do not bound a map tree.
  Put these limits in one private/options-backed location with boundary tests.
  Oversized single values return an explicit limit error; do not silently truncate.

The 500-item SumUp default follows
[Beckhoff's recommendation](https://infosys.beckhoff.com/content/1033/tc3_adsdll2/124835083.html).
Do not depend on a large server/router allocation being available.

Use symbolic handles for normal schema-backed batch/value access. This is
[Beckhoff's recommended symbolic access](https://infosys.beckhoff.com/content/1033/tc3_adsdll2/124833547.html).
For supported symbol-version service, bracket schema upload and value-read groups
with version checks and discard results from changed generations. Pin the schema
used for the successful read. An invalid-handle/version reply triggers one bounded
refresh/read retry on a healthy connection. Do not retry all errors or replay writes.
Allow the first read attempt and at most one recovery attempt within the original
operation budget. A documented unsupported version service is cached as a
capability limitation for that connection; it does not disable otherwise supported
primitive I/O. A transport/protocol failure during a version check fails the
operation. Document the absence of online-change protection when fresh version
validation is unavailable; do not silently treat other failures as non-support.

Handles are not a transaction or PLC-cycle snapshot guarantee. Version checks do
not protect a write against every possible PLC-side edit between checking and
execution. Invalidate promptly, use handle semantics, and document these limits.
Do not promise atomic snapshots or automatic exactly-once writes.

## 7. Metadata acquisition and parsing

Keep ADS datatype upload/parser/type resolution private, pure, and independently
testable. Suggested files: `ads/metadata.go`, `ads/schema.go`, and
`ads/metadata_test.go`; use equivalent small files if that better fits the code.

1. Decode command result/declared payload boundaries before interpreting metadata.
2. Read upload information (F00F), symbol entries (F00B), and datatype entries
   (F00E). Use correctly documented F011 lookup only when needed. Verify the exact
   command form against its service specification; metadata lookup can use ADS
   ReadWrite without writing application values.
3. Support older upload-info capability where available (F00C); a service-not-supported
   result is different from a malformed upload. Symbol-by-name primitive reads
   must remain usable when full datatype publication is unavailable. Cache a
   documented unsupported capability for the current connection generation.
4. For oversized catalogs use bounded chunked upload only where that service's
   offset semantics are verified. Otherwise return a clear limit error, with the
   relevant limit configurable. Never guess chunk offsets or read arbitrary memory.
5. Parse using checked int/uint64 arithmetic and remaining-buffer comparisons.
   Validate complete entry lengths, required NUL terminators, strings, member and
   dimension counts, signed lower bounds, and optional extensions. Reject duplicate
   identities that would overwrite another entry silently. Reject trailing or
   missing bytes unless a documented extension/padding rule accounts for them.
6. Parse supported GUIDs, attributes, enum/subrange information, references,
   datatype flags, member layouts, and array descriptions. Retain bounded unknown
   tails privately. An unfamiliar layout-affecting flag makes the type unsupported;
   it must not produce a confident guessed layout. Unknown valid types do not make
   other unrelated valid symbols disappear.
7. Resolve aliases and references to an internal type graph keyed by declared
   identity plus generation, using GUID/hash information where available. Detect
   cycles, missing references, arithmetic overflow, and excessive expansion.
8. Publish a complete new catalog/schema snapshot atomically after validation and
   generation checks. An incomplete refresh never becomes a loaded catalog.
   Old snapshots can finish already pinned operations, but cannot serve new work
   after invalidation. Distinguish an unavailable snapshot from a valid empty one.

Normalize array axes for the HAL description. Keep original declaration/layout
privately so encoding and member addressing remain faithful. Compute byte/bit
positions from validated metadata, not from a host Go struct's alignment.
BITS and BOOL storage are distinct even though both expose Go bools.
[Beckhoff BIT definition](https://infosys.beckhoff.com/content/1033/tc3_plc_intro/2529442699.html).

`AllTags` returns every advertised root entry regardless of decodability, sorted
by exact name for deterministic results. It does not recursively invent a tag for
every member or array element. Direct symbolic lookups do not add catalog entries.
`Programs` projects actual top-level namespaces from that catalog. `Describe`
accepts exact symbol/member paths and expands type members once, without repeated
metadata uploads for a valid cached generation. A bounded version check may still
require I/O. It describes the current generation, not a promise
that a previously returned raw buffer belongs to it.

## 8. Decoding and writing

Use pure schema-plus-buffer codecs underneath the I/O methods. Validate the
expected complete value size before decoding. Malformed data produces a per-tag
error with the original bytes retained where available. No ignored primitive
tails, successful partial records, guessed padding, or magic 64-byte limits.

Decode one complete structure buffer locally. Resolve nested records, aliases,
enums/subranges, arrays of records, packed BIT members, and all supported primitive
members using advertised positions. Function-block public data is readable only
where a complete supported data layout is published. Pointers/interfaces remain
opaque; never dereference them. Unions remain explicitly unsupported for ordinary
value decoding in this release; do not invent an active member or a generic union API.

STRING is converted from the target's advertised/configured encoding to Go UTF-8.
Use Latin-1 as the documented ADS default and UTF-8 where advertised or explicitly
configured. Keep overrides scoped to the ADS connection; no encoding heuristics.
WSTRING defaults to UCS-2 little endian: reject non-BMP/surrogate code points on
write instead of silently inventing UTF-16 support. Size limits are target bytes
or code units, not Go UTF-8 byte positions. Require valid termination inside the
declared buffer; retain raw bytes on decode errors. Do not require unused bytes
after a terminator to be zero when reading.
[STRING](https://infosys.beckhoff.com/content/1033/tc3_plc_intro/2529410443.html),
[TcEncoding](https://infosys.beckhoff.com/content/1033/tc3_plc_intro/5873680907.html),
[WSTRING](https://infosys.beckhoff.com/content/1033/tc3_plc_intro/2529437323.html).

Ordinary `Write` resolves and validates the target declaration automatically:

- Accept existing valid scalar inputs and canonical read-output types. Use checked
  integer conversions; reject negatives for unsigned targets, overflow, and
  fractional float-to-integer conversions. Preserve exact integer inputs without
  floating-point intermediates. Reject finite float narrowing overflow. Preserve
  IEEE NaN/infinity for REAL/LREAL targets; reject them for integer targets.
- For arrays require the full expected flat element count, including singleton
  arrays. Accept the corresponding typed slices and valid existing documented
  forms; no input-length-derived target stride. No implicit prefix-array writes.
- Encode strings to the declared capacity, terminate, and pad the whole target
  buffer. Reject over-capacity, embedded NUL, invalid Go UTF-8, and characters
  unrepresentable in the selected target encoding. Never truncate silently.
- Reject read-only symbols/members before sending a value write. Access flags are
  advisory metadata; the server's actual rejection remains authoritative.
- Support complete structure values as `map[string]any`, and record arrays as
  `[]any` of complete maps, so ordinary read results are writable. Require every
  writable field exactly once and reject unknown
  fields. Whole-record writes are permitted only for fully understood ordinary
  data layouts with all members writable and all non-field storage identified as
  padding. Zero known padding. Never overwrite hidden/opaque/read-only storage or
  write a function block's runtime internals.
- A whole ordinary packed-BIT record may be encoded from all its members into
  its complete buffer when all bits are accounted for or documented padding.
  A selected member path uses symbolic member access; do not replace the parent
  with an implicit read/modify/write. If direct member writing is unsupported,
  return an explicit error. Partial record maps must not trigger whole-record RMW.
- Preserve documented low-level raw byte write inputs only with exact target
  length and access checks. Keep this advanced behavior explicitly documented;
  it does not establish semantic validation of opaque structures. Do not add a
  second raw-writing API or silently treat arbitrary slices as encoded storage.
- After bytes may have been sent, a transport failure returns an error that makes
  the uncertain write outcome clear while preserving the cause. Never automatically
  replay a value write. Read-back is a test/consumer choice, not a universal extra
  network request silently added to every write.

Batch reads preserve order, duplicates, and one result slot per request. A tag-level
failure does not fail unrelated tags. Unsupported SumUp service can fall back to
individual reads within the same operation budget; link loss, corrupt framing,
or a lost deadline cannot cause requests on the same unusable stream. With partial
success and connection loss, return the completed results plus the top-level
`ErrConnectionLost` wrapper; adapters must preserve both. Retain the underlying
error for `errors.As` and preserve `errors.Is(err, ads.ErrConnectionLost)`.

## 9. Implementation sequence and gates

Work in the order below, with small coherent changes. Do not batch a repository-wide
rewrite into one step. Each completed step should leave normal tests passing;
keep expected-failure audit reproductions out of the regular suite until fixed.

### Step 0 — Baseline and independent fixtures

- Read current package docs and source; record actual HEAD, toolchain, working-tree
  state, and the public signatures/struct layouts being protected.
- Run the existing suite with the race detector. It currently passes, but ADS
  statement coverage was only 4.5% during review. Do not infer connected coverage
  from a disconnected-state helper test.
- Use the supplied raw captures in `ads/testdata/beckhoff/`; verify their checksums.
  Convert the regression cases below into independent focused tests.
  Tests must run without another application, a network, or a real PLC.
- Create `docs/plcio-compatibility.md` with actual value/type/wire examples and the
  intended changes in section 2. Protect documented caller patterns with external
  package examples: type assertions, unkeyed literals, raw ADS use, and catalogs.
- Add protected adapter examples for signed/unsigned boundaries, floats, arrays,
  records, and partial errors. Do not freeze known invalid packets as correct.
- Create offline benchmarks for primitive/batch operations and later decoding;
  record baseline results and round-trip counts.

Gate: baseline understood; evidence is reproducible; compatibility expectations
are concrete. No PLC writes or production code changes are needed for this step.

### Step 1 — ADS foundation

Implement the transport/symbol-parsing portions of B01-B08, B18-B20, and the
arithmetic/frame-bound portions of B15. Establish connection generations here;
complete datatype-generation handling in step 2.
Fix helper error parsing, options, handle release, reconnect, lifecycle ownership,
and the actual connected race. Extract shared command-response parsing instead of
patching each caller with a different length check.

Gate: fake-peer tests for fragmented/stalled traffic, exact release wire bytes,
initial verification, configured endpoints, concurrent lifecycle, every command's
transport failure, and preserved error identities pass under `-race`. Close is
bounded for 0, 1, and many cached handles. Ordinary existing primitive reads/writes
still pass their valid fixtures. All foundation P0 cases pass; the remaining
metadata-generation cases are mandatory in step 2 before expanded decoding.

### Step 2 — Catalog and schemas

Implement the parser, resolver, generation checks, separate caches, complete
`AllTags`, real namespace projection, and the optional description capability.
Resolve B04/B06/B11/B16/B17 fully. Add the small `metadata` types and ADS projection.

Gate: all 40 captured symbols and 75 captured type entries are accounted for;
extensions have explicit parsing or unsupported accounting; counts and byte
boundaries match. Unknown valid types remain discoverable. No malformed/changed
upload poisons a cache. Both multidimensional declarations have the same effective
axes. Missing metadata services preserve supported primitive-by-name operations.

### Step 3 — Ordinary ADS reads and Go conformance

Implement schema-backed codecs and the single internal read engine. Make the
existing ADSAdapter.Read automatically return complete Go values. Add the narrow
protocol bridge from section 4; retain raw API layout/behavior. Resolve B09-B11
for reads and complete B15 splitting/fallback. Wire up per-tag decode failures.

Then make the small, separate adapter numeric-conformance change. Logix/S7
already provide most canonical atomic types; verify their record members rather
than assuming. Omron/PCCC need explicit checked adaptation. Resolve B21 without
editing vendor transports: preserve any partial results returned by those clients.
Do not claim partial results that an underlying client never produced.

Gate: exact captured record values and concrete Go types match section 10;
ordinary callers never load schema manually. Conformance fixtures exercise every
adapter. Unknown buffers are not normalized as integer arrays. Valid existing
scalar values, wire encodings, and protocol-specific raw results remain intact,
except listed bug corrections. Describe remains optional.

### Step 4 — Ordinary writes

Implement strict schema-driven scalar and array encoding, followed by complete
ordinary records and direct member writes. Resolve B10/B12/B22. Keep encoding
validation pure and testable without performing network writes. Adapt canonical
numeric inputs at other adapters only where necessary to preserve supported
read/write round trips.

Gate: independent expected-byte fixtures plus round trips, range/count/access
errors, string boundary cases, padding, packed bits, and uncertain-outcome tests
pass. Negative cases send zero value-write requests. Hardware write validation
uses confirmed scratch variables as described in section 11; an unavailable live
case is recorded as a remaining release test.

### Step 5 — Discovery, documentation, and integration readiness

Resolve B13/B14/B23 and misleading docs. Reuse identity/packet validation, distinguish
verified identity from mere TCP reachability, and report route knowledge honestly.
With existing boolean discovery fields, false means "not verified"; do not add
a new discovery object hierarchy. Do not label an arbitrary open port as a verified
Beckhoff PLC. No inference of "no route" from an arbitrary timeout.

Bound CIDR expansion before enumeration. Initial scan cap is 4,096 IPv4 addresses;
keep small discovery functions simple and return a clear error for broader/IPv6
CIDRs. Correct network/broadcast handling across actual subnet masks, including
/31 and /32; avoid filtering every address ending in .0 or .255. Bound workers
and cancellation by operation limits. Larger scan orchestration belongs to callers.

Update `README.md`, `docs/beckhoff.md`, `docs/api-reference.md`, relevant shared
value docs, and `CHANGELOG.md`. Show the existing Read/Write flow and an optional
Describe example. Document supported/opaque cases, concrete Go types, flat arrays,
units, errors, timeout semantics, and the deliberate compatibility changes. Correct
current claims of automatic ADS structure decoding and native-width Go outputs.

Gate: external-package compatibility examples, the full regression matrix, and
the read-only Beckhoff smoke pass. Application development is outside this plan.

### Step 6 — Audit handoff

Create `docs/plcio-implementation-report.md` containing:

- Actual revision and changed public surface, with short consumer examples.
- A B01-B23 checklist mapping each item to its fix and relevant test.
- Compatibility changes and observed consumer dependencies/migrations.
- Test commands/results and fixture provenance; explicitly distinguish offline,
  live-read, live-write, online-change, and other-vendor hardware evidence.
- Benchmarks, memory/round-trip behavior, soak results, and justified differences.
- Known supported/unsupported cases and any pending external test gates.

Leave reviewable implementation and docs ready for the owner's audit. Do not
publish a release, merge a PR, or claim completion of hardware gates not performed.
If external hardware/authorization is unavailable, finish all independent code
and tests and distinguish implementation readiness from release readiness.

## 10. Evidence and exact fixture expectations

The supplied [Beckhoff fixtures](ads/testdata/beckhoff/README.md) contain raw
symbol, datatype, and value payloads captured read-only on 2026-09-14, with
provenance and SHA-256 checksums. They are test inputs, not a parser or generated
Go layout. Expected wire constants/layouts must come from specification or
independently checked captures, not the implementation under test. Keep tests
self-contained in this repository.

| Capture | Independent expected result |
|---|---|
| `symbols.bin` | 3,440 bytes, 40 complete entries |
| `datatypes.bin` | 13,608 bytes, 75 complete entries |
| `MAIN.test_struct.bin` | 124 bytes; see exact members below |
| `MAIN.test_bitpacked_struct.bin` | `0A`; four BITs at offsets 0,1,2,3 -> false,true,false,true |
| Both `MAIN.test_2d_dint_array_style*.bin` | Six signed INT16s 1..6, despite "dint" in names; effective bounds [1..2,1..3] |
| `MAIN.test_ltime.bin` | Unsigned nanoseconds `8649040500600700`, exactly |

`MAIN.test_struct` must decode in an ordinary driver read to:

```go
map[string]any{
    "my_byte":       uint64(15), // byte offset 0; size 1
    "my_dint":       int64(25),  // byte offset 4; size 4
    "my_sint":       int64(5),   // byte offset 8; size 1
    "my_string":     "Test structure string", // offset 9; size 81
    "my_dint_array": []int64{1, 2, 3, 4, 5, 6, 7, 8}, // offset 92; size 32
}
```

Offset 90 was an earlier incorrect inference; bytes 90-91 are alignment padding.
Assert offsets against uploaded metadata as well as final values. Preserve the
already working individual and SumUp reads of packed BIT member paths.
Fixture counts are properties of these captures, not permanent assertions that
every live project must publish exactly 40 symbols or 75 types.

Additional follow-up reproductions, to implement as focused fake-peer tests:

1. Releasing handle `0x12345678` must send ADS command data
   `06 f0 00 00 00 00 00 00 04 00 00 00 78 56 34 12`.
   Current code inserts four zero bytes before the handle and ignores result errors.
   An ADS result of `0x706` must be retained by the cleanup helper.
   See the [12-byte ADS Write header](https://infosys.beckhoff.com/content/1033/tcadscommon/12440291467.html).
2. Seed a cached writable DINT and handle, inject EOF on the value write, and
   verify the client becomes disconnected and a subsequent explicit reconnect
   actually attempts the configured endpoint.
3. Return a valid eight-byte ReadWrite error body from handle acquisition. The
   error remains an `*AdsError`, not "response too short: 8 bytes".
4. Decode UINT bytes `{42,0}` and UINT-array bytes `{42,0,43,0}`; writing the
   resulting `uint64`/`[]uint64` must yield those exact bytes.
5. With a preseeded symbol and handle zero, launch 64 simultaneous reads against
   an in-memory peer that echoes valid headers and responds to handle/read services.
   The baseline race detector reports `ads/client.go:693` versus `:708`. Test
   deduplicated handle acquisition and cleanup in the corrected implementation.
6. Return successful earlier results plus a later connection error and exercise
   ADSAdapter.Read. Assert both the results and `errors.Is` sentinel survive.

## 11. Test and release matrix

| Area | Required evidence |
|---|---|
| Public API | Compile existing signatures and keyed/unkeyed literals; no required new Driver methods; exact public-surface diff reviewed |
| Common Go values | Signed/unsigned/float scalar and array fixtures across all adapters; record members where supported; no opaque-byte reinterpretation; canonical write inputs preserve values |
| ADS wire/lifecycle | All B cases, fragmented/stalled/late frames, bad addresses/flags/lengths, short writes, disconnect during every operation, repeated Close/Reconnect and concurrent activity under `-race` |
| ADS schema/codec | Negative/nonzero bounds, singleton/native/nested dimensions, aliases/enums/subranges, STRING/WSTRING arrays, nested record arrays, packing modes, unknown extensions, cycles, malformed sizes, and budgets |
| Online change | Equal-size and different-size layout changes, old handles, changed catalog membership, change during upload/read, timeout during refresh; no stale successful decode or automatic write replay |
| S7 | Independent big-endian scalar/array and BOOL fixtures, DB/chunk paths, strings, partial errors, and existing time behavior |
| Logix/Micro800 | Routed/unrouted CIP, atomic arrays, UDT/AOI templates/BOOL packing, partial errors, and current Micro800 behavior |
| PCCC | File/address types, batched/individual mapping, timer/counter records, and checked adaptation of canonical write inputs |
| Omron | FINS big-endian and EIP little-endian fixtures, supported scalar/array/string values, and adapter numeric conformance |
| Toolchains/platforms | Go 1.24 and current consumer toolchain; race tests on a supported host; Linux/macOS/Windows build checks without executing cross-compiled binaries |
| Caller compatibility | External-package examples build and run against the candidate library, covering existing signatures, result layouts, concrete Go types, and error handling |

Run `go test ./...`, `go test -race ./...`, and focused parser fuzzing. Normal
tests never contact hardware. Use fuzz seeds for every captured and malformed
entry family, and run each new metadata/frame parser fuzz target for at least
60 seconds. Record seed and duration on any failure. Use 32-bit build/parser
checks where supported to expose integer-size assumptions. Avoid tests that only
encode and then decode using the same unchecked assumptions; retain independent
expected bytes and values alongside round trips.

Benchmark stable-schema primitive reads, 1/100/500-item batches, the captured
record decode, and larger synthetic catalogs/record arrays. Track allocations,
network exchange counts, and cache reloads. An unchanged generation must not
trigger repeated full metadata uploads or member-by-member structure reads.
Target no unexplained >15% CPU/allocations regression on comparable cached
primitive paths; necessary version checks are accounted for separately in network
round-trip measurements. Investigate differences before changing thresholds.

Perform a minimum 30-minute read-only soak on the supplied Beckhoff target with
repeated primitive/record/batch reads and occasional explicit Close/reconnect.
Compare allocation/goroutine/cache behavior after warmup; no monotonic handle or
goroutine leak. Keep routine live read rates at or below one batch per second
unless the owner authorizes a higher-load performance test.

Available target:

```text
TCP host:       192.168.5.212
TCP port:       48898
Target AMS ID:  5.45.219.226.1.1
Runtime port:   851
```

Use opt-in integration tests, e.g. `PLCIO_ADS_LIVE=1`, with explicit target
configuration and bounded timeouts. The owner supplied this PLC for implementation
testing. Routine symbol/datatype/identity/value reads, handle management, and
closing/reopening the test client's own connection are the default test scope.
The program is trivial; passing it is integration evidence, not complete type or
fault coverage. Use a separate test client connection.

The supplied PLC is the implementation test target. Use confirmed scratch
variables for opt-in live writes, with exact expected values, read-back checks,
and restoration of prior values. Establish their test purpose from the program
or owner-provided information; a name alone does not establish safe test use.
If the required scratch variables or online-change case need a PLC program edit,
prepare that concrete change for owner approval and finish independent tests
first. Route changes, runtime control, and program deployment are outside this
library task. Normal tests and the read-only soak never enable writes implicitly.

Before feature promotion, retain Siemens and Allen-Bradley hardware smoke and
include an Omron smoke where that adapter's representation/input handling changed.
No such targets were provided in this review. Record unavailable hardware tests
as pending external release gates; do not claim their completion from unit tests.
The implementation agent can finish the candidate and audit handoff while these
external gates remain pending. The owner/auditor controls promotion.

## 12. Final acceptance

The candidate is implementation-complete when all in-scope code, documentation,
offline gates, and available authorized integration checks are finished; B01-B23
are traced to evidence; public changes are limited to the planned surface; and
the audit handoff clearly identifies any remaining external release gates.

The owner should be able to use the familiar Connect/AllTags/Read/Write flow,
receive correct consistent Go values for supported data, inspect optional
metadata when needed, and diagnose explicit failures without knowing the vendor's
wire protocol. Do not finish with UDT reads working while supported writes,
reconnect behavior, parser bounds, or the regression evidence remain incomplete.
