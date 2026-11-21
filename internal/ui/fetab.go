package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"lazymage/internal/config"
	"lazymage/internal/frontend"
	"lazymage/internal/plan"
)

// feTab is the Frontend tab: every store view with its URL, theme and
// locale (from the database), the themes grunt builds (local-themes.js),
// Hyvä Tailwind themes found in app/design, and the frontend tasks.
type feTab struct {
	root      string
	stores    []frontend.Store
	storesErr string
	loading   bool
	loaded    bool
	themes    []frontend.Entry
	themeFile string
	tailwind  []twTheme
	resolves  map[string]bool // storefront host → resolves on this machine
	cursor    int
}

// twTheme is a Hyvä theme with its own Tailwind build in app/design.
type twTheme struct {
	name    string // Vendor/theme
	dir     string // relative to the Magento root (the container's working dir)
	scripts map[string]bool
}

type feRowKind int

const (
	feStore feRowKind = iota
	feTheme
	feTailwind
	feTask
)

type feRow struct {
	kind   feRowKind
	i      int    // index into stores / themes / tailwind
	action string // feTask
	label  string
}

var feTasks = []struct{ action, label string }{
	{aFEGruntWatch, "Grunt watch"},
	{aMultistore, "Multistore setup… (store domains, nginx, hosts, SSL)"},
	{aFEThemes, "Generate local-themes.js from the database…"},
	{aFEEditThemes, "Edit local-themes.js"},
	{aFENpmInstall, "npm install (grunt toolchain)"},
	{aFEGruntFree, "Run a grunt command…"},
	{aFEStaticDeploy, "Deploy static content for a theme…"},
	{aFECleanStatic, "Clear generated static files and caches"},
	{aFESetupGrunt, "bin/setup-grunt (first-time grunt setup)"},
}

func (t *feTab) rows() []feRow {
	var r []feRow
	for i := range t.stores {
		r = append(r, feRow{kind: feStore, i: i})
	}
	for i := range t.themes {
		r = append(r, feRow{kind: feTheme, i: i})
	}
	for i := range t.tailwind {
		r = append(r, feRow{kind: feTailwind, i: i})
	}
	for _, k := range feTasks {
		r = append(r, feRow{kind: feTask, action: k.action, label: k.label})
	}
	return r
}

func (t *feTab) selected() (feRow, bool) {
	rs := t.rows()
	if t.cursor < 0 || t.cursor >= len(rs) {
		return feRow{}, false
	}
	return rs[t.cursor], true
}

// selectedStoreURL is the URL of the store under the cursor, if any; o and
// y act on it while the Frontend tab is showing.
func (a *App) selectedStoreURL() string {
	if a.tab != tabFrontend || a.showO || a.fe == nil {
		return ""
	}
	if r, ok := a.fe.selected(); ok && r.kind == feStore {
		return a.fe.stores[r.i].URL
	}
	return ""
}

type feStoresMsg struct {
	root   string
	stores []frontend.Store
	err    error
}

type feResolveMsg struct {
	root  string
	hosts map[string]bool
}

// feRefresh re-reads the theme files and, when stores is set or they were
// never loaded, the store views from the database.
func (a *App) feRefresh(stores bool) tea.Cmd {
	if a.proj == nil {
		return nil
	}
	if a.fe == nil || a.fe.root != a.proj.Root {
		a.fe = &feTab{root: a.proj.Root}
	}
	t := a.fe
	t.themes, t.themeFile = a.currentThemes()
	if (!stores && t.loaded) || t.loading {
		return nil
	}
	if len(a.services) > 0 && plan.DBServiceFrom(a.services) == "" {
		t.storesErr, t.loaded = "no database service in the compose files", true
		return nil
	}
	t.loading = true
	root := a.proj.Root
	return a.dbQuery(frontend.StoresQuery(a.proj.TablePrefix()), func(out string, err error) tea.Cmd {
		if err != nil {
			return func() tea.Msg { return feStoresMsg{root: root, err: err} }
		}
		ss := frontend.ParseStores(out)
		return func() tea.Msg { return feStoresMsg{root: root, stores: ss} }
	})
}

func (a *App) onFEStores(m feStoresMsg) tea.Cmd {
	t := a.fe
	if t == nil || t.root != m.root {
		return nil
	}
	t.loading, t.loaded, t.storesErr = false, true, ""
	if m.err != nil {
		t.storesErr = "could not read the stores: " + m.err.Error()
		return nil
	}
	t.stores = m.stores
	t.tailwind = findTailwind(t.root, m.stores)
	t.cursor = min(t.cursor, max(len(t.rows())-1, 0))
	return resolveHosts(t.root, m.stores)
}

// findTailwind lists the Hyvä themes used by stores that have a Tailwind
// package in app/design (themes from vendor/ are built by their vendor).
func findTailwind(root string, stores []frontend.Store) []twTheme {
	var out []twTheme
	seen := map[string]bool{}
	for _, s := range stores {
		if !s.ThemeHyva || s.Theme == "" || seen[s.Theme] {
			continue
		}
		seen[s.Theme] = true
		dir := "app/design/frontend/" + s.Theme + "/web/tailwind"
		b, err := os.ReadFile(filepath.Join(root, "src", filepath.FromSlash(dir), "package.json"))
		if err != nil {
			continue
		}
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		_ = json.Unmarshal(b, &pkg)
		tw := twTheme{name: s.Theme, dir: dir, scripts: map[string]bool{}}
		for k := range pkg.Scripts {
			tw.scripts[k] = true
		}
		out = append(out, tw)
	}
	return out
}

// buildScript is the Hyvä production build: "build" (1.2+) or "build-prod".
func (tw twTheme) buildScript() string {
	for _, s := range []string{"build", "build-prod"} {
		if tw.scripts[s] {
			return s
		}
	}
	return "build"
}

// resolveHosts checks that each store's host resolves here; a store domain
// missing from the hosts file is the usual reason a second store won't open.
func resolveHosts(root string, stores []frontend.Store) tea.Cmd {
	hosts := map[string]bool{}
	for _, s := range stores {
		if h := frontend.Host(s.URL); h != "" {
			hosts[h] = false
		}
	}
	if len(hosts) == 0 {
		return nil
	}
	return func() tea.Msg {
		for h := range hosts {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			addrs, err := net.DefaultResolver.LookupHost(ctx, h)
			cancel()
			hosts[h] = err == nil && len(addrs) > 0
		}
		return feResolveMsg{root: root, hosts: hosts}
	}
}

func (a *App) feUpdate(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok || a.fe == nil {
		return nil
	}
	t := a.fe
	rows := t.rows()
	switch k.String() {
	case "j", "down":
		t.cursor = min(t.cursor+1, max(len(rows)-1, 0))
		return nil
	case "k", "up":
		t.cursor = max(t.cursor-1, 0)
		return nil
	case "g", "home":
		t.cursor = 0
		return nil
	case "G", "end":
		t.cursor = max(len(rows)-1, 0)
		return nil
	case "R":
		return a.feRefresh(true)
	case "w":
		if r, ok := t.selected(); ok && r.kind == feTailwind {
			return a.feTailwindWatch(t.tailwind[r.i])
		}
		return a.feToggleWatch()
	}
	r, ok := t.selected()
	if !ok {
		return nil
	}
	b := a.builder()
	switch r.kind {
	case feStore:
		s := t.stores[r.i]
		if k.String() == "enter" {
			if s.URL == "" {
				return a.setStatus("store "+s.Code+" has no base URL in the database", true)
			}
			return openURL(a.eff().Browser, s.URL, "store "+s.Code)
		}
	case feTheme:
		e := t.themes[r.i]
		if r := a.feDisabled(); r != "" {
			return a.setStatus(r, true)
		}
		switch k.String() {
		case "enter":
			a.lastGruntTheme = e.Key
			return a.startRun(b.GruntBuild(e.Key), runOpts{kind: kindFrontend})
		case "l":
			a.lastGruntTheme = e.Key
			return a.startRun(b.Grunt("less:"+e.Key), runOpts{kind: kindFrontend})
		case "d":
			return a.startRun(b.Magento(staticDeployArgs(e.Name, e.Locale)...), runOpts{kind: config.KindMagento})
		}
	case feTailwind:
		tw := t.tailwind[r.i]
		if r := a.feDisabled(); r != "" {
			return a.setStatus(r, true)
		}
		switch k.String() {
		case "enter":
			return a.startRun(b.Node("npm", "--prefix", tw.dir, "run", tw.buildScript()), runOpts{kind: kindFrontend})
		case "i":
			return a.startRun(b.Node("npm", "--prefix", tw.dir, "install"), runOpts{kind: kindFrontend})
		}
	case feTask:
		if k.String() == "enter" {
			if r.action == aFEGruntWatch {
				return a.feToggleWatch()
			}
			return a.do(r.action)
		}
	}
	return nil
}

func staticDeployArgs(theme string, locales ...string) []string {
	return append([]string{"setup:static-content:deploy", "-f", "--area", "frontend", "--theme", theme}, locales...)
}

// watchTask is the running background task for argv, if any.
func (a *App) watchTask(argv []string) *bgTask {
	want := config.JoinArgs(argv)
	for _, t := range a.bgs {
		if t.out.exit == nil && config.JoinArgs(t.argv) == want {
			return t
		}
	}
	return nil
}

// feToggleWatch starts grunt watch in the background, or stops it.
func (a *App) feToggleWatch() tea.Cmd {
	argv := a.builder().Grunt("watch")
	if t := a.watchTask(argv); t != nil {
		t.run.Cancel()
		return a.setStatus("stopping grunt watch", false)
	}
	return a.do(aFEGruntWatch)
}

func (a *App) feTailwindWatch(tw twTheme) tea.Cmd {
	argv := a.builder().Node("npm", "--prefix", tw.dir, "run", "watch")
	if t := a.watchTask(argv); t != nil {
		t.run.Cancel()
		return a.setStatus("stopping the Tailwind watch for "+tw.name, false)
	}
	if !tw.scripts["watch"] {
		return a.setStatus(tw.name+"'s package.json has no watch script", true)
	}
	return a.startBackground(argv, runOpts{kind: kindFrontend}, "Tailwind watch ("+tw.name+")")
}

// --- view ------------------------------------------------------------------------

func (a *App) feView(w, h int) string {
	t := a.fe
	if t == nil {
		return sFaint.Render("loading…")
	}
	rows := t.rows()
	var lines []string
	cursorLine := 0
	mark := func(idx int, s string) string {
		if idx == t.cursor {
			cursorLine = len(lines)
			return sSelected.Render(truncate("▶ "+s, w-1))
		}
		return truncateStyled("  "+s, w-1)
	}
	idx := 0

	// Stores
	head := sBold.Render("Stores")
	switch {
	case t.loading:
		head += sFaint.Render(" — reading the database…")
	case t.storesErr != "":
		head += " " + sWarn.Render(t.storesErr)
	case t.loaded:
		head += sFaint.Render(fmt.Sprintf(" — %d store view(s) · enter / o opens · y copies the URL · R reloads", len(t.stores)))
	}
	lines = append(lines, head)
	codeW, nameW, themeW := 8, 12, 14
	for _, s := range t.stores {
		codeW, nameW = max(codeW, len(s.Code)), max(nameW, min(len(s.Name), 24))
		themeW = max(themeW, min(len(s.Theme)+2, 32))
	}
	for _, s := range t.stores {
		var tags []string
		if s.Default {
			tags = append(tags, "default")
		}
		if !s.Active {
			tags = append(tags, "disabled")
		}
		theme := s.Theme
		if theme == "" {
			theme = "?"
		}
		if s.ThemeHyva {
			theme += " ✦"
		}
		url := s.URL
		if url == "" {
			url = "no base URL"
		} else if ok, known := t.resolves[frontend.Host(url)]; known && !ok {
			tags = append(tags, "⚠ host not in your hosts file")
		}
		row := fmt.Sprintf("%-*s %-*s %-*s %-6s %s", codeW, s.Code, nameW, truncate(s.Name, nameW), themeW, truncate(theme, themeW), s.Locale, url)
		if len(tags) > 0 {
			row += "  " + strings.Join(tags, " · ")
		}
		lines = append(lines, mark(idx, row))
		idx++
	}
	if t.loaded && len(t.stores) == 0 && t.storesErr == "" {
		lines = append(lines, sFaint.Render("  no store views — is Magento installed?"))
	}

	// Grunt themes
	lines = append(lines, "")
	gw := sFaint.Render("grunt watch: stopped (w starts it)")
	if a.watchTask(a.builder().Grunt("watch")) != nil {
		gw = sOK.Render("● grunt watch running") + sFaint.Render(" (w stops it, B shows it)")
	}
	switch {
	case t.themeFile == "":
		lines = append(lines, sBold.Render("Grunt themes")+sFaint.Render(" — no local-themes.js yet: generate it below")+"  "+gw)
	default:
		lines = append(lines, sBold.Render("Grunt themes")+sFaint.Render(" — "+a.relPath(t.themeFile)+" · enter builds · l LESS only · d static deploy")+"  "+gw)
	}
	for _, e := range t.themes {
		row := fmt.Sprintf("%-18s %-28s %-9s %-6s %s", e.Key, e.Name, e.Area, e.Locale, sFaint.Render(strings.Join(e.Files, ", ")))
		if e.Key == a.lastGruntTheme {
			row += sFaint.Render("  · last built")
		}
		lines = append(lines, mark(idx, row))
		idx++
	}

	// Hyvä
	if len(t.tailwind) > 0 {
		lines = append(lines, "", sBold.Render("Hyvä Tailwind")+sFaint.Render(" — enter builds · i npm install · w watch"))
		for _, tw := range t.tailwind {
			row := fmt.Sprintf("%-28s %s", tw.name, sFaint.Render(tw.dir))
			if a.watchTask(a.builder().Node("npm", "--prefix", tw.dir, "run", "watch")) != nil {
				row += "  " + sOK.Render("● watching")
			}
			lines = append(lines, mark(idx, row))
			idx++
		}
	}

	// Tasks
	lines = append(lines, "", sBold.Render("Tasks"))
	for _, r := range rows[idx:] {
		label := r.label
		if reason := a.disabledReason(r.action); reason != "" && r.action != aFEGruntWatch {
			label += sFaint.Render("  — " + reason)
		}
		if r.action == aFEGruntWatch {
			if a.watchTask(a.builder().Grunt("watch")) != nil {
				label = "Stop grunt watch"
			} else {
				label = "Start grunt watch (background)"
			}
		}
		lines = append(lines, mark(idx, label))
		idx++
	}

	// Keep the cursor visible.
	start := 0
	if cursorLine >= h {
		start = cursorLine - h + 1
	}
	end := min(start+h, len(lines))
	return strings.Join(lines[start:end], "\n")
}

func (a *App) feHints() string {
	if a.fe != nil {
		if r, ok := a.fe.selected(); ok {
			switch r.kind {
			case feStore:
				return keyHint("enter/o", "open store", "y", "copy URL", "O", "admin", "w", "grunt watch", "R", "reload", "j/k", "move")
			case feTheme:
				return keyHint("enter", "clean+build", "l", "LESS only", "d", "static deploy", "w", "grunt watch", "j/k", "move")
			case feTailwind:
				return keyHint("enter", "build", "i", "npm install", "w", "watch", "j/k", "move")
			}
		}
	}
	return keyHint("enter", "run", "w", "grunt watch", "R", "reload stores", "j/k", "move", "x", "palette", "?", "help")
}
