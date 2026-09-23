# Hardware Verification Log

This log records what the production-hardening changes on top of v0.3.2 were
verified against on real hardware, and what still needs a PLC lab. Update the
"Result" columns as lab runs are completed.

Baseline: v0.3.2 (`682f6c7`). Candidate: branch `production-fixes-v032`.

## How hardware tests are run

- **Reads:** every tag or address is read singly and in a batch, first on
  unmodified v0.3.2 and then on the candidate. Every changed byte must be
  explained by an intended fix.
- **Writes:** each write is a read, then a write, a read-back, a restore of
  the original, and a read to confirm the restore.
  - Neighbouring variables are read before and after, so writes past the
    target are detected.
  - Writes that should be rejected must return an error and leave the value
    unchanged.
  - The run stops at the first value that fails to restore.
- **Reads use each tag's configured type** (`PLCConfig.Tags` for S7). Logix and
  Micro800 targets call `AllTags` + `SetTags` first, as warlink does.
- Only dedicated test variables are written. On the L7, that is the unused
  element `Employee_Data[999]`.

## 1. Verified on hardware (2026-09-22)

| Target | Firmware | Connection |
|---|---|---|
| ControlLogix L7 via 1756-EN2TR | EN2TR 5.28 | Large Forward Open (4002 B) |
| Micro820 2080-LC20-20QWB | 14.11 | Large Forward Open (4002 B); v0.3.2 fell back to unconnected |
| Siemens S7-1200 CPU 1214C 6ES7 214-1AG40-0XB0 | V4.4.1 | rack 0 / slot 0, PDU 240 |
| Beckhoff CX (CX-2DDBE2), TwinCAT 3.1.4024 | Plc30 App 3.1.1957 | ADS, Net ID 5.45.219.226.1.1, port 851 |

### Allen-Bradley ControlLogix L7

| Area | Result |
|---|---|
| Scalar, UDT and program-scoped reads (23 tags) | Pass; identical to v0.3.2 except as below |
| UDT larger than the connection (`Access_Card`, `z_RFIDeas`, 8196 B) | **Fixed.** v0.3.2 returned only the 2-byte handle with no error; now returns the full structure |
| DINT, SINT and UDT-member writes (`Employee_Data[999].*`) | Pass, restored |
| STRING writes (`First_Name`, `Last_Name`, up to 82 chars; `.LEN`) | **Fixed.** v0.3.2 was rejected with 0x2107 Size Too Small. 83 chars → capacity error |
| Bit of DINT (`Card_Number.5`, `.31`), read and atomic write (0x4E) | **Fixed.** v0.3.2 failed to read. Other bits unchanged |
| Range rejections (SINT ← 300 as int64 and Go `int`, DINT ← 3.7) | Pass; v0.3.2 wrapped Go `int` 300 to 44 |
| `Arr[9,99]` on a 1-D array | Rejected; v0.3.2 addressed element 999 |
| Keepalive, 4 concurrent goroutines × 10 reads | Pass |
| 3 simultaneous clients from one host | Pass (random originator serial) |
| Device info, unicast EtherNet/IP discovery across a routed subnet | Pass |

### Allen-Bradley Micro820

| Area | Result |
|---|---|
| Reads of 45 tags | Identical to v0.3.2 |
| Forward Open | **Fixed** (no backplane route for Micro800) |
| All numeric writes (BOOL through LREAL, TIME, array element), Go `int` | Pass, restored |
| STRING writes (scalar, array element, 3-element `[]string`) | **Fixed.** v0.3.2 was rejected with 0x2107. 256 chars → error |
| DINT bit read and write (`test_dint.3`) | Pass; the Micro820 supports 0x4E |
| Range rejections | Pass |
| Keepalive, concurrency, 3 simultaneous clients, discovery, device info | Pass |

### Siemens S7-1200 (DB1 test layout)

| Area | Result |
|---|---|
| Batch with one failing item (`DB9999.0` first) | **Fixed.** v0.3.2 put item values on the wrong tags |
| Single-bit reads other than `.0` (`DBX12.2`, `DBX21.1`–`.3`) | **Fixed.** v0.3.2 always returned false |
| REAL reads | **Fixed.** v0.3.2 returned 1 byte |
| REAL writes (`DBD16` configured REAL ← 1.5) | **Fixed.** Rejected with "data type/size mismatch" before |
| DINT, INT, USINT, UINT, DWORD, WORD, `DBW`/`DBD`, bit writes | Pass; neighbours unchanged |
| STRING, `STRING[4]`, `DINT[6]`, `UDINT[6]` (incl. 4294967295), `DINT[1001]` chunked, struct members | Pass |
| TIME (`DB1.8`): 2500, -100, 250 ms `time.Duration` | Pass. Sub-ms and out-of-range values are rejected |
| String over max length (300 → `STRING[254]`, 255 → array element) | Rejected; the candidate's first build truncated silently |
| Untyped offset-only write (`DB1.50`) | Rejected; v0.3.2 would write 8 bytes |
| Reads honour `PLCConfig.Tags` data types; a type hint on a sized address | Pass |
| Keepalive (SZL 0x0424), concurrency, device info (order number/firmware via SZL 0x0011) | Pass. The S7-1200 refuses SZL 0x001C (normal) |

### Beckhoff CX, TwinCAT 3

| Area | Result |
|---|---|
| Reads of all 40 symbols | Identical to v0.3.2 |
| Writes of every scalar type, STRING, WSTRING (non-ASCII), whole array, array element, struct members, bit-packed member | Pass, restored |
| Case-insensitive names (`main.test_int`) | **Fixed** (v0.3.2 errored) |
| Range and capacity rejections | Pass |
| `MAIN.test_bool` | The program assigns it every cycle; writes are overwritten (not a library issue) |
| Keepalive (ReadState) | Pass; errors when not connected, recognised by `driver.IsConnectionLost` |
| UDP 48899 discovery across a routed subnet | Pass: real Net ID, hostname and version. Also found a second TwinCAT system at 192.168.5.222 (Net ID 192.168.5.212.1.1) |

### Known limitations seen on hardware

- **Logix STRING/UDT decoding needs type information.** Without `SetTags` (or
  discovery), a structure reply only carries the generic 0x02A0 marker, so the
  value is returned as raw bytes.
- **A bare Logix array name reads element 0** unless `SetTags` supplied its
  dimensions. This is unchanged from v0.3.2.
- **Logix discovery reports every tag as writable.** The External Access
  attribute is not read.
- **Two hosts on the lab network answer on the S7 and ADS ports without being
  PLCs:** 192.168.5.82 (TCP 102 open, rejects COTP) and 192.168.5.80 (TCP 48898
  open, no discovery reply). Discovery correctly omits them.

## 2. Lab verification needed

Use scratch memory only, record each original value, and restore it
afterwards. Capture Wireshark with the relevant dissector for any failure.

### Omron CJ2 / CP1 (FINS), once with `Protocol: "fins-tcp"` and once with `"fins-udp"`

The lab's Omron was not reachable from the development network. It appears to
have a static IP on a subnet the lab router doesn't route; the factory default
is 192.168.250.1.

| # | Step | Expected | Result |
|---|---|---|---|
| 1 | Write DINT 0x12345678 to D30000 | CX-Programmer: D30000=0x5678, D30001=0x1234 | |
| 2 | Write REAL 1.0 to D30002 | D30002=0x0000, D30003=0x3F80 | |
| 3 | Write LREAL 1.0 to D30004 | D30004–06=0x0000, D30007=0x3FF0 | |
| 4 | Read all three back with type hints | Same values | |
| 5 | Create a non-fatal error (battery out / user FAL), read and write | Success; capture shows end code 0x0040 | |
| 6 | Address `C100` / `T5` | Parse error ("ambiguous") | |
| 7 | Read `TIM0`, `CNT0` | Match the PVs in CX-Programmer (area 0x89) | |
| 8 | Read ≥5 scattered words (D30000, D30010, W10, H10, CIO100) | One 0x0104 command; values match | |
| 9 | Write 997 words to `D30000[997]`, then 998 | 997 succeeds; 998 rejected before sending | |
| 10 | `fins-udp` Connect to an unused IP | Error within the timeout | |
| 11 | Read `TK0` while the task runs | true | |

### Omron NJ/NX (EtherNet/IP, `Protocol: "eip"`)

| # | Step | Expected | Result |
|---|---|---|---|
| 1 | STRING write "hello", read back | "hello" | |
| 2 | TIME ← 1500 ms and −1.5 s | Sysmac shows T#1s500ms / T#-1s500ms | |
| 3 | TIME_OF_DAY ← 13:45:30.123456789 | Matches in Sysmac | |
| 4 | DATE ← 2024-03-15 | D#2024-03-15 | |
| 5 | DATE_AND_TIME ← 2024-03-15 13:45:30.123; read `_CurrentTime` | Matches; the PLC clock reads as local wall time | |
| 6 | Note the reply type codes (0xDB vs 0x09 for TIME; 0x08/0x0A/0x0B) | Recorded | |
| 7 | BOOL write true/false, read back | Round-trips (2-byte BOOL format) | |
| 8 | Read an ENUM variable | Correct value | |

### SLC 5/05, MicroLogix 1400, PLC-5 (PCCC)

Scratch files: N50, F51, B52, T53, C54, R55, ST56, and on MicroLogix also L57.

| # | Step | Expected | Result |
|---|---|---|---|
| 1 | Set N50:0=1234, F51:0=1.5, B52:0=0xA5A5, T53:0.PRE=100, C54:0.PRE=5, R55:0.LEN=7, L57:0=100000, ST56:0="HELLO"; read whole elements plus .PRE/.ACC/.DN/.LEN and S:1 | Same values. On PLC-5, F51:0 ≈ 2.3e-41 means the word swap is wrong; capture the `plc5TypedRead ... param=` debug lines | |
| 2 | With B52:0=0xA5A5, write B52:0/1=1, then B52:0/0=0; T53:0.EN=1 | 0xA5A7, then 0xA5A6; nothing else changes | |
| 3 | ST56:0 ← "AB" and an 82-char string; check in RSLogix | Round-trips; on PLC-5, note whether the echoed-parameter typed write is accepted | |
| 4 | PLC-5 only: read `I:010/17` (input with an LED); `I:018` | Matches RSLogix (octal); `I:018` errors. Do not write outputs | |
| 5 | PLC-5: read N50:0..116 in one request; T53:0..1 | Correct | |
| 6 | `DiscoverDataFiles` on SLC 5/05 and ML1400 | Matches the RSLogix file list, element counts included | |
| 7 | Rejections: N50:0←70000, T53:0.PRE←-1, 83-char ST, L57:0/3 bit write, F51:0←1e39; on PLC-5, an F read or write on an N file | Errors, PLC unchanged | |
| 8 | SLC 5/05: read N50:0..117 (236 B) | One request succeeds | |

### EtherNet/IP adapter (`eipadapter`), scanned by a ControlLogix/CompactLogix

Generic Ethernet Module: input 101 (N bytes), output 102 (M bytes), config 103
(size 0), RPI 10 ms. Set `OnConnectionEvent` to log events and enable plcio
logging.

| # | Step | Expected | Result |
|---|---|---|---|
| 1 | Exclusive owner, unicast input | Module OK, then `opened` and `run` events; data both ways. In the Forward_Open reply, the T→O ID equals the request's. TCP/IP attr 5 and host name look correct | |
| 2 | Multicast input connection type | Accepted and served unicast (compatibility choice); note the PLC's behaviour | |
| 3 | Input-only (output = zero-size assembly 198); listen-only 199 | Input-only accepted; record the listen-only result | |
| 4 | PLC to Program mode and back | `idle` event, `RunIdle()` false; then `run` | |
| 5 | Pull the cable | `timed out` after RPI × multiplier; outputs keep their last values; a new `opened` after reconnect | |
| 6 | Inhibit the module | `closed` event | |
| 7 | Two scanners on one output assembly; the same triad twice; > MaxConnections | 0x0106; 0x0100; 0x0113 | |
| 8 | RPI 0.5 ms | 0x0111 | |
| 9 | Adapter shutdown (Ctrl-C) | `closed` events; the module faults promptly | |

### Siemens S7-300/400 (not available here)

| # | Step | Expected | Result |
|---|---|---|---|
| 1 | Read T5 / C3; write a timer/counter | Values match TIA/STEP 7 (Snap7 addressing, transport 0x1D/0x1C) | |
| 2 | Keepalive (SZL 0x0424) and device info (SZL 0x0011/0x001C) | Pass; names and serial populated where the CPU serves 0x001C | |
| 3 | DATE_AND_TIME read and write against a DT variable | Round-trips, including the weekday and 1990s years | |
| 4 | Rack ≠ 0 via `s7.WithRackSlot` (e.g. S7-400, slot 3) | Connects | |
| 5 | Chunked writes with a 240-byte PDU | Correct; a failure reports the bytes already written | |
| 6 | DTL, and WSTRING containing an emoji (S7-1200/1500) | Round-trips | |

### Other Logix and Beckhoff cases not covered by the lab PLCs

| # | Step | Expected | Result |
|---|---|---|---|
| 1 | Logix 2-D/3-D array tag: discovered dimensions | All dimensions reported | |
| 2 | Logix STRING array: write about 12 strings over a 504-byte connection | Write Tag Fragmented; read back identical | |
| 3 | Beckhoff: stop the runtime, then pull the cable, while calling Keepalive | `*ads.AdsError` while stopped; `IsConnectionLost` true after the cable pull | |
| 4 | Beckhoff: read many distinct `MAIN.arr[i]` paths with low metadata limits | No "limit exceeded"; values correct across evictions | |
| 5 | Beckhoff: TOD ≥ 24 h | Read returns the raw value; writing it is rejected | |
