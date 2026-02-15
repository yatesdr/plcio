package logging

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// DebugLogger provides verbose debug logging with hex dump capability.
// It writes to a dedicated debug.log file and is intended for troubleshooting
// protocol-level issues such as connection errors, dropped connections, and
// communication failures.
type DebugLogger struct {
	file    *os.File
	mu      sync.Mutex
	closed  bool
	filters map[string]bool // Protocol filters (empty = log all)
}

// Global debug logger instance
var globalDebugLogger *DebugLogger
var globalDebugMu sync.RWMutex

// Known protocol names for filtering
var knownProtocols = []string{
	"omron", "fins", "fins/tcp", "fins/udp", "eip", "eip/discovery", "omron/eip",
	"ads",
	"logix",
	"s7",
	"mqtt",
	"kafka",
	"valkey",
	"tui",
	"plcman",
	"tagpack",
	"browser",
	"debug",
}

// NewDebugLogger creates a new debug logger that writes to the specified path.
// The file is created fresh (truncated if it exists) for each session.
func NewDebugLogger(path string) (*DebugLogger, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open debug log file: %w", err)
	}

	logger := &DebugLogger{
		file:    file,
		filters: make(map[string]bool),
	}

	// Write header
	logger.Log("DEBUG", "Debug logging started - %s", time.Now().Format(time.RFC3339))
	logger.Log("DEBUG", "========================================")

	return logger, nil
}

// SetFilter sets the protocol filter for logging.
// The filter can be a single protocol or comma-separated list.
// Empty string means log all protocols.
// Protocols are matched case-insensitively.
func (l *DebugLogger) SetFilter(filter string) {
	if l == nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.filters = make(map[string]bool)

	if filter == "" {
		return // Empty filter = log all
	}

	// Parse comma-separated protocols
	protocols := strings.Split(filter, ",")
	for _, p := range protocols {
		p = strings.TrimSpace(strings.ToLower(p))
		if p != "" {
			l.filters[p] = true
			// Also add related protocols
			switch p {
			case "logix":
				l.filters["eip"] = true
			case "omron":
				l.filters["fins"] = true
				l.filters["fins/tcp"] = true
				l.filters["fins/udp"] = true
				l.filters["eip"] = true
				l.filters["eip/discovery"] = true
				l.filters["omron/eip"] = true
			case "fins":
				l.filters["fins/tcp"] = true
				l.filters["fins/udp"] = true
				l.filters["omron"] = true
			case "eip":
				l.filters["eip/discovery"] = true
				l.filters["omron"] = true
				l.filters["omron/eip"] = true
			}
		}
	}

	// Log the filter configuration
	if len(l.filters) > 0 {
		filterList := make([]string, 0, len(l.filters))
		for p := range l.filters {
			filterList = append(filterList, p)
		}
		timestamp := time.Now().Format("2006-01-02 15:04:05.000")
		fmt.Fprintf(l.file, "%s [DEBUG] Filtering enabled for protocols: %s\n",
			timestamp, strings.Join(filterList, ", "))
	}
}

// shouldLog returns true if the protocol should be logged based on current filter.
// Must be called with l.mu held.
func (l *DebugLogger) shouldLog(protocol string) bool {
	// Empty filter = log everything
	if len(l.filters) == 0 {
		return true
	}

	// Check if protocol matches filter (case-insensitive)
	protocolLower := strings.ToLower(protocol)
	if l.filters[protocolLower] {
		return true
	}

	// Always allow DEBUG messages (for header/footer)
	if protocolLower == "debug" {
		return true
	}

	return false
}

// SetGlobalDebugLogger sets the global debug logger instance.
func SetGlobalDebugLogger(logger *DebugLogger) {
	globalDebugMu.Lock()
	defer globalDebugMu.Unlock()
	globalDebugLogger = logger
}

// GetGlobalDebugLogger returns the global debug logger instance.
func GetGlobalDebugLogger() *DebugLogger {
	globalDebugMu.RLock()
	defer globalDebugMu.RUnlock()
	return globalDebugLogger
}

// Log writes a formatted message with timestamp and protocol prefix.
func (l *DebugLogger) Log(protocol, format string, args ...interface{}) {
	if l == nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return
	}

	if !l.shouldLog(protocol) {
		return
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(l.file, "%s [%s] %s\n", timestamp, protocol, msg)
}

// LogTX logs a transmitted packet with hex dump.
func (l *DebugLogger) LogTX(protocol string, data []byte) {
	if l == nil {
		return
	}
	l.logPacket(protocol, "TX", data)
}

// LogRX logs a received packet with hex dump.
func (l *DebugLogger) LogRX(protocol string, data []byte) {
	if l == nil {
		return
	}
	l.logPacket(protocol, "RX", data)
}

// logPacket logs a packet with direction and hex dump.
func (l *DebugLogger) logPacket(protocol, direction string, data []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return
	}

	if !l.shouldLog(protocol) {
		return
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	fmt.Fprintf(l.file, "%s [%s] %s (%d bytes):\n", timestamp, protocol, direction, len(data))
	fmt.Fprintf(l.file, "%s\n", hexDump(data))
}

// LogConnect logs a connection event.
func (l *DebugLogger) LogConnect(protocol, address string) {
	l.Log(protocol, "CONNECT to %s", address)
}

// LogConnectSuccess logs a successful connection.
func (l *DebugLogger) LogConnectSuccess(protocol, address, details string) {
	l.Log(protocol, "CONNECTED to %s - %s", address, details)
}

// LogConnectError logs a connection failure.
func (l *DebugLogger) LogConnectError(protocol, address string, err error) {
	l.Log(protocol, "CONNECT FAILED to %s: %v", address, err)
}

// LogDisconnect logs a disconnection event.
func (l *DebugLogger) LogDisconnect(protocol, address, reason string) {
	l.Log(protocol, "DISCONNECT from %s: %s", address, reason)
}

// LogError logs an error with context.
func (l *DebugLogger) LogError(protocol, context string, err error) {
	l.Log(protocol, "ERROR in %s: %v", context, err)
}

// Close closes the debug log file.
func (l *DebugLogger) Close() error {
	if l == nil {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil
	}

	l.closed = true

	// Write footer
	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	fmt.Fprintf(l.file, "%s [DEBUG] Debug logging ended\n", timestamp)

	return l.file.Close()
}

// hexDump returns a hex dump of the data in a readable format.
// Format: offset: hex bytes   ASCII
// Example:
//
//	0000: 65 00 04 00 00 00 00 00  00 00 00 00 00 00 00 00  e...............
//	0010: 00 00 00 00 01 00 00 00                          ........
func hexDump(data []byte) string {
	if len(data) == 0 {
		return "    (empty)"
	}

	var sb strings.Builder
	for offset := 0; offset < len(data); offset += 16 {
		// Offset
		sb.WriteString(fmt.Sprintf("    %04X: ", offset))

		// Hex bytes (first 8)
		for i := 0; i < 8; i++ {
			if offset+i < len(data) {
				sb.WriteString(fmt.Sprintf("%02X ", data[offset+i]))
			} else {
				sb.WriteString("   ")
			}
		}
		sb.WriteString(" ")

		// Hex bytes (second 8)
		for i := 8; i < 16; i++ {
			if offset+i < len(data) {
				sb.WriteString(fmt.Sprintf("%02X ", data[offset+i]))
			} else {
				sb.WriteString("   ")
			}
		}
		sb.WriteString(" ")

		// ASCII representation
		for i := 0; i < 16; i++ {
			if offset+i < len(data) {
				b := data[offset+i]
				if b >= 32 && b < 127 {
					sb.WriteByte(b)
				} else {
					sb.WriteByte('.')
				}
			}
		}
		sb.WriteString("\n")
	}

	return strings.TrimSuffix(sb.String(), "\n")
}

// Global debug logging functions for use by protocol packages

// DebugLog logs a message if debug logging is enabled.
func DebugLog(protocol, format string, args ...interface{}) {
	if logger := GetGlobalDebugLogger(); logger != nil {
		logger.Log(protocol, format, args...)
	}
}

// DebugTX logs transmitted data if debug logging is enabled.
func DebugTX(protocol string, data []byte) {
	if logger := GetGlobalDebugLogger(); logger != nil {
		logger.LogTX(protocol, data)
	}
}

// DebugRX logs received data if debug logging is enabled.
func DebugRX(protocol string, data []byte) {
	if logger := GetGlobalDebugLogger(); logger != nil {
		logger.LogRX(protocol, data)
	}
}

// DebugConnect logs a connection attempt if debug logging is enabled.
func DebugConnect(protocol, address string) {
	if logger := GetGlobalDebugLogger(); logger != nil {
		logger.LogConnect(protocol, address)
	}
}

// DebugConnectSuccess logs a successful connection if debug logging is enabled.
func DebugConnectSuccess(protocol, address, details string) {
	if logger := GetGlobalDebugLogger(); logger != nil {
		logger.LogConnectSuccess(protocol, address, details)
	}
}

// DebugConnectError logs a connection error if debug logging is enabled.
func DebugConnectError(protocol, address string, err error) {
	if logger := GetGlobalDebugLogger(); logger != nil {
		logger.LogConnectError(protocol, address, err)
	}
}

// DebugDisconnect logs a disconnection if debug logging is enabled.
func DebugDisconnect(protocol, address, reason string) {
	if logger := GetGlobalDebugLogger(); logger != nil {
		logger.LogDisconnect(protocol, address, reason)
	}
}

// DebugError logs an error if debug logging is enabled.
func DebugError(protocol, context string, err error) {
	if logger := GetGlobalDebugLogger(); logger != nil {
		logger.LogError(protocol, context, err)
	}
}

