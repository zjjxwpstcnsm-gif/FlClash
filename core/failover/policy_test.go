package failover

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestHongKongNamesAndExitTrace(t *testing.T) {
	for _, name := range []string{"香港 01", "🇭🇰 HKT", "Hong Kong 2", "Hong-Kong", "HK01", "Premium hk-2", "港區", "香江专线"} {
		if EligibleName(name) {
			t.Errorf("accepted %q", name)
		}
	}
	for _, name := range []string{"Japan 1", "Singapore", "US 02", "Tokyo", "BookHKeeper"} {
		if !EligibleName(name) {
			t.Errorf("excluded %q", name)
		}
	}
	for _, trace := range []string{"loc=HK\n", "loc=XX\n", "loc=hk", "", "<html>blocked</html>", "loc=123"} {
		if NonHongKongTrace(trace) {
			t.Errorf("accepted trace %q", trace)
		}
	}
	if !NonHongKongTrace("h=chatgpt.com\nip=192.0.2.1\nloc=JP\n") {
		t.Fatal("rejected valid Japanese exit")
	}
}

func TestFailureSelectsFastestHealthyNonHKAndRecovers(t *testing.T) {
	p := Policy{MaxDelay: time.Second}
	now := time.Unix(100, 0)
	delays := map[string]time.Duration{"HK01": time.Millisecond, "Japan": 100 * time.Millisecond, "Singapore": 200 * time.Millisecond, "US": 400 * time.Millisecond}
	probe := func(_ context.Context, name string) (time.Duration, error) {
		if d, ok := delays[name]; ok {
			return d, nil
		}
		return 0, errors.New("timeout")
	}
	var transitions []string
	selected := ""
	selectNode := func(name string, _ bool) {
		if name != selected {
			transitions = append(transitions, name)
			selected = name
		}
	}
	names := []string{"HK01", "Japan", "Singapore", "US"}
	p.Step(context.Background(), now, names, probe, selectNode)
	delete(delays, "Japan")
	p.Step(context.Background(), now.Add(5*time.Second), names, probe, selectNode)
	if p.Current != "Singapore" {
		t.Fatalf("selected %q", p.Current)
	}
	delays["Japan"] = time.Millisecond
	p.Step(context.Background(), now.Add(10*time.Second), names, probe, selectNode)
	if p.Current != "Singapore" {
		t.Fatal("flapping node was selected too soon")
	}
	delete(delays, "Singapore")
	delete(delays, "US")
	delete(delays, "Japan")
	p.Step(context.Background(), now.Add(15*time.Second), names, probe, selectNode)
	if p.Current != "" {
		t.Fatal("did not fail closed")
	}
	delays["US"] = 250 * time.Millisecond
	p.Step(context.Background(), now.Add(20*time.Second), names, probe, selectNode)
	if !reflect.DeepEqual(transitions, []string{"Japan", "", "Singapore", "", "US"}) {
		t.Fatalf("transitions %v", transitions)
	}
}

func TestLatencyHysteresisAndProviderReplacement(t *testing.T) {
	p := Policy{MaxDelay: time.Second}
	now := time.Unix(100, 0)
	delays := map[string]time.Duration{"JP": 100 * time.Millisecond, "SG": 150 * time.Millisecond}
	probe := func(_ context.Context, name string) (time.Duration, error) { return delays[name], nil }
	selectNode := func(string, bool) {}
	p.Step(context.Background(), now, []string{"JP", "SG"}, probe, selectNode)
	delays["SG"] = 80 * time.Millisecond
	p.Step(context.Background(), now.Add(61*time.Second), []string{"JP", "SG"}, probe, selectNode)
	if p.Current != "JP" {
		t.Fatal("small latency noise caused a switch")
	}
	delays["SG"] = 40 * time.Millisecond
	p.Step(context.Background(), now.Add(122*time.Second), []string{"JP", "SG"}, probe, selectNode)
	if p.Current != "SG" {
		t.Fatal("did not select significantly faster node")
	}
	p.Step(context.Background(), now.Add(127*time.Second), []string{"JP"}, probe, selectNode)
	if p.Current != "JP" {
		t.Fatal("kept a node removed by subscription refresh")
	}
}

func TestCancellationCannotPublishAStaleSelection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := Policy{MaxDelay: time.Second}
	probe := func(ctx context.Context, _ string) (time.Duration, error) { cancel(); return time.Millisecond, nil }
	p.Step(ctx, time.Now(), []string{"JP"}, probe, func(string, bool) { t.Error("published after cancellation") })
}

func TestProbeTimeoutAndEmptySubscription(t *testing.T) {
	p := Policy{MaxDelay: time.Second}
	probe := func(ctx context.Context, _ string) (time.Duration, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > ProbeTimeout {
			t.Error("missing bounded probe deadline")
		}
		return 0, context.DeadlineExceeded
	}
	p.Step(context.Background(), time.Now(), []string{"JP"}, probe, func(string, bool) { t.Error("selected timed-out node") })
	p.Step(context.Background(), time.Now(), nil, probe, func(string, bool) { t.Error("selected empty subscription") })
}

func TestLatencyThresholdIsExclusiveAndDefaultsTo200ms(t *testing.T) {
	for _, delay := range []time.Duration{199 * time.Millisecond, 200 * time.Millisecond, 201 * time.Millisecond} {
		p := Policy{}
		p.Step(context.Background(), time.Now(), []string{"JP"}, func(context.Context, string) (time.Duration, error) {
			return delay, nil
		}, func(string, bool) {})
		if (p.Current == "JP") != (delay < DefaultMaxDelay) {
			t.Fatalf("delay %s selected %q", delay, p.Current)
		}
	}
}

func TestSlowCurrentNodeFailsOverAndThresholdCanBeChanged(t *testing.T) {
	p := Policy{MaxDelay: MaxDelay(200)}
	now := time.Unix(100, 0)
	delays := map[string]time.Duration{"JP": 100 * time.Millisecond, "SG": 150 * time.Millisecond}
	probe := func(_ context.Context, name string) (time.Duration, error) { return delays[name], nil }
	selectNode := func(string, bool) {}
	p.Step(context.Background(), now, []string{"JP", "SG"}, probe, selectNode)
	delays["JP"] = 200 * time.Millisecond
	p.Step(context.Background(), now.Add(CheckInterval), []string{"JP", "SG"}, probe, selectNode)
	if p.Current != "SG" {
		t.Fatalf("did not replace slow current node: %q", p.Current)
	}
	p.MaxDelay = 100 * time.Millisecond
	p.Step(context.Background(), now.Add(2*CheckInterval), []string{"JP", "SG"}, probe, selectNode)
	if p.Current != "" {
		t.Fatal("selected over-threshold node")
	}
	p = Policy{MaxDelay: MaxDelay(400)}
	p.Step(context.Background(), now.Add(3*CheckInterval), []string{"JP", "SG"}, probe, selectNode)
	if p.Current != "SG" {
		t.Fatal("relaxed threshold was not applied")
	}
}

func TestLatencyLimitValidation(t *testing.T) {
	for _, value := range []int{-1, 0, 49, 3001, 1000000000} {
		if MaxDelay(value) != DefaultMaxDelay {
			t.Fatalf("invalid threshold %d", value)
		}
	}
	for _, value := range []int{50, 200, 3000} {
		if MaxDelay(value) != time.Duration(value)*time.Millisecond {
			t.Fatalf("valid threshold %d", value)
		}
	}
}
