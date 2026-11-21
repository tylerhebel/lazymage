package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"lazymage/internal/config"
	"lazymage/internal/frontend"
)

// Frontend: generate local-themes.js from the database, npm install, and
// grunt builds. All Node tooling runs in the PHP container.

func (a *App) localThemesPath() string { return filepath.Join(a.proj.Root, frontend.LocalThemesPath) }

// currentThemes reads local-themes.js, falling back to Magento's themes.js
// (what grunt itself does). It returns the entries and the file used.
func (a *App) currentThemes() ([]frontend.Entry, string) {
	for _, p := range []string{a.localThemesPath(), filepath.Join(filepath.Dir(a.localThemesPath()), "themes.js")} {
		if b, err := os.ReadFile(p); err == nil {
			return frontend.ParseLocalThemes(string(b)), p
		}
	}
	return nil, ""
}

// gruntThemePrompt asks for a theme key from local-themes.js; tab cycles.
func (a *App) gruntThemePrompt(title string, run func(theme string) tea.Cmd) tea.Cmd {
	es, _ := a.currentThemes()
	var keys []string
	def := ""
	for _, e := range es {
		if e.Area != "frontend" {
			continue
		}
		keys = append(keys, e.Key)
		if def == "" && !strings.HasPrefix(e.Name, "Magento/") {
			def = e.Key
		}
	}
	if def == "" && len(keys) > 0 {
		def = keys[0]
	}
	if contains(keys, a.lastGruntTheme) {
		def = a.lastGruntTheme // b, enter rebuilds the same theme
	}
	label := "Theme key from local-themes.js"
	if len(keys) == 0 {
		label = "Theme key (no local-themes.js yet — generate it from Frontend first)"
	}
	a.push(newPrompt(title, argPrompt{Label: label, Default: def, Required: true, Choices: keys}, func(t string) tea.Cmd {
		a.lastGruntTheme = t
		return run(t)
	}))
	return nil
}

func (a *App) feDisabled() string {
	if a.run != nil {
		return "a command is already running (ctrl+c cancels it)"
	}
	return ""
}

func (a *App) doFrontend(action string) tea.Cmd {
	b := a.builder()
	switch action {
	case aFEThemes:
		return a.loadThemeCandidates()
	case aFEEditThemes:
		p := a.localThemesPath()
		if _, err := os.Stat(p); err != nil {
			return a.setStatus("no "+frontend.LocalThemesPath+" yet — generate it from the database first", true)
		}
		return a.edit(p, false)
	case aFESetupGrunt:
		argv := b.Bin("setup-grunt")
		a.push(newConfirm("Run bin/setup-grunt?", "docker-magento's one-time grunt setup: switches compose.dev.yaml to your own\n"+
			"package.json / Gruntfile.js / grunt-config.json, restarts app and phpfpm, runs\nnpm install, and builds the default theme. It writes a local-themes.js for the\n"+
			"default store's theme only; regenerate it afterwards to include every theme.\n\n$ "+config.JoinArgs(argv),
			"Run", func() tea.Cmd { return a.startRun(argv, runOpts{kind: config.KindBin}) }))
	case aFENpmInstall:
		return a.startRun(b.Node("npm", "install"), runOpts{kind: kindFrontend})
	case aFEGruntBuild:
		return a.gruntThemePrompt("Grunt: build theme", func(t string) tea.Cmd {
			return a.startRun(b.GruntBuild(t), runOpts{kind: kindFrontend})
		})
	case aFEGruntLess:
		return a.gruntThemePrompt("Grunt: compile LESS", func(t string) tea.Cmd {
			return a.startRun(b.Grunt("less:"+t), runOpts{kind: kindFrontend})
		})
	case aFEGruntWatch:
		return a.startRun(b.Grunt("watch"), runOpts{kind: kindFrontend})
	case aFEGruntFree:
		a.push(newPrompt("Run a grunt command", argPrompt{Label: "grunt arguments (e.g. clean:luma, exec:luma, less:luma, watch)", Required: true}, func(v string) tea.Cmd {
			words, err := config.SplitArgs(v)
			if err != nil || len(words) == 0 {
				return a.setStatus("could not read the arguments", true)
			}
			return a.startRun(b.Grunt(words...), runOpts{kind: kindFrontend})
		}))
	}
	return nil
}

const kindFrontend = "frontend"

// --- local-themes.js generator ---------------------------------------------------------

func (a *App) loadThemeCandidates() tea.Cmd {
	status := a.setStatus("reading themes and locales from the database…", false)
	return tea.Batch(status, a.dbQuery(frontend.Query(a.proj.TablePrefix()), func(out string, err error) tea.Cmd {
		if err != nil {
			return a.setStatus("could not read themes: "+err.Error(), true)
		}
		d := frontend.Parse(out)
		if len(d.Themes) == 0 {
			return a.setStatus("the database has no themes — is Magento installed?", true)
		}
		var current []frontend.Entry
		if b, err := os.ReadFile(a.localThemesPath()); err == nil {
			current = frontend.ParseLocalThemes(string(b))
		}
		a.push(&themePicker{cands: frontend.Candidates(d, current), hadFile: current != nil})
		return nil
	}))
}

type themePicker struct {
	cands   []frontend.Candidate
	cursor  int
	hadFile bool
	err     string
}

func (p *themePicker) update(a *App, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	p.err = ""
	switch k.String() {
	case "esc", "q", "ctrl+c":
		a.remove(p)
	case "j", "down":
		p.cursor = min(p.cursor+1, len(p.cands)-1)
	case "k", "up":
		p.cursor = max(p.cursor-1, 0)
	case "space", "x":
		p.cands[p.cursor].Selected = !p.cands[p.cursor].Selected
	case "enter":
		var es []frontend.Entry
		for _, c := range p.cands {
			if c.Selected {
				es = append(es, c.Entry)
			}
		}
		if len(es) == 0 {
			p.err = "select at least one theme (space)"
			return nil
		}
		a.remove(p)
		return a.confirmWriteThemes(es, p.hadFile)
	}
	return nil
}

func (p *themePicker) view(a *App, w, h int) string {
	bw := min(w-2, 130)
	lines := []string{sFaint.Render("Grunt builds the checked themes. Preselected: themes your stores use, and entries already in the file."), ""}
	maxRows := max(h-10, 3)
	start := 0
	if p.cursor >= maxRows {
		start = p.cursor - maxRows + 1
	}
	for i := start; i < len(p.cands) && i-start < maxRows; i++ {
		c := p.cands[i]
		box := "[ ]"
		if c.Selected {
			box = "[x]"
		}
		row := box + " " + c.Describe()
		if i == p.cursor {
			lines = append(lines, sSelected.Render(truncate("▶ "+row, bw-4)))
		} else {
			lines = append(lines, truncate("  "+row, bw-4))
		}
	}
	if p.err != "" {
		lines = append(lines, "", sErr.Render("! "+p.err))
	}
	return overlayBox("Frontend — themes for "+frontend.LocalThemesPath, strings.Join(lines, "\n"), bw)
}

func (p *themePicker) hints() string {
	return keyHint("space", "toggle", "j/k", "move", "enter", "preview & write", "esc", "cancel")
}

func (a *App) confirmWriteThemes(es []frontend.Entry, hadFile bool) tea.Cmd {
	js := frontend.Render(es, time.Now())
	path := a.localThemesPath()
	rel := a.relPath(path)
	var b strings.Builder
	fmt.Fprintf(&b, "Writes %d theme(s) to %s", len(es), rel)
	if hadFile {
		b.WriteString(" (the current file is kept as local-themes.js.bak)")
	}
	b.WriteString(":\n\n")
	lines := strings.Split(js, "\n")
	const show = 30
	for i, l := range lines {
		if i == show {
			fmt.Fprintf(&b, "… %d more lines\n", len(lines)-show)
			break
		}
		b.WriteString(sFaint.Render(l) + "\n")
	}
	a.push(newConfirm("Write local-themes.js?", b.String(), "Write", func() tea.Cmd {
		if err := writeThemesFile(path, js); err != nil {
			return a.setStatus("could not write "+rel+": "+err.Error(), true)
		}
		return a.setStatus(fmt.Sprintf("wrote %s (%s) — next: Grunt: build theme", rel, strings.Join(frontend.Keys(es), ", ")), false)
	}))
	return nil
}

// writeThemesFile backs up an existing file, then writes atomically.
func writeThemesFile(path, js string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if old, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(path+".bak", old, 0o644); err != nil {
			return err
		}
	}
	tmp := path + ".lazymage-tmp"
	if err := os.WriteFile(tmp, []byte(js), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
