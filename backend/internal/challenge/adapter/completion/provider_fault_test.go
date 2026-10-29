package completion

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProviderFailureBoundaries(t *testing.T) {
	const private = "synthetic-private-marker"
	for _, scenario := range []struct {
		name, outcome string
		code          int
		body          string
	}{
		{"unauthorized", "failed", 401, private},
		{"rate_limited", "failed", 429, private},
		{"server_error", "failed", 500, private},
		{"unavailable", "failed", 503, private},
		{"redirect", "failed", 307, private},
		{"empty", "unknown", 200, ""},
		{"malformed", "unknown", 200, "{" + private},
		{"truncated", "unknown", 200, "{"},
		{"oversized", "unknown", 200, strings.Repeat(" ", (2<<20)+1)},
		{"no_choice", "failed", 200, `{"choices":[]}`},
		{"incomplete", "failed", 200, `{"choices":[{"finish_reason":"length","message":{"content":"partial"}}]}`},
		{"success", "succeeded", 200, `{"choices":[{"finish_reason":"stop","message":{"content":"OK"}}]}`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var calls, redirects atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				redirects.Add(1)
				w.WriteHeader(200)
			}))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+private {
					t.Error("incorrect provider request boundary")
				}
				if scenario.name == "redirect" {
					w.Header().Set("Location", target.URL)
				}
				if scenario.name == "truncated" {
					w.Header().Set("Content-Length", "100")
				}
				w.WriteHeader(scenario.code)
				_, _ = io.WriteString(w, scenario.body)
			}))
			defer server.Close()
			provider, err := NewProvider(ProviderConfig{BaseURL: server.URL, Protocol: "completion", Model: "fixture", APIKey: private}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			response, outcome, err := provider.Complete(t.Context(), []byte(`{"model":"fixture","messages":[{"role":"user","content":"synthetic"}]}`))
			if outcome != scenario.outcome || (err == nil) != (outcome == "succeeded") {
				t.Fatalf("outcome=%s error=%v, want %s", outcome, err, scenario.outcome)
			}
			if calls.Load() != 1 || redirects.Load() != 0 {
				t.Fatal("provider retried or forwarded credentials to redirect")
			}
			if err != nil && strings.Contains(err.Error(), private) {
				t.Fatal("provider error leaked private upstream content")
			}
			if outcome == "succeeded" && (response == nil || response.Choices[0].Message.Content != "OK") {
				t.Fatal("successful control response lost")
			}
		})
	}
}

func TestProviderInterruptedRequest(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprintf("client_timeout=%t", timeout), func(t *testing.T) {
			var calls atomic.Int32
			entered := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				close(entered)
				<-r.Context().Done()
			}))
			defer server.Close()
			client := server.Client()
			if timeout {
				client.Timeout = 100 * time.Millisecond
			}
			provider, err := NewProvider(ProviderConfig{BaseURL: server.URL, Protocol: "completion", Model: "fixture", APIKey: "fixture-only"}, client)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if !timeout {
				go func() {
					select {
					case <-entered:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			_, outcome, err := provider.Complete(ctx, []byte(`{"model":"fixture"}`))
			if outcome != "unknown" || err == nil || calls.Load() != 1 {
				t.Fatalf("interrupted request: outcome=%s calls=%d error=%v", outcome, calls.Load(), err)
			}
		})
	}
}
