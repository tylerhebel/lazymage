package ui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"lazymage/internal/config"
)

// settingsOverlay edits every setup choice in place (section 8.8).
type settingsOverlay struct {
	cursor  int
	editing bool
	in      textinput.Model
	err     string
}

type settingRow struct {
	id, label string
}

var settingRows = []settingRow{
	{"runtime", "Container runtime"},
	{"mode", "Integration mode"},
	{"shell", "Container shell"},
	{"editor", "Editor"},
	{"browser", "Browser"},
	{"tail", "Log lines on open"},
	{"follow", "Follow logs on open"},
	{"refresh", "Health refresh (seconds)"},
	{"updates", "Check for updates daily"},
	{"favorites", "Favorite Magento commands"},
	{"manage", "Manage custom commands…"},
	{"setup", "Re-run setup…"},
	{"config", "Open config file in editor"},
}

func newSettings(a *App) *settingsOverlay {
	return &settingsOverlay{in: newInput("▸ ", "")}
}

func (s *settingsOverlay) value(a *App, id string) string {
	c := a.cfg
	switch id {
	case "runtime":
		return runtimeLabel(c.Runtime) + sFaint.Render("  ←/→")
	case "mode":
		return modeLabel(c.IntegrationMode) + sFaint.Render("  ←/→")
	case "shell":
		return c.Shell
	case "editor":
		if c.Editor == "" {
			return sFaint.Render("($EDITOR, then vi) → " + strings.Join(a.editorArgv(), " "))
		}
		return c.Editor
	case "browser":
		return c.Browser
	case "tail":
		return strconv.Itoa(c.Logs.TailLines)
	case "follow":
		if c.Logs.FollowOnOpen {
			return "on"
		}
		return "off"
	case "refresh":
		return strconv.Itoa(c.RefreshSeconds)
	case "updates":
		if c.CheckUpdates() {
			return "on" + sFaint.Render("  (U updates now)")
		}
		return "off" + sFaint.Render("  (U still updates on demand)")
	case "favorites":
		return fmt.Sprintf("%d selected", len(c.Favorites))
	case "config":
		return a.cfgPath
	}
	return ""
}

func modeLabel(m string) string {
	switch m {
	case config.ModeHybrid:
		return "Hybrid (recommended)"
	case config.ModeBinOnly:
		return "bin scripts only"
	case config.ModeDockerOnly:
		return "Direct Docker only"
	}
	return m
}

func cycle(xs []string, cur string, dir int) string {
	i := indexOf(xs, cur)
	if i < 0 {
		return xs[0]
	}
	return xs[(i+dir+len(xs))%len(xs)]
}

func (s *settingsOverlay) update(a *App, msg tea.Msg) tea.Cmd {
	id := settingRows[s.cursor].id
	if s.editing {
		if k, ok := msg.(tea.KeyPressMsg); ok {
			switch k.String() {
			case "esc":
				s.editing, s.err = false, ""
				s.in.Blur()
				return nil
			case "enter":
				return s.commit(a, id, strings.TrimSpace(s.in.Value()))
			}
		}
		var cmd tea.Cmd
		s.in, cmd = s.in.Update(msg)
		return cmd
	}
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	key := k.String()
	switch key {
	case "esc", "q", "ctrl+c":
		a.remove(s)
		return nil
	case "j", "down", "tab":
		s.cursor = min(s.cursor+1, len(settingRows)-1)
		return nil
	case "k", "up", "shift+tab":
		s.cursor = max(s.cursor-1, 0)
		return nil
	}
	dir := 0
	switch key {
	case "left", "h":
		dir = -1
	case "right", "l", "space", "enter":
		dir = 1
	}
	if dir == 0 {
		return nil
	}
	switch id {
	case "runtime":
		a.cfg.Runtime = cycle(config.Runtimes, a.cfg.Runtime, dir)
		a.saveConfig()
		return tea.Batch(a.probeCmd(), a.setStatus("runtime: "+runtimeLabel(a.cfg.Runtime), false))
	case "mode":
		a.cfg.IntegrationMode = cycle(config.Modes, a.cfg.IntegrationMode, dir)
		a.saveConfig()
		return a.setStatus("integration mode: "+modeLabel(a.cfg.IntegrationMode), false)
	case "follow":
		a.cfg.Logs.FollowOnOpen = !a.cfg.Logs.FollowOnOpen
		a.saveConfig()
		return nil
	case "updates":
		on := !a.cfg.CheckUpdates()
		a.cfg.UpdateCheck = &on
		if on {
			a.cfg.UpdateCheck = nil // the default; keeps the config file tidy
		}
		a.saveConfig()
		return nil
	}
	if key != "enter" && key != "space" {
		return nil
	}
	switch id {
	case "favorites":
		a.push(newFavorites(a.cfg, func() tea.Cmd {
			a.saveConfig()
			a.mage.refreshItems(a)
			return nil
		}))
	case "manage":
		a.push(newManage(a))
	case "setup":
		return a.do(aSetup)
	case "config":
		return a.do(aEditConfig)
	default:
		s.editing = true
		raw := map[string]string{"shell": a.cfg.Shell, "editor": a.cfg.Editor, "browser": a.cfg.Browser,
			"tail": strconv.Itoa(a.cfg.Logs.TailLines), "refresh": strconv.Itoa(a.cfg.RefreshSeconds)}[id]
		s.in.SetValue(raw)
		s.in.CursorEnd()
		return s.in.Focus()
	}
	return nil
}

func (s *settingsOverlay) commit(a *App, id, v string) tea.Cmd {
	c := a.cfg
	switch id {
	case "shell":
		if v == "" {
			v = "bash"
		}
		c.Shell = v
	case "editor":
		c.Editor = v
	case "browser":
		if v == "" {
			v = "default"
		}
		c.Browser = v
	case "tail", "refresh":
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || (id == "tail" && n > 100000) || (id == "refresh" && n > 3600) {
			s.err = "enter a positive whole number"
			return nil
		}
		if id == "tail" {
			c.Logs.TailLines = n
		} else {
			c.RefreshSeconds = n
		}
	}
	s.editing, s.err = false, ""
	s.in.Blur()
	a.saveConfig()
	return a.setStatus("saved "+id, false)
}

func (s *settingsOverlay) view(a *App, w, h int) string {
	bw := min(w-2, 100)
	var lines []string
	for i, r := range settingRows {
		val := s.value(a, r.id)
		if s.editing && i == s.cursor {
			s.in.SetWidth(bw - 40)
			val = s.in.View()
		}
		line := padRight(r.label, 30) + "  " + val
		if i == s.cursor && !s.editing {
			line = sSelected.Render(padRight("▶ "+r.label, 32)) + "  " + val
		} else {
			line = "  " + line
		}
		lines = append(lines, line)
	}
	if s.err != "" {
		lines = append(lines, "", sErr.Render("! "+s.err))
	}
	lines = append(lines, "", sFaint.Render("Changes save immediately to "+a.cfgPath))
	return overlayBox("Settings", strings.Join(lines, "\n"), bw)
}

func (s *settingsOverlay) hints() string {
	if s.editing {
		return keyHint("enter", "save", "esc", "cancel")
	}
	return keyHint("j/k", "move", "enter", "edit / open", "←/→", "change choice", "esc", "close")
}

// favoritesOverlay toggles favorite Magento commands (also used by setup step 4).
type favoritesOverlay struct {
	cfg     *config.Config
	cursor  int
	onClose func() tea.Cmd
}

func newFavorites(cfg *config.Config, onClose func() tea.Cmd) *favoritesOverlay {
	return &favoritesOverlay{cfg: cfg, onClose: onClose}
}

// favoriteChoices: the preset list, then the rest of the catalog, then any
// favorites the user typed themselves.
func favoriteChoices(cfg *config.Config) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, f := range config.DefaultFavorites {
		add(f)
	}
	for _, mc := range mageCatalog {
		if mc.Prompt == nil {
			add(mc.Cmd)
		}
	}
	for _, f := range cfg.Favorites {
		add(f)
	}
	return out
}

func (f *favoritesOverlay) update(a *App, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	ch := favoriteChoices(f.cfg)
	switch k.String() {
	case "esc", "enter", "q", "ctrl+c":
		a.remove(f)
		return f.onClose()
	case "j", "down":
		f.cursor = min(f.cursor+1, len(ch)-1)
	case "k", "up":
		f.cursor = max(f.cursor-1, 0)
	case "space":
		f.cfg.ToggleFavorite(ch[f.cursor])
	}
	return nil
}

func (f *favoritesOverlay) view(a *App, w, h int) string {
	return overlayBox("Favorite Magento commands", favoritesList(f.cfg, f.cursor, h-4), min(w-2, 70))
}

func favoritesList(cfg *config.Config, cursor, rows int) string {
	ch := favoriteChoices(cfg)
	var lines []string
	start := 0
	if cursor >= rows {
		start = cursor - rows + 1
	}
	for i := start; i < len(ch) && i-start < rows; i++ {
		box := "[ ]"
		if cfg.IsFavorite(ch[i]) {
			box = "[x]"
		}
		line := box + " " + ch[i]
		if i == cursor {
			line = sSelected.Render("▶ " + line)
		} else {
			line = "  " + line
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (f *favoritesOverlay) hints() string {
	return keyHint("space", "toggle", "j/k", "move", "enter/esc", "done")
}
