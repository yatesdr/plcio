# v0.3.1 Logix read validation — 2026-09-14

Baseline: v0.3.0 (`76e5193992e9a838ca76f3ff59418e1462881c76`).
This patch fixes ordinary Logix reads of indexed UDT elements and standard STRING
members, including large fragmented records and small-buffer scalar batches.
It adds no public methods, configuration fields, dependencies or write features.
The module retains Go 1.24 minimum support.

## Resulting behavior

- Indexed/member paths derive their declared types from a discovered root and its
  templates, including program scope and nested arrays. Selected elements use
  count one and symbolic addressing, without inheriting the root instance or
  array count. Derived paths do not change catalog membership.
- Standard `STRING` and the observed controller alias `ASCIISTRING82` decode to
  Go text after checking the LEN/DATA layout and declared/available length.
  Nested standard strings and arrays of standard strings decode likewise.
  Other UDTs retain named member maps; malformed strings remain opaque.
- Native `logix.TagValue.DataType` uses resolved symbol/template metadata where
  available. The unified driver preserves that native code and original `Bytes`;
  its `Value` and `StableValue` contain decoded text/maps/arrays. In particular,
  a decoded standard STRING can retain a native structure code rather than 0xD0.
  `Client.DecodeUDT` continues to return a map at its top level.
- Large structure reads preserve the requested element count and advance
  fragmented offsets by payload bytes, excluding each response's type/handle
  descriptor. One handle prefix is retained in assembled raw storage. Missing
  data, changed types/handles, no progress and interrupted transfers return
  errors instead of successful partial records.
- Routed fragmentation accepts direct embedded replies and wrapped replies;
  Read Tag Fragmented and Unconnected Send share service 0x52.
- Scalar/member MSP batches fit both encoded requests and estimated atomic
  replies within the actual negotiated connection budget. Unconnected routing
  overhead and the connected sequence number are accounted for. ConnectionInfo
  and ConnectionMode now distinguish an active TCP transport from a CIP
  Forward Open connection.

Root metadata and templates are necessary to identify generic CIP structure
responses. Use the existing discovery/SetTags workflow, or ResolveTagType in
manual mode. The response's structure handle is not a template instance ID;
this patch never guesses a schema from that handle. No automatic detection of
arbitrary custom string-like UDTs is claimed.

The protocol reference is Rockwell Automation's
[Logix 5000 Controllers Data Access, 1756-PM020I](https://literature.rockwellautomation.com/idc/groups/literature/documents/pm/1756-pm020_-en-p.pdf),
especially the structure type descriptor and Read Tag Fragmented byte offset.
The LEN/DATA contract is also described in
[Logix 5000 Controllers General Instructions](https://literature.rockwellautomation.com/idc/groups/literature/documents/rm/1756-rm003_-en-p.pdf).

## Independent wire regressions

`logix/read_paths_test.go` uses a loopback EtherNet/IP peer with independent
synthetic records, distinct template IDs and structure handles, and real
Forward Open negotiation. It exercises:

| Messaging | Reply payloads | Reads checked |
|---|---|---|
| Direct unconnected | 480 and 32 bytes | Single STRING/record, mixed batch with failed slot, program STRING, two-record array and explicit count |
| Routed unconnected | 480 bytes; 32-byte direct/wrapped fragments | Same general cases plus both routed response forms |
| Standard Forward Open fallback, 504 bytes | 400 and 32 bytes | Single/count/batch/record-array reads and actual connection metadata |
| Large Forward Open, 4002 bytes | 3900 and 64 bytes | Single/count/batch/record-array reads and actual connection metadata |
| Individual-read path used by Micro800 | 32 bytes | Single/count/multiple selected paths and fragmented structures |

The synthetic record is 8,196 bytes with a final scalar after byte 8,192, so
successful decoding requires complete, correctly aligned fragmentation. The
record-array case preserves count two. Additional tests check indexed/program/
multidimensional paths, invalid bounds, empty/max-length/malformed STRINGs,
custom UDT preservation, and template IDs that collide with atomic type codes.

Nine fragment fault cases reject changed handles/types, empty/missing-handle/
oversized fragments, device errors, premature final replies, unexpected continued
partial status, and transport disconnection. Scalar batches with 50 long names
reproduced the baseline 2,108-byte request overflow on unconnected and 504-byte
connections; buffer-aware batching passes those cases and the 4002-byte case.
Short-path LINT batches separately enforce the reply-size budget.

`driver/logix_string_test.go` independently supplies Template Object attributes
and definitions over the wire. Single/mixed reads verify scalar text, typed
STRING arrays, nested record text, stable values, and unchanged native storage.
Native PLC single and MSP tests separately verify the raw structure descriptor,
unchanged storage, failed slots, and the top-level public DecodeUDT map contract.
The existing shared driver/public-surface compatibility fixtures also pass.

## Read-only hardware evidence

The owner's available Allen-Bradley controller passed both Large Forward Open
(4002 bytes) and routed unconnected reads. Each path discovered 23 tags, decoded
an indexed UDT element into all 12 members, read its two STRING members as text,
and decoded the whole 8,196-byte record into all 22 top-level members. Explicit
single-count reads, ordinary single reads and mixed batch reads agreed with
individual member reads, including a known scalar baseline. The two STRING
responses retained their original 90-byte storage; the indexed record retained
194 bytes. All clients closed and reported inactive transports afterwards.

The current Cletus manager also passed these indexed record/STRING reads against
an isolated candidate using a temporary modfile. No consumer-side binary decoder
or raised preview limit was needed. Consumer preview limits still apply to large
arrays, independently of complete native acquisition.

Only approved read operations and this client's connection setup/cleanup ran.
No variable writes, forces, route installation, runtime control or program edits
were performed. Employee contents and raw record payloads were examined only in
memory and withheld from retained evidence; no live personal values are fixtures.
Omron hardware was unavailable and skipped. Live writes, online layout changes,
and Micro800 hardware remain unverified by this patch.

## Release checks

Full normal and race suites on Go 1.27.1, the full race suite on Go 1.24.0,
unfiltered host vet, and build/vet checks for linux/amd64, linux/386,
windows/amd64, darwin/amd64 and darwin/arm64 passed for publication.
Platform compilation does not establish native Windows runtime behavior.
Previous v0.3.0 evidence remains dated and unchanged in the implementation report.
