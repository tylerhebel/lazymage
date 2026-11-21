package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"lazymage/internal/config"
	"lazymage/internal/docker"
	"lazymage/internal/project"
)

// Multi-project switching: every project lazymage opens is remembered in
// the config (with its history, custom commands and overrides); P lists
// them, most recent first, and switches without restarting.

type projectPicker struct {
	in     textinput.Model
	all    []config.KnownProject
	cursor int
	err    string
}

func newProjectPicker(a *App) *projectPicker {
	in := newInput("▸ ", "filter, or type the path to another project")
	in.Focus()
	return &projectPicker{in: in, all: a.cfg.KnownProjects()}
}

func (p *projectPicker) options() []config.KnownProject {
	q := strings.TrimSpace(p.in.Value())
	if q == "" {
		return p.all
	}
	var out []config.KnownProject
	for _, k := range p.all {
		if fuzzyScore(q, k.Root) > 0 {
			out = append(out, k)
		}
	}
	return out
}

func (p *projectPicker) update(a *App, msg tea.Msg) tea.Cmd {
	opts := p.options()
	if k, ok := msg.(tea.KeyPressMsg); ok {
		p.err = ""
		switch k.String() {
		case "esc", "ctrl+c":
			a.remove(p)
			return nil
		case "up", "ctrl+k":
			p.cursor = max(p.cursor-1, 0)
			return nil
		case "down", "ctrl+j":
			p.cursor = min(p.cursor+1, max(len(opts)-1, 0))
			return nil
		case "enter":
			dir := ""
			if p.cursor < len(opts) {
				dir = opts[p.cursor].Root
			} else {
				dir = expandHome(strings.TrimSpace(p.in.Value()))
			}
			if dir == "" {
				return nil
			}
			if err := a.switchProject(dir); err != "" {
				p.err = err
				return nil
			}
			a.remove(p)
			return a.afterSwitch()
		}
	}
	var cmd tea.Cmd
	p.in, cmd = p.in.Update(msg)
	p.cursor = 0
	return cmd
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func (p *projectPicker) view(a *App, w, h int) string {
	bw := min(w-4, 100)
	p.in.SetWidth(bw - 8)
	opts := p.options()
	lines := []string{p.in.View(), ""}
	if len(opts) == 0 {
		lines = append(lines, sFaint.Render("no known project matches — enter opens the typed path"))
	}
	maxRows := max(h-9, 3)
	start := 0
	if p.cursor >= maxRows {
		start = p.cursor - maxRows + 1
	}
	for i := start; i < len(opts) && i-start < maxRows; i++ {
		k := opts[i]
		meta := "never opened"
		if !k.LastOpened.IsZero() {
			meta = "opened " + k.LastOpened.Local().Format("2006-01-02 15:04")
		}
		if k.Missing {
			meta = "missing"
		}
		if a.proj != nil && k.Root == a.proj.Root {
			meta = "current"
		}
		row := padRight(filepath.Base(k.Root), 22) + " " + padRight(truncate(k.Root, bw-48), bw-48) + " " + meta
		switch {
		case i == p.cursor:
			lines = append(lines, sSelected.Render(truncate("▶ "+row, bw-4)))
		case k.Missing:
			lines = append(lines, sFaint.Render(truncate("  "+row, bw-4)))
		default:
			lines = append(lines, truncate("  "+row, bw-4))
		}
	}
	if p.err != "" {
		lines = append(lines, "", sErr.Render("! "+p.err))
	}
	return overlayBox("Switch project", strings.Join(lines, "\n"), bw)
}

func (p *projectPicker) hints() string {
	return keyHint("type", "filter / path", "↑/↓", "move", "enter", "switch", "esc", "close")
}

func (a *App) switchDisabled() string {
	if a.run != nil {
		return "a command is still running — wait for it or cancel it (ctrl+c) first"
	}
	if a.bgRunning() {
		return "a background task is running — stop it first (palette: Background tasks)"
	}
	return ""
}

// switchProject re-targets the app at another project ("" = ok, else why not).
func (a *App) switchProject(dir string) string {
	if r := a.switchDisabled(); r != "" {
		return r
	}
	p, ok := project.FindRoot(dir)
	if !ok {
		return dir + " is not a docker-magento project (needs a compose file, bin/magento and src/)"
	}
	if a.proj != nil && p.Root == a.proj.Root {
		return "already in " + p.Name
	}
	a.logs.terminate()
	a.logs = newLogsModel()
	a.out, a.showO, a.tab = nil, false, tabStack
	a.containers, a.services, a.health, a.psErr = nil, nil, docker.Summary{}, ""
	a.lastCmd, a.lastExit, a.lastGruntTheme = "", "", ""
	a.git = nil
	a.startDir = p.Root
	a.proj = p
	a.facts = p.StaticFacts()
	a.loadFactsCache()
	a.mage.refreshItems(a)
	a.comp.refreshItems(a)
	a.cfg.TouchProject(p.Root, time.Now())
	if _, err := os.Stat(a.cfgPath); err == nil {
		a.saveConfig()
	}
	return ""
}

func (a *App) afterSwitch() tea.Cmd {
	cmds := []tea.Cmd{a.probeCmd(), a.servicesCmd(), a.setStatus(fmt.Sprintf("switched to %s (%s)", a.proj.Name, a.proj.Root), false)}
	return tea.Batch(append(cmds, a.factsCmds(false)...)...)
}
