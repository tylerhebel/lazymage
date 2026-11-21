package ui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"lazymage/internal/config"
	"lazymage/internal/dbfile"
	"lazymage/internal/plan"
	"lazymage/internal/project"
	"lazymage/internal/runner"
)

// Database tools: dump, import, client shell, base-URL replace. Every
// command runs in the database container and reads the credentials there
// (see plan/db.go), so no password passes through lazymage.

const kindDB = "db"

// dbQueryMsg carries the result of a background query to its continuation.
type dbQueryMsg struct {
	out  string
	err  error
	next func(out string, err error) tea.Cmd
}

// dbQuery runs a short query off the UI loop and hands the rows to next.
func (a *App) dbQuery(sql string, next func(string, error) tea.Cmd) tea.Cmd {
	argv := a.builder().DBQuery(sql)
	root := a.root()
	a.log.printf("db query: %s", config.JoinArgs(argv))
	return func() tea.Msg {
		out, errOut, code, err := runner.Capture(root, argv, 30*time.Second)
		if err == nil && code != 0 {
			err = fmt.Errorf("exit %d: %s", code, orNoOutput(firstLine(errOut)))
		}
		return dbQueryMsg{out: out, err: err, next: next}
	}
}

func orNoOutput(s string) string {
	if s == "" {
		return "no output"
	}
	return s
}

func (a *App) dbDisabled() string {
	if a.run != nil {
		return "a command is already running (ctrl+c cancels it)"
	}
	if len(a.services) > 0 && plan.DBServiceFrom(a.services) == "" {
		return "no database service (db, mysql or mariadb) in the compose files"
	}
	return ""
}

func (a *App) absPath(p string) string {
	if filepath.IsAbs(p) || a.proj == nil {
		return p
	}
	return filepath.Join(a.proj.Root, p)
}

// relPath shows p relative to the project root when it is inside it.
func (a *App) relPath(p string) string {
	if a.proj != nil {
		if r, err := filepath.Rel(a.proj.Root, p); err == nil && !strings.HasPrefix(r, "..") {
			return r
		}
	}
	return p
}

// --- dump ------------------------------------------------------------------------------

// dbDumpPrompt asks where to write the dump.
func (a *App) dbDumpPrompt() tea.Cmd {
	def := dbfile.DefaultName(a.proj.Name, time.Now())
	a.push(newPrompt("Dump the database", argPrompt{Label: "File (relative to the project; .sql.gz is compressed, .sql is plain)", Default: def, Required: true},
		func(v string) tea.Cmd {
			if !dbfile.IsDump(v) {
				v += ".sql.gz"
			}
			path := a.absPath(v)
			if _, err := os.Stat(path); err == nil {
				a.push(newConfirm("Overwrite?", a.relPath(path)+" already exists.", "Overwrite",
					func() tea.Cmd { return a.runDump(path, nil) }))
				return nil
			}
			return a.runDump(path, nil)
		}))
	return nil
}

// runDump streams the dump into path. then (optional) runs after a successful dump.
func (a *App) runDump(path string, then func() tea.Cmd) tea.Cmd {
	w, err := dbfile.Create(path)
	if err != nil {
		return a.setStatus("cannot write dump: "+err.Error(), true)
	}
	rel := a.relPath(path)
	return a.startRun(a.builder().DBDump(), runOpts{
		kind: kindDB, noStdin: true,
		rio:   runner.IO{Stdout: w},
		meter: func() string { return "→ " + rel + " · " + dbfile.HumanBytes(w.Written()) + " written" },
		onExit: func(e runner.Exited) tea.Cmd {
			if e.Code != 0 || e.Cancelled || e.Err != nil {
				w.Abort()
				a.out.buf.Note("dump not saved (partial file removed)")
				if then != nil {
					a.out.buf.Note("import skipped because the backup failed")
				}
				return nil
			}
			if err := w.Commit(); err != nil {
				a.out.buf.Note("dump failed: " + err.Error())
				a.out.exit.Code = 1
				return a.setStatus("dump failed: "+err.Error(), true)
			}
			st, _ := os.Stat(path)
			size := int64(0)
			if st != nil {
				size = st.Size()
			}
			a.out.buf.Note(fmt.Sprintf("saved %s (%s on disk)", rel, dbfile.HumanBytes(size)))
			if then != nil {
				cmd := then()
				if a.out != nil { // the next run's view; keep the backup's location visible
					a.out.buf.Note(fmt.Sprintf("backup saved first: %s (%s)", rel, dbfile.HumanBytes(size)))
				}
				return cmd
			}
			return nil
		},
	})
}

// --- import ----------------------------------------------------------------------------

type dumpPicker struct {
	in      textinput.Model
	entries []dbfile.Entry
	cursor  int
}

func newDumpPicker(a *App) *dumpPicker {
	in := newInput("▸ ", "filter, or type a path to a .sql / .sql.gz file")
	in.Focus()
	return &dumpPicker{in: in, entries: dbfile.List(a.proj.Root, dbfile.DefaultDir)}
}

func (d *dumpPicker) options() []dbfile.Entry {
	q := strings.TrimSpace(d.in.Value())
	if q == "" {
		return d.entries
	}
	var out []dbfile.Entry
	for _, e := range d.entries {
		if fuzzyScore(q, e.Path) > 0 {
			out = append(out, e)
		}
	}
	return out
}

func (d *dumpPicker) update(a *App, msg tea.Msg) tea.Cmd {
	opts := d.options()
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc", "ctrl+c":
			a.remove(d)
			return nil
		case "up", "ctrl+k":
			d.cursor = max(d.cursor-1, 0)
			return nil
		case "down", "ctrl+j":
			d.cursor = min(d.cursor+1, max(len(opts)-1, 0))
			return nil
		case "enter":
			path := ""
			if d.cursor < len(opts) {
				path = opts[d.cursor].Path
			} else if v := strings.TrimSpace(d.in.Value()); v != "" {
				path = v
			}
			if path == "" {
				return nil
			}
			if strings.HasPrefix(path, "~/") {
				if home, err := os.UserHomeDir(); err == nil {
					path = filepath.Join(home, path[2:])
				}
			}
			a.remove(d)
			return a.dbImportConfirm(a.absPath(path))
		}
	}
	var cmd tea.Cmd
	d.in, cmd = d.in.Update(msg)
	d.cursor = 0
	return cmd
}

func (d *dumpPicker) view(a *App, w, h int) string {
	bw := min(w-4, 90)
	d.in.SetWidth(bw - 8)
	opts := d.options()
	lines := []string{d.in.View(), ""}
	if len(opts) == 0 {
		if len(d.entries) == 0 {
			lines = append(lines, sFaint.Render("no dumps in "+dbfile.DefaultDir+"/ or the project root — type a path, then enter"))
		} else {
			lines = append(lines, sFaint.Render("no match — enter imports the typed path"))
		}
	}
	maxRows := max(h-8, 3)
	start := 0
	if d.cursor >= maxRows {
		start = d.cursor - maxRows + 1
	}
	for i := start; i < len(opts) && i-start < maxRows; i++ {
		e := opts[i]
		meta := fmt.Sprintf("%9s  %s", dbfile.HumanBytes(e.Size), e.ModTime.Format("2006-01-02 15:04"))
		name := truncate(e.Path, bw-lenPlain(meta)-8)
		row := padRight(name, bw-lenPlain(meta)-6) + "  " + meta
		if i == d.cursor {
			lines = append(lines, sSelected.Render("▶ "+row))
		} else {
			lines = append(lines, "  "+row)
		}
	}
	return overlayBox("Import a database dump", strings.Join(lines, "\n"), bw)
}

func (d *dumpPicker) hints() string {
	return keyHint("type", "filter / path", "↑/↓", "move", "enter", "import…", "esc", "close")
}

// dbImportConfirm checks the file, then asks: back up first, import, or cancel.
func (a *App) dbImportConfirm(path string) tea.Cmd {
	st, err := os.Stat(path)
	if err != nil {
		return a.setStatus("cannot import: "+err.Error(), true)
	}
	if st.IsDir() {
		return a.setStatus("cannot import: "+a.relPath(path)+" is a directory", true)
	}
	argv := a.builder().DBImport()
	body := fmt.Sprintf("Loads %s (%s) into the project database.\nTables in the dump replace the current ones; this cannot be undone.\n\n$ %s < %s",
		a.relPath(path), dbfile.HumanBytes(st.Size()), config.JoinArgs(argv), a.relPath(path))
	backup := a.absPath(dbfile.DefaultName(a.proj.Name+"-before-import", time.Now()))
	a.push(newChoice("Import database?", body, []choice{
		{label: "Back up, then import", key: "b", run: func() tea.Cmd {
			return a.runDump(backup, func() tea.Cmd { return a.runImport(path) })
		}},
		{label: "Import", key: "i", run: func() tea.Cmd { return a.runImport(path) }},
		{label: "Cancel", key: "n"},
	}))
	return nil
}

func (a *App) runImport(path string) tea.Cmd {
	r, err := dbfile.Open(path)
	if err != nil {
		return a.setStatus("cannot import: "+err.Error(), true)
	}
	argv := a.builder().DBImport()
	rel := a.relPath(path)
	// A chained import starts inside the dump's exit handling, while a.run
	// still points at the dump; clear it so this run can start.
	a.run = nil
	cmd := a.startRun(argv, runOpts{
		kind: kindDB, noStdin: true, refresh: true, display: config.JoinArgs(argv) + " < " + rel,
		rio:   runner.IO{Stdin: r},
		meter: func() string { return "← " + rel + " · " + progressBar(r.Progress(), 20) },
		onExit: func(e runner.Exited) tea.Cmd {
			r.Close()
			switch {
			case e.Cancelled:
				a.out.buf.Note("import cancelled part-way: the database may be incomplete")
			case e.Code == 0 && e.Err == nil:
				a.out.buf.Note("import finished. If the dump came from another environment, run “Replace base URLs…” next, then flush the cache.")
			}
			return nil
		},
	})
	return cmd
}

func progressBar(f float64, w int) string {
	n := int(f * float64(w))
	return "[" + strings.Repeat("█", n) + strings.Repeat("░", w-n) + fmt.Sprintf("] %3.0f%%", f*100)
}

// --- client shell ------------------------------------------------------------------------

func (a *App) dbShell() tea.Cmd {
	argv := a.builder().DBShell()
	cmd := exec.Command(argv[0], argv[1:]...)
	if strings.HasPrefix(argv[0], "bin/") {
		cmd.Path = filepath.Join(a.proj.Root, argv[0])
		cmd.Err = nil
	}
	cmd.Dir = a.proj.Root
	a.log.printf("db shell: %s", config.JoinArgs(argv))
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if _, ok := err.(*exec.ExitError); ok {
			err = nil
		}
		return shellDoneMsg{err: err, what: "the MySQL shell"}
	})
}

// --- base URL replace ------------------------------------------------------------------

type configRow struct{ scope, scopeID, path, value string }

func parseRows(out string) []configRow {
	var rows []configRow
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.SplitN(l, "\t", 4)
		if len(f) == 4 {
			rows = append(rows, configRow{f[0], f[1], f[2], f[3]})
		}
	}
	return rows
}

func (r configRow) String() string {
	scope := r.scope
	if r.scope != "default" {
		scope += "/" + r.scopeID
	}
	return fmt.Sprintf("%-28s %-12s %s", r.path, scope, r.value)
}

// dbReplaceURLs reads the current base URLs, then asks what to replace.
func (a *App) dbReplaceURLs() tea.Cmd {
	prefix := a.proj.TablePrefix()
	status := a.setStatus("reading base URLs from the database…", false)
	return tea.Batch(status, a.dbQuery(plan.BaseURLRows(prefix), func(out string, err error) tea.Cmd {
		if err != nil {
			return a.setStatus("could not read base URLs: "+err.Error(), true)
		}
		rows := parseRows(out)
		from := ""
		for _, r := range rows {
			if r.path == "web/secure/base_url" && r.scope == "default" {
				from = project.HostOf(r.value)
			}
		}
		if from == "" && len(rows) > 0 {
			from = project.HostOf(rows[0].value)
		}
		to := ""
		if a.facts.Domain != "" && a.facts.Domain != from {
			to = a.facts.Domain
		}
		a.push(newPrompt("Replace base URLs (1/2)", argPrompt{Label: "Find (usually the other environment's domain)", Default: from, Required: true},
			func(f string) tea.Cmd {
				a.push(newPrompt("Replace base URLs (2/2)", argPrompt{Label: "Replace “" + f + "” with (your local domain)", Default: to, Required: true},
					func(t string) tea.Cmd { return a.dbReplacePreview(prefix, f, t) }))
				return nil
			}))
		return nil
	}))
}

func (a *App) dbReplacePreview(prefix, from, to string) tea.Cmd {
	if from == to {
		return a.setStatus("nothing to replace: find and replace are the same", true)
	}
	return a.dbQuery(plan.URLReplacePreview(prefix, from), func(out string, err error) tea.Cmd {
		if err != nil {
			return a.setStatus("preview failed: "+err.Error(), true)
		}
		rows := parseRows(out)
		if len(rows) == 0 {
			return a.setStatus("no config values contain “"+from+"”", false)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d value(s) in core_config_data contain “%s” and become “%s”:\n\n", len(rows), from, to)
		const show = 12
		for i, r := range rows {
			if i == show {
				fmt.Fprintf(&b, "… and %d more\n", len(rows)-show)
				break
			}
			b.WriteString(r.String() + "\n")
		}
		argv := a.builder().DBQuery(plan.URLReplace(prefix, from, to))
		b.WriteString("\nThen bin/magento cache:flush.\n\n$ " + config.JoinArgs(argv))
		a.push(newConfirm("Replace base URLs?", b.String(), "Replace", func() tea.Cmd {
			return a.startRun(argv, runOpts{kind: kindDB, refresh: true, noStdin: true,
				onExit: func(e runner.Exited) tea.Cmd {
					if e.Code != 0 || e.Cancelled || e.Err != nil {
						return nil
					}
					flush := a.builder().Magento("cache:flush")
					a.run = nil
					return a.startRun(flush, runOpts{kind: config.KindMagento, refresh: true})
				}})
		}))
		return nil
	})
}

// --- multi-button choice -------------------------------------------------------------------

type choice struct {
	label, key string
	run        func() tea.Cmd // nil = cancel
}

type choiceOverlay struct {
	title, body string
	choices     []choice
	sel         int
}

// newChoice defaults to the last choice, which should be the safe one.
func newChoice(title, body string, cs []choice) *choiceOverlay {
	return &choiceOverlay{title: title, body: body, choices: cs, sel: len(cs) - 1}
}

func (c *choiceOverlay) pick(a *App, i int) tea.Cmd {
	a.remove(c)
	if c.choices[i].run == nil {
		return nil
	}
	return c.choices[i].run()
}

func (c *choiceOverlay) update(a *App, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	n := len(c.choices)
	switch s := k.String(); s {
	case "left", "h", "shift+tab":
		c.sel = (c.sel + n - 1) % n
	case "right", "l", "tab":
		c.sel = (c.sel + 1) % n
	case "esc", "q", "ctrl+c":
		a.remove(c)
	case "enter":
		return c.pick(a, c.sel)
	default:
		for i, ch := range c.choices {
			if ch.key == s {
				return c.pick(a, i)
			}
		}
	}
	return nil
}

func (c *choiceOverlay) view(a *App, w, h int) string {
	bw := min(w-4, 100)
	var lines []string
	for _, l := range strings.Split(c.body, "\n") {
		lines = append(lines, wrapStyledPlain(l, bw-4)...)
	}
	var btns []string
	for i, ch := range c.choices {
		label := " " + ch.label + " (" + ch.key + ") "
		if i == c.sel {
			btns = append(btns, sSelected.Render("▶"+label))
		} else {
			btns = append(btns, " "+label)
		}
	}
	return overlayBox(c.title, strings.Join(lines, "\n")+"\n\n"+strings.Join(btns, "  "), bw)
}

func (c *choiceOverlay) hints() string {
	var parts []string
	for _, ch := range c.choices {
		parts = append(parts, ch.key, strings.ToLower(ch.label))
	}
	return keyHint(append(parts, "←/→", "choose", "enter", "confirm choice")...)
}
