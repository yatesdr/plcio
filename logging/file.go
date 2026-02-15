package logging

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// FileLogger writes log messages to a file.
// It is safe for concurrent use from multiple goroutines.
type FileLogger struct {
	file   *os.File
	mu     sync.Mutex
	closed bool
}

// NewFileLogger creates a new file logger that writes to the specified path.
// The file is created if it doesn't exist, or appended to if it does.
func NewFileLogger(path string) (*FileLogger, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	return &FileLogger{
		file: file,
	}, nil
}

// Log writes a formatted message to the log file with a timestamp.
// This method is safe to call from any goroutine.
func (l *FileLogger) Log(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(l.file, "%s %s\n", timestamp, msg)
}

// Close closes the log file.
func (l *FileLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil
	}

	l.closed = true
	return l.file.Close()
}

