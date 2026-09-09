package main

import (
	"context"
	"reflect"
	"testing"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
)

func TestSmartRulesPreserveDirectRejectAndSubRules(t *testing.T) {
	raw := config.DefaultRawConfig()
	raw.Proxy = []map[string]any{{"name": "Japan"}}
	raw.ProxyGroup = []map[string]any{{"name": "Proxy"}}
	raw.Rule = []string{"DOMAIN-SUFFIX,local,DIRECT", "DOMAIN,ads.test,REJECT", "IP-CIDR,192.0.2.0/24,Proxy,no-resolve", "SUB-RULE,(NETWORK,TCP),web", "MATCH,Japan"}
	raw.SubRules = map[string][]string{"web": {"DOMAIN,example.com,Proxy", "MATCH,DIRECT"}}
	if err := addSmartFailover(raw); err != nil {
		t.Fatal(err)
	}
	want := []string{"DOMAIN-SUFFIX,local,DIRECT", "DOMAIN,ads.test,REJECT", "IP-CIDR,192.0.2.0/24," + smartGroupName + ",no-resolve", "SUB-RULE,(NETWORK,TCP),web", "MATCH," + smartGroupName}
	if !reflect.DeepEqual(raw.Rule[4:], want) {
		t.Fatalf("rules: %v", raw.Rule)
	}
	if raw.SubRules["web"][0] != "DOMAIN,example.com,"+smartGroupName || raw.SubRules["web"][1] != "MATCH,DIRECT" {
		t.Fatal(raw.SubRules)
	}
	if err := addSmartFailover(raw); err == nil {
		t.Fatal("accepted reserved group collision")
	}
}

func TestSmartSelectorStartsClosedAndCannotBeManuallyPinned(t *testing.T) {
	raw := config.DefaultRawConfig()
	raw.Proxy = []map[string]any{
		{"name": "HK01", "type": "socks5", "server": "127.0.0.1", "port": 10001},
		{"name": "Japan", "type": "socks5", "server": "127.0.0.1", "port": 10002},
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
	s := cfg.Proxies[smartGroupName].(*adapter.Proxy).ProxyAdapter.(*smartSelector)
	if s.Now() != "REJECT" {
		t.Fatal("unverified node selected")
	}
	if len(s.candidates()) != 1 || s.candidates()["Japan"] == nil {
		t.Fatal("candidate filtering failed")
	}
	if s.Set("HK01") == nil {
		t.Fatal("manual pin accepted")
	}
	s.ForceSet("HK01")
	if s.Now() != "REJECT" {
		t.Fatal("manual force pin accepted")
	}
	if conn, err := s.DialContext(context.Background(), &C.Metadata{Host: "example.com", DstPort: 443}); err == nil {
		conn.Close()
		t.Fatal("unverified connection was allowed")
	}
}

func TestSmartProviderSubscriptionAndEmptyCandidates(t *testing.T) {
	raw := config.DefaultRawConfig()
	raw.ProxyProvider = map[string]map[string]any{"subscription": {"type": "http", "url": "https://example.com/subscription"}}
	if err := addSmartFailover(raw); err != nil {
		t.Fatal(err)
	}
	group := raw.ProxyGroup[0]
	if group["include-all"] != true || group["empty-fallback"] != "REJECT" {
		t.Fatal(group)
	}
	raw.ProxyProvider = nil
	cfg, err := config.ParseRawConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	previous := smartGroup
	defer func() { smartGroup = previous }()
	installSmartFailover(cfg)
	if smartGroup.Now() != "REJECT" || len(smartGroup.candidates()) != 0 {
		t.Fatal("empty subscription did not fail closed")
	}
}
