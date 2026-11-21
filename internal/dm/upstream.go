// Package dm works with the files docker-magento puts in a project: its
// upstream releases (what bin/update installs), compose.override.yaml,
// lib/versions.tsv, Magento's nginx.conf.sample and the multistore map.
package dm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"lazymage/internal/update"
)

// Repo is docker-magento's GitHub repository.
const Repo = "markshust/docker-magento"

// Base URLs; tests point these at a local server.
var (
	APIBase     = "https://api.github.com"
	CodeloadURL = "https://codeload.github.com"
	Client      = &http.Client{Timeout: 3 * time.Minute}
)

var versionRe = regexp.MustCompile(`(?m)^##\s*Version\s+v?([0-9]+\.[0-9]+\.[0-9]+)`)

// LocalVersion reads the "## Version X.Y.Z" header docker-magento writes at
// the top of compose.yaml ("" if there is none).
func LocalVersion(root string) string {
	for _, f := range []string{"compose.yaml", "docker-compose.yml"} {
		if b, err := os.ReadFile(filepath.Join(root, f)); err == nil {
			if m := versionRe.FindSubmatch(b); m != nil {
				return string(m[1])
			}
		}
	}
	return ""
}

// Upstream is a docker-magento release's compose/ template, which is what
// bin/update copies over a project.
type Upstream struct {
	Tag       string
	Version   string
	Files     map[string]File // project-relative path → file
	Changelog string
}

// File is one template file.
type File struct {
	Data []byte
	Mode os.FileMode
}

func get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "lazymage")
	resp, err := Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return resp, nil
}

// LatestTag asks GitHub for docker-magento's newest release tag.
func LatestTag(ctx context.Context) (string, error) {
	resp, err := get(ctx, APIBase+"/repos/"+Repo+"/releases/latest")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var r struct {
		Tag string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil || r.Tag == "" {
		return "", errors.New("could not read docker-magento's latest release")
	}
	return r.Tag, nil
}

// Fetch downloads the release (or "master") and reads its compose/ folder.
func Fetch(ctx context.Context, tag string) (*Upstream, error) {
	ref := "refs/tags/" + tag
	if tag == "master" {
		ref = "refs/heads/master"
	}
	resp, err := get(ctx, CodeloadURL+"/"+Repo+"/tar.gz/"+ref)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	zr, err := gzip.NewReader(io.LimitReader(resp.Body, 200<<20))
	if err != nil {
		return nil, fmt.Errorf("reading the download: %w", err)
	}
	u := &Upstream{Tag: tag, Files: map[string]File{}}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading the download: %w", err)
		}
		// docker-magento-<ref>/compose/bin/start → bin/start
		_, rel, ok := strings.Cut(h.Name, "/")
		if !ok || h.Typeflag != tar.TypeReg {
			continue
		}
		if rel == "CHANGELOG.md" {
			b, _ := io.ReadAll(io.LimitReader(tr, 4<<20))
			u.Changelog = string(b)
			continue
		}
		rel, ok = strings.CutPrefix(rel, "compose/")
		if !ok || rel == "" || !safeRel(rel) {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, 20<<20))
		if err != nil {
			return nil, err
		}
		u.Files[rel] = File{Data: b, Mode: os.FileMode(h.Mode) & 0o777}
	}
	if len(u.Files) == 0 {
		return nil, errors.New("the download has no compose/ folder")
	}
	if c, ok := u.Files["compose.yaml"]; ok {
		if m := versionRe.FindSubmatch(c.Data); m != nil {
			u.Version = string(m[1])
		}
	}
	if u.Version == "" {
		u.Version = strings.TrimPrefix(tag, "v")
	}
	return u, nil
}

func safeRel(p string) bool {
	c := path.Clean(p)
	return c == p && !strings.HasPrefix(c, "../") && c != ".." && !path.IsAbs(c)
}

// ChangeKind says what applying the template does to one file.
type ChangeKind int

const (
	Same    ChangeKind = iota
	New                // not in the project yet
	Changed            // replaced (the old copy is backed up)
	Merged             // env file: your values are kept, new keys are added
	Kept               // yours, never overwritten (e.g. .vscode settings you have)
)

// Change is one file in an update plan.
type Change struct {
	Path    string
	Kind    ChangeKind
	AddKeys []string // Merged: keys added from upstream
}

// keepIfPresent are template files that belong to the developer once they
// exist; bin/update overwrites them, lazymage does not.
func keepIfPresent(p string) bool {
	return strings.HasPrefix(p, ".vscode/")
}

// Plan compares the template with the project.
func (u *Upstream) Plan(root string) []Change {
	var out []Change
	for p, f := range u.Files {
		cur, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		switch {
		case err != nil:
			out = append(out, Change{Path: p, Kind: New})
		case bytes.Equal(cur, f.Data):
			out = append(out, Change{Path: p, Kind: Same})
		case isEnvFile(p):
			merged, added := MergeEnv(string(cur), string(f.Data))
			if len(added) == 0 || merged == string(cur) {
				out = append(out, Change{Path: p, Kind: Same})
			} else {
				out = append(out, Change{Path: p, Kind: Merged, AddKeys: added})
			}
		case keepIfPresent(p):
			out = append(out, Change{Path: p, Kind: Kept})
		default:
			out = append(out, Change{Path: p, Kind: Changed})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func isEnvFile(p string) bool { return strings.HasPrefix(p, "env/") && strings.HasSuffix(p, ".env") }

// Apply writes the plan: every file it replaces or merges is first copied to
// backupDir (same relative path). Files are written atomically.
func (u *Upstream) Apply(root, backupDir string, plan []Change) error {
	for _, c := range plan {
		if c.Kind == Same || c.Kind == Kept {
			continue
		}
		dst := filepath.Join(root, filepath.FromSlash(c.Path))
		f := u.Files[c.Path]
		data := f.Data
		mode := f.Mode
		if mode == 0 {
			mode = 0o644
		}
		if c.Kind != New {
			old, err := os.ReadFile(dst)
			if err != nil {
				return err
			}
			if st, err := os.Stat(dst); err == nil {
				mode = st.Mode().Perm() | (mode & 0o111)
			}
			bak := filepath.Join(backupDir, filepath.FromSlash(c.Path))
			if err := os.MkdirAll(filepath.Dir(bak), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(bak, old, 0o600); err != nil {
				return err
			}
			if c.Kind == Merged {
				m, _ := MergeEnv(string(old), string(f.Data))
				data = []byte(m)
			}
		}
		if err := WriteFileAtomic(dst, data, mode); err != nil {
			return fmt.Errorf("%s: %w", c.Path, err)
		}
	}
	return nil
}

// WriteFileAtomic writes via a temporary file and rename.
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".lazymage-tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

var envKeyRe = regexp.MustCompile(`^\s*#?\s*([A-Za-z_][A-Za-z0-9_]*)=`)

// MergeEnv keeps every line of local and appends the upstream assignments
// whose key local does not mention at all (set or commented out).
func MergeEnv(local, upstream string) (string, []string) {
	have := map[string]bool{}
	for _, l := range strings.Split(local, "\n") {
		if m := envKeyRe.FindStringSubmatch(l); m != nil {
			have[m[1]] = true
		}
	}
	var add []string
	var keys []string
	for _, l := range strings.Split(upstream, "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if m := envKeyRe.FindStringSubmatch(l); m != nil && !have[m[1]] {
			have[m[1]] = true
			add = append(add, l)
			keys = append(keys, m[1])
		}
	}
	if len(add) == 0 {
		return local, nil
	}
	out := local
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	out += "# Added by lazymage from docker-magento's template\n" + strings.Join(add, "\n") + "\n"
	return out, keys
}

var changelogHeadRe = regexp.MustCompile(`(?m)^## \[([0-9]+\.[0-9]+\.[0-9]+)\][^\n]*$`)

// ChangesSince returns the changelog sections newer than version (all of
// them, up to max lines, when version is "").
func (u *Upstream) ChangesSince(version string, maxLines int) string {
	idx := changelogHeadRe.FindAllStringSubmatchIndex(u.Changelog, -1)
	var b strings.Builder
	for i, m := range idx {
		v := u.Changelog[m[2]:m[3]]
		if version != "" && !update.Newer(v, version) {
			break
		}
		end := len(u.Changelog)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		b.WriteString(strings.TrimSpace(u.Changelog[m[0]:end]) + "\n\n")
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if maxLines > 0 && len(lines) > maxLines {
		lines = append(lines[:maxLines], fmt.Sprintf("… %d more lines in CHANGELOG.md", len(lines)-maxLines))
	}
	return strings.Join(lines, "\n")
}
