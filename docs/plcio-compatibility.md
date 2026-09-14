# Compatibility contract

## v0.3.1 Logix read corrections

Existing method signatures, result/configuration layouts and raw storage remain
source compatible. Indexed/member paths now use discovered root/template metadata;
standard STRING templates decode as text, including nested strings and STRING
arrays. Consumers previously treating these values as opaque buffers or LEN/DATA
maps should use their decoded values. Unified driver DataType remains the native
resolved code, including structure codes for STRING templates; Bytes remains the
original storage. Client.DecodeUDT retains its top-level map return type.

Interrupted or inconsistent fragmented transfers now return errors rather than
successful partial records. ConnectionInfo reports CIP Forward Open status rather
than TCP status; IsConnected retains its transport-liveness meaning.

See [v0.3.1 validation](plcio-v0.3.1-validation.md) for the single/count/batch/buffer
matrix, read-only hardware checks and remaining limitations.

## v0.3.0 baseline contract

Implementation baseline: `1431799176522451efdc205e09521007a0ab1d16`
(v0.2.11). v0.3.0 is a deliberate minor release within the project's beta status.
Pin a dependency version for rollout and rollback.

Existing `driver.Driver` methods, constructors, function types, configuration and
result field layouts remain source compatible. External unkeyed literals and a
stateless raw ADS caller are compiled in `driver/compatibility_external_test.go`.
Native `DataType`, `TypeCode`, and `Bytes` retain their meaning.

v0.3.0 introduces these intentional behavior changes:

| Consumer/use | Before | v0.3.0 and migration |
|---|---|---|
| Unified ADS record read | `[]int` of opaque bytes | Complete `map[string]any`; assert/map member values instead of iterating storage bytes |
| Unified unsupported ADS layout | Opaque numeric slice, no error | Per-tag error with `Bytes` retained; inspect raw storage explicitly |
| ADS browsing | Primitives only; direct lookups alter membership | Complete validated advertised catalog; filter it in the application if desired |
| ADS singleton array | Can collapse to scalar | Typed slice of length one |
| ADS text | Latin-1 bytes exposed as invalid UTF-8; incorrect WSTRING positions | UTF-8 Go strings; e.g. STRING `63 61 66 e9 00` reads `"café"`, WSTRING `e9 00 41 00 00 00` reads `"éA"` |
| Invalid ADS write | Wraps integers or truncates strings/counts | Reject before any value-write request; supply full count and in-range values |
| Link failures | Lost ADS causes, discarded partial adapter results, stale state | Preserve partial slots plus wrapped connection cause; check both levels of errors |
| Unified Omron/PCCC numbers | Native integer/float widths | `int64`, `uint64`, `float64` and typed slices; update concrete type assertions |
| Primitive arrays in Logix records | Homogeneous `[]any` | Typed primitive slices recursively; record arrays stay `[]any` |
| Packed Logix template BOOL members | Any nonzero storage byte makes every member true | Each member uses its published INFO bit position, including subsequent bytes |
| ADS constants | Incorrect symbol flags/service/error codes | Correct documented values and deprecate misleading names |
| Omron writes | CIP BOOL encoded as two bytes; typed CIP arrays always counted as one; FINS BOOL slices unsupported | One-byte CIP BOOL, actual typed-array element count and FINS BOOL slices; review native wire expectations |
| Canonical numeric writes in other adapters | Native encoders can wrap/narrow canonical inputs | Target-width checked canonical inputs reject overflow/fractions; legacy vendor input forms retain their encoders |
| ADS Programs | Guessed MAIN/GVL before catalog | Published top-level namespaces, including an empty list for an empty catalog |

The normal unified numeric contract preserves integer precision (no integer to
float conversion). BOOL is bool, REAL is decoded at float32 precision then widened,
and records contain recursively conforming members. Flat arrays retain total Count
and last-dimension-fastest ordering; optional descriptions retain signed bounds.
Known opaque fallback buffers in other families retain their existing behavior.

ADS TIME/TOD are unsigned milliseconds; DATE/DT are unsigned seconds from
1970-01-01; DT storage is four bytes. LTIME is unsigned nanoseconds. New LDATE/LDT
values use signed nanoseconds from 1970-01-01, and LTOD uses unsigned nanoseconds
since midnight. Other families retain existing time units and semantics.

No other application is edited as part of this library change. Concrete consumer
dependencies found during implementation and remaining hardware gates are
recorded in `plcio-implementation-report.md`.
