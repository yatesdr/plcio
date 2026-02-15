package omron

import (
	"encoding/binary"
	"fmt"

	"github.com/yatesdr/plcio/logging"
)

// FINS command codes.
const (
	FINSCmdMemoryRead      uint16 = 0x0101 // Memory Area Read
	FINSCmdMemoryWrite     uint16 = 0x0102 // Memory Area Write
	FINSCmdMultiMemoryRead uint16 = 0x0104 // Multiple Memory Area Read (batch)
	FINSCmdCPURead         uint16 = 0x0501
	FINSCmdCPUStatus       uint16 = 0x0601
	FINSCmdCycleTime       uint16 = 0x0620
)

// FINS end codes.
const (
	FINSEndOK uint16 = 0x0000
)

// FINSHeader represents a FINS command/response header.
type FINSHeader struct {
	ICF byte   // Information Control Field
	RSV byte   // Reserved (0)
	GCT byte   // Gateway count
	DNA byte   // Destination network address
	DA1 byte   // Destination node address
	DA2 byte   // Destination unit address
	SNA byte   // Source network address
	SA1 byte   // Source node address
	SA2 byte   // Source unit address
	SID byte   // Service ID
}

// Bytes returns the header as a byte slice.
func (h *FINSHeader) Bytes() []byte {
	return []byte{h.ICF, h.RSV, h.GCT, h.DNA, h.DA1, h.DA2, h.SNA, h.SA1, h.SA2, h.SID}
}

// ParseFINSHeader parses a FINS header from bytes.
func ParseFINSHeader(data []byte) (*FINSHeader, error) {
	if len(data) < 10 {
		return nil, fmt.Errorf("FINS header too short: %d bytes", len(data))
	}
	return &FINSHeader{
		ICF: data[0],
		RSV: data[1],
		GCT: data[2],
		DNA: data[3],
		DA1: data[4],
		DA2: data[5],
		SNA: data[6],
		SA1: data[7],
		SA2: data[8],
		SID: data[9],
	}, nil
}

// FINSFrame represents a complete FINS frame.
type FINSFrame struct {
	Header  FINSHeader
	Command uint16
	Data    []byte
}

// Bytes returns the frame as a byte slice.
func (f *FINSFrame) Bytes() []byte {
	result := make([]byte, 12+len(f.Data))
	copy(result[0:10], f.Header.Bytes())
	binary.BigEndian.PutUint16(result[10:12], f.Command)
	copy(result[12:], f.Data)
	return result
}

// FINSResponse represents a FINS response.
type FINSResponse struct {
	Header  FINSHeader
	Command uint16 // Response command (original | 0x0100)
	EndCode uint16 // End code (0 = success)
	Data    []byte // Response data
}

// ParseFINSResponse parses a FINS response.
// Response format (per W227 FINS Commands Reference):
// - Header (10 bytes): ICF, RSV, GCT, DNA, DA1, DA2, SNA, SA1, SA2, SID
// - Command (2 bytes): Response command (original command)
// - End Code (2 bytes): Main code + Sub code (0x0000 = success)
// - Data (variable): Response data
func ParseFINSResponse(data []byte) (*FINSResponse, error) {
	if len(data) < 14 {
		logging.DebugLog("FINS", "Response too short: %d bytes (need 14 minimum for header+cmd+endcode)", len(data))
		logging.DebugLog("FINS", "Raw response bytes: %X", data)
		return nil, fmt.Errorf("FINS response too short: %d bytes", len(data))
	}

	header, err := ParseFINSHeader(data[0:10])
	if err != nil {
		logging.DebugLog("FINS", "Header parse error: %v", err)
		return nil, err
	}

	// ICF bit 6 should be 1 for response
	isResponse := (header.ICF & 0x40) != 0
	logging.DebugLog("FINS", "Header: ICF=0x%02X (isResponse=%v) GCT=%d DNA=%d DA1=%d DA2=%d SNA=%d SA1=%d SA2=%d SID=%d",
		header.ICF, isResponse, header.GCT, header.DNA, header.DA1, header.DA2,
		header.SNA, header.SA1, header.SA2, header.SID)

	resp := &FINSResponse{
		Header:  *header,
		Command: binary.BigEndian.Uint16(data[10:12]),
		EndCode: binary.BigEndian.Uint16(data[12:14]),
		Data:    data[14:],
	}

	// Log response details
	mainCode := resp.EndCode >> 8
	subCode := resp.EndCode & 0xFF
	logging.DebugLog("FINS", "Response: cmd=0x%04X endCode=0x%04X (main=0x%02X sub=0x%02X) dataLen=%d",
		resp.Command, resp.EndCode, mainCode, subCode, len(resp.Data))

	if len(resp.Data) > 0 && len(resp.Data) <= 64 {
		logging.DebugLog("FINS", "Response data: %X", resp.Data)
	} else if len(resp.Data) > 64 {
		logging.DebugLog("FINS", "Response data (first 64 bytes): %X...", resp.Data[:64])
	}

	return resp, nil
}

// BuildMemoryReadRequest builds a FINS memory read request.
func BuildMemoryReadRequest(area byte, address uint16, bitOffset byte, count uint16) []byte {
	data := make([]byte, 6)
	data[0] = area
	data[1] = byte(address >> 8)
	data[2] = byte(address)
	data[3] = bitOffset
	binary.BigEndian.PutUint16(data[4:6], count)
	logging.DebugLog("FINS", "MemoryRead request: area=0x%02X(%s) address=%d.%d count=%d",
		area, AreaName(area), address, bitOffset, count)
	return data
}

// BuildMemoryWriteRequest builds a FINS memory write request.
func BuildMemoryWriteRequest(area byte, address uint16, bitOffset byte, values []byte) []byte {
	// For word writes, count is number of words
	// For bit writes, count is number of bits
	var count uint16
	if IsBitArea(area) {
		count = uint16(len(values)) // Each byte is one bit
	} else {
		count = uint16(len(values) / 2) // Each word is 2 bytes
	}

	data := make([]byte, 6+len(values))
	data[0] = area
	data[1] = byte(address >> 8)
	data[2] = byte(address)
	data[3] = bitOffset
	binary.BigEndian.PutUint16(data[4:6], count)
	copy(data[6:], values)
	return data
}

// FINSEndCodeError returns a human-readable error for a FINS end code.
// Error codes are defined in the FINS Commands Reference Manual (W227-E1-2), Section 8.
func FINSEndCodeError(endCode uint16) error {
	if endCode == FINSEndOK {
		return nil
	}

	mainCode := endCode >> 8
	subCode := endCode & 0xFF
	logging.DebugLog("FINS", "EndCode error: 0x%04X (main=0x%02X sub=0x%02X)", endCode, mainCode, subCode)

	var msg string
	switch mainCode {
	case 0x00:
		switch subCode {
		case 0x00:
			msg = "Normal completion"
		case 0x01:
			msg = "Normal completion with warning: service canceled"
		default:
			msg = "Normal completion"
		}
	case 0x01:
		switch subCode {
		case 0x01:
			msg = "Local node error: local node not in network"
		case 0x02:
			msg = "Local node error: token timeout"
		case 0x03:
			msg = "Local node error: retries failed"
		case 0x04:
			msg = "Local node error: too many send frames"
		case 0x05:
			msg = "Local node error: node address range error"
		case 0x06:
			msg = "Local node error: node address duplication"
		default:
			msg = "Local node error"
		}
	case 0x02:
		switch subCode {
		case 0x01:
			msg = "Destination node error: destination node not in network"
		case 0x02:
			msg = "Destination node error: unit missing"
		case 0x03:
			msg = "Destination node error: third node missing"
		case 0x04:
			msg = "Destination node error: destination node busy"
		case 0x05:
			msg = "Destination node error: response timeout"
		default:
			msg = "Destination node error"
		}
	case 0x03:
		switch subCode {
		case 0x01:
			msg = "Controller error: communications controller error"
		case 0x02:
			msg = "Controller error: CPU unit error"
		case 0x03:
			msg = "Controller error: controller error"
		case 0x04:
			msg = "Controller error: unit number error"
		default:
			msg = "Communications controller error"
		}
	case 0x04:
		switch subCode {
		case 0x01:
			msg = "Not executable: undefined command"
		case 0x02:
			msg = "Not executable: not supported by model/version"
		default:
			msg = "Not executable"
		}
	case 0x05:
		switch subCode {
		case 0x01:
			msg = "Routing error: destination address not in routing tables"
		case 0x02:
			msg = "Routing error: no routing tables"
		case 0x03:
			msg = "Routing error: routing table error"
		case 0x04:
			msg = "Routing error: too many relays"
		default:
			msg = "Routing error"
		}
	case 0x10:
		switch subCode {
		case 0x01:
			msg = "Command format error: command too long"
		case 0x02:
			msg = "Command format error: command too short"
		case 0x03:
			msg = "Command format error: elements/data don't match"
		case 0x04:
			msg = "Command format error: command format error"
		case 0x05:
			msg = "Command format error: header error"
		default:
			msg = "Command format error"
		}
	case 0x11:
		switch subCode {
		case 0x01:
			msg = "Parameter error: area classification missing"
		case 0x02:
			msg = "Parameter error: access size error"
		case 0x03:
			msg = "Parameter error: address range error"
		case 0x04:
			msg = "Parameter error: address range exceeded"
		case 0x06:
			msg = "Parameter error: program missing"
		case 0x09:
			msg = "Parameter error: relational error"
		case 0x0A:
			msg = "Parameter error: duplicate data access"
		case 0x0B:
			msg = "Parameter error: response too long"
		case 0x0C:
			msg = "Parameter error: parameter error"
		default:
			msg = "Parameter error"
		}
	case 0x20:
		switch subCode {
		case 0x02:
			msg = "Read not possible: protected"
		case 0x03:
			msg = "Read not possible: table missing"
		case 0x04:
			msg = "Read not possible: data missing"
		case 0x05:
			msg = "Read not possible: program missing"
		case 0x06:
			msg = "Read not possible: file missing"
		case 0x07:
			msg = "Read not possible: data mismatch"
		default:
			msg = "Read not possible"
		}
	case 0x21:
		switch subCode {
		case 0x01:
			msg = "Write not possible: read-only"
		case 0x02:
			msg = "Write not possible: protected (cannot write to protected area during operation)"
		case 0x03:
			msg = "Write not possible: cannot register"
		case 0x05:
			msg = "Write not possible: program missing"
		case 0x06:
			msg = "Write not possible: file missing"
		case 0x07:
			msg = "Write not possible: file name already exists"
		case 0x08:
			msg = "Write not possible: cannot change"
		default:
			msg = "Write not possible"
		}
	case 0x22:
		switch subCode {
		case 0x01:
			msg = "Not executable in current mode: not possible during execution"
		case 0x02:
			msg = "Not executable in current mode: not possible while running"
		case 0x03:
			msg = "Not executable in current mode: wrong PLC mode (program mode required)"
		case 0x04:
			msg = "Not executable in current mode: wrong PLC mode (debug mode required)"
		case 0x05:
			msg = "Not executable in current mode: wrong PLC mode (monitor mode required)"
		case 0x06:
			msg = "Not executable in current mode: wrong PLC mode (run mode required)"
		case 0x07:
			msg = "Not executable in current mode: specified node not polling node"
		case 0x08:
			msg = "Not executable in current mode: step cannot be executed"
		default:
			msg = "Not executable in current mode"
		}
	case 0x23:
		switch subCode {
		case 0x01:
			msg = "No such device: file device missing"
		case 0x02:
			msg = "No such device: memory missing"
		case 0x03:
			msg = "No such device: clock missing"
		default:
			msg = "No such device"
		}
	case 0x24:
		switch subCode {
		case 0x01:
			msg = "Cannot start/stop: table missing"
		default:
			msg = "Cannot start/stop"
		}
	case 0x25:
		switch subCode {
		case 0x02:
			msg = "Unit error: memory error"
		case 0x03:
			msg = "Unit error: I/O setting error"
		case 0x04:
			msg = "Unit error: too many I/O points"
		case 0x05:
			msg = "Unit error: CPU bus error"
		case 0x06:
			msg = "Unit error: I/O duplication"
		case 0x07:
			msg = "Unit error: I/O bus error"
		case 0x09:
			msg = "Unit error: SYSMAC BUS/2 error"
		case 0x0A:
			msg = "Unit error: CPU bus unit error"
		case 0x0D:
			msg = "Unit error: SYSMAC BUS duplication"
		case 0x0F:
			msg = "Unit error: memory error"
		case 0x10:
			msg = "Unit error: SYSMAC BUS terminator missing"
		default:
			msg = "Unit error"
		}
	case 0x26:
		switch subCode {
		case 0x01:
			msg = "Command error: no protection"
		case 0x02:
			msg = "Command error: incorrect password"
		case 0x04:
			msg = "Command error: protected"
		case 0x05:
			msg = "Command error: service already executing"
		case 0x06:
			msg = "Command error: service stopped"
		case 0x07:
			msg = "Command error: no execution right"
		case 0x08:
			msg = "Command error: settings not complete"
		case 0x09:
			msg = "Command error: necessary items not set"
		case 0x0A:
			msg = "Command error: number already defined"
		case 0x0B:
			msg = "Command error: error will not clear"
		default:
			msg = "Command error"
		}
	case 0x30:
		switch subCode {
		case 0x01:
			msg = "Abort: no memory card"
		case 0x02:
			msg = "Abort: memory card type error"
		case 0x03:
			msg = "Abort: I/O point overflow"
		default:
			msg = "Abort"
		}
	case 0x40:
		switch subCode {
		case 0x01:
			msg = "Fatal error: transmission failure"
		default:
			msg = "Fatal error: communications error"
		}
	default:
		msg = "Unknown error"
	}

	return fmt.Errorf("FINS error 0x%04X: %s", endCode, msg)
}

// CPUStatus represents the CPU operating status.
type CPUStatus struct {
	Running bool
	Mode    byte // 0=Program, 1=Debug, 2=Monitor, 4=Run
}

// ParseCPUStatus parses a CPU status response.
func ParseCPUStatus(data []byte) (*CPUStatus, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("CPU status response too short")
	}
	return &CPUStatus{
		Running: data[0] != 0,
		Mode:    data[1],
	}, nil
}

// CycleTime represents CPU cycle time information.
type CycleTime struct {
	Average uint32 // in 0.1ms units
	Max     uint32 // in 0.1ms units
	Min     uint32 // in 0.1ms units
}

// ParseCycleTime parses a cycle time response.
func ParseCycleTime(data []byte) (*CycleTime, error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("cycle time response too short")
	}
	return &CycleTime{
		Average: binary.BigEndian.Uint32(data[0:4]),
		Max:     binary.BigEndian.Uint32(data[4:8]),
		Min:     binary.BigEndian.Uint32(data[8:12]),
	}, nil
}
