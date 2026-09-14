# Packed member lookup captures — 2026-09-14

Four read-only F009 replies from the supplied Beckhoff PLC at TCP
`192.168.5.212:48898`, AMS `5.45.219.226.1.1`, runtime port 851. These were
captured while extending live checks to the owner's 34 variables and nine member
paths. Each file is the 86-byte symbol-info payload without the ADS result/length
or AMS/TCP envelopes. `sha256.json` records the original captured bytes.

The source is the uncommitted implementation candidate based on HEAD
`1431799176522451efdc205e09521007a0ab1d16`. Capture used a separate client and
existing metadata read APIs. No PLC value write, route, runtime or program action
was performed. The original `../beckhoff/` capture set remains unchanged.

All four replies reserve 35 bytes for the name field (declared name length 34
plus terminator), but contain `MAIN.test_bitpacked_struct` followed by nine zero
bytes. The type field is `BIT`; flags `0x08` include its GUID
`95190718000000000000000000000010`, matching the uploaded BIT datatype. Size is
one byte and native ADS type is BOOL (`0x21`). Index group is `0x4041`; offsets
are `3077200`, `3077201`, `3077202`, `3077203`. Uploaded parent storage is group
`0x4040`, offset `384650`, size one byte, with published bit positions 0–3.
Each bit address therefore equals `384650*8 + memberBitOffset`.

This observed lookup form is accepted only after validation against the complete
published parent layout and BIT GUID. Other lookup identities, nonzero interior
padding, changed addresses/GUIDs and malformed catalog names remain errors.
The library retains requested member paths for handle acquisition, decodes Go
`bool` values, and preserves the PLC's published `BIT` name in descriptions.

Normal tests only replay these files; they never contact the PLC or regenerate
them. Keep capture bytes immutable and store subsequent captures separately.
