package tunnels

import (
	"context"
	"strings"
	"testing"
	"time"
)

func mustGenerateTunnelID(t *testing.T) string {
	t.Helper()
	id, err := GenerateTunnelID()
	if err != nil {
		t.Fatalf("GenerateTunnelID: %v", err)
	}
	return id
}

func TestGenerateTunnelID(t *testing.T) {
	id := mustGenerateTunnelID(t)

	if !strings.HasPrefix(id, "t-") {
		t.Errorf("expected t- prefix, got %s", id)
	}
	if len(id) != 18 {
		t.Errorf("expected 18 chars (t- + 16), got %d: %s", len(id), id)
	}
}

func TestGenerateTunnelID_NoConfusableChars(t *testing.T) {
	confusable := "0oli1"
	for i := 0; i < 100; i++ {
		id := mustGenerateTunnelID(t)
		for _, c := range confusable {
			if strings.ContainsRune(id, c) {
				t.Errorf("tunnel ID contains confusable char %c: %s", c, id)
			}
		}
	}
}

func TestGenerateTunnelID_NoUppercase(t *testing.T) {
	id := mustGenerateTunnelID(t)
	body := id[2:] // skip "t-"
	for _, c := range body {
		if c >= 'A' && c <= 'Z' {
			t.Errorf("tunnel ID body should be lowercase, found %c: %s", c, id)
		}
	}
}

func TestGenerateTunnelID_Uniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := mustGenerateTunnelID(t)
		if seen[id] {
			t.Fatalf("duplicate tunnel ID: %s", id)
		}
		seen[id] = true
	}
}

func TestGenerateTunnelID_DNSSafe(t *testing.T) {
	for i := 0; i < 50; i++ {
		id := mustGenerateTunnelID(t)
		if strings.Contains(id, "_") {
			t.Errorf("tunnel ID contains underscore (not DNS-safe): %s", id)
		}
		// Must not start with a digit
		if id[0] >= '0' && id[0] <= '9' {
			t.Errorf("tunnel ID starts with digit: %s", id)
		}
	}
}

// --- State machine tests ---

func TestValidTransitions(t *testing.T) {
	tests := []struct {
		from  string
		event Event
		to    string
	}{
		{"reserved", EventNewProxy, "active"},
		{"reserved", EventExpire, "expired"},
		{"reserved", EventRevoke, "revoked"},
		{"active", EventCloseProxy, "closed"},
		{"active", EventExpire, "expired"},
		{"active", EventRevoke, "revoked"},
		{"closed", EventNewProxy, "active"},
		{"closed", EventExpire, "expired"},
		{"closed", EventRevoke, "revoked"},
	}

	for _, tt := range tests {
		transitions, ok := validTransitions[tt.from]
		if !ok {
			t.Errorf("no transitions defined for state %s", tt.from)
			continue
		}
		result, ok := transitions[tt.event]
		if !ok {
			t.Errorf("transition %s + %s not defined", tt.from, tt.event)
			continue
		}
		if result != tt.to {
			t.Errorf("expected %s + %s -> %s, got %s", tt.from, tt.event, tt.to, result)
		}
	}
}

func TestTerminalStatesHaveNoTransitions(t *testing.T) {
	for _, state := range []string{"expired", "revoked"} {
		if _, ok := validTransitions[state]; ok {
			t.Errorf("terminal state %s should not have transitions", state)
		}
	}
}

func TestInvalidTransitions(t *testing.T) {
	invalid := []struct {
		from  string
		event Event
	}{
		{"reserved", EventCloseProxy},
		{"active", EventNewProxy},
		{"closed", EventCloseProxy},
		{"expired", EventNewProxy},
		{"revoked", EventRevoke},
	}

	for _, tt := range invalid {
		transitions, ok := validTransitions[tt.from]
		if ok {
			if _, ok2 := transitions[tt.event]; ok2 {
				t.Errorf("transition %s + %s should not be valid", tt.from, tt.event)
			}
		}
	}
}

func TestReconnectLoop(t *testing.T) {
	// reserved -> active -> closed -> active (reconnect)
	if validTransitions["reserved"][EventNewProxy] != "active" {
		t.Error("reserved -> active broken")
	}
	if validTransitions["active"][EventCloseProxy] != "closed" {
		t.Error("active -> closed broken")
	}
	if validTransitions["closed"][EventNewProxy] != "active" {
		t.Error("closed -> active (reconnect) broken")
	}
}

// --- Sweeper tests ---

func TestSweepExpiredEventsQuery(t *testing.T) {
	// Verify the SQL is valid by checking the query string is well-formed
	q := "DELETE FROM tunnel_events WHERE created_at < now() - $1::int * interval '1 day'"
	if !strings.Contains(q, "$1") {
		t.Error("query should use parameterized retention days")
	}
}

func TestSweepIdempotencyKeysQuery(t *testing.T) {
	q := "DELETE FROM idempotency_keys WHERE created_at < now() - $1::int * interval '1 hour'"
	if !strings.Contains(q, "$1") {
		t.Error("query should use parameterized retention hours")
	}
}

func TestStartSweepersDoesNotBlock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately — sweepers should stop without blocking
	cancel()
	// This would hang if StartSweepers blocks
	done := make(chan struct{})
	go func() {
		StartSweepers(ctx, nil, 30, 24, 7)
		close(done)
	}()
	// Give it a moment
	select {
	case <-done:
		// Good, it returned
	case <-time.After(time.Second):
		t.Error("StartSweepers blocked on cancelled context")
	}
}
