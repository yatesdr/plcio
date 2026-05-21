package eipadapter

import (
	"encoding/binary"
	"sync"

	"github.com/yatesdr/plcio/cip"
)

// ObjectRequest is one CIP service invocation routed to an object.
type ObjectRequest struct {
	Service  byte
	Path     *cip.ParsedPath
	Data     []byte // service-specific payload (after path)
	Session  uint32 // EIP session handle that originated this (0 for UDP)
	ConnID   uint32 // connection ID if connected; 0 if unconnected
}

// ObjectResponse is what an Object returns.
type ObjectResponse struct {
	Status   byte
	ExtData  []uint16
	Data     []byte
}

// Object is implemented by CIP objects exposed by the adapter.
type Object interface {
	Class() uint32
	Instance() uint32
	Handle(req *ObjectRequest) ObjectResponse
}

// Registry stores objects indexed by (class, instance).
type Registry struct {
	mu      sync.RWMutex
	byClass map[uint32]map[uint32]Object
}

func NewRegistry() *Registry {
	return &Registry{byClass: make(map[uint32]map[uint32]Object)}
}

func (r *Registry) Register(o Object) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.byClass[o.Class()]
	if !ok {
		c = make(map[uint32]Object)
		r.byClass[o.Class()] = c
	}
	c[o.Instance()] = o
}

func (r *Registry) Lookup(class, instance uint32) Object {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.byClass[class]
	if !ok {
		return nil
	}
	return c[instance]
}

// classExists reports whether the registry has any instances of the given
// class. Used to disambiguate "unknown class" from "unknown instance".
func (r *Registry) classExists(class uint32) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.byClass[class]
	return ok
}

// okResponse wraps payload in a CIP success response header.
func okResponse(service byte, data []byte) []byte {
	out := make([]byte, 0, 4+len(data))
	out = append(out, service|0x80, 0x00, cip.StatusSuccess, 0x00)
	out = append(out, data...)
	return out
}

// errResponse builds a CIP error response with optional extended status words.
func errResponse(service, status byte, ext ...uint16) []byte {
	out := make([]byte, 0, 4+2*len(ext))
	out = append(out, service|0x80, 0x00, status, byte(len(ext)))
	for _, w := range ext {
		out = binary.LittleEndian.AppendUint16(out, w)
	}
	return out
}
