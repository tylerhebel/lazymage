package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lazymage/internal/config"
	"lazymage/internal/docker"
)

func TestProjectOverridesInSettings(t *testing.T) {
	a, root := newTestApp(t, true)
	a.push(newSettings(a))
	s := top(a).(*settingsOverlay)
	for i, r := range settingRows {
		if r.id == "mode" {
			s.cursor = i
		}
	}
	a.Update(key("p")) // make integration mode project-specific
	if !a.eff().Overridden["mode"] {
		t.Fatal("p should create a project override")
	}
	a.Update(key("l")) // cycle: changes the override, not the global value
	if a.cfg.IntegrationMode != config.ModeHybrid || a.eff().IntegrationMode == config.ModeHybrid {
		t.Fatalf("global %q effective %q", a.cfg.IntegrationMode, a.eff().IntegrationMode)
	}
	if a.builder().Mode != a.eff().IntegrationMode {
		t.Fatal("commands must use the project's mode")
	}
	c, err := config.Load(config.Path())
	if err != nil || c.Projects[root].Overrides == nil {
		t.Fatalf("override not saved: %v", err)
	}
	a.Update(key("p")) // back to global
	if a.eff().Overridden["mode"] || a.cfg.Projects[root].Overrides != nil {
		t.Fatal("p again should drop the override")
	}
}

func TestSwitchProject(t *testing.T) {
	a, first := newTestApp(t, true)
	other := t.TempDir()
	os.MkdirAll(filepath.Join(other, "bin"), 0o755)
	os.MkdirAll(filepath.Join(other, "src"), 0o755)
	os.WriteFile(filepath.Join(other, "bin", "magento"), []byte("#!/bin/sh\n"), 0o755)
	os.WriteFile(filepath.Join(other, "compose.yaml"), []byte("services:\n  phpfpm:\n    image: x\n"), 0o644)
	a.cfg.Project(other).LastOpened = time.Now().Add(-time.Hour)

	a.do(aSwitchProj)
	p, ok := top(a).(*projectPicker)
	if !ok || len(p.all) != 2 || p.all[0].Root != first {
		t.Fatalf("picker should list both projects, current first: %+v", p)
	}
	a.Update(key("down"))
	a.Update(key("enter"))
	if a.proj == nil || a.proj.Root != other || len(a.overlays) != 0 {
		t.Fatalf("did not switch: %v (picker error %q, cursor %d)", a.proj, p.err, p.cursor)
	}
	if a.cfg.Projects[other].LastOpened.Before(time.Now().Add(-time.Minute)) {
		t.Fatal("switching should record the time")
	}
	// Not while a background task runs.
	a.bgs = []*bgTask{{out: &outputView{}}}
	a.do(aSwitchProj)
	if len(a.overlays) != 0 || !strings.Contains(a.status, "background") {
		t.Fatalf("switch should be blocked: %q", a.status)
	}
}

func TestGitTab(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	a, root, msgs := fakeDBApp(t)
	src := filepath.Join(root, "src")
	os.WriteFile(filepath.Join(src, "app", "etc", "env.php"), []byte("<?php\n"), 0o644)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", "first"}} {
		if out, err := exec.Command("git", append([]string{"-C", src}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", args, out)
		}
	}
	os.WriteFile(filepath.Join(src, "app", "etc", "env.php"), []byte("<?php return [];\n"), 0o644)
	pump(t, a, msgs, a.do(aTab4), func() bool { return a.git != nil && a.git.loaded })
	out := stripANSI(a.View().Content)
	for _, want := range []string{"4 Git", "main", "modified", "app/etc/env.php", "first"} {
		if !strings.Contains(out, want) {
			t.Errorf("git tab missing %q:\n%s", want, out)
		}
	}
	_, cmd := a.Update(key("enter")) // diff of the selected file
	pump(t, a, msgs, cmd, func() bool { return a.out != nil && a.out.exit != nil })
	if !strings.Contains(outText(a), "+<?php return [];") {
		t.Fatalf("diff output:\n%s", outText(a))
	}
}

func TestBackgroundTasks(t *testing.T) {
	a, root, msgs := fakeDBApp(t)
	os.WriteFile(filepath.Join(root, "bin", "clinotty"), []byte("#!/bin/sh\necho \"$@\"; sleep 30\n"), 0o755)
	pump(t, a, msgs, a.do(aFEGruntWatch), func() bool { return len(a.bgs) == 1 && len(a.bgs[0].out.buf.Lines) > 1 })
	if a.run != nil || a.showO {
		t.Fatal("grunt watch must not take the foreground")
	}
	if !strings.Contains(stripANSI(a.View().Content), "◌ grunt watch") {
		t.Fatal("tab bar should show the background task")
	}
	// The foreground is still free.
	if r := a.disabledReason("magento:cache:flush"); r != "" {
		t.Fatalf("foreground blocked: %s", r)
	}
	// Move a foreground command to the background.
	a.startRun(a.builder().Magento("indexer:reindex"), runOpts{kind: config.KindMagento})
	a.Update(key("ctrl+b"))
	if a.run != nil || len(a.bgs) != 2 {
		t.Fatalf("ctrl+b should move the command: run=%v bgs=%d", a.run, len(a.bgs))
	}
	// Stop both from the list.
	a.do(aBgList)
	if _, ok := top(a).(*bgList); !ok {
		t.Fatal("B should list background tasks")
	}
	a.Update(key("s"))
	a.Update(key("down"))
	a.Update(key("s"))
	pump(t, a, msgs, nil, func() bool { return !a.bgRunning() })
	for _, tk := range a.bgs {
		if tk.out.exit == nil || !tk.out.exit.Cancelled {
			t.Fatalf("task not stopped: %+v", tk.out.exit)
		}
	}
}

func TestOpsChecklist(t *testing.T) {
	a, root := newTestApp(t, true)
	os.MkdirAll(filepath.Join(root, "src", "var"), 0o755)
	os.WriteFile(filepath.Join(root, "src", "var", ".maintenance.flag"), nil, 0o644)
	os.WriteFile(filepath.Join(root, "src", "var", ".cron-enabled"), nil, 0o644)
	os.WriteFile(filepath.Join(root, "src", "nginx.conf.sample"), []byte("fastcgi_pass fastcgi_backend;"), 0o644)
	os.WriteFile(filepath.Join(root, "bin", "setup-nginx"), []byte("#!/bin/sh\n"), 0o755)
	a.services = []string{"phpfpm", "phpfpm-xdebug"}
	a.containers = []docker.Container{{Service: "phpfpm-xdebug", State: "running"}}
	out := stripANSI(a.View().Content)
	for _, want := range []string{"Maintenance    ON", "Cron           enabled", "nginx not routing"} {
		if !strings.Contains(out, want) {
			t.Errorf("checklist missing %q:\n%s", want, out)
		}
	}
	if a.disabledReason(aXdebugSetup) != "" || a.disabledReason(aXdebugOn) == "" {
		t.Fatal("cookie-style project: offer setup-nginx, not the legacy toggle")
	}
	os.WriteFile(filepath.Join(root, "src", "nginx.conf.sample"), []byte("fastcgi_pass $fastcgi_backend;"), 0o644)
	if !strings.Contains(stripANSI(a.View().Content), "Xdebug         ready") {
		t.Fatal("routing set up: Xdebug should read ready")
	}
}
