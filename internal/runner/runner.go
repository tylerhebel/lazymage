// Package runner starts external commands asynchronously and streams their
// output back to the UI as messages (section 9 execution rules).
package runner

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Chunk is a piece of output, in arrival order. Lines are split by the UI.
type Chunk struct {
	RunID  int
	Data   string
	Stderr bool
}

// Exited reports the end of a run.
type Exited struct {
	RunID     int
	Code      int // -1 when killed by a signal or failed to start
	Err       error
	Cancelled bool
	Duration  time.Duration
}

// Run is a running child process.
type Run struct {
	ID        int
	Argv      []string
	Started   time.Time
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	cancelled atomic.Bool
	done      chan struct{}
}

// Sender delivers messages to the UI (tea.Program.Send).
type Sender func(msg any)

// Start runs argv in dir. Output is delivered as Chunk messages (batched every
// ~30ms) and the end as one Exited message. argv[0] beginning with "bin/" is
// resolved inside dir.
func Start(id int, dir string, argv []string, env []string, send Sender) (*Run, error) {
	if len(argv) == 0 {
		return nil, errors.New("empty command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if strings.HasPrefix(argv[0], "bin/") {
		cmd.Path = filepath.Join(dir, argv[0])
		cmd.Err = nil
	}
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	setProcessGroup(cmd)

	agg := newAggregator(id, send)
	cmd.Stdout = agg.writer(false)
	cmd.Stderr = agg.writer(true)
	// docker-magento's bin/start leaves a background watcher holding our pipes
	// open after the script itself exits; don't wait on it forever.
	cmd.WaitDelay = 1500 * time.Millisecond
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	r := &Run{ID: id, Argv: argv, Started: time.Now(), cmd: cmd, stdin: stdin, done: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		agg.close()
		return nil, err
	}
	go func() {
		err := cmd.Wait()
		agg.close()
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		if errors.Is(err, exec.ErrWaitDelay) {
			err = nil // the command exited; only a detached child kept the pipes
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			err = nil // non-zero exit is reported through Code
		}
		close(r.done)
		send(Exited{RunID: id, Code: code, Err: err, Cancelled: r.cancelled.Load(), Duration: time.Since(r.Started)})
	}()
	return r, nil
}

// WriteStdin sends a line of input to the child.
func (r *Run) WriteStdin(s string) error {
	_, err := io.WriteString(r.stdin, s)
	return err
}

// CloseStdin sends EOF.
func (r *Run) CloseStdin() { _ = r.stdin.Close() }

// Cancel interrupts the child's process group, escalating to SIGTERM after 3s
// and SIGKILL after 6s. It does not block.
func (r *Run) Cancel() {
	if !r.cancelled.CompareAndSwap(false, true) {
		return
	}
	go func() {
		interruptGroup(r.cmd)
		for _, step := range []struct {
			after time.Duration
			fn    func(*exec.Cmd)
		}{{3 * time.Second, terminateGroup}, {3 * time.Second, killGroup}} {
			select {
			case <-r.done:
				return
			case <-time.After(step.after):
				step.fn(r.cmd)
			}
		}
	}()
}

// Terminate signals the process group immediately and synchronously (used on
// quit, when there is no time to escalate gently).
func (r *Run) Terminate() {
	r.cancelled.Store(true)
	interruptGroup(r.cmd)
	terminateGroup(r.cmd)
}

// Done is closed when the process has exited.
func (r *Run) Done() <-chan struct{} { return r.done }

// aggregator batches output writes into Chunk messages, preserving the order
// of writes across stdout and stderr.
type aggregator struct {
	id     int
	send   Sender
	mu     sync.Mutex
	sendMu sync.Mutex // serializes flushes so chunks never arrive after Exited
	parts  []Chunk
	timer  *time.Timer
	closed bool
}

func newAggregator(id int, send Sender) *aggregator { return &aggregator{id: id, send: send} }

type aggWriter struct {
	a      *aggregator
	stderr bool
}

func (a *aggregator) writer(stderr bool) io.Writer { return &aggWriter{a: a, stderr: stderr} }

func (w *aggWriter) Write(p []byte) (int, error) {
	a := w.a
	a.mu.Lock()
	defer a.mu.Unlock()
	if n := len(a.parts); n > 0 && a.parts[n-1].Stderr == w.stderr {
		a.parts[n-1].Data += string(p)
	} else {
		a.parts = append(a.parts, Chunk{RunID: a.id, Data: string(p), Stderr: w.stderr})
	}
	if a.timer == nil && !a.closed {
		a.timer = time.AfterFunc(30*time.Millisecond, a.flush)
	}
	return len(p), nil
}

func (a *aggregator) flush() {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.mu.Lock()
	parts := a.parts
	a.parts = nil
	a.timer = nil
	a.mu.Unlock()
	for _, c := range parts {
		a.send(c)
	}
}

func (a *aggregator) close() {
	a.mu.Lock()
	a.closed = true
	if a.timer != nil {
		a.timer.Stop()
		a.timer = nil
	}
	a.mu.Unlock()
	a.flush()
}

// Capture runs a short command and returns its combined result; used for
// fact lookups (bin/magento --version etc.). It honours timeout.
func Capture(dir string, argv []string, timeout time.Duration) (stdout, stderr string, code int, err error) {
	if len(argv) == 0 {
		return "", "", -1, errors.New("empty command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if strings.HasPrefix(argv[0], "bin/") {
		cmd.Path = filepath.Join(dir, argv[0])
		cmd.Err = nil
	}
	cmd.Dir = dir
	setProcessGroup(cmd)
	var o, e strings.Builder
	cmd.Stdout, cmd.Stderr = &o, &e
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return "", "", -1, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-time.After(timeout):
		killGroup(cmd)
		<-done
		return o.String(), e.String(), -1, errors.New("timed out")
	}
	code = -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) || errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	return o.String(), e.String(), code, err
}
