# Independently checked SDK definitions

`beckhoff-sdk-constants.json` contains integer definitions extracted from the
ECMA-335 Constant table in Beckhoff's official ADS Abstractions SDK 7.0.339,
`lib/netstandard2.0/TwinCAT.Ads.Abstractions.dll`. The JSON records its SHA-256
and vendor package URL. This is independent test evidence for flags, service
groups and errors, rather than expected values generated from plcio.

The companion official `Beckhoff.TwinCAT.Ads` 7.0.339 XML documents the 42-byte
AdsDataTypeEntry header, field offsets, bits-versus-bytes semantics and optional
sections. The captured types independently establish attribute and enum lengths,
GUID positions and eight-byte zero padding. Original PLC captures remain under
`../beckhoff/` and are not regenerated or modified.

SDK inspection used a temporary Python dnfile reader, solely as development
tooling. plcio retains pure Go, standard-library-only runtime dependencies.
