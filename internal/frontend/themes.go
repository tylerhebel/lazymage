// Package frontend builds Magento's grunt theme config
// (dev/tools/grunt/configs/local-themes.js) from the themes and store
// settings in the database, and reads an existing one back.
package frontend

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// LocalThemesPath is the grunt config, relative to the project root. In
// docker-magento the configs directory is bind-mounted (compose.dev.yaml),
// so the host file is the one grunt reads in the container.
const LocalThemesPath = "src/dev/tools/grunt/configs/local-themes.js"

// Entry is one grunt theme: the key is what grunt tasks take (exec:<key>).
type Entry struct {
	Key    string
	Area   string
	Name   string // Vendor/theme
	Locale string
	Files  []string
	DSL    string
}

// Theme is a physical theme from the database, with where it is used.
type Theme struct {
	ID     string
	Path   string // Vendor/theme
	Area   string
	Parent string // parent theme_id
	Hyva   bool   // Hyvä themes build with Tailwind, not grunt
	// UsedBy maps locale → the scopes (e.g. "store default") that show this
	// theme in that locale. Empty when no store uses the theme.
	UsedBy map[string][]string
}

// Query returns every row the generator needs in one round trip:
// physical themes, theme and locale settings per scope, and stores.
func Query(prefix string) string {
	t := func(n string) string { return "`" + strings.ReplaceAll(prefix+n, "`", "``") + "`" }
	return "SELECT 'theme', theme_id, theme_path, CONCAT(area, ':', IFNULL(parent_id, '')) FROM " + t("theme") + " WHERE type = 0" +
		" UNION ALL SELECT 'cfg', CONCAT(scope, ':', scope_id), path, value FROM " + t("core_config_data") +
		" WHERE path IN ('design/theme/theme_id', 'general/locale/code')" +
		" UNION ALL SELECT 'store', store_id, website_id, code FROM " + t("store") + " WHERE store_id > 0"
}

// Data is the parsed query result.
type Data struct {
	Themes        []Theme
	DefaultLocale string
}

type store struct{ id, website, code string }

// Parse reads Query's tab-separated output and works out, for every store,
// which theme it shows and in which locale (store → website → default).
func Parse(out string) Data {
	var (
		themes  []Theme
		byID    = map[string]*Theme{}
		cfg     = map[string]map[string]string{} // path → "scope:id" → value
		stores  []store
		defLoc  = "en_US"
		rowsRaw [][]string
	)
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.Split(strings.TrimRight(l, "\r"), "\t")
		if len(f) == 4 {
			rowsRaw = append(rowsRaw, f)
		}
	}
	for _, f := range rowsRaw {
		switch f[0] {
		case "theme":
			area, parent, _ := strings.Cut(f[3], ":")
			if parent == "NULL" {
				parent = ""
			}
			themes = append(themes, Theme{ID: f[1], Path: f[2], Area: area, Parent: parent, UsedBy: map[string][]string{}})
		case "cfg":
			if cfg[f[2]] == nil {
				cfg[f[2]] = map[string]string{}
			}
			cfg[f[2]][f[1]] = f[3]
		case "store":
			stores = append(stores, store{id: f[1], website: f[2], code: f[3]})
		}
	}
	for i := range themes {
		byID[themes[i].ID] = &themes[i]
	}
	// Hyvä: the theme or any ancestor is under Hyva/.
	for i := range themes {
		for t, n := &themes[i], 0; t != nil && n < 20; t, n = byID[t.Parent], n+1 {
			if strings.HasPrefix(t.Path, "Hyva/") {
				themes[i].Hyva = true
				break
			}
		}
	}
	if v := cfg["general/locale/code"]["default:0"]; v != "" {
		defLoc = v
	}
	resolve := func(path string, s store) string {
		c := cfg[path]
		for _, k := range []string{"stores:" + s.id, "websites:" + s.website, "default:0"} {
			if v := c[k]; v != "" {
				return v
			}
		}
		return ""
	}
	for _, s := range stores {
		t := byID[resolve("design/theme/theme_id", s)]
		if t == nil {
			continue
		}
		loc := resolve("general/locale/code", s)
		if loc == "" {
			loc = defLoc
		}
		t.UsedBy[loc] = append(t.UsedBy[loc], "store "+s.code)
	}
	sort.SliceStable(themes, func(i, j int) bool {
		if themes[i].Area != themes[j].Area {
			return themes[i].Area == "frontend"
		}
		return themes[i].Path < themes[j].Path
	})
	return Data{Themes: themes, DefaultLocale: defLoc}
}

// Candidate is one possible entry in the generated file.
type Candidate struct {
	Entry
	Theme    Theme
	InUse    []string // scopes that show it in this locale
	InFile   bool     // already in the current local-themes.js
	Selected bool
}

// stockFiles are Magento's own lists from themes.js.
var stockFiles = map[string][]string{
	"Magento/blank":   {"css/styles-m", "css/styles-l", "css/email", "css/email-inline"},
	"Magento/luma":    {"css/styles-m", "css/styles-l", "css/email", "css/email-inline"},
	"Magento/backend": {"css/styles-old", "css/styles"},
}

// Candidates lists one entry per theme and locale in use (or per theme in
// the default locale when unused). Entries already in the current file keep
// their key and files; selected by default are those in use (except Hyvä)
// and those already in the file. Entries in the file whose theme is not in
// the database are kept as selected candidates too, so nothing is dropped
// silently.
func Candidates(d Data, current []Entry) []Candidate {
	type nk struct{ name, locale string }
	cur := map[nk]Entry{}
	for _, e := range current {
		cur[nk{e.Name, e.Locale}] = e
	}
	used := map[string]bool{}
	var out []Candidate
	for _, t := range d.Themes {
		locales := make([]string, 0, len(t.UsedBy))
		for l := range t.UsedBy {
			locales = append(locales, l)
		}
		sort.Strings(locales)
		if len(locales) == 0 {
			locales = []string{d.DefaultLocale}
		}
		for _, loc := range locales {
			c := Candidate{Theme: t, InUse: t.UsedBy[loc]}
			c.Entry = Entry{Area: t.Area, Name: t.Path, Locale: loc, DSL: "less", Files: defaultFiles(t)}
			if e, ok := cur[nk{t.Path, loc}]; ok {
				c.Key, c.Files, c.InFile = e.Key, e.Files, true
				if e.DSL != "" {
					c.DSL = e.DSL
				}
			}
			c.Selected = c.InFile || (len(c.InUse) > 0 && !t.Hyva)
			out = append(out, c)
		}
	}
	for _, c := range out {
		if c.InFile {
			used[c.Key] = true
		}
	}
	// Keys for new entries: the theme code (luma), then code_locale, then
	// vendor_code_locale, so grunt task names stay short but unique.
	for i := range out {
		if out[i].Key != "" {
			continue
		}
		vendor, code, _ := strings.Cut(out[i].Name, "/")
		code, vendor = keyPart(code), keyPart(vendor)
		loc := strings.ToLower(out[i].Locale)
		for _, k := range []string{code, code + "_" + loc, vendor + "_" + code + "_" + loc} {
			if !used[k] {
				out[i].Key = k
				break
			}
		}
		if out[i].Key == "" {
			out[i].Key = fmt.Sprintf("%s_%s_%d", code, loc, i)
		}
		used[out[i].Key] = true
	}
	// Entries the database doesn't know about (a theme that was removed, or
	// hand-written ones): keep them, selected.
	inDB := map[nk]bool{}
	for _, c := range out {
		inDB[nk{c.Name, c.Locale}] = true
	}
	for _, e := range current {
		if !inDB[nk{e.Name, e.Locale}] {
			out = append(out, Candidate{Entry: e, InFile: true, Selected: true})
		}
	}
	return out
}

func defaultFiles(t Theme) []string {
	if f, ok := stockFiles[t.Path]; ok {
		return append([]string(nil), f...)
	}
	if t.Area == "adminhtml" {
		return []string{"css/styles-old", "css/styles"}
	}
	return []string{"css/styles-m", "css/styles-l"}
}

var nonKey = regexp.MustCompile(`[^a-z0-9_]+`)

func keyPart(s string) string {
	return strings.Trim(nonKey.ReplaceAllString(strings.ToLower(s), "_"), "_")
}

var identRe = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

func jsString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`).Replace(s) + "'"
}

// Render writes local-themes.js for the given entries.
func Render(es []Entry, now time.Time) string {
	var b strings.Builder
	b.WriteString("'use strict';\n\n")
	fmt.Fprintf(&b, "// Generated by lazymage on %s from the themes and locales in the Magento\n", now.Format("2006-01-02"))
	b.WriteString("// database. Grunt reads this file instead of themes.js when it exists.\n")
	b.WriteString("// Run a theme with: grunt exec:<key> && grunt less:<key>\n")
	b.WriteString("module.exports = {\n")
	for i, e := range es {
		key := e.Key
		if !identRe.MatchString(key) {
			key = jsString(key)
		}
		fmt.Fprintf(&b, "    %s: {\n", key)
		fmt.Fprintf(&b, "        area: %s,\n", jsString(e.Area))
		fmt.Fprintf(&b, "        name: %s,\n", jsString(e.Name))
		fmt.Fprintf(&b, "        locale: %s,\n", jsString(e.Locale))
		b.WriteString("        files: [\n")
		for j, f := range e.Files {
			sep := ","
			if j == len(e.Files)-1 {
				sep = ""
			}
			fmt.Fprintf(&b, "            %s%s\n", jsString(f), sep)
		}
		b.WriteString("        ],\n")
		dsl := e.DSL
		if dsl == "" {
			dsl = "less"
		}
		fmt.Fprintf(&b, "        dsl: %s\n", jsString(dsl))
		sep := ","
		if i == len(es)-1 {
			sep = ""
		}
		fmt.Fprintf(&b, "    }%s\n", sep)
	}
	b.WriteString("};\n")
	return b.String()
}

var (
	entryRe  = regexp.MustCompile(`(?s)(?:^|[{,\s])([A-Za-z_$][\w$]*|'[^']*'|"[^"]*")\s*:\s*\{([^{}]*)\}`)
	fieldRe  = regexp.MustCompile(`\b(area|name|locale|dsl)\s*:\s*(?:'([^']*)'|"([^"]*)")`)
	filesRe  = regexp.MustCompile(`(?s)\bfiles\s*:\s*\[([^\]]*)\]`)
	quotedRe = regexp.MustCompile(`'([^']*)'|"([^"]*)"`)
)

// ParseLocalThemes reads the theme entries from a local-themes.js or
// themes.js. It understands Magento's own format, util.inspect output (what
// bin/setup-grunt writes), and this package's output; anything it cannot
// read is skipped.
func ParseLocalThemes(js string) []Entry {
	js = regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(js, "")
	var out []Entry
	for _, m := range entryRe.FindAllStringSubmatch(js, -1) {
		e := Entry{Key: strings.Trim(m[1], `'"`)}
		for _, f := range fieldRe.FindAllStringSubmatch(m[2], -1) {
			v := f[2] + f[3]
			switch f[1] {
			case "area":
				e.Area = v
			case "name":
				e.Name = v
			case "locale":
				e.Locale = v
			case "dsl":
				e.DSL = v
			}
		}
		if fm := filesRe.FindStringSubmatch(m[2]); fm != nil {
			for _, q := range quotedRe.FindAllStringSubmatch(fm[1], -1) {
				e.Files = append(e.Files, q[1]+q[2])
			}
		}
		if e.Name != "" && e.Area != "" {
			out = append(out, e)
		}
	}
	return out
}

// Keys lists entry keys, for grunt task prompts.
func Keys(es []Entry) []string {
	var ks []string
	for _, e := range es {
		ks = append(ks, e.Key)
	}
	return ks
}

// Describe is a one-line summary of a candidate for lists.
func (c Candidate) Describe() string {
	var tags []string
	if len(c.InUse) > 0 {
		tags = append(tags, "used by "+strings.Join(c.InUse, ", "))
	}
	if c.InFile {
		tags = append(tags, "in current file")
	}
	if c.Theme.Hyva {
		tags = append(tags, "Hyvä — builds with Tailwind, not grunt")
	}
	if c.Theme.ID == "" && c.InFile {
		tags = append(tags, "not in the database")
	}
	s := fmt.Sprintf("%-22s %-28s %-9s %s", c.Key, c.Name, c.Area, c.Locale)
	if len(tags) > 0 {
		s += "  · " + strings.Join(tags, " · ")
	}
	return s
}
