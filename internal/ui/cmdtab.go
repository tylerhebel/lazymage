package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"lazymage/internal/config"
)

type cmdItem struct {
	label  string
	desc   string
	fav    bool
	mage   *mageCommand
	comp   *composerCommand
	custom *config.Scoped
	free   bool
}

// cmdTab is the Magento or Composer tab: a list of presets and favorites plus
// a free-form command line with per-project history.
type cmdTab struct {
	kind   string
	items  []cmdItem
	cursor int

	input  textinput.Model
	typing bool
	hist   []string
	histAt int
	draft  string

	filterIn  textinput.Model
	filtering bool
	filter    string
}

func newCmdTab(kind string) *cmdTab {
	prompt := "bin/magento ▸ "
	ph := "press i to type a command, e.g. cache:clean full_page"
	if kind == config.KindComposer {
		prompt, ph = "composer ▸ ", "press i to type a command, e.g. require vendor/package"
	}
	return &cmdTab{kind: kind, input: newInput(prompt, ph), filterIn: newInput("filter ▸ ", "")}
}

func (t *cmdTab) title() string {
	if t.kind == config.KindComposer {
		return "Composer"
	}
	return "Magento"
}

func (t *cmdTab) refreshItems(a *App) {
	var items []cmdItem
	if t.kind == config.KindMagento {
		seen := map[string]bool{}
		for _, f := range a.cfg.Favorites {
			mc, ok := findMage(f)
			if !ok {
				words, _ := config.SplitArgs(f)
				if len(words) == 0 {
					continue
				}
				mc = mageCommand{Cmd: words[0], Args: words[1:], Category: catSetup, Desc: "favorite"}
			}
			m := mc
			seen[f] = true
			items = append(items, cmdItem{label: strings.TrimSpace(f), desc: mc.Desc, fav: true, mage: &m})
		}
		for _, mc := range mageCatalog {
			if seen[mc.Cmd] {
				continue
			}
			m := mc
			label := mc.Cmd
			if mc.Prompt != nil {
				label += "…"
			}
			items = append(items, cmdItem{label: label, desc: mc.Desc, mage: &m})
		}
	} else {
		for _, cc := range composerCatalog {
			c := cc
			items = append(items, cmdItem{label: cc.Title, desc: cc.Desc, comp: &c})
		}
	}
	for _, sc := range a.cfg.EffectiveCommands(a.root()) {
		if sc.Overridden || sc.Cmd.Kind != t.kind {
			continue
		}
		s := sc
		items = append(items, cmdItem{label: sc.Cmd.Name, desc: "custom · " + sc.Cmd.Command, custom: &s, fav: sc.Cmd.Pinned})
	}
	free := "Run a Magento command…"
	if t.kind == config.KindComposer {
		free = "Run a Composer command…"
	}
	items = append(items, cmdItem{label: free, desc: "free-form, with history", free: true})
	t.items = items
	if t.cursor >= len(items) {
		t.cursor = max(len(items)-1, 0)
	}
	t.refreshHistory(a)
}

// refreshHistory loads this kind's previous commands (args only) for arrow-key recall.
func (t *cmdTab) refreshHistory(a *App) {
	t.hist = nil
	if a.proj == nil {
		return
	}
	p := a.cfg.Projects[a.proj.Root]
	if p == nil {
		return
	}
	marker := "bin/magento "
	if t.kind == config.KindComposer {
		marker = "composer "
	}
	for _, h := range p.History {
		if h.Kind != t.kind {
			continue
		}
		i := strings.LastIndex(h.Command, marker)
		if i < 0 {
			continue
		}
		args := h.Command[i+len(marker):]
		if n := len(t.hist); n > 0 && t.hist[n-1] == args {
			continue
		}
		t.hist = append(t.hist, args)
	}
	t.histAt = len(t.hist)
}

func (t *cmdTab) focusInput() tea.Cmd {
	t.typing = true
	t.histAt = len(t.hist)
	return t.input.Focus()
}

func (t *cmdTab) visibleItems() []int {
	var out []int
	f := strings.ToLower(t.filter)
	for i, it := range t.items {
		if f == "" || strings.Contains(strings.ToLower(it.label+" "+it.desc), f) {
			out = append(out, i)
		}
	}
	return out
}

// argsFromInput splits the typed command and drops a redundant wrapper prefix.
func (t *cmdTab) argsFromInput(s string) ([]string, error) {
	words, err := config.SplitArgs(s)
	if err != nil {
		return nil, err
	}
	for len(words) > 0 && (words[0] == "bin/magento" || words[0] == "magento" || words[0] == "bin/composer" || words[0] == "composer") {
		words = words[1:]
	}
	return words, nil
}

// update handles keys. With inputPhase it only consumes keys when a text
// input has focus, so global keys still work otherwise.
func (t *cmdTab) update(a *App, msg tea.Msg, inputPhase bool) (tea.Cmd, bool) {
	if inputPhase {
		if p, ok := msg.(tea.PasteMsg); ok {
			if t.typing {
				var cmd tea.Cmd
				t.input, cmd = t.input.Update(p)
				return cmd, true
			}
			return nil, false
		}
		k, ok := msg.(tea.KeyPressMsg)
		if !ok {
			return nil, false
		}
		if t.typing {
			return t.updateTyping(a, k), true
		}
		if t.filtering {
			switch k.String() {
			case "enter", "esc":
				t.filtering = false
				t.filterIn.Blur()
				if k.String() == "esc" {
					t.filter = ""
				}
				t.cursor = 0
				return nil, true
			}
			var cmd tea.Cmd
			t.filterIn, cmd = t.filterIn.Update(k)
			t.filter = t.filterIn.Value()
			t.cursor = 0
			return cmd, true
		}
		return nil, false
	}
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil, false
	}
	vis := t.visibleItems()
	switch k.String() {
	case "j", "down":
		t.cursor = min(t.cursor+1, max(len(vis)-1, 0))
	case "k", "up":
		t.cursor = max(t.cursor-1, 0)
	case "g", "home":
		t.cursor = 0
	case "G", "end":
		t.cursor = max(len(vis)-1, 0)
	case "i", ":":
		return t.focusInput(), true
	case "/":
		t.filtering = true
		t.filterIn.SetValue(t.filter)
		return t.filterIn.Focus(), true
	case "esc":
		t.filter = ""
	case "space":
		if t.kind == config.KindMagento && t.cursor < len(vis) {
			it := t.items[vis[t.cursor]]
			if it.mage != nil {
				name := it.label
				if !it.fav {
					name = it.mage.Cmd
				}
				name = strings.TrimSuffix(name, "…")
				a.cfg.ToggleFavorite(name)
				a.saveConfig()
				t.refreshItems(a)
				if a.cfg.IsFavorite(name) {
					return a.setStatus("★ added to favorites: "+name, false), true
				}
				return a.setStatus("removed from favorites: "+name, false), true
			}
		}
	case "enter":
		if t.cursor < len(vis) {
			return t.runItem(a, t.items[vis[t.cursor]]), true
		}
	}
	return nil, true
}

func (t *cmdTab) runItem(a *App, it cmdItem) tea.Cmd {
	switch {
	case it.free:
		return t.focusInput()
	case it.mage != nil:
		return a.runMageCommand(*it.mage)
	case it.comp != nil:
		return a.runComposerPreset(*it.comp)
	case it.custom != nil:
		return a.runCustom(*it.custom)
	}
	return nil
}

func (t *cmdTab) updateTyping(a *App, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		t.typing = false
		t.input.Blur()
		return nil
	case "up":
		if t.histAt == len(t.hist) {
			t.draft = t.input.Value()
		}
		if t.histAt > 0 {
			t.histAt--
			t.input.SetValue(t.hist[t.histAt])
			t.input.CursorEnd()
		}
		return nil
	case "down":
		if t.histAt < len(t.hist) {
			t.histAt++
			if t.histAt == len(t.hist) {
				t.input.SetValue(t.draft)
			} else {
				t.input.SetValue(t.hist[t.histAt])
			}
			t.input.CursorEnd()
		}
		return nil
	case "enter":
		args, err := t.argsFromInput(t.input.Value())
		if err != nil {
			return a.setStatus("cannot parse command: "+err.Error(), true)
		}
		if len(args) == 0 {
			return a.setStatus("type a command first", true)
		}
		t.input.SetValue("")
		t.typing = false
		t.input.Blur()
		if t.kind == config.KindComposer {
			return a.runComposerArgs(args)
		}
		return a.runMageArgs(args, false)
	}
	var cmd tea.Cmd
	t.input, cmd = t.input.Update(k)
	return cmd
}

func (t *cmdTab) hints() string {
	if t.typing {
		return keyHint("enter", "run", "↑/↓", "history", "esc", "back to list")
	}
	if t.filtering {
		return keyHint("enter", "keep filter", "esc", "clear")
	}
	if t.kind == config.KindMagento {
		return keyHint("enter", "run", "i", "type a command", "space", "favorite", "/", "filter", "tab", "next tab", "x", "palette", "?", "help")
	}
	return keyHint("enter", "run", "i", "type a command", "/", "filter", "tab", "next tab", "x", "palette", "?", "help")
}

func (t *cmdTab) view(a *App, w, h int) string {
	var out []string
	t.input.SetWidth(w - 20)
	out = append(out, sBold.Render(t.title())+sFaint.Render(fmt.Sprintf(" — %d commands · history %d", len(t.items), len(t.hist))))
	out = append(out, t.input.View())
	preview := ""
	if t.typing {
		if args, err := t.argsFromInput(t.input.Value()); err != nil {
			preview = sErr.Render("  " + err.Error())
		} else if len(args) > 0 {
			var argv []string
			if t.kind == config.KindComposer {
				argv = a.builder().Composer(args...)
			} else {
				argv = a.builder().Magento(args...)
			}
			preview = sFaint.Render("  will run: ") + config.JoinArgs(argv)
		} else {
			preview = sFaint.Render("  type a command, ↑ for history")
		}
	} else {
		preview = sFaint.Render("  ↑ recalls history while typing")
	}
	out = append(out, truncateStyled(preview, w))
	if t.filtering || t.filter != "" {
		t.filterIn.SetWidth(w - 12)
		out = append(out, t.filterIn.View())
	}
	out = append(out, sFaint.Render(strings.Repeat("─", w)))

	vis := t.visibleItems()
	listH := h - len(out)
	start := 0
	if t.cursor >= listH {
		start = t.cursor - listH + 1
	}
	labelW := 34
	for p := start; p < len(vis) && p-start < listH; p++ {
		it := t.items[vis[p]]
		mark := "  "
		if it.fav {
			mark = sWarn.Render("★ ")
		}
		tag := ""
		if it.custom != nil {
			tag = sNote.Render(" [custom]")
			if it.custom.Cmd.Kind == config.KindHostShell {
				tag += sWarn.Render(" [host shell]")
			}
			if it.custom.Cmd.Shortcut != "" {
				tag += sFaint.Render(" " + it.custom.Cmd.Shortcut)
			}
		}
		line := mark + padRight(truncate(it.label, labelW), labelW) + " " + sFaint.Render(truncate(it.desc, max(w-labelW-20, 10))) + tag
		if !t.typing && p == t.cursor {
			line = sSelected.Render(padRight(stripANSI(mark)+padRight(truncate(it.label, labelW), labelW)+" "+truncate(it.desc, max(w-labelW-20, 10))+stripANSI(tag), w))
		}
		out = append(out, truncateStyled(line, w))
	}
	if len(vis) == 0 {
		out = append(out, sFaint.Render("  no commands match "+fmt.Sprintf("%q", t.filter)))
	}
	return strings.Join(out, "\n")
}
