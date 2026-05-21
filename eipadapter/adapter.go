package eipadapter

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"
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

	sessions sessionTable

	wg     sync.WaitGroup
	stopCh chan struct{}
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
	a.registry.Register(NewTCPIPInterfaceObject())
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
		a.cfg.Identity.IP = preferredLocalIPv4()
	}

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
	_ = a.tcpListener.Close()
	_ = a.udpDiscover.Close()
	_ = a.udpIO.Close()
	a.connMgr.closeAll()
	a.wg.Wait()
	return nil
}

// Close shuts the adapter down. Safe to call once; subsequent calls are
// no-ops.
func (a *Adapter) Close() error {
	select {
	case <-a.stopCh:
	default:
		close(a.stopCh)
	}
	return nil
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
