// Package plan turns an action into the exact argv to run, according to the
// integration mode (section 9). Every argv is an argument list; nothing user
// typed is ever pasted into a shell string, except host-shell custom commands
// whose values are quoted by config.ExpandShell.
package plan

import (
	"fmt"
	"path/filepath"

	"lazymage/internal/config"
	"lazymage/internal/docker"
	"lazymage/internal/project"
)

type Builder struct {
	P          *project.Project
	Mode       string
	PHPService string
	DBService  string // "" means docker-magento's "db"
}

func New(p *project.Project, mode, phpService string) Builder {
	if phpService == "" {
		phpService = "phpfpm"
	}
	return Builder{P: p, Mode: mode, PHPService: phpService}
}

func (b Builder) has(name string) bool { return b.P != nil && b.P.HasBin(name) }

// Compose returns `docker compose [-f ...] args...`, or bin/docker-compose in
// bin-only mode when the project has it.
func (b Builder) Compose(args ...string) []string {
	if b.Mode == config.ModeBinOnly && b.has("docker-compose") {
		return append([]string{"bin/docker-compose"}, args...)
	}
	argv := []string{docker.Bin(), "compose"}
	if b.P != nil {
		argv = append(argv, b.P.ComposeArgs()...)
	}
	return append(argv, args...)
}

// DirectCompose always uses docker compose (hybrid status and logs).
func (b Builder) DirectCompose(args ...string) []string {
	argv := []string{docker.Bin(), "compose"}
	if b.P != nil {
		argv = append(argv, b.P.ComposeArgs()...)
	}
	return append(argv, args...)
}

// execT runs a non-interactive command in the PHP container without a TTY
// (stdin stays open so prompts can be answered).
func (b Builder) execT(args ...string) []string {
	return b.DirectCompose(append([]string{"exec", "-T", b.PHPService}, args...)...)
}

// Magento builds a bin/magento invocation.
// Hybrid / bin-only use the project's wrappers. Because the TUI streams output
// through pipes (not a terminal), bin/clinotty — docker-magento's no-TTY
// variant of bin/cli — is used when present, so `docker compose exec` never
// fails with "the input device is not a TTY".
func (b Builder) Magento(args ...string) []string {
	if b.Mode == config.ModeDockerOnly {
		return b.execT(append([]string{"bin/magento"}, args...)...)
	}
	if b.has("clinotty") {
		return append([]string{"bin/clinotty", "bin/magento"}, args...)
	}
	return append([]string{"bin/magento"}, args...)
}

// Composer builds a composer invocation (bin/composer where present).
func (b Builder) Composer(args ...string) []string {
	if b.Mode == config.ModeDockerOnly {
		return b.execT(append([]string{"composer"}, args...)...)
	}
	if b.has("clinotty") {
		return append([]string{"bin/clinotty", "composer"}, args...)
	}
	if b.has("composer") {
		return append([]string{"bin/composer"}, args...)
	}
	return b.execT(append([]string{"composer"}, args...)...)
}

// Lifecycle builds start / stop / restart for the whole stack.
func (b Builder) Lifecycle(action string) []string {
	if b.Mode != config.ModeDockerOnly && b.has(action) {
		return []string{"bin/" + action}
	}
	switch action {
	case "start":
		return b.Compose("up", "-d", "--remove-orphans")
	case "stop":
		return b.Compose("stop")
	default:
		return b.Compose("restart")
	}
}

// Bin builds a bin/<name> wrapper call.
func (b Builder) Bin(name string, args ...string) []string {
	return append([]string{"bin/" + name}, args...)
}

// HostShell runs a host shell command string.
func (b Builder) HostShell(cmd string) []string { return []string{"/bin/sh", "-c", cmd} }

// PS returns the status command and whether its output is JSON.
func (b Builder) PS() ([]string, bool) {
	if b.Mode == config.ModeBinOnly {
		if b.has("docker-compose") {
			return []string{"bin/docker-compose", "ps", "-a"}, false
		}
		return b.DirectCompose("ps", "-a"), false
	}
	return b.DirectCompose("ps", "-a", "--format", "json"), true
}

// Services lists services defined in the compose files (needs no engine).
func (b Builder) Services() []string { return b.Compose("config", "--services") }

// Logs streams compose logs (tail < 0 means all lines). since may be "" or an RFC3339 timestamp.
func (b Builder) Logs(service string, tail int, follow bool, since string) []string {
	t := fmt.Sprint(tail)
	if tail < 0 {
		t = "all"
	}
	args := []string{"logs", "--no-color", "--tail", t}
	if follow {
		args = append(args, "-f")
	}
	if since != "" {
		args = append(args, "--since", since)
	}
	if service != "" {
		args = append(args, service)
	}
	if b.Mode == config.ModeBinOnly {
		return b.Compose(args...)
	}
	return b.DirectCompose(args...)
}

// Shell builds the interactive shell into the PHP container. The configured
// shell (often the host login shell, e.g. /bin/zsh) is tried first and the
// session falls back to bash when the container does not have it.
func (b Builder) Shell(shell string) []string {
	name := filepath.Base(shell)
	if name == "" || name == "." || name == "/" {
		name = "bash"
	}
	if name == "bash" && b.Mode != config.ModeDockerOnly && b.has("bash") {
		return []string{"bin/bash"}
	}
	script := `if command -v "$0" >/dev/null 2>&1; then exec "$0"; fi; echo "lazymage: $0 is not installed in the container, using bash"; exec bash`
	return b.DirectCompose("exec", b.PHPService, "sh", "-c", script, name)
}

// Custom builds the argv for a custom command whose placeholders are filled.
func (b Builder) Custom(cc config.CustomCommand, values map[string]string) ([]string, error) {
	if cc.Kind == config.KindHostShell {
		return b.HostShell(config.ExpandShell(cc.Command, values)), nil
	}
	args, err := config.ExpandArgs(cc.Command, values)
	if err != nil {
		return nil, err
	}
	switch cc.Kind {
	case config.KindMagento:
		if len(args) > 0 && (args[0] == "bin/magento" || args[0] == "magento") {
			args = args[1:]
		}
		return b.Magento(args...), nil
	case config.KindComposer:
		if len(args) > 0 && (args[0] == "bin/composer" || args[0] == "composer") {
			args = args[1:]
		}
		return b.Composer(args...), nil
	case config.KindBin:
		if len(args) == 0 {
			return nil, fmt.Errorf("empty command")
		}
		name := args[0]
		if filepath.Dir(name) == "bin" {
			name = filepath.Base(name)
		}
		if filepath.Base(name) != name || name == ".." {
			return nil, fmt.Errorf("bin command must be a script name under bin/, got %q", args[0])
		}
		return b.Bin(name, args[1:]...), nil
	case config.KindCompose:
		if len(args) >= 2 && args[0] == "docker" && args[1] == "compose" {
			args = args[2:]
		}
		return b.Compose(args...), nil
	}
	return nil, fmt.Errorf("unknown kind %q", cc.Kind)
}

// Node runs a Node.js tool (npm, npx, node) in the PHP container, which in
// docker-magento has Node installed. bin/npm and bin/grunt use bin/cli (a
// TTY), so bin/clinotty is used for piped output, as for bin/magento.
func (b Builder) Node(tool string, args ...string) []string {
	if b.Mode != config.ModeDockerOnly && b.has("clinotty") {
		return append([]string{"bin/clinotty", tool}, args...)
	}
	return b.execT(append([]string{tool}, args...)...)
}

// Grunt runs `npx grunt args...`.
func (b Builder) Grunt(args ...string) []string {
	return b.Node("npx", append([]string{"grunt"}, args...)...)
}

// GruntBuild cleans, deploys and compiles one theme in a single run:
// grunt clean:<theme> && grunt exec:<theme> && grunt less:<theme>.
func (b Builder) GruntBuild(theme string) []string {
	script := `npx grunt clean:"$1" && npx grunt exec:"$1" && npx grunt less:"$1"`
	return b.Node("sh", "-c", script, "lazymage", theme)
}

// RestartService restarts one service (bin/restart takes service names too).
func (b Builder) RestartService(svc string) []string {
	if b.Mode != config.ModeDockerOnly && b.has("restart") {
		return []string{"bin/restart", svc}
	}
	return b.Compose("restart", svc)
}
