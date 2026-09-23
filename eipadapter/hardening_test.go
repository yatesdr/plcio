package eipadapter

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/yatesdr/plcio/cip"
	"github.com/yatesdr/plcio/eip"
)

// ---- fixtures -------------------------------------------------------------

func testIdentity() Identity {
	return Identity{
		VendorID:     0x1337,
		DeviceType:   0x000C,
		ProductCode:  1,
		RevMajor:     1,
		SerialNumber: 0xC0FFEE01,
		ProductName:  "Test Adapter",
		State:        0x03,
	}
}

func freePort(t *testing.T, network string) uint16 {
	t.Helper()
	if network == "udp4" {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		return uint16(c.LocalAddr().(*net.UDPAddr).Port)
	}
	l, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return uint16(l.Addr().(*net.TCPAddr).Port)
}

// runningAdapter is a served adapter on loopback with private ports.
type runningAdapter struct {
	*Adapter
	cancel context.CancelFunc
	done   chan struct{}
}

func startTestAdapter(t *testing.T, mutate func(*Config), asm ...*Assembly) *runningAdapter {
	t.Helper()
	cfg := Config{
		BindAddr:   "127.0.0.1",
		TCPPort:    freePort(t, "tcp4"),
		UDPPort:    freePort(t, "udp4"),
		IOPort:     freePort(t, "udp4"),
		Identity:   testIdentity(),
		Assemblies: asm,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ra := &runningAdapter{Adapter: a, cancel: cancel, done: make(chan struct{})}
	go func() {
		_ = a.Serve(ctx)
		close(ra.done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-ra.done:
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return on cleanup")
		}
	})
	return ra
}

// newUnitAdapter builds an adapter without serving, for driving the
// connection manager and I/O paths directly. Its I/O socket is a real
// loopback socket so producers can send.
func newUnitAdapter(t *testing.T, mutate func(*Config), asm ...*Assembly) *Adapter {
	t.Helper()
	cfg := Config{Identity: testIdentity(), Assemblies: asm}
	if mutate != nil {
		mutate(&cfg)
	}
	cfg.defaults()
	a := &Adapter{cfg: cfg, registry: NewRegistry(), asmByInstance: map[uint32]*Assembly{}, stopCh: make(chan struct{})}
	for _, x := range asm {
		a.asmByInstance[x.InstanceID] = x
		a.registry.Register(x)
	}
	a.connMgr = NewConnectionManager(a)
	a.registry.Register(a.connMgr)
	uc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	a.udpIO = uc
	t.Cleanup(func() {
		close(a.stopCh)
		a.connMgr.closeAll()
		a.wg.Wait()
		uc.Close()
	})
	return a
}

// foSpec describes a Forward_Open request.
type foSpec struct {
	large              bool
	serial             uint16
	otID, toID         uint32
	otRPI, toRPI       uint32
	otParams, toParams uint32
	trigger, mult      byte
	path               []byte
}

// consume 102 (O->T) / produce 101 (T->O), no config.
var ioPath = []byte{0x20, 0x04, 0x24, 0x80, 0x2C, 0x66, 0x2C, 0x65}

func newFO(serial uint16, otID uint32, otSize, toSize int) foSpec {
	return foSpec{
		serial: serial, otID: otID,
		otRPI: 10_000, toRPI: 10_000,
		otParams: 0x4000 | uint32(otSize), toParams: 0x4000 | uint32(toSize),
		trigger: 0x01, mult: 1, path: ioPath,
	}
}

// data returns the request data (after service + path).
func (s foSpec) data() []byte {
	b := []byte{0x0A, 0x0E}
	b = binary.LittleEndian.AppendUint32(b, s.otID)
	b = binary.LittleEndian.AppendUint32(b, s.toID)
	b = binary.LittleEndian.AppendUint16(b, s.serial)
	b = binary.LittleEndian.AppendUint16(b, 0x1234)
	b = binary.LittleEndian.AppendUint32(b, 0x5678ABCD)
	b = append(b, s.mult, 0, 0, 0)
	b = binary.LittleEndian.AppendUint32(b, s.otRPI)
	if s.large {
		b = binary.LittleEndian.AppendUint32(b, s.otParams)
	} else {
		b = binary.LittleEndian.AppendUint16(b, uint16(s.otParams))
	}
	b = binary.LittleEndian.AppendUint32(b, s.toRPI)
	if s.large {
		b = binary.LittleEndian.AppendUint32(b, s.toParams)
	} else {
		b = binary.LittleEndian.AppendUint16(b, uint16(s.toParams))
	}
	b = append(b, s.trigger, byte(len(s.path)/2))
	return append(b, s.path...)
}

// request returns the full CIP request addressed to the Connection Manager.
func (s foSpec) request() []byte {
	svc := cmForwardOpen
	if s.large {
		svc = cmForwardOpenLarge
	}
	return append([]byte{svc, 0x02, 0x20, 0x06, 0x24, 0x01}, s.data()...)
}

func forwardCloseRequest(serial uint16) []byte {
	b := []byte{cmForwardClose, 0x02, 0x20, 0x06, 0x24, 0x01, 0x0A, 0x0E}
	b = binary.LittleEndian.AppendUint16(b, serial)
	b = binary.LittleEndian.AppendUint16(b, 0x1234)
	b = binary.LittleEndian.AppendUint32(b, 0x5678ABCD)
	return append(b, 0x02, 0x00, 0x20, 0x04, 0x24, 0x80)
}

func (a *Adapter) openFO(s foSpec, origin *peerAddr) ObjectResponse {
	return a.connMgr.handleForwardOpen(s.data(), s.large, origin)
}

func extOf(r ObjectResponse) uint16 {
	if len(r.ExtData) == 0 {
		return 0
	}
	return r.ExtData[0]
}

func loopbackOrigin(port uint16) *peerAddr {
	return &peerAddr{ip: [4]byte{127, 0, 0, 1}, port: port}
}

// rawClient speaks raw encapsulation over TCP.
type rawClient struct {
	t       *testing.T
	conn    net.Conn
	session uint32
}

func dialRaw(t *testing.T, a *Adapter) *rawClient {
	t.Helper()
	conn, err := net.DialTimeout("tcp4", a.TCPAddr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &rawClient{t: t, conn: conn}
}

func (c *rawClient) send(f *eip.Frame) {
	c.t.Helper()
	_ = c.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.conn.Write(f.Bytes()); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

func (c *rawClient) recv() (*eip.Frame, error) {
	_ = c.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	return eip.ReadFrame(c.conn)
}

func (c *rawClient) mustRecv() *eip.Frame {
	c.t.Helper()
	f, err := c.recv()
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	return f
}

func (c *rawClient) registerStatus() (uint32, uint32) {
	c.t.Helper()
	c.send(&eip.Frame{Command: eip.RegisterSession, SessionHandle: c.session, Data: []byte{1, 0, 0, 0}})
	f := c.mustRecv()
	return f.Status, f.SessionHandle
}

func (c *rawClient) register() {
	c.t.Helper()
	st, h := c.registerStatus()
	if st != eip.EncapStatusSuccess || h == 0 {
		c.t.Fatalf("RegisterSession: status 0x%X handle 0x%X", st, h)
	}
	c.session = h
}

// rr sends an unconnected CIP request and returns the CIP response.
func (c *rawClient) rr(cipReq []byte, extra ...eip.EipCommonPacketItem) []byte {
	c.t.Helper()
	cpf := eip.EipCommonPacket{Items: append([]eip.EipCommonPacketItem{
		{TypeId: eip.CpfAddressNullId},
		{TypeId: eip.CpfUnconnectedMessageId, Length: uint16(len(cipReq)), Data: cipReq},
	}, extra...)}
	c.send(&eip.Frame{Command: eip.SendRRData, SessionHandle: c.session, Data: eip.BuildRRData(cpf.Bytes())})
	f := c.mustRecv()
	body, err := eip.ParseRRData(f.Data)
	if err != nil {
		c.t.Fatal(err)
	}
	pkt, err := eip.ParseEipCommonPacket(body)
	if err != nil {
		c.t.Fatal(err)
	}
	for _, it := range pkt.Items {
		if it.TypeId == eip.CpfUnconnectedMessageId {
			return it.Data
		}
	}
	c.t.Fatalf("no unconnected data item in reply")
	return nil
}

// udpSink is a loopback socket used as the T->O destination.
func udpSink(t *testing.T) *net.UDPConn {
	t.Helper()
	uc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { uc.Close() })
	return uc
}

func sinkItem(uc *net.UDPConn) eip.EipCommonPacketItem {
	return socketItem(0x8001, net.IPv4(127, 0, 0, 1), uint16(uc.LocalAddr().(*net.UDPAddr).Port))
}

func o2tPacket(otID, seq uint32, runIdle *byte, data []byte) []byte {
	if runIdle != nil {
		data = append([]byte{*runIdle, 0, 0, 0}, data...)
	}
	return buildO2TPacket(otID, seq, data)
}

func eventually(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func ioAssemblies() (in, out *Assembly) {
	return NewAssembly(101, AssemblyInput, 4), NewAssembly(102, AssemblyOutput, 4)
}

// ---- 1. handleSendUnitData nil dereference --------------------------------

func TestSendUnitDataForwardCloseOfSameConnection(t *testing.T) {
	in, out := ioAssemblies()
	a := startTestAdapter(t, nil, in, out)
	sink := udpSink(t)
	c := dialRaw(t, a.Adapter)
	c.register()

	resp := c.rr(newFO(7, 0xAB00, 10, 6).request(), sinkItem(sink))
	if resp[2] != cip.StatusSuccess {
		t.Fatalf("Forward_Open status 0x%02X", resp[2])
	}
	conn := a.connMgr.lookupByOT(0xAB00)
	if conn == nil {
		t.Fatal("connection not registered")
	}

	// Forward_Close for the connection the SendUnitData is addressed to.
	body := binary.LittleEndian.AppendUint16(nil, 1)
	body = append(body, forwardCloseRequest(7)...)
	cpf := eip.EipCommonPacket{Items: []eip.EipCommonPacketItem{
		{TypeId: eip.CpfAddressConnectionId, Length: 4, Data: binary.LittleEndian.AppendUint32(nil, 0xAB00)},
		{TypeId: eip.CpfConnectedTransportPacketId, Length: uint16(len(body)), Data: body},
	}}
	c.send(&eip.Frame{Command: eip.SendUnitData, SessionHandle: c.session, Data: eip.BuildRRData(cpf.Bytes())})
	f := c.mustRecv()
	if f.Command != eip.SendUnitData || f.Status != eip.EncapStatusSuccess {
		t.Fatalf("reply cmd 0x%X status 0x%X", f.Command, f.Status)
	}
	rr, _ := eip.ParseRRData(f.Data)
	pkt, err := eip.ParseEipCommonPacket(rr)
	if err != nil || len(pkt.Items) != 2 {
		t.Fatalf("reply CPF: %v %+v", err, pkt)
	}
	if got := binary.LittleEndian.Uint32(pkt.Items[0].Data); got != conn.TOConnID {
		t.Errorf("reply conn ID 0x%08X, want 0x%08X", got, conn.TOConnID)
	}
	if d := pkt.Items[1].Data; len(d) < 5 || d[2] != cmForwardClose|0x80 || d[4] != cip.StatusSuccess {
		t.Errorf("Forward_Close reply: %x", d)
	}
	if a.connMgr.lookupByOT(0xAB00) != nil {
		t.Error("connection still registered after Forward_Close")
	}
}

// ---- 2. resource limits ----------------------------------------------------

func TestSecondRegisterSessionRejected(t *testing.T) {
	a := startTestAdapter(t, nil)
	c := dialRaw(t, a.Adapter)
	c.register()
	for i := 0; i < 3; i++ {
		st, _ := c.registerStatus()
		if st != eip.EncapStatusInvalidCommand {
			t.Fatalf("second RegisterSession status 0x%X, want 0x%X", st, eip.EncapStatusInvalidCommand)
		}
	}
	count := func() int {
		a.sessions.mu.Lock()
		defer a.sessions.mu.Unlock()
		return len(a.sessions.bySess)
	}
	if n := count(); n != 1 {
		t.Fatalf("%d sessions registered, want 1", n)
	}
	c.conn.Close()
	eventually(t, 2*time.Second, func() bool { return count() == 0 }, "session not removed when TCP connection closed")
}

func TestTCPConnectionLimit(t *testing.T) {
	a := startTestAdapter(t, func(c *Config) { c.MaxTCPConnections = 2 })
	c1, c2 := dialRaw(t, a.Adapter), dialRaw(t, a.Adapter)
	c1.register()
	c2.register()
	c3 := dialRaw(t, a.Adapter)
	_, _ = c3.conn.Write((&eip.Frame{Command: eip.RegisterSession, Data: []byte{1, 0, 0, 0}}).Bytes())
	if _, err := c3.recv(); err == nil {
		t.Fatal("connection beyond MaxTCPConnections was served")
	}
	// Closing one frees a slot.
	c1.conn.Close()
	eventually(t, 2*time.Second, func() bool {
		a.tcpMu.Lock()
		defer a.tcpMu.Unlock()
		return len(a.tcpConns) == 1
	}, "closed connection not untracked")
	dialRaw(t, a.Adapter).register()
}

func TestForwardOpenConnectionLimit(t *testing.T) {
	in, _ := ioAssemblies()
	a := newUnitAdapter(t, func(c *Config) { c.MaxConnections = 2 }, in)
	produceOnly := func(serial uint16, otID uint32) foSpec {
		s := newFO(serial, otID, 2, 6)
		s.path = []byte{0x20, 0x04, 0x24, 0x80, 0x2C, 0x65}
		return s
	}
	for i := uint16(1); i <= 2; i++ {
		if r := a.openFO(produceOnly(i, uint32(i)), nil); r.Status != cip.StatusSuccess {
			t.Fatalf("FO %d: status 0x%02X ext 0x%04X", i, r.Status, extOf(r))
		}
	}
	r := a.openFO(produceOnly(3, 3), nil)
	if r.Status != cip.StatusConnectionFailure || extOf(r) != 0x0113 {
		t.Fatalf("FO over limit: status 0x%02X ext 0x%04X, want 0x01/0x0113", r.Status, extOf(r))
	}
	// Unsuccessful Forward_Open response body echoes the triad.
	if len(r.Data) != 10 || binary.LittleEndian.Uint16(r.Data) != 3 {
		t.Errorf("reject body: %x", r.Data)
	}
}

func TestConsumeOnlyConnectionWatchdog(t *testing.T) {
	now := time.Now()
	a := &Adapter{cfg: Config{Now: func() time.Time { return now }}, stopCh: make(chan struct{})}
	a.connMgr = NewConnectionManager(a)
	c := &Connection{OTConnID: 1, TOConnID: 2, OTRPI: 5000, createdAt: now.Add(-time.Second), Consume: NewAssembly(102, AssemblyOutput, 4)}
	a.connMgr.byOT[1] = c
	a.connMgr.byTO[2] = c
	done := make(chan struct{})
	go func() { defer close(done); a.runProducer(c) }()
	defer close(a.stopCh)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("consume-only watchdog did not expire")
	}
	if a.connMgr.lookupByOT(1) != nil {
		t.Fatal("consume-only connection without traffic was never expired")
	}
}

func TestUnusedConnectionTimesOutFromForwardOpen(t *testing.T) {
	in, out := ioAssemblies()
	events := make(chan ConnectionEvent, 8)
	a := newUnitAdapter(t, func(c *Config) { c.OnConnectionEvent = func(e ConnectionEvent) { events <- e } }, in, out)
	s := newFO(1, 0x77, 10, 6)
	s.otRPI, s.mult = 1000, 0 // 4 ms timeout
	if r := a.openFO(s, nil); r.Status != cip.StatusSuccess {
		t.Fatalf("FO status 0x%02X ext 0x%04X", r.Status, extOf(r))
	}
	eventually(t, 2*time.Second, func() bool { return a.connMgr.lookupByOT(0x77) == nil }, "connection that never received traffic did not time out")
	want := []ConnectionEventType{ConnectionOpened, ConnectionTimedOut}
	for _, w := range want {
		select {
		case e := <-events:
			if e.Type != w || e.OTConnID != 0x77 || e.ConsumeInstance != 102 || e.ProduceInstance != 101 {
				t.Fatalf("event %+v, want %v", e, w)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("missing %v event", w)
		}
	}
	// Ownership is released so a new owner can connect.
	if r := a.openFO(newFO(2, 0x78, 10, 6), nil); r.Status != cip.StatusSuccess {
		t.Fatalf("re-open after timeout: 0x%02X ext 0x%04X", r.Status, extOf(r))
	}
}

// ---- 3. duplicate Forward_Open ---------------------------------------------

func TestDuplicateForwardOpenRejected(t *testing.T) {
	in, _ := ioAssemblies()
	a := newUnitAdapter(t, nil, in)
	s := newFO(9, 0x100, 2, 6)
	s.path = []byte{0x20, 0x04, 0x24, 0x80, 0x2C, 0x65}
	if r := a.openFO(s, nil); r.Status != cip.StatusSuccess {
		t.Fatalf("first FO: 0x%02X", r.Status)
	}
	first := a.connMgr.lookupByOT(0x100)

	dupTriad := s
	dupTriad.otID = 0x200
	r := a.openFO(dupTriad, nil)
	if r.Status != cip.StatusConnectionFailure || extOf(r) != cip.ExtConnectionInUse {
		t.Errorf("same triad: status 0x%02X ext 0x%04X, want 0x01/0x0100", r.Status, extOf(r))
	}
	a.connMgr.mu.RLock()
	n := len(a.connMgr.byOT)
	a.connMgr.mu.RUnlock()
	if n != 1 || a.connMgr.lookupByOT(0x100) != first {
		t.Fatalf("original connection replaced or duplicates registered (%d connections)", n)
	}

	// A different connection proposing an O->T ID that is already in use
	// is not a duplicate: the target chooses the O->T ID (CIP Vol 1
	// 3-5.4.1), so it gets a fresh one instead of a refusal.
	sameOTID := s
	sameOTID.serial = 10
	r = a.openFO(sameOTID, nil)
	if r.Status != cip.StatusSuccess {
		t.Fatalf("same O->T ID, new triad: status 0x%02X ext 0x%04X", r.Status, extOf(r))
	}
	fo, err := cip.ParseForwardOpenResponse(r.Data)
	if err != nil {
		t.Fatal(err)
	}
	if fo.OTConnectionID == 0x100 || a.connMgr.lookupByOT(fo.OTConnectionID) == nil || a.connMgr.lookupByOT(0x100) != first {
		t.Fatalf("colliding O->T ID: reply 0x%08X; original replaced or new not registered", fo.OTConnectionID)
	}
}

func TestExpireOnlyRemovesSameConnection(t *testing.T) {
	a := &Adapter{}
	a.connMgr = NewConnectionManager(a)
	live := &Connection{OTConnID: 7, TOConnID: 8}
	stale := &Connection{OTConnID: 7, TOConnID: 8}
	a.connMgr.byOT[7] = live
	a.connMgr.byTO[8] = live
	a.connMgr.expire(stale)
	if a.connMgr.lookupByOT(7) != live || a.connMgr.lookupByTO(8) != live {
		t.Fatal("expire of a stale connection removed the live one")
	}
}

// ---- 4. shutdown -----------------------------------------------------------

func TestServeReturnsPromptlyOnShutdown(t *testing.T) {
	for _, how := range []string{"cancel", "Close"} {
		t.Run(how, func(t *testing.T) {
			in, out := ioAssemblies()
			a := startTestAdapter(t, nil, in, out)
			sink := udpSink(t)

			idle := dialRaw(t, a.Adapter)
			idle.register()
			c := dialRaw(t, a.Adapter)
			c.register()
			s := newFO(1, 0x42, 10, 6)
			s.otRPI, s.toRPI, s.mult = 1_000_000, 50_000, 7 // long watchdog
			if resp := c.rr(s.request(), sinkItem(sink)); resp[2] != cip.StatusSuccess {
				t.Fatalf("Forward_Open status 0x%02X", resp[2])
			}
			_ = sink.SetReadDeadline(time.Now().Add(2 * time.Second))
			if _, _, err := sink.ReadFromUDP(make([]byte, 1500)); err != nil {
				t.Fatalf("producer not running: %v", err)
			}

			start := time.Now()
			if how == "cancel" {
				a.cancel()
			} else {
				_ = a.Close()
			}
			select {
			case <-a.done:
			case <-time.After(2 * time.Second):
				t.Fatal("Serve did not return promptly")
			}
			if d := time.Since(start); d > time.Second {
				t.Errorf("Serve took %v to return", d)
			}
			if _, err := idle.recv(); err == nil || !(errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || isConnReset(err)) {
				t.Errorf("idle client not disconnected: %v", err)
			}
			if a.connMgr.lookupByOT(0x42) != nil {
				t.Error("I/O connection survived shutdown")
			}
		})
	}
}

func TestCloseFromConnectionEventCallback(t *testing.T) {
	in, out := ioAssemblies()
	var ad *Adapter
	closed := make(chan struct{}, 1)
	a := startTestAdapter(t, func(c *Config) {
		c.OnConnectionEvent = func(e ConnectionEvent) {
			if e.Type == ConnectionClosed {
				_ = ad.Close() // re-entrant Close must not deadlock
				closed <- struct{}{}
			}
		}
	}, in, out)
	ad = a.Adapter
	c := dialRaw(t, a.Adapter)
	c.register()
	s := newFO(1, 0x43, 10, 6)
	s.otRPI, s.mult = 1_000_000, 3
	if resp := c.rr(s.request(), sinkItem(udpSink(t))); resp[2] != cip.StatusSuccess {
		t.Fatalf("Forward_Open status 0x%02X", resp[2])
	}
	_ = a.Close()
	select {
	case <-a.done:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return")
	}
	select {
	case <-closed:
	default:
		t.Fatal("no ConnectionClosed event on shutdown")
	}
}

func isConnReset(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && !ne.Timeout()
}

// ---- 5. connection events and Run/Idle ---------------------------------------

func TestConnectionEventsAndRunIdle(t *testing.T) {
	in, out := ioAssemblies()
	var mu sync.Mutex
	var got []ConnectionEvent
	a := startTestAdapter(t, func(c *Config) {
		c.OnConnectionEvent = func(e ConnectionEvent) {
			mu.Lock()
			got = append(got, e)
			mu.Unlock()
		}
	}, in, out)
	types := func() []ConnectionEventType {
		mu.Lock()
		defer mu.Unlock()
		var ts []ConnectionEventType
		for _, e := range got {
			ts = append(ts, e.Type)
		}
		return ts
	}
	waitFor := func(n int) {
		t.Helper()
		eventually(t, 3*time.Second, func() bool { return len(types()) >= n }, "missing connection event")
	}

	sink := udpSink(t)
	c := dialRaw(t, a.Adapter)
	c.register()
	s := newFO(1, 0x55, 10, 6)  // O->T with Run/Idle header
	s.otRPI, s.mult = 50_000, 2 // 800 ms timeout
	if resp := c.rr(s.request(), sinkItem(sink)); resp[2] != cip.StatusSuccess {
		t.Fatalf("Forward_Open status 0x%02X", resp[2])
	}
	waitFor(1)
	if _, ok := out.RunIdle(); ok {
		t.Error("RunIdle known before any O->T data")
	}

	scanner, err := net.DialUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, a.IOAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer scanner.Close()
	run, idle := byte(1), byte(0)
	if _, err := scanner.Write(o2tPacket(0x55, 1, &run, []byte{1, 2, 3, 4})); err != nil {
		t.Fatal(err)
	}
	waitFor(2)
	if r, ok := out.RunIdle(); !r || !ok {
		t.Errorf("RunIdle = %v,%v after Run header", r, ok)
	}
	if !bytes.Equal(out.Bytes(), []byte{1, 2, 3, 4}) {
		t.Errorf("output data %x", out.Bytes())
	}
	// Same state again: no new event.
	_, _ = scanner.Write(o2tPacket(0x55, 2, &run, []byte{1, 2, 3, 4}))
	_, _ = scanner.Write(o2tPacket(0x55, 3, &idle, []byte{5, 6, 7, 8}))
	waitFor(3)
	if r, ok := out.RunIdle(); r || !ok {
		t.Errorf("RunIdle = %v,%v after Idle header", r, ok)
	}

	// Stop sending: the connection times out, data is left untouched.
	waitFor(4)
	want := []ConnectionEventType{ConnectionOpened, ConnectionRun, ConnectionIdle, ConnectionTimedOut}
	if ts := types(); len(ts) != len(want) {
		t.Fatalf("events %v, want %v", ts, want)
	} else {
		for i := range want {
			if ts[i] != want[i] {
				t.Fatalf("events %v, want %v", ts, want)
			}
		}
	}
	mu.Lock()
	last := got[3]
	mu.Unlock()
	if last.OTConnID != 0x55 || !last.Originator.Equal(net.IPv4(127, 0, 0, 1)) || last.ConsumeInstance != 102 {
		t.Errorf("timeout event %+v", last)
	}
	if _, ok := out.RunIdle(); ok {
		t.Error("RunIdle still known after timeout")
	}
	if !bytes.Equal(out.Bytes(), []byte{5, 6, 7, 8}) {
		t.Errorf("output data changed on timeout: %x", out.Bytes())
	}
}

// ---- 6. RPI handling ---------------------------------------------------------

func TestForwardOpenRPIBelowMinimumRejected(t *testing.T) {
	in, out := ioAssemblies()
	a := newUnitAdapter(t, nil, in, out)
	for name, mod := range map[string]func(*foSpec){
		"T->O": func(s *foSpec) { s.toRPI = 500 },
		"O->T": func(s *foSpec) { s.otRPI = 999 },
	} {
		s := newFO(1, 0x10, 10, 6)
		mod(&s)
		r := a.openFO(s, nil)
		if r.Status != cip.StatusConnectionFailure || extOf(r) != cip.ExtRPIOutOfRange {
			t.Errorf("%s RPI below 1ms: status 0x%02X ext 0x%04X, want 0x01/0x0111", name, r.Status, extOf(r))
		}
	}
	// An accepted connection reports the RPIs it actually uses as APIs.
	s := newFO(2, 0x11, 10, 6)
	s.otRPI, s.toRPI = 1000, 2000
	r := a.openFO(s, nil)
	if r.Status != cip.StatusSuccess {
		t.Fatalf("1ms RPI rejected: 0x%02X ext 0x%04X", r.Status, extOf(r))
	}
	if ot, to := binary.LittleEndian.Uint32(r.Data[16:20]), binary.LittleEndian.Uint32(r.Data[20:24]); ot != 1000 || to != 2000 {
		t.Errorf("APIs %d/%d, want 1000/2000", ot, to)
	}
}

// ---- 7. Forward_Open validation ------------------------------------------------

func TestForwardOpenValidation(t *testing.T) {
	cfgAsm := NewAssembly(103, AssemblyConfig, 0)
	for _, tt := range []struct {
		name   string
		mod    func(*foSpec)
		ext    uint16 // 0 = accepted
		format byte
	}{
		{"run/idle O->T", func(s *foSpec) {}, 0, otFormatRunIdle},
		{"modeless O->T", func(s *foSpec) { s.otParams = 0x4000 | 6 }, 0, otFormatModeless},
		{"variable O->T", func(s *foSpec) { s.otParams = 0x4200 | 20 }, 0, otFormatAuto},
		{"large FO", func(s *foSpec) {
			s.large, s.otParams, s.toParams = true, 0x40000000|10, 0x40000000|6
		}, 0, otFormatRunIdle},
		{"T->O size", func(s *foSpec) { s.toParams = 0x4000 | 4 }, cip.ExtInvalidConnSize, 0},
		{"T->O size large", func(s *foSpec) {
			s.large, s.otParams, s.toParams = true, 0x40000000|10, 0x40000000|8
		}, cip.ExtInvalidConnSize, 0},
		{"O->T size", func(s *foSpec) { s.otParams = 0x4000 | 5 }, cip.ExtInvalidConnSize, 0},
		{"multicast T->O served unicast", func(s *foSpec) { s.toParams = 0x2000 | 6 }, 0, otFormatRunIdle},
		{"null O->T", func(s *foSpec) { s.otParams = 10 }, 0x0108, 0},
		{"consume input assembly", func(s *foSpec) {
			s.path = []byte{0x20, 0x04, 0x24, 0x80, 0x2C, 0x65, 0x2C, 0x65}
		}, 0x0117, 0},
		{"consume config assembly", func(s *foSpec) {
			s.path = []byte{0x20, 0x04, 0x24, 0x80, 0x2C, 0x67, 0x2C, 0x65}
		}, 0x0117, 0},
		{"produce output assembly", func(s *foSpec) {
			s.path = []byte{0x20, 0x04, 0x24, 0x80, 0x2C, 0x66, 0x2C, 0x66}
			s.toParams = 0x4000 | 6
		}, 0x0117, 0},
		{"class 3 transport", func(s *foSpec) { s.trigger = 0xA3 }, cip.ExtTransportClassUnsupp, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in, out := ioAssemblies()
			a := newUnitAdapter(t, nil, in, out, cfgAsm)
			s := newFO(1, 0x20, 10, 6)
			tt.mod(&s)
			r := a.openFO(s, nil)
			if tt.ext == 0 {
				if r.Status != cip.StatusSuccess {
					t.Fatalf("rejected: 0x%02X ext 0x%04X", r.Status, extOf(r))
				}
				if c := a.connMgr.lookupByOT(0x20); c == nil || c.otFormat != tt.format {
					t.Fatalf("connection %+v, want O->T format %d", c, tt.format)
				}
				return
			}
			if r.Status != cip.StatusConnectionFailure || extOf(r) != tt.ext {
				t.Fatalf("status 0x%02X ext 0x%04X, want 0x01/0x%04X", r.Status, extOf(r), tt.ext)
			}
			if a.connMgr.lookupByOT(0x20) != nil {
				t.Fatal("rejected connection registered")
			}
		})
	}
}

func TestForwardOpenOwnershipConflict(t *testing.T) {
	in, out := ioAssemblies()
	a := newUnitAdapter(t, nil, in, out)
	if r := a.openFO(newFO(1, 0x30, 10, 6), nil); r.Status != cip.StatusSuccess {
		t.Fatalf("first owner: 0x%02X", r.Status)
	}
	r := a.openFO(newFO(2, 0x31, 10, 6), nil)
	if r.Status != cip.StatusConnectionFailure || extOf(r) != cip.ExtOwnershipConflict {
		t.Fatalf("second owner: status 0x%02X ext 0x%04X, want 0x01/0x0106", r.Status, extOf(r))
	}
}

// ---- 8. panic safety net --------------------------------------------------------

func TestRecoverTCPHandlerPanic(t *testing.T) {
	in, out := ioAssemblies()
	a := startTestAdapter(t, func(c *Config) {
		c.OnForwardOpen = func(*ForwardOpenContext) error { panic("boom") }
	}, in, out)
	c := dialRaw(t, a.Adapter)
	c.register()
	cpf := eip.EipCommonPacket{Items: []eip.EipCommonPacketItem{
		{TypeId: eip.CpfAddressNullId},
		{TypeId: eip.CpfUnconnectedMessageId, Data: newFO(1, 0x60, 10, 6).request()},
	}}
	cpf.Items[1].Length = uint16(len(cpf.Items[1].Data))
	c.send(&eip.Frame{Command: eip.SendRRData, SessionHandle: c.session, Data: eip.BuildRRData(cpf.Bytes())})
	if _, err := c.recv(); err == nil {
		t.Fatal("panicking session was not closed")
	}
	// The adapter keeps serving.
	dialRaw(t, a.Adapter).register()
	if a.connMgr.lookupByOT(0x60) != nil {
		t.Error("connection registered despite panic")
	}
}

func TestRecoverConnectionGoroutinePanic(t *testing.T) {
	in, out := ioAssemblies()
	a := newUnitAdapter(t, func(c *Config) {
		c.OnConnectionEvent = func(e ConnectionEvent) {
			if e.Type == ConnectionTimedOut {
				panic("boom")
			}
		}
	}, in, out)
	s := newFO(1, 0x61, 10, 6)
	s.otRPI, s.mult = 1000, 0
	if r := a.openFO(s, nil); r.Status != cip.StatusSuccess {
		t.Fatalf("FO: 0x%02X", r.Status)
	}
	eventually(t, 2*time.Second, func() bool { return a.connMgr.lookupByOT(0x61) == nil }, "connection not removed")
	done := make(chan struct{})
	go func() { a.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("connection goroutine did not exit")
	}
}

func TestRecoverIOPacketPanic(t *testing.T) {
	in, out := ioAssemblies()
	a := newUnitAdapter(t, func(c *Config) {
		c.OnConnectionEvent = func(e ConnectionEvent) {
			if e.Type == ConnectionRun {
				panic("boom")
			}
		}
	}, in, out)
	s := newFO(1, 0x62, 10, 6)
	s.otRPI, s.mult = 1_000_000, 3
	if r := a.openFO(s, loopbackOrigin(freePort(t, "udp4"))); r.Status != cip.StatusSuccess {
		t.Fatalf("FO: 0x%02X", r.Status)
	}
	run := byte(1)
	a.handleIOPacketSafe(o2tPacket(0x62, 1, &run, []byte{1, 2, 3, 4}), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 2222})
}

// ---- 9. O->T bound to originator, sequence checked ------------------------------

func TestOTPacketSequenceAndOriginator(t *testing.T) {
	src := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5555}
	setup := func() (*Adapter, *Assembly, *Connection) {
		now := time.Now()
		a := &Adapter{cfg: Config{Now: func() time.Time { return now }}}
		a.connMgr = NewConnectionManager(a)
		asm := NewAssembly(102, AssemblyOutput, 1)
		c := &Connection{OTConnID: 42, Consume: asm, peerAddr: peerAddr{ip: [4]byte{127, 0, 0, 1}, port: 2222}}
		a.connMgr.byOT[42] = c
		return a, asm, c
	}

	a, asm, _ := setup()
	for _, p := range []struct {
		seq  uint32
		data byte
		want byte
	}{
		{5, 0xA, 0xA},
		{5, 0xB, 0xA}, // duplicate
		{4, 0xC, 0xA}, // stale
		{6, 0xD, 0xD},
	} {
		a.handleIOPacket(buildO2TPacket(42, p.seq, []byte{p.data}), src)
		if got := asm.GetByte(0); got != p.want {
			t.Fatalf("after seq %d: data 0x%X, want 0x%X", p.seq, got, p.want)
		}
	}

	a, asm, _ = setup()
	a.handleIOPacket(buildO2TPacket(42, 0xFFFFFFFE, []byte{1}), src)
	a.handleIOPacket(buildO2TPacket(42, 1, []byte{2}), src)
	if asm.GetByte(0) != 2 {
		t.Fatal("sequence wraparound rejected")
	}

	// A connection without a known originator accepts nothing and never
	// learns a destination from traffic.
	a, asm, c := setup()
	c.peerAddr = peerAddr{}
	a.handleIOPacket(buildO2TPacket(42, 1, []byte{9}), &net.UDPAddr{IP: net.IPv4(10, 9, 8, 7), Port: 2222})
	if asm.GetByte(0) != 0 || c.peerAddr.port != 0 || !c.lastInboundAt.IsZero() {
		t.Fatal("packet from unknown source accepted or learned as destination")
	}
}

func TestForwardOpenBindsOriginator(t *testing.T) {
	in, out := ioAssemblies()
	a := startTestAdapter(t, nil, in, out)
	c := dialRaw(t, a.Adapter)
	c.register()
	s := newFO(1, 0x70, 10, 6)
	s.otRPI, s.mult = 1_000_000, 3
	if resp := c.rr(s.request()); resp[2] != cip.StatusSuccess {
		t.Fatalf("Forward_Open status 0x%02X", resp[2])
	}
	conn := a.connMgr.lookupByOT(0x70)
	conn.mu.RLock()
	peer := conn.peerAddr
	conn.mu.RUnlock()
	if peer != (peerAddr{ip: [4]byte{127, 0, 0, 1}, port: 2222}) {
		t.Fatalf("originator %+v, want 127.0.0.1:2222", peer)
	}
}

// ---- 10. explicit writes to an owned output assembly ------------------------------

func TestSetAttributeRejectedWhileOwned(t *testing.T) {
	in, out := ioAssemblies()
	a := newUnitAdapter(t, nil, in, out)
	set := append([]byte{0x10, 0x03, 0x20, 0x04, 0x24, 0x66, 0x30, 0x03}, 1, 2, 3, 4)
	if resp := a.dispatch(set, 0, 0); resp[2] != cip.StatusSuccess {
		t.Fatalf("unowned write: 0x%02X", resp[2])
	}
	s := newFO(1, 0x80, 10, 6)
	s.otRPI, s.mult = 1_000_000, 3
	if r := a.openFO(s, nil); r.Status != cip.StatusSuccess {
		t.Fatalf("FO: 0x%02X", r.Status)
	}
	set[8] = 9
	if resp := a.dispatch(set, 0, 0); resp[2] != cip.StatusObjectStateConflict {
		t.Fatalf("write to owned assembly: 0x%02X, want 0x0C", resp[2])
	}
	if out.GetByte(0) != 1 {
		t.Fatal("owned assembly overwritten")
	}
	if resp := a.dispatch(forwardCloseRequest(1), 0, 0); resp[2] != cip.StatusSuccess {
		t.Fatalf("Forward_Close: 0x%02X", resp[2])
	}
	if resp := a.dispatch(set, 0, 0); resp[2] != cip.StatusSuccess || out.GetByte(0) != 9 {
		t.Fatalf("write after Forward_Close: 0x%02X", resp[2])
	}
}

// ---- 12. advertised identity / TCP/IP object ----------------------------------------

func TestIdentityAndTCPIPUseBindAddress(t *testing.T) {
	a := startTestAdapter(t, nil)
	loop := net.IPv4(127, 0, 0, 1)
	if !a.cfg.Identity.IP.Equal(loop) {
		t.Fatalf("Identity.IP = %v, want bind address 127.0.0.1", a.cfg.Identity.IP)
	}

	var wantMask net.IPMask
	addrs, _ := net.InterfaceAddrs()
	for _, ad := range addrs {
		if n, ok := ad.(*net.IPNet); ok && n.IP.Equal(loop) {
			wantMask = n.Mask
			if len(wantMask) == 16 {
				wantMask = wantMask[12:]
			}
		}
	}
	// Addresses are UDINTs (little-endian), CIP Vol 2 5-4.3.2.5.
	udint := func(b []byte) net.IP {
		v := binary.LittleEndian.Uint32(b)
		return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
	}
	resp := a.dispatch([]byte{0x0E, 0x03, 0x20, 0xF5, 0x24, 0x01, 0x30, 0x05}, 0, 0)
	if resp[2] != cip.StatusSuccess || len(resp) != 4+22 {
		t.Fatalf("attr 5: %x", resp)
	}
	if !udint(resp[4:8]).Equal(loop) {
		t.Errorf("attr 5 IP %v", udint(resp[4:8]))
	}
	if wantMask != nil && !udint(resp[8:12]).Equal(net.IP(wantMask)) {
		t.Errorf("attr 5 mask %v, want %v", udint(resp[8:12]), net.IP(wantMask))
	}

	host, _ := os.Hostname()
	resp = a.dispatch([]byte{0x0E, 0x03, 0x20, 0xF5, 0x24, 0x01, 0x30, 0x06}, 0, 0)
	if resp[2] != cip.StatusSuccess || !bytes.Equal(resp[4:], cipString(host, maxHostNameLen)) {
		t.Errorf("attr 6 %q, want %q", resp[4:], host)
	}
}
