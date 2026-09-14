# Changelog

All notable changes to this project will be documented in this file.

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
