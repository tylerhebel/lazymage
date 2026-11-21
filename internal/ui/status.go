package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"lazymage/internal/config"
	"lazymage/internal/docker"
	"lazymage/internal/project"
	"lazymage/internal/runner"
)

type probeMsg struct{ info docker.Info }
type runtimeCheckMsg struct{ ok bool }
type servicesMsg struct{ svcs []string }
type tickMsg struct{}
type psMsg struct {
	cs         []docker.Container
	err        string
	engineDown bool
}
type factMsg struct {
	field, value, source string
}
type engineStartedMsg struct {
	err    string
	output string
}
type enginePollMsg struct{ info docker.Info }

func (a *App) probeCmd() tea.Cmd {
	return func() tea.Msg { return probeMsg{docker.Probe(5 * time.Second)} }
}

func (a *App) tickCmd() tea.Cmd {
	secs := 2
	if a.cfg != nil && a.cfg.RefreshSeconds > 0 {
		secs = a.cfg.RefreshSeconds
	}
	d := time.Duration(secs) * time.Second
	// During a lifecycle action, poll faster so per-service progress is live.
	if a.run != nil && a.out != nil && a.out.lifecycle {
		d = time.Second
	}
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{} })
}

// servicesCmd lists the services defined by the compose files. This needs no
// running engine; if compose itself is unavailable, fall back to reading YAML text.
func (a *App) servicesCmd() tea.Cmd {
	if a.proj == nil {
		return nil
	}
	p := a.proj
	argv := a.builder().Services()
	return func() tea.Msg {
		out, _, code, err := runner.Capture(p.Root, argv, 10*time.Second)
		var svcs []string
		if err == nil && code == 0 {
			for _, l := range strings.Split(out, "\n") {
				if l = strings.TrimSpace(l); l != "" {
					svcs = append(svcs, l)
				}
			}
		}
		if len(svcs) == 0 {
			var texts []string
			files := []string{p.ComposeFile}
			args := p.ComposeArgs()
			for i := 0; i+1 < len(args); i += 2 {
				files = append(files, args[i+1])
			}
			for _, f := range files {
				b, _ := os.ReadFile(filepath.Join(p.Root, f))
				texts = append(texts, string(b))
			}
			svcs = project.ComposeServicesFromText(texts...)
		}
		return servicesMsg{svcs}
	}
}

func (a *App) psCmd() tea.Cmd {
	if a.proj == nil || a.psBusy {
		return nil
	}
	a.psBusy = true
	argv, isJSON := a.builder().PS()
	root := a.proj.Root
	return func() tea.Msg {
		out, stderr, code, err := runner.Capture(root, argv, 10*time.Second)
		if err != nil || code != 0 {
			msg := strings.TrimSpace(stderr)
			if err != nil {
				msg = err.Error()
			}
			return psMsg{err: msg, engineDown: docker.IsEngineDown(stderr)}
		}
		if isJSON {
			cs, perr := docker.ParsePSJSON(out)
			if perr != nil {
				return psMsg{err: perr.Error()}
			}
			return psMsg{cs: cs}
		}
		return psMsg{cs: docker.ParsePSText(out)}
	}
}

// handleBackground processes non-key messages from async work.
func (a *App) handleBackground(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case probeMsg:
		a.probed = true
		a.info = msg.info
		prev := a.engine
		if msg.info.Reachable {
			a.engine = engineRunning
		} else if msg.info.CLIFound || msg.info.Err != "" {
			a.engine = engineStopped
		} else {
			a.engine = engineStopped
			a.info.Err = "docker CLI not found on PATH"
		}
		var cmds []tea.Cmd
		if a.wiz != nil {
			cmds = append(cmds, a.wiz.onProbe(a))
		}
		if a.checkRuntimeInstalled {
			a.checkRuntimeInstalled = false
			rt, info := a.cfg.Runtime, msg.info
			cmds = append(cmds, func() tea.Msg { return runtimeCheckMsg{ok: docker.RuntimeInstalled(rt, info)} })
		}
		if a.engine == engineRunning {
			cmds = append(cmds, a.psCmd())
			if prev != engineRunning {
				cmds = append(cmds, a.factsCmds(false)...)
			}
		}
		return tea.Batch(cmds...), true

	case runtimeCheckMsg:
		if !msg.ok && a.screen == screenMain && a.wiz == nil {
			a.overlays = nil
			cmd := a.startWizard(false, []string{"runtime"})
			return tea.Batch(cmd, a.setStatus("the saved runtime ("+runtimeLabel(a.cfg.Runtime)+") is no longer installed — pick another", true)), true
		}
		return nil, true

	case servicesMsg:
		a.services = msg.svcs
		if svc := project.PickPHPService(msg.svcs); a.facts.PHPService != svc && len(msg.svcs) > 0 {
			a.facts.PHPService = svc
		}
		for _, s := range msg.svcs {
			if strings.Contains(s, "opensearch") {
				a.facts.SearchEngine = "OpenSearch"
			} else if strings.Contains(s, "elasticsearch") && a.facts.SearchEngine == "" {
				a.facts.SearchEngine = "Elasticsearch"
			}
		}
		a.health = docker.Summarize(a.services, a.containers)
		return nil, true

	case tickMsg:
		cmds := []tea.Cmd{a.tickCmd()}
		if a.screen == screenMain && a.proj != nil && a.engine != engineStopped && a.probed {
			cmds = append(cmds, a.psCmd())
		}
		return tea.Batch(cmds...), true

	case psMsg:
		a.psBusy = false
		if msg.engineDown {
			a.engine = engineStopped
			a.containers = nil
			a.info.Reachable = false
			a.info.Err = firstLine(msg.err)
			return nil, true
		}
		a.psErr = msg.err
		if msg.err == "" {
			a.containers = msg.cs
			a.health = docker.Summarize(a.services, a.containers)
			for _, c := range msg.cs {
				if c.Service == a.facts.PHPService {
					if v := project.PHPVersionFromImage(c.Image); v != "" {
						a.facts.PHPVersion = v
					}
				}
			}
			if a.out != nil && a.out.lifecycle {
				a.out.progress = msg.cs
			}
		}
		return nil, true

	case factMsg:
		switch msg.field {
		case "version":
			a.facts.MagentoVersion, a.facts.VersionSource = msg.value, msg.source
		case "mode":
			a.facts.DeployMode, a.facts.ModeSource = msg.value, msg.source
		case "domain":
			a.facts.Domain, a.facts.DomainSource = msg.value, msg.source
		}
		a.saveFactsCache()
		return nil, true

	case engineStartedMsg:
		if msg.err != "" {
			a.engineStarting = false
			a.engineNote = sErr.Render("Start failed: "+msg.err) + "\n" + sFaint.Render(msg.output)
			return a.setStatus("engine start failed: "+msg.err, true), true
		}
		return a.pollEngine(), true

	case enginePollMsg:
		if msg.info.Reachable {
			a.engineStarting = false
			a.engineNote = ""
			a.info = msg.info
			a.engine = engineRunning
			cmds := []tea.Cmd{a.setStatus("engine is running", false), a.psCmd(), a.servicesCmd()}
			cmds = append(cmds, a.factsCmds(false)...)
			if a.wiz != nil {
				cmds = append(cmds, a.wiz.onProbe(a))
			}
			return tea.Batch(cmds...), true
		}
		if time.Now().After(a.engineDeadline) {
			a.engineStarting = false
			a.info = msg.info
			a.engineNote = sErr.Render("The engine did not respond within 30 seconds.") + "\n" + sFaint.Render(msg.info.Err)
			cmds := []tea.Cmd{a.setStatus("engine did not start within 30s", true)}
			if a.wiz != nil {
				cmds = append(cmds, a.wiz.onProbe(a))
			}
			return tea.Batch(cmds...), true
		}
		return a.pollEngine(), true

	case runTickMsg:
		if a.run != nil && a.run.ID == msg.id {
			return runTick(msg.id), true
		}
		return nil, true

	case shellDoneMsg:
		what := msg.what
		if what == "" {
			what = "the container shell"
		}
		if msg.err != nil {
			return a.setStatus(what+" exited: "+msg.err.Error(), true), true
		}
		return a.setStatus("back from "+what, false), true

	case updateCheckedMsg:
		return a.onUpdateChecked(msg), true
	case updateInstalledMsg:
		return a.onUpdateInstalled(msg), true

	case dbQueryMsg:
		return msg.next(msg.out, msg.err), true

	case editorDoneMsg:
		if msg.err != nil {
			return a.setStatus("editor: "+msg.err.Error(), true), true
		}
		if msg.reload {
			return a.reloadConfig(), true
		}
		return nil, true

	case logsReconnectMsg:
		return a.logs.reconnect(a, msg), true

	case openDoneMsg:
		if msg.err != "" {
			return a.setStatus(msg.err, true), true
		}
		return a.setStatus(msg.ok, false), true
	}
	return nil, false
}

// startEngine runs the runtime's start command, then polls docker info for 30s.
func (a *App) startEngine() tea.Cmd { return a.startEngineFor(a.cfg.Runtime) }

func (a *App) startEngineFor(rt string) tea.Cmd {
	if a.engineStarting {
		return nil
	}
	p := docker.StartPlanFor(rt)
	if p.ShowOnly != "" {
		a.engineNote = "lazymage does not start this engine for you:\n\n    " + sBold.Render(p.ShowOnly) + "\n\nthen press r."
		return a.setStatus("run: "+p.ShowOnly, false)
	}
	a.engineStarting = true
	a.engineDeadline = time.Now().Add(30 * time.Second)
	a.engineNote = sFaint.Render("$ " + config.JoinArgs(p.Argv))
	a.log.printf("engine start: %s", config.JoinArgs(p.Argv))
	argv := p.Argv
	return func() tea.Msg {
		out, stderr, code, err := runner.Capture("", argv, 30*time.Second)
		if err != nil {
			return engineStartedMsg{err: err.Error(), output: stderr}
		}
		if code != 0 {
			return engineStartedMsg{err: fmt.Sprintf("exit %d", code), output: strings.TrimSpace(stderr + out)}
		}
		return engineStartedMsg{}
	}
}

func (a *App) pollEngine() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return enginePollMsg{docker.Probe(3 * time.Second)}
	})
}

// factsCmds refreshes slow facts through bin/magento. They are only run once
// the engine is confirmed running (section 7). force=false skips values that
// are already cached from bin/magento.
func (a *App) factsCmds(force bool) []tea.Cmd {
	if a.proj == nil || a.engine != engineRunning {
		return nil
	}
	b := a.builder()
	root := a.proj.Root
	var cmds []tea.Cmd
	capture := func(field string, argv []string, parse func(string) string, source string) tea.Cmd {
		return func() tea.Msg {
			out, _, code, err := runner.Capture(root, argv, 60*time.Second)
			if err != nil || code != 0 {
				return nil
			}
			v := parse(out)
			if v == "" {
				return nil
			}
			return factMsg{field: field, value: v, source: source}
		}
	}
	if force || a.facts.VersionSource != "bin/magento" {
		cmds = append(cmds, capture("version", b.Magento("--version"), project.ParseCLIVersion, "bin/magento"))
	}
	if force || a.facts.ModeSource != "bin/magento" {
		cmds = append(cmds, capture("mode", b.Magento("deploy:mode:show"), project.ParseModeShow, "bin/magento"))
	}
	if a.facts.Domain == "" || (force && a.facts.DomainSource == "magento config") {
		cmds = append(cmds, capture("domain", b.Magento("config:show", "web/secure/base_url"), func(s string) string {
			return project.HostOf(project.ParseBaseURL(s))
		}, "magento config"))
	}
	return cmds
}

// refresh re-detects runtime, health and project facts (key r).
func (a *App) refresh() tea.Cmd {
	if a.proj != nil {
		static := a.proj.StaticFacts()
		// Keep engine-derived facts, refresh file-derived ones.
		if a.facts.DomainSource != "magento config" || static.Domain != "" {
			a.facts.Domain, a.facts.DomainSource = static.Domain, static.DomainSource
		}
		a.facts.AdminPath = static.AdminPath
		if a.facts.VersionSource != "bin/magento" {
			a.facts.MagentoVersion, a.facts.VersionSource = static.MagentoVersion, static.VersionSource
		}
		if a.facts.ModeSource != "bin/magento" {
			a.facts.DeployMode, a.facts.ModeSource = static.DeployMode, static.ModeSource
		}
	}
	cmds := []tea.Cmd{a.probeCmd(), a.servicesCmd(), a.setStatus("refreshing status and project facts…", false)}
	return tea.Batch(append(cmds, a.factsCmds(true)...)...)
}

// --- facts cache -----------------------------------------------------------------

func factsCachePath() string { return filepath.Join(config.Dir(), "cache", "facts.json") }

func (a *App) loadFactsCache() {
	if a.proj == nil {
		return
	}
	b, err := os.ReadFile(factsCachePath())
	if err != nil {
		return
	}
	var all map[string]project.Facts
	if json.Unmarshal(b, &all) != nil {
		return
	}
	c, ok := all[a.proj.Root]
	if !ok {
		return
	}
	if c.VersionSource == "bin/magento" && c.MagentoVersion != "" {
		a.facts.MagentoVersion, a.facts.VersionSource = c.MagentoVersion, c.VersionSource
	}
	if c.ModeSource == "bin/magento" && c.DeployMode != "" {
		a.facts.DeployMode, a.facts.ModeSource = c.DeployMode, c.ModeSource
	}
	if a.facts.Domain == "" && c.Domain != "" {
		a.facts.Domain, a.facts.DomainSource = c.Domain, c.DomainSource
	}
}

func (a *App) saveFactsCache() {
	if a.proj == nil {
		return
	}
	p := factsCachePath()
	all := map[string]project.Facts{}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &all)
	}
	all[a.proj.Root] = a.facts
	b, _ := json.MarshalIndent(all, "", "  ")
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, b, 0o644)
}

// --- debug log ---------------------------------------------------------------------

type debugLog struct {
	mu sync.Mutex
	f  *os.File
}

func openDebugLog(on bool) *debugLog {
	if !on {
		return &debugLog{}
	}
	_ = os.MkdirAll(config.Dir(), 0o755)
	f, err := os.OpenFile(filepath.Join(config.Dir(), "debug.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return &debugLog{}
	}
	return &debugLog{f: f}
}

// printf writes a line. Callers pass already-redacted command strings.
func (d *debugLog) printf(format string, args ...any) {
	if d == nil || d.f == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	fmt.Fprintf(d.f, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
