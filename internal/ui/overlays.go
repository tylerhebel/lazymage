package ui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"lazymage/internal/config"
)

// --- confirm ------------------------------------------------------------------------

type confirmOverlay struct {
	title, body, yes string
	onYes            func() tea.Cmd
	sel              int // 0 yes, 1 no — default is "no" for safety
}

func newConfirm(title, body, yes string, onYes func() tea.Cmd) *confirmOverlay {
	return &confirmOverlay{title: title, body: body, yes: yes, onYes: onYes, sel: 1}
}

func (c *confirmOverlay) update(a *App, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "left", "h", "right", "l", "tab", "shift+tab":
		c.sel = 1 - c.sel
	case "y":
		a.remove(c)
		return c.onYes()
	case "n", "esc", "q":
		a.remove(c)
	case "enter":
		a.remove(c)
		if c.sel == 0 {
			return c.onYes()
		}
	case "ctrl+c":
		a.remove(c)
	}
	return nil
}

func (c *confirmOverlay) view(a *App, w, h int) string {
	bw := min(w-4, 90)
	var lines []string
	for _, l := range strings.Split(c.body, "\n") {
		lines = append(lines, wrapStyledPlain(l, bw-4)...)
	}
	yes, no := " "+c.yes+" ", " Cancel "
	if c.sel == 0 {
		yes = sSelected.Render("▶" + yes)
		no = " " + no
	} else {
		no = sSelected.Render("▶" + no)
		yes = " " + yes
	}
	body := strings.Join(lines, "\n") + "\n\n" + yes + "   " + no
	return overlayBox(c.title, body, bw)
}

func (c *confirmOverlay) hints() string {
	return keyHint("y", c.yes, "n/esc", "cancel", "←/→", "choose", "enter", "confirm choice")
}

// wrapStyledPlain wraps a line only if it has no styling; styled lines are kept whole.
func wrapStyledPlain(s string, w int) []string {
	if stripANSI(s) != s {
		return []string{s}
	}
	return wrap(s, w)
}

// --- single-value prompt ---------------------------------------------------------------

type promptOverlay struct {
	title    string
	p        argPrompt
	in       textinput.Model
	onSubmit func(string) tea.Cmd
	err      string
}

func newPrompt(title string, p argPrompt, onSubmit func(string) tea.Cmd) *promptOverlay {
	in := newInput("▸ ", p.Default)
	in.SetValue(p.Default)
	in.CursorEnd()
	in.Focus()
	return &promptOverlay{title: title, p: p, in: in, onSubmit: onSubmit}
}

func (o *promptOverlay) update(a *App, msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc", "ctrl+c":
			a.remove(o)
			return a.setStatus("cancelled", false)
		case "tab":
			if len(o.p.Choices) > 0 {
				cur := o.in.Value()
				next := o.p.Choices[0]
				for i, c := range o.p.Choices {
					if c == cur {
						next = o.p.Choices[(i+1)%len(o.p.Choices)]
					}
				}
				o.in.SetValue(next)
				o.in.CursorEnd()
			}
			return nil
		case "enter":
			v := strings.TrimSpace(o.in.Value())
			if o.p.Required && v == "" {
				o.err = "a value is required"
				return nil
			}
			if len(o.p.Choices) > 0 && v != "" && !contains(o.p.Choices, v) {
				o.err = "choose one of: " + strings.Join(o.p.Choices, ", ")
				return nil
			}
			a.remove(o)
			return o.onSubmit(v)
		}
	}
	var cmd tea.Cmd
	o.in, cmd = o.in.Update(msg)
	o.err = ""
	return cmd
}

func (o *promptOverlay) view(a *App, w, h int) string {
	bw := min(w-4, 90)
	o.in.SetWidth(bw - 8)
	body := o.p.Label
	if o.p.Required {
		body += sFaint.Render(" (required)")
	}
	body += "\n\n" + o.in.View()
	if len(o.p.Choices) > 0 {
		body += "\n" + sFaint.Render("tab cycles: "+strings.Join(o.p.Choices, " · "))
	}
	if o.err != "" {
		body += "\n" + sErr.Render("! "+o.err)
	}
	return overlayBox(o.title, body, bw)
}

func (o *promptOverlay) hints() string { return keyHint("enter", "ok", "esc", "cancel") }

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// --- service picker (log source) ----------------------------------------------------------

type servicePicker struct {
	in      textinput.Model
	cursor  int
	restart bool // pick a service to restart instead of a log source
}

func newServicePicker(a *App) *servicePicker {
	in := newInput("▸ ", "type to search services")
	in.Focus()
	return &servicePicker{in: in}
}

func (s *servicePicker) options(a *App) []string {
	opts := []string{""}
	if s.restart {
		opts = nil
	}
	q := strings.ToLower(s.in.Value())
	var scored []string
	for _, svc := range a.services {
		if q == "" || fuzzyScore(q, svc) > 0 {
			scored = append(scored, svc)
		}
	}
	if q != "" {
		sort.SliceStable(scored, func(i, j int) bool { return fuzzyScore(q, scored[i]) > fuzzyScore(q, scored[j]) })
		if !s.restart && fuzzyScore(q, "all services") == 0 {
			opts = nil
		}
	}
	return append(opts, scored...)
}

func (s *servicePicker) update(a *App, msg tea.Msg) tea.Cmd {
	opts := s.options(a)
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc", "ctrl+c":
			a.remove(s)
			return nil
		case "up", "ctrl+k":
			s.cursor = max(s.cursor-1, 0)
			return nil
		case "down", "ctrl+j":
			s.cursor = min(s.cursor+1, max(len(opts)-1, 0))
			return nil
		case "enter":
			a.remove(s)
			if s.cursor < len(opts) && s.restart {
				return a.startRun(a.builder().RestartService(opts[s.cursor]), runOpts{kind: "lifecycle", lifecycle: true})
			}
			if s.cursor < len(opts) {
				return a.logs.open(a, opts[s.cursor])
			}
			return nil
		}
	}
	var cmd tea.Cmd
	s.in, cmd = s.in.Update(msg)
	s.cursor = 0
	return cmd
}

func (s *servicePicker) view(a *App, w, h int) string {
	bw := min(w-4, 70)
	s.in.SetWidth(bw - 8)
	opts := s.options(a)
	lines := []string{s.in.View(), ""}
	if len(a.services) == 0 {
		lines = append(lines, sFaint.Render("(service list not loaded yet — all services is still available)"))
	}
	maxRows := max(h-8, 3)
	start := 0
	if s.cursor >= maxRows {
		start = s.cursor - maxRows + 1
	}
	for i := start; i < len(opts) && i-start < maxRows; i++ {
		label := opts[i]
		if label == "" {
			label = "All services (combined)"
		} else {
			label = a.serviceState(label)
		}
		if i == s.cursor {
			lines = append(lines, sSelected.Render(padRight("▶ "+stripANSI(label), bw-4)))
		} else {
			lines = append(lines, "  "+label)
		}
	}
	title := "Logs — pick a source"
	if s.restart {
		title = "Restart a service"
	}
	return overlayBox(title, strings.Join(lines, "\n"), bw)
}

func (s *servicePicker) hints() string {
	return keyHint("type", "search", "↑/↓", "move", "enter", "follow logs", "esc", "close")
}

// serviceState labels a service with its container state, in the picker only.
func (a *App) serviceState(svc string) string {
	for _, c := range a.containers {
		if c.Service == svc {
			state := c.State
			if c.Health != "" {
				state += ", " + c.Health
			}
			return fmt.Sprintf("%s %s", svc, sFaint.Render("("+state+")"))
		}
	}
	return svc + sFaint.Render(" (no container)")
}

// --- help -------------------------------------------------------------------------------------

type helpOverlay struct {
	scroll int
}

func newHelp(a *App) *helpOverlay { return &helpOverlay{} }

func (o *helpOverlay) lines(a *App, w int) []string {
	var out []string
	for _, sec := range helpSections(a.cfg.EffectiveCommands(a.root())) {
		out = append(out, sAccent.Render(sec.Title))
		for _, r := range sec.Rows {
			out = append(out, "  "+padRight(sBold.Render(r[0]), 22)+" "+truncate(r[1], w-30))
		}
		out = append(out, "")
	}
	out = append(out, sFaint.Render("Every built-in action is also in the palette (x). Press S here to re-run setup."))
	return out
}

func (o *helpOverlay) update(a *App, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "esc", "?", "q", "ctrl+c":
		a.remove(o)
	case "j", "down":
		o.scroll++
	case "k", "up":
		o.scroll = max(o.scroll-1, 0)
	case "pgdown", "space":
		o.scroll += 10
	case "pgup":
		o.scroll = max(o.scroll-10, 0)
	case "S":
		a.remove(o)
		return a.do(aSetup)
	}
	return nil
}

func (o *helpOverlay) view(a *App, w, h int) string {
	bw := min(w-2, 110)
	all := o.lines(a, bw-4)
	rows := max(h-4, 3)
	o.scroll = min(o.scroll, max(len(all)-rows, 0))
	vis := all[o.scroll:min(o.scroll+rows, len(all))]
	title := fmt.Sprintf("Help — keybindings (%d–%d of %d)", o.scroll+1, o.scroll+len(vis), len(all))
	return overlayBox(title, strings.Join(vis, "\n"), bw)
}

func (o *helpOverlay) hints() string {
	return keyHint("j/k", "scroll", "S", "re-run setup", "esc", "close")
}

// --- message box --------------------------------------------------------------------------------

type messageOverlay struct{ title, body string }

func (m *messageOverlay) update(a *App, msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc", "enter", "q", "ctrl+c":
			a.remove(m)
		}
	}
	return nil
}

func (m *messageOverlay) view(a *App, w, h int) string {
	return overlayBox(m.title, m.body, min(w-4, 90))
}

var _ = config.Redacted
