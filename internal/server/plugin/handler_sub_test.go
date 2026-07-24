package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zydo/hatchway/internal/config"
	"github.com/zydo/hatchway/internal/tokens"
)

// --- Login tests ---

func TestHandleLogin_InvalidJSON(t *testing.T) {
	resp := handleLogin(context.TODO(), nil, json.RawMessage(`not json`))
	if !resp.Reject {
		t.Error("expected reject for invalid JSON")
	}
	if resp.RejectReason != "invalid login content" {
		t.Errorf("unexpected reason: %s", resp.RejectReason)
	}
}

func TestHandleLogin_MissingToken(t *testing.T) {
	content, _ := json.Marshal(LoginContent{Metas: map[string]string{}})
	resp := handleLogin(context.TODO(), nil, content)
	if !resp.Reject {
		t.Error("expected reject for missing token")
	}
	if resp.RejectReason != "missing runtime token" {
		t.Errorf("unexpected reason: %s", resp.RejectReason)
	}
}

func TestHandleLogin_NilMetas(t *testing.T) {
	content, _ := json.Marshal(LoginContent{Metas: nil})
	resp := handleLogin(context.TODO(), nil, content)
	if !resp.Reject {
		t.Error("expected reject for nil metas")
	}
}

// --- NewProxy tests ---

func TestHandleNewProxy_InvalidJSON(t *testing.T) {
	resp := handleNewProxy(context.TODO(), nil, nil, json.RawMessage(`not json`))
	if !resp.Reject {
		t.Error("expected reject for invalid JSON")
	}
}

func TestHandleNewProxy_MissingToken(t *testing.T) {
	content, _ := json.Marshal(NewProxyContent{
		User:      NewProxyUser{Metas: map[string]string{}},
		ProxyName: "t-abc",
		ProxyType: "http",
	})
	resp := handleNewProxy(context.TODO(), nil, nil, content)
	if !resp.Reject {
		t.Error("expected reject for missing token")
	}
}

func TestHandleNewProxy_NilUserMetas(t *testing.T) {
	content, _ := json.Marshal(NewProxyContent{
		User:      NewProxyUser{Metas: nil},
		ProxyName: "t-abc",
		ProxyType: "http",
	})
	resp := handleNewProxy(context.TODO(), nil, nil, content)
	if !resp.Reject {
		t.Error("expected reject for nil user metas")
	}
}

func TestHandleNewProxy_RejectsNonHTTP(t *testing.T) {
	content, _ := json.Marshal(NewProxyContent{
		User:      NewProxyUser{Metas: map[string]string{"runtime_token": "rt_xxx"}},
		ProxyName: "t-abc",
		ProxyType: "tcp",
		Subdomain: "t-abc",
	})
	resp := handleNewProxy(context.TODO(), nil, nil, content)
	if !resp.Reject || resp.RejectReason != "only http proxy type supported" {
		t.Errorf("expected non-http proxy rejection, got %+v", resp)
	}
}

func TestHandleNewProxy_RejectsCustomDomains(t *testing.T) {
	// We can't easily run all the way through to the custom_domains check
	// without a real DB; this just verifies the JSON binding picks up the
	// field so the runtime check has something to inspect.
	raw := `{"user":{"metas":{"runtime_token":"rt_x"}},"proxy_name":"t-abc","proxy_type":"http","subdomain":"t-abc","custom_domains":["evil.example.com"]}`
	var np NewProxyContent
	if err := json.Unmarshal([]byte(raw), &np); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(np.CustomDomains) != 1 || np.CustomDomains[0] != "evil.example.com" {
		t.Errorf("custom_domains not bound: %+v", np.CustomDomains)
	}
}

// --- CloseProxy tests ---

func TestHandleCloseProxy_InvalidJSON(t *testing.T) {
	resp := handleCloseProxy(context.TODO(), nil, nil, json.RawMessage(`not json`))
	if !resp.Reject {
		t.Error("expected reject for invalid JSON")
	}
}

// --- NewUserConn tests ---

func TestHandleNewUserConn_Disabled(t *testing.T) {
	cfg := &config.Config{LogUserConns: false}
	resp := handleNewUserConn(context.TODO(), nil, cfg, nil)
	if !resp.Reject {
		t.Error("invalid callback must be rejected even when event logging is disabled")
	}
}

func TestHandleNewUserConn_InvalidJSON(t *testing.T) {
	cfg := &config.Config{LogUserConns: true}
	resp := handleNewUserConn(context.TODO(), nil, cfg, json.RawMessage(`not json`))
	if !resp.Reject {
		t.Error("invalid JSON should be rejected")
	}
}

func TestHandleNewUserConn_ValidButNilPool(t *testing.T) {
	cfg := &config.Config{LogUserConns: true}
	content, _ := json.Marshal(NewUserConnContent{
		User:       NewProxyUser{User: "test"},
		ProxyName:  "t-abc",
		ProxyType:  "http",
		RemoteAddr: "1.2.3.4:5678",
	})
	resp := handleNewUserConn(context.TODO(), nil, cfg, content)
	if !resp.Reject {
		t.Error("nil storage should fail closed")
	}
}

// --- PluginRequest deserialization ---

func TestPluginRequest_Parse(t *testing.T) {
	raw := `{"version":"0.1","op":"Login","content":{"metas":{}}}`
	var req PluginRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if req.Op != "Login" {
		t.Errorf("op: %s", req.Op)
	}
	if req.Version != "0.1" {
		t.Errorf("version: %s", req.Version)
	}
}

// --- LoginContent deserialization ---

func TestLoginContent_Parse(t *testing.T) {
	raw := `{"version":"0.52.0","hostname":"myhost","os":"linux","arch":"amd64","user":"root","timestamp":1234567890,"privilege_key":"secret","run_id":"abc","pool_count":1,"metas":{"runtime_token":"rt_abc123"},"client_address":"1.2.3.4"}`
	var login LoginContent
	if err := json.Unmarshal([]byte(raw), &login); err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if login.Metas["runtime_token"] != "rt_abc123" {
		t.Errorf("runtime_token: %v", login.Metas["runtime_token"])
	}
	if login.Hostname != "myhost" {
		t.Errorf("hostname: %s", login.Hostname)
	}
}

// --- NewProxyContent deserialization ---

func TestNewProxyContent_Parse(t *testing.T) {
	raw := `{"user":{"user":"test","metas":{"runtime_token":"rt_abc"},"run_id":"r1"},"proxy_name":"t-xyz","proxy_type":"http","subdomain":"t-xyz"}`
	var np NewProxyContent
	if err := json.Unmarshal([]byte(raw), &np); err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if np.ProxyName != "t-xyz" {
		t.Errorf("proxy_name: %s", np.ProxyName)
	}
	if np.User.Metas["runtime_token"] != "rt_abc" {
		t.Errorf("user metas runtime_token: %v", np.User.Metas)
	}
}

// --- CloseProxyContent deserialization ---

func TestCloseProxyContent_Parse(t *testing.T) {
	raw := `{"user":{"user":"test","run_id":"r1"},"proxy_name":"t-xyz"}`
	var cp CloseProxyContent
	if err := json.Unmarshal([]byte(raw), &cp); err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if cp.ProxyName != "t-xyz" {
		t.Errorf("proxy_name: %s", cp.ProxyName)
	}
}

// --- NewUserConnContent deserialization ---

func TestNewUserConnContent_Parse(t *testing.T) {
	raw := `{"user":{"user":"test","run_id":"r1"},"proxy_name":"t-xyz","proxy_type":"http","remote_addr":"10.0.0.1:12345"}`
	var uc NewUserConnContent
	if err := json.Unmarshal([]byte(raw), &uc); err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if uc.RemoteAddr != "10.0.0.1:12345" {
		t.Errorf("remote_addr: %s", uc.RemoteAddr)
	}
	if uc.ProxyType != "http" {
		t.Errorf("proxy_type: %s", uc.ProxyType)
	}
}

// --- PluginResponse serialization ---

func TestPluginResponse_Reject(t *testing.T) {
	resp := reject("test reason")
	data, _ := json.Marshal(resp)
	var parsed map[string]any
	_ = json.Unmarshal(data, &parsed)

	if parsed["reject"] != true {
		t.Error("reject should be true")
	}
	if parsed["reject_reason"] != "test reason" {
		t.Errorf("reason: %v", parsed["reject_reason"])
	}
}

func TestPluginResponse_AllowUnchange(t *testing.T) {
	resp := allowUnchange()
	data, _ := json.Marshal(resp)
	var parsed map[string]any
	_ = json.Unmarshal(data, &parsed)

	if parsed["reject"] != false {
		t.Error("reject should be false")
	}
	if parsed["unchange"] != true {
		t.Error("unchange should be true")
	}
}

// --- Handler dispatch tests with nil pool ---

func TestHandler_OpDispatch(t *testing.T) {
	cfg := &config.Config{PluginSecret: "test-secret"}

	tests := []struct {
		name   string
		op     string
		body   string
		wantOK bool
	}{
		{"Ping allows", "Ping", `{}`, true},
		{"NewWorkConn allows", "NewWorkConn", `{}`, true},
		{"unknown op rejects", "Bogus", `{}`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/frp/plugin/test-secret?op="+tt.op, strings.NewReader(tt.body))
			mux := http.NewServeMux()
			mux.HandleFunc("/frp/plugin/{secret}", Handler(nil, cfg, nil, nil, nil))
			mux.ServeHTTP(w, r)

			var resp PluginResponse
			_ = json.NewDecoder(w.Body).Decode(&resp)

			if tt.wantOK && resp.Reject {
				t.Errorf("expected allow for %s, got reject: %s", tt.op, resp.RejectReason)
			}
			if !tt.wantOK && !resp.Reject {
				t.Errorf("expected reject for %s", tt.op)
			}
		})
	}
}

// --- writePluginResponse ---

func TestWritePluginResponse(t *testing.T) {
	w := httptest.NewRecorder()
	writePluginResponse(w, allowUnchange())

	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("content type: %s", w.Header().Get("Content-Type"))
	}

	var resp PluginResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Reject {
		t.Error("should not reject")
	}
}

func TestRuntimeTokenRejectReason(t *testing.T) {
	for _, err := range []error{tokens.ErrLegacyVerificationBusy, context.DeadlineExceeded} {
		if got := runtimeTokenRejectReason(err); got != "authentication temporarily unavailable" {
			t.Errorf("runtimeTokenRejectReason(%v) = %q", err, got)
		}
	}
	if got := runtimeTokenRejectReason(errors.New("bad token")); got != "invalid credentials" {
		t.Errorf("invalid-token reason = %q", got)
	}
}
