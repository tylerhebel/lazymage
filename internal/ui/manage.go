package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"lazymage/internal/config"
)

// manageOverlay lists only custom commands (section 8.7).
type manageOverlay struct {
	filter    textinput.Model
	filtering bool
	cursor    int
}

func newManage(a *App) *manageOverlay {
	return &manageOverlay{filter: newInput("/", "search custom commands")}
}

func (m *manageOverlay) list(a *App) []config.Scoped {
	q := strings.TrimSpace(m.filter.Value())
	var out []config.Scoped
	type scored struct {
		sc config.Scoped
		s  int
	}
	var ss []scored
	for _, sc := range a.cfg.EffectiveCommands(a.root()) {
		s := 1
		if q != "" {
			s = max(fuzzyScore(q, sc.Cmd.Name)*3, fuzzyScore(q, sc.Cmd.Command)*2, fuzzyScore(q, sc.Cmd.Category))
			if s == 0 {
				continue
			}
		}
		ss = append(ss, scored{sc, s})
	}
	// Pinned first, then name (EffectiveCommands is name-sorted); by score when searching.
	for pass := 0; pass < 2; pass++ {
		for _, x := range ss {
			if (pass == 0) == x.sc.Cmd.Pinned {
				out = append(out, x.sc)
			}
		}
	}
	if q != "" {
		for i := 1; i < len(out); i++ {
			for j := i; j > 0; j-- {
				si, sj := 0, 0
				for _, x := range ss {
					if x.sc.Project == out[j].Project && x.sc.Index == out[j].Index {
						si = x.s
					}
					if x.sc.Project == out[j-1].Project && x.sc.Index == out[j-1].Index {
						sj = x.s
					}
				}
				if si > sj {
					out[j], out[j-1] = out[j-1], out[j]
				}
			}
		}
	}
	return out
}

func (m *manageOverlay) update(a *App, msg tea.Msg) tea.Cmd {
	list := m.list(a)
	k, isKey := msg.(tea.KeyPressMsg)
	if m.filtering {
		if isKey && (k.String() == "enter" || k.String() == "esc") {
			m.filtering = false
			m.filter.Blur()
			if k.String() == "esc" {
				m.filter.SetValue("")
			}
			return nil
		}
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		m.cursor = 0
		return cmd
	}
	if !isKey {
		return nil
	}
	var sel *config.Scoped
	if m.cursor < len(list) {
		s := list[m.cursor]
		sel = &s
	}
	switch k.String() {
	case "esc", "q", "ctrl+c":
		a.remove(m)
	case "/":
		m.filtering = true
		return m.filter.Focus()
	case "j", "down":
		m.cursor = min(m.cursor+1, max(len(list)-1, 0))
	case "k", "up":
		m.cursor = max(m.cursor-1, 0)
	case "n", "ctrl+n":
		a.push(newCustomForm(a, nil, false))
	case "enter":
		if sel != nil {
			if sel.Overridden {
				return a.setStatus("this global command is overridden by a project command of the same name", true)
			}
			a.remove(m)
			return a.runCustom(*sel)
		}
	case "e":
		if sel != nil {
			a.push(newCustomForm(a, sel, false))
		}
	case "c":
		if sel != nil {
			a.push(newCustomForm(a, sel, true))
		}
	case "space":
		if sel != nil {
			list := a.cfg.Commands(a.root(), sel.Project)
			(*list)[sel.Index].Pinned = !(*list)[sel.Index].Pinned
			a.saveConfig()
			state := "unpinned"
			if (*list)[sel.Index].Pinned {
				state = "pinned"
			}
			return a.setStatus(state+" "+sel.Cmd.Name, false)
		}
	case "d":
		if sel != nil {
			s := *sel
			body := fmt.Sprintf("Delete %q (%s)?", s.Cmd.Name, scopeLabel(s.Project))
			if s.Project {
				body += "\nA global command with the same name, if any, is not affected."
			}
			a.push(newConfirm("Delete custom command", body, "Delete", func() tea.Cmd {
				list := a.cfg.Commands(a.root(), s.Project)
				if s.Index < len(*list) && (*list)[s.Index].Name == s.Cmd.Name {
					*list = append((*list)[:s.Index], (*list)[s.Index+1:]...)
				}
				a.saveConfig()
				a.mage.refreshItems(a)
				a.comp.refreshItems(a)
				m.cursor = max(m.cursor-1, 0)
				return a.setStatus("deleted "+s.Cmd.Name, false)
			}))
		}
	}
	return nil
}

func (m *manageOverlay) view(a *App, w, h int) string {
	bw := w - 2
	inner := bw - 4
	list := m.list(a)
	var lines []string
	if m.filtering || m.filter.Value() != "" {
		m.filter.SetWidth(inner - 4)
		lines = append(lines, m.filter.View(), "")
	}
	if len(list) == 0 {
		lines = append(lines, sFaint.Render("No custom commands yet. Press n to create one."))
	}
	rows := max(h-6, 3)
	start := 0
	if m.cursor >= rows {
		start = m.cursor - rows + 1
	}
	for i := start; i < len(list) && i-start < rows; i++ {
		sc := list[i]
		pin := "  "
		if sc.Cmd.Pinned {
			pin = "★ "
		}
		tags := "[" + scopeLabel(sc.Project) + "] " + sc.Cmd.Kind
		if sc.Cmd.Kind == config.KindHostShell {
			tags += " (host shell)"
		}
		if sc.Overridden {
			tags += " — overridden by project"
		}
		key := sc.Cmd.Shortcut
		cmdW := max(inner-2-26-2-30-2-2-10, 10)
		row := pin + padRight(truncate(sc.Cmd.Name, 26), 26) + "  " + padRight(truncate(tags, 30), 30) + "  " +
			padRight(truncate(sc.Cmd.Command, cmdW), cmdW) + "  " + truncate(key, 10)
		switch {
		case i == m.cursor:
			lines = append(lines, sSelected.Render(padRight(row, inner)))
		case sc.Overridden:
			lines = append(lines, sFaint.Render(row))
		default:
			lines = append(lines, row)
		}
	}
	return overlayBox(fmt.Sprintf("Manage custom commands — %d", len(list)), strings.Join(lines, "\n"), bw)
}

func (m *manageOverlay) hints() string {
	if m.filtering {
		return keyHint("enter", "keep search", "esc", "clear")
	}
	return keyHint("enter", "run", "e", "edit", "c", "duplicate", "d", "delete", "space", "pin", "n", "new", "/", "search", "esc", "close")
}
