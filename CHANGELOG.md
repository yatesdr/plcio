# Changelog

All notable changes to this project will be documented in this file.

## [0.3.3] - 2026-09-22

Production-hardening maintenance release. Hardware-verified on a ControlLogix L7, a
Micro820, an S7-1200 and a Beckhoff CX (TwinCAT 3); see
[docs/hardware-verification.md](docs/hardware-verification.md) for the
evidence and the remaining lab checklist (Omron, SLC/MicroLogix/PLC-5,
EtherNet/IP adapter, S7-300/400).

### Fixed: wrong data (hardware-confirmed)
- S7: one failed item in a batch read shifted later values onto the wrong tags.
- S7: single-bit reads other than `.0` always returned false.
- S7: REAL reads returned one byte, and REAL writes were rejected by the CPU.
- Logix: structures larger than the connection size returned only the 2-byte
  handle, without an error.
- Logix and Micro800: STRING writes were rejected (0x2107). Scalar and array
  strings now use the controller's native layout, with no truncation.
- Logix: array indexes such as `Arr[1,2]`, `Arr[-1]` and `Arr[x]` silently
  addressed the wrong element. Multi-dimensional indexes are now encoded
  correctly, and invalid ones are errors.

### Fixed: crashes, protocol and resource handling
- Panics on malformed packets: an EtherNet/IP CPF length overflow, Multiple
  Service reply offsets, and Logix additional-status slicing.
- The eipadapter crashed on a Forward_Close sent over the same connection.
- Logix Forward Close didn't match the Forward Open, leaving controller
  connections open. The originator serial is now random per connection.
- Connected replies are checked for connection ID and sequence number. S7
  responses are checked for PDU reference, function code and item count.
- EtherNet/IP and FINS/TCP framing errors now drop the connection instead of
  leaving the stream out of step.
- Truncated reads and partial transfers are errors, never short data returned
  as success.
- All driver adapters are safe for concurrent use. `Connect` closes the
  previous client.

### Fixed: silent truncation and unsafe writes
- Numeric writes of every Go numeric kind are range-checked. Previously only
  int64/uint64/float64 were checked, so e.g. a Go `int` of 300 to a SINT
  stored 44.
- S7:
  - An untyped write to an offset-only address is an error (it used to write
    8 bytes).
  - Oversized writes, over-length strings and out-of-range addresses are
    rejected.
- PCCC:
  - SLC/MicroLogix bit writes use the masked write (0xAB). PLC-5 uses
    Read-Modify-Write (0x26).
  - ST strings are byte-swapped correctly.
  - File-directory discovery uses the documented layout.
- Omron:
  - FINS 32/64-bit values use Omron's low-word-first order.
  - Non-fatal CPU status bits no longer fail every command.
  - The 0x0104 multi-read request and reply formats are corrected.
  - Bare `C`/`T` address prefixes are rejected as ambiguous (use `CIO`/`CNT`,
    `TK`/`TIM`).
  - Write bounds are enforced.
  - NJ/NX STRING, TIME and DATE types are handled.

### Added
- Logix: bit access on integer tags (`Tag.5`), with atomic writes (CIP 0x4E).
- S7:
  - TIME writes, and signed TIME reads.
  - S5TIME, DATE, TIME_OF_DAY, DATE_AND_TIME and DTL.
  - UTF-16 WSTRING.
  - Snap7-compatible timer/counter access.
  - Real CPU info (order number and firmware).
  - A real Keepalive.
- PLC-5: typed read/write (0x68/0x67) and octal I/O addressing.
- ADS:
  - Case-insensitive symbol names.
  - ReadState Keepalive.
  - UDP 48899 discovery that reports the device's real AMS Net ID.
  - Handle release on cache invalidation, and cache eviction instead of
    permanent limit errors.
- Discovery:
  - Unicast EtherNet/IP ListIdentity across the scan CIDR, which reaches
    routed subnets.
  - `DiscoverAllWithReport`, which also returns per-protocol errors.
- Driver:
  - `driver.ErrConnectionLost` and `driver.IsConnectionLost`.
  - Family names are case-insensitive.
- eipadapter:
  - Connection events and Run/Idle state.
  - Connection and session limits, and a watchdog from Forward_Open.
  - Forward_Open validation.
  - Prompt shutdown and panic recovery.

### Behaviour changes to review
- Invalid inputs that used to be silently truncated or redirected now return
  errors. This covers range, size, address and type checks, unknown PLC
  families, and ambiguous Omron `C`/`T` prefixes.
- S7 `Read` uses the data types configured in `PLCConfig.Tags` when the
  request has no hint.
- Logix `Driver.Read` always returns one result per request, in request order.
- FINS DINT/REAL/LREAL values change word order. Any application-side word
  swap must be removed.

## [0.3.2] - 2026-09-22

### Fixed
- EtherNet/IP adapter cyclic UDP output now sends raw CPF without an explicit-message
  encapsulation header or interface/timeout prefix.
- Forward_Open honors the originator's requested unicast receive port and returns
  socket-address information; the default receive port is 2222.
- Adapter output packets with incorrect assembly lengths or an unexpected source IP
  are ignored without refreshing the receive watchdog.
- The receive watchdog uses the O->T interval, expires connections with no initial
  traffic, avoids arithmetic overflow, and lets producer loops stop promptly.

### Compatibility and validation
- Public APIs, scanner/tag-access paths, and TCP explicit-message framing are unchanged.
  Legacy wrapped UDP input remains accepted; custom consumers expecting wrapped UDP
  output must accept raw CPF instead. Oversized assembly writes are no longer truncated.
- Packet fixtures, loopback I/O, and the full race-enabled test suite pass. Physical
  PLC interoperability and ODVA conformance have not been established for these changes.

## [0.3.0] - 2026-09-14

### Added
- Automatic published ADS record, packed BIT, alias/enum/subrange and flat array
  decoding in ordinary Driver.Read; optional caller-owned metadata descriptions.
- ReadDecoded bridge and ADS identity/resource/string-encoding options through
  NewADSAdapterWithOptions, preserving the original constructors and result layouts.
- Independent captured wire/schema fixtures, bounded parser fuzz targets, adapter
  TCP fixtures, opt-in read-only PLC smoke and soak tests.

### Fixed
- Logix template BOOL members use their published INFO bit positions, including positions
  beyond the first byte, while retaining existing template APIs.
- ADS framing/deadlines, short writes/errors, original-endpoint reconnect,
  deduplicated handles, bounded Close, cache generation invalidation and one
  deadline-bounded read recovery; value writes are never automatically replayed.
- Complete advertised catalog membership and namespaces; checked SumUp splitting,
  failed-slot offsets and explicit unsupported-service fallback.
- ADS catalog dimensions use the same generation and operation budget as names;
  whole-value writes enforce known child access without changing shared schemas.
- STRING encoding attributes apply through aliases and arrays, with explicit
  override precedence; schema validation bounds shared-graph work and CPU time.
- Direct TwinCAT packed-member lookups accept validated zero-padded parent
  names while preserving exact requested handles and strict catalog parsing.
- Correct ADS READONLY/service/error constants, unsigned TIME/TOD and 4-byte DT,
  Latin-1/UTF-8 STRING and UCS-2 WSTRING storage, exact write sizes/ranges/counts.
- Shared adapters retain partial successful results alongside connection errors.
  Omron/PCCC numbers and decoded primitive record arrays follow the shared Go
  categories; canonical numeric write inputs preserve integer precision.
- Omron CIP BOOL writes use one byte and typed CIP arrays carry their actual
  element count; FINS accepts complete BOOL slices.
- ADS discovery validates identity separately from route verification; ADS/S7/FINS
  IPv4 expansion and worker counts are bounded, with correct /31 and /32 handling.
- Omron TCP/discovery addresses use proper host/port construction.

### Compatibility and validation
- Minor feature release within the project's beta status. See the
  [migration contract](docs/plcio-compatibility.md) and
  [audit evidence and remaining hardware validation](docs/plcio-implementation-report.md).
- Read-only Beckhoff validation covers all 34 supplied variables and nine member
  paths. Logix scalar/UDT reads pass; Siemens testing establishes connectivity.
  Live writes, real online changes, Siemens value reads and Omron hardware tests
  remain unverified.
- Unified decoded representations, complete ADS browsing and stricter invalid
  writes are intentional behavior changes. Existing Driver methods, function
  signatures, shared configuration/result field layouts and native Bytes/type
  meanings remain source compatible.

## [0.2.0] - 2026-05-21

### Added
- **EtherNet/IP Adapter Mode**: new `eipadapter/` package implementing the
  adapter (server) side of EtherNet/IP. Lets a Go program be scanned by a PLC,
  exposing Identity / Message Router / Assembly / Connection Manager / TCP-IP
  Interface / Ethernet Link objects, responding to ListIdentity discovery, and
  producing Class 1 cyclic I/O at the negotiated RPI. Use for smart sensors,
  vision systems, and bench fixtures.
- **`eip` package**: exported `Frame` type with `ReadFrame`, `ParseFrame`,
  `BuildRRData`, `ParseRRData` helpers — symmetric server-side counterparts to
  the existing client primitives. Existing client-side `EipEncap` type and
  paths are unchanged.
- **`cip` package**: `ParseForwardOpenRequest`, `BuildForwardOpenSuccess`,
  `BuildForwardOpenError`, `ParseForwardCloseRequest`,
  `BuildForwardCloseSuccess`, and `ParsePath` for adapter-side use. Existing
  scanner-side Forward_Open builder and response parser are unchanged.

### Notes
- All additions are strictly additive. No existing scanner-side code paths
  are modified; v0.1.6 applications continue to work without changes.
- The adapter is not safety-rated. Do not use it as a permissive output in a
  safety function. See `docs/safety-and-intended-use.md`.

## [0.1.6] - 2026-03-16

### Fixed
- **PCCC Discovery Fallback**: SLC and MicroLogix discovery now falls back to
  parsing the EtherNet/IP `ListIdentity` product name when the PCCC Diagnostic
  Status probe (`CMD 0x06`) is rejected by the PLC. This restores file
  discovery on some SLC-500 controllers that return `STS=0x10`.

## [0.1.5] - 2026-02-18

### Added
- **PCCC Batch Reads**: Contiguous full-element reads in the same data file are
  automatically batched into a single PCCC round-trip. For example, reading
  `N7:0`, `N7:1`, `N7:2` issues one command instead of three. Each batch is
  capped at 236 bytes (the PCCC payload limit). Sub-element and bit-level reads
  remain individual. Failed batches fall back to individual reads automatically.

## [0.1.4] - 2026-02-18

### Added
- **PCCC Data Table Discovery**: SLC 500 and MicroLogix processors now support
  automatic data table discovery via the file directory (system file 0). The
  `AllTags()` method enumerates all configured data files with type and element
  count. PLC-5 does not support discovery (no file directory).

## [0.1.3] - 2026-02-18

### Added
- **PCCC Type Helpers**: `TypeCodeFromName()`, `SupportedTypeNames()`, and
  `TypeSize()` functions for mapping between type names and codes.

## [0.1.2] - 2026-02-18

### Added
- **PCCC Protocol Support**: New `pccc/` package for Allen-Bradley SLC 500,
  PLC-5, and MicroLogix processors using PCCC-over-EtherNet/IP (Execute PCCC
  service 0x4B on class 0x67). File-based data table addressing (N7:0, F8:5,
  B3:0/5, T4:0.ACC), read/write with automatic type decoding, bit-level
  read-modify-write, and Timer/Counter/Control sub-element maps.
- **PCCC Driver Adapter**: `driver.PCCCAdapter` implementing the unified `Driver`
  interface for `FamilySLC500`, `FamilyPLC5`, and `FamilyMicroLogix`.

## [0.1.1] - 2026-02-18

### Added
- **CIP Connection Path**: `ConnectionPath` field on `PLCConfig` for multi-hop
  Logix routing through communication modules (e.g., `"1,0,2,192.168.2.50"`).

## [0.1.0] - 2026-02-18

### Added
- Initial release after extraction from the warlink project.
- **Allen-Bradley Logix**: EtherNet/IP driver with tag discovery, batch reads
  (Multi-Service Packet), UDT/structure decoding, and write support.
- **Allen-Bradley Micro800**: Micro820/Micro850 support via EtherNet/IP.
- **Siemens S7**: S7comm driver with chunked reads/writes and string support.
- **Beckhoff TwinCAT**: ADS driver with symbol discovery and SumUp batching.
- **Omron FINS**: FINS TCP/UDP driver for CS/CJ/CP series.
- **Omron EIP**: Experimental EtherNet/IP driver for NJ/NX series.
- **Network Discovery**: Multi-protocol PLC scanning (EIP broadcast, S7 port
  scan, ADS broadcast, FINS scan).
- **Unified Driver Interface**: Vendor-agnostic `driver.Driver` for all families.
