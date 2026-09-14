# Implementation audit handoff — 2026-09-14

## Candidate and review scope

This report records the implementation and audit evidence for v0.3.0, based on
the [owner's plan](../plcio-beckhoff-improvement-plan-2026-09-14.md). The baseline
is `1431799176522451efdc205e09521007a0ab1d16` (v0.2.11). Following the independent
audit fixes and full Beckhoff read matrix, the owner requested commitment and
publication on 2026-09-14. The selected release is v0.3.0, a minor release within
the project's beta status. Its tag identifies the committed implementation,
tests, captures and documentation. No application deployment is included.
The original supplied plan and captured evidence retain their historical wording.

The initial tracked tree was clean. The owner-supplied plan and Beckhoff captures were already untracked. Original capture bytes and checksums remain unchanged. Implementation, tests, SDK evidence and documentation are in this checkout; neighboring applications were inspected read-only. No AGENTS.md applied. The module remains Go 1.24.0 with standard-library-only runtime dependencies.

The candidate implements bounded ADS transport and lifecycle, complete generation-scoped catalogs and published type schemas, local record/array codecs, strict writes, accurate discovery, optional metadata, and shared adapter value/error conformance. Non-ADS transport implementations remain in place. Small native fixes cover Omron BOOL/array write storage and Logix packed template BOOL decoding; neither adds public methods or changes result field layouts. Existing EtherNet/IP adapter tests now select a temporary TCP port, avoiding collisions with local protocol peers.

The first candidate passed its existing suites but failed the independent audit's eight additional regressions across five implementation gaps. Its completion claim was premature. The focused follow-up below fixes those gaps and records new verification; the release does not claim completion of the external hardware validation listed at the end.

## Follow-up audit fixes — 2026-09-14

The external report is `/private/tmp/plcio-audit-20260914-5lpgxpcg/audit-report.md`. All eight reproductions were independently confirmed against the original candidate before implementation. The serialized packed-BOOL reproduction also passed against v0.2.11. The supplied audit snapshot and original captures were preserved.

| Finding | Focused implementation | Permanent regression evidence |
|---|---|---|
| Read-only children | Derive a sorted access index from the current uploaded catalog; include validated direct lookups. Reject whole-value semantic and raw-byte writes containing known restricted storage before a handle/value write. Annotate caller-owned descriptions, including inherited nested access and indexed elements, without mutating shared datatype schemas. | TestAuditPublishedReadOnlyMember; TestPublishedNestedAccessIsInstanceScoped |
| Logix packed BOOL | Preserve the published member INFO bit index and remove order-based inference. Validate scalar BOOL positions 0–31 while retaining array INFO lengths and existing exported layouts. | TestAuditBOOLUsesPublishedBitLocation; TestSerializedTemplateBOOLPositions (gaps, multiple bytes, nested templates, invalid positions, BOOL array length) |
| Catalog consistency and timeout | Project names, declarations and dimensions from one pinned snapshot; bracket the result with version validation and discard/retry once within the same deadline. The adapter also checks that deadline while copying its result. A single module-internal binding carries the projection and deadline; it has no registry/cache and adds no public browse API or fields. | TestAuditCatalogOperationBudget; TestAuditCatalogGenerationConsistency; TestCatalogTimeoutDiscardsProjection; TestCatalogRepeatedChangesStopAfterOneRetry |
| STRING encoding | Apply one attribute policy after layout resolution through symbols, aliases and array elements. Honor UTF-8/Latin-1 overrides in both directions and connection precedence; reject unsupported declared encodings for semantic reads/writes. Copy affected nodes so overrides cannot alter a cached base type. | Three audit encoding reproductions; TestPublishedArrayEncodingReadsWritesAndOverrides using serialized datatype/symbol metadata and independent bytes |
| Schema work bounds | Reject unsupported semantic values before access traversal. Memoize access walks and charge each node/edge attempt to the configured limit, including raw writes. Pass remaining logical expansion budget into child recursion; check the operation deadline during parsing, resolution, annotation, codec loops and projection. Interrupted resolution is not memoized as an unsupported type. | TestAuditUnsupportedLayoutWalkBudget includes deterministic bounded-work assertions for the 3,924-byte, 24-type shared graph and a low-budget rejection; TestSchemaValidationHonorsDeadlineAndWorkLimits |

The auditor's tests are retained in [ads/audit_review_test.go](../ads/audit_review_test.go), [driver/audit_review_test.go](../driver/audit_review_test.go) and [logix/audit_review_test.go](../logix/audit_review_test.go). Additional coverage is in [ads/audit_fix_test.go](../ads/audit_fix_test.go) and [logix/template_fixture_test.go](../logix/template_fixture_test.go). One concurrency fixture now populates its snapshot catalog as production does, rather than seeding only the older catalog field.

Follow-up validation passed: full normal suite, full race suites on Go 1.26.1 and Go 1.24.0, public surface/capture checks, and build/vet checks for linux/amd64, windows/amd64, darwin/amd64 and linux/386. The schema declaration fuzz target ran another 61.39 seconds and 3,954,329 executions without failure. Earlier parser campaigns, benchmarks and the 30-minute soak below describe the original candidate; they were not repeated wholesale for this follow-up. No new performance claim is made from these correctness fixes.

The supplied Beckhoff target passed the read-only smoke again under the race detector: 40 catalog entries, correct [2,3] dimensions, captured record/BIT/array/time/member values and explicit reconnect. The Logix target at `192.168.1.100` returned a 1756-EN2TR/B gateway identity (revision 5.28) and 23 catalog entries. Using the existing `LogixAdapter.SetTags` setup, ordinary reads decoded `Sta090_CAM1` (`ud_Camera_SR2000`, 1,364-byte storage plus handle), plus published CONTROL and TIMER records. All 14 BOOL members agreed with their respective raw buffers, including descending INFO indices in the SINT host at byte offset 3. DINT and BOOL scalar reads passed. The serialized offline tests establish nonconsecutive indices and positions spanning bytes within a larger host. Without SetTags, the existing native read path returns opaque structure data; this follow-up does not change that configuration contract.

The Siemens target at `192.168.5.190` accepted a COTP/S7 connection and PDU negotiation at rack 0, slot 0. `GetCPUInfo` currently returns a local placeholder, so this is connectivity evidence only, not wire-verified identity or value conformance. No configured Siemens DB addresses were supplied. The temporary read-only Logix/Siemens runner is `/private/tmp/plcio-followup-live.go`; it uses the existing APIs and is not a new library integration surface.

No live variable write, route installation, runtime control or program edit was performed. Scratch writes, real online changes, Siemens value tests and Omron hardware evidence remain pending and are explicit release limitations.

## Complete Beckhoff variable matrix — 2026-09-14

The owner supplied 34 variable paths and nine member paths with expected values.
The new opt-in `TestBeckhoffLiveTagMatrix` checks all 43 using ordinary adapter
reads, caller-owned descriptions and catalog projections. It verifies concrete
Go values, counts, declared names, array axis lengths/lower bounds, time
units/epochs, record member names, complete 124-byte record storage and unchanged
catalog membership after omitted-member lookups.

The expanded test exposed a further direct packed-member lookup issue: TwinCAT's
F009 reply contains a zero-padded storage-parent name, native BOOL code, BIT GUID
and bit address. The strict parser rejected its interior zero padding. Parent
record decoding had passed earlier and did not exercise this path. The focused
fix accepts this direct-lookup form only when the current uploaded parent schema,
BIT GUID and exact bit address validate the requested member. Malformed catalog
names, unrelated identities, nonzero padding, wrong GUIDs/addresses, unsupported
references/interfaces and missing parent metadata remain errors. Handle
acquisition retains the requested member name; no parent read/modify/write occurs.

The four new [lookup captures](../ads/testdata/beckhoff-bit-lookups-2026-09-14/README.md)
are stored separately with checksums; original captures remain immutable.
`TestCapturedPackedMemberLookups` replays them through uploaded metadata and
ordinary decoded reads. `TestPackedLookupRejectsUnverifiedAliases` tests ten
invalid cases across ReadDecoded/Describe/Write and proves none acquire handles,
read/write values or enter the lookup cache. An offline write test verifies one
normalized value write through the exact requested bit-member handle.

The full live matrix passed under the host race detector. All 34 variables are
present in the 40-entry advertised catalog, including `MAIN.test_struct` with
its actual published `TEST_STRUCT` declaration. Both multidimensional variables
are INT arrays with effective bounds [1..2,1..3]. All nine member values match;
the four packed member descriptions preserve TwinCAT's published `BIT` type and
decode to Go `bool`. The full 80-digit STRING and both string-array variants are
intact; DATE/DT are seconds from the Unix epoch, TIME/TOD milliseconds, and LTIME
nanoseconds. Native storage codes remain native: BYTE/WORD/DWORD/LWORD for their
unsigned aliases and DWORD for the 32-bit time/date aliases; declared names and
semantics remain available through the catalog and descriptions.

After the packed-member fix, the full race suites passed on Go 1.24.0 and
1.26.1. Build/vet checks passed for Linux amd64/386, Windows amd64 and Darwin
amd64. The symbol-parser fuzz target, extended with the four new lookup captures
and the lookup-specific parsing mode, passed 5,710,213 executions in 60.679
seconds. All live checks were read-only; the write regression uses an offline
peer.

Reproduce this read-only gate:

```sh
PLCIO_ADS_LIVE=1 PLCIO_ADS_HOST=192.168.5.212:48898 \
PLCIO_ADS_NET_ID=5.45.219.226.1.1 go test -race ./ads \
-run '^TestBeckhoffLiveTagMatrix$' -count=1 -v -timeout=60s
```

The captured live output is `/private/tmp/plcio-beckhoff-full-matrix-20260914.txt`.
No live value writes were performed during this additional gate.

## Final v0.3.0 release checks — 2026-09-14

The [official Go download feed](https://go.dev/dl/?mode=json) identified Go
1.27.1 as the latest stable toolchain. Release checks use that toolchain from a
temporary module cache. `go list -m all` contains only this module and
`go mod tidy -diff` produces no changes: there are no external package
dependencies to upgrade. The library retains its Go 1.24.0 minimum.

Final normal tests and the full race suite pass on Go 1.27.1; the full race
suite also passes on Go 1.24.0. Unfiltered `go vet ./...` passes on Go 1.27.1.
Build/vet checks pass on that toolchain for Linux amd64/386, Windows amd64 and
Darwin amd64. The complete read-only Beckhoff matrix passes again under Go
1.27.1's race detector; output is in
`/private/tmp/plcio-v0.3.0-go127-live-matrix.txt`.

The broader vet check identified three Omron host/port formatting sites, now
using `net.JoinHostPort`, and intentional imported unkeyed compatibility
literals. Local defined types retain those exact imported layouts and positional
checks before conversion to the public result types; no vet checks are disabled.
Changed and new Go files are formatted and `git diff --check` passes. Original
capture and supplied-plan hashes remain unchanged.

Run full suites for different toolchains sequentially: the existing EtherNet/IP
adapter UDP fixture uses port 44818. One concurrent Go 1.24 run collided there;
the isolated rerun passed. The pending hardware validation below is unchanged.

## Public surface and consumer examples

Existing `driver.Driver` methods, constructors, function types, exported configuration/result field layouts, `ads.TagValue`, `ads.TagInfo` and `ads.SymbolEntry` remain source compatible, including external unkeyed literals. Native `Bytes`, `DataType` and `TypeCode` retain their meanings. The baseline AST surface is recorded independently in `internal/compatibility/testdata/public-api-baseline.json`; `TestPublishedSurface` checks preservation and rejects unplanned additions in ads/driver. External caller examples compile and run in `driver/compatibility_external_test.go`.

Planned additions:

- The small `metadata` package: `Dimension`, `Kind`, `Type`, `Member`, `Symbol`, category constants and documented units/epoch. Descriptions are caller-owned deep copies; storage offsets and handles stay private to ADS.
- Optional `driver.Describer`, `(*ADSAdapter).Describe(TagRequest)` and `NewADSAdapterWithOptions(*PLCConfig, ...ads.Option)`. No method was added to `Driver`.
- `ads.DecodedTagValue{Raw *TagValue, Value any}`, `(*Client).ReadDecoded(...string)` and `(*Client).Describe(string)`. Ordinary ADSAdapter.Read uses the pinned bridge automatically; stateless raw Read/GoValue remains available.
- ADS options `WithLocalAmsNetId`, `WithLocalAmsPort`, `WithMaxPayload`, `WithMaxBatchItems`, `WithMetadataLimits`, `WithExpansionLimits` and `WithStringEncoding`. Original target identity/port/timeout options retain their signatures.
- Accurate ADS flag/error constants and deprecations for misleading legacy names. READONLY is `0x20`, value-by-name is F004, datatype-by-name is F011, device exception is `0x072C`; legacy ambiguous aliases retain documented numeric meanings where necessary. Independent SDK tests cover neighboring definitions.

The ordinary flow remains:

```go
drv, err := driver.NewADSAdapter(&driver.PLCConfig{
    Address: "192.168.5.212:48898", AmsNetId: "5.45.219.226.1.1", AmsPort: 851,
})
if err != nil { return err }
if err = drv.Connect(); err != nil { return err }
defer drv.Close()
values, readErr := drv.Read([]driver.TagRequest{{Name: "MAIN.test_struct"}})
for _, value := range values {
    if value.Error != nil { /* retain value.Bytes for diagnosis */; continue }
    record := value.Value.(map[string]any)
    _ = record["my_dint"].(int64)
}
if readErr != nil { return readErr } // Earlier successful slots are still useful.
```

`drv.Write(name, completeMap)` writes a supported record using its published schema without parent read/modify/write. All members/counts must be supplied and valid. Direct member paths are separate symbolic writes. Metadata inspection is optional:

```go
if describer, ok := any(drv).(driver.Describer); ok {
    symbol, err := describer.Describe(driver.TagRequest{Name: "MAIN.test_struct"})
    if err != nil { return err }
    _ = symbol.Type.Members
}
```

The [compatibility contract](plcio-compatibility.md), [Beckhoff guide](beckhoff.md) and [API reference](api-reference.md) document intentional concrete value changes, strict writes, time/text behavior and limits. Shared signed/unsigned/float values use int64/uint64/float64; primitive arrays use typed slices recursively. Record arrays remain []any of maps. Known opaque buffers in other protocols are preserved. REAL is decoded at float32 precision and widened; integer normalization never passes through float64.

## B01–B23 evidence

The matrix below records the original implementation tests. The follow-up section adds the regressions that exposed and corrected gaps in B03/B06/B10/B12 and the native Logix parser. Live evidence supplements applicable read/lifecycle cases without substituting for fault tests.

| ID | Fix | Relevant tests |
|---|---|---|
| B01 | Correct SDK flags; GUID, reference/interface and read-only evaluated independently of BIT storage | TestSymbolFlagsIndependent; TestOfficialSDKConstants; TestCapturedDatatypeTable |
| B02 | Retain original TCP endpoint, target/local AMS identities, runtime port and timeout through deduplicated verified reconnect | TestReconnectOriginalEndpoint; TestCloseRejectsLateReconnectPublication; live smoke/soak |
| B03 | One deadline for wait, metadata, batches, recovery and cleanup; full short writes; stalled/corrupt streams discarded | TestInitialVerificationBounded; TestOperationWaitBounded; TestStalledExchangesBounded; TestCloseHandleCleanupBudget; TestCloseAbortsActiveRead |
| B04 | Checked 30-byte symbol parser; validate counts, terminators, lengths, optional sections and complete catalog | TestRejectMalformedSymbolCatalog; TestCapturedSymbolTable; FuzzSymbolParser |
| B05 | Validate TCP/AMS bounds, flags, command, invoke and endpoints; exact body reads | TestFrameValidation; TestFragmentedResponseAndShortWrites; TestPartialFrameEOF; TestRouterErrorEnvelope; FuzzFrameParsers |
| B06 | Invalidate handles/catalog/schema together; immutable version-bracketed snapshot; one bounded read refresh | TestReadRecoversChangedLayoutsAndHandlesOnce; TestInterruptedAndChangedUploadNeverPublishes; TestCachedMissingCatalogDoesNotHideVersionFailure; TestRecoveryKeepsOriginalDeadline |
| B07 | Operation serialization removes handle race; state lock does not cover blocking network I/O; epoch prevents late publication | TestConcurrentHandleAcquisition; TestConcurrentReadWriteDescribeCatalogAndClose; TestCloseRejectsLateReconnectPublication; race suites |
| B08 | F004/F011 corrected; upload/lookup/write requests use exact service groups; no symbol download | TestOfficialSDKConstants; TestCatalogDescriptionMembershipAndVersions; TestOrdinaryRecordAndDirectMemberWritesUseOneValueRequest |
| B09 | DT width four; TIME/TOD unsigned milliseconds; exact long-time integers | TestTimeReadSemantics; TestStatelessHelpersMatchNativeCodec; TestCapturedDecodedValues; TestIndependentEncodingBytes |
| B10 | Latin-1/UTF-8 STRING and UCS-2 LE WSTRING; strict capacity, positions and terminators | TestDecodeStringsAndSingleArrays; TestIndependentEncodingBytes; TestInvalidEncoding; TestStringEncodingMetadataAndAlias; TestExplicitStringEncodingOverridesAttributes |
| B11 | Effective signed-bound dimensions, flat count/stride, singleton/native/nested arrays and record arrays | TestCapturedSchemasAndOwnedDescription; TestSchemaResolutionBoundaries; TestNestedRecordArrayAndExpansion; TestRecordArraysAndKnownReadOnlyStorage; live AllTags dimension checks |
| B12 | Prevalidate writable schema, integer ranges, strings, full members/counts and padded storage | TestCompleteCapturedRecordWrites; TestInvalidEncoding; TestNegativeWritesSendNoValues; TestRecordArraysAndKnownReadOnlyStorage |
| B13 | Shared complete identity decoder checks Result before fields; discovery tolerates fragmentation and rejects invalid replies | TestDiscoveryUsesCompleteValidatedIdentity; TestShortReadWriteErrorPreserved |
| B14 | Identity distinct from route; actual IPv4 masks, 4096-address/128-worker limits; IPv6 rejected before scans | TestBroadcastIdentityDoesNotVerifyRuntimeRoute; TestIPv4ExpansionBoundsAndMasks; FuzzDiscoveryResponse |
| B15 | Checked request/response arithmetic, item/byte splitting, ordered partial results; F080 failed slots consume requested sizes | TestBatchesSplitByCountAndBytes; TestF080FailedSlotStillConsumesRequestedBytes; TestPartialReadEOF; TestSumUpUnsupportedFallbackKeepsPartialResults |
| B16 | Programs loads complete catalog, returns sorted published top-level namespaces and true empty result | TestCatalogDescriptionMembershipAndVersions; TestEmptyCatalogPrograms |
| B17 | Advertised catalog separate from lookup/handle cache; omitted member reads do not change membership | TestCatalogDescriptionMembershipAndVersions; TestReadDecodedAutomaticallyPinsSchema |
| B18 | Exact 12-byte ADS Write header plus four-byte release handle; propagate ADS release failures | TestReleaseHandleWireAndError; TestCloseHandleCleanupBudget |
| B19 | Write/metadata transport failures immediately make connection unusable; explicit reconnect uses original endpoint | TestWriteEOFDisconnectsAndRetainsCause; TestEveryCommandTransportFailure; TestReconnectOriginalEndpoint |
| B20 | Command decoders preserve short ADS device errors, including eight-byte ReadWrite error | TestShortReadWriteErrorPreserved; TestRouterErrorEnvelope; TestReleaseHandleWireAndError |
| B21 | Each affected adapter retains earlier successes and per-tag errors together with wrapped top-level connection errors | TestADSAdapterPreservesPartialRead; TestLogixAdapterReadWriteAndPartialError; TestPCCCAdapterReadWriteAndPartialError; TestS7AdapterReadWriteAndPartialError; TestS7DBChunkReadFixtures; TestOmronAdapterReadWriteAndPartialError |
| B22 | ADS accepts read-produced uint64/[]uint64; checked target-width adaptation makes supported canonical inputs writable in other adapters | TestIndependentEncodingBytes; TestCanonicalNativeEncoderRoundTrips; TestCanonicalNumericWriteBounds; adapter TCP read/write peers; TestLogixRoutedCanonicalArrayWrite; TestOmronEIPCanonicalArraysAndBOOLWrite; TestOmronFINSBOOLArrayWrite |
| B23 | Exported error table/names audited against independent official SDK extraction; accurate aliases and explicit legacy deprecations | TestOfficialSDKConstants |

## Regression matrix and fixture provenance

Normal tests use local peers/files and never enable hardware access. Peers construct independent literal wire headers and expected payloads; codec tests use literal bytes and checked captures alongside round trips.

| Area | Offline evidence and limits |
|---|---|
| ADS catalog/types | All 40 captured symbols and 75 types accounted for; GUID/attributes/enums/extensions, malformed budgets, incomplete/change-during-upload rejection, lookup-independent browse membership |
| ADS schema/codec | Captured record offsets 0/4/8/9/92, padding 90–91; BIT positions 0–3; two [2,3] arrays are INT16 despite names; unsigned LTIME 8649040500600700; aliases/enums/subranges, negative/singleton/nested dimensions, strings/record arrays, cycles/depth/count limits and unknown layouts |
| ADS faults/concurrency | Every command transport failure, stalled writes/header/body/release, fragmented/bad/late frames, connection epoch, 64 simultaneous handle reads, mixed read/write/describe/catalog and concurrent Close; changed equal/different-size layouts, stale versions/handles, interrupted refresh and no write replay |
| S7 | Independent big-endian numeric/BOOL arrays, text and unchanged time fixtures; TCP INT read/write, multi-request partial EOF; 600-byte INT DB array split into 460/140-byte chunks with complete values or final-chunk EOF |
| Logix/Micro800 | Unrouted individual Micro800 and routed MSP/UCMM TCP read/write; atomic arrays; cached native UDT/nested AOI template fixture, hidden members and packed BOOL positions across bytes; partial EOF. Templates use the existing native API |
| PCCC | Native N/L/F address boundary fixtures; existing contiguous-run/batch/address tests; timer record keys/status bits; TCP read/write with exact signed storage and subsequent EOF/invalid-address partial mapping |
| Omron | FINS big-endian and CIP little-endian scalar/array fixtures; FINS numeric read/write and partial EOF; ordinary FINS BOOL-array read and exact write-back; CIP one-byte BOOL and typed-array write count. Existing EIP ordinary reads still request one element; automatic whole-array browsing/read expansion was not added |
| Shared caller API | Existing keyed/unkeyed layouts and function types compile; canonical wide values/typed arrays/nested maps preserve precision; opaque slices retained; sentinels and underlying EOF/device codes survive errors.Is/errors.As |

Original [Beckhoff captures](../ads/testdata/beckhoff/README.md) were supplied read-only on 2026-09-14 from the configured test PLC. Source capture revision is `1c19ef5071a008588a06da6dc77d78490b941d4a`; checked implementation baseline is the HEAD above. Symbols are 3440 bytes, datatypes 13608 bytes and TEST_STRUCT 124 bytes. `TestCapturedChecksums` verifies every original SHA-256. No capture was regenerated or modified.

[Independent SDK evidence](../ads/testdata/spec/README.md) records constants extracted from Beckhoff ADS Abstractions 7.0.339's ECMA-335 Constant table, vendor package URL and DLL SHA-256 `d41b465cee282ee470c5a62b5c6121f3c5ab6f250ab2016b662c948265bee355`. The companion official SDK XML and captured lengths established the 42-byte datatype header and optional member sections. Development-only inspection tooling introduced no runtime dependency. F080 cursor expectations follow requested-size slots from the SDK, independently of the implementation serializer.

## Toolchain, platform and fuzz gates

Host: Go 1.26.1, darwin/arm64, Apple M2 Max. Minimum toolchain checked: Go 1.24.0. Loopback listeners require the sandbox's network permission; these checks ran with that permission. The initial sandbox-only listener failure was environmental. A later full-suite port collision was corrected in the existing test helper and the suites rerun.

Final commands and results:

```sh
GOCACHE=/private/tmp/plcio-go-cache go test ./...
GOCACHE=/private/tmp/plcio-go-cache go test -race ./...
GOTOOLCHAIN=go1.24.0 GOCACHE=/private/tmp/plcio-go124-cache go test -race ./...
```

All three final commands passed across all packages. The final runs include batch result allocation reuse, Logix packed BOOL correction and the additional routed, S7 chunked DB and Omron BOOL-array fixtures. Public-surface and immutable-capture checks passed as part of both suites.

All four final build/vet checks passed. They use `CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> go test -exec=/usr/bin/true ./...` for linux/amd64, windows/amd64, darwin/amd64 and linux/386. This compiles package and test binaries and runs build-time vet; it does not execute foreign binaries. Tests and race checks execute on the supported macOS host, not on those cross targets.

Every new parser fuzz target passed at least 60 seconds using `go test ./ads -run '^$' -fuzz '^<target>$' -fuzztime=60s -parallel=4`. Targets start from literal malformed seeds and checked captured entry/declaration families; discoveries remain in temporary Go cache rather than changing original fixtures.

| Target | Completed duration | Inputs |
|---|---:|---:|
| FuzzFrameParsers | 60 seconds | 7898777 |
| FuzzSymbolParser | 60.37 seconds | 6002147 |
| FuzzDatatypeParser | 61.36 seconds | 5501177 |
| FuzzSchemaDeclaration | 61.33 seconds | 4280371 |
| FuzzDiscoveryResponse | 60.36 seconds | 6110604 |

No fuzz failure or failing seed was found. Subsequent edits did not change parser behavior; they adjusted constant names, allocation reuse and protocol regression fixtures.

## Performance and memory evidence

Connected-path benchmarks use the same independently serialized peer source in an isolated archive of baseline HEAD and the candidate. Symbols/handles are preseeded; schema/version capability is explicitly unavailable for the comparable cached path. Each 1/100/500-item operation makes one value exchange. These measurements exclude cold metadata upload and separately measure version validation. The peer's allocations are included in reported B/op. Runs use Go 1.26.1, three repetitions and one-second benchtime unless stated.

The final quiet sequential net.Pipe comparison with GOMAXPROCS=1:

| Items | Baseline ns/op range | Candidate ns/op range | Baseline → candidate B/op | Baseline → candidate allocs/op |
|---|---:|---:|---:|---:|
| 1 | 2343–2351 | 3042–3051 | 408 → 688 | 9 → 15 |
| 100 | 9304–9321 | 11773–11916 | 19333 → 19584 | 215 → 217 |
| 500 | 36273–36925 | 43826–46951 | 93248 → 93440 | 1018 → 1017 |

Median time increases are approximately 30%, 27% and 21%. Allocation-count growth exceeds 15% for a single primitive. These differences were investigated, not hidden by choosing earlier faster repetitions or loosening the target. Heap/CPU profiles identified per-operation deadline timers in net.Pipe, operation ownership/state checks, pinned symbol entries and validated complete response handling. The baseline set TCP keepalive but did not enforce operation deadlines. net.Pipe allocates timers when deadlines are renewed, whereas real TCP uses runtime socket polling. Removing avoidable per-tag error inspection/resolver construction, redundant name parsing and duplicate batch result slices reduced the 500-item candidate from approximately 52 microseconds/97536 B to the final values above. Single-read residual overhead is the bounded lifecycle/validation cost; large-batch allocation bytes are within 0.3% of baseline. The stricter guarantees account for the residual synthetic-path difference, but this is still a visible performance tradeoff for very low-latency in-process workloads.

The same final benchmark over real loopback TCP provides an additional comparable socket path, not a PLC throughput claim. Quiet sequential GOMAXPROCS=1 runs:

| Items | Baseline ns/op range | Candidate ns/op range | Baseline → candidate B/op / allocs |
|---|---:|---:|---:|
| 1 | 25468–28744 | 26026–26852 | 408 / 9 → 432 / 11 |
| 100 | 37826–38337 | 41456–70698 | 19333 / 215 → 19328 / 213 |
| 500 | 62411–68702 | 70683–72942 | 93248 / 1018 → 93184 / 1013 |

Median differences are approximately -2%, +20% and +6%. Single-read real-socket allocation growth is two small lifecycle allocations rather than the six additional allocations seen with pipe deadline timers. TCP allocation bytes are within 6% of baseline, and batch allocation counts decrease. The 100-item run includes a 70.7-microsecond outlier; it still has measurable validation/deadline overhead.

Default GOMAXPROCS=12 TCP runs were more variable: baseline 1/100/500 ranges were 24792–25914 / 35486–36328 / 60443–62257 ns; candidate ranges were 25352–27980 / 39980–40626 / 105654–185539 ns. The 500-item result differed substantially from the subsequent single-thread run; CPU profiles of the in-process peer paths were dominated by OS thread/poller waits and wakeups. This host/peer benchmark does not isolate a portable PLC latency difference. Both sets are retained here rather than selecting the more favorable result. Remaining timing variation and the cost of required lifecycle/framing checks are review considerations; no extra metadata exchanges or batch allocation growth explains them. Reproduce this benchmark on the deployment host when very low-latency polling is a requirement.

Final candidate net.Pipe/default GOMAXPROCS=12:

| Items | Cached ns/op range, one exchange | Version-validated ns/op range, three exchanges | Version-validated B/op / allocs |
|---|---:|---:|---:|
| 1 | 3949–4059 | 11424–11528 | 1840 / 37 |
| 100 | 14518–14795 | 22442–22848 | 20736 / 239 |
| 500 | 46132–47686 | 57532–59555 | 94593 / 1039 |

A supported stable version service adds one initial F008 check and one post-value-group F008 check. Cold metadata generation checks add separate exchanges. The captured read fixture asserts exactly two full uploads on cold load and none on an unchanged later read, with one symbolic whole-record value read rather than member fetches. Splitting, explicit unsupported SumUp fallback and refresh can add groups within the same deadline; no transport failure is treated as unsupported service.

Schema-pinned local decode and synthetic scale benchmarks (three runs):

| Benchmark | Time range | B/op | Allocs/op | Input bytes |
|---|---:|---:|---:|---:|
| Captured record decode | 379.8–385.6 ns | 560 | 7 | 124 |
| 2048-symbol catalog parse | 262.3–269.9 microseconds | 633706 | 6158 | 98304 |
| 20000-symbol catalog parse | 2.888–2.902 milliseconds | about 5999580 | 60070 | 960000 |
| 1024-record array decode | 381.6–390.4 microseconds | 591897 | 7170 | 126976 |

Stateless primitive GoValue changed from 5.591–5.596 to 12.91–12.96 ns (zero allocations). DINT encoding changed from 11.49–11.53 to 25.24–25.32 ns (4 B, one allocation). Checked exact-width/range handling is additional work; absolute increases are about 7 and 14 ns. The connected benchmarks and round-trip table measure the useful end-to-end consequence separately.

## Authorized Beckhoff integration

A separate test connection used TCP `192.168.5.212:48898`, target AMS `5.45.219.226.1.1`, runtime port 851. Opt-in tests require explicit host/Net ID; normal tests skip them.

Final read-only driver smoke passed under the host race detector (0.41 seconds): identity Plc30, application 3.1.1957, 40 advertised symbols; both multidimensional arrays expose axis lengths [2,3]. Ordinary reads matched the captured record, four packed BIT members, both flat INT arrays, exact LTIME and direct DINT member. Optional record description and truthful Close/explicit reconnect also passed. Identity/count/value expectations in these tests describe the supplied trivial project, not universal PLC counts.

```sh
PLCIO_ADS_LIVE=1 PLCIO_ADS_HOST=192.168.5.212:48898 \
PLCIO_ADS_NET_ID=5.45.219.226.1.1 go test -race ./ads \
-run '^TestBeckhoffLiveReadOnly$' -count=1 -v
PLCIO_ADS_SOAK=1 PLCIO_ADS_HOST=192.168.5.212:48898 \
PLCIO_ADS_NET_ID=5.45.219.226.1.1 go test ./ads \
-run '^TestBeckhoffLiveReadOnlySoak$' -count=1 -timeout=35m -v
```

The completed 30-minute soak ran from 2026-09-14 13:58:02 UTC to 14:28:02 UTC (1800.06 seconds). It performed 1800 six-symbol decoded batches at one batch per second. Explicit Close/reconnect after batches 600 and 1200 both passed. Every sampled point retained 41 lookup/handle entries, 40 catalog entries, 75 datatype entries and two goroutines.

| Batch | Heap after forced GC (bytes) | Total allocated bytes | GC count |
|---|---:|---:|---:|
| 30 | 459616 | 770312 | 1 |
| 300 | 422272 | 1879880 | 4 |
| 600 | 422544 | 3108224 | 7 |
| 900 | 423600 | 4565224 | 10 |
| 1200 | 423840 | 5792512 | 13 |
| 1500 | 424304 | 7283872 | 16 |
| 1800 | 424752 | 8511928 | 19 |

There was no handle or goroutine growth. Retained heap stayed around 422–425 kB after warmup, with approximately 2.5 KiB drift from batch 300 to 1800 rather than sustained request-sized retention. TotalAlloc is cumulative and increased as expected. This is evidence for this rate/project/duration, not a proof of absence of every leak. The soak binary preceded final helper/options/performance refinements; the successful pinned decode path remained the same. The final smoke was rerun after the final implementation edits; the 30-minute run was not repeated for allocation reuse or offline fixture changes.

The only live operations were device/symbol/datatype/value reads, acquisition/release of this client's handles, and its own Close/reconnect. No PLC variable write, route creation, runtime control or program deployment was performed.

## Observed consumer dependencies and migrations

Neighboring warlink currently pins plcio v0.2.11; waralert pins v0.1.0. Neither module was edited or upgraded, and whole-application hardware tests were not inferred from library test success.

- Both plcman `FromDriverTagValue` bridges copy the unified Value; they carry the new decoded categories. warlink also copies StableValue, Bytes, Count and Error; waralert copies Value and Error. warlink retains its own ignore-list filtering helper. warlink also uses raw ADS type-name helpers. Direct raw ADS GoValue remains stateless/opaque for records; callers choosing the native API need ReadDecoded to receive schema-aware records.
- warlink/www/handlers_api.go classifies maps as structures only when logix.IsStructure(DataType) is also true. ADS BIGTYPE values use their native code; structure classification should use family-aware optional metadata or the decoded map category.
- warlink's web/TUI code has []interface{} expansion/conversion cases. Typed primitive arrays require matching typed cases or slice reflection; record arrays still use []any. Audit JSON-to-write conversion against the actual target type and strict ranges.
- warlink/rule/condition.go already accepts int64 and uint64, but conversion to float64 can lose integer precision above 2^53. Library normalization preserves those integers; exact rules/JSON consumers must retain precision themselves.
- S7 simple-address writes and FINS numeric writes require matching PLCConfig.Tags DataType hints, as before. A Read TypeHint alone does not configure subsequent Write. Logix/Omron EIP canonical writes resolve/probe the target type through existing native services; these may add read exchanges before the single write and do not imply automatic value read-back.
- Consumers asserting Omron/PCCC narrow native widths, treating ADS records as []int, assuming primitive-only browsing or accepting overflow/truncation should migrate according to the compatibility table. Pin v0.3.0 deliberately and retain a rollback dependency.

## Supported cases and remaining hardware validation

Published ordinary primitives, aliases, enums, subranges, strings, effective multidimensional arrays, nested records and supported packed BIT members decode locally from complete schema-pinned buffers. Unsupported pointers/interfaces/unions/unknown layout-affecting extensions become explicit per-tag errors with raw Bytes available; browsing still exposes their metadata. Incomplete metadata, cyclic/over-budget expansion and inconsistent storage fail rather than producing a successful partial record.

Default bounds: five-second complete operation, 1 MiB command payload, 500 SumUp items, 32 MiB aggregate metadata, 100000 symbols/types, depth 64 and 1000000 expanded elements/members. Upload services are complete bounded reads, not guessed offset chunking. A PLC requiring more metadata can use explicit validated limits; upload data exceeding the command budget fails visibly.

Version checks are best effort with the services the PLC implements. Explicit unsupported responses permit documented fallback; corruption/timeout never do. SDK documentation warns that minor online changes may leave the symbol version unchanged. An unchanged advertised counter cannot guarantee detection of every same-size change; explicit reconnect refreshes metadata. Reads permit one recovery inside the original deadline. Writes never replay or promise atomic/exactly-once outcome; transport failure after a write means its outcome is uncertain. Full record writes require complete values and do not implicitly read/modify/write packed parents.

The following hardware validation remains pending for this beta release:

1. Confirmed scratch-variable live writes, exact expected bytes/values, read-back and restoration. Scratch purpose/values were not confirmed; no live value writes were authorized or attempted. Offline complete record/direct member and canonical write peers pass.
2. Live online-change cases, including equal/different-size layouts and any required owner-approved PLC program edit. Offline changed generations, stale handles and interrupted refresh tests pass.
3. Siemens value conformance and Omron hardware smoke for changed value/write handling. The follow-up adds Allen-Bradley scalar/UDT/BOOL read evidence and Siemens connectivity at the newly supplied targets; Siemens value addresses and an Omron target remain unspecified. Live write conformance remains part of gate 1.
The owner requested a new release and publication on 2026-09-14 after the audit
fixes and full Beckhoff matrix passed. This release decision does not establish
live-write or online-change conformance, and the observed performance tradeoffs
and metadata budgets above remain relevant to deployment.

No route/runtime/program action or application change is needed to review the candidate. The owner controls commit selection, hardware approval and feature promotion.
