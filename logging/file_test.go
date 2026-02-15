package logging

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestNewFileLogger(t *testing.T) {
	// Create temp directory for test files
	tmpDir := t.TempDir()

	t.Run("creates new file", func(t *testing.T) {
		path := filepath.Join(tmpDir, "test1.log")
		logger, err := NewFileLogger(path)
		if err != nil {
			t.Fatalf("NewFileLogger failed: %v", err)
		}
		defer logger.Close()

		// Verify file exists
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Error("log file was not created")
		}
	})

	t.Run("appends to existing file", func(t *testing.T) {
		path := filepath.Join(tmpDir, "test2.log")

		// Create file with initial content
		if err := os.WriteFile(path, []byte("existing content\n"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}

		logger, err := NewFileLogger(path)
		if err != nil {
			t.Fatalf("NewFileLogger failed: %v", err)
		}
		logger.Log("new content")
		logger.Close()

		// Verify both contents exist
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read file: %v", err)
		}
		if !strings.Contains(string(content), "existing content") {
			t.Error("existing content was overwritten")
		}
		if !strings.Contains(string(content), "new content") {
			t.Error("new content was not appended")
		}
	})

	t.Run("returns error for invalid path", func(t *testing.T) {
		_, err := NewFileLogger("/nonexistent/directory/file.log")
		if err == nil {
			t.Error("expected error for invalid path")
		}
	})
}

func TestFileLogger_Log(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.log")

	logger, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger failed: %v", err)
	}
	defer logger.Close()

	t.Run("writes formatted message with timestamp", func(t *testing.T) {
		logger.Log("test message %d", 42)

		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read file: %v", err)
		}

		str := string(content)
		if !strings.Contains(str, "test message 42") {
			t.Errorf("expected 'test message 42' in output, got: %s", str)
		}
		// Check timestamp format (YYYY-MM-DD HH:MM:SS.mmm)
		if len(str) < 23 {
			t.Error("output too short to contain timestamp")
		}
	})

	t.Run("does not write after close", func(t *testing.T) {
		path2 := filepath.Join(tmpDir, "test2.log")
		logger2, _ := NewFileLogger(path2)
		logger2.Close()

		// This should not panic or write
		logger2.Log("should not appear")

		content, _ := os.ReadFile(path2)
		if strings.Contains(string(content), "should not appear") {
			t.Error("logged after close")
		}
	})
}

func TestFileLogger_LogWithPrefix(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.log")

	logger, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger failed: %v", err)
	}
	defer logger.Close()

	logger.Log("connected to %s", "broker.local")

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	str := string(content)
	if !strings.Contains(str, "connected to broker.local") {
		t.Errorf("expected message, got: %s", str)
	}
}

func TestFileLogger_Close(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.log")

	logger, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger failed: %v", err)
	}

	// First close should succeed
	if err := logger.Close(); err != nil {
		t.Errorf("first Close failed: %v", err)
	}

	// Second close should be safe (no error, no panic)
	if err := logger.Close(); err != nil {
		t.Errorf("second Close failed: %v", err)
	}
}

func TestFileLogger_Concurrent(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.log")

	logger, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger failed: %v", err)
	}
	defer logger.Close()

	// Spawn multiple goroutines writing concurrently
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			logger.Log("message from goroutine %d", n)
		}(i)
	}
	wg.Wait()

	// Verify file has content (exact count may vary due to timing)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 100 {
		t.Errorf("expected 100 lines, got %d", len(lines))
	}
}
