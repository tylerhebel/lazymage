package ui

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"lazymage/internal/gitstat"
)

// gitView is the Git tab: the state of the repository holding src/, read
// with git on the host. Read-only: diffs open in the output view, files in
// the editor, and lazygit / tig (if installed) take over for anything more.
type gitView struct {
	root   string // project root this state belongs to
	st     gitstat.Status
	err    string
	cursor int
	busy   bool
	loaded bool
}

type gitStatusMsg struct {
	root string
	st   gitstat.Status
	err  error
}

func (a *App) gitRefresh() tea.Cmd {
	if a.proj == nil {
		return nil
	}
	if a.git == nil || a.git.root != a.proj.Root {
		a.git = &gitView{root: a.proj.Root}
	}
	if a.git.busy {
		return nil
	}
	a.git.busy = true
	root := a.proj.Root
	return func() tea.Msg {
		repo, err := gitstat.FindRepo(root)
		if err != nil {
			return gitStatusMsg{root: root, err: err}
		}
		st, err := gitstat.Read(repo)
		return gitStatusMsg{root: root, st: st, err: err}
	}
}

func (a *App) onGitStatus(m gitStatusMsg) tea.Cmd {
	if a.git == nil || a.git.root != m.root {
		return nil // switched project meanwhile
	}
	g := a.git
	g.busy, g.loaded = false, true
	g.err = ""
	if m.err != nil {
		g.err = m.err.Error()
		return nil
	}
	g.st = m.st
	g.cursor = min(g.cursor, max(len(g.st.Files)-1, 0))
	return nil
}

func (g *gitView) selected() (gitstat.File, bool) {
	if g == nil || g.cursor >= len(g.st.Files) {
		return gitstat.File{}, false
	}
	return g.st.Files[g.cursor], true
}

func (a *App) gitUpdate(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok || a.git == nil {
		return nil
	}
	g := a.git
	switch k.String() {
	case "j", "down":
		g.cursor = min(g.cursor+1, max(len(g.st.Files)-1, 0))
	case "k", "up":
		g.cursor = max(g.cursor-1, 0)
	case "enter", "d":
		f, ok := g.selected()
		if !ok {
			return nil
		}
		if f.X == '?' {
			return a.setStatus(f.Path+" is untracked — e opens it in your editor", false)
		}
		return a.startRun(gitstat.DiffArgv(g.st.Repo, f), runOpts{kind: "git", noStdin: true})
	case "e":
		if f, ok := g.selected(); ok {
			return a.edit(filepath.Join(g.st.Repo, f.Path), false)
		}
	case "L":
		return a.gitTool()
	}
	return nil
}

// gitTool hands the terminal to lazygit (or tig) in the repository.
func (a *App) gitTool() tea.Cmd {
	if a.git == nil || a.git.st.Repo == "" {
		return a.setStatus("no git repository loaded", true)
	}
	repo := a.git.st.Repo
	for _, tool := range [][]string{{"lazygit", "-p", repo}, {"tig"}} {
		if _, err := exec.LookPath(tool[0]); err != nil {
			continue
		}
		cmd := exec.Command(tool[0], tool[1:]...)
		cmd.Dir = repo
		return tea.ExecProcess(cmd, func(err error) tea.Msg {
			if _, ok := err.(*exec.ExitError); ok {
				err = nil
			}
			return shellDoneMsg{err: err, what: tool[0]}
		})
	}
	return a.setStatus("install lazygit or tig to manage the repository from here", true)
}

func (a *App) gitView(w, h int) string {
	g := a.git
	if g == nil || !g.loaded {
		return sFaint.Render("reading the git repository…")
	}
	if g.err != "" {
		return sBold.Render("Git") + "\n\n" + sWarn.Render(g.err) + "\n\n" + sFaint.Render("r re-checks")
	}
	s := g.st
	branch := s.Branch
	if branch == "" {
		branch = "detached at " + s.Head
	}
	head := sBold.Render("Git") + sFaint.Render(" — ") + sAccent.Render(branch)
	if s.Upstream != "" {
		sync := "up to date with " + s.Upstream
		if s.Ahead > 0 || s.Behind > 0 {
			sync = fmt.Sprintf("↑%d ↓%d vs %s", s.Ahead, s.Behind, s.Upstream)
		}
		head += sFaint.Render(" · " + sync)
	} else if s.Branch != "" {
		head += sFaint.Render(" · no upstream")
	}
	out := []string{truncateStyled(head, w), sFaint.Render(truncate(s.Repo, w)), ""}

	if s.Clean() {
		out = append(out, sOK.Render("✓ working tree clean"))
	} else {
		out = append(out, sFaint.Render(fmt.Sprintf("%d changed file(s)", len(s.Files))))
	}
	commitRows := min(len(s.Commits), 8)
	listH := max(h-len(out)-commitRows-3, 3)
	start := 0
	if g.cursor >= listH {
		start = g.cursor - listH + 1
	}
	for i := start; i < len(s.Files) && i-start < listH; i++ {
		f := s.Files[i]
		label := f.Label()
		style := sWarn
		switch {
		case label == "untracked":
			style = sFaint
		case label == "conflict":
			style = sErr
		case f.Staged():
			style = sOK
		}
		path := f.Path
		if f.From != "" {
			path = f.From + " → " + f.Path
		}
		row := padRight(style.Render(label), 26) + " " + path
		if i == g.cursor {
			out = append(out, sSelected.Render(truncate("▶ "+stripANSI(row), w)))
		} else {
			out = append(out, truncateStyled("  "+row, w))
		}
	}
	if commitRows > 0 {
		out = append(out, "", sFaint.Render("Recent commits"))
		for _, c := range s.Commits[:commitRows] {
			out = append(out, truncate("  "+c, w))
		}
	}
	for len(out) < h {
		out = append(out, "")
	}
	return strings.Join(out[:min(len(out), h)], "\n")
}

func (a *App) gitHints() string {
	return keyHint("j/k", "move", "enter", "diff", "e", "edit", "L", "lazygit / tig", "r", "refresh", "x", "palette")
}
