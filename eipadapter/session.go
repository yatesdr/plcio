package eipadapter

import (
	"math/rand"
	"net"
	"sync"
)

// session represents one registered EIP TCP session.
type session struct {
	handle uint32
	conn   net.Conn
}

type sessionTable struct {
	mu     sync.Mutex
	bySess map[uint32]*session
}

func (t *sessionTable) ensure() {
	if t.bySess == nil {
		t.bySess = make(map[uint32]*session)
	}
}

func (t *sessionTable) register(conn net.Conn) uint32 {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ensure()
	for {
		h := rand.Uint32()
		if h == 0 {
			continue
		}
		if _, dup := t.bySess[h]; dup {
			continue
		}
		t.bySess[h] = &session{handle: h, conn: conn}
		return h
	}
}

func (t *sessionTable) lookup(h uint32) *session {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ensure()
	return t.bySess[h]
}

func (t *sessionTable) remove(h uint32) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ensure()
	delete(t.bySess, h)
}
