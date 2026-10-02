package failover

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	CheckInterval   = 5 * time.Second
	ProbeTimeout    = 5 * time.Second
	ScanTimeout     = 2 * ProbeTimeout
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
	scanOffset   int
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
	measure := func(parent context.Context, name string) (time.Duration, error) {
		testCtx, cancel := context.WithTimeout(parent, ProbeTimeout)
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
			delay, err = measure(ctx, p.Current)
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
	recovering := p.Current == ""
	var cooling []string
	names = nil
	for name := range available {
		if name == p.Current {
			continue
		}
		if now.Before(p.blockedUntil[name]) {
			if p.blockedUntil[name] != now.Add(Cooldown) {
				cooling = append(cooling, name)
			}
		} else {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) > 0 {
		offset := p.scanOffset % len(names)
		names = append(names[offset:], names[:offset]...)
	}
	scan := func(candidates []string) {
		type result struct {
			name  string
			delay time.Duration
			err   error
		}
		scanCtx, cancel := context.WithTimeout(ctx, ScanTimeout)
		defer cancel()
		var wg sync.WaitGroup
		var dispatched atomic.Int32
		jobs := make(chan string)
		completed := make(chan result, len(candidates))
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(jobs)
			for _, name := range candidates {
				select {
				case <-scanCtx.Done():
					return
				case jobs <- name:
					dispatched.Add(1)
				}
			}
		}()
		for i := 0; i < min(10, len(candidates)); i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for name := range jobs {
					if scanCtx.Err() != nil {
						return
					}
					delay, err := measure(scanCtx, name)
					completed <- result{name: name, delay: delay, err: err}
				}
			}()
		}
		go func() {
			wg.Wait()
			close(completed)
		}()
		for r := range completed {
			if ctx.Err() != nil || r.err != nil {
				continue
			}
			results[r.name] = r.delay
			if p.Current == "" {
				p.Current = r.name
				p.lastSwitch = now
				delete(p.blockedUntil, r.name)
				selectNode(r.name, false)
			}
		}
		p.scanOffset += int(dispatched.Load())
	}
	scan(names)
	if p.Current == "" && ctx.Err() == nil {
		sort.Strings(cooling)
		scan(cooling)
		names = append(names, cooling...)
	}
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
	if !recovering && p.Current != "" && (now.Sub(p.lastSwitch) < ScanInterval || results[p.Current]-results[best] < Tolerance) {
		return
	}
	p.Current = best
	p.lastSwitch = now
	selectNode(best, false)
}
