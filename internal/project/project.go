// Package project finds a docker-magento project root and reads the facts
// that do not need a running engine.
package project

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ComposeFileNames are the compose files that mark a project root (section 7).
var ComposeFileNames = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// Markers describes what detection looks for, for the not-a-project screen.
var Markers = []string{
	"a Docker Compose file (" + strings.Join(ComposeFileNames, ", ") + ")",
	"an executable bin/magento",
	"a src/ directory",
}

const Unknown = "unknown"

type Project struct {
	Root        string
	Name        string
	ComposeFile string // the marker compose file, e.g. compose.yaml
}

// FindRoot walks up from start until a directory has all three markers.
// It returns ok=false at the filesystem root.
func FindRoot(start string) (*Project, bool) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return nil, false
	}
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	for {
		if p, ok := isRoot(dir); ok {
			return p, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, false
		}
		dir = parent
	}
}

func isRoot(dir string) (*Project, bool) {
	compose := ""
	for _, n := range ComposeFileNames {
		if fi, err := os.Stat(filepath.Join(dir, n)); err == nil && !fi.IsDir() {
			compose = n
			break
		}
	}
	if compose == "" {
		return nil, false
	}
	fi, err := os.Stat(filepath.Join(dir, "bin", "magento"))
	if err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
		return nil, false
	}
	if fi, err := os.Stat(filepath.Join(dir, "src")); err != nil || !fi.IsDir() {
		return nil, false
	}
	return &Project{Root: dir, Name: filepath.Base(dir), ComposeFile: compose}, true
}

// HasBin reports whether bin/<name> exists and is executable.
func (p *Project) HasBin(name string) bool {
	fi, err := os.Stat(filepath.Join(p.Root, "bin", name))
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

// ComposeArgs mirrors docker-magento's bin/docker-compose file selection so
// direct `docker compose` calls see the same stack as the bin scripts:
// compose.yaml + compose.healthcheck.yaml + compose.dev.yaml (+ versions, override).
// When only the marker file exists, no -f flags are passed (compose's own discovery).
func (p *Project) ComposeArgs() []string {
	files := []string{p.ComposeFile}
	extra := false
	for _, f := range []string{"compose.healthcheck.yaml", "compose.dev.yaml", "compose.versions.yaml", "compose.override.yaml"} {
		if _, err := os.Stat(filepath.Join(p.Root, f)); err == nil {
			files = append(files, f)
			if f != "compose.override.yaml" {
				extra = true
			}
		}
	}
	if !extra {
		return nil
	}
	var args []string
	for _, f := range files {
		args = append(args, "-f", f)
	}
	return args
}

// Facts are the values shown in the header. Empty means unknown.
type Facts struct {
	Domain         string `json:"domain"`
	DomainSource   string `json:"domain_source"`
	MagentoVersion string `json:"magento_version"`
	VersionSource  string `json:"version_source"`
	DeployMode     string `json:"deploy_mode"`
	ModeSource     string `json:"mode_source"`
	PHPVersion     string `json:"php_version"`
	SearchEngine   string `json:"search_engine"`
	AdminPath      string `json:"admin_path"`
	PHPService     string `json:"php_service"`
}

// Or returns s, or "unknown" when empty.
func Or(s string) string {
	if s == "" {
		return Unknown
	}
	return s
}

// StaticFacts reads everything available from files, without Docker.
func (p *Project) StaticFacts() Facts {
	var f Facts
	if d, src := p.domainFromFiles(); d != "" {
		f.Domain, f.DomainSource = d, src
	}
	envphp, _ := os.ReadFile(filepath.Join(p.Root, "src", "app", "etc", "env.php"))
	f.AdminPath = ParseAdminFrontName(string(envphp))
	if m := ParseEnvPHPMode(string(envphp)); m != "" {
		f.DeployMode, f.ModeSource = m, "env.php"
	}
	if v := p.versionFromComposer(); v != "" {
		f.MagentoVersion, f.VersionSource = v, "composer"
	}
	compose, _ := os.ReadFile(filepath.Join(p.Root, p.ComposeFile))
	f.PHPVersion = PHPVersionFromImage(string(compose))
	f.SearchEngine = SearchEngineFromText(string(compose))
	f.PHPService = "phpfpm"
	return f
}

var envDomainKeys = []string{"DOMAIN", "MAGENTO_DOMAIN", "BASE_DOMAIN", "VIRTUAL_HOST", "APP_DOMAIN", "MAGENTO_HOST"}

// domainFromFiles: root .env (section 7), then a base_url pinned in env.php.
// Stock docker-magento keeps the domain only in the database (bin/setup-domain
// runs config:set), so the engine-side lookup in Phase 2 covers that case.
func (p *Project) domainFromFiles() (string, string) {
	env := ReadEnvFile(filepath.Join(p.Root, ".env"))
	for _, k := range envDomainKeys {
		if v := env[k]; v != "" {
			return HostOf(v), ".env " + k
		}
	}
	for k, v := range env {
		if strings.Contains(k, "BASE_URL") && v != "" {
			return HostOf(v), ".env " + k
		}
	}
	envphp, _ := os.ReadFile(filepath.Join(p.Root, "src", "app", "etc", "env.php"))
	if u := ParseEnvPHPBaseURL(string(envphp)); u != "" {
		return HostOf(u), "env.php"
	}
	return "", ""
}

// ReadEnvFile parses KEY=VALUE lines (optional export, quotes stripped).
func ReadEnvFile(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out[strings.TrimSpace(k)] = v
	}
	return out
}

// HostOf turns "https://magento.test/" or "magento.test" into "magento.test".
func HostOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	s = strings.TrimSuffix(strings.SplitN(s, "/", 2)[0], "/")
	return s
}

var (
	frontNameRe  = regexp.MustCompile(`['"]frontName['"]\s*=>\s*['"]([^'"]+)['"]`)
	mageModeRe   = regexp.MustCompile(`['"]MAGE_MODE['"]\s*=>\s*['"]([a-z]+)['"]`)
	baseURLRe    = regexp.MustCompile(`['"]base_url['"]\s*=>\s*['"](https?://[^'"]+)['"]`)
	phpImageRe   = regexp.MustCompile(`magento-php:(\d+\.\d+)`)
	phpGenericRe = regexp.MustCompile(`(?m)image:\s*\S*php\S*:(\d+\.\d+)`)
	cliVersionRe = regexp.MustCompile(`(?i)magento cli (?:version )?v?([0-9][0-9A-Za-z.\-]*)`)
	modeShowRe   = regexp.MustCompile(`(?i)current application mode:\s*([a-z]+)`)
)

func ParseAdminFrontName(envphp string) string {
	if m := frontNameRe.FindStringSubmatch(envphp); m != nil {
		return m[1]
	}
	return ""
}

func ParseEnvPHPMode(envphp string) string {
	if m := mageModeRe.FindStringSubmatch(envphp); m != nil {
		return m[1]
	}
	return ""
}

func ParseEnvPHPBaseURL(envphp string) string {
	if m := baseURLRe.FindStringSubmatch(envphp); m != nil {
		return m[1]
	}
	return ""
}

// ParseCLIVersion reads `bin/magento --version` output ("Magento CLI 2.4.7-p3").
func ParseCLIVersion(out string) string {
	if m := cliVersionRe.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

// ParseModeShow reads `bin/magento deploy:mode:show` output.
func ParseModeShow(out string) string {
	if m := modeShowRe.FindStringSubmatch(out); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

// ParseBaseURL reads `bin/magento config:show web/secure/base_url` output.
func ParseBaseURL(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			return line
		}
	}
	return ""
}

func PHPVersionFromImage(s string) string {
	if m := phpImageRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	if m := phpGenericRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// SearchEngineFromText looks for an uncommented opensearch / elasticsearch image.
func SearchEngineFromText(compose string) string {
	for _, line := range strings.Split(compose, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") || !strings.HasPrefix(t, "image:") {
			continue
		}
		switch {
		case strings.Contains(t, "opensearch"):
			return "OpenSearch"
		case strings.Contains(t, "elasticsearch"):
			return "Elasticsearch"
		}
	}
	return ""
}

// versionFromComposer reads the magento2-base version from src/composer.lock,
// then the root version from src/composer.json (same fallbacks as bin/magento-version).
func (p *Project) versionFromComposer() string {
	if data, err := os.ReadFile(filepath.Join(p.Root, "src", "composer.lock")); err == nil {
		var lock struct {
			Packages []struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"packages"`
		}
		if json.Unmarshal(data, &lock) == nil {
			for _, pkg := range lock.Packages {
				if pkg.Name == "magento/magento2-base" || pkg.Name == "mage-os/magento2-base" {
					return pkg.Version
				}
			}
			for _, pkg := range lock.Packages {
				if strings.HasPrefix(pkg.Name, "magento/product-") && strings.HasSuffix(pkg.Name, "-edition") {
					return pkg.Version
				}
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(p.Root, "src", "composer.json")); err == nil {
		var cj struct {
			Name    string            `json:"name"`
			Version string            `json:"version"`
			Require map[string]string `json:"require"`
		}
		if json.Unmarshal(data, &cj) == nil {
			if cj.Version != "" && strings.Contains(cj.Name, "magento") {
				return cj.Version
			}
			for _, k := range []string{"magento/product-community-edition", "magento/product-enterprise-edition", "mage-os/product-community-edition"} {
				if v := cj.Require[k]; v != "" {
					return v
				}
			}
		}
	}
	return ""
}

// ComposeServicesFromText is a no-engine fallback for listing services: it
// reads top-level `services:` keys from the given compose files.
func ComposeServicesFromText(texts ...string) []string {
	seen := map[string]bool{}
	var out []string
	keyRe := regexp.MustCompile(`^  ([A-Za-z0-9][A-Za-z0-9_.\-]*):\s*$`)
	for _, t := range texts {
		in := false
		for _, line := range strings.Split(t, "\n") {
			if strings.HasPrefix(line, "services:") {
				in = true
				continue
			}
			if in && len(line) > 0 && line[0] != ' ' && line[0] != '#' {
				in = false
			}
			if in {
				if m := keyRe.FindStringSubmatch(line); m != nil && !seen[m[1]] {
					seen[m[1]] = true
					out = append(out, m[1])
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// PickPHPService chooses the PHP-FPM service name from a service list.
func PickPHPService(services []string) string {
	for _, s := range services {
		if s == "phpfpm" {
			return s
		}
	}
	for _, s := range services {
		if strings.Contains(s, "php") && !strings.Contains(s, "xdebug") && !strings.Contains(s, "admin") {
			return s
		}
	}
	return "phpfpm"
}
