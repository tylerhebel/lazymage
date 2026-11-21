// Package config loads and saves ~/.config/lazymage/config.yaml.
//
// Field names are the contract from section 11 of the spec. Unknown keys at any
// level are preserved on save through inline "Extra" maps. A malformed file is
// never overwritten: Load returns a *ParseError and the caller decides.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const (
	RuntimeOrbStack      = "orbstack"
	RuntimeDockerDesktop = "docker-desktop"
	RuntimeDockerEngine  = "docker-engine"

	ModeHybrid     = "hybrid"
	ModeBinOnly    = "bin-only"
	ModeDockerOnly = "docker-only"

	KindMagento   = "magento"
	KindComposer  = "composer"
	KindBin       = "bin"
	KindCompose   = "compose"
	KindHostShell = "host-shell"

	HistoryCap = 50
)

var (
	Runtimes = []string{RuntimeOrbStack, RuntimeDockerDesktop, RuntimeDockerEngine}
	Modes    = []string{ModeHybrid, ModeBinOnly, ModeDockerOnly}
	Kinds    = []string{KindMagento, KindComposer, KindBin, KindCompose, KindHostShell}
)

// DefaultFavorites is the preset list from section 8.3.
var DefaultFavorites = []string{
	"cache:flush", "cache:clean", "indexer:reindex", "setup:upgrade", "setup:di:compile",
	"setup:static-content:deploy", "deploy:mode:show", "config:show", "module:status", "cron:run",
}

type Logs struct {
	TailLines    int            `yaml:"tail_lines"`
	FollowOnOpen bool           `yaml:"follow_on_open"`
	Extra        map[string]any `yaml:",inline"`
}

type Prompt struct {
	Key      string         `yaml:"key"`
	Label    string         `yaml:"label"`
	Default  string         `yaml:"default"`
	Required bool           `yaml:"required"`
	Extra    map[string]any `yaml:",inline"`
}

type CustomCommand struct {
	Name     string         `yaml:"name"`
	Category string         `yaml:"category"`
	Kind     string         `yaml:"kind"`
	Command  string         `yaml:"command"`
	Prompts  []Prompt       `yaml:"prompts"`
	Confirm  bool           `yaml:"confirm"`
	Shortcut string         `yaml:"shortcut"`
	Pinned   bool           `yaml:"pinned"`
	Extra    map[string]any `yaml:",inline"`
}

type HistoryEntry struct {
	Kind       string         `yaml:"kind"`
	Command    string         `yaml:"command"` // redacted
	Started    time.Time      `yaml:"started"`
	DurationMs int64          `yaml:"duration_ms"`
	ExitCode   int            `yaml:"exit_code"`
	Extra      map[string]any `yaml:",inline"`
}

type Project struct {
	History        []HistoryEntry  `yaml:"history"`
	CustomCommands []CustomCommand `yaml:"custom_commands"`
	Extra          map[string]any  `yaml:",inline"`
}

type Config struct {
	Runtime         string              `yaml:"runtime"`
	IntegrationMode string              `yaml:"integration_mode"`
	Shell           string              `yaml:"shell"`
	Editor          string              `yaml:"editor"`
	Browser         string              `yaml:"browser"`
	Logs            Logs                `yaml:"logs"`
	RefreshSeconds  int                 `yaml:"refresh_seconds"`
	Favorites       []string            `yaml:"favorites"`
	CustomCommands  []CustomCommand     `yaml:"custom_commands"`
	Projects        map[string]*Project `yaml:"projects"`
	Extra           map[string]any      `yaml:",inline"`

	// Problems found while loading (malformed custom commands etc.). Not saved.
	Warnings []string `yaml:"-"`
}

// ParseError means the file exists but is not valid config. It must not be overwritten.
type ParseError struct {
	Path string
	Err  error
}

func (e *ParseError) Error() string { return fmt.Sprintf("%s: %v", e.Path, e.Err) }
func (e *ParseError) Unwrap() error { return e.Err }

// Dir is the lazymage config directory: $XDG_CONFIG_HOME/lazymage or ~/.config/lazymage
// on both macOS and Linux, as the spec names it.
func Dir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "lazymage")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "lazymage")
}

func Path() string { return filepath.Join(Dir(), "config.yaml") }

// Defaults returns factory settings. loginShell is the user's detected login shell.
func Defaults(loginShell string) *Config {
	if loginShell == "" {
		loginShell = "bash"
	}
	return &Config{
		Runtime:         "",
		IntegrationMode: ModeHybrid,
		Shell:           loginShell,
		Editor:          "",
		Browser:         "default",
		Logs:            Logs{TailLines: 200, FollowOnOpen: true},
		RefreshSeconds:  2,
		Favorites:       append([]string(nil), DefaultFavorites...),
		Projects:        map[string]*Project{},
	}
}

// Load reads the config at path. It returns os.ErrNotExist (wrapped) if the
// file does not exist, and *ParseError if it cannot be parsed.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Decode in two passes: everything except custom commands first, then each
	// custom command on its own, so one bad entry cannot break the rest.
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, &ParseError{Path: path, Err: err}
	}
	c := &Config{}
	if len(doc.Content) == 0 { // empty file
		c.normalize()
		return c, nil
	}
	top := doc.Content[0]
	if top.Kind != yaml.MappingNode {
		return nil, &ParseError{Path: path, Err: errors.New("top level must be a mapping")}
	}
	globalCmds := takeKey(top, "custom_commands")
	projects := takeKey(top, "projects")
	if err := top.Decode(c); err != nil {
		return nil, &ParseError{Path: path, Err: err}
	}
	c.CustomCommands = decodeCommands(seqItems(globalCmds), "global", &c.Warnings)
	c.Projects = map[string]*Project{}
	if projects != nil {
		if projects.Kind != yaml.MappingNode {
			return nil, &ParseError{Path: path, Err: errors.New("projects must be a mapping")}
		}
		for i := 0; i+1 < len(projects.Content); i += 2 {
			root, pn := projects.Content[i].Value, projects.Content[i+1]
			if pn.Kind != yaml.MappingNode {
				c.Warnings = append(c.Warnings, fmt.Sprintf("project %s: entry skipped (not a mapping)", root))
				continue
			}
			cmds := takeKey(pn, "custom_commands")
			p := &Project{}
			if err := pn.Decode(p); err != nil {
				return nil, &ParseError{Path: path, Err: fmt.Errorf("project %s: %w", root, err)}
			}
			p.CustomCommands = decodeCommands(seqItems(cmds), "project "+root, &c.Warnings)
			c.Projects[root] = p
		}
	}
	c.normalize()
	return c, nil
}

// takeKey removes key from a mapping node and returns its value node (or nil).
func takeKey(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			v := m.Content[i+1]
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return v
		}
	}
	return nil
}

func seqItems(n *yaml.Node) []yaml.Node {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	out := make([]yaml.Node, len(n.Content))
	for i, c := range n.Content {
		out[i] = *c
	}
	return out
}

func decodeCommands(nodes []yaml.Node, scope string, warnings *[]string) []CustomCommand {
	var out []CustomCommand
	for i := range nodes {
		var cc CustomCommand
		if err := nodes[i].Decode(&cc); err != nil {
			*warnings = append(*warnings, fmt.Sprintf("custom command #%d (%s) skipped: %v", i+1, scope, err))
			continue
		}
		if errs := ValidateShape(cc); len(errs) > 0 {
			*warnings = append(*warnings, fmt.Sprintf("custom command %q (%s) skipped: %s", cc.Name, scope, strings.Join(errs, "; ")))
			continue
		}
		out = append(out, cc)
	}
	return out
}

// ValidateShape checks a custom command on its own (no uniqueness checks).
func ValidateShape(cc CustomCommand) []string {
	var errs []string
	if strings.TrimSpace(cc.Name) == "" {
		errs = append(errs, "name is required")
	}
	if strings.TrimSpace(cc.Command) == "" {
		errs = append(errs, "command is required")
	}
	if !contains(Kinds, cc.Kind) {
		errs = append(errs, fmt.Sprintf("kind must be one of %s", strings.Join(Kinds, ", ")))
	}
	defined := map[string]bool{}
	for _, p := range cc.Prompts {
		defined[p.Key] = true
	}
	for _, ph := range Placeholders(cc.Command) {
		if !defined[ph] {
			errs = append(errs, fmt.Sprintf("{{%s}} has no prompt definition", ph))
		}
	}
	return errs
}

func (c *Config) normalize() {
	if c.IntegrationMode == "" {
		c.IntegrationMode = ModeHybrid
	}
	if c.Logs.TailLines <= 0 {
		c.Logs.TailLines = 200
	}
	if c.RefreshSeconds <= 0 {
		c.RefreshSeconds = 2
	}
	if c.Browser == "" {
		c.Browser = "default"
	}
	if c.Projects == nil {
		c.Projects = map[string]*Project{}
	}
}

// Validate reports problems that make a loaded config unusable without setup
// (for example an unknown runtime value). It returns the setup steps to re-open.
func (c *Config) InvalidSteps() []string {
	var s []string
	if !contains(Runtimes, c.Runtime) {
		s = append(s, "runtime")
	}
	if !contains(Modes, c.IntegrationMode) {
		s = append(s, "integration")
	}
	return s
}

// Save writes the config atomically (temp file + rename).
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return err
	}
	_ = enc.Close()
	data := buf.Bytes()
	header := []byte("# lazymage config — edited by lazymage setup and Settings. Unknown keys are kept.\n")
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(header, data...)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// BackupBroken renames a malformed config aside so setup can start fresh
// without destroying it. Returns the backup path.
func BackupBroken(path string) (string, error) {
	b := path + ".broken-" + time.Now().Format("20060102-150405")
	return b, os.Rename(path, b)
}

func (c *Config) Clone() *Config {
	data, _ := yaml.Marshal(c)
	var n Config
	_ = yaml.Unmarshal(data, &n)
	n.normalize()
	n.Warnings = append([]string(nil), c.Warnings...)
	return &n
}

func (c *Config) Project(root string) *Project {
	if c.Projects == nil {
		c.Projects = map[string]*Project{}
	}
	p := c.Projects[root]
	if p == nil {
		p = &Project{}
		c.Projects[root] = p
	}
	return p
}

// AddHistory appends a (pre-redacted) entry, capped at HistoryCap.
func (c *Config) AddHistory(root string, e HistoryEntry) {
	p := c.Project(root)
	p.History = append(p.History, e)
	if n := len(p.History); n > HistoryCap {
		p.History = append([]HistoryEntry(nil), p.History[n-HistoryCap:]...)
	}
}

func (c *Config) IsFavorite(cmd string) bool { return contains(c.Favorites, cmd) }

func (c *Config) ToggleFavorite(cmd string) {
	for i, f := range c.Favorites {
		if f == cmd {
			c.Favorites = append(c.Favorites[:i], c.Favorites[i+1:]...)
			return
		}
	}
	c.Favorites = append(c.Favorites, cmd)
}

// Scoped identifies a custom command and where it lives.
type Scoped struct {
	Cmd        CustomCommand
	Project    bool // true: project scope
	Index      int  // index within its scope's slice
	Overridden bool // a global shadowed by a project command of the same name
}

// EffectiveCommands returns global + project commands for root, with project
// commands overriding globals by name. Overridden globals are included and flagged.
func (c *Config) EffectiveCommands(root string) []Scoped {
	var out []Scoped
	proj := map[string]bool{}
	if root != "" {
		if p := c.Projects[root]; p != nil {
			for i, cc := range p.CustomCommands {
				proj[strings.ToLower(cc.Name)] = true
				out = append(out, Scoped{Cmd: cc, Project: true, Index: i})
			}
		}
	}
	for i, cc := range c.CustomCommands {
		out = append(out, Scoped{Cmd: cc, Index: i, Overridden: proj[strings.ToLower(cc.Name)]})
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Cmd.Name) < strings.ToLower(out[j].Cmd.Name) })
	return out
}

// Commands returns the slice for a scope, for editing.
func (c *Config) Commands(root string, project bool) *[]CustomCommand {
	if project {
		return &c.Project(root).CustomCommands
	}
	return &c.CustomCommands
}

// IsNotExist reports whether err means "no config file yet".
func IsNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
