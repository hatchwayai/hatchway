package frp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestProcessStartAndStop(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not available")
	}

	p := NewProcess("test-sleep", "sleep", []string{"60"})

	ctx := context.Background()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !p.Running() {
		t.Error("should be running after Start")
	}

	if err := p.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if p.Running() {
		t.Error("should not be running after Stop")
	}
}

func TestProcessDoubleStart(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not available")
	}

	p := NewProcess("test", "sleep", []string{"60"})
	ctx := context.Background()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	defer func() { _ = p.Stop(2 * time.Second) }()

	if err := p.Start(ctx); err == nil {
		t.Error("expected error on double start")
	}
}

func TestProcessStopWhenNotRunning(t *testing.T) {
	p := NewProcess("test", "sleep", []string{"60"})
	// Stopping a process that was never started should be a no-op
	if err := p.Stop(1 * time.Second); err != nil {
		t.Fatalf("Stop on non-running process: %v", err)
	}
}

func TestProcessWait(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not available")
	}

	p := NewProcess("test", "sleep", []string{"0.1"})
	ctx := context.Background()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	_ = p.Wait()
	if p.Running() {
		t.Error("should not be running after Wait")
	}
}

func TestProcessWaitBeforeStartReturnsError(t *testing.T) {
	p := NewProcess("test", "sleep", []string{"1"})
	if err := p.Wait(); err == nil {
		t.Fatal("Wait before Start should return an error")
	}
}

func TestProcessWait_ReturnsExitError(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	p := NewProcess("test", "sh", []string{"-c", "exit 3"})
	ctx := context.Background()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := p.Wait(); err == nil {
		t.Error("Wait should return the subprocess's exit error for a nonzero exit, got nil")
	}
}

func TestProcessBadBinary(t *testing.T) {
	p := NewProcess("test", "/nonexistent/binary", []string{})
	ctx := context.Background()

	if err := p.Start(ctx); err == nil {
		t.Error("expected error for bad binary path")
		defer func() { _ = p.Stop(2 * time.Second) }()
	}
}

func TestProcessContextCancel(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not available")
	}

	ctx, cancel := context.WithCancel(context.Background())
	p := NewProcess("test", "sleep", []string{"60"})

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	cancel()

	// Give it a moment to react to context cancellation
	time.Sleep(200 * time.Millisecond)
	if p.Running() {
		_ = p.Stop(2 * time.Second)
	}
}

func TestProcessNewProcessFields(t *testing.T) {
	p := NewProcess("myapp", "/usr/bin/myapp", []string{"--flag", "value"})
	if p.name != "myapp" {
		t.Errorf("name = %q", p.name)
	}
	if p.binPath != "/usr/bin/myapp" {
		t.Errorf("binPath = %q", p.binPath)
	}
	if len(p.args) != 2 || p.args[0] != "--flag" {
		t.Errorf("args = %v", p.args)
	}
}

func TestLogWriter(t *testing.T) {
	w := &logWriter{name: "test"}
	n, err := w.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 5 {
		t.Errorf("wrote %d bytes, want 5", n)
	}
}

func TestRestartWithBackoff(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not available")
	}

	p := NewProcess("test", "sleep", []string{"0.1"})
	ctx := context.Background()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for it to finish
	_ = p.Wait()

	// Restart with short backoff
	if err := p.RestartWithBackoff(ctx, 100*time.Millisecond); err != nil {
		t.Fatalf("RestartWithBackoff: %v", err)
	}
	if !p.Running() {
		t.Error("should be running after restart")
	}
	_ = p.Stop(2 * time.Second)
}

func TestRestartWithBackoff_ContextCancelledDuringBackoff(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not available")
	}

	p := NewProcess("test", "sleep", []string{"0.1"})
	startCtx := context.Background()

	if err := p.Start(startCtx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = p.Wait()

	restartCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := p.RestartWithBackoff(restartCtx, time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("RestartWithBackoff with a cancelled ctx = %v, want context.Canceled", err)
	}
	if p.Running() {
		t.Error("should not have restarted when ctx was already cancelled")
	}
}

func TestMain(m *testing.M) {
	// Set slog to discard output during tests
	os.Exit(m.Run())
}
