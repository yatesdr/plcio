package driver

import (
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
)

// IsLikelyConnectionError checks if an error indicates a connection problem
// that warrants a reconnection attempt.
func IsLikelyConnectionError(err error) bool {
	if err == nil {
		return false
	}

	// Check for EOF (connection closed)
	if errors.Is(err, io.EOF) {
		return true
	}

	// Check for network errors
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	// Check for syscall errors (connection reset, broken pipe, etc.)
	if errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNABORTED) {
		return true
	}

	// Check error message for connection-related keywords
	errMsg := strings.ToLower(err.Error())
	connectionKeywords := []string{
		"connection refused",
		"connection reset",
		"broken pipe",
		"use of closed network connection",
		"i/o timeout",
		"no route to host",
		"network is unreachable",
		"connection timed out",
		"eof",
		"forcibly closed",
		"socket closed",
		"not connected",
	}

	for _, keyword := range connectionKeywords {
		if strings.Contains(errMsg, keyword) {
			return true
		}
	}

	return false
}
