package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"lazymage/internal/config"
	"lazymage/internal/docker"
	"lazymage/internal/plan"
	"lazymage/internal/project"
	"lazymage/internal/runner"
	"lazymage/internal/update"
)

const (
	minWidth  = 100
	minHeight = 30
)

type screenKind int

const (
	screenMain screenKind = iota
	screenSetup
	screenMalformed
)

type engineState int

const (
	engineUnknown engineState = iota
	engineRunning
	engineStopped
)

// Options configure a new App.
type Options struct {
	Version    string
	StartDir   string
	ForceSetup bool
	Debug      bool
	LoginShell string
}

// App is the root Bubble Tea model. It is used by pointer so overlays and
// helpers can act on shared state directly.
type App struct {
	opts Options
	send func(tea.Msg)
	log  *debugLog

	cfg      *config.Config
	cfgPath  string
	parseErr *config.ParseError
	// cfgLocked blocks saves after a hand edit left the file unparseable.
	cfgLocked bool
	// checkRuntimeInstalled re-opens the runtime step if the saved runtime
	// turns out not to be installed (checked once after the first probe).
	checkRuntimeInstalled bool

	startDir string
	proj     *project.Project
	facts    project.Facts

	info       docker.Info
	probed     bool
	engine     engineState
	health     docker.Summary
	containers []docker.Container
	services   []string
	psBusy     bool
	psErr      string

	engineStarting bool
	engineDeadline time.Time
	engineNote     string

	w, h   int
	screen screenKind
	wiz    *wizard

	tab   int // 0 logs, 1 magento, 2 composer
	logs  *logsModel
	mage  *cmdTab
	comp  *cmdTab
	out   *outputView
	showO bool

	run    *runner.Run
	runSeq int

	overlays []overlay

	status    string
	statusErr bool
	statusSeq int
	recent    []string

	lastCmd  string
	lastExit string

	updateAvail update.Release // newer release found by the daily check
	updating    bool
	restartExe  string // set by "Restart now" after an update

	lastGruntTheme string
}

// overlay is a modal layer drawn over the main panel.
type overlay interface {
	update(a *App, msg tea.Msg) tea.Cmd
	view(a *App, w, h int) string
}

// New builds the app: loads config, detects the project, decides the first screen.
func New(opts Options) *App {
	a := &App{opts: opts, startDir: opts.StartDir, cfgPath: config.Path()}
	a.log = openDebugLog(opts.Debug)
	a.detectProject(opts.StartDir)
	a.logs = newLogsModel()
	a.mage = newCmdTab(config.KindMagento)
	a.comp = newCmdTab(config.KindComposer)

	cfg, err := config.Load(a.cfgPath)
	switch {
	case err == nil:
		a.cfg = cfg
		for _, w := range cfg.Warnings {
			a.log.printf("config warning: %s", w)
		}
		if len(cfg.Warnings) > 0 {
			a.setStatus(fmt.Sprintf("%d malformed custom command(s) skipped — see %s", len(cfg.Warnings), a.cfgPath), true)
		}
		if opts.ForceSetup {
			a.startWizard(false, nil)
		} else if steps := cfg.InvalidSteps(); len(steps) > 0 {
			a.startWizard(false, steps)
		} else {
			a.checkRuntimeInstalled = true
		}
	case config.IsNotExist(err):
		a.cfg = config.Defaults(opts.LoginShell)
		a.startWizard(true, nil)
	default:
		if pe, ok := err.(*config.ParseError); ok {
			a.parseErr = pe
		} else {
			a.parseErr = &config.ParseError{Path: a.cfgPath, Err: err}
		}
		a.cfg = config.Defaults(opts.LoginShell)
		a.screen = screenMalformed
	}
	a.loadFactsCache()
	a.mage.refreshItems(a)
	a.comp.refreshItems(a)
	return a
}

// SetSender wires tea.Program.Send for background goroutines.
func (a *App) SetSender(send func(tea.Msg)) { a.send = send }

func (a *App) detectProject(dir string) bool {
	p, ok := project.FindRoot(dir)
	if !ok {
		a.proj = nil
		a.facts = project.Facts{}
		return false
	}
	a.proj = p
	a.facts = p.StaticFacts()
	return true
}

func (a *App) builder() plan.Builder {
	mode := config.ModeHybrid
	if a.cfg != nil && a.cfg.IntegrationMode != "" {
		mode = a.cfg.IntegrationMode
	}
	b := plan.New(a.proj, mode, a.facts.PHPService)
	b.DBService = plan.DBServiceFrom(a.services)
	return b
}

func (a *App) saveConfig() bool {
	if a.cfgLocked {
		a.setStatus("config file has a parse error — not saved; fix "+a.cfgPath+" and reopen it from the palette", true)
		return false
	}
	if err := a.cfg.Save(a.cfgPath); err != nil {
		a.setStatus("could not save config: "+err.Error(), true)
		return false
	}
	return true
}

// --- tea.Model ---------------------------------------------------------------

func (a *App) Init() tea.Cmd {
	return tea.Batch(a.probeCmd(), a.servicesCmd(), a.tickCmd(), a.autoUpdateCheck())
}

type statusClearMsg struct{ seq int }

func (a *App) setStatus(s string, isErr bool) tea.Cmd {
	a.status, a.statusErr = s, isErr
	a.statusSeq++
	seq := a.statusSeq
	a.log.printf("status: %s", s)
	return tea.Tick(8*time.Second, func(time.Time) tea.Msg { return statusClearMsg{seq} })
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.w, a.h = msg.Width, msg.Height
		return a, nil
	case statusClearMsg:
		if msg.seq == a.statusSeq {
			a.status = ""
		}
		return a, nil
	case runner.Chunk:
		a.routeChunk(msg)
		return a, nil
	case runner.Exited:
		return a, a.routeExit(msg)
	}
	if cmd, handled := a.handleBackground(msg); handled {
		return a, cmd
	}

	switch a.screen {
	case screenSetup:
		return a, a.wiz.update(a, msg)
	case screenMalformed:
		return a, a.updateMalformed(msg)
	}

	if len(a.overlays) > 0 {
		top := a.overlays[len(a.overlays)-1]
		return a, top.update(a, msg)
	}
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return a, a.handleKey(msg)
	case tea.PasteMsg:
		return a, a.handlePaste(msg)
	}
	return a, nil
}

func (a *App) push(o overlay) { a.overlays = append(a.overlays, o) }

func (a *App) pop() {
	if n := len(a.overlays); n > 0 {
		a.overlays = a.overlays[:n-1]
	}
}

// remove pops a specific overlay wherever it is in the stack.
func (a *App) remove(o overlay) {
	for i := len(a.overlays) - 1; i >= 0; i-- {
		if a.overlays[i] == o {
			a.overlays = append(a.overlays[:i], a.overlays[i+1:]...)
			return
		}
	}
}

// handleKey is the main-screen dispatcher.
func (a *App) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()

	// The running command's output view owns the keyboard (stdin line).
	if a.showO && a.out != nil {
		if cmd, used := a.out.update(a, msg); used {
			return cmd
		}
	}
	if a.proj == nil {
		return a.handleNoProjectKey(key)
	}
	if a.engine == engineStopped && !a.showO {
		if key == "enter" {
			return a.do(aEngineStart)
		}
	}

	// A focused text input in a tab takes printable keys first.
	if !a.showO {
		switch a.tab {
		case 0:
			if cmd, used := a.logs.updateInput(a, msg); used {
				return cmd
			}
		case 1:
			if cmd, used := a.mage.update(a, msg, true); used {
				return cmd
			}
		case 2:
			if cmd, used := a.comp.update(a, msg, true); used {
				return cmd
			}
		}
	}

	// Custom command shortcuts take precedence over built-in keys they replaced.
	if sc, ok := a.customByShortcut(key); ok {
		return a.runCustom(sc)
	}
	ctx := "commands"
	if a.tab == 0 {
		ctx = "logs"
	}
	if act := actionForKey(key, ctx); act != "" {
		return a.do(act)
	}
	if a.showO {
		return nil
	}
	switch a.tab {
	case 0:
		return a.logs.update(a, msg)
	case 1:
		cmd, _ := a.mage.update(a, msg, false)
		return cmd
	case 2:
		cmd, _ := a.comp.update(a, msg, false)
		return cmd
	}
	return nil
}

func (a *App) handlePaste(msg tea.PasteMsg) tea.Cmd {
	if a.showO && a.out != nil {
		cmd, _ := a.out.update(a, msg)
		return cmd
	}
	switch a.tab {
	case 1:
		cmd, _ := a.mage.update(a, msg, true)
		return cmd
	case 2:
		cmd, _ := a.comp.update(a, msg, true)
		return cmd
	}
	return nil
}

func (a *App) handleNoProjectKey(key string) tea.Cmd {
	switch key {
	case "r":
		if a.detectProject(a.startDir) {
			a.mage.refreshItems(a)
			a.comp.refreshItems(a)
			return tea.Batch(a.setStatus("project found: "+a.proj.Root, false), a.servicesCmd(), a.probeCmd())
		}
		return a.setStatus("still no docker-magento project at or above "+a.startDir, true)
	}
	if act := actionForKey(key, "global"); act != "" {
		return a.do(act)
	}
	return nil
}

// --- View -----------------------------------------------------------------------

func (a *App) View() tea.View {
	var body string
	switch {
	case a.w == 0:
		body = "lazymage — starting…"
	case a.w < 40 || a.h < 12:
		body = fmt.Sprintf("lazymage needs at least %dx%d (now %dx%d)", minWidth, minHeight, a.w, a.h)
	case a.screen == screenSetup:
		body = a.wiz.view(a, a.w, a.h)
	case a.screen == screenMalformed:
		body = a.viewMalformed()
	default:
		body = a.viewMain()
	}
	v := tea.NewView(body)
	v.AltScreen = true
	v.WindowTitle = "lazymage"
	if a.proj != nil {
		v.WindowTitle = "lazymage · " + a.proj.Name
	}
	return v
}

func (a *App) viewMain() string {
	header := a.viewHeader()
	footer := a.viewFooter()
	tabs := a.viewTabs()
	mainH := a.h - lipgloss.Height(header) - lipgloss.Height(footer) - lipgloss.Height(tabs)
	innerW, innerH := a.w-4, mainH-2

	var content string
	switch {
	case len(a.overlays) > 0:
		top := a.overlays[len(a.overlays)-1]
		content = center(top.view(a, innerW, innerH), innerW, innerH)
	case a.proj == nil:
		content = a.viewNoProject(innerW, innerH)
	case a.showO && a.out != nil:
		content = a.out.view(a, innerW, innerH)
	case a.engine == engineStopped:
		content = a.viewEngineStopped(innerW, innerH)
	default:
		switch a.tab {
		case 0:
			content = a.logs.view(a, innerW, innerH)
		case 1:
			content = a.mage.view(a, innerW, innerH)
		case 2:
			content = a.comp.view(a, innerW, innerH)
		}
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, tabs, box(content, a.w, mainH), footer)
}

func (a *App) viewTabs() string {
	names := []string{"1 Logs", "2 Magento", "3 Composer"}
	var parts []string
	for i, n := range names {
		if i == a.tab && !a.showO {
			parts = append(parts, sTabActive.Render(n))
		} else {
			parts = append(parts, sTab.Render(n))
		}
	}
	if a.out != nil {
		label := "Output"
		if a.run != nil {
			label = "● Running"
		} else if a.out.exit != nil {
			label = fmt.Sprintf("Output (exit %d)", a.out.exit.Code)
		}
		if a.showO {
			parts = append(parts, sTabActive.Render(label))
		} else {
			parts = append(parts, sTab.Render(label+" — v"))
		}
	}
	line := strings.Join(parts, " ")
	if a.w < minWidth || a.h < minHeight {
		line += sWarn.Render(fmt.Sprintf("  terminal %dx%d is below %dx%d", a.w, a.h, minWidth, minHeight))
	}
	return truncateStyled(line, a.w)
}

func (a *App) viewNoProject(w, h int) string {
	var b strings.Builder
	b.WriteString(sErr.Render("Not a docker-magento project") + "\n\n")
	b.WriteString("Started from: " + a.startDir + "\n\n")
	b.WriteString("lazymage looked here and in every parent directory for a folder containing all of:\n")
	for _, m := range project.Markers {
		b.WriteString("  • " + m + "\n")
	}
	b.WriteString("\n" + keyHint("cd", "into a project and relaunch", "r", "retry detection", "x", "palette", "S", "settings", "q", "quit"))
	return b.String()
}

func (a *App) viewEngineStopped(w, h int) string {
	var b strings.Builder
	b.WriteString(sErr.Render("Engine: stopped") + "\n\n")
	if a.info.Err != "" {
		b.WriteString(sFaint.Render("docker info: "+a.info.Err) + "\n\n")
	}
	if a.engineStarting {
		b.WriteString(sWarn.Render(fmt.Sprintf("Starting %s… waiting for docker info (%ds left)", runtimeLabel(a.cfg.Runtime), int(time.Until(a.engineDeadline).Seconds()))) + "\n")
	} else {
		b.WriteString(sSelected.Render(" ▶ Start engine ") + "  " + sFaint.Render(engineStartDesc(a.cfg.Runtime)) + "\n\n")
		b.WriteString(keyHint("enter", "start engine", "r", "re-check", "x", "palette", "q", "quit"))
	}
	if a.engineNote != "" {
		b.WriteString("\n\n" + a.engineNote)
	}
	return b.String()
}

func (a *App) viewHeader() string {
	inner := a.w - 4
	name := "no project"
	if a.proj != nil {
		name = a.proj.Name
	}
	ver := project.Or(a.facts.MagentoVersion)
	if ver != project.Unknown {
		ver = "Magento " + ver
	} else {
		ver = "Magento unknown"
	}
	rt := runtimeLabel(a.cfg.Runtime)
	ctx := project.Or(a.info.Context)
	sep := sFaint.Render(" │ ")
	line1 := sAccent.Render(name) + sep + project.Or(a.facts.Domain) + sep + ver + sep +
		project.Or(a.facts.DeployMode) + sep + rt + sFaint.Render(" · "+ctx)
	hl := a.healthLabel()
	gap := inner - lipgloss.Width(line1) - lipgloss.Width(hl)
	if gap < 2 {
		line1 = truncateStyled(line1, inner-lipgloss.Width(hl)-2)
		gap = 2
	}
	line1 += strings.Repeat(" ", gap) + hl

	var l2 []string
	if a.facts.PHPVersion != "" {
		l2 = append(l2, "PHP "+a.facts.PHPVersion)
	}
	if a.facts.SearchEngine != "" {
		l2 = append(l2, a.facts.SearchEngine)
	}
	if a.facts.AdminPath != "" {
		l2 = append(l2, "admin /"+a.facts.AdminPath)
	}
	l2 = append(l2, "lazymage "+a.opts.Version, "integration "+a.cfg.IntegrationMode)
	if a.proj != nil {
		l2 = append(l2, a.proj.Root)
	}
	line2 := sFaint.Render(strings.Join(l2, " · "))
	if a.updateAvail.Tag != "" {
		note := sWarn.Render("⬆ " + a.updateAvail.Tag + " available — U to update")
		line2 = truncateStyled(line2, max(inner-lipgloss.Width(note)-2, 0))
		line2 += strings.Repeat(" ", max(inner-lipgloss.Width(line2)-lipgloss.Width(note), 2)) + note
	}
	return sBorder.Width(a.w).Padding(0, 1).Render(line1 + "\n" + truncateStyled(line2, inner))
}

// healthLabel uses a symbol, a colour, and text — colour is never the only signal.
func (a *App) healthLabel() string {
	switch {
	case a.engine == engineStopped:
		return sErr.Render("■ engine stopped")
	case a.engine == engineUnknown || a.proj == nil:
		return sFaint.Render("? unknown")
	case a.psErr != "" && len(a.containers) == 0 && a.health.Total == 0:
		return sFaint.Render("? unknown")
	case a.health.Health == docker.HealthAllUp:
		return sOK.Render(fmt.Sprintf("● all up %d/%d", a.health.Up, a.health.Total))
	case a.health.Health == docker.HealthSomeDown:
		extra := ""
		if a.health.Starting > 0 {
			extra = fmt.Sprintf(", %d starting", a.health.Starting)
		}
		return sWarn.Render(fmt.Sprintf("◐ some down %d/%d up%s", a.health.Up, a.health.Total, extra))
	}
	return sFaint.Render("? unknown")
}

func (a *App) viewFooter() string {
	inner := a.w - 4
	var l1 string
	switch {
	case a.status != "":
		if a.statusErr {
			l1 = sErr.Render("! " + a.status)
		} else {
			l1 = sOK.Render("✓ ") + a.status
		}
	case a.run != nil:
		l1 = sWarn.Render(fmt.Sprintf("● running %s · %s", truncate(a.out.title, inner-40), fmtDur(time.Since(a.run.Started)))) + "  " + keyHint("ctrl+c", "cancel", "v", "view")
	default:
		var parts []string
		if a.lastCmd != "" {
			parts = append(parts, "last: "+a.lastCmd, a.lastExit)
		}
		parts = append(parts, "config: "+a.cfgPath)
		l1 = sFaint.Render(strings.Join(parts, " · "))
	}
	l2 := a.contextHints()
	return sBorder.Width(a.w).Padding(0, 1).Render(truncateStyled(l1, inner) + "\n" + truncateStyled(l2, inner))
}

func (a *App) contextHints() string {
	if len(a.overlays) > 0 {
		if h, ok := a.overlays[len(a.overlays)-1].(interface{ hints() string }); ok {
			return h.hints()
		}
		return keyHint("esc", "close")
	}
	if a.proj == nil {
		return keyHint("r", "retry", "x", "palette", "S", "settings", "?", "help", "q", "quit")
	}
	if a.showO && a.out != nil {
		if a.run != nil {
			return keyHint("type", "input for prompts", "enter", "send line", "ctrl+d", "send EOF", "ctrl+c", "cancel", "pgup/pgdn", "scroll", "esc", "hide")
		}
		return keyHint("esc", "dismiss", "j/k", "scroll", "x", "palette", "?", "help")
	}
	switch a.tab {
	case 0:
		return a.logs.hints()
	default:
		t := a.mage
		if a.tab == 2 {
			t = a.comp
		}
		return t.hints()
	}
}

func runtimeLabel(rt string) string {
	switch rt {
	case config.RuntimeOrbStack:
		return "OrbStack"
	case config.RuntimeDockerDesktop:
		return "Docker Desktop"
	case config.RuntimeDockerEngine:
		return "Docker Engine"
	}
	return "runtime unknown"
}

func engineStartDesc(rt string) string {
	p := docker.StartPlanFor(rt)
	if p.ShowOnly != "" {
		return "run yourself: " + p.ShowOnly
	}
	return "runs: " + config.JoinArgs(p.Argv)
}

func fmtDur(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// --- malformed config screen -------------------------------------------------

func (a *App) updateMalformed(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "s", "enter":
		b, err := config.BackupBroken(a.cfgPath)
		if err != nil {
			return a.setStatus("could not move the broken config aside: "+err.Error(), true)
		}
		a.parseErr = nil
		return tea.Batch(a.startWizard(true, nil), a.setStatus("broken config kept at "+b, false))
	case "q", "ctrl+c", "esc":
		return tea.Quit
	}
	return nil
}

func (a *App) viewMalformed() string {
	var b strings.Builder
	b.WriteString(sErr.Render("lazymage could not read its config file") + "\n\n")
	b.WriteString("File:  " + a.parseErr.Path + "\n")
	b.WriteString("Error: " + a.parseErr.Err.Error() + "\n\n")
	b.WriteString("The file has not been changed. Fix it in an editor and relaunch, or:\n\n")
	b.WriteString("  " + sBold.Render("s / enter") + "  start setup fresh — the broken file is renamed to config.yaml.broken-<time>, not deleted\n")
	b.WriteString("  " + sBold.Render("q") + "          exit without changing anything\n")
	if a.status != "" {
		b.WriteString("\n" + sErr.Render(a.status))
	}
	return sBorder.Width(a.w).Height(a.h).Padding(1, 2).Render(b.String())
}
