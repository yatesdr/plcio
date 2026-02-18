# Changelog

All notable changes to this project will be documented in this file.

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
