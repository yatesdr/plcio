// Package ads implements the Beckhoff ADS (Automation Device Specification) protocol
// for communicating with TwinCAT PLCs.
package ads

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yatesdr/plcio/logging"
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
	CmdReadDeviceInfo     uint16 = 0x0001
	CmdRead               uint16 = 0x0002
	CmdWrite              uint16 = 0x0003
	CmdReadState          uint16 = 0x0004
	CmdWriteControl       uint16 = 0x0005
	CmdAddDeviceNotify    uint16 = 0x0006
	CmdDeleteDeviceNotify uint16 = 0x0007
	CmdDeviceNotification uint16 = 0x0008
	CmdReadWrite          uint16 = 0x0009
)

// ADS State Flags
const (
	StateFlagRequest  uint16 = 0x0004 // This is a request
	StateFlagResponse uint16 = 0x0005 // This is a response (request | 0x0001)
	StateFlagAdsCmd   uint16 = 0x0004 // ADS command
)

// ADS Index Groups for symbol access
const (
	IndexGroupSymbolTable          uint32 = 0xF000 // Symbol table
	IndexGroupSymbolName           uint32 = 0xF001 // Symbol name
	IndexGroupSymbolValue          uint32 = 0xF002 // Symbol value
	IndexGroupSymbolHandleByName   uint32 = 0xF003 // Get handle by symbol name
	IndexGroupSymbolValueByName    uint32 = 0xF004 // Read value by symbol name
	IndexGroupSymbolValueByHandle  uint32 = 0xF005 // Read/write value by handle
	IndexGroupSymbolReleaseHandle  uint32 = 0xF006 // Release handle
	IndexGroupSymbolInfoByName     uint32 = 0xF007 // Get symbol info by name
	IndexGroupSymbolVersion        uint32 = 0xF008 // Symbol version
	IndexGroupSymbolInfoByNameEx   uint32 = 0xF009 // Extended symbol info by name
	IndexGroupDataTypeInfoByNameEx uint32 = 0xF011 // Data type info by name
	IndexGroupSymbolUpload         uint32 = 0xF00B // Upload symbol table
	IndexGroupSymbolUploadInfo     uint32 = 0xF00C // Upload symbol info (count, size)
	IndexGroupDataTypeUpload       uint32 = 0xF00E // Upload data types
	IndexGroupSymbolUploadInfo2    uint32 = 0xF00F // Upload symbol info v2

	// SumUp commands for batched read/write operations
	IndexGroupSumUpRead      uint32 = 0xF080 // Read multiple values in one request
	IndexGroupSumUpWrite     uint32 = 0xF081 // Write multiple values in one request
	IndexGroupSumUpReadWrite uint32 = 0xF082 // Read/write multiple in one request
)

// ADS Ports
const (
	PortLogger        uint16 = 100   // Logger
	PortEventLog      uint16 = 110   // Event log
	PortIO            uint16 = 300   // I/O
	PortNC            uint16 = 500   // NC
	PortPLC1          uint16 = 801   // TwinCAT 2 PLC Runtime 1
	PortPLC2          uint16 = 811   // TwinCAT 2 PLC Runtime 2
	PortTC3PLC1       uint16 = 851   // TwinCAT 3 PLC Runtime 1
	PortTC3PLC2       uint16 = 852   // TwinCAT 3 PLC Runtime 2
	PortCamshaft      uint16 = 900   // Camshaft controller
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
	maxPayload uint32
	timeout    time.Duration
	dead       atomic.Bool
	closeOnce  sync.Once
}

func newAdsConnection(conn net.Conn, localNetId AmsNetId, localPort uint16) *adsConnection {
	return &adsConnection{conn: conn, localNetId: localNetId, localPort: localPort,
		maxPayload: 1 << 20, timeout: 5 * time.Second}
}

// The client serializes complete operations. There is exactly one in-flight
// exchange, and no receive goroutine. close can abort I/O without taking its lock.
func (c *adsConnection) sendRequest(targetNetId AmsNetId, targetPort uint16, cmdId uint16, data []byte) ([]byte, error) {
	return c.sendRequestUntil(targetNetId, targetPort, cmdId, data, time.Now().Add(c.timeout))
}

func (c *adsConnection) fail(err error) error {
	c.close()
	return fmt.Errorf("%w: %w", ErrConnectionLost, err)
}

func (c *adsConnection) sendRequestUntil(targetNetId AmsNetId, targetPort uint16, cmdId uint16, data []byte, deadline time.Time) ([]byte, error) {
	if c.dead.Load() {
		return nil, fmt.Errorf("%w: %w", ErrConnectionLost, net.ErrClosed)
	}
	if uint64(len(data)) > uint64(c.maxPayload) {
		return nil, fmt.Errorf("ADS request payload exceeds limit %d", c.maxPayload)
	}
	if !time.Now().Before(deadline) {
		return nil, fmt.Errorf("operation deadline: %w", context.DeadlineExceeded)
	}
	if err := c.conn.SetDeadline(deadline); err != nil {
		return nil, c.fail(err)
	}
	invokeId := nextInvokeId()
	buf := make([]byte, 38+len(data))
	binary.LittleEndian.PutUint32(buf[2:6], uint32(32+len(data)))
	copy(buf[6:12], targetNetId[:])
	binary.LittleEndian.PutUint16(buf[12:14], targetPort)
	copy(buf[14:20], c.localNetId[:])
	binary.LittleEndian.PutUint16(buf[20:22], c.localPort)
	binary.LittleEndian.PutUint16(buf[22:24], cmdId)
	binary.LittleEndian.PutUint16(buf[24:26], StateFlagRequest)
	binary.LittleEndian.PutUint32(buf[26:30], uint32(len(data)))
	binary.LittleEndian.PutUint32(buf[34:38], invokeId)
	copy(buf[38:], data)
	logging.DebugTX("ADS", buf)
	for remaining := buf; len(remaining) > 0; {
		n, err := c.conn.Write(remaining)
		if n < 0 || n > len(remaining) {
			return nil, c.fail(fmt.Errorf("invalid write count %d", n))
		}
		if err != nil {
			return nil, c.fail(fmt.Errorf("write request: %w", err))
		}
		if n == 0 {
			return nil, c.fail(fmt.Errorf("write request: %w", io.ErrShortWrite))
		}
		remaining = remaining[n:]
	}
	tcpBuf := make([]byte, 6)
	if _, err := io.ReadFull(c.conn, tcpBuf); err != nil {
		return nil, c.fail(fmt.Errorf("read TCP header: %w", err))
	}
	length, err := validateTCPHeader(tcpBuf, c.maxPayload)
	if err != nil {
		return nil, c.fail(err)
	}
	amsBuf := make([]byte, int(length))
	if _, err := io.ReadFull(c.conn, amsBuf); err != nil {
		return nil, c.fail(fmt.Errorf("read AMS data: %w", err))
	}
	logging.DebugRX("ADS", append(tcpBuf, amsBuf...))
	payload, err := validateAMSResponse(amsBuf, c.localNetId, c.localPort, targetNetId, targetPort, cmdId, invokeId)
	if err != nil {
		if _, ok := err.(*AdsError); ok {
			return nil, err
		}
		return nil, c.fail(err)
	}
	return payload, nil
}

func validateTCPHeader(data []byte, budget uint32) (uint32, error) {
	if len(data) != 6 || binary.LittleEndian.Uint16(data[:2]) != 0 {
		return 0, fmt.Errorf("invalid AMS/TCP header")
	}
	length := binary.LittleEndian.Uint32(data[2:6])
	if length < 32 || uint64(length) > uint64(budget)+32 || uint64(length) > uint64(int(^uint(0)>>1)) {
		return 0, fmt.Errorf("AMS frame length %d exceeds payload limit %d or is smaller than header", length, budget)
	}
	return length, nil
}

func validateAMSResponse(data []byte, local AmsNetId, localPort uint16, target AmsNetId, targetPort, command uint16, invoke uint32) ([]byte, error) {
	if len(data) < 32 {
		return nil, fmt.Errorf("AMS header truncated")
	}
	if binary.LittleEndian.Uint32(data[20:24]) != uint32(len(data)-32) {
		return nil, fmt.Errorf("AMS data length mismatch")
	}
	if binary.LittleEndian.Uint32(data[28:32]) != invoke {
		return nil, fmt.Errorf("invoke ID mismatch")
	}
	if binary.LittleEndian.Uint16(data[16:18]) != command {
		return nil, fmt.Errorf("ADS response command mismatch")
	}
	if binary.LittleEndian.Uint16(data[18:20]) != StateFlagResponse {
		return nil, fmt.Errorf("invalid ADS response flags")
	}
	if AmsNetId(data[:6]) != local || binary.LittleEndian.Uint16(data[6:8]) != localPort {
		return nil, fmt.Errorf("ADS response target mismatch")
	}
	code := binary.LittleEndian.Uint32(data[24:28])
	// A router can originate a global/router error, using its system port (0)
	// instead of the addressed runtime. Only that error envelope relaxes source port.
	routerError := code != 0 && code < 0x700 && len(data) == 32
	sourcePort := binary.LittleEndian.Uint16(data[14:16])
	if AmsNetId(data[8:14]) != target || (sourcePort != targetPort && !(routerError && sourcePort == 0)) {
		return nil, fmt.Errorf("ADS response source mismatch")
	}
	if code != 0 {
		return nil, &AdsError{Code: code}
	}
	return data[32:], nil
}

func (c *adsConnection) close() error {
	c.dead.Store(true)
	var err error
	c.closeOnce.Do(func() {
		if c.conn != nil {
			err = c.conn.Close()
		}
	})
	return err
}

// Decode Result before any success-only fields: short error replies must retain
// their AdsError, including the eight-byte ReadWrite error from handle lookup.
func commandResult(data []byte) ([]byte, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("ADS result truncated: %d bytes", len(data))
	}
	if code := binary.LittleEndian.Uint32(data[:4]); code != 0 {
		return nil, &AdsError{Code: code}
	}
	return data[4:], nil
}

func readCommandData(data []byte) ([]byte, error) {
	rest, err := commandResult(data)
	if err != nil {
		return nil, err
	}
	if len(rest) < 4 {
		return nil, fmt.Errorf("ADS read length truncated")
	}
	if uint64(binary.LittleEndian.Uint32(rest[:4])) != uint64(len(rest)-4) {
		return nil, fmt.Errorf("ADS read payload length mismatch")
	}
	return rest[4:], nil
}

func decodeDeviceInfo(data []byte) (*DeviceInfo, error) {
	rest, err := commandResult(data)
	if err != nil {
		return nil, err
	}
	if len(rest) != 20 {
		return nil, fmt.Errorf("ADS device identity size: %d, want 20", len(rest))
	}
	end := 4
	for end < len(rest) && rest[end] != 0 {
		end++
	}
	return &DeviceInfo{rest[0], rest[1], binary.LittleEndian.Uint16(rest[2:4]), string(rest[4:end])}, nil
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
	ErrNoError               uint32 = 0x0000
	ErrInternal              uint32 = 0x0001
	ErrNoRuntime             uint32 = 0x0002
	ErrAllocLockedMem        uint32 = 0x0003
	ErrInsertMailbox         uint32 = 0x0004
	ErrWrongHMsg             uint32 = 0x0005
	ErrTargetPortNotFound    uint32 = 0x0006
	ErrTargetMachineNotFound uint32 = 0x0007
	ErrUnknownCmdId          uint32 = 0x0008
	ErrBadTaskId             uint32 = 0x0009
	ErrNoIO                  uint32 = 0x000A
	ErrUnknownAmsCmd         uint32 = 0x000B
	ErrWin32Error            uint32 = 0x000C
	ErrPortNotConnected      uint32 = 0x000D
	ErrInvalidAmsLength      uint32 = 0x000E
	ErrInvalidAmsNetId       uint32 = 0x000F
	ErrLowInstLevel          uint32 = 0x0010
	ErrNoDebugInfo           uint32 = 0x0011
	ErrPortDisabled          uint32 = 0x0012
	ErrPortAlreadyConnected  uint32 = 0x0013
	ErrAmsSync               uint32 = 0x0014
	// Deprecated: legacy name/value retained; use ErrSyncTimeout.
	ErrAmsSyncSendError uint32 = 0x0015
	// Deprecated: legacy name/value retained; use ErrAmsSyncAmsError.
	ErrAmsNoSync          uint32 = 0x0016
	ErrNoIndexMap         uint32 = 0x0017
	ErrInvalidAmsPort     uint32 = 0x0018
	ErrNoMemory           uint32 = 0x0019
	ErrTcpSend            uint32 = 0x001A
	ErrHostUnreachable    uint32 = 0x001B
	ErrInvalidAmsFragment uint32 = 0x001C
	ErrTlsSend            uint32 = 0x001D
	ErrAccessDenied       uint32 = 0x001E

	// Router errors
	ErrRouterNoLockedMem     uint32 = 0x0500
	ErrRouterResizeMem       uint32 = 0x0501
	ErrRouterMailboxFull     uint32 = 0x0502
	ErrRouterDebugboxFull    uint32 = 0x0503
	ErrRouterUnknownPortType uint32 = 0x0504
	ErrRouterNotInitialized  uint32 = 0x0505
	// Deprecated: legacy name/value retained; use ErrRouterPortToBeRemoved.
	ErrRouterPortRemoved uint32 = 0x0506
	// Deprecated: legacy name/value retained; use ErrRouterPortNotRegistered.
	ErrRouterPortNotOpen uint32 = 0x0507
	// Deprecated: legacy name/value retained; use ErrRouterNoMoreQueues.
	ErrRouterPortOpen uint32 = 0x0508
	// Deprecated: legacy name/value retained; use ErrRouterInvalidPort.
	ErrRouterPortConnected uint32 = 0x0509
	// Deprecated: legacy name/value retained; use ErrRouterNotActive.
	ErrRouterPortNotConnected uint32 = 0x050A
	// Deprecated: legacy name/value retained; use ErrRouterFragmentBoxFull.
	ErrRouterNoSendQueue uint32 = 0x050B

	// Device/ADS errors
	ErrDeviceError                uint32 = 0x0700
	ErrDeviceSrvNotSupp           uint32 = 0x0701
	ErrDeviceInvalidGrp           uint32 = 0x0702
	ErrDeviceInvalidOffs          uint32 = 0x0703
	ErrDeviceInvalidAccess        uint32 = 0x0704
	ErrDeviceInvalidSize          uint32 = 0x0705
	ErrDeviceInvalidData          uint32 = 0x0706
	ErrDeviceNotReady             uint32 = 0x0707
	ErrDeviceBusy                 uint32 = 0x0708
	ErrDeviceInvalidContext       uint32 = 0x0709
	ErrDeviceNoMemory             uint32 = 0x070A
	ErrDeviceInvalidParam         uint32 = 0x070B
	ErrDeviceNotFound             uint32 = 0x070C
	ErrDeviceSyntax               uint32 = 0x070D
	ErrDeviceIncompatible         uint32 = 0x070E
	ErrDeviceExists               uint32 = 0x070F
	ErrDeviceSymbolNotFound       uint32 = 0x0710
	ErrDeviceSymbolVersionInvalid uint32 = 0x0711
	ErrDeviceInvalidState         uint32 = 0x0712
	ErrDeviceTransModeNotSupp     uint32 = 0x0713
	ErrDeviceNotifyHndInvalid     uint32 = 0x0714
	ErrDeviceClientUnknown        uint32 = 0x0715
	ErrDeviceNoMoreHdls           uint32 = 0x0716
	ErrDeviceInvalidWatchSize     uint32 = 0x0717
	ErrDeviceNotInit              uint32 = 0x0718
	ErrDeviceTimeout              uint32 = 0x0719
	ErrDeviceNoInterface          uint32 = 0x071A
	ErrDeviceInvalidInterface     uint32 = 0x071B
	ErrDeviceInvalidClsId         uint32 = 0x071C
	ErrDeviceInvalidObjId         uint32 = 0x071D
	ErrDevicePending              uint32 = 0x071E
	ErrDeviceAborted              uint32 = 0x071F
	ErrDeviceWarning              uint32 = 0x0720
	ErrDeviceInvalidArrayIdx      uint32 = 0x0721
	ErrDeviceSymbolNotActive      uint32 = 0x0722
	ErrDeviceAccessDenied         uint32 = 0x0723
	ErrDeviceLicenseNotFound      uint32 = 0x0724
	ErrDeviceLicenseExpired       uint32 = 0x0725
	ErrDeviceLicenseExceeded      uint32 = 0x0726
	ErrDeviceLicenseInvalid       uint32 = 0x0727
	ErrDeviceLicenseSystemId      uint32 = 0x0728
	ErrDeviceLicenseNoTimeLimit   uint32 = 0x0729
	// Deprecated: legacy name/value retained; use ErrDeviceLicenseFutureIssue.
	ErrDeviceLicenseTime uint32 = 0x072A
	// Deprecated: legacy name/value retained; use ErrDeviceLicenseTimeTooLong.
	ErrDeviceLicenseType     uint32 = 0x072B
	ErrDeviceLicensePlatform uint32 = 0x0736
	ErrDeviceException       uint32 = 0x072C
	// Deprecated: legacy name/value retained; use ErrDeviceInvalidSignature.
	ErrDeviceLicenseFile        uint32 = 0x072E
	ErrDeviceInvalidSignature   uint32 = 0x072E
	ErrDeviceCertInvalid        uint32 = 0x072F
	ErrDeviceLicenseOemNotFound uint32 = 0x0730
	ErrDeviceLicenseRestricted  uint32 = 0x0731
	ErrDeviceLicenseDemoDenied  uint32 = 0x0732
	ErrDeviceInvalidFncId       uint32 = 0x0733
	ErrDeviceOutOfRange         uint32 = 0x0734
	ErrDeviceInvalidAlignment   uint32 = 0x0735
	// Deprecated: legacy name/value retained; use ErrDeviceLicensePlatform.
	ErrDeviceLicensePlatformLevel uint32 = 0x0737
	ErrDeviceContextFwd           uint32 = 0x0737
	// Deprecated: legacy name/value retained; use ErrDeviceRealTime.
	ErrDevicePortDisabled uint32 = 0x0739
	// Deprecated: legacy name/value retained; use ErrDeviceCertificateEntrust.
	ErrDevicePortConnected uint32 = 0x073A
	// Deprecated: legacy name/value retained; use ErrDeviceLicenseIdNotUnique.
	ErrDeviceInvalidQualifier uint32 = 0x073B
	// Deprecated: legacy name/value retained; use ErrDeviceNoRealtimeConfig.
	ErrDeviceInvalidMailbox     uint32 = 0x073C
	ErrSyncTimeout              uint32 = 0x0015
	ErrAmsSyncAmsError          uint32 = 0x0016
	ErrTcpConnectionRefused     uint32 = 0x001F
	ErrRouterPortAlreadyInUse   uint32 = 0x0506
	ErrRouterPortNotRegistered  uint32 = 0x0507
	ErrRouterNoMoreQueues       uint32 = 0x0508
	ErrRouterInvalidPort        uint32 = 0x0509
	ErrRouterNotActive          uint32 = 0x050A
	ErrRouterFragmentBoxFull    uint32 = 0x050B
	ErrRouterFragmentTimeout    uint32 = 0x050C
	ErrRouterPortToBeRemoved    uint32 = 0x050D
	ErrDeviceLicenseFutureIssue uint32 = 0x072A
	ErrDeviceLicenseTimeTooLong uint32 = 0x072B
	ErrDeviceLicenseDuplicated  uint32 = 0x072D
	ErrDeviceDispatch           uint32 = 0x0738
	ErrDeviceRealTime           uint32 = 0x0739
	ErrDeviceCertificateEntrust uint32 = 0x073A
	ErrDeviceLicenseIdNotUnique uint32 = 0x073B
	ErrDeviceNoRealtimeConfig   uint32 = 0x073C
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
	case ErrDeviceSymbolVersionInvalid:
		return "Symbol version invalid"
	case ErrDeviceNotifyHndInvalid:
		return "Invalid notification/symbol handle"
	case ErrDeviceSymbolNotActive:
		return "Symbol not active"
	case ErrDeviceException:
		return "Device exception"
	case ErrDeviceLicenseDuplicated:
		return "License duplicated"
	case ErrDeviceInvalidSignature:
		return "Invalid signature"
	case ErrDeviceCertInvalid:
		return "Invalid certificate"
	case ErrRouterPortAlreadyInUse:
		return "Router port already in use"
	case ErrRouterPortNotRegistered:
		return "Router port not registered"
	case ErrRouterNoMoreQueues:
		return "Router port/queue limit reached"
	case ErrRouterInvalidPort:
		return "Invalid router port"
	case ErrRouterNotActive:
		return "Router not active"
	case ErrRouterFragmentBoxFull:
		return "Router fragment mailbox full"
	case ErrRouterFragmentTimeout:
		return "Router fragment timeout"
	case ErrRouterPortToBeRemoved:
		return "Router port being removed"
	default:
		return "Unknown error"
	}
}
