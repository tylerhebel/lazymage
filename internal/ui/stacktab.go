package ui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"lazymage/internal/config"
	"lazymage/internal/docker"
	"lazymage/internal/project"
)

// Tabs. Logs has no tab of its own any more: it opens from the palette and
// shows as a temporary tab while it is the current view.
const (
	tabStack    = 0
	tabMagento  = 1
	tabComposer = 2
	tabGit      = 3
	tabLogs     = 4
	numTabs     = 4 // tabs cycled with tab / 1–4
)

// stackTab is the first tab: a checklist of what a working environment
// needs (engine, containers, Magento, storefront) and the tasks that get it
// there — start, restart, stop, restart one service.
type stackTab struct {
	cursor int
}

type stackTask struct {
	action, label string
}

var stackTasks = []stackTask{
	{aStackStart, "Start environment"},
	{aStackRestart, "Restart environment"},
	{aStackRestartSvc, "Restart one service…"},
	{aStackStop, "Stop environment"},
	{aEngineStart, "Start container engine"},
	{aRefresh, "Re-check status"},
}

func (s *stackTab) update(a *App, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "j", "down":
		s.cursor = min(s.cursor+1, len(stackTasks)-1)
	case "k", "up":
		s.cursor = max(s.cursor-1, 0)
	case "enter":
		return a.do(stackTasks[s.cursor].action)
	}
	return nil
}

func (s *stackTab) hints() string {
	return keyHint("j/k", "move", "enter", "run", "x", "palette", "?", "help", "q", "quit")
}

// downServices names the services that are not up.
func (a *App) downServices() []string {
	up := map[string]bool{}
	for _, c := range a.containers {
		if docker.Up(c) {
			up[c.Service] = true
		}
	}
	var down []string
	for _, svc := range a.services {
		if !up[svc] {
			down = append(down, svc)
		}
	}
	sort.Strings(down)
	return down
}

func (s *stackTab) checklist(a *App, w int) []string {
	step := func(n int, ok, warn bool, title, detail string) string {
		mark := sOK.Render("✓")
		switch {
		case warn:
			mark = sWarn.Render("◐")
		case !ok:
			mark = sErr.Render("○")
		}
		return truncateStyled(fmt.Sprintf(" %s %d. %-14s %s", mark, n, title, detail), w)
	}
	var out []string

	switch a.engine {
	case engineRunning:
		out = append(out, step(1, true, false, "Engine", "running — "+runtimeLabel(a.cfg.Runtime)))
	case engineStopped:
		out = append(out, step(1, false, false, "Engine", "stopped — Start container engine"))
	default:
		out = append(out, step(1, false, true, "Engine", "checking…"))
	}

	h := a.health
	switch {
	case a.engine != engineRunning:
		out = append(out, step(2, false, false, "Containers", sFaint.Render("waiting for the engine")))
	case h.Health == docker.HealthAllUp:
		out = append(out, step(2, true, false, "Containers", fmt.Sprintf("all up %d/%d", h.Up, h.Total)))
	case h.Total > 0 && h.Up == 0:
		out = append(out, step(2, false, false, "Containers", fmt.Sprintf("none up 0/%d — Start environment", h.Total)))
	case h.Total > 0:
		detail := fmt.Sprintf("%d/%d up", h.Up, h.Total)
		if h.Starting > 0 {
			detail += fmt.Sprintf(", %d starting", h.Starting)
		}
		if down := a.downServices(); len(down) > 0 {
			detail += " — not up: " + strings.Join(down, ", ")
		}
		out = append(out, step(2, false, true, "Containers", detail))
	default:
		out = append(out, step(2, false, true, "Containers", "unknown"))
	}

	mage := project.Or(a.facts.MagentoVersion)
	if a.facts.DeployMode != "" {
		mage += " · " + a.facts.DeployMode + " mode"
	}
	out = append(out, step(3, a.facts.MagentoVersion != "", a.facts.MagentoVersion == "", "Magento", mage))

	if u := a.storefrontURL(); u != "" {
		out = append(out, step(4, true, false, "Storefront", u+sFaint.Render("  (o opens it, O the admin)")))
	} else {
		out = append(out, step(4, false, true, "Storefront", "domain unknown — set it with bin/setup-domain"))
	}
	return out
}

func (s *stackTab) view(a *App, w, h int) string {
	out := []string{sBold.Render("Environment") + sFaint.Render(" — "+a.proj.Name), ""}
	out = append(out, s.checklist(a, w)...)
	out = append(out, a.opsChecklist(w)...)
	out = append(out, "", sFaint.Render(strings.Repeat("─", w)))
	b := a.builder()
	for i, t := range stackTasks {
		cmd := ""
		switch t.action {
		case aStackStart, aStackRestart, aStackStop:
			cmd = config.JoinArgs(b.Lifecycle(strings.TrimPrefix(t.action, "stack.")))
		case aStackRestartSvc:
			cmd = config.JoinArgs(b.RestartService("<service>"))
		case aEngineStart:
			cmd = engineStartDesc(a.cfg.Runtime)
		case aRefresh:
			cmd = "docker info · compose ps · bin/magento --version"
		}
		label := t.label
		if sc := shortcutFor(t.action); sc != "" {
			label += sFaint.Render(" (" + sc + ")")
		}
		line := padRight(label, 34) + " "
		if reason := a.disabledReason(t.action); reason != "" {
			line += sFaint.Render("— " + reason)
		} else {
			line += sFaint.Render(cmd)
		}
		if i == s.cursor {
			out = append(out, sSelected.Render(truncate("▶ "+stripANSI(line), w)))
		} else {
			out = append(out, truncateStyled("  "+line, w))
		}
	}
	for len(out) < h {
		out = append(out, "")
	}
	return strings.Join(out[:min(len(out), h)], "\n")
}
