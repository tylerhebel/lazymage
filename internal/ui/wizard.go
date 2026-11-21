package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"lazymage/internal/config"
	"lazymage/internal/docker"
	"lazymage/internal/project"
)

var stepTitles = map[int]string{
	1: "Project confirmation",
	2: "Container runtime",
	3: "Integration mode",
	4: "Daily commands and favorites",
	5: "Preferences",
	6: "Review and save",
}

type candidatesMsg struct{ cands []docker.Candidate }

// wizard is the first-run setup (section 4). It edits a draft config and
// writes nothing until the review step is confirmed.
type wizard struct {
	draft *config.Config
	first bool
	steps []int
	idx   int
	err   string

	// step 1
	s1cursor    int
	pathEditing bool
	pathIn      textinput.Model

	// step 2
	cands      []docker.Candidate
	candsReady bool
	rtCursor   int
	rtPicked   bool // the user moved the cursor; don't override with detection
	sub        int  // -1 none; else cursor in the not-running menu
	starting   string
	rtMsg      string

	// step 3
	modeCursor int

	// step 4
	favCursor int
	form      *customForm

	// step 5
	prefCursor int
	prefs      map[string]*textinput.Model
	follow     bool
}

var prefFields = []struct{ id, label, help string }{
	{"shell", "Container shell", "used for the PHP container shell (s); falls back to bash when the container lacks it"},
	{"editor", "Editor", "for .env and the config file; empty = $EDITOR, then vi"},
	{"browser", "Browser", "\"default\" uses the system browser, or give an app / command name"},
	{"tail", "Log lines on open", "how many past lines to show when opening logs"},
	{"follow", "Follow logs on open", "space toggles"},
	{"refresh", "Health refresh (seconds)", "how often the header health indicator updates"},
}

// startWizard opens setup. first = no config exists yet. only limits the run
// to broken steps ("runtime", "integration") for a partial re-run.
func (a *App) startWizard(first bool, only []string) tea.Cmd {
	w := &wizard{first: first, sub: -1}
	w.draft = a.cfg.Clone()
	if first && w.draft.Shell == "" {
		w.draft.Shell = a.opts.LoginShell
	}
	if len(only) > 0 {
		for _, s := range only {
			switch s {
			case "runtime":
				w.steps = append(w.steps, 2)
			case "integration":
				w.steps = append(w.steps, 3)
			}
		}
	} else {
		w.steps = []int{1, 2, 3, 4, 5, 6}
	}
	w.modeCursor = max(indexOf(config.Modes, w.draft.IntegrationMode), 0)
	w.pathIn = newInput("path ▸ ", "~/code/my-magento-project")
	w.prefs = map[string]*textinput.Model{}
	for _, f := range prefFields {
		if f.id == "follow" {
			continue
		}
		in := newInput("", "")
		w.prefs[f.id] = &in
	}
	w.prefs["shell"].SetValue(w.draft.Shell)
	w.prefs["editor"].SetValue(w.draft.Editor)
	w.prefs["browser"].SetValue(w.draft.Browser)
	w.prefs["tail"].SetValue(strconv.Itoa(w.draft.Logs.TailLines))
	w.prefs["refresh"].SetValue(strconv.Itoa(w.draft.RefreshSeconds))
	w.follow = w.draft.Logs.FollowOnOpen
	if first {
		w.follow = true
	}
	a.wiz = w
	a.screen = screenSetup
	if a.probed {
		return w.onProbe(a)
	}
	return nil
}

func (w *wizard) step() int { return w.steps[w.idx] }

// onProbe refreshes runtime candidates after docker info changes.
func (w *wizard) onProbe(a *App) tea.Cmd {
	info := a.info
	return func() tea.Msg { return candidatesMsg{docker.Candidates(info)} }
}

func (w *wizard) setCandidates(a *App, cands []docker.Candidate) {
	w.cands = cands
	w.candsReady = true
	if w.starting != "" {
		for _, c := range cands {
			if c.Runtime == w.starting && c.Running {
				w.draft.Runtime = w.starting
				w.starting = ""
				w.sub = -1
				w.rtMsg = sOK.Render(runtimeLabel(c.Runtime) + " is running.")
				w.advance(a)
				return
			}
		}
		if !a.engineStarting {
			w.rtMsg = sErr.Render(runtimeLabel(w.starting)+" did not come up.") + " " + sFaint.Render(a.info.Err)
			w.starting = ""
		}
	}
	if w.rtPicked {
		return
	}
	pick := -1
	for i, c := range cands {
		if c.Runtime == w.draft.Runtime && w.draft.Runtime != "" && (c.Installed || c.Running) {
			pick = i // re-run: current value pre-selected
		}
	}
	if pick < 0 {
		for i, c := range cands {
			if c.Running {
				pick = i
			}
		}
	}
	if pick < 0 && cands[0].Installed {
		pick = 0 // OrbStack is the default when detected
	}
	if pick < 0 {
		for i, c := range cands {
			if c.Installed {
				pick = i
				break
			}
		}
	}
	w.rtCursor = max(pick, 0)
}

func (w *wizard) advance(a *App) tea.Cmd {
	w.err = ""
	if w.idx+1 < len(w.steps) {
		w.idx++
		return w.enterStep(a)
	}
	return w.finish(a)
}

func (w *wizard) back() {
	w.err = ""
	if w.idx > 0 {
		w.idx--
	}
}

func (w *wizard) enterStep(a *App) tea.Cmd {
	if w.step() == 5 {
		return w.focusPref()
	}
	return nil
}

// finish writes the config and opens the main TUI.
func (w *wizard) finish(a *App) tea.Cmd {
	if !contains(config.Runtimes, w.draft.Runtime) {
		w.err = "pick a container runtime first (step 2)"
		w.idx = indexOfInt(w.steps, 2)
		if w.idx < 0 {
			w.idx = 0
		}
		return nil
	}
	if err := w.draft.Save(a.cfgPath); err != nil {
		w.err = "could not save config: " + err.Error()
		return nil
	}
	a.cfg = w.draft
	a.cfgLocked = false
	a.wiz = nil
	a.screen = screenMain
	a.mage.refreshItems(a)
	a.comp.refreshItems(a)
	return tea.Batch(a.setStatus("setup saved — your choices live in "+a.cfgPath, false), a.probeCmd(), a.servicesCmd())
}

func indexOfInt(xs []int, v int) int {
	for i, x := range xs {
		if x == v {
			return i
		}
	}
	return -1
}

func (w *wizard) update(a *App, msg tea.Msg) tea.Cmd {
	if m, ok := msg.(candidatesMsg); ok {
		w.setCandidates(a, m.cands)
		return nil
	}
	if w.form != nil {
		cmd := w.form.update(a, msg)
		if w.form.done {
			w.form = nil
		}
		return cmd
	}
	k, isKey := msg.(tea.KeyPressMsg)
	if isKey && k.String() == "ctrl+c" {
		if w.first {
			return tea.Quit // nothing has been written
		}
		a.wiz = nil
		a.screen = screenMain
		return a.setStatus("setup cancelled — nothing changed", false)
	}
	switch w.step() {
	case 1:
		return w.updateProject(a, msg)
	case 2:
		if isKey {
			return w.updateRuntime(a, k)
		}
	case 3:
		if isKey {
			return w.updateMode(a, k)
		}
	case 4:
		if isKey {
			return w.updateFavorites(a, k)
		}
	case 5:
		return w.updatePrefs(a, msg)
	case 6:
		if isKey {
			switch k.String() {
			case "enter":
				return w.finish(a)
			case "esc":
				w.back()
				return w.enterStep(a)
			case "1", "2", "3", "4", "5":
				n := int(k.String()[0] - '0')
				if i := indexOfInt(w.steps, n); i >= 0 {
					w.idx = i
					return w.enterStep(a)
				}
			}
		}
	}
	return nil
}

func (w *wizard) updateProject(a *App, msg tea.Msg) tea.Cmd {
	if w.pathEditing {
		if k, ok := msg.(tea.KeyPressMsg); ok {
			switch k.String() {
			case "esc":
				w.pathEditing = false
				w.pathIn.Blur()
				return nil
			case "enter":
				p := strings.TrimSpace(w.pathIn.Value())
				if strings.HasPrefix(p, "~") {
					home, _ := os.UserHomeDir()
					p = filepath.Join(home, strings.TrimPrefix(p, "~"))
				}
				w.pathEditing = false
				w.pathIn.Blur()
				if p == "" {
					return nil
				}
				if a.detectProject(p) {
					a.startDir = p
					a.loadFactsCache()
					w.err = ""
					return tea.Batch(a.servicesCmd(), a.probeCmd())
				}
				w.err = "no docker-magento project at or above " + p
				return nil
			}
		}
		var cmd tea.Cmd
		w.pathIn, cmd = w.pathIn.Update(msg)
		return cmd
	}
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	if a.proj != nil {
		switch k.String() {
		case "enter":
			return w.advance(a)
		case "c":
			w.pathEditing = true
			return w.pathIn.Focus()
		}
		return nil
	}
	switch k.String() {
	case "j", "down", "k", "up", "tab":
		w.s1cursor = 1 - w.s1cursor
	case "enter":
		if w.s1cursor == 0 {
			w.pathEditing = true
			return w.pathIn.Focus()
		}
		return w.advance(a) // project-less preview mode
	}
	return nil
}

var subOptions = []string{"Start it now", "Continue anyway", "Pick a different runtime"}

func (w *wizard) updateRuntime(a *App, k tea.KeyPressMsg) tea.Cmd {
	if !w.candsReady || len(w.cands) == 0 {
		if k.String() == "esc" {
			w.back()
		}
		return nil
	}
	if w.starting != "" {
		return nil
	}
	if w.sub >= 0 {
		switch k.String() {
		case "j", "down", "tab":
			w.sub = (w.sub + 1) % 3
		case "k", "up", "shift+tab":
			w.sub = (w.sub + 2) % 3
		case "esc":
			w.sub = -1
		case "enter":
			rt := w.cands[w.rtCursor].Runtime
			switch w.sub {
			case 0:
				p := docker.StartPlanFor(rt)
				if p.ShowOnly != "" {
					w.rtMsg = "lazymage does not start this engine itself. Run: " + sBold.Render(p.ShowOnly) + " — then choose Continue anyway or press r to re-check."
					return nil
				}
				w.starting = rt
				w.rtMsg = sWarn.Render("Starting " + runtimeLabel(rt) + "… ($ " + config.JoinArgs(p.Argv) + ")")
				return a.startEngineFor(rt)
			case 1:
				w.draft.Runtime = rt
				w.sub = -1
				return w.advance(a)
			case 2:
				w.sub = -1
			}
		}
		return nil
	}
	switch k.String() {
	case "j", "down":
		w.rtCursor = min(w.rtCursor+1, len(w.cands)-1)
		w.rtPicked = true
		w.rtMsg = ""
	case "k", "up":
		w.rtCursor = max(w.rtCursor-1, 0)
		w.rtPicked = true
		w.rtMsg = ""
	case "r":
		w.candsReady = false
		return a.probeCmd()
	case "esc":
		w.back()
	case "enter":
		c := w.cands[w.rtCursor]
		if c.Running {
			w.draft.Runtime = c.Runtime
			return w.advance(a)
		}
		w.sub = 0
	}
	return nil
}

func (w *wizard) updateMode(a *App, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "j", "down":
		w.modeCursor = min(w.modeCursor+1, len(config.Modes)-1)
	case "k", "up":
		w.modeCursor = max(w.modeCursor-1, 0)
	case "esc":
		w.back()
	case "enter":
		w.draft.IntegrationMode = config.Modes[w.modeCursor]
		return w.advance(a)
	}
	return nil
}

func (w *wizard) updateFavorites(a *App, k tea.KeyPressMsg) tea.Cmd {
	ch := favoriteChoices(w.draft)
	switch k.String() {
	case "j", "down":
		w.favCursor = min(w.favCursor+1, len(ch)-1)
	case "k", "up":
		w.favCursor = max(w.favCursor-1, 0)
	case "space":
		w.draft.ToggleFavorite(ch[w.favCursor])
	case "n":
		t := formTarget{cfg: w.draft, root: a.root(), builder: a.builder(), onSave: func(cc config.CustomCommand, project bool) tea.Cmd {
			return a.setStatus("added "+cc.Name+" — saved with the rest of setup", false)
		}}
		w.form = newCustomFormFor(t, nil, false)
	case "esc":
		w.back()
	case "enter":
		return w.advance(a)
	}
	return nil
}

func (w *wizard) focusPref() tea.Cmd {
	for _, in := range w.prefs {
		in.Blur()
	}
	if in := w.prefs[prefFields[w.prefCursor].id]; in != nil {
		return in.Focus()
	}
	return nil
}

func (w *wizard) updatePrefs(a *App, msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "down", "tab":
			w.prefCursor = min(w.prefCursor+1, len(prefFields)-1)
			return w.focusPref()
		case "up", "shift+tab":
			w.prefCursor = max(w.prefCursor-1, 0)
			return w.focusPref()
		case "esc":
			w.back()
			return nil
		case "space":
			if prefFields[w.prefCursor].id == "follow" {
				w.follow = !w.follow
				return nil
			}
		case "enter":
			return w.commitPrefs(a)
		}
	}
	if in := w.prefs[prefFields[w.prefCursor].id]; in != nil {
		var cmd tea.Cmd
		*in, cmd = in.Update(msg)
		return cmd
	}
	return nil
}

func (w *wizard) commitPrefs(a *App) tea.Cmd {
	tail, err1 := strconv.Atoi(strings.TrimSpace(w.prefs["tail"].Value()))
	ref, err2 := strconv.Atoi(strings.TrimSpace(w.prefs["refresh"].Value()))
	if err1 != nil || tail <= 0 {
		w.err = "Log lines on open must be a positive whole number"
		return nil
	}
	if err2 != nil || ref <= 0 {
		w.err = "Health refresh must be a positive whole number of seconds"
		return nil
	}
	d := w.draft
	d.Shell = strings.TrimSpace(w.prefs["shell"].Value())
	if d.Shell == "" {
		d.Shell = "bash"
	}
	d.Editor = strings.TrimSpace(w.prefs["editor"].Value())
	d.Browser = strings.TrimSpace(w.prefs["browser"].Value())
	if d.Browser == "" {
		d.Browser = "default"
	}
	d.Logs.TailLines, d.RefreshSeconds, d.Logs.FollowOnOpen = tail, ref, w.follow
	return w.advance(a)
}

// --- view ------------------------------------------------------------------------------

func (w *wizard) view(a *App, width, height int) string {
	inner := width - 6
	var b strings.Builder
	n := w.step()
	progress := ""
	for i := range w.steps {
		if i <= w.idx {
			progress += "●"
		} else {
			progress += "○"
		}
	}
	title := "lazymage setup"
	if len(w.steps) < 6 {
		title += " — fixing a saved choice that is no longer valid"
	}
	b.WriteString(sAccent.Render(title) + "   " + progress + "  " + sFaint.Render(fmt.Sprintf("step %d of %d", w.idx+1, len(w.steps))) + "\n")
	b.WriteString(sBold.Render(fmt.Sprintf("Step %d — %s", n, stepTitles[n])) + "\n\n")

	if w.form != nil {
		b.WriteString(w.form.view(a, inner, height-8))
	} else {
		switch n {
		case 1:
			b.WriteString(w.viewProject(a, inner))
		case 2:
			b.WriteString(w.viewRuntime(a, inner))
		case 3:
			b.WriteString(w.viewMode(inner))
		case 4:
			b.WriteString(w.viewFavorites(a, height-12))
		case 5:
			b.WriteString(w.viewPrefs(inner))
		case 6:
			b.WriteString(w.viewReview(a))
		}
	}
	if w.err != "" {
		b.WriteString("\n\n" + sErr.Render("! "+w.err))
	}
	body := b.String()
	hints := w.hints(a)
	lines := strings.Split(body, "\n")
	maxBody := height - 5
	if len(lines) > maxBody {
		lines = lines[:maxBody]
	}
	for len(lines) < maxBody {
		lines = append(lines, "")
	}
	for i, l := range lines {
		lines[i] = truncateStyled(l, inner)
	}
	lines = append(lines, sFaint.Render(strings.Repeat("─", inner)), truncateStyled(hints, inner))
	return sBorder.Width(width).Padding(0, 2).Render(strings.Join(lines, "\n"))
}

func (w *wizard) hints(a *App) string {
	if w.form != nil {
		return w.form.hints()
	}
	quit := "exit without saving"
	if !w.first {
		quit = "cancel setup"
	}
	common := []string{"ctrl+c", quit}
	switch w.step() {
	case 1:
		if w.pathEditing {
			return keyHint("enter", "look for a project there", "esc", "back")
		}
		if a.proj != nil {
			return keyHint(append([]string{"enter", "confirm and continue", "c", "use a different directory"}, common...)...)
		}
		return keyHint(append([]string{"j/k", "choose", "enter", "select"}, common...)...)
	case 2:
		if w.sub >= 0 {
			return keyHint(append([]string{"j/k", "choose", "enter", "select", "esc", "back to list"}, common...)...)
		}
		return keyHint(append([]string{"j/k", "choose", "enter", "use this runtime", "r", "re-detect", "esc", "back"}, common...)...)
	case 3:
		return keyHint(append([]string{"j/k", "choose", "enter", "continue", "esc", "back"}, common...)...)
	case 4:
		return keyHint(append([]string{"space", "toggle favorite", "n", "add a custom command", "enter", "continue (or skip)", "esc", "back"}, common...)...)
	case 5:
		return keyHint(append([]string{"↑/↓", "field", "type", "edit", "space", "toggle", "enter", "continue", "esc", "back"}, common...)...)
	case 6:
		return keyHint(append([]string{"enter", "save and open lazymage", "esc", "back", "1–5", "jump to a step"}, common...)...)
	}
	return ""
}

func (w *wizard) viewProject(a *App, inner int) string {
	var b strings.Builder
	if a.proj != nil {
		f := a.facts
		rows := [][2]string{
			{"Project root", a.proj.Root},
			{"Project name", a.proj.Name},
			{"Domain", project.Or(f.Domain) + srcNote(f.DomainSource)},
			{"Magento version", project.Or(f.MagentoVersion) + srcNote(f.VersionSource)},
			{"Deploy mode", project.Or(f.DeployMode) + srcNote(f.ModeSource)},
		}
		for _, r := range rows {
			b.WriteString("  " + padRight(r[0], 18) + r[1] + "\n")
		}
		b.WriteString("\n" + sFaint.Render("Values marked unknown fill in from bin/magento once the engine is running."))
	} else {
		b.WriteString(sErr.Render("No docker-magento project found.") + "\n\n")
		b.WriteString("Started from: " + a.startDir + "\n")
		b.WriteString("Looked here and in every parent directory for a folder containing all of:\n")
		for _, m := range project.Markers {
			b.WriteString("  • " + m + "\n")
		}
		b.WriteString("\n")
		opts := []string{"Look in a different directory…", "Continue without a project (preview mode)"}
		for i, o := range opts {
			if i == w.s1cursor {
				b.WriteString(sSelected.Render("▶ "+o) + "\n")
			} else {
				b.WriteString("  " + o + "\n")
			}
		}
		b.WriteString("\n" + sFaint.Render("lazymage never creates a project. Preview mode saves your settings; panels stay empty until you launch inside a project."))
	}
	if w.pathEditing {
		w.pathIn.SetWidth(inner - 10)
		b.WriteString("\n\n" + w.pathIn.View())
	}
	return b.String()
}

func srcNote(s string) string {
	if s == "" {
		return ""
	}
	return sFaint.Render("  (from " + s + ")")
}

func (w *wizard) viewRuntime(a *App, inner int) string {
	var b strings.Builder
	if !w.candsReady {
		return "Detecting runtimes (docker info, docker context)…"
	}
	ctx := a.info.Context
	if ctx == "" {
		ctx = "unknown"
	}
	cli := a.info.CLIVersion
	if cli == "" {
		cli = "not found"
	}
	b.WriteString(sFaint.Render(fmt.Sprintf("docker CLI %s · active context %s · engine %s", cli, ctx, map[bool]string{true: "reachable", false: "not reachable"}[a.info.Reachable])) + "\n\n")
	for i, c := range w.cands {
		state := sFaint.Render("not detected")
		switch {
		case c.Running:
			state = sOK.Render("● running")
		case c.Installed:
			state = sWarn.Render("○ installed, not running")
		}
		label := padRight(runtimeLabel(c.Runtime), 16) + " " + state
		if c.Runtime == config.RuntimeOrbStack {
			label += sFaint.Render("  (recommended on macOS)")
		}
		if i == w.rtCursor {
			b.WriteString(sSelected.Render("▶ "+stripANSI(label)) + "\n")
		} else {
			b.WriteString("  " + label + "\n")
		}
		for _, e := range c.Evidence {
			b.WriteString(sFaint.Render("      "+truncate(e, inner-8)) + "\n")
		}
	}
	if w.sub >= 0 {
		rt := w.cands[w.rtCursor].Runtime
		b.WriteString("\n" + sWarn.Render(runtimeLabel(rt)+" is not running.") + "\n")
		for i, o := range subOptions {
			if i == 0 {
				o += sFaint.Render("  " + engineStartDesc(rt))
			}
			if i == w.sub {
				b.WriteString(sSelected.Render("▶ "+stripANSI(o)) + "\n")
			} else {
				b.WriteString("  " + o + "\n")
			}
		}
	}
	if w.rtMsg != "" {
		b.WriteString("\n" + w.rtMsg)
	}
	return b.String()
}

var modeHelp = map[string]string{
	config.ModeHybrid:     "Actions use the project's bin/* wrappers; status and logs use docker compose directly for structured, streaming output.",
	config.ModeBinOnly:    "Everything goes through bin/* scripts and plain docker compose ps text. Most upstream-faithful; status parsing is more fragile.",
	config.ModeDockerOnly: "Everything uses docker compose and docker compose exec phpfpm … directly, ignoring the bin/* wrappers.",
}

func (w *wizard) viewMode(inner int) string {
	var b strings.Builder
	b.WriteString("How should lazymage run commands?\n\n")
	for i, m := range config.Modes {
		label := modeLabel(m)
		if i == w.modeCursor {
			b.WriteString(sSelected.Render("▶ "+padRight(label, 24)) + "\n")
		} else {
			b.WriteString("  " + label + "\n")
		}
		for _, l := range wrap(modeHelp[m], inner-8) {
			b.WriteString(sFaint.Render("    "+l) + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (w *wizard) viewFavorites(a *App, rows int) string {
	var b strings.Builder
	b.WriteString("Favorites are shown first in the Magento tab and the palette.\n\n")
	b.WriteString(favoritesList(w.draft, w.favCursor, max(rows-6, 4)) + "\n")
	var customs []string
	for _, c := range w.draft.CustomCommands {
		customs = append(customs, c.Name+" (global)")
	}
	if a.proj != nil {
		if p := w.draft.Projects[a.proj.Root]; p != nil {
			for _, c := range p.CustomCommands {
				customs = append(customs, c.Name+" (project)")
			}
		}
	}
	b.WriteString("\n" + fmt.Sprintf("Custom commands: %d", len(customs)))
	if len(customs) > 0 {
		b.WriteString(sFaint.Render(" — " + strings.Join(customs, ", ")))
	}
	return b.String()
}

func (w *wizard) viewPrefs(inner int) string {
	var b strings.Builder
	for i, f := range prefFields {
		mark := "  "
		if i == w.prefCursor {
			mark = sAccent.Render("▸ ")
		}
		var val string
		if f.id == "follow" {
			val = map[bool]string{true: sOK.Render("[x] on"), false: sFaint.Render("[ ] off")}[w.follow]
		} else {
			w.prefs[f.id].SetWidth(40)
			val = w.prefs[f.id].View()
		}
		b.WriteString(mark + padRight(f.label, 26) + val + "\n")
		b.WriteString(sFaint.Render("    "+padRight("", 24)+truncate(f.help, inner-30)) + "\n")
	}
	return b.String()
}

func (w *wizard) viewReview(a *App) string {
	d := w.draft
	proj := "none (preview mode)"
	if a.proj != nil {
		proj = a.proj.Name + sFaint.Render("  "+a.proj.Root)
	}
	editor := d.Editor
	if editor == "" {
		editor = "$EDITOR, then vi"
	}
	nCustom := len(d.CustomCommands)
	if a.proj != nil && d.Projects[a.proj.Root] != nil {
		nCustom += len(d.Projects[a.proj.Root].CustomCommands)
	}
	rows := [][2]string{
		{"1 Project", proj},
		{"2 Runtime", runtimeLabel(d.Runtime)},
		{"3 Integration mode", modeLabel(d.IntegrationMode)},
		{"4 Favorites", fmt.Sprintf("%d selected · %d custom command(s)", len(d.Favorites), nCustom)},
		{"5 Shell", d.Shell},
		{"  Editor", editor},
		{"  Browser", d.Browser},
		{"  Logs", fmt.Sprintf("%d lines on open, follow %s", d.Logs.TailLines, map[bool]string{true: "on", false: "off"}[d.Logs.FollowOnOpen])},
		{"  Health refresh", fmt.Sprintf("every %ds", d.RefreshSeconds)},
	}
	var b strings.Builder
	for _, r := range rows {
		b.WriteString("  " + padRight(r[0], 22) + r[1] + "\n")
	}
	b.WriteString("\nSaved to " + sBold.Render(a.cfgPath))
	return lipgloss.NewStyle().Render(b.String())
}
