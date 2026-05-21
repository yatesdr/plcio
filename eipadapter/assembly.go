package eipadapter

import (
	"sync"
	"sync/atomic"

	"github.com/yatesdr/plcio/cip"
)

// AssemblyDirection categorises an Assembly's role in Class 1 cyclic I/O.
type AssemblyDirection int

const (
	// AssemblyInput is produced by the adapter (T->O direction).
	AssemblyInput AssemblyDirection = iota
	// AssemblyOutput is consumed from the scanner (O->T direction).
	AssemblyOutput
	// AssemblyConfig is delivered once during Forward_Open in the
	// connection path. It cannot be cyclically read/written.
	AssemblyConfig
)

// Assembly implements CIP Class 0x04 — a fixed-size byte buffer at a specific
// instance ID. Scanners read/write assemblies cyclically via Class 1
// connections and ad-hoc via explicit messaging.
type Assembly struct {
	// InstanceID is the Assembly Object's CIP instance number. Common
	// conventions:
	//   100-127 = input (T->O)
	//   150-199 = output (O->T)
	//   151+    = config
	// (Allen-Bradley generic device defaults pair Input=101, Output=102,
	// Config=103.) Choose whatever fits your application.
	InstanceID uint32

	Direction AssemblyDirection

	// Size in bytes. Fixed at construction.
	Size int

	mu       sync.RWMutex
	bytes    []byte
	updates  atomic.Uint64
	onChange func(old, new []byte)
}

// NewAssembly builds a fixed-size assembly at the given instance/direction.
func NewAssembly(instance uint32, dir AssemblyDirection, size int) *Assembly {
	return &Assembly{
		InstanceID: instance,
		Direction:  dir,
		Size:       size,
		bytes:      make([]byte, size),
	}
}

// Bytes returns a copy of the current contents.
func (a *Assembly) Bytes() []byte {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]byte, len(a.bytes))
	copy(out, a.bytes)
	return out
}

// SetBytes writes data into the assembly at offset. Returns the number of
// bytes written (clipped to the assembly size). If OnChange is registered,
// it fires synchronously with the pre- and post-write snapshots.
func (a *Assembly) SetBytes(offset int, data []byte) int {
	a.mu.Lock()
	if offset < 0 || offset >= len(a.bytes) {
		a.mu.Unlock()
		return 0
	}
	var oldSnap, newSnap []byte
	if a.onChange != nil {
		oldSnap = make([]byte, len(a.bytes))
		copy(oldSnap, a.bytes)
	}
	n := copy(a.bytes[offset:], data)
	if a.onChange != nil {
		newSnap = make([]byte, len(a.bytes))
		copy(newSnap, a.bytes)
	}
	cb := a.onChange
	a.updates.Add(1)
	a.mu.Unlock()
	if cb != nil {
		cb(oldSnap, newSnap)
	}
	return n
}

// SetByte writes a single byte at offset, returning ok.
func (a *Assembly) SetByte(offset int, b byte) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if offset < 0 || offset >= len(a.bytes) {
		return false
	}
	a.bytes[offset] = b
	a.updates.Add(1)
	return true
}

// GetByte reads a single byte at offset.
func (a *Assembly) GetByte(offset int) byte {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if offset < 0 || offset >= len(a.bytes) {
		return 0
	}
	return a.bytes[offset]
}

// OnChange registers a callback invoked when the assembly contents change.
// For output assemblies this is the primary way to react to scanner writes.
// The callback receives copies; it may run in any goroutine and may run
// concurrently with itself if changes overlap.
func (a *Assembly) OnChange(fn func(old, new []byte)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.onChange = fn
}

// Updates returns the total number of times this assembly has been mutated.
// Useful for heartbeat / staleness logic.
func (a *Assembly) Updates() uint64 { return a.updates.Load() }

// receiveFromScanner is the internal write path used when a scanner writes
// to an output assembly via explicit messaging or Class 1 O->T. It bypasses
// the read lock and triggers OnChange with the old/new snapshots.
func (a *Assembly) receiveFromScanner(data []byte) {
	a.mu.Lock()
	old := make([]byte, len(a.bytes))
	copy(old, a.bytes)
	n := copy(a.bytes, data)
	_ = n
	newBuf := make([]byte, len(a.bytes))
	copy(newBuf, a.bytes)
	cb := a.onChange
	a.updates.Add(1)
	a.mu.Unlock()
	if cb != nil {
		cb(old, newBuf)
	}
}

// CIP service handlers.

const (
	asmGetAttributeSingle byte = 0x0E
	asmSetAttributeSingle byte = 0x10
)

func (a *Assembly) Class() uint32    { return 0x04 }
func (a *Assembly) Instance() uint32 { return a.InstanceID }

func (a *Assembly) Handle(req *ObjectRequest) ObjectResponse {
	switch req.Service {
	case asmGetAttributeSingle:
		// Attribute 3 = data, attribute 4 = size (in bytes).
		switch req.Path.Attribute {
		case 3:
			return ObjectResponse{Status: cip.StatusSuccess, Data: a.Bytes()}
		case 4:
			return ObjectResponse{Status: cip.StatusSuccess, Data: le16(uint16(a.Size))}
		default:
			return ObjectResponse{Status: cip.StatusAttrNotSupported}
		}
	case asmSetAttributeSingle:
		if req.Path.Attribute != 3 {
			return ObjectResponse{Status: cip.StatusAttrNotSupported}
		}
		if a.Direction == AssemblyInput {
			return ObjectResponse{Status: cip.StatusObjectStateConflict}
		}
		if len(req.Data) > a.Size {
			return ObjectResponse{Status: cip.StatusTooMuchData}
		}
		a.receiveFromScanner(req.Data)
		return ObjectResponse{Status: cip.StatusSuccess}
	default:
		return ObjectResponse{Status: cip.StatusServiceNotSupported}
	}
}
