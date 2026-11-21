package runner

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

type collector struct {
	mu   sync.Mutex
	msgs []any
	done chan Exited
}

func (c *collector) send(m any) {
	c.mu.Lock()
	c.msgs = append(c.msgs, m)
	c.mu.Unlock()
	if e, ok := m.(Exited); ok {
		c.done <- e
	}
}

func (c *collector) output() (out, errOut string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range c.msgs {
		if ch, ok := m.(Chunk); ok {
			if ch.Stderr {
				errOut += ch.Data
			} else {
				out += ch.Data
			}
		}
	}
	return
}

func TestStreamsAndExitCode(t *testing.T) {
	c := &collector{done: make(chan Exited, 1)}
	_, err := Start(1, t.TempDir(), []string{"sh", "-c", "echo out; echo err >&2; exit 3"}, nil, c.send)
	if err != nil {
		t.Fatal(err)
	}
	e := <-c.done
	out, errOut := c.output()
	if e.Code != 3 || out != "out\n" || errOut != "err\n" {
		t.Fatalf("code=%d out=%q err=%q", e.Code, out, errOut)
	}
}

func TestStdinPrompt(t *testing.T) {
	c := &collector{done: make(chan Exited, 1)}
	r, err := Start(2, t.TempDir(), []string{"sh", "-c", "printf 'Continue? [y/N] '; read a; echo got:$a"}, nil, c.send)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if out, _ := c.output(); out != "Continue? [y/N] " {
		t.Fatalf("prompt without newline not delivered: %q", out)
	}
	r.WriteStdin("y\n")
	e := <-c.done
	if out, _ := c.output(); !strings.Contains(out, "got:y") || e.Code != 0 {
		t.Fatalf("out=%q code=%d", out, e.Code)
	}
}

func TestCancelKillsGroup(t *testing.T) {
	c := &collector{done: make(chan Exited, 1)}
	r, err := Start(3, t.TempDir(), []string{"sh", "-c", "sleep 30 & sleep 30; wait"}, nil, c.send)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	r.Cancel()
	select {
	case e := <-c.done:
		if !e.Cancelled || e.Code == 0 {
			t.Fatalf("want cancelled non-zero, got %+v", e)
		}
		if time.Since(start) > 4*time.Second {
			t.Fatal("cancel too slow")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("cancel did not stop the process")
	}
}

func TestDetachedChildDoesNotHang(t *testing.T) {
	c := &collector{done: make(chan Exited, 1)}
	// Like docker-magento bin/start: a background child keeps stdout open.
	_, err := Start(4, t.TempDir(), []string{"sh", "-c", "echo started; sleep 20 & exit 0"}, nil, c.send)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-c.done:
		if e.Code != 0 || e.Err != nil {
			t.Fatalf("%+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hung on detached child")
	}
}

func TestRelativeBinResolvesInDir(t *testing.T) {
	dir := t.TempDir()
	stdout, _, code, err := Capture(dir, []string{"sh", "-c", "pwd"}, 2*time.Second)
	if err != nil || code != 0 || !strings.Contains(stdout, dir[len(dir)-8:]) {
		t.Fatalf("%q %d %v", stdout, code, err)
	}
}

func TestStartIORedirects(t *testing.T) {
	var out bytes.Buffer
	msgs := make(chan any, 100)
	r, err := StartIO(1, t.TempDir(), []string{"sh", "-c", "tr a-z A-Z; echo oops >&2"}, nil, func(m any) { msgs <- m }, IO{Stdin: strings.NewReader("hello\n"), Stdout: &out})
	if err != nil {
		t.Fatal(err)
	}
	<-r.Done()
	var stderr string
	for {
		m := <-msgs
		if c, ok := m.(Chunk); ok {
			if !c.Stderr {
				t.Errorf("stdout leaked to the UI: %q", c.Data)
			}
			stderr += c.Data
		}
		if e, ok := m.(Exited); ok {
			if e.Code != 0 {
				t.Fatalf("exit %d", e.Code)
			}
			break
		}
	}
	if out.String() != "HELLO\n" || stderr != "oops\n" {
		t.Fatalf("stdout %q stderr %q", out.String(), stderr)
	}
	if r.WriteStdin("x") == nil {
		t.Error("WriteStdin should refuse when stdin is a file")
	}
}
