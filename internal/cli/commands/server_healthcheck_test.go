package commands

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func executeHealthcheck(t *testing.T, args ...string) error {
	t.Helper()
	cmd := serverHealthcheckCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs(args)
	return cmd.Execute()
}

func TestServerHealthcheckCmdDefaults(t *testing.T) {
	cmd := serverHealthcheckCmd()
	urlFlag := cmd.Flags().Lookup("url")
	if urlFlag == nil || urlFlag.DefValue != defaultHealthcheckURL {
		t.Fatalf("--url default = %v, want %q", urlFlag, defaultHealthcheckURL)
	}
	timeoutFlag := cmd.Flags().Lookup("timeout")
	if timeoutFlag == nil || timeoutFlag.DefValue != defaultHealthcheckTimeout.String() {
		t.Fatalf("--timeout default = %v, want %s", timeoutFlag, defaultHealthcheckTimeout)
	}
	if findByName(serverCmd(), "healthcheck") == nil {
		t.Fatal("server command does not register healthcheck")
	}
}

func TestServerHealthcheckCmdSuccess(t *testing.T) {
	methodCh := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methodCh <- r.Method
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ready")
	}))
	defer server.Close()

	if err := executeHealthcheck(t, "--url", server.URL, "--timeout", "1s"); err != nil {
		t.Fatalf("healthcheck failed: %v", err)
	}
	method := <-methodCh
	if method != http.MethodGet {
		t.Fatalf("method = %q, want GET", method)
	}
}

func TestServerHealthcheckCmdRequiresHTTP200WithoutLeakingBody(t *testing.T) {
	const secret = "database-password=do-not-print"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, secret, http.StatusServiceUnavailable)
	}))
	defer server.Close()

	err := executeHealthcheck(t, "--url", server.URL)
	if err == nil || err.Error() != "healthcheck returned HTTP 503" {
		t.Fatalf("error = %v, want concise HTTP 503 error", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("healthcheck error exposed response body: %v", err)
	}
}

func TestServerHealthcheckCmdTimeoutDoesNotExposeURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	const querySecret = "token=do-not-print"
	err := executeHealthcheck(t, "--url", server.URL+"?"+querySecret, "--timeout", "10ms")
	if err == nil || err.Error() != "healthcheck timed out" {
		t.Fatalf("error = %v, want timeout error", err)
	}
	if strings.Contains(err.Error(), querySecret) {
		t.Fatalf("healthcheck error exposed URL query: %v", err)
	}
}

func TestServerHealthcheckCmdRejectsNonPositiveTimeout(t *testing.T) {
	err := executeHealthcheck(t, "--timeout", "0s")
	if err == nil || err.Error() != "healthcheck timeout must be positive" {
		t.Fatalf("error = %v, want positive-timeout error", err)
	}
}

type trackingBody struct {
	*strings.Reader
	closed bool
}

func (b *trackingBody) Close() error {
	b.closed = true
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRunServerHealthcheckDrainsAndClosesResponse(t *testing.T) {
	body := &trackingBody{Reader: strings.NewReader("ready")}
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       body,
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}

	if err := runServerHealthcheck(context.Background(), client, "http://healthcheck.invalid/readyz"); err != nil {
		t.Fatalf("healthcheck failed: %v", err)
	}
	if body.Len() != 0 {
		t.Fatalf("response body was not drained; %d bytes remain", body.Len())
	}
	if !body.closed {
		t.Fatal("response body was not closed")
	}
}

func TestRunServerHealthcheckHidesTransportError(t *testing.T) {
	const secret = "proxy-password=do-not-print"
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New(secret)
	})}

	err := runServerHealthcheck(context.Background(), client, "http://healthcheck.invalid/readyz?token=also-secret")
	if err == nil || err.Error() != "healthcheck request failed" {
		t.Fatalf("error = %v, want generic request failure", err)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), secret) {
		t.Fatalf("healthcheck error exposed transport details: %v", err)
	}
}
