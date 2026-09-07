package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hatchwayai/hatchway/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

// stubTransition records calls and returns a configurable error.
type stubTransition struct {
	calls []transitionCall
	err   error
}

type transitionCall struct {
	tunnelID string
	event    string
}

func (s *stubTransition) fn(ctx context.Context, pool *pgxpool.Pool, tunnelID string, event string) error {
	s.calls = append(s.calls, transitionCall{tunnelID: tunnelID, event: event})
	return s.err
}

func newConfig(secret string) *config.Config {
	return &config.Config{
		PluginSecret: secret,
	}
}

// pluginMux wraps the handler in a ServeMux so r.PathValue("secret") is populated.
func pluginMux(cfg *config.Config, tf TransitionFunc) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/frp/plugin/{secret}", Handler(nil, cfg, tf, nil, nil))
	return mux
}

func postPlugin(cfg *config.Config, tf TransitionFunc, op string, body any) *PluginResponse {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/frp/plugin/"+cfg.PluginSecret+"?op="+op, bytes.NewReader(b))
	w := httptest.NewRecorder()

	pluginMux(cfg, tf).ServeHTTP(w, req)

	var resp PluginResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	return &resp
}

// --- Tests ---

func TestPluginRejectsMissingSecret(t *testing.T) {
	cfg := newConfig("s3cret")
	b, _ := json.Marshal(map[string]any{})
	req := httptest.NewRequest("POST", "/frp/plugin?op=Login", bytes.NewReader(b))
	// no secret in path — mux won't match
	w := httptest.NewRecorder()

	pluginMux(cfg, nil).ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing secret, got %d", w.Code)
	}
}

func TestPluginRejectsWrongSecret(t *testing.T) {
	cfg := newConfig("s3cret")
	b, _ := json.Marshal(map[string]any{})
	req := httptest.NewRequest("POST", "/frp/plugin/wrong?op=Login", bytes.NewReader(b))
	w := httptest.NewRecorder()

	pluginMux(cfg, nil).ServeHTTP(w, req)

	var resp PluginResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if !resp.Reject {
		t.Error("expected reject for wrong secret")
	}
}

func TestPluginRejectsNoSecretConfigured(t *testing.T) {
	cfg := newConfig("")
	b, _ := json.Marshal(map[string]any{})
	// empty secret → URL is "/frp/plugin/" which doesn't match mux pattern
	req := httptest.NewRequest("POST", "/frp/plugin/?op=Login", bytes.NewReader(b))
	w := httptest.NewRecorder()

	pluginMux(cfg, nil).ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 when no secret configured, got %d", w.Code)
	}
}

func TestPluginRejectsMissingOp(t *testing.T) {
	cfg := newConfig("s3cret")
	b, _ := json.Marshal(map[string]any{})
	req := httptest.NewRequest("POST", "/frp/plugin/"+cfg.PluginSecret, bytes.NewReader(b))
	w := httptest.NewRecorder()

	pluginMux(cfg, nil).ServeHTTP(w, req)

	var resp PluginResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if !resp.Reject {
		t.Error("expected reject for missing op")
	}
}

func TestPluginRejectsUnknownOp(t *testing.T) {
	cfg := newConfig("s3cret")
	resp := postPlugin(cfg, nil, "Bogus", map[string]any{})
	if !resp.Reject {
		t.Error("expected reject for unknown op")
	}
}

func TestPluginRejectsInvalidBody(t *testing.T) {
	cfg := newConfig("s3cret")
	req := httptest.NewRequest("POST", "/frp/plugin/s3cret?op=Login", bytes.NewReader([]byte("not json")))
	w := httptest.NewRecorder()

	pluginMux(cfg, nil).ServeHTTP(w, req)

	var resp PluginResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if !resp.Reject {
		t.Error("expected reject for invalid body")
	}
}

func TestPingAllowedUnchange(t *testing.T) {
	cfg := newConfig("s3cret")
	resp := postPlugin(cfg, nil, "Ping", map[string]any{})
	if resp.Reject {
		t.Error("ping should not reject")
	}
	if !resp.Unchange {
		t.Error("ping should return unchange")
	}
}

func TestNewWorkConnAllowedUnchange(t *testing.T) {
	cfg := newConfig("s3cret")
	resp := postPlugin(cfg, nil, "NewWorkConn", map[string]any{})
	if resp.Reject {
		t.Error("NewWorkConn should not reject")
	}
}

// Login tests

func TestLoginRejectsMissingRuntimeToken(t *testing.T) {
	cfg := newConfig("s3cret")
	login := LoginContent{
		Metas: map[string]string{},
	}
	resp := postPlugin(cfg, nil, "Login", map[string]any{
		"content": login,
	})
	if !resp.Reject {
		t.Error("expected reject for missing runtime token")
	}
}

func TestLoginRejectsNilMetas(t *testing.T) {
	cfg := newConfig("s3cret")
	resp := postPlugin(cfg, nil, "Login", map[string]any{
		"content": map[string]any{
			"metas": nil,
		},
	})
	if !resp.Reject {
		t.Error("expected reject for nil metas")
	}
}

// NewProxy tests

func TestNewProxyRejectsMissingRuntimeToken(t *testing.T) {
	cfg := newConfig("s3cret")
	st := &stubTransition{}
	resp := postPlugin(cfg, st.fn, "NewProxy", map[string]any{
		"content": map[string]any{
			"user": map[string]any{
				"metas": map[string]string{},
			},
			"proxy_name": "t-abc",
			"proxy_type": "http",
			"subdomain":  "t-abc",
		},
	})
	if !resp.Reject {
		t.Error("expected reject for missing runtime token")
	}
}

func TestNewProxyRejectsInvalidContent(t *testing.T) {
	cfg := newConfig("s3cret")
	st := &stubTransition{}
	resp := postPlugin(cfg, st.fn, "NewProxy", map[string]any{
		"content": "not an object",
	})
	if !resp.Reject {
		t.Error("expected reject for invalid content")
	}
}

// CloseProxy tests

func TestCloseProxyRejectsInvalidContent(t *testing.T) {
	cfg := newConfig("s3cret")
	st := &stubTransition{}
	resp := postPlugin(cfg, st.fn, "CloseProxy", map[string]any{
		"content": 123,
	})
	if !resp.Reject {
		t.Error("expected reject for invalid closeproxy content")
	}
}

// NewUserConn tests

func TestNewUserConnFailsClosedWithoutStorage(t *testing.T) {
	cfg := newConfig("s3cret")
	cfg.LogUserConns = false
	resp := postPlugin(cfg, nil, "NewUserConn", map[string]any{
		"content": map[string]any{
			"proxy_name":  "t-abc",
			"proxy_type":  "tcp",
			"remote_addr": "1.2.3.4:5678",
		},
	})
	if !resp.Reject {
		t.Error("NewUserConn should reject when tunnel state cannot be checked")
	}
}

// Response shape tests

func TestRejectResponseShape(t *testing.T) {
	r := reject("bad thing")
	if !r.Reject {
		t.Error("expected Reject=true")
	}
	if r.RejectReason != "bad thing" {
		t.Errorf("expected reason 'bad thing', got %q", r.RejectReason)
	}
	if r.Unchange {
		t.Error("expected Unchange=false")
	}
}

func TestAllowUnchangeResponseShape(t *testing.T) {
	r := allowUnchange()
	if r.Reject {
		t.Error("expected Reject=false")
	}
	if !r.Unchange {
		t.Error("expected Unchange=true")
	}
}

// Type deserialization tests

func TestLoginContentDecode(t *testing.T) {
	raw := `{"version":"0.58.0","metas":{"runtime_token":"rt_test123"},"user":"test","timestamp":12345}`
	var login LoginContent
	if err := json.Unmarshal([]byte(raw), &login); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if login.Metas["runtime_token"] != "rt_test123" {
		t.Errorf("expected runtime_token, got %v", login.Metas)
	}
}

func TestNewProxyContentDecode(t *testing.T) {
	raw := `{
		"user": {"user":"test","metas":{"runtime_token":"rt_abc"},"run_id":"r1"},
		"proxy_name": "t-abc",
		"proxy_type": "http",
		"subdomain": "t-abc"
	}`
	var np NewProxyContent
	if err := json.Unmarshal([]byte(raw), &np); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if np.ProxyName != "t-abc" {
		t.Errorf("expected proxy_name t-abc, got %q", np.ProxyName)
	}
	if np.User.Metas["runtime_token"] != "rt_abc" {
		t.Errorf("expected runtime_token in user.metas, got %v", np.User.Metas)
	}
	if np.Subdomain != "t-abc" {
		t.Errorf("expected subdomain t-abc, got %q", np.Subdomain)
	}
}

func TestCloseProxyContentDecode(t *testing.T) {
	raw := `{
		"user": {"user":"test","run_id":"r1"},
		"proxy_name": "t-abc"
	}`
	var cp CloseProxyContent
	if err := json.Unmarshal([]byte(raw), &cp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cp.ProxyName != "t-abc" {
		t.Errorf("expected proxy_name t-abc, got %q", cp.ProxyName)
	}
}

func TestNewUserConnContentDecode(t *testing.T) {
	raw := `{
		"user": {"user":"test","run_id":"r1"},
		"proxy_name": "t-abc",
		"proxy_type": "tcp",
		"remote_addr": "1.2.3.4:5678"
	}`
	var uc NewUserConnContent
	if err := json.Unmarshal([]byte(raw), &uc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if uc.ProxyName != "t-abc" {
		t.Errorf("expected proxy_name t-abc, got %q", uc.ProxyName)
	}
	if uc.RemoteAddr != "1.2.3.4:5678" {
		t.Errorf("expected remote_addr, got %q", uc.RemoteAddr)
	}
}

// Full round-trip test for the frp protocol request shape

func TestFullProtocolRequestRoundTrip(t *testing.T) {
	cfg := newConfig("s3cret")

	loginBody := PluginRequest{
		Version: "0.1.0",
		Op:      "Login",
		Content: json.RawMessage(`{"version":"0.58.0","metas":{"runtime_token":"rt_test"}}`),
	}
	b, _ := json.Marshal(loginBody)

	req := httptest.NewRequest("POST", "/frp/plugin/s3cret?version=0.1.0&op=Login", bytes.NewReader(b))
	w := httptest.NewRecorder()

	pluginMux(cfg, nil).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp PluginResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	// login will reject because no DB, but should get a well-formed response
	if !resp.Reject {
		t.Error("expected reject (no DB pool)")
	}
}

// Stub transition tests

func TestStubTransitionRecordsCalls(t *testing.T) {
	st := &stubTransition{}
	fn := st.fn
	_ = fn(context.Background(), nil, "t-abc", "NewProxy")
	_ = fn(context.Background(), nil, "t-xyz", "CloseProxy")

	if len(st.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(st.calls))
	}
	if st.calls[0].tunnelID != "t-abc" || st.calls[0].event != "NewProxy" {
		t.Errorf("call 0: %+v", st.calls[0])
	}
	if st.calls[1].tunnelID != "t-xyz" || st.calls[1].event != "CloseProxy" {
		t.Errorf("call 1: %+v", st.calls[1])
	}
}
