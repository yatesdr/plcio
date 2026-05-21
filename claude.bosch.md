# Bosch Rexroth PLC Support — Research Notes

## Product Lines

### ctrlX CORE (current flagship, actively developed)
- Linux-based (ctrlX OS), app-based architecture
- Uses CODESYS V3 runtime for PLC logic
- Primary fieldbus: EtherCAT
- Also supports: PROFINET, EtherNet/IP, OPC UA, Modbus TCP, REST API, MQTT, SSE

### Legacy IndraControl (still sold, not strategic)
- IndraControl L (L10, L20, L40) — scalable IEC 61131-3 PLCs, CODESYS-based
- IndraControl XM (XM21, XM22, XM42) — embedded motion + PLC, XM22 has native OPC UA
- IndraControl V/VR — PLC + HMI combo panels
- IndraLogic XLC — high-performance, CODESYS V3
- Primary fieldbus: SERCOS III

---

## Option 1: ctrlX REST API (RECOMMENDED)

### Why
- Pure HTTPS + JSON — only needs Go stdlib (net/http, crypto/tls, encoding/json)
- Zero external dependencies, fits plcio's architecture perfectly
- OpenAPI specs published by Bosch Rexroth on GitHub: github.com/boschrexroth/rest-api-description
- Proven pure Go reference: Telegraf ctrlx_datalayer plugin (contributed by Bosch Rexroth, no CGO)
- Supports read, write, bulk operations, subscriptions (SSE), tag discovery (metadata browse)

### API Endpoints
| Operation | Method | Path |
|---|---|---|
| Authenticate | POST | `/identity-manager/api/v2/auth/token` (JSON: name, password -> Bearer token) |
| Read variable | GET | `/automation/api/v2/plc/app/Application/sym/<Program>/<Variable>` |
| Write variable | PUT | `/automation/api/v2/plc/app/Application/sym/<Program>/<Variable>` (JSON body) |
| Bulk read | PUT | `/automation/api/v2/bulk?type=read` (JSON array of {type, path}) |
| Subscribe (SSE) | GET | `/automation/api/v2/events?nodes=<path1>,<path2>&publishIntervalMs=1000` |
| Browse metadata | GET | `/automation/api/v2/<path>?type=metadata` |

### PLC Variable Path Format
`plc/app/Application/sym/<ProgramName>/<VariableName>`

### Driver Interface Mapping
- `Connect()` -> authenticate, get Bearer token
- `Read()` -> bulk PUT or individual GETs
- `Write()` -> PUT with JSON body
- `AllTags()` -> browse metadata tree recursively
- `Keepalive()` -> token refresh
- `GetDeviceInfo()` -> system info endpoint
- `Close()` -> no persistent connection (HTTP), just discard token

### Architecture in plcio
- New `ctrlx/` package alongside logix/, ads/, s7/, pccc/, omron/
- Unique: uses HTTPS transport instead of raw TCP
- Does NOT use eip/ or cip/ packages
- New driver adapter: `driver.CtrlXAdapter` with `FamilyCtrlX`

### Key Resources
- OpenAPI specs: github.com/boschrexroth/rest-api-description
- Interactive docs: boschrexroth.github.io/rest-api-description/ctrlx-automation/ctrlx-core/
- Telegraf plugin source (pure Go reference): github.com/influxdata/telegraf/tree/master/plugins/inputs/ctrlx_datalayer
- Official Go SDK (CGO, NOT what we want): github.com/boschrexroth/ctrlx-datalayer-golang
- Community how-tos: developer.community.boschrexroth.com

---

## Option 2: EtherNet/IP CIP (NEEDS MORE RESEARCH)

### What We Know
- ctrlX CORE supports EtherNet/IP in both Scanner and Adapter roles
- Adapter mode uses assembly instances (Instance 64 O->T, Instance 65 T->O, Config Instance 110)
- Available via CODESYS packages or native ctrlX OS app (ctrlX COREplus)
- plcio already has full EtherNet/IP CIP stack (eip/ and cip/ packages)

### Open Questions (need to research)
- Does ctrlX support CIP explicit messaging (Read Tag / Write Tag services)?
- Or is it assembly-instance-only (raw byte arrays manually mapped in CODESYS)?
- Does it support symbolic tag access like Rockwell Logix?
- Does it respond to ListIdentity broadcasts? What vendor ID / device type?
- Can variables be discovered over CIP, or only pre-configured assembly mappings?
- How does CODESYS V3 implement EtherNet/IP? Some CODESYS runtimes support CIP explicit messaging for named variables.

### Likely Limitations vs REST API
- Probably assembly-based (raw bytes), not symbolic tag access
- Requires manual variable mapping on the ctrlX side
- No tag discovery (assemblies are opaque byte blobs)
- Configuration overhead: user must map PLC vars to assembly bytes in CODESYS
- Advantage: potentially lower latency than HTTPS, real-time capable

### Verdict
- If ctrlX only does assembly-based EIP, it's clunky compared to REST
- If CODESYS exposes CIP explicit messaging with symbolic access, it could reuse existing plcio stack
- Worth investigating further before deciding

---

## Option 3: Other Protocols (lower priority)

### Modbus TCP
- Supported on ctrlX via Modbus TCP App or CODESYS libraries
- Generic protocol, not Rexroth-specific
- Register-based, requires manual address mapping
- plcio could add Modbus TCP as a generic driver (benefits many PLCs, not just Rexroth)

### OPC UA
- Supported on ctrlX (port 4840) and legacy XM22
- Open protocol but extremely complex to implement from scratch (thousands of pages of spec)
- Existing Go library: github.com/gopcua/opcua (would be external dependency)
- Not suitable for plcio's zero-dependency constraint without massive effort

---

## Recommended Implementation Order
1. **ctrlX REST API** — primary driver, covers modern Rexroth hardware
2. **EtherNet/IP CIP** — investigate further; if symbolic access works, add as alternative
3. **Modbus TCP** — future generic driver (benefits all vendors, not Rexroth-specific)
