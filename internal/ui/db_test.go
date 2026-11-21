package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"lazymage/internal/docker"
	"lazymage/internal/runner"
)

// fakeDBApp is a test app wired to the fake docker CLI with the db service up.
func fakeDBApp(t *testing.T) (*App, string, chan tea.Msg) {
	t.Helper()
	a, root := newTestApp(t, true)
	fake, _ := filepath.Abs("../../testdata/fakebin/docker")
	t.Setenv("LAZYMAGE_DOCKER", fake)
	t.Setenv("FAKE_DOCKER_STATE", t.TempDir())
	os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services:\n  phpfpm:\n    image: x\n  db:\n    image: mariadb\n"), 0o644)
	if _, errOut, code, _ := runner.Capture(root, []string{fake, "compose", "up", "-d"}, 10*time.Second); code != 0 {
		t.Fatalf("fake up: %s", errOut)
	}
	msgs := make(chan tea.Msg, 1000)
	a.SetSender(func(m tea.Msg) { msgs <- m })
	a.services = []string{"phpfpm", "db"}
	return a, root, msgs
}

// pump runs cmd (dropping ones that block, like ticks) and feeds the app
// every message until done() holds.
func pump(t *testing.T, a *App, msgs chan tea.Msg, cmd tea.Cmd, done func() bool) {
	t.Helper()
	var run func(tea.Cmd)
	run = func(c tea.Cmd) {
		if c == nil {
			return
		}
		ch := make(chan tea.Msg, 1)
		go func() { ch <- c() }()
		select {
		case m := <-ch:
			if b, ok := m.(tea.BatchMsg); ok {
				for _, x := range b {
					run(x)
				}
				return
			}
			if m != nil {
				msgs <- m
			}
		case <-time.After(150 * time.Millisecond):
		}
	}
	run(cmd)
	deadline := time.After(20 * time.Second)
	for !done() {
		select {
		case m := <-msgs:
			switch m.(type) {
			case runTickMsg:
				continue
			}
			_, c := a.Update(m)
			run(c)
		case <-deadline:
			t.Fatalf("timed out; output:\n%s", outText(a))
		}
	}
}

func outText(a *App) string {
	if a.out == nil {
		return ""
	}
	var b strings.Builder
	for _, l := range a.out.buf.Lines {
		b.WriteString(l.Text + "\n")
	}
	return b.String()
}

func top(a *App) overlay {
	if len(a.overlays) == 0 {
		return nil
	}
	return a.overlays[len(a.overlays)-1]
}

func TestDBDumpThenImportWithBackup(t *testing.T) {
	a, root, msgs := fakeDBApp(t)
	a.do(aDBDump)
	if _, ok := top(a).(*promptOverlay); !ok {
		t.Fatal("dump should ask for a file name")
	}
	pr := top(a).(*promptOverlay)
	pr.in.SetValue("backups/one")
	_, cmd := a.Update(key("enter"))
	pump(t, a, msgs, cmd, func() bool { return a.run == nil && a.out != nil && a.out.exit != nil })
	dump := filepath.Join(root, "backups", "one.sql.gz")
	if st, err := os.Stat(dump); err != nil || st.Size() == 0 {
		t.Fatalf("dump not written: %v\n%s", err, outText(a))
	}
	if _, err := os.Stat(dump + ".partial"); !os.IsNotExist(err) {
		t.Fatal("partial file left behind")
	}
	if strings.Contains(outText(a), "INSERT INTO") {
		t.Fatal("dump contents must go to the file, not the output view")
	}

	// Import it through bin/mysql, backing up first.
	withBinMysql(t, root)
	a.do(aDBImport)
	if _, ok := top(a).(*dumpPicker); !ok {
		t.Fatal("import should open the dump picker")
	}
	a.Update(key("enter"))
	ch, ok := top(a).(*choiceOverlay)
	if !ok {
		t.Fatal("import must confirm")
	}
	if ch.choices[ch.sel].run != nil {
		t.Fatal("the default choice must be cancel")
	}
	_, cmd = a.Update(key("b"))
	pump(t, a, msgs, cmd, func() bool {
		return a.run == nil && a.out != nil && a.out.exit != nil && strings.Contains(a.out.title, "< backups/one.sql.gz")
	})
	if !strings.HasPrefix(a.out.title, "bin/mysql < ") {
		t.Fatalf("import should run bin/mysql < file, ran %s", a.out.title)
	}
	if a.out.exit.Code != 0 {
		t.Fatalf("import failed:\n%s", outText(a))
	}
	backups, _ := filepath.Glob(filepath.Join(root, "backups", "*-before-import-*.sql.gz"))
	if len(backups) != 1 {
		t.Fatalf("expected one pre-import backup, got %v", backups)
	}
	h := a.cfg.Projects[a.proj.Root].History
	if len(h) < 3 {
		t.Fatalf("history: %+v", h)
	}
	for _, e := range h {
		if strings.Contains(e.Command, "MYSQL_PWD=") && !strings.Contains(e.Command, `MYSQL_PWD="$p"`) {
			t.Errorf("history holds a password: %s", e.Command)
		}
	}
}

// withBinMysql gives the fake project docker-magento's bin/mysql (its non-TTY
// branch, which is the one lazymage uses): clinotty → exec -T phpfpm mysql.
func withBinMysql(t *testing.T, root string) {
	t.Helper()
	script := "#!/bin/sh\nexec \"$LAZYMAGE_DOCKER\" compose exec -T phpfpm mysql -hdb -umagento -pmagento magento \"$@\"\n"
	if err := os.WriteFile(filepath.Join(root, "bin", "mysql"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestDBReplaceURLs(t *testing.T) {
	a, root, msgs := fakeDBApp(t)
	withBinMysql(t, root)
	os.WriteFile(filepath.Join(os.Getenv("FAKE_DOCKER_STATE"), "db-domain"), []byte("www.prod-shop.com"), 0o644)
	a.facts.Domain = "shop.test"
	pump(t, a, msgs, a.do(aDBReplaceURL), func() bool { _, ok := top(a).(*promptOverlay); return ok })
	if v := top(a).(*promptOverlay).in.Value(); v != "www.prod-shop.com" {
		t.Fatalf("find default %q", v)
	}
	a.Update(key("enter"))
	if v := top(a).(*promptOverlay).in.Value(); v != "shop.test" {
		t.Fatalf("replace default %q", v)
	}
	_, cmd := a.Update(key("enter"))
	pump(t, a, msgs, cmd, func() bool { _, ok := top(a).(*confirmOverlay); return ok })
	if body := top(a).(*confirmOverlay).body; !strings.Contains(body, "web/cookie/cookie_domain") {
		t.Fatalf("preview should list matching rows:\n%s", body)
	}
	if body := top(a).(*confirmOverlay).body; !strings.Contains(body, "$ bin/mysql -N -B -e 'UPDATE") {
		t.Fatalf("the update should go through bin/mysql:\n%s", body)
	}
	_, cmd = a.Update(key("y"))
	pump(t, a, msgs, cmd, func() bool {
		return a.run == nil && a.out != nil && a.out.exit != nil && strings.Contains(a.out.title, "cache:flush")
	})
	if a.out.exit.Code != 0 {
		t.Fatalf("cache:flush after replace failed:\n%s", outText(a))
	}
	b, _ := os.ReadFile(filepath.Join(os.Getenv("FAKE_DOCKER_STATE"), "db-domain"))
	if string(b) != "shop.test" {
		t.Fatalf("db domain now %q", b)
	}
}

func TestUpdateNoticeFitsHeader(t *testing.T) {
	a, _ := newTestApp(t, true)
	a.updateAvail.Tag = "v0.2.0"
	for _, w := range []int{100, 160} {
		a.w, a.h = w, 30
		out := a.View().Content
		if !strings.Contains(stripANSI(out), "v0.2.0 available") {
			t.Fatalf("width %d: no update notice", w)
		}
		for i, l := range strings.Split(out, "\n") {
			if lenPlain(l) > w {
				t.Fatalf("width %d line %d overflows: %d", w, i, lenPlain(l))
			}
		}
		if n := len(strings.Split(out, "\n")); n != 30 {
			t.Fatalf("width %d: %d lines", w, n)
		}
	}
	es, _ := buildEntries(a)
	found := false
	for _, e := range es {
		if e.ID == aUpdate && e.Shortcut == "U" && e.Disabled == "" {
			found = true
		}
	}
	if !found {
		t.Fatal("palette should offer Update lazymage… with shortcut U")
	}
}

func TestGenerateLocalThemes(t *testing.T) {
	a, root, msgs := fakeDBApp(t)
	cfgDir := filepath.Join(root, "src/dev/tools/grunt/configs")
	os.MkdirAll(cfgDir, 0o755)
	old := "module.exports = { mine: { area: 'frontend', name: 'Acme/default', locale: 'en_US', files: ['css/styles-m', 'css/extra'], dsl: 'less' } };"
	os.WriteFile(filepath.Join(cfgDir, "local-themes.js"), []byte(old), 0o644)

	pump(t, a, msgs, a.do(aFEThemes), func() bool { _, ok := top(a).(*themePicker); return ok })
	p := top(a).(*themePicker)
	sel := map[string]bool{}
	for _, c := range p.cands {
		if c.Selected {
			sel[c.Key+"|"+c.Locale] = true
		}
	}
	if !sel["mine|en_US"] || !sel["default|de_DE"] || len(sel) != 2 {
		t.Fatalf("preselection: %v", sel)
	}
	a.Update(key("enter"))
	if _, ok := top(a).(*confirmOverlay); !ok {
		t.Fatal("writing must show a preview first")
	}
	a.Update(key("y"))
	got, _ := os.ReadFile(filepath.Join(cfgDir, "local-themes.js"))
	bak, _ := os.ReadFile(filepath.Join(cfgDir, "local-themes.js.bak"))
	if string(bak) != old {
		t.Fatal("the previous file must be backed up")
	}
	for _, want := range []string{"mine: {", "'css/extra'", "default: {", "locale: 'de_DE'"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("local-themes.js missing %q:\n%s", want, got)
		}
	}

	// The grunt prompt offers the generated keys, then remembers the choice.
	a.do(aFEGruntBuild)
	pr, ok := top(a).(*promptOverlay)
	if !ok || len(pr.p.Choices) != 2 || !contains(pr.p.Choices, "mine") || !contains(pr.p.Choices, "default") {
		t.Fatal("grunt prompt should offer the keys from local-themes.js")
	}
	pr.in.SetValue("mine")
	_, cmd := a.Update(key("enter"))
	pump(t, a, msgs, cmd, func() bool { return a.run == nil && a.out != nil && a.out.exit != nil })
	if !strings.Contains(a.out.title, `npx grunt clean:"$1" && npx grunt exec:"$1" && npx grunt less:"$1"' lazymage mine`) {
		t.Fatalf("grunt build command: %s", a.out.title)
	}
	a.do(aFEGruntBuild)
	if v := top(a).(*promptOverlay).in.Value(); v != "mine" {
		t.Fatalf("second build should default to the last theme, got %q", v)
	}
}

func TestStackTabReplacesLogs(t *testing.T) {
	a, _ := newTestApp(t, true)
	a.services = []string{"app", "db", "phpfpm"}
	a.containers = []docker.Container{{Service: "app", State: "running"}, {Service: "phpfpm", State: "running"}}
	a.health = docker.Summary{Health: docker.HealthSomeDown, Up: 2, Total: 3}
	a.facts.Domain = "shop.test"
	out := stripANSI(a.View().Content)
	if os.Getenv("SHOW") != "" {
		t.Log("\n" + out)
	}
	for _, want := range []string{"1 Stack", "Environment", "Engine", "2/3 up — not up: db", "Start environment", "Restart one service…", "https://shop.test/"} {
		if !strings.Contains(out, want) {
			t.Errorf("stack tab missing %q", want)
		}
	}
	if strings.Contains(out, "1 Logs") {
		t.Error("the Logs tab should be gone")
	}
	a.stack.cursor = 2 // Restart one service…
	a.Update(key("enter"))
	sp, ok := top(a).(*servicePicker)
	if !ok || !sp.restart || len(sp.options(a)) != 3 {
		t.Fatal("restart one service should open a service picker without 'all services'")
	}
	a.Update(key("esc"))
	a.do(aLogsAll)
	if a.tab != tabLogs || !strings.Contains(stripANSI(a.View().Content), "Logs") {
		t.Fatal("logs still open from the palette, as a temporary tab")
	}
	a.do(aNextTab)
	if a.tab != tabStack {
		t.Fatalf("tab from logs goes back to the cycle, got %d", a.tab)
	}
}
