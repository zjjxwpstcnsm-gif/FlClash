package failover

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	CheckInterval   = 5 * time.Second
	ProbeTimeout    = 3 * time.Second
	ScanInterval    = 60 * time.Second
	Cooldown        = 30 * time.Second
	Tolerance       = 50 * time.Millisecond
	DefaultMaxDelay = 200 * time.Millisecond
)

var hongKong = regexp.MustCompile(`(?i)香港|香江|港区|港區|🇭🇰|hong[\s_-]*kong|(^|[^a-z])hk([^a-z]|$)`)

func EligibleName(name string) bool {
	return !hongKong.MatchString(name)
}

func NonHongKongTrace(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		if country, ok := strings.CutPrefix(strings.TrimSpace(line), "loc="); ok {
			return len(country) == 2 && country[0] >= 'A' && country[0] <= 'Z' && country[1] >= 'A' && country[1] <= 'Z' && country != "HK" && country != "XX"
		}
	}
	return false
}

type Probe func(context.Context, string) (time.Duration, error)

type Policy struct {
	Current      string
	MaxDelay     time.Duration
	lastScan     time.Time
	lastSwitch   time.Time
	blockedUntil map[string]time.Time
}

func MaxDelay(milliseconds int) time.Duration {
	if milliseconds < 50 || milliseconds > 3000 {
		return DefaultMaxDelay
	}
	return time.Duration(milliseconds) * time.Millisecond
}

func (p *Policy) Step(ctx context.Context, now time.Time, names []string, probe Probe, selectNode func(string, bool)) {
	if p.blockedUntil == nil {
		p.blockedUntil = map[string]time.Time{}
	}
	available := map[string]bool{}
	for _, name := range names {
		if EligibleName(name) {
			available[name] = true
		}
	}
	for name := range p.blockedUntil {
		if !available[name] {
			delete(p.blockedUntil, name)
		}
	}
	measure := func(name string) (time.Duration, error) {
		testCtx, cancel := context.WithTimeout(ctx, ProbeTimeout)
		defer cancel()
		delay, err := probe(testCtx, name)
		limit := p.MaxDelay
		if limit <= 0 {
			limit = DefaultMaxDelay
		}
		if err == nil && delay >= limit {
			return delay, errors.New("latency limit exceeded")
		}
		return delay, err
	}
	results := map[string]time.Duration{}
	if p.Current != "" {
		delay, err := time.Duration(0), context.DeadlineExceeded
		if available[p.Current] {
			delay, err = measure(p.Current)
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			p.blockedUntil[p.Current] = now.Add(Cooldown)
			p.Current = ""
			selectNode("", true)
		} else {
			results[p.Current] = delay
			selectNode(p.Current, false)
			if now.Sub(p.lastScan) < ScanInterval {
				return
			}
		}
	}
	names = nil
	for name := range available {
		if name != p.Current && !now.Before(p.blockedUntil[name]) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var mu sync.Mutex
	var wg sync.WaitGroup
	jobs := make(chan string)
	for i := 0; i < min(10, len(names)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for name := range jobs {
				if ctx.Err() != nil {
					continue
				}
				delay, err := measure(name)
				if err == nil {
					mu.Lock()
					results[name] = delay
					mu.Unlock()
				}
			}
		}()
	}
	for _, name := range names {
		select {
		case jobs <- name:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return
		}
	}
	close(jobs)
	wg.Wait()
	if ctx.Err() != nil {
		return
	}
	p.lastScan = now
	best := p.Current
	for _, name := range names {
		if delay, ok := results[name]; ok && (best == "" || delay < results[best]) {
			best = name
		}
	}
	if best == "" || best == p.Current {
		return
	}
	if p.Current != "" && (now.Sub(p.lastSwitch) < ScanInterval || results[p.Current]-results[best] < Tolerance) {
		return
	}
	p.Current = best
	p.lastSwitch = now
	selectNode(best, false)
}
