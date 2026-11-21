package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"lazymage/internal/config"
	"lazymage/internal/runner"
)

// A step plan runs several steps in order — in-process work (file edits,
// checks) and commands — and stops at the first failure. If the plan has a
// rollback, it runs after a failure, so a half-applied change does not stay
// behind. A summary of every step is shown at the end.

type step struct {
	label    string
	fn       func() error    // in-process work; runs off the UI thread
	argv     []string        // a command, streamed into the output view
	argvFn   func() []string // a command built when the step starts
	term     bool            // run argv with the terminal handed over
	kind     string          // history kind for commands
	optional bool            // a failure is reported but does not stop the plan
	skip     func() bool     // evaluated when the step is reached
	changes  bool            // changes the environment: from here on, a failure rolls back
}

type stepPlan struct {
	title    string
	steps    []step
	i        int
	results  []stepResult
	rollback []step // run after a failure
	rolling  bool
	changed  bool // a changing step has started
	onDone   func(ok bool) tea.Cmd
	started  time.Time
}

type stepResult struct {
	label  string
	state  string // ok, failed, skipped, warn
	detail string
}

type stepFnMsg struct {
	p   *stepPlan
	err error
}

func (a *App) runPlan(p *stepPlan) tea.Cmd {
	if a.run != nil {
		return a.setStatus("a command is already running (ctrl+c cancels it)", true)
	}
	p.started = time.Now()
	a.log.printf("plan: %s (%d steps)", p.title, len(p.steps))
	return a.nextStep(p)
}

func (p *stepPlan) current() *step {
	list := p.steps
	if p.rolling {
		list = p.rollback
	}
	if p.i >= len(list) {
		return nil
	}
	return &list[p.i]
}

func (p *stepPlan) total() int {
	if p.rolling {
		return len(p.rollback)
	}
	return len(p.steps)
}

func (a *App) nextStep(p *stepPlan) tea.Cmd {
	for {
		s := p.current()
		if s == nil {
			return a.finishPlan(p, !p.rolling && !p.failed())
		}
		if s.skip != nil && s.skip() {
			p.results = append(p.results, stepResult{label: s.label, state: "skipped"})
			p.i++
			continue
		}
		if s.changes {
			p.changed = true
		}
		a.status = fmt.Sprintf("%s — step %d/%d: %s", p.title, p.i+1, p.total(), s.label)
		switch {
		case s.fn != nil:
			fn := s.fn
			return func() tea.Msg { return stepFnMsg{p: p, err: fn()} }
		case s.term:
			argv := s.argv
			if s.argvFn != nil {
				argv = s.argvFn()
			}
			return a.runInTerminalCode(argv, s.kind, func(code int, err error) tea.Cmd {
				if err == nil && code != 0 {
					err = fmt.Errorf("exit %d", code)
				}
				return a.stepDone(p, err)
			})
		default:
			argv := s.argv
			if s.argvFn != nil {
				argv = s.argvFn()
			}
			if len(argv) == 0 {
				p.results = append(p.results, stepResult{label: s.label, state: "skipped"})
				p.i++
				continue
			}
			kind := s.kind
			if kind == "" {
				kind = config.KindCompose
			}
			label := s.label
			n, tot := p.i+1, p.total()
			cmd := a.startRun(argv, runOpts{kind: kind, onExit: func(e runner.Exited) tea.Cmd {
				var err error
				switch {
				case e.Cancelled:
					err = fmt.Errorf("cancelled")
				case e.Err != nil:
					err = e.Err
				case e.Code != 0:
					err = fmt.Errorf("exit %d", e.Code)
				}
				return a.stepDone(p, err)
			}})
			if a.out != nil {
				prefix := p.title
				if p.rolling {
					prefix = "Rolling back " + p.title
				}
				a.out.buf.Note(fmt.Sprintf("── %s · step %d/%d: %s", prefix, n, tot, label))
			}
			return cmd
		}
	}
}

func (p *stepPlan) failed() bool {
	for _, r := range p.results {
		if r.state == "failed" {
			return true
		}
	}
	return false
}

func (a *App) onStepFn(m stepFnMsg) tea.Cmd { return a.stepDone(m.p, m.err) }

func (a *App) stepDone(p *stepPlan, err error) tea.Cmd {
	s := p.current()
	if s == nil {
		return nil
	}
	label := s.label
	if p.rolling {
		label = "rollback: " + label
	}
	if err == nil {
		p.results = append(p.results, stepResult{label: label, state: "ok"})
		p.i++
		return a.nextStep(p)
	}
	a.log.printf("plan %s: step %q failed: %v", p.title, s.label, err)
	if s.optional {
		p.results = append(p.results, stepResult{label: label, state: "warn", detail: err.Error()})
		p.i++
		return a.nextStep(p)
	}
	p.results = append(p.results, stepResult{label: label, state: "failed", detail: err.Error()})
	if !p.rolling && p.changed && len(p.rollback) > 0 {
		p.rolling, p.i = true, 0
		return a.nextStep(p)
	}
	return a.finishPlan(p, false)
}

func (a *App) finishPlan(p *stepPlan, ok bool) tea.Cmd {
	var b strings.Builder
	for _, r := range p.results {
		mark := sOK.Render("✓")
		switch r.state {
		case "failed":
			mark = sErr.Render("✗")
		case "warn":
			mark = sWarn.Render("◐")
		case "skipped":
			mark = sFaint.Render("–")
		}
		line := mark + " " + r.label
		if r.state == "skipped" {
			line += sFaint.Render(" (not needed)")
		}
		if r.detail != "" {
			line += sFaint.Render(" — " + r.detail)
		}
		b.WriteString(line + "\n")
	}
	title := p.title + " — done"
	switch {
	case p.rolling:
		title = p.title + " — failed, rolled back"
		b.WriteString("\n" + sWarn.Render("Nothing was left half-changed: the earlier steps were undone.") + "\n")
	case !ok:
		title = p.title + " — failed"
	}
	if !ok {
		b.WriteString("\nThe failing step's output is in the output view (v). The doctor (D) re-checks\nthe environment and offers fixes.\n")
	}
	b.WriteString("\n" + sFaint.Render(fmt.Sprintf("took %s · enter / esc to close", fmtDur(time.Since(p.started)))))
	a.status = ""
	a.push(&messageOverlay{title: title, body: b.String()})
	var cmds []tea.Cmd
	if ok {
		cmds = append(cmds, a.setStatus(p.title+" — done", false))
	} else {
		cmds = append(cmds, a.setStatus(p.title+" — failed", true))
	}
	if p.onDone != nil {
		cmds = append(cmds, p.onDone(ok))
	}
	return tea.Batch(cmds...)
}

// runInTerminalCode is runInTerminal with the exit code passed on.
func (a *App) runInTerminalCode(argv []string, kind string, then func(code int, err error) tea.Cmd) tea.Cmd {
	return a.runInTerminal(argv, kind, nil, then)
}
