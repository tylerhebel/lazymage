package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"lazymage/internal/config"
	"lazymage/internal/docker"
)

// newTestApp builds an App against a temp config dir and a minimal project.
func newTestApp(t *testing.T, withConfig bool) (*App, string) {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("LAZYMAGE_DOCKER", "/nonexistent/docker")
	root := t.TempDir()
	for _, d := range []string{"bin", "src/app/etc"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services:\n  phpfpm:\n    image: markoshust/magento-php:8.4-fpm-3\n"), 0o644)
	for _, b := range []string{"magento", "clinotty", "start", "stop", "restart"} {
		os.WriteFile(filepath.Join(root, "bin", b), []byte("#!/bin/sh\n"), 0o755)
	}
	if withConfig {
		c := config.Defaults("/bin/zsh")
		c.Runtime = config.RuntimeOrbStack
		if err := c.Save(config.Path()); err != nil {
			t.Fatal(err)
		}
	}
	a := New(Options{Version: "test", StartDir: filepath.Join(root, "src", "app"), LoginShell: "/bin/zsh"})
	a.w, a.h = 100, 30
	a.engine = engineRunning
	return a, root
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+s":
		return tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	case "ctrl+n":
		return tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func typeText(a *App, s string) {
	for _, r := range s {
		a.Update(key(string(r)))
	}
}

func TestFirstRunShowsWizardAndCancelWritesNothing(t *testing.T) {
	a, _ := newTestApp(t, false)
	if a.screen != screenSetup || !a.wiz.first {
		t.Fatal("first run should open the setup wizard")
	}
	_, cmd := a.Update(key("ctrl+c"))
	if cmd == nil {
		t.Fatal("ctrl+c on first run should quit")
	}
	if _, err := os.Stat(config.Path()); !os.IsNotExist(err) {
		t.Fatal("cancelled first-run setup must not leave a config file")
	}
}

func TestWizardSavesExactRuntime(t *testing.T) {
	for _, rt := range config.Runtimes {
		a, _ := newTestApp(t, false)
		w := a.wiz
		w.setCandidates(a, candidateStub{}.build())
		a.Update(key("enter")) // step 1 → 2
		for i, c := range w.cands {
			if c.Runtime == rt {
				w.rtCursor, w.rtPicked = i, true
			}
		}
		a.Update(key("enter")) // not running → sub menu
		a.Update(key("j"))     // Continue anyway
		a.Update(key("enter"))
		for w.step() != 6 {
			a.Update(key("enter"))
		}
		a.Update(key("enter"))
		c, err := config.Load(config.Path())
		if err != nil {
			t.Fatal(err)
		}
		if c.Runtime != rt {
			t.Errorf("chose %s, saved %s", rt, c.Runtime)
		}
		if a.screen != screenMain {
			t.Error("saving should open the main screen")
		}
	}
}

type candidateStub []struct{}

type dockerCandidate = docker.Candidate

func (candidateStub) build() []dockerCandidate {
	return []dockerCandidate{{Runtime: config.RuntimeOrbStack}, {Runtime: config.RuntimeDockerDesktop}, {Runtime: config.RuntimeDockerEngine}}
}

func TestSecondLaunchSkipsWizard(t *testing.T) {
	a, _ := newTestApp(t, true)
	if a.screen != screenMain {
		t.Fatal("existing valid config should open the main screen")
	}
	if a.proj == nil {
		t.Fatal("project should be detected from a nested directory")
	}
}

func TestPaletteCatalogAndSearch(t *testing.T) {
	a, _ := newTestApp(t, true)
	es, _ := buildEntries(a)
	titles := map[string]paletteEntry{}
	for _, e := range es {
		titles[e.Title] = e
	}
	want := []string{
		"Start stack", "Stop stack (confirm)", "Restart stack", "Start engine", "Refresh status and project facts",
		"Logs: all services", "Logs: pick a service…", "Pause / resume stream", "Toggle follow", "Search logs", "Clear log buffer", "Save log buffer to file…",
		"cache:flush", "cache:clean", "cache:status", "indexer:reindex", "indexer:status",
		"setup:upgrade", "setup:di:compile", "setup:static-content:deploy", "deploy:mode:show", "config:show", "module:status", "cron:run", "Run a Magento command…",
		"composer install", "composer update", "composer require…", "composer show --outdated", "composer dump-autoload", "Run a Composer command…",
		"Shell into PHP container", "Open storefront", "Open admin", "Copy storefront URL", "Open project .env in editor", "Open TUI config in editor",
		"Settings", "Re-run setup", "Help", "Create a custom command…", "Manage custom commands…", "Quit",
	}
	for _, w := range want {
		if _, ok := titles[w]; !ok {
			t.Errorf("palette missing %q", w)
		}
	}
	if titles["Shell into PHP container"].Shortcut != "s" || titles["Quit"].Shortcut != "q" || titles["Toggle follow"].Shortcut != "f" {
		t.Error("palette rows must show their shortcuts")
	}
	for q, top := range map[string]string{"flush": "cache:flush", "reindex": "indexer:reindex", "restart": "Restart stack", "shell": "Shell into PHP container"} {
		r := rankEntries(es, q, nil)
		if len(r) == 0 || r[0].Title != top {
			got := ""
			if len(r) > 0 {
				got = r[0].Title
			}
			t.Errorf("search %q: top is %q, want %q", q, got, top)
		}
	}
	a.engine = engineStopped
	es, _ = buildEntries(a)
	for _, e := range es {
		if e.ID == aLogsAll && e.Disabled == "" {
			t.Error("logs should be disabled with a reason while the engine is stopped")
		}
	}
}

func TestCustomCommandFlow(t *testing.T) {
	a, root := newTestApp(t, true)
	a.Update(key("x"))
	a.Update(key("ctrl+n"))
	f, ok := a.overlays[len(a.overlays)-1].(*customForm)
	if !ok {
		t.Fatal("ctrl+n in the palette should open the custom command form")
	}
	typeText(a, "Reindex one")
	a.Update(key("tab"))
	a.Update(key("tab"))
	a.Update(key("tab")) // command
	typeText(a, "indexer:reindex {{indexer}}")
	if len(f.prompts) != 1 || f.prompts[0].key != "indexer" {
		t.Fatalf("placeholder prompt not created: %+v", f.prompts)
	}
	// Shortcut: "o" is the built-in storefront key → conflict dialog.
	for f.cur() != "shortcut" {
		a.Update(key("tab"))
	}
	a.Update(key("enter"))
	a.Update(key("o"))
	if f.conflict == nil {
		t.Fatal("assigning a used shortcut must ask replace / pick another / cancel")
	}
	a.Update(key("c")) // cancel keeps the previous (none)
	if f.shortcut != "" {
		t.Fatal("cancel must not assign the shortcut")
	}
	a.Update(key("enter"))
	a.Update(key("c")) // plain "c" is free
	if f.shortcut != "c" {
		t.Fatalf("shortcut not assigned: %q", f.shortcut)
	}
	a.Update(key("ctrl+s"))
	if len(a.overlays) != 0 {
		t.Fatalf("form should close on save, errors: %v", f.errs)
	}
	cmds := a.cfg.Project(a.proj.Root).CustomCommands
	if len(cmds) != 1 || cmds[0].Shortcut != "c" {
		t.Fatalf("not saved: %+v", cmds)
	}
	// It is in the palette immediately, and in help.
	es, _ := buildEntries(a)
	found := false
	for _, e := range es {
		if e.Title == "Reindex one" && e.Custom && e.Shortcut == "c" {
			found = true
		}
	}
	if !found {
		t.Fatal("new custom command missing from the palette")
	}
	help := helpSections(a.cfg.EffectiveCommands(a.root()))
	if last := help[len(help)-1]; len(last.Rows) != 1 || last.Rows[0][0] != "c" {
		t.Fatalf("help should list the custom shortcut: %+v", last)
	}
	// Persisted: reload from disk.
	c, err := config.Load(config.Path())
	if err != nil || len(c.Project(root2(a)).CustomCommands) != 1 {
		t.Fatalf("custom command not persisted: %v", err)
	}
	_ = root
}

func root2(a *App) string { return a.proj.Root }

func TestShortcutReplaceStealsFromOtherCustom(t *testing.T) {
	a, _ := newTestApp(t, true)
	a.cfg.CustomCommands = []config.CustomCommand{{Name: "A", Kind: config.KindBin, Command: "a", Shortcut: "z"}}
	f := newCustomForm(a, nil, false)
	a.push(f)
	typeText(a, "B")
	f.command.SetValue("b")
	f.kind = indexOf(config.Kinds, config.KindBin)
	f.field = indexOf(f.fields(), "shortcut")
	a.Update(key("enter"))
	a.Update(key("z"))
	if f.conflict == nil || f.conflict.custom == nil {
		t.Fatal("expected a conflict with custom command A")
	}
	a.Update(key("r"))
	a.Update(key("ctrl+s"))
	if a.cfg.CustomCommands[0].Shortcut != "" {
		t.Fatal("replace should clear A's shortcut")
	}
	if got := a.cfg.Project(a.proj.Root).CustomCommands; len(got) != 1 || got[0].Shortcut != "z" {
		t.Fatalf("B should own z: %+v", got)
	}
}

func TestViewFitsMinimumSize(t *testing.T) {
	a, _ := newTestApp(t, true)
	for _, sz := range [][2]int{{100, 30}, {160, 50}} {
		a.w, a.h = sz[0], sz[1]
		for _, tab := range []int{0, 1, 2} {
			a.tab = tab
			out := a.View().Content
			lines := strings.Split(out, "\n")
			if len(lines) != sz[1] {
				t.Errorf("%v tab %d: %d lines", sz, tab, len(lines))
			}
		}
	}
}

func TestHealthTextNotColorOnly(t *testing.T) {
	a, _ := newTestApp(t, true)
	a.engine = engineStopped
	if !strings.Contains(stripANSI(a.healthLabel()), "engine stopped") {
		t.Fatal("health must include text")
	}
}

func TestBufferPartialAndCR(t *testing.T) {
	var b Buffer
	b.Append("Continue? [y/N] ", false)
	if len(b.Lines) != 1 || b.Lines[0].Text != "Continue? [y/N] " {
		t.Fatalf("partial line: %+v", b.Lines)
	}
	b.Append("\n10%\r50%\r100%\ndone\n", false)
	if b.Lines[1].Text != "100%" || b.Lines[2].Text != "done" {
		t.Fatalf("cr handling: %+v", b.Lines)
	}
	b.Append("\x1b[31mred\x1b[0m\n", true)
	if b.Lines[3].Text != "red" || !b.Lines[3].Err {
		t.Fatalf("ansi/stderr: %+v", b.Lines[3])
	}
}
