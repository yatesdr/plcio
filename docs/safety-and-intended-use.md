# Safety, Intended Use & Legal Disclaimers

## IMPORTANT: READ BEFORE USE

**Programmable Logic Controllers (PLCs) control physical equipment in industrial environments including manufacturing lines, chemical processes, power generation, water treatment, HVAC systems, and other critical infrastructure. Improper interaction with PLCs can cause equipment damage, environmental harm, serious injury, or death.**

This document describes the intended use of plcio, its limitations, and your responsibilities as a user.

---

## Disclaimer of Warranty

**THIS SOFTWARE IS PROVIDED "AS IS" WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE, AND NONINFRINGEMENT.**

The authors and contributors of plcio make no representations or warranties regarding:
- The accuracy, reliability, or completeness of data read from PLCs
- The successful delivery or execution of write commands
- The continuous availability or stability of PLC connections
- The suitability of this software for any particular application
- The correctness of protocol implementations for untested hardware

**YOU USE THIS SOFTWARE ENTIRELY AT YOUR OWN RISK.**

---

## Limitation of Liability

**IN NO EVENT SHALL THE AUTHORS, CONTRIBUTORS, OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES, OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT, OR OTHERWISE, ARISING FROM, OUT OF, OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.**

This includes but is not limited to:
- Damage to equipment, machinery, or facilities
- Personal injury or death
- Environmental damage or regulatory violations
- Production losses or business interruption
- Data loss or corruption
- Consequential, incidental, special, or punitive damages of any kind

---

## Intended Use

plcio is designed for **monitoring and data collection** in industrial environments. Its primary purpose is to:

- **Read** tag values from PLCs for data logging, analytics, and visualization
- **Publish** PLC data to IT systems (MQTT brokers, databases, message queues)
- **Discover** PLCs and their available tags on a network
- **Write** occasional acknowledgment flags and status codes back to PLCs

### Appropriate Use Cases

- Data historians and time-series databases
- SCADA dashboards and HMI data feeds
- Condition monitoring and predictive maintenance
- OEE (Overall Equipment Effectiveness) calculation
- Production reporting and quality tracking
- Alarm and event logging
- IT/OT integration bridges

### Write Operations

The write functionality in plcio is **not optimized for high-throughput writing** and is specifically designed for:

- **Acknowledgment responses** &mdash; Writing a status code (e.g., 1 = success, -1 = error) to confirm that data was received
- **Handshake flags** &mdash; Setting/clearing flags as part of a request-acknowledge pattern
- **Occasional parameter updates** &mdash; Infrequent changes to setpoints or configuration values

**Write operations are single-tag, best-effort, and non-transactional.** There is no guarantee of delivery timing, atomicity, or ordering when writing multiple tags.

---

## What plcio is NOT

plcio is **NOT** suitable for and must **NEVER** be used for:

### Machine Control
- Executing automation logic or control sequences
- Replacing or supplementing PLC programs
- Motion control, positioning, or servo operations
- Any closed-loop control (PID, temperature, pressure, flow)

### Safety Functions
- Emergency stop (E-stop) circuits or safety interlocks
- Safety-rated operations of any kind (SIL, PLe, Cat 3/4)
- Guard monitoring or safety light curtain management
- Any function where failure could result in injury

### Real-Time Operations
- Time-critical control loops requiring deterministic timing
- Operations requiring guaranteed response times
- Synchronization with PLC scan cycles
- Any task requiring sub-millisecond precision

### High-Frequency Writing
- Rapid successive writes to the same tag
- Streaming data into PLC memory
- PWM, pulse output, or high-speed counting
- Any operation requiring sustained write throughput

---

## The Correct Write-Back Pattern

When write operations are needed, follow this pattern where the **PLC maintains full control of all process logic**:

```
1. PLC controls all equipment and logic internally
2. PLC signals readiness (sets a "data ready" flag)
3. plcio reads the flag and associated data
4. plcio processes/publishes the data
5. plcio writes an acknowledgment to a DEDICATED tag:
   - 1 = success
   - -1 = error
   - 0 = not yet processed
6. PLC reads the ack tag and continues its logic
```

### Critical Rules for Write-Back

1. **Use dedicated write-back tags** &mdash; Tags written by plcio should be controlled ONLY by plcio and read ONLY by the PLC. Never share tags between PLC logic and external writes.

2. **PLC must be the authority** &mdash; The PLC program must handle the case where the acknowledgment never arrives (timeout logic). plcio writes are best-effort.

3. **Use DINT for status codes** &mdash; DINT (32-bit signed integer) is the most reliable data type for write-back across all PLC families.

4. **Never write to output tags** &mdash; Never write to tags that directly control physical outputs (motors, valves, relays).

5. **Never write to safety tags** &mdash; Never write to any tag involved in safety functions.

---

## Network Security Considerations

PLC communication protocols (EtherNet/IP, S7comm, ADS, FINS) were designed for use on isolated industrial networks and generally **do not include authentication or encryption**. When using plcio:

- **Network segmentation** &mdash; Keep PLC networks isolated from corporate/internet networks using firewalls, VLANs, or DMZ architectures
- **Access control** &mdash; Restrict which hosts can communicate with PLCs
- **Monitoring** &mdash; Log and monitor all PLC communication for unauthorized access
- **Principle of least privilege** &mdash; Only enable the PLC protocols and ports that are actually needed
- **Physical security** &mdash; Ensure physical access to PLC networks is restricted

plcio does not implement authentication, encryption, or access control. These must be provided by your network infrastructure.

---

## Regulatory Compliance

Depending on your industry, PLC systems may be subject to regulations including:

- **IEC 62443** &mdash; Industrial automation and control systems security
- **NIST SP 800-82** &mdash; Guide to ICS security
- **ISA/IEC 62443** &mdash; Security for industrial automation
- **FDA 21 CFR Part 11** &mdash; Electronic records in pharmaceutical manufacturing
- **NERC CIP** &mdash; Critical infrastructure protection for power systems

**It is your responsibility** to ensure that your use of plcio complies with all applicable regulations, standards, and organizational policies.

---

## Testing and Validation

Before deploying plcio in any environment:

1. **Test thoroughly** in a non-production environment with isolated PLCs
2. **Verify all tag addresses** and type hints against the PLC program
3. **Test failure scenarios** &mdash; Disconnect cables, stop PLCs, introduce network delays
4. **Validate write-back logic** &mdash; Ensure the PLC handles missing/late acknowledgments correctly
5. **Monitor resource usage** &mdash; CPU, memory, and network bandwidth on both the plcio host and the PLC
6. **Document your configuration** &mdash; Maintain records of which tags are read and written, by which systems

---

## Reporting Issues

If you discover a bug, unexpected behavior, or potential safety concern:

1. **Stop using the affected feature immediately** in production
2. Report the issue on GitHub with detailed reproduction steps
3. Include the PLC family, firmware version, and plcio version
4. Do not assume the issue is isolated to your environment

---

## Summary

| | Appropriate | Not Appropriate |
|---|---|---|
| **Reading** | Data logging, monitoring, analytics | N/A |
| **Writing** | Acknowledgments, status codes, occasional parameters | Control logic, safety functions, high-frequency updates |
| **Timing** | Seconds to minutes polling cycles | Millisecond-precision, real-time, deterministic |
| **Reliability** | Best-effort with application-level retry | Guaranteed delivery, safety-critical |

**When in doubt, do not write to the PLC.** Reading is always safer than writing. If your use case requires writing, consult with the automation engineer responsible for the PLC program before configuring any write operations.
