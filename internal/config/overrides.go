package config

import (
	"os"
	"sort"
	"time"
)

// Overrides are per-project settings that win over the global ones. Empty
// fields fall through to the global value.
type Overrides struct {
	IntegrationMode string `yaml:"integration_mode,omitempty"`
	Shell           string `yaml:"shell,omitempty"`
	Editor          string `yaml:"editor,omitempty"`
	Browser         string `yaml:"browser,omitempty"`
	RefreshSeconds  int    `yaml:"refresh_seconds,omitempty"`
}

// OverridableSettings are the setting IDs a project can override.
var OverridableSettings = []string{"mode", "shell", "editor", "browser", "refresh"}

// Effective is the settings in force for one project.
type Effective struct {
	IntegrationMode string
	Shell           string
	Editor          string
	Browser         string
	RefreshSeconds  int
	Overridden      map[string]bool // setting ID → set by the project
}

// Effective merges the project's overrides (if any) over the global settings.
// It never creates a project entry.
func (c *Config) Effective(root string) Effective {
	e := Effective{IntegrationMode: c.IntegrationMode, Shell: c.Shell, Editor: c.Editor, Browser: c.Browser,
		RefreshSeconds: c.RefreshSeconds, Overridden: map[string]bool{}}
	p := c.Projects[root]
	if p == nil || p.Overrides == nil {
		return e
	}
	o := p.Overrides
	set := func(id string, dst *string, v string) {
		if v != "" {
			*dst = v
			e.Overridden[id] = true
		}
	}
	set("mode", &e.IntegrationMode, o.IntegrationMode)
	set("shell", &e.Shell, o.Shell)
	set("editor", &e.Editor, o.Editor)
	set("browser", &e.Browser, o.Browser)
	if o.RefreshSeconds > 0 {
		e.RefreshSeconds = o.RefreshSeconds
		e.Overridden["refresh"] = true
	}
	return e
}

// SetOverride sets (or, with clear, removes) one project override, starting
// from value. Empty override sets are removed so the file stays tidy.
func (c *Config) SetOverride(root, id, value string, refresh int, clear bool) {
	p := c.Project(root)
	if p.Overrides == nil {
		p.Overrides = &Overrides{}
	}
	o := p.Overrides
	switch id {
	case "mode":
		o.IntegrationMode = pick(value, clear)
	case "shell":
		o.Shell = pick(value, clear)
	case "editor":
		o.Editor = pick(value, clear)
	case "browser":
		o.Browser = pick(value, clear)
	case "refresh":
		if clear {
			o.RefreshSeconds = 0
		} else {
			o.RefreshSeconds = refresh
		}
	}
	if *o == (Overrides{}) {
		p.Overrides = nil
	}
}

func pick(v string, clear bool) string {
	if clear {
		return ""
	}
	return v
}

// KnownProject is a project lazymage has opened before.
type KnownProject struct {
	Root       string
	LastOpened time.Time
	Missing    bool // the directory no longer exists
}

// TouchProject records that a project was opened now.
func (c *Config) TouchProject(root string, now time.Time) {
	c.Project(root).LastOpened = now.UTC().Truncate(time.Second)
}

// KnownProjects lists every project in the config, most recently opened first.
func (c *Config) KnownProjects() []KnownProject {
	var out []KnownProject
	for root, p := range c.Projects {
		if p == nil {
			continue
		}
		_, err := os.Stat(root)
		out = append(out, KnownProject{Root: root, LastOpened: p.LastOpened, Missing: err != nil})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastOpened.Equal(out[j].LastOpened) {
			return out[i].LastOpened.After(out[j].LastOpened)
		}
		return out[i].Root < out[j].Root
	})
	return out
}
