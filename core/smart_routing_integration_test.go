package main

import (
	"context"
	"core/failover"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metacubex/http"
	"github.com/metacubex/mihomo/adapter/outbound"
	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
)

type localSmartProxy struct {
	C.Proxy
	address string
}

func (p *localSmartProxy) DialContext(ctx context.Context, m *C.Metadata) (C.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", p.address)
	if err != nil {
		return nil, err
	}
	return outbound.NewConn(conn, p.Adapter()), nil
}

func TestSmartFailoverRoutesRequestsThroughRecoveredNodes(t *testing.T) {
	raw := config.DefaultRawConfig()
	for _, name := range []string{"HK01", "Japan", "Singapore"} {
		raw.Proxy = append(raw.Proxy, map[string]any{"name": name, "type": "socks5", "server": "127.0.0.1", "port": 10002})
	}
	if err := addSmartFailover(raw); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseRawConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	previous := smartGroup
	defer func() { smartGroup = previous }()
	installSmartFailover(cfg)
	s := smartGroup
	proxies := map[string]C.Proxy{}
	failed := map[string]*atomic.Bool{}
	var hkRequests atomic.Int32
	for _, name := range []string{"HK01", "Japan", "Singapore"} {
		name := name
		failure := &atomic.Bool{}
		failed[name] = failure
		server := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			if name == "HK01" {
				hkRequests.Add(1)
			}
			if failure.Load() {
				w.WriteHeader(stdhttp.StatusServiceUnavailable)
				return
			}
			fmt.Fprintf(w, "loc=JP\nnode=%s\n", name)
		}))
		defer server.Close()
		proxies[name] = &localSmartProxy{Proxy: cfg.Proxies[name], address: strings.TrimPrefix(server.URL, "http://")}
	}
	policy := failover.Policy{MaxDelay: time.Second}
	probe := func(ctx context.Context, name string) (time.Duration, error) {
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return proxies[name].DialContext(ctx, &C.Metadata{Host: "service.test", DstPort: 80, NetWork: C.TCP})
		}}
		defer transport.CloseIdleConnections()
		return probeSmartHTTP(ctx, &http.Client{Transport: transport}, "http://service.test/trace")
	}
	selectNode := func(name string, _ bool) {
		p := proxies[name]
		if p == nil {
			p = s.reject
		}
		s.choice.Store(&smartChoice{proxy: p})
	}
	names := []string{"HK01", "Japan", "Singapore"}
	now := time.Now()
	policy.Step(context.Background(), now, names, probe, selectNode)
	first := s.Now()
	if first != "Japan" && first != "Singapore" {
		t.Fatalf("no verified node selected: %s", first)
	}
	failed[first].Store(true)
	policy.Step(context.Background(), now.Add(failover.CheckInterval), names, probe, selectNode)
	if s.Now() == first || s.Now() == "REJECT" {
		t.Fatal("failed node was not replaced")
	}
	conn, err := s.DialContext(context.Background(), &C.Metadata{Host: "service.test", DstPort: 80, NetWork: C.TCP})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if !containsChain(conn.Chains(), smartGroupName) || !containsChain(conn.Chains(), s.Now()) {
		t.Fatalf("request used wrong routing chain: %v", conn.Chains())
	}
	if _, err := io.WriteString(conn, "GET /app HTTP/1.1\r\nHost: service.test\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(conn)
	if err != nil || !strings.Contains(string(body), "node="+s.Now()) {
		t.Fatalf("request did not reach the replacement: %s error=%v", body, err)
	}
	failed[s.Now()].Store(true)
	policy.Step(context.Background(), now.Add(2*failover.CheckInterval), names, probe, selectNode)
	if s.Now() != "REJECT" {
		t.Fatal("all-failed state did not reject proxy traffic")
	}
	failed["Japan"].Store(false)
	policy.Step(context.Background(), now.Add(3*failover.CheckInterval), names, probe, selectNode)
	if s.Now() != "Japan" || hkRequests.Load() != 0 {
		t.Fatalf("recovery=%s Hong Kong requests=%d", s.Now(), hkRequests.Load())
	}
}
