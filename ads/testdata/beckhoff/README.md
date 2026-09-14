# Beckhoff regression inputs

These existing ADS payloads were captured read-only on 2026-09-14 from the supplied
test PLC: TCP `192.168.5.212:48898`, AMS Net ID `5.45.219.226.1.1`, runtime port
`851`. They were copied byte-for-byte into this repository for the
[implementation plan](../../../plcio-beckhoff-improvement-plan-2026-09-14.md).
Preparing this handoff did not contact the PLC or change library implementation.

The capture record identifies source revision
`1c19ef5071a008588a06da6dc77d78490b941d4a`; its ADS/driver implementation matches
the plan's reviewed baseline. The capture used metadata/value reads and symbolic
handles. It did not exercise PLC variable writes, online changes, or other PLC
families. These inputs establish captured bytes, not complete protocol coverage.

`sha256.json` records the original SHA-256 digest of each binary payload. Verify
these before promoting them into tests. All files are payload bytes without
AMS/TCP or ADS command-response headers; construct fake-peer envelopes separately.
The local `.gitattributes` preserves binary bytes across platform checkouts.

| File | Size | Checked interpretation |
|---|---:|---|
| `symbols.bin` | 3,440 | F00B symbol upload; 40 entries |
| `datatypes.bin` | 13,608 | F00E datatype upload; 75 entries, including extensions |
| `MAIN.test_struct.bin` | 124 | BYTE 15 at byte 0; DINT 25 at 4; SINT 5 at 8; STRING(80) at 9; eight DINTs 1..8 at 92 |
| `MAIN.test_bitpacked_struct.bin` | 1 | `0A`; BIT positions 0..3 are false, true, false, true |
| `MAIN.test_ltime.bin` | 8 | Unsigned little-endian nanoseconds: `8649040500600700` |
| `MAIN.test_2d_dint_array_style1.bin` | 12 | Six INT16s 1..6; nested array declaration, effective bounds [1..2,1..3] |
| `MAIN.test_2d_dint_array_style2.bin` | 12 | Same INT16 values/bounds, native multidimensional declaration |
| `MAIN.test_struct.my_byte.bin` | 1 | BYTE 15 |
| `MAIN.test_struct.my_dint.bin` | 4 | DINT 25 |
| `MAIN.test_struct.my_sint.bin` | 1 | SINT 5 |
| `MAIN.test_struct.my_string.bin` | 81 | `Test structure string`, terminated/padded |
| `MAIN.test_struct.my_dint_array.bin` | 32 | Eight DINTs 1..8 |

The ordinary structure's bytes 90-91 are alignment padding. Do not infer element
types from variable names: the two variables containing `dint` are INT arrays.
Verify layouts against uploaded metadata as well as expected values.

Keep these captures immutable. Store any later capture separately with its date,
target configuration, operation scope, checksums, and independently checked
expectations. Normal tests use local files and must never regenerate fixtures
by connecting to a PLC.
