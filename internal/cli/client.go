package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Client is a typed HTTP client for the Hatchway API.
type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

// NewClient creates a Client from resolved credentials.
func NewClient(cred *Credentials) *Client {
	return &Client{
		BaseURL: cred.Server,
		Token:   cred.Token,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// CreateTunnelRequest is the payload accepted by POST /v1/tunnels.
type CreateTunnelRequest struct {
	Type       string `json:"type"`
	LocalHost  string `json:"local_host"`
	LocalPort  int    `json:"local_port"`
	TTLSeconds int    `json:"ttl_seconds"`
}

// TunnelResponse is the public tunnel representation returned by the API.
type TunnelResponse struct {
	TunnelID     string     `json:"tunnel_id"`
	Status       string     `json:"status"`
	Type         string     `json:"type"`
	PublicURL    string     `json:"public_url"`
	ExpiresAt    *time.Time `json:"expires_at"`
	RuntimeToken string     `json:"runtime_token,omitempty"`
	FRP          *FRPConfig `json:"frp,omitempty"`
}

// FRPConfig contains the server-authorized fields needed to start frpc.
type FRPConfig struct {
	ServerAddr  string `json:"server_addr"`
	ServerPort  int    `json:"server_port"`
	ServerToken string `json:"server_token"`
	ProxyName   string `json:"proxy_name"`
	ProxyType   string `json:"proxy_type"`
	Subdomain   string `json:"subdomain"`
	LocalIP     string `json:"local_ip"`
	LocalPort   int    `json:"local_port"`
}

// ListTunnelsResponse is one page of the tunnel-list API.
type ListTunnelsResponse struct {
	Tunnels    []TunnelResponse `json:"tunnels"`
	NextCursor *string          `json:"next_cursor"`
}

// ErrorResponse is the public API error envelope.
type ErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

const maxClientResponseBodySize = 1024 * 1024

// CreateTunnel calls POST /v1/tunnels. If idempotencyKey is non-empty it is
// passed as the Idempotency-Key header so safe network retries don't allocate
// duplicate tunnels against the user's quota.
func (c *Client) CreateTunnel(req *CreateTunnelRequest, idempotencyKey string) (*TunnelResponse, error) {
	return c.CreateTunnelCtx(context.Background(), req, idempotencyKey)
}

// CreateTunnelCtx is the context-aware variant of CreateTunnel. Use it when
// you need to bound the call by something other than the client's default
// Timeout — e.g. cleanup on Ctrl-C.
func (c *Client) CreateTunnelCtx(ctx context.Context, req *CreateTunnelRequest, idempotencyKey string) (*TunnelResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	headers := map[string]string{}
	if idempotencyKey != "" {
		headers["Idempotency-Key"] = idempotencyKey
	}

	// A create may commit even if its 201 response is truncated in transit.
	// One same-key replay recovers the cached response and its one-time runtime
	// credential without allocating another tunnel.
	const maxDecodeAttempts = 2
	for attempt := 1; ; attempt++ {
		resp, err := c.doCtx(ctx, "POST", "/v1/tunnels", body, headers)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusCreated {
			err := parseError(resp)
			closeResponseBody(resp)
			return nil, err
		}

		tunnel, decodeErr := decodeTunnelResponse(resp)
		closeResponseBody(resp)
		if decodeErr == nil {
			return tunnel, nil
		}
		if idempotencyKey == "" || attempt == maxDecodeAttempts {
			return nil, decodeErr
		}
		if err := waitForRetry(ctx, retryDelay(nil, attempt)); err != nil {
			return nil, err
		}
	}
}

func decodeTunnelResponse(resp *http.Response) (*TunnelResponse, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxClientResponseBodySize+1))
	if err != nil {
		return nil, fmt.Errorf("read create response: %w", err)
	}
	if len(body) > maxClientResponseBodySize {
		return nil, fmt.Errorf("create response exceeds %d bytes", maxClientResponseBodySize)
	}
	var tunnel TunnelResponse
	if err := json.Unmarshal(body, &tunnel); err != nil {
		return nil, fmt.Errorf("decode create response: %w", err)
	}
	return &tunnel, nil
}

// ListTunnels calls GET /v1/tunnels and follows pagination until exhausted.
// limit is the per-page hint passed to the server (default 50, max 100).
func (c *Client) ListTunnels(limit int) (*ListTunnelsResponse, error) {
	return c.ListTunnelsCtx(context.Background(), limit)
}

// ListTunnelsCtx is the context-aware variant of ListTunnels.
func (c *Client) ListTunnelsCtx(ctx context.Context, limit int) (*ListTunnelsResponse, error) {
	all := &ListTunnelsResponse{}
	cursor := ""
	seenCursors := make(map[string]struct{})
	for {
		path := "/v1/tunnels"
		q := url.Values{}
		if limit > 0 {
			q.Set("limit", strconv.Itoa(limit))
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		if encoded := q.Encode(); encoded != "" {
			path = path + "?" + encoded
		}

		//nolint:bodyclose // decodeListPage owns and closes the response body.
		resp, err := c.doCtx(ctx, "GET", path, nil, nil)
		if err != nil {
			return nil, err
		}
		page, err := decodeListPage(resp)
		if err != nil {
			return nil, err
		}
		all.Tunnels = append(all.Tunnels, page.Tunnels...)

		if page.NextCursor == nil || *page.NextCursor == "" {
			all.NextCursor = nil
			return all, nil
		}
		if _, seen := seenCursors[*page.NextCursor]; seen {
			return nil, fmt.Errorf("server returned a repeated pagination cursor")
		}
		seenCursors[*page.NextCursor] = struct{}{}
		cursor = *page.NextCursor
	}
}

func decodeListPage(resp *http.Response) (*ListTunnelsResponse, error) {
	defer closeResponseBody(resp)
	if resp.StatusCode != http.StatusOK {
		return nil, parseError(resp)
	}
	var page ListTunnelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &page, nil
}

// GetTunnel calls GET /v1/tunnels/{id}.
func (c *Client) GetTunnel(id string) (*TunnelResponse, error) {
	return c.GetTunnelCtx(context.Background(), id)
}

// GetTunnelCtx is the context-aware variant of GetTunnel.
func (c *Client) GetTunnelCtx(ctx context.Context, id string) (*TunnelResponse, error) {
	resp, err := c.doCtx(ctx, "GET", "/v1/tunnels/"+url.PathEscape(id), nil, nil)
	if err != nil {
		return nil, err
	}
	defer closeResponseBody(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, parseError(resp)
	}

	var tunnel TunnelResponse
	if err := json.NewDecoder(resp.Body).Decode(&tunnel); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &tunnel, nil
}

// DeleteTunnel calls DELETE /v1/tunnels/{id}.
func (c *Client) DeleteTunnel(id string) error {
	return c.DeleteTunnelCtx(context.Background(), id)
}

// DeleteTunnelCtx is the context-aware variant of DeleteTunnel.
func (c *Client) DeleteTunnelCtx(ctx context.Context, id string) error {
	resp, err := c.doCtx(ctx, "DELETE", "/v1/tunnels/"+url.PathEscape(id), nil, nil)
	if err != nil {
		return err
	}
	defer closeResponseBody(resp)

	if resp.StatusCode != http.StatusNoContent {
		return parseError(resp)
	}
	return nil
}

// Whoami calls GET /v1/me.
func (c *Client) Whoami() (string, error) {
	return c.WhoamiCtx(context.Background())
}

// WhoamiCtx is the context-aware variant of Whoami.
func (c *Client) WhoamiCtx(ctx context.Context) (string, error) {
	resp, err := c.doCtx(ctx, "GET", "/v1/me", nil, nil)
	if err != nil {
		return "", err
	}
	defer closeResponseBody(resp)

	if resp.StatusCode != http.StatusOK {
		return "", parseError(resp)
	}

	var result struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	return result.UserID, nil
}

func (c *Client) doCtx(ctx context.Context, method, path string, body []byte, headers map[string]string) (*http.Response, error) {
	const maxAttempts = 3
	retryableRequest := method == http.MethodGet || method == http.MethodHead || method == http.MethodDelete || headers["Idempotency-Key"] != ""
	retryIdempotencyConflict := headers["Idempotency-Key"] != ""

	for attempt := 1; ; attempt++ {
		var bodyReader io.Reader
		if body != nil {
			bodyReader = bytes.NewReader(body)
		}

		req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bodyReader)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}

		resp, err := c.HTTPClient.Do(req)
		if !retryableRequest || attempt == maxAttempts || !shouldRetry(resp, err, retryIdempotencyConflict) {
			return resp, err
		}
		if resp != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
			_ = resp.Body.Close()
		}
		if err := waitForRetry(ctx, retryDelay(resp, attempt)); err != nil {
			return nil, err
		}
	}
}

func shouldRetry(resp *http.Response, err error, retryIdempotencyConflict bool) bool {
	if err != nil {
		return true
	}
	if resp == nil {
		return false
	}
	switch resp.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	case http.StatusConflict:
		return retryIdempotencyConflict && resp.Header.Get("Retry-After") != ""
	default:
		return false
	}
}

func retryDelay(resp *http.Response, attempt int) time.Duration {
	delay := time.Duration(1<<(attempt-1)) * 100 * time.Millisecond
	if resp == nil {
		return delay
	}
	retryAfter, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil || retryAfter < 0 {
		return delay
	}
	const maxRetryAfter = 5 * time.Second
	serverDelay := min(time.Duration(retryAfter)*time.Second, maxRetryAfter)
	return max(delay, serverDelay)
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func parseError(resp *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return fmt.Errorf("read HTTP %d error response: %w", resp.StatusCode, err)
	}
	var errResp ErrorResponse
	if json.Unmarshal(body, &errResp) == nil && errResp.Error.Message != "" {
		return fmt.Errorf("%s: %s", errResp.Error.Code, errResp.Error.Message)
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
}

func closeResponseBody(resp *http.Response) {
	_ = resp.Body.Close()
}

// ParseTTL parses a human-friendly TTL string like "15m", "1h", "24h". The
// real upper bound lives on the server (HATCHWAY_MAX_TTL); the client just
// rejects parse errors and non-positive values so obvious typos surface
// before a network round-trip.
func ParseTTL(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid TTL %q (use e.g. 5m, 15m, 1h, 24h)", s)
	}
	if d <= 0 {
		return 0, fmt.Errorf("TTL must be positive, got %s", s)
	}
	if d < time.Second || d%time.Second != 0 {
		return 0, fmt.Errorf("TTL must be a whole number of seconds and at least 1s, got %s", s)
	}
	return d, nil
}

// CheckLocalPort dials 127.0.0.1:port to verify a service is listening.
func CheckLocalPort(port int) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return fmt.Errorf("nothing listening on %s: %w", addr, err)
	}
	if err := conn.Close(); err != nil {
		return fmt.Errorf("close local port probe: %w", err)
	}
	return nil
}
