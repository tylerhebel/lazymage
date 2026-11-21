package ui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"lazymage/internal/config"
	"lazymage/internal/plan"
	"lazymage/internal/runner"
)

type shellDoneMsg struct {
	err  error
	what string // "" means the container shell
}
type editorDoneMsg struct {
	err    error
	reload bool
}
type openDoneMsg struct{ ok, err string }

// do runs a built-in action by ID, whether it came from a key or the palette.
func (a *App) do(action string) tea.Cmd {
	if reason := a.disabledReason(action); reason != "" {
		return a.setStatus(reason, true)
	}
	a.touchRecent(action)
	switch action {
	case aPalette:
		a.push(newPalette(a))
	case aHelp:
		a.push(newHelp(a))
	case aSettings:
		a.push(newSettings(a))
	case aSetup:
		a.overlays = nil
		return a.startWizard(false, nil)
	case aQuit:
		return a.quit()
	case aUpdate:
		return a.startUpdate()
	case aNextTab:
		a.tab, a.showO = (a.tab+1)%3, false
	case aPrevTab:
		a.tab, a.showO = (a.tab+2)%3, false
	case aTab1, aTab2, aTab3:
		a.tab, a.showO = int(action[len(action)-1]-'1'), false
	case aRefresh:
		return a.refresh()
	case aCancelRun:
		if a.run != nil {
			a.run.Cancel()
			a.out.buf.Note("cancelling… (sent interrupt to the process group)")
			return a.setStatus("cancelling the running command", false)
		}
		return a.setStatus("nothing is running — press q to quit", false)
	case aViewOutput:
		if a.out != nil {
			a.showO = true
		}
	case aEngineStart:
		return a.startEngine()
	case aStackStart:
		return a.startRun(a.builder().Lifecycle("start"), runOpts{kind: "lifecycle", lifecycle: true})
	case aStackRestart:
		return a.startRun(a.builder().Lifecycle("restart"), runOpts{kind: "lifecycle", lifecycle: true})
	case aStackStop:
		argv := a.builder().Lifecycle("stop")
		a.push(newConfirm("Stop the whole stack?", "This stops every container in "+a.proj.Name+".\n\n$ "+config.JoinArgs(argv), "Stop stack",
			func() tea.Cmd { return a.startRun(argv, runOpts{kind: "lifecycle", lifecycle: true}) }))

	case aLogsAll:
		a.tab, a.showO = 0, false
		return a.logs.open(a, "")
	case aLogsPick:
		a.tab, a.showO = 0, false
		a.push(newServicePicker(a))
	case aLogsPause, aLogsFollow, aLogsSearch, aLogsNext, aLogsPrev, aLogsFilter, aLogsClear, aLogsSave:
		a.tab, a.showO = 0, false
		return a.logs.action(a, action)

	case aMageFree:
		a.tab, a.showO = 1, false
		return a.mage.focusInput()
	case aComposerFree:
		a.tab, a.showO = 2, false
		return a.comp.focusInput()

	case aShell:
		return a.shell()
	case aStorefront:
		return openURL(a.cfg.Browser, a.storefrontURL(), "storefront")
	case aAdmin:
		return openURL(a.cfg.Browser, a.adminURL(), "admin")
	case aCopyURL:
		return a.copyText(a.storefrontURL())
	case aFEThemes, aFEEditThemes, aFESetupGrunt, aFENpmInstall, aFEGruntBuild, aFEGruntLess, aFEGruntWatch, aFEGruntFree:
		return a.doFrontend(action)

	case aDBDump:
		return a.dbDumpPrompt()
	case aDBImport:
		a.push(newDumpPicker(a))
	case aDBShell:
		return a.dbShell()
	case aDBReplaceURL:
		return a.dbReplaceURLs()

	case aEditEnv:
		return a.edit(filepath.Join(a.proj.Root, ".env"), false)
	case aEditConfig:
		if _, err := os.Stat(a.cfgPath); err != nil {
			a.saveConfig()
		}
		return a.edit(a.cfgPath, true)
	case aNewCustom:
		a.push(newCustomForm(a, nil, false))
	case aManage:
		a.push(newManage(a))

	default:
		if strings.HasPrefix(action, "magento:") {
			if mc, ok := findMage(strings.TrimPrefix(action, "magento:")); ok {
				a.tab, a.showO = 1, false
				return a.runMageCommand(mc)
			}
		}
		for _, cc := range composerCatalog {
			if cc.ID == action {
				a.tab, a.showO = 2, false
				return a.runComposerPreset(cc)
			}
		}
	}
	return nil
}

// disabledReason says why an action cannot run right now ("" = it can).
func (a *App) disabledReason(action string) string {
	switch action {
	case aPalette, aHelp, aSettings, aSetup, aQuit, aUpdate, aNewCustom, aManage, aEditConfig,
		aNextTab, aPrevTab, aTab1, aTab2, aTab3, aCancelRun, aViewOutput:
		return ""
	}
	if a.proj == nil {
		return "not in a docker-magento project"
	}
	switch action {
	case aRefresh:
		return ""
	case aEngineStart:
		if a.engine == engineRunning {
			return "the engine is already running"
		}
		if a.engineStarting {
			return "the engine is starting"
		}
		return ""
	case aFEEditThemes:
		return ""
	case aFESetupGrunt:
		if !a.proj.HasBin("setup-grunt") {
			return "this project has no bin/setup-grunt"
		}
	case aEditEnv:
		if _, err := os.Stat(filepath.Join(a.proj.Root, ".env")); err != nil {
			return "this project has no .env file"
		}
		return ""
	case aStorefront, aCopyURL:
		if a.facts.Domain == "" {
			return "domain unknown — set it with bin/setup-domain or a DOMAIN key in .env, then press r"
		}
		return ""
	case aAdmin:
		if a.facts.Domain == "" {
			return "domain unknown"
		}
		if a.facts.AdminPath == "" {
			return "admin path unknown (no backend frontName in src/app/etc/env.php)"
		}
		return ""
	case aLogsPause, aLogsFollow, aLogsSearch, aLogsNext, aLogsPrev, aLogsFilter, aLogsClear, aLogsSave:
		if !a.logs.opened {
			return "open logs first (l)"
		}
		return ""
	}
	if a.engine == engineStopped {
		return "engine stopped — start it first"
	}
	if a.engine == engineUnknown && action != aLogsAll && action != aLogsPick {
		// Still probing: allow, the command will report its own error.
	}
	switch action {
	case aDBDump, aDBImport, aDBReplaceURL, aFEThemes:
		return a.dbDisabled()
	case aFESetupGrunt, aFENpmInstall, aFEGruntBuild, aFEGruntLess, aFEGruntWatch, aFEGruntFree:
		return a.feDisabled()
	case aDBShell:
		if len(a.services) > 0 && plan.DBServiceFrom(a.services) == "" {
			return "no database service (db, mysql or mariadb) in the compose files"
		}
		return ""
	case aStackStart, aStackStop, aStackRestart, aMageFree, aComposerFree:
		if a.run != nil {
			return "a command is already running (ctrl+c cancels it)"
		}
	}
	if strings.HasPrefix(action, "magento:") || strings.HasPrefix(action, "composer.") {
		if a.run != nil {
			return "a command is already running (ctrl+c cancels it)"
		}
	}
	return ""
}

func (a *App) touchRecent(id string) {
	switch id {
	case aPalette, aHelp, aNextTab, aPrevTab, aTab1, aTab2, aTab3, aQuit, aCancelRun:
		return
	}
	out := []string{id}
	for _, r := range a.recent {
		if r != id && len(out) < 20 {
			out = append(out, r)
		}
	}
	a.recent = out
}

func (a *App) quit() tea.Cmd {
	if a.run != nil {
		a.push(newConfirm("Quit lazymage?", "A command is still running:\n\n$ "+a.out.title+"\n\nQuitting cancels it.", "Cancel it and quit", func() tea.Cmd {
			a.run.Terminate()
			a.logs.terminate()
			return tea.Quit
		}))
		return nil
	}
	a.logs.terminate()
	return tea.Quit
}

// --- running commands ------------------------------------------------------------

type runOpts struct {
	kind      string // history kind: magento, composer, lifecycle, bin, compose, host-shell
	refresh   bool   // refresh cached facts afterwards
	lifecycle bool   // show per-service progress
	hostShell bool

	display string                      // shown instead of the argv (e.g. "… < dump.sql")
	rio     runner.IO                   // redirected stdin / stdout
	noStdin bool                        // hide the stdin field (input comes from a file)
	meter   func() string               // live progress line (bytes written, % read)
	onExit  func(runner.Exited) tea.Cmd // runs after the exit is recorded
}

// startRun shows the exact command, then streams it into the output view.
func (a *App) startRun(argv []string, o runOpts) tea.Cmd {
	if a.run != nil {
		return a.setStatus("a command is already running (ctrl+c cancels it)", true)
	}
	if a.proj == nil {
		return a.setStatus("not in a docker-magento project", true)
	}
	a.runSeq++
	id := a.runSeq
	a.status = ""
	display := config.JoinArgs(argv)
	if o.display != "" {
		display = o.display
	}
	a.out = newOutputView(id, display, o)
	a.showO = true
	a.out.buf.Note("$ " + display)
	if o.hostShell {
		a.out.buf.Note("host shell command — runs on your machine, not in a container")
	}
	a.log.printf("run: %s", config.JoinArgs(config.RedactArgs(argv)))
	r, err := runner.StartIO(id, a.proj.Root, argv, nil, a.sendAny, o.rio)
	if err != nil {
		a.out.buf.Note("could not start: " + err.Error())
		code := -1
		a.out.exit = &runner.Exited{RunID: id, Code: code, Err: err}
		a.recordHistory(argv, o.kind, time.Now(), 0, code)
		status := a.setStatus("could not start: "+err.Error(), true)
		if o.onExit != nil {
			return tea.Batch(status, o.onExit(*a.out.exit))
		}
		return status
	}
	a.run = r
	cmds := []tea.Cmd{a.out.focus(), runTick(id)}
	if o.lifecycle {
		cmds = append(cmds, a.psCmd())
	}
	return tea.Batch(cmds...)
}

// runTickMsg redraws once a second while a command runs, so elapsed time
// keeps moving even when the command is silent.
type runTickMsg struct{ id int }

func runTick(id int) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return runTickMsg{id} })
}

func (a *App) sendAny(m any) {
	if a.send != nil {
		a.send(m)
	}
}

func (a *App) routeChunk(c runner.Chunk) {
	if a.logs.runID == c.RunID {
		a.logs.onChunk(a, c)
		return
	}
	if a.out != nil && a.out.runID == c.RunID {
		a.out.buf.Append(c.Data, c.Stderr)
	}
}

func (a *App) routeExit(e runner.Exited) tea.Cmd {
	if a.logs.runID == e.RunID {
		return a.logs.onExit(a, e)
	}
	if a.out == nil || a.out.runID != e.RunID {
		return nil
	}
	a.out.exit = &e
	var argv []string
	if a.run != nil {
		argv = a.run.Argv
	}
	a.run = nil
	switch {
	case e.Cancelled:
		a.out.buf.Note(fmt.Sprintf("cancelled after %s — exit code %d%s", fmtDur(e.Duration), e.Code, signalNote(e.Code)))
		if strings.Contains(a.out.title, " exec ") || strings.HasPrefix(a.out.title, "bin/") {
			a.out.buf.Note("note: a process inside the container may keep running until it finishes on its own")
		}
	case e.Err != nil:
		a.out.buf.Note(fmt.Sprintf("failed: %v (exit code %d)", e.Err, e.Code))
	case e.Code == 0:
		a.out.buf.Note(fmt.Sprintf("exit code 0 · %s", fmtDur(e.Duration)))
	default:
		a.out.buf.Note(fmt.Sprintf("exit code %d · %s — the command failed; output stays here until you dismiss it (esc)", e.Code, fmtDur(e.Duration)))
	}
	a.recordHistory(argv, a.out.opts.kind, time.Now().Add(-e.Duration), e.Duration, e.Code)
	a.lastCmd = truncate(config.JoinArgs(config.RedactArgs(argv)), 60)
	a.lastExit = fmt.Sprintf("exit %d", e.Code)
	if e.Cancelled {
		a.lastExit = fmt.Sprintf("cancelled (exit %d)", e.Code)
	}
	var cmds []tea.Cmd
	if a.out.opts.refresh {
		cmds = append(cmds, a.factsCmds(true)...)
	}
	if a.out.opts.lifecycle {
		cmds = append(cmds, a.psCmd())
	}
	if e.Code == 0 && !e.Cancelled {
		cmds = append(cmds, a.setStatus("done: "+a.lastCmd, false))
	} else {
		cmds = append(cmds, a.setStatus(a.lastCmd+" — "+a.lastExit, true))
	}
	if a.out.opts.onExit != nil {
		cmds = append(cmds, a.out.opts.onExit(e))
	}
	return tea.Batch(cmds...)
}

func signalNote(code int) string {
	if code == -1 {
		return " (terminated by a signal)"
	}
	return ""
}

// recordHistory stores a redacted entry in per-project history.
func (a *App) recordHistory(argv []string, kind string, start time.Time, d time.Duration, code int) {
	if a.proj == nil {
		return
	}
	a.cfg.AddHistory(a.proj.Root, config.HistoryEntry{
		Kind: kind, Command: config.JoinArgs(config.RedactArgs(argv)), Started: start.UTC().Truncate(time.Second),
		DurationMs: d.Milliseconds(), ExitCode: code,
	})
	if a.screen == screenMain && a.cfgPath != "" {
		if _, err := os.Stat(a.cfgPath); err == nil {
			a.saveConfig()
		}
	}
	// Free-form input history (arrow keys) is per kind.
	a.mage.refreshHistory(a)
	a.comp.refreshHistory(a)
}

// runMageArgs runs bin/magento with args, confirming destructive ones.
func (a *App) runMageArgs(args []string, confirm bool) tea.Cmd {
	if len(args) == 0 {
		return nil
	}
	if r := a.disabledReason(aMageFree); r != "" {
		return a.setStatus(r, true)
	}
	argv := a.builder().Magento(args...)
	refresh := false
	switch args[0] {
	case "setup:upgrade", "deploy:mode:set", "setup:install":
		refresh = true
	case "config:set":
		refresh = strings.Contains(strings.Join(args, " "), "base_url")
	}
	opts := runOpts{kind: config.KindMagento, refresh: refresh}
	run := func() tea.Cmd { return a.startRun(argv, opts) }
	if confirm || config.LooksDestructive(config.KindMagento, strings.Join(args, " ")) {
		a.push(newConfirm("Run this Magento command?", "$ "+config.JoinArgs(argv), "Run", run))
		return nil
	}
	return run()
}

func (a *App) runMageCommand(mc mageCommand) tea.Cmd {
	if r := a.disabledReason("magento:" + mc.Cmd); r != "" {
		return a.setStatus(r, true)
	}
	if mc.Prompt != nil {
		a.push(newPrompt(mc.Cmd, *mc.Prompt, func(v string) tea.Cmd {
			return a.runMageArgs(append(append([]string{mc.Cmd}, mc.Args...), v), mc.Confirm)
		}))
		return nil
	}
	return a.runMageArgs(append([]string{mc.Cmd}, mc.Args...), mc.Confirm)
}

func (a *App) runComposerArgs(args []string) tea.Cmd {
	if len(args) == 0 {
		return nil
	}
	if r := a.disabledReason(aComposerFree); r != "" {
		return a.setStatus(r, true)
	}
	return a.startRun(a.builder().Composer(args...), runOpts{kind: config.KindComposer})
}

func (a *App) runComposerPreset(cc composerCommand) tea.Cmd {
	if r := a.disabledReason(cc.ID); r != "" {
		return a.setStatus(r, true)
	}
	if cc.Prompt != nil {
		a.push(newPrompt(cc.Title, *cc.Prompt, func(v string) tea.Cmd {
			words, err := config.SplitArgs(v)
			if err != nil || len(words) == 0 {
				words = []string{v}
			}
			return a.runComposerArgs(append(append([]string(nil), cc.Args...), words...))
		}))
		return nil
	}
	return a.runComposerArgs(cc.Args)
}

// --- custom commands ----------------------------------------------------------------

func (a *App) root() string {
	if a.proj == nil {
		return ""
	}
	return a.proj.Root
}

func (a *App) customByShortcut(key string) (config.Scoped, bool) {
	for _, sc := range a.cfg.EffectiveCommands(a.root()) {
		if !sc.Overridden && sc.Cmd.Shortcut == key {
			return sc, true
		}
	}
	return config.Scoped{}, false
}

func (a *App) customDisabled(cc config.CustomCommand) string {
	if a.proj == nil {
		return "not in a docker-magento project"
	}
	if a.run != nil {
		return "a command is already running (ctrl+c cancels it)"
	}
	if cc.Kind != config.KindHostShell && cc.Kind != config.KindBin && a.engine == engineStopped {
		return "engine stopped — start it first"
	}
	return ""
}

// runCustom asks for each prompted argument, shows the fully built command,
// and runs it under the same rules as built-ins.
func (a *App) runCustom(sc config.Scoped) tea.Cmd {
	cc := sc.Cmd
	if r := a.customDisabled(cc); r != "" {
		return a.setStatus(cc.Name+": "+r, true)
	}
	a.touchRecent(customID(sc))
	values := map[string]string{}
	var ask func(i int) tea.Cmd
	ask = func(i int) tea.Cmd {
		if i < len(cc.Prompts) {
			p := cc.Prompts[i]
			label := p.Label
			if label == "" {
				label = p.Key
			}
			a.push(newPrompt(fmt.Sprintf("%s (%d/%d)", cc.Name, i+1, len(cc.Prompts)), argPrompt{Label: label, Default: p.Default, Required: p.Required}, func(v string) tea.Cmd {
				values[p.Key] = v
				return ask(i + 1)
			}))
			return nil
		}
		argv, err := a.builder().Custom(cc, values)
		if err != nil {
			return a.setStatus(cc.Name+": "+err.Error(), true)
		}
		opts := runOpts{kind: cc.Kind, hostShell: cc.Kind == config.KindHostShell}
		run := func() tea.Cmd { return a.startRun(argv, opts) }
		if len(cc.Prompts) > 0 || cc.Confirm {
			title := "Run " + cc.Name + "?"
			body := "$ " + config.JoinArgs(argv)
			if cc.Kind == config.KindHostShell {
				body = sWarn.Render("[host shell] runs on your machine") + "\n\n" + body
			}
			a.push(newConfirm(title, body, "Run", run))
			return nil
		}
		return run()
	}
	return ask(0)
}

func customID(sc config.Scoped) string {
	return "custom:" + scopeLabel(sc.Project) + ":" + strings.ToLower(sc.Cmd.Name)
}

// --- shell, browser, clipboard, editor ---------------------------------------------

func (a *App) shell() tea.Cmd {
	argv := a.builder().Shell(a.cfg.Shell)
	cmd := exec.Command(argv[0], argv[1:]...)
	if strings.HasPrefix(argv[0], "bin/") {
		cmd.Path = filepath.Join(a.proj.Root, argv[0])
		cmd.Err = nil
	}
	cmd.Dir = a.proj.Root
	a.log.printf("shell: %s", config.JoinArgs(argv))
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if _, ok := err.(*exec.ExitError); ok {
			err = nil // a non-zero exit from the last command in the shell is not an error
		}
		return shellDoneMsg{err: err}
	})
}

func (a *App) storefrontURL() string {
	if a.facts.Domain == "" {
		return ""
	}
	return "https://" + a.facts.Domain + "/"
}

func (a *App) adminURL() string {
	if a.facts.Domain == "" || a.facts.AdminPath == "" {
		return ""
	}
	return "https://" + a.facts.Domain + "/" + a.facts.AdminPath
}

// browserCommand returns argv to open url with the configured browser.
func browserCommand(browser, url string) ([]string, error) {
	named := browser != "" && browser != "default"
	switch runtime.GOOS {
	case "darwin":
		if named {
			return []string{"open", "-a", browser, url}, nil
		}
		return []string{"open", url}, nil
	default:
		if named {
			return []string{browser, url}, nil
		}
		for _, c := range []string{"xdg-open", "wslview", "sensible-browser"} {
			if _, err := exec.LookPath(c); err == nil {
				return []string{c, url}, nil
			}
		}
		return nil, fmt.Errorf("no browser opener found (install xdg-utils, or set a browser in Settings)")
	}
}

func openURL(browser, url, what string) tea.Cmd {
	return func() tea.Msg {
		argv, err := browserCommand(browser, url)
		if err != nil {
			return openDoneMsg{err: err.Error()}
		}
		c := exec.Command(argv[0], argv[1:]...)
		if err := c.Start(); err != nil {
			return openDoneMsg{err: "could not open " + what + ": " + err.Error()}
		}
		go func() { _ = c.Wait() }()
		return openDoneMsg{ok: "opened " + what + ": " + url}
	}
}

func (a *App) copyText(s string) tea.Cmd {
	var tools [][]string
	if runtime.GOOS == "darwin" {
		tools = [][]string{{"pbcopy"}}
	} else {
		tools = [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}, {"clip.exe"}}
	}
	for _, t := range tools {
		if _, err := exec.LookPath(t[0]); err != nil {
			continue
		}
		c := exec.Command(t[0], t[1:]...)
		c.Stdin = strings.NewReader(s)
		if err := c.Run(); err == nil {
			return a.setStatus("copied "+s, false)
		}
	}
	// Fall back to the terminal clipboard escape (OSC 52).
	return tea.Batch(tea.SetClipboard(s), a.setStatus("copied "+s+" (via terminal clipboard)", false))
}

func (a *App) editorArgv() []string {
	ed := a.cfg.Editor
	if ed == "" {
		ed = os.Getenv("VISUAL")
	}
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	if ed == "" {
		ed = "vi"
	}
	f, err := config.SplitArgs(ed)
	if err != nil || len(f) == 0 {
		return []string{"vi"}
	}
	return f
}

func (a *App) edit(path string, reload bool) tea.Cmd {
	argv := append(a.editorArgv(), path)
	cmd := exec.Command(argv[0], argv[1:]...)
	if a.proj != nil {
		cmd.Dir = a.proj.Root
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return editorDoneMsg{err: err, reload: reload} })
}

// reloadConfig re-reads the config after it was edited by hand. A config that
// no longer parses is reported and never overwritten: saving is blocked until it is fixed.
func (a *App) reloadConfig() tea.Cmd {
	c, err := config.Load(a.cfgPath)
	if err != nil {
		a.cfgLocked = true
		return a.setStatus("config no longer parses — not saving until fixed: "+err.Error(), true)
	}
	a.cfgLocked = false
	a.cfg = c
	a.mage.refreshItems(a)
	a.comp.refreshItems(a)
	if len(c.Warnings) > 0 {
		return a.setStatus(fmt.Sprintf("config reloaded; %d custom command(s) skipped: %s", len(c.Warnings), c.Warnings[0]), true)
	}
	return a.setStatus("config reloaded", false)
}
