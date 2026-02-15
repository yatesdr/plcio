// Package ads implements the Beckhoff ADS (Automation Device Specification) protocol
// for communicating with TwinCAT PLCs.
package ads

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync/atomic"

	"plcio/logging"
)

// ADS TCP Header (6 bytes)
// The AMS/TCP header wraps all ADS communication over TCP.
type tcpHeader struct {
	Reserved uint16 // Always 0
	Length   uint32 // Length of AMS header + data
}

// AMS Header (32 bytes)
// Every ADS command has an AMS header identifying source/target and command.
type amsHeader struct {
	TargetNetId [6]byte // Target AMS Net ID
	TargetPort  uint16  // Target AMS port
	SourceNetId [6]byte // Source AMS Net ID
	SourcePort  uint16  // Source AMS port
	CommandId   uint16  // ADS command ID
	StateFlags  uint16  // State flags (request/response, etc.)
	DataLength  uint32  // Length of ADS data following header
	ErrorCode   uint32  // ADS error code (0 = success)
	InvokeId    uint32  // Invoke ID for matching request/response
}

// ADS Command IDs
const (
	CmdReadDeviceInfo       uint16 = 0x0001
	CmdRead                 uint16 = 0x0002
	CmdWrite                uint16 = 0x0003
	CmdReadState            uint16 = 0x0004
	CmdWriteControl         uint16 = 0x0005
	CmdAddDeviceNotify      uint16 = 0x0006
	CmdDeleteDeviceNotify   uint16 = 0x0007
	CmdDeviceNotification   uint16 = 0x0008
	CmdReadWrite            uint16 = 0x0009
)

// ADS State Flags
const (
	StateFlagRequest  uint16 = 0x0004 // This is a request
	StateFlagResponse uint16 = 0x0005 // This is a response (request | 0x0001)
	StateFlagAdsCmd   uint16 = 0x0004 // ADS command
)

// ADS Index Groups for symbol access
const (
	IndexGroupSymbolTable         uint32 = 0xF000 // Symbol table
	IndexGroupSymbolName          uint32 = 0xF001 // Symbol name
	IndexGroupSymbolValue         uint32 = 0xF002 // Symbol value
	IndexGroupSymbolHandleByName  uint32 = 0xF003 // Get handle by symbol name
	IndexGroupSymbolValueByName   uint32 = 0xF005 // Read value by symbol name
	IndexGroupSymbolValueByHandle uint32 = 0xF005 // Read/write value by handle
	IndexGroupSymbolReleaseHandle uint32 = 0xF006 // Release handle
	IndexGroupSymbolInfoByName    uint32 = 0xF007 // Get symbol info by name
	IndexGroupSymbolVersion       uint32 = 0xF008 // Symbol version
	IndexGroupSymbolInfoByNameEx  uint32 = 0xF009 // Extended symbol info by name
	IndexGroupDataTypeInfoByNameEx uint32 = 0xF00A // Data type info by name
	IndexGroupSymbolUpload        uint32 = 0xF00B // Upload symbol table
	IndexGroupSymbolUploadInfo    uint32 = 0xF00C // Upload symbol info (count, size)
	IndexGroupDataTypeUpload      uint32 = 0xF00E // Upload data types
	IndexGroupSymbolUploadInfo2   uint32 = 0xF00F // Upload symbol info v2

	// SumUp commands for batched read/write operations
	IndexGroupSumUpRead      uint32 = 0xF080 // Read multiple values in one request
	IndexGroupSumUpWrite     uint32 = 0xF081 // Write multiple values in one request
	IndexGroupSumUpReadWrite uint32 = 0xF082 // Read/write multiple in one request
)

// ADS Ports
const (
	PortLogger      uint16 = 100  // Logger
	PortEventLog    uint16 = 110  // Event log
	PortIO          uint16 = 300  // I/O
	PortNC          uint16 = 500  // NC
	PortPLC1        uint16 = 801  // TwinCAT 2 PLC Runtime 1
	PortPLC2        uint16 = 811  // TwinCAT 2 PLC Runtime 2
	PortTC3PLC1     uint16 = 851  // TwinCAT 3 PLC Runtime 1
	PortTC3PLC2     uint16 = 852  // TwinCAT 3 PLC Runtime 2
	PortCamshaft    uint16 = 900  // Camshaft controller
	PortSystemService uint16 = 10000 // System service
)

// Default ADS TCP port
const DefaultTCPPort = 48898

// invokeIdCounter is used to generate unique invoke IDs for requests.
var invokeIdCounter uint32

// nextInvokeId returns the next unique invoke ID.
func nextInvokeId() uint32 {
	return atomic.AddUint32(&invokeIdCounter, 1)
}

// adsConnection handles the low-level TCP connection for ADS communication.
type adsConnection struct {
	conn       net.Conn
	localNetId AmsNetId
	localPort  uint16
}

// newAdsConnection creates a new ADS connection.
func newAdsConnection(conn net.Conn, localNetId AmsNetId, localPort uint16) *adsConnection {
	return &adsConnection{
		conn:       conn,
		localNetId: localNetId,
		localPort:  localPort,
	}
}

// sendRequest sends an ADS request and returns the response data.
func (c *adsConnection) sendRequest(targetNetId AmsNetId, targetPort uint16, cmdId uint16, data []byte) ([]byte, error) {
	invokeId := nextInvokeId()

	// Build AMS header
	amsHdr := amsHeader{
		TargetNetId: targetNetId,
		TargetPort:  targetPort,
		SourceNetId: c.localNetId,
		SourcePort:  c.localPort,
		CommandId:   cmdId,
		StateFlags:  StateFlagRequest,
		DataLength:  uint32(len(data)),
		ErrorCode:   0,
		InvokeId:    invokeId,
	}

	// Build TCP header
	tcpHdr := tcpHeader{
		Reserved: 0,
		Length:   32 + uint32(len(data)), // AMS header (32) + data
	}

	// Serialize and send
	buf := make([]byte, 6+32+len(data))
	binary.LittleEndian.PutUint16(buf[0:2], tcpHdr.Reserved)
	binary.LittleEndian.PutUint32(buf[2:6], tcpHdr.Length)

	copy(buf[6:12], amsHdr.TargetNetId[:])
	binary.LittleEndian.PutUint16(buf[12:14], amsHdr.TargetPort)
	copy(buf[14:20], amsHdr.SourceNetId[:])
	binary.LittleEndian.PutUint16(buf[20:22], amsHdr.SourcePort)
	binary.LittleEndian.PutUint16(buf[22:24], amsHdr.CommandId)
	binary.LittleEndian.PutUint16(buf[24:26], amsHdr.StateFlags)
	binary.LittleEndian.PutUint32(buf[26:30], amsHdr.DataLength)
	binary.LittleEndian.PutUint32(buf[30:34], amsHdr.ErrorCode)
	binary.LittleEndian.PutUint32(buf[34:38], amsHdr.InvokeId)

	if len(data) > 0 {
		copy(buf[38:], data)
	}

	logging.DebugTX("ADS", buf)

	_, err := c.conn.Write(buf)
	if err != nil {
		logging.DebugError("ADS", "sendRequest write", err)
		return nil, fmt.Errorf("write request: %w", err)
	}

	// Read response
	return c.readResponse(invokeId)
}

// readResponse reads an ADS response from the connection.
func (c *adsConnection) readResponse(expectedInvokeId uint32) ([]byte, error) {
	// Read TCP header (6 bytes)
	tcpBuf := make([]byte, 6)
	if _, err := io.ReadFull(c.conn, tcpBuf); err != nil {
		logging.DebugError("ADS", "readResponse read TCP header", err)
		return nil, fmt.Errorf("read TCP header: %w", err)
	}

	length := binary.LittleEndian.Uint32(tcpBuf[2:6])
	if length < 32 {
		logging.DebugLog("ADS", "Invalid AMS length: %d", length)
		return nil, fmt.Errorf("invalid AMS length: %d", length)
	}

	// Read AMS header + data
	amsBuf := make([]byte, length)
	if _, err := io.ReadFull(c.conn, amsBuf); err != nil {
		logging.DebugError("ADS", "readResponse read AMS data", err)
		return nil, fmt.Errorf("read AMS data: %w", err)
	}

	// Log complete received packet (TCP header + AMS data)
	fullPacket := append(tcpBuf, amsBuf...)
	logging.DebugRX("ADS", fullPacket)

	// Parse AMS header
	var respHdr amsHeader
	copy(respHdr.TargetNetId[:], amsBuf[0:6])
	respHdr.TargetPort = binary.LittleEndian.Uint16(amsBuf[6:8])
	copy(respHdr.SourceNetId[:], amsBuf[8:14])
	respHdr.SourcePort = binary.LittleEndian.Uint16(amsBuf[14:16])
	respHdr.CommandId = binary.LittleEndian.Uint16(amsBuf[16:18])
	respHdr.StateFlags = binary.LittleEndian.Uint16(amsBuf[18:20])
	respHdr.DataLength = binary.LittleEndian.Uint32(amsBuf[20:24])
	respHdr.ErrorCode = binary.LittleEndian.Uint32(amsBuf[24:28])
	respHdr.InvokeId = binary.LittleEndian.Uint32(amsBuf[28:32])

	// Verify invoke ID
	if respHdr.InvokeId != expectedInvokeId {
		return nil, fmt.Errorf("invoke ID mismatch: expected %d, got %d", expectedInvokeId, respHdr.InvokeId)
	}

	// Check for AMS-level error
	if respHdr.ErrorCode != 0 {
		return nil, &AdsError{Code: respHdr.ErrorCode}
	}

	// Return data portion
	return amsBuf[32:], nil
}

// close closes the underlying TCP connection.
func (c *adsConnection) close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// AdsError represents an ADS protocol error.
type AdsError struct {
	Code uint32
}

func (e *AdsError) Error() string {
	return fmt.Sprintf("ADS error 0x%08X: %s", e.Code, adsErrorName(e.Code))
}

// Common ADS error codes
const (
	ErrNoError              uint32 = 0x0000
	ErrInternal             uint32 = 0x0001
	ErrNoRuntime            uint32 = 0x0002
	ErrAllocLockedMem       uint32 = 0x0003
	ErrInsertMailbox        uint32 = 0x0004
	ErrWrongHMsg            uint32 = 0x0005
	ErrTargetPortNotFound   uint32 = 0x0006
	ErrTargetMachineNotFound uint32 = 0x0007
	ErrUnknownCmdId         uint32 = 0x0008
	ErrBadTaskId            uint32 = 0x0009
	ErrNoIO                 uint32 = 0x000A
	ErrUnknownAmsCmd        uint32 = 0x000B
	ErrWin32Error           uint32 = 0x000C
	ErrPortNotConnected     uint32 = 0x000D
	ErrInvalidAmsLength     uint32 = 0x000E
	ErrInvalidAmsNetId      uint32 = 0x000F
	ErrLowInstLevel         uint32 = 0x0010
	ErrNoDebugInfo          uint32 = 0x0011
	ErrPortDisabled         uint32 = 0x0012
	ErrPortAlreadyConnected uint32 = 0x0013
	ErrAmsSync              uint32 = 0x0014
	ErrAmsSyncSendError     uint32 = 0x0015
	ErrAmsNoSync            uint32 = 0x0016
	ErrNoIndexMap           uint32 = 0x0017
	ErrInvalidAmsPort       uint32 = 0x0018
	ErrNoMemory             uint32 = 0x0019
	ErrTcpSend              uint32 = 0x001A
	ErrHostUnreachable      uint32 = 0x001B
	ErrInvalidAmsFragment   uint32 = 0x001C
	ErrTlsSend              uint32 = 0x001D
	ErrAccessDenied         uint32 = 0x001E

	// Router errors
	ErrRouterNoLockedMem    uint32 = 0x0500
	ErrRouterResizeMem      uint32 = 0x0501
	ErrRouterMailboxFull    uint32 = 0x0502
	ErrRouterDebugboxFull   uint32 = 0x0503
	ErrRouterUnknownPortType uint32 = 0x0504
	ErrRouterNotInitialized uint32 = 0x0505
	ErrRouterPortRemoved    uint32 = 0x0506
	ErrRouterPortNotOpen    uint32 = 0x0507
	ErrRouterPortOpen       uint32 = 0x0508
	ErrRouterPortConnected  uint32 = 0x0509
	ErrRouterPortNotConnected uint32 = 0x050A
	ErrRouterNoSendQueue    uint32 = 0x050B

	// Device/ADS errors
	ErrDeviceError         uint32 = 0x0700
	ErrDeviceSrvNotSupp    uint32 = 0x0701
	ErrDeviceInvalidGrp    uint32 = 0x0702
	ErrDeviceInvalidOffs   uint32 = 0x0703
	ErrDeviceInvalidAccess uint32 = 0x0704
	ErrDeviceInvalidSize   uint32 = 0x0705
	ErrDeviceInvalidData   uint32 = 0x0706
	ErrDeviceNotReady      uint32 = 0x0707
	ErrDeviceBusy          uint32 = 0x0708
	ErrDeviceInvalidContext uint32 = 0x0709
	ErrDeviceNoMemory      uint32 = 0x070A
	ErrDeviceInvalidParam  uint32 = 0x070B
	ErrDeviceNotFound      uint32 = 0x070C
	ErrDeviceSyntax        uint32 = 0x070D
	ErrDeviceIncompatible  uint32 = 0x070E
	ErrDeviceExists        uint32 = 0x070F
	ErrDeviceSymbolNotFound uint32 = 0x0710
	ErrDeviceSymbolVersionInvalid uint32 = 0x0711
	ErrDeviceInvalidState  uint32 = 0x0712
	ErrDeviceTransModeNotSupp uint32 = 0x0713
	ErrDeviceNotifyHndInvalid uint32 = 0x0714
	ErrDeviceClientUnknown uint32 = 0x0715
	ErrDeviceNoMoreHdls    uint32 = 0x0716
	ErrDeviceInvalidWatchSize uint32 = 0x0717
	ErrDeviceNotInit       uint32 = 0x0718
	ErrDeviceTimeout       uint32 = 0x0719
	ErrDeviceNoInterface   uint32 = 0x071A
	ErrDeviceInvalidInterface uint32 = 0x071B
	ErrDeviceInvalidClsId  uint32 = 0x071C
	ErrDeviceInvalidObjId  uint32 = 0x071D
	ErrDevicePending       uint32 = 0x071E
	ErrDeviceAborted       uint32 = 0x071F
	ErrDeviceWarning       uint32 = 0x0720
	ErrDeviceInvalidArrayIdx uint32 = 0x0721
	ErrDeviceSymbolNotActive uint32 = 0x0722
	ErrDeviceAccessDenied  uint32 = 0x0723
	ErrDeviceLicenseNotFound uint32 = 0x0724
	ErrDeviceLicenseExpired uint32 = 0x0725
	ErrDeviceLicenseExceeded uint32 = 0x0726
	ErrDeviceLicenseInvalid uint32 = 0x0727
	ErrDeviceLicenseSystemId uint32 = 0x0728
	ErrDeviceLicenseNoTimeLimit uint32 = 0x0729
	ErrDeviceLicenseTime   uint32 = 0x072A
	ErrDeviceLicenseType   uint32 = 0x072B
	ErrDeviceLicensePlatform uint32 = 0x072C
	ErrDeviceException     uint32 = 0x072D
	ErrDeviceLicenseFile   uint32 = 0x072E
	ErrDeviceInvalidSignature uint32 = 0x072F
	ErrDeviceCertInvalid   uint32 = 0x0730
	ErrDeviceLicenseOemNotFound uint32 = 0x0731
	ErrDeviceLicenseRestricted uint32 = 0x0732
	ErrDeviceLicenseDemoDenied uint32 = 0x0733
	ErrDeviceInvalidFncId  uint32 = 0x0734
	ErrDeviceOutOfRange    uint32 = 0x0735
	ErrDeviceInvalidAlignment uint32 = 0x0736
	ErrDeviceLicensePlatformLevel uint32 = 0x0737
	ErrDeviceContextFwd    uint32 = 0x0738
	ErrDevicePortDisabled  uint32 = 0x0739
	ErrDevicePortConnected uint32 = 0x073A
	ErrDeviceInvalidQualifier uint32 = 0x073B
	ErrDeviceInvalidMailbox uint32 = 0x073C
)

func adsErrorName(code uint32) string {
	switch code {
	case ErrNoError:
		return "No error"
	case ErrTargetPortNotFound:
		return "Target port not found"
	case ErrTargetMachineNotFound:
		return "Target machine not found"
	case ErrDeviceError:
		return "Device error"
	case ErrDeviceSrvNotSupp:
		return "Service not supported"
	case ErrDeviceInvalidGrp:
		return "Invalid index group"
	case ErrDeviceInvalidOffs:
		return "Invalid index offset"
	case ErrDeviceInvalidAccess:
		return "Invalid access"
	case ErrDeviceInvalidSize:
		return "Invalid size"
	case ErrDeviceInvalidData:
		return "Invalid data"
	case ErrDeviceNotReady:
		return "Device not ready"
	case ErrDeviceBusy:
		return "Device busy"
	case ErrDeviceNoMemory:
		return "Out of memory"
	case ErrDeviceInvalidParam:
		return "Invalid parameter"
	case ErrDeviceNotFound:
		return "Device not found"
	case ErrDeviceSymbolNotFound:
		return "Symbol not found"
	case ErrDeviceTimeout:
		return "Timeout"
	case ErrDeviceAccessDenied:
		return "Access denied"
	default:
		return "Unknown error"
	}
}
