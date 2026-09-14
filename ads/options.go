package ads

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

type options struct {
	targetNetId                                                                    AmsNetId
	targetPort                                                                     uint16
	localNetId                                                                     AmsNetId
	localPort                                                                      uint16
	targetSet, localSet                                                            bool
	timeout                                                                        time.Duration
	maxPayload, maxBatch, maxMetadata, maxSymbols, maxTypes, maxDepth, maxElements uint32
	err                                                                            error
	stringEncodingSet, utf8Strings                                                 bool
	deadline                                                                       time.Time // operation-local; never retained in connection options
}

// Option configures ADS identity, timeouts and resource limits.
type Option func(*options)

func defaultOptions() options {
	return options{targetPort: PortTC3PLC1, timeout: 5 * time.Second, maxPayload: 1 << 20,
		maxBatch: 500, maxMetadata: 32 << 20, maxSymbols: 100000, maxTypes: 100000,
		maxDepth: 64, maxElements: 1000000}
}

// WithAmsNetId configures the target AMS identity independently of the TCP host.
func WithAmsNetId(id string) Option {
	return func(o *options) {
		o.targetSet = true
		n, err := ParseAmsNetId(id)
		o.targetNetId = n
		o.err = errors.Join(o.err, err)
	}
}

// WithAmsPort configures the target runtime AMS port (default 851).
func WithAmsPort(port uint16) Option { return func(o *options) { o.targetPort = port } }

// WithLocalAmsNetId configures the identity expected by the PLC's existing route.
func WithLocalAmsNetId(id string) Option {
	return func(o *options) {
		o.localSet = true
		n, err := ParseAmsNetId(id)
		o.localNetId = n
		o.err = errors.Join(o.err, err)
	}
}

// WithLocalAmsPort configures the local AMS port, independently of TCP ports.
func WithLocalAmsPort(port uint16) Option {
	return func(o *options) {
		if port == 0 {
			o.err = errors.Join(o.err, fmt.Errorf("local AMS port must be positive"))
		}
		o.localPort = port
	}
}

// WithTimeout sets the budget for a complete operation, including waiting,
// metadata, batching and one permitted read recovery. Default: five seconds.
func WithTimeout(timeout time.Duration) Option { return func(o *options) { o.timeout = timeout } }

// WithStringEncoding overrides STRING storage encoding for this connection.
// Accepts Latin-1 or UTF-8. Without an override, published TcEncoding attributes
// are honored and Latin-1 is the default. WSTRING remains UCS-2 little endian.
func WithStringEncoding(encoding string) Option {
	return func(o *options) {
		o.stringEncodingSet = true
		switch {
		case strings.EqualFold(encoding, "Latin-1"):
			o.utf8Strings = false
		case strings.EqualFold(encoding, "UTF-8"):
			o.utf8Strings = true
		default:
			o.err = errors.Join(o.err, fmt.Errorf("unsupported STRING encoding %q", encoding))
		}
	}
}

// WithMaxPayload sets the ADS command payload budget per request/response.
func WithMaxPayload(bytes uint32) Option { return func(o *options) { o.maxPayload = bytes } }

// WithMaxBatchItems sets the SumUp item cap (default 500), also limited by bytes.
func WithMaxBatchItems(count uint32) Option { return func(o *options) { o.maxBatch = count } }

// WithMetadataLimits bounds aggregate uploaded bytes, symbols and datatype entries.
func WithMetadataLimits(bytes, symbols, types uint32) Option {
	return func(o *options) { o.maxMetadata, o.maxSymbols, o.maxTypes = bytes, symbols, types }
}

// WithExpansionLimits bounds schema depth and expanded elements/members.
func WithExpansionLimits(depth, elements uint32) Option {
	return func(o *options) { o.maxDepth, o.maxElements = depth, elements }
}

func configure(address string, opts []Option) (string, options, error) {
	cfg := defaultOptions()
	for _, opt := range opts {
		if opt == nil {
			return "", cfg, fmt.Errorf("nil ADS option")
		}
		opt(&cfg)
	}
	if cfg.err != nil {
		return "", cfg, cfg.err
	}
	if cfg.timeout <= 0 || cfg.targetPort == 0 {
		return "", cfg, fmt.Errorf("ADS timeout and runtime port must be positive")
	}
	if cfg.maxPayload < 24 || uint64(cfg.maxPayload)+38 > uint64(int(^uint(0)>>1)) || cfg.maxPayload > ^uint32(0)-38 || cfg.maxBatch == 0 || cfg.maxMetadata == 0 || cfg.maxSymbols == 0 || cfg.maxTypes == 0 || cfg.maxDepth == 0 || cfg.maxElements == 0 {
		return "", cfg, fmt.Errorf("invalid ADS resource limits")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		if net.ParseIP(address) != nil || !containsColon(address) {
			host, port = address, strconv.Itoa(DefaultTCPPort)
		} else {
			return "", cfg, fmt.Errorf("ADS TCP address: %w", err)
		}
	}
	p, err := strconv.ParseUint(port, 10, 16)
	if err != nil || p == 0 || host == "" {
		return "", cfg, fmt.Errorf("invalid ADS TCP endpoint %q", address)
	}
	if !cfg.targetSet {
		cfg.targetNetId, err = AmsNetIdFromIP(host)
		if err != nil {
			return "", cfg, fmt.Errorf("specify AMS Net ID for TCP host %q: %w", host, err)
		}
	}
	if cfg.targetNetId.IsZero() || (cfg.localSet && cfg.localNetId.IsZero()) {
		return "", cfg, fmt.Errorf("zero AMS identity is not a routable endpoint")
	}
	return net.JoinHostPort(host, port), cfg, nil
}

func containsColon(s string) bool {
	for _, r := range s {
		if r == ':' {
			return true
		}
	}
	return false
}
