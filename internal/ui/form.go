package ui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"lazymage/internal/config"
	"lazymage/internal/plan"
)

type promptRow struct {
	key      string
	label    textinput.Model
	def      textinput.Model
	required bool
	extra    map[string]any
}

type shortcutConflict struct {
	key      string
	desc     string
	custom   *config.Scoped // nil = built-in
	sel      int            // 0 replace, 1 pick another, 2 cancel
	previous string
}

// formTarget abstracts where the form saves: the live config (main screen) or
// the setup wizard's draft.
type formTarget struct {
	cfg     *config.Config
	root    string // "" = no project (global scope only)
	builder plan.Builder
	onSave  func(cc config.CustomCommand, project bool) tea.Cmd
}

// customForm creates or edits a custom command (section 8.7).
type customForm struct {
	t           formTarget
	orig        *config.Scoped // when editing
	dup         bool
	name        textinput.Model
	category    textinput.Model
	kind        int
	command     textinput.Model
	prompts     []promptRow
	confirm     bool
	confTouched bool
	shortcut    string
	capturing   bool
	project     bool
	pinned      bool
	field       int
	errs        map[string]string
	conflict    *shortcutConflict
	steal       []config.Scoped
	done        bool
	extra       map[string]any
}

func newCustomForm(a *App, orig *config.Scoped, dup bool) *customForm {
	t := formTarget{cfg: a.cfg, root: a.root(), builder: a.builder(), onSave: func(cc config.CustomCommand, project bool) tea.Cmd {
		if !a.saveConfig() {
			return nil
		}
		a.mage.refreshItems(a)
		a.comp.refreshItems(a)
		scope := "all projects"
		if project {
			scope = "this project"
		}
		return a.setStatus(fmt.Sprintf("saved custom command %q (%s) — it is in the palette now", cc.Name, scope), false)
	}}
	return newCustomFormFor(t, orig, dup)
}

func newCustomFormFor(t formTarget, orig *config.Scoped, dup bool) *customForm {
	f := &customForm{
		t:        t,
		name:     newInput("", "e.g. Reindex catalog"),
		category: newInput("", "pick with ctrl+n / ctrl+p, or type a new one"),
		command:  newInput("", "e.g. indexer:reindex {{indexer}}"),
		project:  t.root != "",
		errs:     map[string]string{},
	}
	f.name.CharLimit = 80
	if orig != nil {
		cc := orig.Cmd
		f.name.SetValue(cc.Name)
		if dup {
			f.name.SetValue(cc.Name + " copy")
		} else {
			f.orig = orig
		}
		f.dup = dup
		f.category.SetValue(cc.Category)
		for i, k := range config.Kinds {
			if k == cc.Kind {
				f.kind = i
			}
		}
		f.command.SetValue(cc.Command)
		for _, p := range cc.Prompts {
			f.prompts = append(f.prompts, newPromptRow(p))
		}
		f.confirm, f.confTouched = cc.Confirm, true
		if !dup {
			f.shortcut = cc.Shortcut
		}
		f.project = orig.Project && t.root != ""
		f.pinned = cc.Pinned
		f.extra = cc.Extra
		f.syncPrompts()
	}
	f.name.Focus()
	return f
}

func newPromptRow(p config.Prompt) promptRow {
	l := newInput("", "label shown when asking")
	l.SetValue(p.Label)
	d := newInput("", "default value (optional)")
	d.SetValue(p.Default)
	return promptRow{key: p.Key, label: l, def: d, required: p.Required, extra: p.Extra}
}

// fields lists the form rows in order; prompt rows appear for each placeholder.
func (f *customForm) fields() []string {
	fs := []string{"name", "category", "kind", "command"}
	for i := range f.prompts {
		fs = append(fs, fmt.Sprintf("p%d.label", i), fmt.Sprintf("p%d.default", i), fmt.Sprintf("p%d.required", i))
	}
	return append(fs, "confirm", "shortcut", "scope", "pin", "save")
}

func (f *customForm) cur() string {
	fs := f.fields()
	f.field = min(max(f.field, 0), len(fs)-1)
	return fs[f.field]
}

func (f *customForm) kindValue() string { return config.Kinds[f.kind] }

// syncPrompts keeps one prompt definition per {{placeholder}} in the command.
func (f *customForm) syncPrompts() {
	keys := config.Placeholders(f.command.Value())
	byKey := map[string]promptRow{}
	for _, p := range f.prompts {
		byKey[p.key] = p
	}
	var out []promptRow
	for _, k := range keys {
		if p, ok := byKey[k]; ok {
			out = append(out, p)
		} else {
			out = append(out, newPromptRow(config.Prompt{Key: k, Label: k, Required: true}))
		}
	}
	f.prompts = out
	if !f.confTouched {
		f.confirm = config.LooksDestructive(f.kindValue(), f.command.Value())
	}
}

func (f *customForm) focusField() tea.Cmd {
	f.name.Blur()
	f.category.Blur()
	f.command.Blur()
	for i := range f.prompts {
		f.prompts[i].label.Blur()
		f.prompts[i].def.Blur()
	}
	if in := f.inputFor(f.cur()); in != nil {
		return in.Focus()
	}
	return nil
}

func (f *customForm) inputFor(field string) *textinput.Model {
	switch field {
	case "name":
		return &f.name
	case "category":
		return &f.category
	case "command":
		return &f.command
	}
	var i int
	var part string
	if n, _ := fmt.Sscanf(field, "p%d.%s", &i, &part); n == 2 && i < len(f.prompts) {
		switch part {
		case "label":
			return &f.prompts[i].label
		case "default":
			return &f.prompts[i].def
		}
	}
	return nil
}

func (f *customForm) categories() []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range categoryOrder {
		seen[c] = true
		out = append(out, c)
	}
	addAll := func(cs []config.CustomCommand) {
		for _, c := range cs {
			if c.Category != "" && !seen[c.Category] {
				seen[c.Category] = true
				out = append(out, c.Category)
			}
		}
	}
	addAll(f.t.cfg.CustomCommands)
	if f.t.root != "" {
		addAll(f.t.cfg.Project(f.t.root).CustomCommands)
	}
	return out
}

// sameAsOrig reports whether sc is the command being edited.
func (f *customForm) sameAsOrig(sc config.Scoped) bool {
	return f.orig != nil && sc.Project == f.orig.Project && sc.Index == f.orig.Index
}

// findConflict reports what already uses key in the chosen scope.
func (f *customForm) findConflict(key string) *shortcutConflict {
	if d := builtinUsing(key); d != "" {
		return &shortcutConflict{key: key, desc: "built-in: " + d}
	}
	for _, sc := range f.scopeCommands() {
		if f.sameAsOrig(sc) || sc.Cmd.Shortcut != key {
			continue
		}
		s := sc
		return &shortcutConflict{key: key, desc: fmt.Sprintf("custom command %q (%s)", sc.Cmd.Name, scopeLabel(sc.Project)), custom: &s}
	}
	return nil
}

// scopeCommands are the commands whose shortcuts and names can collide:
// globals and this project's commands.
func (f *customForm) scopeCommands() []config.Scoped {
	var out []config.Scoped
	if f.t.root != "" {
		for i, c := range f.t.cfg.Project(f.t.root).CustomCommands {
			out = append(out, config.Scoped{Cmd: c, Project: true, Index: i})
		}
	}
	for i, c := range f.t.cfg.CustomCommands {
		out = append(out, config.Scoped{Cmd: c, Index: i})
	}
	return out
}

func (f *customForm) update(a *App, msg tea.Msg) tea.Cmd {
	k, isKey := msg.(tea.KeyPressMsg)
	if f.conflict != nil {
		if !isKey {
			return nil
		}
		return f.updateConflict(k)
	}
	if f.capturing {
		if !isKey {
			return nil
		}
		key := k.String()
		switch key {
		case "esc":
			f.capturing = false
			return nil
		case "backspace", "delete":
			f.capturing = false
			f.shortcut = ""
			delete(f.errs, "shortcut")
			return nil
		}
		f.capturing = false
		if reservedKeys[key] {
			f.errs["shortcut"] = fmt.Sprintf("%s is reserved for operating lazymage — pick another", key)
			return nil
		}
		if c := f.findConflict(key); c != nil {
			c.previous = f.shortcut
			c.sel = 1
			f.conflict = c
			return nil
		}
		f.shortcut = key
		delete(f.errs, "shortcut")
		return nil
	}

	if isKey {
		switch k.String() {
		case "esc", "ctrl+c":
			f.done = true
			a.remove(f)
			return nil
		case "ctrl+s":
			return f.save(a)
		case "tab", "down":
			f.field++
			return f.focusField()
		case "shift+tab", "up":
			f.field--
			return f.focusField()
		}
		field := f.cur()
		switch {
		case field == "category" && (k.String() == "ctrl+n" || k.String() == "ctrl+p"):
			cats := f.categories()
			i := indexOf(cats, f.category.Value())
			if k.String() == "ctrl+n" {
				i = (i + 1) % len(cats)
			} else {
				i = (i - 1 + len(cats)) % len(cats)
			}
			f.category.SetValue(cats[i])
			f.category.CursorEnd()
			return nil
		case field == "kind" && isToggleKey(k.String()):
			if k.String() == "left" || k.String() == "h" {
				f.kind = (f.kind - 1 + len(config.Kinds)) % len(config.Kinds)
			} else {
				f.kind = (f.kind + 1) % len(config.Kinds)
			}
			f.syncPrompts()
			return nil
		case field == "scope" && isToggleKey(k.String()):
			if f.t.root == "" {
				f.errs["scope"] = "not in a project — only All projects is available"
				return nil
			}
			f.project = !f.project
			return nil
		case field == "confirm" && isToggleKey(k.String()):
			f.confirm, f.confTouched = !f.confirm, true
			return nil
		case field == "pin" && isToggleKey(k.String()):
			f.pinned = !f.pinned
			return nil
		case strings.HasSuffix(field, ".required") && isToggleKey(k.String()):
			var i int
			fmt.Sscanf(field, "p%d.", &i)
			f.prompts[i].required = !f.prompts[i].required
			return nil
		case field == "shortcut" && k.String() == "enter":
			f.capturing = true
			return nil
		case field == "shortcut" && (k.String() == "backspace" || k.String() == "delete"):
			f.shortcut = ""
			return nil
		case field == "save" && k.String() == "enter":
			return f.save(a)
		case k.String() == "enter":
			f.field++
			return f.focusField()
		}
	}
	if in := f.inputFor(f.cur()); in != nil {
		var cmd tea.Cmd
		*in, cmd = in.Update(msg)
		if f.cur() == "command" {
			f.syncPrompts()
		}
		delete(f.errs, f.cur())
		return cmd
	}
	return nil
}

func isToggleKey(k string) bool {
	switch k {
	case "space", "enter", "left", "right", "h", "l":
		return true
	}
	return false
}

func indexOf(xs []string, s string) int {
	for i, x := range xs {
		if x == s {
			return i
		}
	}
	return -1
}

func (f *customForm) updateConflict(k tea.KeyPressMsg) tea.Cmd {
	c := f.conflict
	switch k.String() {
	case "left", "h", "shift+tab":
		c.sel = (c.sel + 2) % 3
	case "right", "l", "tab":
		c.sel = (c.sel + 1) % 3
	case "r":
		c.sel = 0
		return f.resolveConflict()
	case "p":
		c.sel = 1
		return f.resolveConflict()
	case "esc", "c":
		c.sel = 2
		return f.resolveConflict()
	case "enter":
		return f.resolveConflict()
	}
	return nil
}

func (f *customForm) resolveConflict() tea.Cmd {
	c := f.conflict
	f.conflict = nil
	switch c.sel {
	case 0: // Replace
		f.shortcut = c.key
		if c.custom != nil {
			f.steal = append(f.steal, *c.custom)
		}
		delete(f.errs, "shortcut")
	case 1: // Pick another
		f.capturing = true
	default: // Cancel: keep the previous shortcut
		f.shortcut = c.previous
	}
	return nil
}

func (f *customForm) build() config.CustomCommand {
	cc := config.CustomCommand{
		Name:     strings.TrimSpace(f.name.Value()),
		Category: strings.TrimSpace(f.category.Value()),
		Kind:     f.kindValue(),
		Command:  strings.TrimSpace(f.command.Value()),
		Prompts:  []config.Prompt{},
		Confirm:  f.confirm,
		Shortcut: f.shortcut,
		Pinned:   f.pinned,
		Extra:    f.extra,
	}
	for _, p := range f.prompts {
		cc.Prompts = append(cc.Prompts, config.Prompt{Key: p.key, Label: strings.TrimSpace(p.label.Value()), Default: p.def.Value(), Required: p.required, Extra: p.extra})
	}
	return cc
}

// validate fills f.errs; it returns true when the form can be saved.
func (f *customForm) validate() bool {
	f.errs = map[string]string{}
	cc := f.build()
	if cc.Name == "" {
		f.errs["name"] = "name is required"
	}
	if cc.Command == "" {
		f.errs["command"] = "command is required"
	} else if cc.Kind != config.KindHostShell {
		if _, err := config.SplitArgs(cc.Command); err != nil {
			f.errs["command"] = err.Error()
		}
	}
	if cc.Kind == config.KindBin && f.errs["command"] == "" {
		if _, err := f.t.builder.Custom(cc, map[string]string{}); err != nil {
			f.errs["command"] = err.Error()
		}
	}
	defined := map[string]bool{}
	for _, p := range cc.Prompts {
		defined[p.Key] = true
	}
	for _, ph := range config.Placeholders(cc.Command) {
		if !defined[ph] {
			f.errs["command"] = fmt.Sprintf("{{%s}} has no prompt definition", ph)
		}
	}
	project := f.project && f.t.root != ""
	for _, sc := range f.scopeCommands() {
		if f.sameAsOrig(sc) {
			continue
		}
		if sc.Project == project && strings.EqualFold(sc.Cmd.Name, cc.Name) && cc.Name != "" {
			f.errs["name"] = fmt.Sprintf("%q already exists in %s scope", sc.Cmd.Name, scopeLabel(project))
		}
		if cc.Shortcut != "" && sc.Cmd.Shortcut == cc.Shortcut && !f.stolen(sc) {
			f.errs["shortcut"] = fmt.Sprintf("%s is used by %q — press enter on this field to choose replace / pick another", cc.Shortcut, sc.Cmd.Name)
		}
	}
	if cc.Shortcut != "" && reservedKeys[cc.Shortcut] {
		f.errs["shortcut"] = cc.Shortcut + " is reserved"
	}
	return len(f.errs) == 0
}

func (f *customForm) stolen(sc config.Scoped) bool {
	for _, s := range f.steal {
		if s.Project == sc.Project && s.Index == sc.Index {
			return true
		}
	}
	return false
}

func (f *customForm) save(a *App) tea.Cmd {
	if !f.validate() {
		// Jump to the first field with an error.
		for i, name := range f.fields() {
			if f.errs[name] != "" {
				f.field = i
				return f.focusField()
			}
		}
		return nil
	}
	cc := f.build()
	project := f.project && f.t.root != ""
	cfg := f.t.cfg
	// Clear shortcuts taken over with Replace.
	for _, s := range f.steal {
		list := cfg.Commands(f.t.root, s.Project)
		if s.Index < len(*list) {
			(*list)[s.Index].Shortcut = ""
		}
	}
	if f.orig != nil {
		old := cfg.Commands(f.t.root, f.orig.Project)
		if f.orig.Project == project && f.orig.Index < len(*old) {
			(*old)[f.orig.Index] = cc
		} else {
			if f.orig.Index < len(*old) {
				*old = append((*old)[:f.orig.Index], (*old)[f.orig.Index+1:]...)
			}
			dst := cfg.Commands(f.t.root, project)
			*dst = append(*dst, cc)
		}
	} else {
		dst := cfg.Commands(f.t.root, project)
		*dst = append(*dst, cc)
	}
	f.done = true
	a.remove(f)
	return f.t.onSave(cc, project)
}

func (f *customForm) hints() string {
	if f.conflict != nil {
		return keyHint("r", "replace", "p", "pick another", "c/esc", "cancel")
	}
	if f.capturing {
		return keyHint("any key", "assign", "backspace", "clear", "esc", "stop")
	}
	return keyHint("tab/↓", "next field", "shift+tab/↑", "previous", "space/←/→", "toggle", "ctrl+s", "save", "esc", "cancel")
}

func (f *customForm) view(a *App, w, h int) string {
	bw := min(w-2, 110)
	inner := bw - 4
	labelW := 24
	inW := inner - labelW - 4
	for _, in := range []*textinput.Model{&f.name, &f.category, &f.command} {
		in.SetWidth(inW)
	}
	cur := f.cur()
	var lines []string
	row := func(field, label, value string) {
		mark := "  "
		if field == cur {
			mark = sAccent.Render("▸ ")
		}
		lines = append(lines, mark+padRight(label, labelW)+value)
		if e := f.errs[field]; e != "" {
			lines = append(lines, "  "+strings.Repeat(" ", labelW)+sErr.Render("! "+e))
		}
	}
	choice := func(opts []string, sel int) string {
		var parts []string
		for i, o := range opts {
			if i == sel {
				parts = append(parts, sSelected.Render(" "+o+" "))
			} else {
				parts = append(parts, sFaint.Render(" "+o+" "))
			}
		}
		return strings.Join(parts, " ")
	}
	toggle := func(on bool) string {
		if on {
			return sOK.Render("[x] on")
		}
		return sFaint.Render("[ ] off")
	}

	row("name", "Name", f.name.View())
	row("category", "Category", f.category.View())
	if cur == "category" {
		lines = append(lines, "  "+strings.Repeat(" ", labelW)+sFaint.Render(truncate("existing: "+strings.Join(f.categories(), " · "), inW)))
	}
	kinds := []string{"Magento", "Composer", "bin/*", "Compose", "host shell"}
	row("kind", "Kind", choice(kinds, f.kind))
	row("command", "Command", f.command.View())
	prefix := map[string]string{config.KindMagento: "bin/magento is added for you", config.KindComposer: "composer is added for you", config.KindBin: "first word is the script under bin/", config.KindCompose: "docker compose is added for you", config.KindHostShell: "runs with /bin/sh on your machine — confirms by default"}
	hint := prefix[f.kindValue()] + " · use {{name}} for prompted values"
	if c := f.command.Value(); c != "" {
		vals := map[string]string{}
		for _, p := range f.prompts {
			vals[p.key] = "{{" + p.key + "}}"
		}
		if argv, err := f.t.builder.Custom(f.build(), vals); err == nil && f.t.builder.P != nil {
			hint = "will run: " + config.JoinArgs(argv)
		}
	}
	lines = append(lines, "  "+strings.Repeat(" ", labelW)+sFaint.Render(truncate(hint, inW)))
	if len(f.prompts) == 0 {
		lines = append(lines, "  "+padRight("Prompted arguments", labelW)+sFaint.Render("none — add {{placeholder}} to the command"))
	}
	for i := range f.prompts {
		p := &f.prompts[i]
		p.label.SetWidth(inW - 12)
		p.def.SetWidth(inW - 12)
		lines = append(lines, "  "+sNote.Render(fmt.Sprintf("Prompt {{%s}}", p.key)))
		row(fmt.Sprintf("p%d.label", i), "    label", p.label.View())
		row(fmt.Sprintf("p%d.default", i), "    default", p.def.View())
		row(fmt.Sprintf("p%d.required", i), "    required", toggle(p.required))
	}
	row("confirm", "Needs confirmation?", toggle(f.confirm))
	sc := f.shortcut
	switch {
	case f.capturing:
		sc = sWarn.Render("press the key or chord to assign… (esc stops, backspace clears)")
	case sc == "":
		sc = sFaint.Render("none — enter to assign")
	default:
		sc = sBold.Render(sc) + sFaint.Render("  (enter to change, backspace to clear)")
		if b := builtinUsing(f.shortcut); b != "" {
			sc += sWarn.Render(" overrides: " + b)
		}
	}
	row("shortcut", "Keyboard shortcut", sc)
	scopeSel := 1
	if f.project && f.t.root != "" {
		scopeSel = 0
	}
	row("scope", "Scope", choice([]string{"This project only", "All projects"}, scopeSel))
	row("pin", "Pin to top of palette?", toggle(f.pinned))
	saveLbl := " Save "
	if cur == "save" {
		saveLbl = sSelected.Render(" ▶ Save ")
	}
	lines = append(lines, "", "  "+saveLbl+sFaint.Render("   (ctrl+s saves from any field)"))

	if f.conflict != nil {
		c := f.conflict
		opts := []string{"Replace", "Pick another", "Cancel"}
		lines = append(lines, "", sWarn.Render(fmt.Sprintf("  %s is already used by %s", c.key, c.desc)), "  "+choice(opts, c.sel))
		if c.custom == nil {
			lines = append(lines, sFaint.Render("  Replace makes this command win over the built-in key; the built-in stays in the palette."))
		}
	}

	// Keep the focused row visible in short terminals.
	maxRows := max(h-3, 5)
	if len(lines) > maxRows {
		focus := 0
		for i, l := range lines {
			if strings.Contains(l, "▸ ") {
				focus = i
			}
		}
		start := min(max(focus-maxRows/2, 0), len(lines)-maxRows)
		lines = lines[start : start+maxRows]
	}
	title := "New custom command"
	if f.orig != nil {
		title = "Edit custom command — " + f.orig.Cmd.Name
	} else if f.dup {
		title = "Duplicate custom command"
	}
	return overlayBox(title, strings.Join(lines, "\n"), bw)
}

var _ = sort.Strings
