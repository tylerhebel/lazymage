package ui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"lazymage/internal/config"
)

// Cron, maintenance mode and Xdebug.
//
// Status comes from files docker-magento keeps on the host (src/var is
// bind-mounted): var/.maintenance.flag (Magento) and var/.cron-enabled
// (written by bin/cron start). bin/cron, bin/debug-cli and the legacy
// bin/xdebug need a terminal (they use bin/cli / bin/root), so they run with
// the terminal handed over, then come back to lazymage.

const catOps = "Cron, maintenance & Xdebug"

func (a *App) varFlag(name string) bool {
	if a.proj == nil {
		return false
	}
	_, err := os.Stat(filepath.Join(a.proj.Root, "src", "var", name))
	return err == nil
}

func (a *App) maintenanceOn() bool { return a.varFlag(".maintenance.flag") }
func (a *App) cronEnabled() bool   { return a.varFlag(".cron-enabled") }

// xdebugStyle: "cookie" (phpfpm-xdebug service, current docker-magento),
// "legacy" (a bin/xdebug that toggles the extension), or "".
func (a *App) xdebugStyle() string {
	if a.proj == nil {
		return ""
	}
	for _, s := range a.services {
		if s == "phpfpm-xdebug" {
			return "cookie"
		}
	}
	if b, err := os.ReadFile(filepath.Join(a.proj.Root, "bin", "xdebug")); err == nil && !strings.Contains(string(b), "has been removed") {
		return "legacy"
	}
	return ""
}

// xdebugRouting reports whether nginx sends XDEBUG_SESSION requests to
// phpfpm-xdebug (bin/setup-nginx rewrites fastcgi_pass to the cookie map).
func (a *App) xdebugRouting() bool {
	b, err := os.ReadFile(filepath.Join(a.proj.Root, "src", "nginx.conf.sample"))
	return err == nil && strings.Contains(string(b), "fastcgi_pass $fastcgi_backend")
}

func (a *App) xdebugContainerUp() bool {
	for _, c := range a.containers {
		if c.Service == "phpfpm-xdebug" {
			return c.State == "running"
		}
	}
	return false
}

// opsChecklist adds cron / maintenance / Xdebug rows to the Stack tab.
func (a *App) opsChecklist(w int) []string {
	row := func(mark, title, detail string) string {
		return truncateStyled(fmt.Sprintf(" %s    %-14s %s", mark, title, detail), w)
	}
	var out []string
	if a.maintenanceOn() {
		out = append(out, row(sWarn.Render("!"), "Maintenance", sWarn.Render("ON — the storefront shows the maintenance page")))
	} else {
		out = append(out, row(sOK.Render("✓"), "Maintenance", "off"))
	}
	if a.cronEnabled() {
		out = append(out, row(sOK.Render("✓"), "Cron", "enabled (bin/cron start)"))
	} else {
		out = append(out, row(sFaint.Render("·"), "Cron", sFaint.Render("not enabled — palette: Cron: start")))
	}
	switch a.xdebugStyle() {
	case "cookie":
		detail := "send the XDEBUG_SESSION cookie to debug a request"
		switch {
		case !a.xdebugRouting():
			out = append(out, row(sWarn.Render("◐"), "Xdebug", "nginx not routing to phpfpm-xdebug yet — palette: Xdebug: set up routing"))
		case !a.xdebugContainerUp():
			out = append(out, row(sWarn.Render("◐"), "Xdebug", "phpfpm-xdebug is not running"))
		default:
			out = append(out, row(sOK.Render("✓"), "Xdebug", "ready — "+detail))
		}
	case "legacy":
		out = append(out, row(sFaint.Render("·"), "Xdebug", "bin/xdebug toggles it — palette: Xdebug"))
	}
	return out
}

// --- terminal hand-over -------------------------------------------------------------------

type termDoneMsg struct {
	argv []string
	kind string
	code int
	err  error
	then func() tea.Cmd
}

// runInTerminal runs argv with the terminal handed over (for scripts that need
// a TTY), pauses so the output can be read, and records it in history.
func (a *App) runInTerminal(argv []string, kind string, then func() tea.Cmd) tea.Cmd {
	script := `"$@"; s=$?; printf '\n[exit %s — press enter to return to lazymage] ' "$s"; read _; exit "$s"`
	cmd := exec.Command("sh", append([]string{"-c", script, "lazymage"}, argv...)...)
	cmd.Dir = a.proj.Root
	a.log.printf("terminal: %s", config.JoinArgs(config.RedactArgs(argv)))
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code, err = ee.ExitCode(), nil
		}
		return termDoneMsg{argv: argv, kind: kind, code: code, err: err, then: then}
	})
}

func (a *App) onTermDone(m termDoneMsg) tea.Cmd {
	a.recordHistory(m.argv, m.kind, time.Now(), 0, m.code)
	label := config.JoinArgs(m.argv)
	var cmds []tea.Cmd
	switch {
	case m.err != nil:
		cmds = append(cmds, a.setStatus(label+": "+m.err.Error(), true))
	case m.code != 0:
		cmds = append(cmds, a.setStatus(fmt.Sprintf("%s — exit %d", label, m.code), true))
	default:
		cmds = append(cmds, a.setStatus("done: "+label, false))
	}
	if m.then != nil {
		cmds = append(cmds, m.then())
	}
	return tea.Batch(cmds...)
}

// --- actions --------------------------------------------------------------------------------

func (a *App) doOps(action string) tea.Cmd {
	b := a.builder()
	binMode := a.eff().IntegrationMode != config.ModeDockerOnly
	switch action {
	case aCronStart, aCronStop:
		op := strings.TrimPrefix(action, "cron.")
		if binMode && a.proj.HasBin("cron") {
			return a.runInTerminal([]string{"bin/cron", op}, config.KindBin, nil)
		}
		script := "touch /var/www/html/var/.cron-enabled && service cron start"
		if op == "stop" {
			script = "rm -f /var/www/html/var/.cron-enabled; service cron stop"
		}
		return a.startRun(b.DirectCompose("exec", "-T", "-u", "root", b.PHPService, "sh", "-c", script), runOpts{kind: config.KindCompose})
	case aCronStatus:
		if binMode && a.proj.HasBin("rootnotty") {
			return a.startRun([]string{"bin/rootnotty", "service", "cron", "status"}, runOpts{kind: config.KindBin, noStdin: true})
		}
		return a.startRun(b.DirectCompose("exec", "-T", "-u", "root", b.PHPService, "service", "cron", "status"), runOpts{kind: config.KindCompose, noStdin: true})

	case aXdebugSetup:
		argv := b.Bin("setup-nginx")
		a.push(newConfirm("Set up Xdebug routing?", "bin/setup-nginx rewrites src/nginx.conf.sample so requests carrying the\nXDEBUG_SESSION cookie go to the phpfpm-xdebug container, then reloads nginx.\n\n$ "+config.JoinArgs(argv),
			"Run", func() tea.Cmd { return a.startRun(argv, runOpts{kind: config.KindBin}) }))
	case aXdebugCLI:
		a.push(newPrompt("Run a CLI command with Xdebug", argPrompt{Label: "Command (runs in phpfpm-xdebug, e.g. bin/magento indexer:reindex)", Required: true}, func(v string) tea.Cmd {
			words, err := config.SplitArgs(v)
			if err != nil || len(words) == 0 {
				return a.setStatus("could not read the command", true)
			}
			if binMode && a.proj.HasBin("debug-cli") {
				return a.runInTerminal(append([]string{"bin/debug-cli"}, words...), config.KindBin, nil)
			}
			return a.startRun(b.DirectCompose(append([]string{"exec", "-T", "phpfpm-xdebug"}, words...)...), runOpts{kind: config.KindCompose})
		}))
	case aXdebugHelp:
		a.push(&messageOverlay{title: "Debugging with Xdebug", body: strings.Join([]string{
			"docker-magento runs Xdebug in its own container, phpfpm-xdebug. nginx sends a",
			"request there only when it carries the XDEBUG_SESSION cookie, so normal page",
			"loads stay fast. There is nothing to switch on or off.",
			"",
			"1. In your IDE, listen for debug connections on port 9003",
			"   (PhpStorm: server name and path mapping per the docker-magento README).",
			"2. In the browser, set the XDEBUG_SESSION cookie: use the Xdebug Helper",
			"   extension, or add ?XDEBUG_SESSION_START=PHPSTORM to the URL once.",
			"3. Reload the page; breakpoints hit in your IDE.",
			"",
			"CLI: palette → Xdebug: run a CLI command (bin/debug-cli).",
			"",
			sFaint.Render("enter / esc to close"),
		}, "\n")})
	case aXdebugOn, aXdebugOff, aXdebugStatus:
		op := map[string]string{aXdebugOn: "enable", aXdebugOff: "disable", aXdebugStatus: "status"}[action]
		return a.runInTerminal([]string{"bin/xdebug", op}, config.KindBin, nil)
	}
	return nil
}

func (a *App) opsDisabled(action string) string {
	switch action {
	case aCronStart, aCronStop, aCronStatus:
		if a.run != nil {
			return "a command is already running (ctrl+c cancels it)"
		}
	case aXdebugSetup:
		switch {
		case a.xdebugStyle() != "cookie":
			return "this project has no phpfpm-xdebug service"
		case !a.proj.HasBin("setup-nginx"):
			return "this project has no bin/setup-nginx"
		case a.xdebugRouting():
			return "already set up — nginx routes XDEBUG_SESSION requests to phpfpm-xdebug"
		case a.run != nil:
			return "a command is already running (ctrl+c cancels it)"
		}
	case aXdebugCLI:
		if a.xdebugStyle() != "cookie" {
			return "this project has no phpfpm-xdebug service"
		}
		if a.run != nil {
			return "a command is already running (ctrl+c cancels it)"
		}
	case aXdebugHelp:
		if a.xdebugStyle() != "cookie" {
			return "this project has no phpfpm-xdebug service"
		}
	case aXdebugOn, aXdebugOff, aXdebugStatus:
		if a.xdebugStyle() != "legacy" {
			return "not needed: this docker-magento routes debugging by cookie (see Xdebug: how to debug)"
		}
	}
	return ""
}
