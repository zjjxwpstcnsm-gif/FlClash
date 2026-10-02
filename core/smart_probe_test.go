package main

import (
	"context"
	"core/failover"
	"crypto/x509"
	"fmt"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metacubex/http"
	"github.com/metacubex/tls"
)

func TestSmartProbeExcludesColdDialAndTLSStartup(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		calls.Add(1)
		w.Header().Set("Connection", "close")
		fmt.Fprint(w, "h=chatgpt.com\nloc=JP\n")
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		timer := time.NewTimer(300 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), failover.ProbeTimeout)
	defer cancel()
	start := time.Now()
	delay, err := probeSmartHTTP(ctx, &http.Client{Transport: transport}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || time.Since(start) < 600*time.Millisecond {
		t.Fatal("test did not exercise two slow connection handshakes")
	}
	if delay >= failover.DefaultMaxDelay {
		t.Fatalf("cold connection overhead counted as service latency: %s", delay)
	}
}

func TestSmartProbeStillMeasuresSlowServiceResponse(t *testing.T) {
	server := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		time.Sleep(220 * time.Millisecond)
		fmt.Fprint(w, "loc=JP\n")
	}))
	defer server.Close()
	client := &http.Client{}
	defer client.CloseIdleConnections()
	delay, err := probeSmartHTTP(context.Background(), client, server.URL)
	if err != nil || delay < failover.DefaultMaxDelay {
		t.Fatalf("slow service was accepted: delay=%s error=%v", delay, err)
	}
}

func TestSmartProbeRejectsRefusedOrExcludedService(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"access refusal", 403, "<html>Forbidden</html>", "service_refused:403"},
		{"Hong Kong exit", 200, "loc=HK\n", "exit_region"},
		{"unknown exit", 200, "loc=XX\n", "exit_region"},
		{"invalid trace", 200, "<html>blocked</html>", "exit_region"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			server := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
				w.WriteHeader(scenario.status)
				fmt.Fprint(w, scenario.body)
			}))
			defer server.Close()
			client := &http.Client{}
			defer client.CloseIdleConnections()
			_, err := probeSmartHTTP(context.Background(), client, server.URL)
			if err == nil || !strings.Contains(err.Error(), scenario.want) {
				t.Fatalf("reason=%v want=%s", err, scenario.want)
			}
		})
	}
}

func TestSmartProbeHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := probeSmartHTTP(ctx, &http.Client{}, "https://chatgpt.com/cdn-cgi/trace"); err != context.Canceled {
		t.Fatalf("canceled probe returned %v", err)
	}
}
