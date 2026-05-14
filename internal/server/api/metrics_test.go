package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestNewMetrics(t *testing.T) {
	m := NewMetrics()
	if len(m.PluginOps) != 6 {
		t.Errorf("expected 6 plugin op counters, got %d", len(m.PluginOps))
	}
}

func TestMetricsIncrPluginOp(t *testing.T) {
	m := NewMetrics()
	m.IncrPluginOp("Login")
	m.IncrPluginOp("Login")
	m.IncrPluginOp("NewProxy")

	if m.PluginOps["Login"].Load() != 2 {
		t.Errorf("Login count = %d, want 2", m.PluginOps["Login"].Load())
	}
	if m.PluginOps["NewProxy"].Load() != 1 {
		t.Errorf("NewProxy count = %d, want 1", m.PluginOps["NewProxy"].Load())
	}
}

func TestMetricsIncrUnknownOp(t *testing.T) {
	m := NewMetrics()
	m.IncrPluginOp("Bogus")
	// Should not panic, unknown ops are silently ignored
}

func TestMetricsRateLimitReject(t *testing.T) {
	m := NewMetrics()
	m.IncrRateLimitReject()
	m.IncrRateLimitReject()
	if m.RateLimitRejects.Load() != 2 {
		t.Errorf("rate limit rejections = %d, want 2", m.RateLimitRejects.Load())
	}
}

func TestMetricsTransition(t *testing.T) {
	m := NewMetrics()
	m.IncrTransition()
	if m.TunnelTransitions.Load() != 1 {
		t.Errorf("transitions = %d, want 1", m.TunnelTransitions.Load())
	}
}

func TestMetricsHandlerFormat(t *testing.T) {
	m := NewMetrics()
	m.IncrPluginOp("Login")
	m.IncrRateLimitReject()

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()

	handler := MetricsHandler(nil, m)
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "hatchway_plugin_ops_total") {
		t.Error("missing plugin ops metric")
	}
	if !strings.Contains(body, `hatchway_plugin_ops_total{op="Login"} 1`) {
		t.Errorf("Login metric missing or wrong in:\n%s", body)
	}
	if !strings.Contains(body, "hatchway_rate_limit_rejections_total 1") {
		t.Errorf("rate limit metric missing or wrong in:\n%s", body)
	}
	if !strings.Contains(body, "hatchway_tunnel_transitions_total 0") {
		t.Errorf("transitions metric missing or wrong in:\n%s", body)
	}
}

func TestMetricsHandlerConcurrent(t *testing.T) {
	m := NewMetrics()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.IncrPluginOp("Login")
			m.IncrRateLimitReject()
		}()
	}
	wg.Wait()

	if m.PluginOps["Login"].Load() != 100 {
		t.Errorf("expected 100, got %d", m.PluginOps["Login"].Load())
	}
	if m.RateLimitRejects.Load() != 100 {
		t.Errorf("expected 100, got %d", m.RateLimitRejects.Load())
	}
}
