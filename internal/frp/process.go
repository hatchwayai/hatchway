package frp

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Process manages an frpc or frps subprocess.
type Process struct {
	name    string
	binPath string
	args    []string

	mu      sync.Mutex
	run     *processRun
	running bool
}

type processRun struct {
	cmd     *exec.Cmd
	done    chan struct{}
	waitErr error
}

// NewProcess creates a new subprocess manager.
// name is a human-readable label (e.g. "frpc", "frps") for logging.
func NewProcess(name, binPath string, args []string) *Process {
	return &Process{
		name:    name,
		binPath: binPath,
		args:    args,
	}
}

// Start launches the subprocess. Returns an error if the binary
// cannot be found or fails to start.
func (p *Process) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running {
		return fmt.Errorf("%s already running", p.name)
	}

	// #nosec G204 -- callers provide an explicit executable and argument
	// vector; exec.CommandContext does not invoke a shell.
	cmd := exec.CommandContext(ctx, p.binPath, p.args...)
	cmd.Stderr = &logWriter{name: p.name}
	cmd.Stdout = &logWriter{name: p.name}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", p.name, err)
	}

	run := &processRun{cmd: cmd, done: make(chan struct{})}
	p.run = run
	p.running = true

	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		run.waitErr = err
		if p.run == run {
			p.running = false
		}
		p.mu.Unlock()

		if err != nil {
			slog.Warn(p.name+" exited", "error", err)
		} else {
			slog.Info(p.name + " exited cleanly")
		}
		close(run.done)
	}()

	slog.Info(p.name+" started", "bin", p.binPath)
	return nil
}

// Stop sends an interrupt signal (SIGINT on Unix) to the subprocess and waits
// up to timeout for it to exit. Sends SIGKILL if it doesn't exit in time.
// Safe to call concurrently with Start: the *os.Process is captured under the
// lock, so we never race with a fresh Start replacing p.run while we're
// signaling.
func (p *Process) Stop(timeout time.Duration) error {
	p.mu.Lock()
	if p.run == nil || !p.running {
		p.mu.Unlock()
		return nil
	}
	run := p.run
	proc := run.cmd.Process
	done := run.done
	p.mu.Unlock()

	if proc == nil {
		return nil
	}

	if err := proc.Signal(os.Interrupt); err != nil {
		_ = proc.Kill()
	}

	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		slog.Warn(p.name + " didn't exit in time, killing")
		_ = proc.Kill()
		<-done
		return fmt.Errorf("%s killed after timeout", p.name)
	}
}

// Wait blocks until the subprocess exits and returns its error (nil for clean exit).
func (p *Process) Wait() error {
	p.mu.Lock()
	run := p.run
	p.mu.Unlock()
	if run == nil {
		return fmt.Errorf("%s has not been started", p.name)
	}

	<-run.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return run.waitErr
}

// Running returns whether the subprocess is currently running.
func (p *Process) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

// RestartWithBackoff stops the process, waits, then starts it again.
// maxBackoff caps the wait duration.
func (p *Process) RestartWithBackoff(ctx context.Context, maxBackoff time.Duration) error {
	if err := p.Stop(5 * time.Second); err != nil {
		slog.Warn("stop failed during restart", "name", p.name, "error", err)
	}

	select {
	case <-time.After(maxBackoff):
	case <-ctx.Done():
		return ctx.Err()
	}

	return p.Start(ctx)
}

// logWriter pipes subprocess output to slog.
type logWriter struct {
	name string
}

func (w *logWriter) Write(p []byte) (n int, err error) {
	slog.Debug(w.name, "output", string(p))
	return len(p), nil
}
