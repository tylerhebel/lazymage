// Package docker talks to the container runtime, only through the docker and
// docker compose CLIs (section 6): runtime detection, engine start, and
// parsing of compose status output.
package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"lazymage/internal/config"
)

// Bin is the docker CLI to call; tests and the demo harness override it via LAZYMAGE_DOCKER.
func Bin() string {
	if b := os.Getenv("LAZYMAGE_DOCKER"); b != "" {
		return b
	}
	return "docker"
}

func run(ctx context.Context, dir string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, Bin(), args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// Info is what `docker info` and `docker context` tell us.
type Info struct {
	CLIFound      bool
	CLIVersion    string
	Reachable     bool   // docker info succeeded
	ServerOS      string // OperatingSystem, e.g. "OrbStack", "Docker Desktop", "Ubuntu 24.04"
	ServerName    string // Name
	ServerVersion string
	Context       string
	ContextHost   string
	Err           string // stderr from docker info when unreachable
	Detected      string // config.Runtime* of the running engine, "" if unknown
}

// Probe runs docker info / context show. It never blocks longer than timeout.
func Probe(timeout time.Duration) Info {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var in Info
	if _, err := exec.LookPath(Bin()); err != nil {
		return in
	}
	in.CLIFound = true
	if out, _, err := run(ctx, "", "version", "--format", "{{.Client.Version}}"); err == nil || out != "" {
		in.CLIVersion = strings.TrimSpace(out)
	}
	if out, _, err := run(ctx, "", "context", "show"); err == nil {
		in.Context = strings.TrimSpace(out)
	}
	if out, _, err := run(ctx, "", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}"); err == nil {
		in.ContextHost = strings.TrimSpace(out)
	}
	out, stderr, err := run(ctx, "", "info", "--format", "{{json .}}")
	if err != nil {
		in.Err = firstLine(stderr)
		if in.Err == "" && ctx.Err() != nil {
			in.Err = "docker info timed out"
		}
		return in
	}
	var di struct {
		OperatingSystem string `json:"OperatingSystem"`
		Name            string `json:"Name"`
		ServerVersion   string `json:"ServerVersion"`
		ServerErrors    []string
	}
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &di) != nil || di.ServerVersion == "" {
		// `docker info --format json` prints client info even when the server is
		// unreachable on some versions; ServerVersion is the reliable signal.
		in.Err = firstLine(strings.Join(di.ServerErrors, "; ") + stderr)
		if in.Err == "" {
			in.Err = "server not reachable"
		}
		return in
	}
	in.Reachable = true
	in.ServerOS, in.ServerName, in.ServerVersion = di.OperatingSystem, di.Name, di.ServerVersion
	in.Detected = Classify(in.ServerOS, in.ServerName, in.Context, in.ContextHost)
	return in
}

// Classify maps docker info / context values to a runtime, matching on the
// returned values rather than one hard-coded string.
func Classify(serverOS, name, ctx, host string) string {
	all := strings.ToLower(strings.Join([]string{serverOS, name, ctx, host}, " "))
	switch {
	case strings.Contains(all, "orbstack"):
		return config.RuntimeOrbStack
	case strings.Contains(all, "docker desktop") || strings.Contains(all, "desktop-linux") || strings.Contains(all, "docker-desktop") || strings.Contains(all, "/.docker/desktop/") || strings.Contains(all, "/.docker/run/docker.sock"):
		return config.RuntimeDockerDesktop
	case serverOS != "":
		return config.RuntimeDockerEngine
	}
	return ""
}

// Candidate is one runtime option on setup step 2.
type Candidate struct {
	Runtime   string
	Installed bool
	Running   bool
	Evidence  []string
}

// Candidates lists all three runtimes with install / running evidence.
func Candidates(in Info) []Candidate {
	home, _ := os.UserHomeDir()
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	ctxs := contextNames()

	orb := Candidate{Runtime: config.RuntimeOrbStack}
	if p, err := exec.LookPath("orb"); err == nil {
		orb.Installed = true
		orb.Evidence = append(orb.Evidence, "orb CLI: "+p)
	}
	if exists("/Applications/OrbStack.app") {
		orb.Installed = true
		orb.Evidence = append(orb.Evidence, "/Applications/OrbStack.app")
	}
	if ctxs["orbstack"] {
		orb.Installed = true
		orb.Evidence = append(orb.Evidence, "docker context: orbstack")
	}

	dd := Candidate{Runtime: config.RuntimeDockerDesktop}
	if exists("/Applications/Docker.app") {
		dd.Installed = true
		dd.Evidence = append(dd.Evidence, "/Applications/Docker.app")
	}
	if exists(filepath.Join(home, ".docker", "desktop")) {
		dd.Installed = true
		dd.Evidence = append(dd.Evidence, "~/.docker/desktop")
	}
	if ctxs["desktop-linux"] {
		dd.Installed = true
		dd.Evidence = append(dd.Evidence, "docker context: desktop-linux")
	}

	eng := Candidate{Runtime: config.RuntimeDockerEngine}
	if runtime.GOOS == "linux" {
		for _, p := range []string{"/usr/bin/dockerd", "/usr/local/bin/dockerd", "/var/run/docker.sock"} {
			if exists(p) {
				eng.Installed = true
				eng.Evidence = append(eng.Evidence, p)
			}
		}
	}

	cands := []Candidate{orb, dd, eng}
	for i := range cands {
		c := &cands[i]
		if in.Reachable && in.Detected == c.Runtime {
			c.Running, c.Installed = true, true
			c.Evidence = append(c.Evidence, fmt.Sprintf("docker info: %s (server %s)", in.ServerOS, in.ServerVersion))
		}
		if in.Context != "" && in.Detected == c.Runtime {
			c.Evidence = append(c.Evidence, "active context: "+in.Context)
		}
		if in.CLIVersion != "" && c.Installed {
			c.Evidence = append(c.Evidence, "docker CLI "+in.CLIVersion)
		}
		if !c.Installed {
			c.Evidence = append(c.Evidence, "not found")
		}
	}
	return cands
}

func contextNames() map[string]bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, _, _ := run(ctx, "", "context", "ls", "--format", "{{.Name}}")
	m := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(l), "*")); l != "" {
			m[strings.TrimSpace(l)] = true
		}
	}
	return m
}

// RuntimeInstalled reports whether a saved runtime still looks installed;
// used for the partial setup re-run.
func RuntimeInstalled(rt string, in Info) bool {
	for _, c := range Candidates(in) {
		if c.Runtime == rt {
			return c.Installed || c.Running
		}
	}
	return false
}

// StartPlan says how to start an engine: a command to run, or (Linux Docker
// Engine) a command to show the user instead of running it.
type StartPlan struct {
	Argv     []string
	ShowOnly string
}

func StartPlanFor(rt string) StartPlan {
	switch rt {
	case config.RuntimeOrbStack:
		return StartPlan{Argv: []string{"orb", "start"}}
	case config.RuntimeDockerDesktop:
		if runtime.GOOS == "darwin" {
			return StartPlan{Argv: []string{"open", "-a", "Docker"}}
		}
		return StartPlan{Argv: []string{"systemctl", "--user", "start", "docker-desktop"}}
	default:
		if runtime.GOOS == "darwin" {
			return StartPlan{ShowOnly: "start your Docker engine, then press r"}
		}
		return StartPlan{ShowOnly: "sudo systemctl start docker"}
	}
}

// Container is one row of compose status.
type Container struct {
	Name    string `json:"Name"`
	Service string `json:"Service"`
	State   string `json:"State"`  // running, exited, restarting, created, paused, dead
	Health  string `json:"Health"` // healthy, unhealthy, starting, ""
	Status  string `json:"Status"`
	Image   string `json:"Image"`
}

// ParsePSJSON accepts both forms compose has used: a JSON array (v2.0–2.20)
// and newline-delimited objects (v2.21+).
func ParsePSJSON(out string) ([]Container, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	var cs []Container
	if strings.HasPrefix(out, "[") {
		if err := json.Unmarshal([]byte(out), &cs); err != nil {
			return nil, err
		}
		return normalize(cs), nil
	}
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var c Container
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("compose ps: %w", err)
		}
		cs = append(cs, c)
	}
	return normalize(cs), nil
}

func normalize(cs []Container) []Container {
	for i := range cs {
		cs[i].State = strings.ToLower(cs[i].State)
		cs[i].Health = strings.ToLower(cs[i].Health)
		if cs[i].Health == "" {
			s := strings.ToLower(cs[i].Status)
			switch {
			case strings.Contains(s, "(healthy)"):
				cs[i].Health = "healthy"
			case strings.Contains(s, "(unhealthy)"):
				cs[i].Health = "unhealthy"
			case strings.Contains(s, "health: starting"):
				cs[i].Health = "starting"
			}
		}
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].Service < cs[j].Service })
	return cs
}

// ParsePSText parses plain `docker compose ps` (bin-only mode) by header columns.
func ParsePSText(out string) []Container {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 {
		return nil
	}
	hdr := lines[0]
	col := func(name string) int {
		i := strings.Index(hdr, name)
		if i < 0 {
			return -1
		}
		return len([]rune(hdr[:i]))
	}
	cols := []struct {
		name string
		at   int
	}{{"NAME", col("NAME")}, {"IMAGE", col("IMAGE")}, {"COMMAND", col("COMMAND")}, {"SERVICE", col("SERVICE")}, {"CREATED", col("CREATED")}, {"STATUS", col("STATUS")}, {"PORTS", col("PORTS")}}
	var present []struct {
		name string
		at   int
	}
	for _, c := range cols {
		if c.at >= 0 {
			present = append(present, c)
		}
	}
	sort.Slice(present, func(i, j int) bool { return present[i].at < present[j].at })
	// Column offsets are in runes: COMMAND values contain "…".
	field := func(l, name string) string {
		line := []rune(l)
		for i, c := range present {
			if c.name != name {
				continue
			}
			end := len(line)
			if i+1 < len(present) && present[i+1].at < end {
				end = present[i+1].at
			}
			if c.at >= len(line) {
				return ""
			}
			return strings.TrimSpace(string(line[c.at:end]))
		}
		return ""
	}
	var cs []Container
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		c := Container{Name: field(l, "NAME"), Image: field(l, "IMAGE"), Service: field(l, "SERVICE"), Status: field(l, "STATUS")}
		s := strings.ToLower(c.Status)
		switch {
		case strings.HasPrefix(s, "up"):
			c.State = "running"
		case strings.HasPrefix(s, "restarting"):
			c.State = "restarting"
		case strings.HasPrefix(s, "exited"):
			c.State = "exited"
		case strings.HasPrefix(s, "created"):
			c.State = "created"
		case strings.HasPrefix(s, "paused"):
			c.State = "paused"
		default:
			c.State = s
		}
		cs = append(cs, c)
	}
	return normalize(cs)
}

// Health is the header's overall indicator.
type Health int

const (
	HealthUnknown Health = iota
	HealthAllUp
	HealthSomeDown
	HealthEngineStopped
)

func (h Health) Text() string {
	switch h {
	case HealthAllUp:
		return "all up"
	case HealthSomeDown:
		return "some down"
	case HealthEngineStopped:
		return "engine stopped"
	}
	return "unknown"
}

// Summary is overall health plus counts.
type Summary struct {
	Health   Health
	Up       int
	Total    int
	Starting int
}

// Up reports whether a container counts as up: running and not unhealthy / still starting.
func Up(c Container) bool {
	return c.State == "running" && c.Health != "unhealthy" && c.Health != "starting"
}

// Summarize computes health from the expected service list and containers.
// Services with no container count as down.
func Summarize(expected []string, cs []Container) Summary {
	byService := map[string][]Container{}
	for _, c := range cs {
		byService[c.Service] = append(byService[c.Service], c)
	}
	svcs := expected
	if len(svcs) == 0 {
		for s := range byService {
			svcs = append(svcs, s)
		}
	}
	var s Summary
	s.Total = len(svcs)
	for _, name := range svcs {
		list := byService[name]
		ok := len(list) > 0
		for _, c := range list {
			if !Up(c) {
				ok = false
			}
			if c.Health == "starting" {
				s.Starting++
			}
		}
		if ok {
			s.Up++
		}
	}
	switch {
	case s.Total == 0:
		s.Health = HealthSomeDown
	case s.Up == s.Total:
		s.Health = HealthAllUp
	default:
		s.Health = HealthSomeDown
	}
	return s
}

// IsEngineDown guesses from a compose/docker error whether the daemon is unreachable.
func IsEngineDown(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "cannot connect to the docker daemon") ||
		strings.Contains(s, "is the docker daemon running") ||
		strings.Contains(s, "error during connect") ||
		strings.Contains(s, "connection refused") && strings.Contains(s, "docker") ||
		strings.Contains(s, "no such file or directory") && strings.Contains(s, ".sock")
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
