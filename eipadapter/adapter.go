package eipadapter

import (
	"context"
	"fmt"
	"net"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/yatesdr/plcio/logging"
)

// Config configures an Adapter. Address fields default to the standard
// EtherNet/IP ports (44818) bound to all interfaces.
type Config struct {
	// BindAddr is the IPv4 address to listen on. Default "0.0.0.0".
	BindAddr string
	// TCPPort defaults to 44818.
	TCPPort uint16
	// UDPPort defaults to 44818 (for ListIdentity broadcasts).
	UDPPort uint16
	// IOPort is the UDP port for Class 1 cyclic I/O. Default 2222 per ODVA.
	IOPort uint16

	// Identity is mandatory.
	Identity Identity

	// Assemblies the adapter exposes. Class 0x04. Forward_Open requests
	// reference these via their instance IDs.
	Assemblies []*Assembly

	// OnForwardOpen is invoked when a scanner attempts to open a Class 1
	// connection. Return non-nil to reject. nil callback means accept all
	// well-formed requests.
	OnForwardOpen func(*ForwardOpenContext) error

	// OnConnectionEvent, if set, is called when a Class 1 I/O connection is
	// opened, closed (Forward_Close or adapter shutdown) or timed out, and
	// when the Run/Idle header of the scanner's O->T data changes. It runs
	// synchronously on an adapter goroutine and must not block.
	//
	// Output assembly data is left untouched when a connection closes, times
	// out or goes Idle: the last received bytes remain in the assembly. Use
	// this callback (or Assembly.RunIdle) to drive outputs to a safe state.
	OnConnectionEvent func(ConnectionEvent)

	// MaxConnections caps the number of concurrent Class 1 I/O connections.
	// Further Forward_Opens are rejected with extended status 0x0113 (out of
	// connections). Default 32, matching the Message Router's advertised
	// maximum.
	MaxConnections int

	// MaxTCPConnections caps the number of concurrent TCP (encapsulation)
	// connections. Connections beyond the limit are closed immediately
	// after accept. Default 64.
	MaxTCPConnections int

	// Now is overridable for tests. Default time.Now.
	Now func() time.Time
}

func (c *Config) defaults() {
	if c.BindAddr == "" {
		c.BindAddr = "0.0.0.0"
	}
	if c.TCPPort == 0 {
		c.TCPPort = 44818
	}
	if c.UDPPort == 0 {
		c.UDPPort = 44818
	}
	if c.IOPort == 0 {
		c.IOPort = 2222
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.MaxConnections <= 0 {
		c.MaxConnections = 32
	}
	if c.MaxTCPConnections <= 0 {
		c.MaxTCPConnections = 64
	}
}

// Adapter is a running EtherNet/IP adapter instance.
type Adapter struct {
	cfg Config

	tcpListener *net.TCPListener
	udpDiscover *net.UDPConn
	udpIO       *net.UDPConn

	registry *Registry
	connMgr  *ConnectionManager
	asmByInstance map[uint32]*Assembly
	tcpip         *TCPIPInterfaceObject

	sessions sessionTable

	// tcpConns tracks live TCP connections so shutdown can close them and
	// MaxTCPConnections can be enforced. tcpClosing is set once shutdown has
	// begun; nothing new is tracked after that.
	tcpMu      sync.Mutex
	tcpConns   map[net.Conn]struct{}
	tcpClosing bool

	wg       sync.WaitGroup
	stopCh   chan struct{}
	stopOnce sync.Once
}

// New validates configuration and binds the adapter's TCP and UDP sockets.
// It does not begin serving — call Serve to start accepting connections.
func New(cfg Config) (*Adapter, error) {
	cfg.defaults()
	if cfg.Identity.ProductName == "" {
		return nil, fmt.Errorf("eipadapter: Identity.ProductName is required")
	}
	if cfg.Identity.VendorID == 0 {
		return nil, fmt.Errorf("eipadapter: Identity.VendorID is required")
	}

	a := &Adapter{
		cfg:           cfg,
		registry:      NewRegistry(),
		asmByInstance: make(map[uint32]*Assembly),
		stopCh:        make(chan struct{}),
	}

	a.registry.Register(NewIdentityObject(&a.cfg.Identity))
	a.registry.Register(NewMessageRouterObject())
	a.tcpip = NewTCPIPInterfaceObject()
	a.registry.Register(a.tcpip)
	a.registry.Register(NewEthernetLinkObject())

	for _, asm := range cfg.Assemblies {
		if _, dup := a.asmByInstance[asm.InstanceID]; dup {
			return nil, fmt.Errorf("eipadapter: duplicate Assembly instance %d", asm.InstanceID)
		}
		a.asmByInstance[asm.InstanceID] = asm
		a.registry.Register(asm)
	}

	a.connMgr = NewConnectionManager(a)
	a.registry.Register(a.connMgr)

	tcpAddr := net.JoinHostPort(cfg.BindAddr, strconv.Itoa(int(cfg.TCPPort)))
	la, err := net.ResolveTCPAddr("tcp4", tcpAddr)
	if err != nil {
		return nil, fmt.Errorf("eipadapter: resolve TCP %q: %w", tcpAddr, err)
	}
	tl, err := net.ListenTCP("tcp4", la)
	if err != nil {
		return nil, fmt.Errorf("eipadapter: listen TCP %q: %w", tcpAddr, err)
	}
	a.tcpListener = tl

	udpAddr := net.JoinHostPort(cfg.BindAddr, strconv.Itoa(int(cfg.UDPPort)))
	ua, err := net.ResolveUDPAddr("udp4", udpAddr)
	if err != nil {
		_ = tl.Close()
		return nil, fmt.Errorf("eipadapter: resolve UDP %q: %w", udpAddr, err)
	}
	uc, err := net.ListenUDP("udp4", ua)
	if err != nil {
		_ = tl.Close()
		return nil, fmt.Errorf("eipadapter: listen UDP %q: %w", udpAddr, err)
	}
	a.udpDiscover = uc

	ioAddr := net.JoinHostPort(cfg.BindAddr, strconv.Itoa(int(cfg.IOPort)))
	iua, err := net.ResolveUDPAddr("udp4", ioAddr)
	if err != nil {
		_ = tl.Close()
		_ = uc.Close()
		return nil, fmt.Errorf("eipadapter: resolve I/O UDP %q: %w", ioAddr, err)
	}
	iuc, err := net.ListenUDP("udp4", iua)
	if err != nil {
		_ = tl.Close()
		_ = uc.Close()
		return nil, fmt.Errorf("eipadapter: listen I/O UDP %q: %w", ioAddr, err)
	}
	a.udpIO = iuc

	if a.cfg.Identity.Port == 0 {
		a.cfg.Identity.Port = a.cfg.TCPPort
	}
	if a.cfg.Identity.IP == nil {
		// Advertise the bind address when it names a specific interface, so
		// a multi-homed host (separate IT/OT NICs) reports the IP scanners
		// can actually reach. Otherwise fall back to the default-route IP.
		if ip4 := la.IP.To4(); ip4 != nil && !ip4.IsUnspecified() {
			a.cfg.Identity.IP = ip4
		} else {
			a.cfg.Identity.IP = preferredLocalIPv4()
		}
	}
	a.tcpip.setInterface(a.cfg.Identity.IP)

	return a, nil
}

// TCPAddr returns the address the adapter is listening on. Useful for tests
// where the port is auto-assigned.
func (a *Adapter) TCPAddr() *net.TCPAddr { return a.tcpListener.Addr().(*net.TCPAddr) }

// IOAddr returns the UDP address used for Class 1 cyclic I/O.
func (a *Adapter) IOAddr() *net.UDPAddr { return a.udpIO.LocalAddr().(*net.UDPAddr) }

// Assembly returns the Assembly with the given instance, or nil.
func (a *Adapter) Assembly(instance uint32) *Assembly { return a.asmByInstance[instance] }

// Serve runs the adapter loops until ctx is cancelled or Close is called.
func (a *Adapter) Serve(ctx context.Context) error {
	a.wg.Add(3)
	go a.serveTCP(ctx)
	go a.serveDiscoverUDP(ctx)
	go a.serveIOUDP(ctx)

	select {
	case <-ctx.Done():
	case <-a.stopCh:
	}
	a.shutdown()
	a.wg.Wait()
	return nil
}

// Close shuts the adapter down: it closes the listener, every TCP
// connection and both UDP sockets, and stops all I/O connections. A running
// Serve returns once its goroutines have exited. Safe to call more than
// once; subsequent calls are no-ops.
func (a *Adapter) Close() error {
	a.shutdown()
	return nil
}

// shutdown tears down all sockets and connections exactly once. Closing the
// sockets unblocks every reader, so Serve's goroutines exit promptly.
func (a *Adapter) shutdown() {
	first := false
	a.stopOnce.Do(func() {
		first = true
		close(a.stopCh)
		_ = a.tcpListener.Close()
		_ = a.udpDiscover.Close()
		_ = a.udpIO.Close()
		a.closeTCPConns()
	})
	// Outside the Once: closeAll fires ConnectionClosed callbacks, which
	// may themselves call Close.
	if first {
		a.connMgr.closeAll()
	}
}

// trackConn registers an accepted TCP connection. It returns false when the
// adapter is shutting down or MaxTCPConnections is reached; the caller must
// then close conn itself.
func (a *Adapter) trackConn(conn net.Conn) bool {
	a.tcpMu.Lock()
	defer a.tcpMu.Unlock()
	if a.tcpClosing || len(a.tcpConns) >= a.cfg.MaxTCPConnections {
		return false
	}
	if a.tcpConns == nil {
		a.tcpConns = make(map[net.Conn]struct{})
	}
	a.tcpConns[conn] = struct{}{}
	return true
}

// untrackConn forgets conn and closes it.
func (a *Adapter) untrackConn(conn net.Conn) {
	a.tcpMu.Lock()
	delete(a.tcpConns, conn)
	a.tcpMu.Unlock()
	_ = conn.Close()
}

func (a *Adapter) closeTCPConns() {
	a.tcpMu.Lock()
	defer a.tcpMu.Unlock()
	a.tcpClosing = true
	for c := range a.tcpConns {
		_ = c.Close()
	}
}

// recoverPanic is deferred at the top of every server goroutine (and around
// each UDP datagram) so a bug triggered by one malformed packet, or a
// panicking application callback, cannot take down the host process. The
// panic is logged with its stack; the caller then tears down only the
// affected connection.
func recoverPanic(where string) {
	if r := recover(); r != nil {
		logging.DebugLog("eipadapter", "recovered panic in %s: %v\n%s", where, r, debug.Stack())
	}
}

// preferredLocalIPv4 returns a best-effort outbound IPv4 for use in the
// Identity object's socket address. Falls back to 0.0.0.0 if none is found.
func preferredLocalIPv4() net.IP {
	c, err := net.Dial("udp4", "8.8.8.8:80")
	if err == nil {
		defer c.Close()
		if la, ok := c.LocalAddr().(*net.UDPAddr); ok {
			return la.IP.To4()
		}
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return net.IPv4zero
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ip, _, err := net.ParseCIDR(a.String())
			if err != nil {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				return ip4
			}
		}
	}
	return net.IPv4zero
}
