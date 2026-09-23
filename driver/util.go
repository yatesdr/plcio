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
//
// Classification is structural first: any package ErrConnectionLost sentinel
// (see IsConnectionLost), io.EOF, io.ErrUnexpectedEOF, net.ErrClosed, socket
// errnos (ECONNRESET, ECONNREFUSED, EPIPE, ECONNABORTED, ENETUNREACH,
// EHOSTUNREACH, ETIMEDOUT) and net.Error values (including timeouts). Message
// text is only a last resort for errors that were flattened to strings, and
// keywords match whole words only, so a tag named "Geofence" or "EOF_Count"
// does not look like an EOF.
func IsLikelyConnectionError(err error) bool {
	if err == nil {
		return false
	}

	if IsConnectionLost(err) {
		return true
	}

	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return true
	}

	// Check for syscall errors (connection reset, broken pipe, etc.)
	if errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ETIMEDOUT) {
		return true
	}

	// Check for network errors (dial/read/write failures and timeouts)
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	// Last resort: connection-related phrases in the message, as whole words.
	errMsg := strings.ToLower(err.Error())
	for _, keyword := range connectionKeywords {
		if containsWord(errMsg, keyword) {
			return true
		}
	}

	return false
}

var connectionKeywords = []string{
	"connection refused",
	"connection reset",
	"connection aborted",
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
	"connection lost",
}

// containsWord reports whether keyword occurs in s delimited by non-identifier
// characters (or the ends of s) on both sides.
func containsWord(s, keyword string) bool {
	for start := 0; start <= len(s)-len(keyword); {
		i := strings.Index(s[start:], keyword)
		if i < 0 {
			return false
		}
		i += start
		end := i + len(keyword)
		if (i == 0 || !isIdentByte(s[i-1])) && (end == len(s) || !isIdentByte(s[end])) {
			return true
		}
		start = i + 1
	}
	return false
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b >= 0x80
}
