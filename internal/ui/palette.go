package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"lazymage/internal/config"
)

type paletteOverlay struct {
	in        textinput.Model
	cursor    int
	listFocus bool
	all       []paletteEntry
	customs   map[string]config.Scoped
}

func newPalette(a *App) *paletteOverlay {
	in := newInput("▸ ", "search actions — e.g. flush, reindex, restart, shell")
	in.Focus()
	p := &paletteOverlay{in: in}
	p.reload(a)
	return p
}

// reload rebuilds entries so the palette reflects state (disabled reasons,
// new custom commands) every time it is opened or changed.
func (p *paletteOverlay) reload(a *App) {
	p.all, p.customs = buildEntries(a)
}

func buildEntries(a *App) ([]paletteEntry, map[string]config.Scoped) {
	b := a.builder()
	var es []paletteEntry
	n := 0
	add := func(id, title, cat, command, kw string) {
		n++
		es = append(es, paletteEntry{ID: id, Title: title, Category: cat, Command: command, Keywords: kw,
			Shortcut: shortcutFor(id), Disabled: a.disabledReason(id), order: n})
	}
	j := config.JoinArgs
	hasProj := a.proj != nil

	cmdOr := func(f func() string) string {
		if !hasProj {
			return ""
		}
		return f()
	}
	add(aStackStart, "Start stack", catStack, cmdOr(func() string { return j(b.Lifecycle("start")) }), "start up boot launch stack containers")
	add(aStackStop, "Stop stack (confirm)", catStack, cmdOr(func() string { return j(b.Lifecycle("stop")) }), "stop down halt shutdown stack containers")
	add(aStackRestart, "Restart stack", catStack, cmdOr(func() string { return j(b.Lifecycle("restart")) }), "restart reboot reload stack containers")
	add(aEngineStart, "Start engine", catStack, engineStartDesc(a.cfg.Runtime), "engine docker orbstack desktop start daemon")
	add(aRefresh, "Refresh status and project facts", catStack, "docker info · compose ps · bin/magento --version", "refresh reload status health facts")

	logsCmd := cmdOr(func() string { return j(b.Logs("", a.cfg.Logs.TailLines, true, "")) })
	add(aLogsAll, "Logs: all services", catLogs, logsCmd, "logs all combined tail follow")
	add(aLogsPick, "Logs: pick a service…", catLogs, "choose one service, then follow its logs", "logs service container pick choose")
	add(aLogsPause, "Pause / resume stream", catLogs, "pause new lines, keep the buffer", "pause resume stop stream logs")
	add(aLogsFollow, "Toggle follow", catLogs, "keep the newest line in view", "follow tail autoscroll logs")
	add(aLogsSearch, "Search logs", catLogs, "find text in the buffer, n / N to move", "search find grep logs")
	add(aLogsNext, "Next search match", catLogs, "jump to the next match", "next match search logs")
	add(aLogsPrev, "Previous search match", catLogs, "jump to the previous match", "previous match search logs")
	add(aLogsFilter, "Filter logs by text…", catLogs, "show only lines containing text", "filter grep only lines logs")
	add(aLogsClear, "Clear log buffer", catLogs, "empty the visible buffer", "clear empty reset logs")
	add(aLogsSave, "Save log buffer to file…", catLogs, "write the visible lines to a file", "save export write file logs")

	for _, mc := range mageCatalog {
		title := mc.Cmd
		if mc.Prompt != nil {
			title += "…"
		}
		args := append([]string{mc.Cmd}, mc.Args...)
		if mc.Prompt != nil {
			args = append(args, "<"+strings.ToLower(strings.SplitN(mc.Prompt.Label, " ", 2)[0])+">")
		}
		add("magento:"+mc.Cmd, title, mc.Category, cmdOr(func() string { return j(b.Magento(args...)) }), mc.Keywords+" magento "+mc.Desc)
		es[len(es)-1].Pinned = a.cfg.IsFavorite(mc.Cmd)
	}
	add(aMageFree, "Run a Magento command…", catSetup, cmdOr(func() string { return j(b.Magento("<command>")) }), "magento free form custom type bin/magento")

	for _, cc := range composerCatalog {
		args := cc.Args
		if cc.Prompt != nil {
			args = append(append([]string(nil), args...), "<package>")
		}
		add(cc.ID, cc.Title, catComposer, cmdOr(func() string { return j(b.Composer(args...)) }), cc.Keywords+" composer "+cc.Desc)
	}
	add(aComposerFree, "Run a Composer command…", catComposer, cmdOr(func() string { return j(b.Composer("<command>")) }), "composer free form custom type")

	add(aShell, "Shell into PHP container", catProject, cmdOr(func() string { return j(b.Shell(a.cfg.Shell)) }), "shell bash zsh terminal ssh exec cli container php")
	add(aStorefront, "Open storefront", catProject, "open "+orUnknown(a.storefrontURL()), "open browser storefront site frontend web")
	add(aAdmin, "Open admin", catProject, "open "+orUnknown(a.adminURL()), "open browser admin backend dashboard")
	add(aCopyURL, "Copy storefront URL", catProject, "copy "+orUnknown(a.storefrontURL()), "copy clipboard url link")
	add(aEditEnv, "Open project .env in editor", catProject, j(a.editorArgv())+" .env", "edit env environment editor")
	add(aEditConfig, "Open TUI config in editor", catProject, j(a.editorArgv())+" "+a.cfgPath, "edit config yaml settings editor")

	add(aSettings, "Settings", catApp, "edit every setup choice", "settings preferences options config")
	add(aSetup, "Re-run setup", catApp, "the first-run wizard, with current values", "setup wizard first run onboarding")
	add(aHelp, "Help", catApp, "every keybinding", "help keys keybindings shortcuts")
	add(aNewCustom, "Create a custom command…", catApp, "name, command, prompts, shortcut, scope", "new create custom command add")
	add(aManage, "Manage custom commands…", catApp, "run, edit, duplicate, delete, pin", "manage custom commands edit delete")
	add(aQuit, "Quit", catApp, "exit lazymage", "quit exit close")

	customs := map[string]config.Scoped{}
	for _, sc := range a.cfg.EffectiveCommands(a.root()) {
		if sc.Overridden {
			continue
		}
		id := customID(sc)
		customs[id] = sc
		vals := map[string]string{}
		for _, p := range sc.Cmd.Prompts {
			vals[p.Key] = "{{" + p.Key + "}}"
		}
		display := sc.Cmd.Command
		if hasProj {
			if argv, err := b.Custom(sc.Cmd, vals); err == nil {
				display = j(argv)
			}
		}
		cat := sc.Cmd.Category
		if cat == "" {
			cat = "Custom"
		}
		n++
		es = append(es, paletteEntry{ID: id, Title: sc.Cmd.Name, Category: cat, Command: display,
			Keywords: "custom " + sc.Cmd.Kind + " " + scopeLabel(sc.Project), Shortcut: sc.Cmd.Shortcut,
			Disabled: a.customDisabled(sc.Cmd), Custom: true, HostSh: sc.Cmd.Kind == config.KindHostShell, Pinned: sc.Cmd.Pinned, order: n})
	}
	return es, customs
}

func orUnknown(s string) string {
	if s == "" {
		return "(domain unknown)"
	}
	return s
}

func (p *paletteOverlay) filtered(a *App) []paletteEntry {
	return rankEntries(p.all, p.in.Value(), a.recent)
}

func (p *paletteOverlay) update(a *App, msg tea.Msg) tea.Cmd {
	list := p.filtered(a)
	if k, ok := msg.(tea.KeyPressMsg); ok {
		key := k.String()
		switch key {
		case "esc", "ctrl+c":
			a.remove(p)
			return nil
		case "ctrl+n":
			a.remove(p)
			return a.do(aNewCustom)
		case "up", "ctrl+k":
			p.cursor = max(p.cursor-1, 0)
			return nil
		case "down", "ctrl+j":
			p.cursor = min(p.cursor+1, max(len(list)-1, 0))
			return nil
		case "tab":
			p.listFocus = !p.listFocus
			if p.listFocus {
				p.in.Blur()
				return nil
			}
			return p.in.Focus()
		case "enter":
			if p.cursor >= len(list) {
				return nil
			}
			e := list[p.cursor]
			if e.Disabled != "" {
				return a.setStatus(e.Title+": "+e.Disabled, true)
			}
			a.remove(p)
			if sc, ok := p.customs[e.ID]; ok {
				return a.runCustom(sc)
			}
			return a.do(e.ID)
		}
		if p.listFocus {
			switch key {
			case "j":
				p.cursor = min(p.cursor+1, max(len(list)-1, 0))
			case "k":
				p.cursor = max(p.cursor-1, 0)
			case "g":
				p.cursor = 0
			case "G":
				p.cursor = max(len(list)-1, 0)
			}
			return nil
		}
	}
	before := p.in.Value()
	var cmd tea.Cmd
	p.in, cmd = p.in.Update(msg)
	if p.in.Value() != before {
		p.cursor = 0
	}
	return cmd
}

func (p *paletteOverlay) view(a *App, w, h int) string {
	bw := w - 2
	inner := bw - 4
	p.in.SetWidth(inner - 4)
	list := p.filtered(a)
	lines := []string{p.in.View(), ""}
	rows := max(h-6, 3)
	start := 0
	if p.cursor >= rows {
		start = p.cursor - rows + 1
	}
	titleW, catW, keyW := 30, 24, 14
	if inner < 110 {
		titleW, catW, keyW = 27, 22, 11
	}
	rows-- // detail line for the selected entry
	cmdW := max(inner-titleW-catW-keyW-6, 10)
	for i := start; i < len(list) && i-start < rows; i++ {
		e := list[i]
		title := e.Title
		if e.Pinned {
			title = "★ " + title
		}
		tags := ""
		if e.Custom {
			tags += " custom"
		}
		if e.HostSh {
			tags += " host-shell"
		}
		detail := e.Command
		if e.Disabled != "" {
			detail = "⊘ " + e.Disabled
		}
		row := padRight(truncate(title, titleW), titleW) + "  " +
			padRight(truncate(e.Category+tags, catW), catW) + "  " +
			padRight(truncate(detail, cmdW), cmdW) + "  " +
			padRight(truncate(e.Shortcut, keyW), keyW)
		switch {
		case i == p.cursor:
			lines = append(lines, sSelected.Render(padRight(row, inner)))
		case e.Disabled != "":
			lines = append(lines, sFaint.Render(row))
		default:
			lines = append(lines, row)
		}
	}
	if len(list) == 0 {
		lines = append(lines, sFaint.Render("no actions match — ctrl+n creates a custom command"))
	} else if p.cursor < len(list) {
		e := list[p.cursor]
		detail := "runs: " + e.Command
		if e.Disabled != "" {
			detail = "unavailable: " + e.Disabled
		}
		if e.Shortcut != "" {
			detail += "   key: " + e.Shortcut
		}
		lines = append(lines, "", sNote.Render(truncate(detail, inner)))
	}
	title := fmt.Sprintf("Command palette — %d of %d", len(list), len(p.all))
	return overlayBox(title, strings.Join(lines, "\n"), bw)
}

func (p *paletteOverlay) hints() string {
	return keyHint("type", "fuzzy search", "↑/↓ ctrl+j/k", "move", "tab", "list focus (j/k)", "enter", "run", "ctrl+n", "new custom command", "esc", "close")
}
