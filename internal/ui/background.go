package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"lazymage/internal/config"
	"lazymage/internal/runner"
)

// Background tasks run beside the one foreground command: grunt watch
// starts there, and ctrl+b moves a running command there. Their output is
// kept; B lists them, and each can be viewed or stopped.

type bgTask struct {
	run  *runner.Run
	out  *outputView
	argv []string
}

func (a *App) bgRunning() bool {
	for _, t := range a.bgs {
		if t.out.exit == nil {
			return true
		}
	}
	return false
}

func (a *App) bgByRun(id int) *bgTask {
	for _, t := range a.bgs {
		if t.out.runID == id {
			return t
		}
	}
	return nil
}

// startBackground starts argv as a background task.
func (a *App) startBackground(argv []string, o runOpts, label string) tea.Cmd {
	if a.proj == nil {
		return a.setStatus("not in a docker-magento project", true)
	}
	for _, t := range a.bgs {
		if t.out.exit == nil && config.JoinArgs(t.argv) == config.JoinArgs(argv) {
			return a.setStatus(label+" is already running in the background — B shows it", false)
		}
	}
	a.runSeq++
	id := a.runSeq
	o.noStdin = true
	out := newOutputView(id, config.JoinArgs(argv), o)
	out.buf.Note("$ " + out.title)
	a.log.printf("background: %s", config.JoinArgs(config.RedactArgs(argv)))
	r, err := runner.StartIO(id, a.proj.Root, argv, nil, a.sendAny, o.rio)
	if err != nil {
		a.recordHistory(argv, o.kind, time.Now(), 0, -1)
		return a.setStatus("could not start "+label+": "+err.Error(), true)
	}
	out.run = r
	a.bgs = append(a.bgs, &bgTask{run: r, out: out, argv: argv})
	return a.setStatus(label+" started in the background — B shows its output", false)
}

// toBackground moves the running foreground command to the background.
func (a *App) toBackground() tea.Cmd {
	if a.run == nil || a.out == nil {
		return nil
	}
	if a.out.opts.onExit != nil || a.out.opts.lifecycle || a.out.opts.rio.Stdin != nil || a.out.opts.rio.Stdout != nil {
		return a.setStatus("this command has to finish in the foreground", true)
	}
	t := &bgTask{run: a.run, out: a.out, argv: a.run.Argv}
	t.out.opts.noStdin = true
	a.bgs = append(a.bgs, t)
	a.run, a.out, a.showO = nil, nil, false
	return a.setStatus("moved to the background — B shows it", false)
}

func (a *App) bgExit(t *bgTask, e runner.Exited) tea.Cmd {
	t.out.exit = &e
	switch {
	case e.Cancelled:
		t.out.buf.Note(fmt.Sprintf("stopped after %s", fmtDur(e.Duration)))
	case e.Code == 0 && e.Err == nil:
		t.out.buf.Note(fmt.Sprintf("exit code 0 · %s", fmtDur(e.Duration)))
	default:
		t.out.buf.Note(fmt.Sprintf("exit code %d · %s", e.Code, fmtDur(e.Duration)))
	}
	a.recordHistory(t.argv, t.out.opts.kind, time.Now().Add(-e.Duration), e.Duration, e.Code)
	name := bgName(t)
	if e.Code == 0 || e.Cancelled {
		return a.setStatus("background: "+name+" finished", false)
	}
	return a.setStatus(fmt.Sprintf("background: %s exited with code %d — B shows its output", name, e.Code), true)
}

// bgName is a short label: the meaningful tail of the command.
func bgName(t *bgTask) string {
	argv := t.argv
	for i, w := range argv {
		if w == "grunt" || w == "npm" || w == "bin/magento" || w == "composer" {
			return strings.Join(argv[i:], " ")
		}
	}
	return truncate(config.JoinArgs(argv), 30)
}

// bgChips are the tab-bar markers for background tasks.
func (a *App) bgChips() []string {
	var out []string
	for _, t := range a.bgs {
		name := truncate(bgName(t), 24)
		switch {
		case t.out.exit == nil:
			out = append(out, sWarn.Render("◌ "+name))
		case t.out.exit.Code == 0 || t.out.exit.Cancelled:
			out = append(out, sFaint.Render("✓ "+name))
		default:
			out = append(out, sErr.Render(fmt.Sprintf("✗ %s (%d)", name, t.out.exit.Code)))
		}
	}
	if len(out) > 0 {
		out = append(out, sFaint.Render("B"))
	}
	return out
}

// stopBackground terminates every running background task (on quit).
func (a *App) stopBackground() {
	for _, t := range a.bgs {
		if t.out.exit == nil {
			t.run.Terminate()
		}
	}
}

// --- list and viewer -------------------------------------------------------------------

type bgList struct{ cursor int }

func (l *bgList) update(a *App, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	if len(a.bgs) == 0 {
		a.remove(l)
		return nil
	}
	l.cursor = min(l.cursor, len(a.bgs)-1)
	t := a.bgs[l.cursor]
	switch k.String() {
	case "esc", "q", "B":
		a.remove(l)
	case "j", "down":
		l.cursor = min(l.cursor+1, len(a.bgs)-1)
	case "k", "up":
		l.cursor = max(l.cursor-1, 0)
	case "enter":
		a.remove(l)
		a.push(&bgViewer{t: t})
	case "s", "ctrl+c":
		if t.out.exit == nil {
			t.run.Cancel()
			return a.setStatus("stopping "+bgName(t)+"…", false)
		}
	case "d", "x":
		if t.out.exit == nil {
			return a.setStatus("stop it first (s)", true)
		}
		a.bgs = append(a.bgs[:l.cursor], a.bgs[l.cursor+1:]...)
		if len(a.bgs) == 0 {
			a.remove(l)
		}
	}
	return nil
}

func (l *bgList) view(a *App, w, h int) string {
	bw := min(w-4, 100)
	var lines []string
	for i, t := range a.bgs {
		state := sWarn.Render("running " + fmtDur(time.Since(t.out.started)))
		if e := t.out.exit; e != nil {
			state = sFaint.Render(fmt.Sprintf("exit %d", e.Code))
			if e.Cancelled {
				state = sFaint.Render("stopped")
			}
		}
		row := padRight(truncate(bgName(t), bw-30), bw-28) + " " + state
		if i == l.cursor {
			lines = append(lines, sSelected.Render("▶ "+stripANSI(row)))
		} else {
			lines = append(lines, "  "+row)
		}
	}
	return overlayBox("Background tasks", strings.Join(lines, "\n"), bw)
}

func (l *bgList) hints() string {
	return keyHint("enter", "view output", "s", "stop", "d", "remove finished", "esc", "close")
}

// bgViewer shows a background task's output over the main panel.
type bgViewer struct{ t *bgTask }

func (v *bgViewer) update(a *App, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	o := v.t.out
	switch k.String() {
	case "esc", "q", "enter":
		a.remove(v)
	case "ctrl+c", "s":
		if o.exit == nil {
			v.t.run.Cancel()
			return a.setStatus("stopping "+bgName(v.t)+"…", false)
		}
	case "pgup":
		o.offset = min(o.offset+10, max(len(o.buf.Lines)-1, 0))
	case "pgdown":
		o.offset = max(o.offset-10, 0)
	case "k", "up":
		o.offset = min(o.offset+1, max(len(o.buf.Lines)-1, 0))
	case "j", "down":
		o.offset = max(o.offset-1, 0)
	case "G", "end":
		o.offset = 0
	case "g":
		o.offset = max(len(o.buf.Lines)-1, 0)
	}
	return nil
}

func (v *bgViewer) view(a *App, w, h int) string { return v.t.out.view(a, w, h) }

func (v *bgViewer) hints() string {
	return keyHint("j/k", "scroll", "s / ctrl+c", "stop", "esc", "back (keeps running)")
}
