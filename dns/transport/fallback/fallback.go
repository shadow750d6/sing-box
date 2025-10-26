package fallback

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"

	mDNS "github.com/miekg/dns"
)

var (
	_ adapter.WrappingDNSTransport = (*Transport)(nil)
)

func RegisterTransport(registry *dns.TransportRegistry) {
	dns.RegisterTransport[option.FallbackDNSServerOptions](registry, C.DNSTypeFallback, New)
}

type Transport struct {
	dns.TransportAdapter
	logger              log.ContextLogger
	servers             []adapter.DNSTransport
	fallbackTimeout     time.Duration
	reenableTimeout     time.Duration
	disabledUntilTimeMs []atomic.Uint64
}

func New(ctx context.Context, logger log.ContextLogger, tag string, options option.FallbackDNSServerOptions) (adapter.DNSTransport, error) {
	if len(options.Servers) <= 1 {
		return nil, E.New("requires at least two servers")
	}
	if options.FallbackTimeout <= 0 {
		return nil, E.New("invalid fallback_timeout: ", options.FallbackTimeout)
	}
	if options.ReenableTimeout <= 0 {
		return nil, E.New("invalid reenable_timeout: ", options.ReenableTimeout)
	}

	return &Transport{
		TransportAdapter:    dns.NewTransportAdapter(C.DNSTypeFallback, tag, []string(options.Servers)),
		logger:              logger,
		fallbackTimeout:     time.Duration(options.FallbackTimeout),
		reenableTimeout:     time.Duration(options.ReenableTimeout),
		disabledUntilTimeMs: make([]atomic.Uint64, len(options.Servers)),
	}, nil
}

func (t *Transport) Start(stage adapter.StartStage) error {
	return nil
}

func (t *Transport) ReceiveDepedencies(deps []adapter.DNSTransport) error {
	if len(deps) != len(t.Dependencies()) {
		return E.New("mismatched dependencies")
	}
	t.servers = deps
	return nil
}

func (t *Transport) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	nowMs := time.Now().UnixMilli()
	for i, server := range t.servers {
		if t.disabledUntilTimeMs[i].Load() > uint64(nowMs) {
			continue
		}
		subCtx, cancel := context.WithTimeout(ctx, t.fallbackTimeout)
		defer cancel()
		response, err := server.Exchange(subCtx, message)
		if err == nil {
			t.logger.TraceContext(subCtx, "sub server with tag ", server.Tag(), " query succeeds")
			return response, nil
		}
		reenableTime := uint64(nowMs) + uint64(t.reenableTimeout.Milliseconds())
		t.logger.WarnContext(subCtx, "sub server with tag ", server.Tag(), " failed and disabled, reenable at ", reenableTime, ": ", err)
		t.disabledUntilTimeMs[i].Store(reenableTime)
	}
	return nil, E.New("all sub servers failed")
}

func (t *Transport) Close() error {
	return nil
}
