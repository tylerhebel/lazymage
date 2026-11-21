package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"lazymage/internal/docker"
	"lazymage/internal/runner"
)

// outputView shows a running (or finished) command. It takes over the main
// panel while the command runs and stays until dismissed.
type outputView struct {
	runID     int
	title     string
	opts      runOpts
	buf       *Buffer
	exit      *runner.Exited
	started   time.Time
	stdin     textinput.Model
	offset    int
	lifecycle bool
	progress  []docker.Container
	run       *runner.Run // the process, while it runs (foreground or background)
}

func (o *outputView) running() bool { return o.run != nil && o.exit == nil }

func newOutputView(id int, title string, o runOpts) *outputView {
	in := newInput("stdin ▸ ", "type here to answer a prompt, enter to send")
	return &outputView{runID: id, title: title, opts: o, buf: &Buffer{Max: 20000}, started: time.Now(), stdin: in, lifecycle: o.lifecycle}
}

func (o *outputView) focus() tea.Cmd { return o.stdin.Focus() }

func (o *outputView) update(a *App, msg tea.Msg) (tea.Cmd, bool) {
	running := o.running()
	if p, ok := msg.(tea.PasteMsg); ok && running {
		var cmd tea.Cmd
		o.stdin, cmd = o.stdin.Update(p)
		return cmd, true
	}
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil, false
	}
	switch k.String() {
	case "pgup":
		o.offset = min(o.offset+10, max(len(o.buf.Lines)-1, 0))
		return nil, true
	case "pgdown":
		o.offset = max(o.offset-10, 0)
		return nil, true
	case "up":
		o.offset = min(o.offset+1, max(len(o.buf.Lines)-1, 0))
		return nil, true
	case "down":
		o.offset = max(o.offset-1, 0)
		return nil, true
	case "end":
		o.offset = 0
		return nil, true
	}
	if running {
		switch k.String() {
		case "ctrl+c":
			if a.run == o.run {
				return a.do(aCancelRun), true
			}
			o.run.Cancel()
			return nil, true
		case "ctrl+b":
			if a.run == o.run {
				return a.toBackground(), true
			}
			return nil, true
		case "esc":
			a.showO = false
			return a.setStatus("command still running — v shows it again, ctrl+c cancels, ctrl+b frees the screen for other commands", false), true
		case "ctrl+p":
			return nil, false
		case "enter":
			line := o.stdin.Value()
			o.stdin.SetValue("")
			o.buf.Note("› " + line)
			if err := o.run.WriteStdin(line + "\n"); err != nil {
				o.buf.Note("could not send input: " + err.Error())
			}
			o.offset = 0
			return nil, true
		case "ctrl+d":
			o.run.CloseStdin()
			o.buf.Note("› (EOF)")
			return nil, true
		}
		var cmd tea.Cmd
		o.stdin, cmd = o.stdin.Update(k)
		return cmd, true
	}
	switch k.String() {
	case "esc", "enter":
		a.showO = false
		return nil, true
	case "k":
		o.offset = min(o.offset+1, max(len(o.buf.Lines)-1, 0))
		return nil, true
	case "j":
		o.offset = max(o.offset-1, 0)
		return nil, true
	case "g":
		o.offset = max(len(o.buf.Lines)-1, 0)
		return nil, true
	case "G":
		o.offset = 0
		return nil, true
	}
	return nil, false
}

func (o *outputView) view(a *App, w, h int) string {
	running := o.running()
	var head []string
	status := ""
	switch {
	case running:
		status = sWarn.Render(fmt.Sprintf("● running · %s", fmtDur(time.Since(o.started))))
		if o.opts.meter != nil {
			status = sFaint.Render(o.opts.meter()) + "  " + status
		}
	case o.exit == nil:
		status = sFaint.Render("not started")
	case o.exit.Cancelled:
		status = sWarn.Render(fmt.Sprintf("■ cancelled · exit %d", o.exit.Code))
	case o.exit.Code == 0 && o.exit.Err == nil:
		status = sOK.Render(fmt.Sprintf("✓ exit 0 · %s", fmtDur(o.exit.Duration)))
	default:
		status = sErr.Render(fmt.Sprintf("✗ exit %d · %s", o.exit.Code, fmtDur(o.exit.Duration)))
	}
	head = append(head, truncateStyled(sBold.Render("$ "+o.title), w-lenPlain(status)-2)+"  "+status)

	if o.lifecycle && len(o.progress) > 0 {
		head = append(head, progressRows(o.progress, a.services, w)...)
	}
	head = append(head, sFaint.Render(strings.Repeat("─", w)))

	footer := 0
	running = running && !o.opts.noStdin
	if running {
		footer = 1
	}
	bodyH := h - len(head) - footer
	idx := make([]int, len(o.buf.Lines))
	for i := range idx {
		idx[i] = i
	}
	rows := renderLines(o.buf.Lines, idx, o.offset, w, bodyH, "", -1)
	out := append(head, rows...)
	if running {
		o.stdin.SetWidth(w - 10)
		out = append(out, o.stdin.View())
	}
	return strings.Join(out, "\n")
}

// progressRows renders per-service state during start / stop / restart.
// This is transient progress for the running action, not a standing panel.
func progressRows(cs []docker.Container, services []string, w int) []string {
	byS := map[string]docker.Container{}
	for _, c := range cs {
		byS[c.Service] = c
	}
	names := services
	if len(names) == 0 {
		for _, c := range cs {
			names = append(names, c.Service)
		}
	}
	var cells []string
	for _, n := range names {
		c, ok := byS[n]
		var cell string
		switch {
		case !ok:
			cell = sFaint.Render("○ " + n + " not created")
		case docker.Up(c):
			cell = sOK.Render("● " + n + " up")
		case c.Health == "starting":
			cell = sWarn.Render("◐ " + n + " starting")
		case c.State == "running":
			cell = sWarn.Render("◐ " + n + " " + c.Health)
		default:
			cell = sErr.Render("○ " + n + " " + c.State)
		}
		cells = append(cells, cell)
	}
	// Lay cells out in columns.
	colW := 28
	perRow := max(w/colW, 1)
	var rows []string
	for i := 0; i < len(cells); i += perRow {
		var r []string
		for j := i; j < min(i+perRow, len(cells)); j++ {
			r = append(r, padRight(cells[j], colW))
		}
		rows = append(rows, strings.Join(r, ""))
	}
	return rows
}

func lenPlain(s string) int { return len([]rune(stripANSI(s))) }
