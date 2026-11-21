package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"lazymage/internal/runner"
)

type logsReconnectMsg struct {
	gen int
}

// logsModel streams `docker compose logs -f` for one service or all of them.
type logsModel struct {
	buf      *Buffer
	opened   bool
	source   string // "" = all services
	runID    int
	run      *runner.Run
	gen      int // bumps on every open/stop so stale reconnects are ignored
	follow   bool
	paused   bool
	pending  []runner.Chunk
	offset   int
	lastData time.Time

	searching bool
	searchIn  textinput.Model
	query     string
	cur       int // buffer index of the current match, -1 none

	filter string

	userStopped  bool
	disconnected bool
	attempts     int
}

func newLogsModel() *logsModel {
	return &logsModel{buf: &Buffer{Max: 10000}, searchIn: newInput("/", "search logs"), cur: -1}
}

func (l *logsModel) sourceLabel() string {
	if l.source == "" {
		return "all services"
	}
	return l.source
}

// open starts streaming from source ("" = all services), replacing any current stream.
func (l *logsModel) open(a *App, source string) tea.Cmd {
	if a.engine == engineStopped {
		return a.setStatus("engine stopped — start it first", true)
	}
	l.stop()
	l.userStopped = false
	l.opened = true
	l.source = source
	l.buf = &Buffer{Max: 10000}
	l.follow = a.cfg.Logs.FollowOnOpen
	l.paused = false
	l.pending = nil
	l.offset = 0
	l.cur = -1
	l.disconnected = false
	l.attempts = 0
	l.gen++
	return l.start(a, a.cfg.Logs.TailLines, "")
}

func (l *logsModel) start(a *App, tail int, since string) tea.Cmd {
	argv := a.builder().Logs(l.source, tail, true, since)
	a.runSeq++
	l.runID = a.runSeq
	if l.attempts == 0 {
		l.buf.Note("$ " + strings.Join(argv, " "))
	}
	r, err := runner.Start(l.runID, a.proj.Root, argv, nil, a.sendAny)
	if err != nil {
		l.buf.Note("could not start log stream: " + err.Error())
		return nil
	}
	l.run = r
	return nil
}

// stop ends the stream at the user's request (switching source, quitting).
func (l *logsModel) stop() {
	l.gen++
	if l.run != nil {
		l.userStopped = true
		l.run.Cancel()
		l.run = nil
	}
}

// terminate ends the stream synchronously (on quit).
func (l *logsModel) terminate() {
	l.gen++
	if l.run != nil {
		l.userStopped = true
		l.run.Terminate()
		l.run = nil
	}
}

func (l *logsModel) onChunk(a *App, c runner.Chunk) {
	l.lastData = time.Now()
	if l.disconnected {
		l.disconnected = false
		l.attempts = 0
		l.buf.Note(fmt.Sprintf("stream resumed at %s (container restarted)", time.Now().Format("15:04:05")))
	}
	if l.paused {
		l.pending = append(l.pending, c)
		return
	}
	before := len(l.buf.Lines) + l.buf.Dropped
	l.buf.Append(c.Data, c.Stderr)
	if !l.follow && l.offset > 0 {
		// Keep the viewport still while new lines arrive.
		l.offset += len(l.buf.Lines) + l.buf.Dropped - before
	}
}

// onExit handles the stream ending. Unless the user stopped it, the followed
// container stopped or restarted: note it and reconnect from that moment.
func (l *logsModel) onExit(a *App, e runner.Exited) tea.Cmd {
	l.run = nil
	if l.userStopped {
		l.userStopped = false
		return nil
	}
	if !l.disconnected {
		l.disconnected = true
		l.buf.Note(fmt.Sprintf("stream ended at %s (exit %d) — container stopped or restarted; reconnecting…", time.Now().Format("15:04:05"), e.Code))
	}
	gen := l.gen
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return logsReconnectMsg{gen: gen} })
}

func (l *logsModel) reconnect(a *App, m logsReconnectMsg) tea.Cmd {
	if m.gen != l.gen || !l.opened || l.run != nil || a.proj == nil {
		return nil
	}
	if a.engine == engineStopped {
		gen := l.gen
		return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return logsReconnectMsg{gen: gen} })
	}
	l.attempts++
	since := l.lastData
	if since.IsZero() {
		since = time.Now().Add(-2 * time.Second)
	}
	return l.start(a, -1, since.UTC().Format(time.RFC3339))
}

// visible returns buffer indexes after the text filter.
func (l *logsModel) visible() []int {
	idx := make([]int, 0, len(l.buf.Lines))
	f := strings.ToLower(l.filter)
	for i, ln := range l.buf.Lines {
		if f == "" || ln.Note || strings.Contains(strings.ToLower(ln.Text), f) {
			idx = append(idx, i)
		}
	}
	return idx
}

func (l *logsModel) matches(idx []int) []int {
	if l.query == "" {
		return nil
	}
	q := strings.ToLower(l.query)
	var m []int
	for p, i := range idx {
		if strings.Contains(strings.ToLower(l.buf.Lines[i].Text), q) {
			m = append(m, p)
		}
	}
	return m
}

// jump moves to the next (dir=1) or previous (dir=-1) match.
func (l *logsModel) jump(dir int, h int) string {
	idx := l.visible()
	ms := l.matches(idx)
	if len(ms) == 0 {
		l.cur = -1
		return "no matches for " + l.query
	}
	curPos := -1
	for p, i := range idx {
		if i == l.cur {
			curPos = p
		}
	}
	target := -1
	if curPos < 0 {
		target = ms[len(ms)-1] // start from the newest
	} else if dir > 0 {
		for _, p := range ms {
			if p > curPos {
				target = p
				break
			}
		}
		if target < 0 {
			target = ms[0]
		}
	} else {
		for k := len(ms) - 1; k >= 0; k-- {
			if ms[k] < curPos {
				target = ms[k]
				break
			}
		}
		if target < 0 {
			target = ms[len(ms)-1]
		}
	}
	l.cur = idx[target]
	l.follow = false
	l.offset = max(len(idx)-1-target-h/2, 0)
	n := 0
	for k, p := range ms {
		if p == target {
			n = k + 1
		}
	}
	return fmt.Sprintf("match %d of %d", n, len(ms))
}

func (l *logsModel) action(a *App, act string) tea.Cmd {
	h := max(a.h-14, 5)
	switch act {
	case aLogsPause:
		l.paused = !l.paused
		if !l.paused {
			for _, c := range l.pending {
				l.buf.Append(c.Data, c.Stderr)
			}
			n := len(l.pending)
			l.pending = nil
			return a.setStatus(fmt.Sprintf("stream resumed (%d buffered chunk(s) added)", n), false)
		}
		return a.setStatus("stream paused — new lines are kept and shown on resume", false)
	case aLogsFollow:
		l.follow = !l.follow
		if l.follow {
			l.offset = 0
			return a.setStatus("follow on", false)
		}
		return a.setStatus("follow off", false)
	case aLogsSearch:
		l.searching = true
		l.searchIn.SetValue(l.query)
		return l.searchIn.Focus()
	case aLogsNext:
		return a.setStatus(l.jump(1, h), false)
	case aLogsPrev:
		return a.setStatus(l.jump(-1, h), false)
	case aLogsFilter:
		a.push(newPrompt("Filter logs", argPrompt{Label: "Show only lines containing (empty clears)", Default: l.filter}, func(v string) tea.Cmd {
			l.filter = v
			l.offset = 0
			if v == "" {
				return a.setStatus("filter cleared", false)
			}
			return a.setStatus("filter: "+v, false)
		}))
	case aLogsClear:
		l.buf.Clear()
		l.offset, l.cur = 0, -1
		return a.setStatus("log buffer cleared", false)
	case aLogsSave:
		def := filepath.Join(a.proj.Root, fmt.Sprintf("lazymage-logs-%s-%s.log", strings.ReplaceAll(l.sourceLabel(), " ", "-"), time.Now().Format("20060102-150405")))
		a.push(newPrompt("Save log buffer", argPrompt{Label: "File path", Default: def, Required: true}, func(v string) tea.Cmd {
			return l.save(a, v)
		}))
	}
	return nil
}

func (l *logsModel) save(a *App, path string) tea.Cmd {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, path[2:])
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(a.proj.Root, path)
	}
	var b strings.Builder
	n := 0
	for _, i := range l.visible() {
		ln := l.buf.Lines[i]
		if ln.Note {
			continue
		}
		b.WriteString(ln.Text + "\n")
		n++
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return a.setStatus("could not save: "+err.Error(), true)
	}
	return a.setStatus(fmt.Sprintf("saved %d lines to %s", n, path), false)
}

// updateInput handles the search line while it has focus.
func (l *logsModel) updateInput(a *App, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if !l.searching {
		return nil, false
	}
	switch msg.String() {
	case "enter":
		l.searching = false
		l.searchIn.Blur()
		l.query = l.searchIn.Value()
		l.cur = -1
		if l.query == "" {
			return a.setStatus("search cleared", false), true
		}
		return a.setStatus(l.jump(-1, max(a.h-14, 5))+" — n / N for next / previous", false), true
	case "esc":
		l.searching = false
		l.searchIn.Blur()
		return nil, true
	}
	var cmd tea.Cmd
	l.searchIn, cmd = l.searchIn.Update(msg)
	return cmd, true
}

func (l *logsModel) update(a *App, msg tea.KeyPressMsg) tea.Cmd {
	n := len(l.visible())
	switch msg.String() {
	case "enter":
		if !l.opened {
			a.push(newServicePicker(a))
		}
	case "k", "up":
		l.offset = min(l.offset+1, max(n-1, 0))
		l.follow = false
	case "j", "down":
		l.offset = max(l.offset-1, 0)
	case "pgup":
		l.offset = min(l.offset+max(a.h-16, 5), max(n-1, 0))
		l.follow = false
	case "pgdown":
		l.offset = max(l.offset-max(a.h-16, 5), 0)
	case "g", "home":
		l.offset = max(n-1, 0)
		l.follow = false
	case "G", "end":
		l.offset = 0
	case "esc":
		if l.query != "" {
			l.query, l.cur = "", -1
			return a.setStatus("search cleared", false)
		}
		if l.filter != "" {
			l.filter = ""
			return a.setStatus("filter cleared", false)
		}
	}
	return nil
}

func (l *logsModel) hints() string {
	if l.searching {
		return keyHint("enter", "search", "esc", "cancel")
	}
	if !l.opened {
		return keyHint("l", "pick a service", "a", "all services", "tab", "next tab", "x", "palette", "?", "help", "q", "quit")
	}
	return keyHint("f", "follow", "space", "pause", "/", "search", "n/N", "next/prev", "l", "source", "j/k", "scroll", "x", "palette", "?", "help")
}

func (l *logsModel) view(a *App, w, h int) string {
	if !l.opened {
		return "\n" + sBold.Render("Logs") + "\n\n" +
			"Pick a log source to start streaming:\n\n" +
			"  " + sBold.Render("l") + "  pick a service…\n" +
			"  " + sBold.Render("a") + "  all services combined\n\n" +
			sFaint.Render(fmt.Sprintf("Shows the last %d lines, then follows. Change this in Settings (S).", a.cfg.Logs.TailLines))
	}
	var st []string
	st = append(st, sBold.Render("Logs: "+l.sourceLabel()))
	switch {
	case l.disconnected:
		st = append(st, sWarn.Render("reconnecting…"))
	case l.paused:
		st = append(st, sWarn.Render(fmt.Sprintf("⏸ paused (%d chunk(s) waiting)", len(l.pending))))
	case l.run != nil:
		st = append(st, sOK.Render("▶ streaming"))
	default:
		st = append(st, sFaint.Render("stopped"))
	}
	if l.follow {
		st = append(st, "follow on")
	} else {
		st = append(st, sFaint.Render("follow off"))
	}
	idx := l.visible()
	if l.filter != "" {
		st = append(st, sNote.Render(fmt.Sprintf("filter %q (%d lines)", l.filter, len(idx))))
	}
	if l.query != "" {
		st = append(st, sNote.Render(fmt.Sprintf("search %q: %d", l.query, len(l.matches(idx)))))
	}
	head := truncateStyled(strings.Join(st, sFaint.Render(" · ")), w)
	bodyH := h - 1
	if l.searching {
		bodyH--
	}
	if l.follow {
		l.offset = 0
	}
	rows := renderLines(l.buf.Lines, idx, l.offset, w, bodyH, l.query, l.cur)
	out := append([]string{head}, rows...)
	if l.searching {
		l.searchIn.SetWidth(w - 4)
		out = append(out, l.searchIn.View())
	}
	return strings.Join(out, "\n")
}
