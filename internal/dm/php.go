package dm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// PHPRepo is docker-magento's PHP image.
const PHPRepo = "markoshust/magento-php"

// fallbackPHPTags are used when the project has no lib/versions.tsv
// (docker-magento 53.2.0's newest tag for each PHP version).
var fallbackPHPTags = []string{"8.2-fpm-10", "8.3-fpm-8", "8.4-fpm-3", "8.5-fpm-1"}

// VersionRow is one row of docker-magento's lib/versions.tsv.
type VersionRow struct {
	Edition, Version, PHP string
}

// ReadVersions parses lib/versions.tsv from the project (nil if missing).
func ReadVersions(root string) []VersionRow {
	b, err := os.ReadFile(filepath.Join(root, "lib", "versions.tsv"))
	if err != nil {
		return nil
	}
	return ParseVersions(string(b))
}

// ParseVersions reads versions.tsv content.
func ParseVersions(s string) []VersionRow {
	var out []VersionRow
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		f := strings.Split(strings.TrimRight(l, "\r"), "\t")
		if len(f) >= 3 && f[2] != "" {
			out = append(out, VersionRow{Edition: f[0], Version: f[1], PHP: f[2]})
		}
	}
	return out
}

// Recommended is the PHP tag versions.tsv lists for a Magento (or Mage-OS)
// version, using its boundary-aware prefix rule ("" if no row matches).
func Recommended(rows []VersionRow, edition, version string) string {
	for _, r := range rows {
		if r.Edition != edition {
			continue
		}
		if version == r.Version || strings.HasPrefix(version, r.Version+".") || strings.HasPrefix(version, r.Version+"-") {
			return r.PHP
		}
	}
	return ""
}

var phpTagRe = regexp.MustCompile(`^(\d+)\.(\d+)-fpm(?:-xdebug)?-(\d+)$`)

// PHPMinor is "8.4" for "8.4-fpm-3" ("" if the tag is not docker-magento's).
func PHPMinor(tag string) string {
	m := phpTagRe.FindStringSubmatch(tag)
	if m == nil {
		return ""
	}
	return m[1] + "." + m[2]
}

// XdebugTag turns "8.4-fpm-3" into "8.4-fpm-xdebug-3".
func XdebugTag(tag string) string {
	if strings.Contains(tag, "-xdebug-") {
		return tag
	}
	return strings.Replace(tag, "-fpm-", "-fpm-xdebug-", 1)
}

// PHPTags lists the newest image tag for each PHP version that versions.tsv
// mentions (or the built-in list), oldest PHP first.
func PHPTags(rows []VersionRow) []string {
	best := map[string]string{}
	consider := func(tag string) {
		m := phpTagRe.FindStringSubmatch(tag)
		if m == nil || strings.Contains(tag, "xdebug") {
			return
		}
		minor := m[1] + "." + m[2]
		if cur, ok := best[minor]; !ok || build(tag) > build(cur) {
			best[minor] = tag
		}
	}
	for _, t := range fallbackPHPTags {
		consider(t)
	}
	for _, r := range rows {
		consider(r.PHP)
	}
	var out []string
	for _, t := range best {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return minorLess(PHPMinor(out[i]), PHPMinor(out[j])) })
	return out
}

func build(tag string) int {
	m := phpTagRe.FindStringSubmatch(tag)
	if m == nil {
		return -1
	}
	n, _ := strconv.Atoi(m[3])
	return n
}

func minorLess(a, b string) bool {
	pa, pb := strings.SplitN(a, ".", 2), strings.SplitN(b, ".", 2)
	for i := 0; i < 2 && i < len(pa) && i < len(pb); i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y
		}
	}
	return a < b
}

// ServiceImages reads `docker compose config --format json` output: the
// image of every service after all compose files are merged.
func ServiceImages(configJSON string) (map[string]string, error) {
	var c struct {
		Services map[string]struct {
			Image string `json:"image"`
		} `json:"services"`
	}
	if err := json.Unmarshal([]byte(configJSON), &c); err != nil {
		return nil, fmt.Errorf("reading compose config: %w", err)
	}
	out := map[string]string{}
	for name, s := range c.Services {
		out[name] = s.Image
	}
	return out, nil
}

// SplitImage splits "repo:tag" (a registry port is not a tag).
func SplitImage(img string) (repo, tag string) {
	i := strings.LastIndex(img, ":")
	if i < 0 || strings.Contains(img[i:], "/") {
		return img, ""
	}
	return img[:i], img[i+1:]
}
