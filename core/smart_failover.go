package main

import (
	"context"
	"core/failover"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/metacubex/http"
	"github.com/metacubex/http/httptrace"
	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/outboundgroup"
	"github.com/metacubex/mihomo/common/callback"
	N "github.com/metacubex/mihomo/common/net"
	"github.com/metacubex/mihomo/component/ca"
	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/log"
	"github.com/metacubex/mihomo/tunnel"
	"github.com/metacubex/mihomo/tunnel/statistic"
)

const smartGroupName = "FlClash Auto (non-HK)"
const smartProbeURL = "https://chatgpt.com/cdn-cgi/trace"

var smartGroup *smartSelector
var smartCancel context.CancelFunc
var smartMaxDelay = 200

type smartChoice struct {
	proxy C.Proxy
}

type smartSelector struct {
	*outboundgroup.Selector
	choice   atomic.Pointer[smartChoice]
	reject   C.Proxy
	wake     chan struct{}
	statusMu sync.Mutex
	results  map[string]smartNodeResult
	checking bool
}

type smartNodeResult struct {
	DelayMs   int64  `json:"delayMs"`
	Reason    string `json:"reason"`
	CheckedAt int64  `json:"checkedAt"`
}

func (s *smartSelector) current() C.Proxy {
	if choice := s.choice.Load(); choice != nil {
		return choice.proxy
	}
	return s.reject
}

func (s *smartSelector) Now() string                      { return s.current().Name() }
func (s *smartSelector) Unwrap(*C.Metadata, bool) C.Proxy { return s.current() }
func (s *smartSelector) SupportUDP() bool                 { return true }
func (s *smartSelector) IsL3Protocol(m *C.Metadata) bool  { return s.current().IsL3Protocol(m) }
func (s *smartSelector) Set(string) error {
	return errors.New("this group is managed by automatic failover")
}
func (s *smartSelector) ForceSet(string) {}

func (s *smartSelector) failed() {
	if s.current() == s.reject {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *smartSelector) DialContext(ctx context.Context, m *C.Metadata) (C.Conn, error) {
	p := s.current()
	if p == s.reject {
		return nil, errors.New("no verified non-Hong Kong node is available")
	}
	c, err := p.DialContext(ctx, m)
	if err != nil {
		s.failed()
	} else {
		c.AppendToChains(s)
		if N.NeedHandshake(c) {
			c = callback.NewFirstWriteCallBackConn(c, func(err error) {
				if err != nil {
					s.failed()
				}
			})
		}
	}
	return c, err
}

func (s *smartSelector) ListenPacketContext(ctx context.Context, m *C.Metadata) (C.PacketConn, error) {
	p := s.current()
	if p == s.reject {
		return nil, errors.New("no verified non-Hong Kong node is available")
	}
	c, err := p.ListenPacketContext(ctx, m)
	if err != nil {
		s.failed()
	} else {
		c.AppendToChains(s)
	}
	return c, err
}

func (s *smartSelector) MarshalJSON() ([]byte, error) {
	names := []string{}
	for _, p := range s.candidates() {
		names = append(names, p.Name())
	}
	sort.Strings(names)
	return json.Marshal(map[string]any{
		"type": "URLTest", "now": s.Now(), "all": names,
		"testUrl": smartProbeURL, "fixed": "", "hidden": false,
		"emptyFallback": "REJECT",
	})
}

func (s *smartSelector) candidates() map[string]C.Proxy {
	proxies := map[string]C.Proxy{}
	for _, p := range s.GetProxies(false) {
		if p.Type() < C.Shadowsocks {
			continue
		}
		if failover.EligibleName(p.Name()) {
			proxies[p.Name()] = p
		}
	}
	return proxies
}

func addSmartFailover(raw *config.RawConfig) error {
	targets := map[string]bool{}
	for _, proxy := range raw.Proxy {
		if name, ok := proxy["name"].(string); ok {
			targets[name] = true
		}
	}
	for _, group := range raw.ProxyGroup {
		if name, ok := group["name"].(string); ok {
			targets[name] = true
		}
	}
	if targets[smartGroupName] || raw.ProxyProvider[smartGroupName] != nil {
		return errors.New("reserved automatic failover group name already exists")
	}
	for _, name := range []string{"DIRECT", "REJECT", "REJECT-DROP", "PASS", "COMPATIBLE"} {
		delete(targets, name)
	}
	raw.ProxyGroup = append(raw.ProxyGroup, map[string]any{
		"name": smartGroupName, "type": "select", "include-all": true,
		"proxies": []string{"REJECT"}, "default-selected": "REJECT",
		"empty-fallback": "REJECT", "hidden": false,
	})
	rewrite := func(rules []string) {
		for i, rule := range rules {
			parts := strings.Split(rule, ",")
			if len(parts) < 2 || strings.TrimSpace(parts[0]) == "SUB-RULE" {
				continue
			}
			target := len(parts) - 1
			for target > 0 && (strings.TrimSpace(parts[target]) == "no-resolve" || strings.TrimSpace(parts[target]) == "src") {
				target--
			}
			if targets[strings.TrimSpace(parts[target])] {
				parts[target] = smartGroupName
				rules[i] = strings.Join(parts, ",")
			}
		}
	}
	rewrite(raw.Rule)
	for _, rules := range raw.SubRules {
		rewrite(rules)
	}
	serviceRules := []string{}
	for _, host := range []string{"chatgpt.com", "openai.com", "oaistatic.com", "oaiusercontent.com"} {
		serviceRules = append(serviceRules, "DOMAIN-SUFFIX,"+host+","+smartGroupName)
	}
	raw.Rule = append(serviceRules, raw.Rule...)
	return nil
}

func installSmartFailover(cfg *config.Config) {
	p, ok := cfg.Proxies[smartGroupName].(*adapter.Proxy)
	if !ok {
		return
	}
	s, ok := p.ProxyAdapter.(*outboundgroup.Selector)
	if !ok {
		return
	}
	smartGroup = &smartSelector{Selector: s, reject: cfg.Proxies["REJECT"], wake: make(chan struct{}, 1), results: map[string]smartNodeResult{}}
	p.ProxyAdapter = smartGroup
}

func stopSmartFailover() {
	if smartCancel != nil {
		smartCancel()
		smartCancel = nil
	}
}

func startSmartFailover() {
	if smartCancel != nil {
		return
	}
	if smartGroup == nil || !isRunning {
		return
	}
	s := smartGroup
	s.choice.Store(&smartChoice{proxy: s.reject})
	ctx, cancel := context.WithCancel(context.Background())
	smartCancel = cancel
	maxDelay := failover.MaxDelay(smartMaxDelay)
	go func() {
		policy := failover.Policy{MaxDelay: maxDelay}
		var lastCheck time.Time
		for {
			if remaining := time.Second - time.Since(lastCheck); remaining > 0 {
				timer := time.NewTimer(remaining)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			lastCheck = time.Now()
			proxies := s.candidates()
			s.statusMu.Lock()
			s.checking = true
			for name := range s.results {
				if proxies[name] == nil {
					delete(s.results, name)
				}
			}
			s.statusMu.Unlock()
			names := make([]string, 0, len(proxies))
			for name := range proxies {
				names = append(names, name)
			}
			policy.Step(ctx, time.Now(), names, func(ctx context.Context, name string) (time.Duration, error) {
				delay, err := probeSmartNode(ctx, proxies[name])
				if !errors.Is(ctx.Err(), context.Canceled) {
					reason := ""
					if err != nil {
						reason = err.Error()
					} else if delay >= maxDelay {
						reason = "latency_limit"
					}
					s.statusMu.Lock()
					s.results[name] = smartNodeResult{DelayMs: max(1, delay.Milliseconds()), Reason: reason, CheckedAt: time.Now().UnixMilli()}
					s.statusMu.Unlock()
				}
				value := int32(-1)
				if err == nil {
					value = int32(max(1, delay.Milliseconds()))
				}
				if !errors.Is(ctx.Err(), context.Canceled) {
					sendMessage(Message{Type: DelayMessage, Data: &Delay{Name: name, Url: smartProbeURL, Value: value}})
				}
				return delay, err
			}, func(name string, failed bool) {
				runLock.Lock()
				defer runLock.Unlock()
				if ctx.Err() != nil || smartGroup != s || !isRunning {
					return
				}
				previous := s.Now()
				p := proxies[name]
				if p == nil {
					p = s.reject
				}
				s.choice.Store(&smartChoice{proxy: p})
				if failed {
					statistic.DefaultManager.Range(func(c statistic.Tracker) bool {
						info := c.Info()
						if containsChain(info.Chain, smartGroupName) && containsChain(info.Chain, previous) {
							_ = c.Close()
						}
						return true
					})
				}
				if previous != p.Name() {
					log.Infoln("[AutoFailover] %s -> %s", previous, p.Name())
				}
				value := int32(-1)
				if p != s.reject {
					s.statusMu.Lock()
					value = int32(s.results[p.Name()].DelayMs)
					s.statusMu.Unlock()
				}
				sendMessage(Message{Type: DelayMessage, Data: &Delay{Name: smartGroupName, Url: smartProbeURL, Value: value}})
			})
			s.statusMu.Lock()
			s.checking = false
			s.statusMu.Unlock()
			if ctx.Err() != nil {
				return
			}
			timer := time.NewTimer(failover.CheckInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			case <-s.wake:
				timer.Stop()
			}
		}
	}()
}

func containsChain(chain C.Chain, name string) bool {
	for _, item := range chain {
		if item == name {
			return true
		}
	}
	return false
}

func probeSmartNode(ctx context.Context, proxy C.Proxy) (time.Duration, error) {
	if proxy == nil {
		return 0, errors.New("proxy unavailable")
	}
	tlsConfig, err := ca.GetTLSConfig(ca.Option{})
	if err != nil {
		return 0, err
	}
	transport := &http.Transport{
		TLSClientConfig: tlsConfig, MaxIdleConnsPerHost: 1,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return proxy.DialContext(ctx, &C.Metadata{Host: "chatgpt.com", DstPort: 443, NetWork: C.TCP, Type: C.INNER})
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return probeSmartHTTP(ctx, client, smartProbeURL)
}

func probeSmartHTTP(ctx context.Context, client *http.Client, url string) (time.Duration, error) {
	var delay time.Duration
	for i := 0; i < 2; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return 0, err
		}
		start := time.Now()
		var written atomic.Pointer[time.Time]
		if i == 1 {
			req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
				WroteRequest: func(info httptrace.WroteRequestInfo) {
					if info.Err == nil {
						now := time.Now()
						written.Store(&now)
					}
				},
			}))
		}
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			return 0, errors.New("connection_failed")
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if readErr != nil {
			return 0, readErr
		}
		if resp.StatusCode != http.StatusOK {
			return 0, fmt.Errorf("service_refused:%d", resp.StatusCode)
		}
		if !failover.NonHongKongTrace(string(body)) {
			return 0, errors.New("exit_region")
		}
		if sent := written.Load(); sent != nil {
			start = *sent
		}
		delay = time.Since(start)
	}
	return delay, nil
}

func handleSmartFailoverStatus(recheck bool) map[string]any {
	runLock.Lock()
	defer runLock.Unlock()
	status := map[string]any{"enabled": smartGroup != nil, "running": isRunning, "maxDelayMs": failover.MaxDelay(smartMaxDelay).Milliseconds(), "mode": tunnel.Mode().String()}
	if smartGroup == nil {
		status["state"] = "disabled"
		return status
	}
	s := smartGroup
	if recheck && isRunning {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	candidates := s.candidates()
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	state := "unavailable"
	if !isRunning {
		state = "stopped"
	} else if s.current() != s.reject {
		state = "ready"
	} else if len(candidates) == 0 {
		state = "empty"
	} else if s.checking {
		state = "checking"
	}
	results := map[string]smartNodeResult{}
	for name, result := range s.results {
		if candidates[name] != nil {
			results[name] = result
		}
	}
	status["state"] = state
	status["current"] = s.Now()
	status["candidateCount"] = len(candidates)
	status["results"] = results
	status["delayMs"] = s.results[s.Now()].DelayMs
	return status
}

func selectSmartGlobal() {
	if smartGroup == nil {
		return
	}
	if p, ok := tunnel.Proxies()["GLOBAL"].(*adapter.Proxy); ok {
		if selector, ok := p.ProxyAdapter.(outboundgroup.SelectAble); ok {
			selector.ForceSet(smartGroupName)
		}
	}
}
