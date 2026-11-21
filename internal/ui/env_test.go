package ui

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"lazymage/internal/config"
	"lazymage/internal/dm"
	"lazymage/internal/docker"
)

// envApp is a fake-docker app in docker-only mode (so every command goes
// through the fake CLI) with the whole stack up.
func envApp(t *testing.T) (*App, string, chan tea.Msg) {
	t.Helper()
	a, root, msgs := fakeDBApp(t)
	// No GitHub in tests: a local server that knows no releases.
	gh := httptest.NewServer(http.NotFoundHandler())
	// No nginx either: a listener that hangs up at once.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	old, oldTLS := dm.APIBase, dm.LocalTLS
	dm.APIBase, dm.LocalTLS = gh.URL, ln.Addr().String()
	t.Cleanup(func() { dm.APIBase, dm.LocalTLS = old, oldTLS; gh.Close(); ln.Close() })
	os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services:\n  app:\n    image: x\n  phpfpm:\n    image: x\n  phpfpm-xdebug:\n    image: x\n  db:\n    image: mariadb\n"), 0o644)
	a.cfg.IntegrationMode = config.ModeDockerOnly
	a.services = []string{"app", "phpfpm", "phpfpm-xdebug", "db"}
	a.engine = engineRunning
	a.facts.MagentoVersion = "2.4.7-p3"
	pump(t, a, msgs, a.startRun(a.builder().Compose("up", "-d"), runOpts{kind: config.KindCompose}), func() bool { return a.run == nil && a.out != nil && a.out.exit != nil })
	a.containers = []docker.Container{{Name: "proj-phpfpm-1", Service: "phpfpm", State: "running"}, {Name: "proj-app-1", Service: "app", State: "running"},
		{Name: "proj-db-1", Service: "db", State: "running"}, {Name: "proj-phpfpm-xdebug-1", Service: "phpfpm-xdebug", State: "running"}}
	a.overlays = nil
	return a, root, msgs
}

func planDone(a *App, title string) func() bool {
	return func() bool {
		return planSummary(a, title) != nil && !a.doctorBusy
	}
}

// planSummary finds a plan's summary among the overlays (WSL adds the
// Windows hosts note on top of it).
func planSummary(a *App, title string) *messageOverlay {
	for i := len(a.overlays) - 1; i >= 0; i-- {
		if m, ok := a.overlays[i].(*messageOverlay); ok && strings.HasPrefix(m.title, title) {
			return m
		}
	}
	return nil
}

func overrideImage(t *testing.T, root, svc string) string {
	t.Helper()
	o, err := dm.LoadOverride(root)
	if err != nil {
		t.Fatal(err)
	}
	return o.Image(svc)
}

func TestPHPSwitchVerifiesAndRollsBack(t *testing.T) {
	a, root, msgs := envApp(t)

	// 1. A PHP that works: 8.3.
	pump(t, a, msgs, a.do(aPHPSwitch), func() bool { _, ok := top(a).(*listPicker); return ok })
	lp := top(a).(*listPicker)
	if !strings.Contains(lp.body, "8.4-fpm-3") {
		t.Fatalf("picker should show the current image:\n%s", lp.body)
	}
	idx := -1
	for i, it := range lp.items {
		if strings.Contains(it.label, "(8.3-") {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("no PHP 8.3 choice: %+v", lp.items)
	}
	lp.cursor = idx
	a.Update(key("enter"))
	if _, ok := top(a).(*confirmOverlay); !ok {
		t.Fatal("switching must confirm first")
	}
	_, cmd := a.Update(key("y"))
	pump(t, a, msgs, cmd, planDone(a, "Switch to PHP 8.3"))
	m := top(a).(*messageOverlay)
	if m.title != "Switch to PHP 8.3 — done" {
		t.Fatalf("%s\n%s\n%s", m.title, m.body, outText(a))
	}
	if got := overrideImage(t, root, "phpfpm"); got != "markoshust/magento-php:8.3-fpm-8" {
		t.Fatalf("override phpfpm image = %q", got)
	}
	if got := overrideImage(t, root, "phpfpm-xdebug"); got != "markoshust/magento-php:8.3-fpm-xdebug-8" {
		t.Fatalf("override xdebug image = %q", got)
	}
	a.overlays = nil

	// 2. A PHP that breaks bin/magento (8.5 in the fake): rolled back.
	before, _ := os.ReadFile(filepath.Join(root, dm.OverrideFile))
	pump(t, a, msgs, a.switchPHP("markoshust/magento-php", "8.5-fpm-1", true, true), planDone(a, "Switch to PHP 8.5"))
	m = top(a).(*messageOverlay)
	if !strings.Contains(m.title, "rolled back") || !strings.Contains(m.body, "bin/magento works") {
		t.Fatalf("expected a rollback:\n%s\n%s", m.title, m.body)
	}
	after, _ := os.ReadFile(filepath.Join(root, dm.OverrideFile))
	if string(after) != string(before) {
		t.Fatalf("override not restored:\n%s", after)
	}
	a.overlays = nil

	// 3. A PHP that fails the platform check (8.1): nothing changes, and
	// switching anyway is offered.
	pump(t, a, msgs, a.switchPHP("markoshust/magento-php", "8.1-fpm-9", true, true), func() bool {
		if m, ok := top(a).(*messageOverlay); ok {
			t.Fatalf("%s\n%s\n%s", m.title, m.body, outText(a))
		}
		c, ok := top(a).(*confirmOverlay)
		return ok && strings.Contains(c.title, "platform requirements") && !a.doctorBusy
	})
	after, _ = os.ReadFile(filepath.Join(root, dm.OverrideFile))
	if string(after) != string(before) {
		t.Fatal("a failed platform check must not change anything")
	}
	a.overlays = nil

	// 4. An image tag that does not exist: stops at the pull.
	pump(t, a, msgs, a.switchPHP("markoshust/magento-php", "8.4-fpm-bad", true, true), planDone(a, "Switch to PHP"))
	if m := top(a).(*messageOverlay); !strings.Contains(m.body, "✗") || !strings.Contains(m.body, "pull") {
		t.Fatalf("pull failure:\n%s", m.body)
	}
}

func TestMultistoreApplyAndRollback(t *testing.T) {
	a, root, msgs := envApp(t)
	hosts := filepath.Join(t.TempDir(), "hosts")
	os.WriteFile(hosts, []byte("127.0.0.1 magento.test de.magento.test broken.magento.test b2b.invalid\n"), 0o644)
	etcHosts = hosts
	t.Cleanup(func() { etcHosts = "/etc/hosts" })
	sample := "location ~ ^/(index|get|static|errors/report|errors/404|errors/503|health_check)\\.php$ {\n    fastcgi_pass   fastcgi_backend;\n    include        fastcgi_params;\n}\n"
	os.MkdirAll(filepath.Join(root, "src"), 0o755)
	os.WriteFile(filepath.Join(root, dm.NginxSample), []byte(sample), 0o644)

	pump(t, a, msgs, a.do(aMultistore), func() bool { _, ok := top(a).(*msOverlay); return ok })
	o := top(a).(*msOverlay)
	if len(o.stores) != 3 {
		t.Fatalf("stores: %+v", o.stores)
	}
	// german → its own domain; the disabled b2b website keeps b2b.invalid.
	gi := -1
	for i, s := range o.stores {
		if s.Code == "german" {
			gi = i
		}
	}
	o.domains[gi] = "de.magento.test"
	a.Update(key("a"))
	if _, ok := top(a).(*confirmOverlay); !ok {
		t.Fatalf("apply must confirm first; err=%q", o.err)
	}
	_, cmd := a.Update(key("y"))
	pump(t, a, msgs, cmd, planDone(a, "Multistore setup"))
	m := planSummary(a, "Multistore setup")
	if m.title != "Multistore setup — done" {
		t.Fatalf("%s\n%s\n%s", m.title, m.body, outText(a))
	}
	conf, _ := os.ReadFile(filepath.Join(root, dm.StoreMapFile))
	if !strings.Contains(string(conf), "de.magento.test german;") {
		t.Fatalf("store.map.conf:\n%s", conf)
	}
	if o, _ := dm.LoadOverride(root); !o.HasVolume("app", dm.StoreMapVolume) {
		t.Fatal("store map not mounted")
	}
	if b, _ := os.ReadFile(filepath.Join(root, dm.NginxSample)); !dm.HasRunParams(string(b)) {
		t.Fatal("nginx.conf.sample not patched")
	}
	// The base URL was set for the store scope.
	if !strings.Contains(m.body, "web/secure/base_url of german → https://de.magento.test/") {
		t.Fatalf("summary:\n%s", m.body)
	}
	a.overlays = nil

	// nginx rejects the next config: everything is put back.
	goodConf, _ := os.ReadFile(filepath.Join(root, dm.StoreMapFile))
	goodSample, _ := os.ReadFile(filepath.Join(root, dm.NginxSample))
	o2 := newMSOverlay(a.fe.stores)
	for i, s := range o2.stores {
		if s.Code == "german" {
			o2.domains[i] = "broken.magento.test"
		}
	}
	ms, _ := dm.Mappings(o2.sites())
	pump(t, a, msgs, a.applyMultistore(o2, ms, []string{"magento.test", "broken.magento.test", "b2b.invalid"}), planDone(a, "Multistore setup"))
	m = planSummary(a, "Multistore setup")
	if !strings.Contains(m.title, "rolled back") || !strings.Contains(m.body, "emerg") {
		t.Fatalf("expected rollback:\n%s\n%s", m.title, m.body)
	}
	if b, _ := os.ReadFile(filepath.Join(root, dm.StoreMapFile)); string(b) != string(goodConf) {
		t.Fatalf("store map not restored:\n%s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, dm.NginxSample)); string(b) != string(goodSample) {
		t.Fatal("nginx sample not restored")
	}
}

func TestDoctorFindsAndFixes(t *testing.T) {
	a, root, msgs := envApp(t)
	// The override asks for PHP 8.3 but the running container is 8.4.
	o, _ := dm.LoadOverride(root)
	o.SetImage("phpfpm", "markoshust/magento-php:8.3-fpm-8")
	o.SetImage("phpfpm-xdebug", "markoshust/magento-php:8.3-fpm-xdebug-8")
	o.Save()
	// A store map exists but nginx.conf.sample was reset by a Magento upgrade.
	os.WriteFile(filepath.Join(root, dm.StoreMapFile), []byte("map $host $MAGE_RUN_CODE { default \"\"; }\n"), 0o644)
	os.MkdirAll(filepath.Join(root, "src"), 0o755)
	os.WriteFile(filepath.Join(root, dm.NginxSample), []byte("location ~ ^/(index|get|static|errors/report|errors/404|errors/503|health_check)\\.php$ {\n    include        fastcgi_params;\n}\n"), 0o644)

	pump(t, a, msgs, a.do(aDoctor), func() bool { d, ok := top(a).(*doctorOverlay); return ok && !d.loading && !a.doctorBusy })
	byTitle := map[string]docCheck{}
	for _, c := range a.doctor {
		byTitle[c.title] = c
	}
	php := byTitle["PHP"]
	if php.state != chkFail || !strings.Contains(php.detail, "not recreated") {
		t.Fatalf("PHP check: %+v", php)
	}
	if ms := byTitle["Multistore nginx"]; ms.state != chkFail || ms.fix == nil {
		t.Fatalf("multistore check: %+v", ms)
	}
	if c := byTitle["Magento CLI"]; c.state != chkOK {
		t.Fatalf("magento check: %+v", c)
	}
	if c := byTitle["Composer platform"]; c.state != chkOK {
		t.Fatalf("platform check: %+v", c)
	}
	if f, _ := a.doctorProblems(); f < 2 {
		t.Fatalf("problems: %d", f)
	}
	if !strings.Contains(a.viewHeader(), "problem(s) — D to fix") {
		t.Fatal("header should flag the problems")
	}

	// Fix the PHP check: recreates the containers, then re-checks.
	d := top(a).(*doctorOverlay)
	for i, c := range a.doctor {
		if c.title == "PHP" {
			d.cursor = i
		}
	}
	_, cmd := a.Update(key("enter"))
	start := a.doctorAt
	pump(t, a, msgs, cmd, func() bool { return a.doctorAt.After(start) && !a.doctorBusy })
	for _, c := range a.doctor {
		if c.title == "PHP" && c.state == chkFail {
			t.Fatalf("PHP still failing after the fix: %+v", c)
		}
	}

	// Fix the multistore wiring.
	for _, c := range a.doctor {
		if c.title == "Multistore nginx" {
			start = a.doctorAt
			pump(t, a, msgs, c.fix(a), func() bool { return a.doctorAt.After(start) && !a.doctorBusy })
		}
	}
	for _, c := range a.doctor {
		if c.title == "Multistore nginx" && c.state != chkOK {
			t.Fatalf("multistore still failing: %+v", c)
		}
	}
	_ = time.Second
}

func dmTarball(t *testing.T, compose string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	add := func(name, body string, mode int64) {
		tw.WriteHeader(&tar.Header{Name: "docker-magento-53.9.0/" + name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	add("compose/compose.yaml", compose, 0o644)
	add("compose/bin/brand-new", "#!/bin/sh\n", 0o755)
	add("compose/env/db.env", "MYSQL_PASSWORD=magento\nMYSQL_NEW_SETTING=1\n", 0o644)
	add("CHANGELOG.md", "## [53.9.0] - 2026-11-20\n- Shiny new things\n", 0o644)
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func TestDockerMagentoUpdate(t *testing.T) {
	a, root, msgs := envApp(t)
	compose := "## Version 53.9.0\nservices:\n  app:\n    image: x\n  phpfpm:\n    image: x\n  phpfpm-xdebug:\n    image: x\n  db:\n    image: mariadb\n"
	tgz := dmTarball(t, compose)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/markshust/docker-magento/releases/latest":
			w.Write([]byte(`{"tag_name":"53.9.0"}`))
		case "/markshust/docker-magento/tar.gz/refs/tags/53.9.0":
			w.Write(tgz)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	oldAPI, oldCL := dm.APIBase, dm.CodeloadURL
	dm.APIBase, dm.CodeloadURL = srv.URL, srv.URL
	defer func() { dm.APIBase, dm.CodeloadURL = oldAPI, oldCL }()
	os.MkdirAll(filepath.Join(root, "env"), 0o755)
	os.WriteFile(filepath.Join(root, "env/db.env"), []byte("MYSQL_PASSWORD=mine\n"), 0o644)
	oldCompose, _ := os.ReadFile(filepath.Join(root, "compose.yaml"))

	pump(t, a, msgs, a.do(aDMUpdate), func() bool { _, ok := top(a).(*confirmOverlay); return ok })
	c := top(a).(*confirmOverlay)
	for _, want := range []string{"unknown version → 53.9.0", "compose.yaml", "bin/brand-new", "MYSQL_NEW_SETTING", "Shiny new things"} {
		if !strings.Contains(c.body, want) {
			t.Errorf("preview missing %q:\n%s", want, c.body)
		}
	}
	_, cmd := a.Update(key("y"))
	pump(t, a, msgs, cmd, planDone(a, "Update docker-magento"))
	if m := planSummary(a, "Update docker-magento"); m.title != "Update docker-magento to 53.9.0 — done" {
		t.Fatalf("%s\n%s\n%s", m.title, m.body, outText(a))
	}
	if dm.LocalVersion(root) != "53.9.0" {
		t.Fatal("compose.yaml not updated")
	}
	env, _ := os.ReadFile(filepath.Join(root, "env/db.env"))
	if !strings.Contains(string(env), "MYSQL_PASSWORD=mine") || !strings.Contains(string(env), "MYSQL_NEW_SETTING=1") {
		t.Fatalf("env not merged: %s", env)
	}
	baks, _ := filepath.Glob(filepath.Join(root, "backups", "docker-magento-*", "compose.yaml"))
	if len(baks) != 1 {
		t.Fatal("compose.yaml not backed up")
	}
	if b, _ := os.ReadFile(baks[0]); string(b) != string(oldCompose) {
		t.Fatal("backup content")
	}
	a.overlays = nil

	// A release whose compose file breaks `up`: the previous files come back.
	before, _ := os.ReadFile(filepath.Join(root, "compose.yaml"))
	tgz = dmTarball(t, "## Version 53.9.0\n# FAILUP\nservices:\n  app:\n    image: x\n")
	pump(t, a, msgs, a.do(aDMUpdate), func() bool { _, ok := top(a).(*confirmOverlay); return ok })
	_, cmd = a.Update(key("y"))
	pump(t, a, msgs, cmd, planDone(a, "Update docker-magento"))
	if m := planSummary(a, "Update docker-magento"); !strings.Contains(m.title, "rolled back") {
		t.Fatalf("%s\n%s", m.title, m.body)
	}
	if after, _ := os.ReadFile(filepath.Join(root, "compose.yaml")); string(after) != string(before) {
		t.Fatalf("compose.yaml not restored:\n%s", after)
	}
}
