package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"lazymage/internal/config"
	"lazymage/internal/dm"
	"lazymage/internal/docker"
	"lazymage/internal/frontend"
	"lazymage/internal/plan"
	"lazymage/internal/runner"
	"lazymage/internal/update"
)

// The doctor checks what usually breaks a docker-magento environment after
// a change (PHP version, docker-magento update, new store domains) and
// offers a fix for each problem. A quick pass runs in the background at
// start-up; D runs the full set.

const (
	chkOK      = "ok"
	chkWarn    = "warn"
	chkFail    = "fail"
	chkUnknown = "unknown"
)

type docCheck struct {
	title    string
	state    string
	detail   string
	fixLabel string
	fix      func(a *App) tea.Cmd
}

type doctorMsg struct {
	root   string
	quick  bool
	checks []docCheck
}

// doctorInput is what the checks need, copied so they can run off the UI thread.
type doctorInput struct {
	root       string
	b          plan.Builder
	services   []string
	containers []docker.Container
	magento    string
	cacheDir   string
	quick      bool
}

func (a *App) doctorCmd(quick bool) tea.Cmd {
	if a.proj == nil || a.engine == engineStopped {
		return nil
	}
	if a.doctorBusy {
		return nil
	}
	a.doctorBusy = true
	in := doctorInput{
		root: a.proj.Root, b: a.builder(), services: append([]string(nil), a.services...),
		containers: append([]docker.Container(nil), a.containers...), magento: a.facts.MagentoVersion,
		cacheDir: config.Dir(), quick: quick,
	}
	return func() tea.Msg { return doctorMsg{root: in.root, quick: quick, checks: runDoctor(in)} }
}

func (a *App) onDoctor(m doctorMsg) tea.Cmd {
	a.doctorBusy = false
	if a.proj == nil || a.proj.Root != m.root {
		return nil
	}
	a.doctor = m.checks
	a.doctorAt = time.Now()
	if d, ok := top(a).(*doctorOverlay); ok {
		d.loading = false
		d.cursor = min(d.cursor, max(len(a.doctor)-1, 0))
	}
	return nil
}

// doctorProblems counts failed and warning checks from the last run.
func (a *App) doctorProblems() (fails, warns int) {
	for _, c := range a.doctor {
		switch c.state {
		case chkFail:
			fails++
		case chkWarn:
			warns++
		}
	}
	return
}

func runDoctor(in doctorInput) []docCheck {
	var out []docCheck
	// Read the containers fresh: the UI's copy may predate a restart.
	if ps, _, code, err := runner.Capture(in.root, in.b.DirectCompose("ps", "-a", "--format", "json"), 20*time.Second); err == nil && code == 0 {
		if cs, err := docker.ParsePSJSON(ps); err == nil {
			in.containers = cs
		}
	}
	running := map[string]docker.Container{}
	for _, c := range in.containers {
		running[c.Service] = c
	}
	has := func(svc string) bool {
		for _, s := range in.services {
			if s == svc {
				return true
			}
		}
		return false
	}
	phpUp := docker.Up(running[in.b.PHPService])

	out = append(out, checkDMVersion(in))
	out = append(out, checkContainers(in))
	out = append(out, checkPHP(in, phpUp, has("phpfpm-xdebug")))
	if !in.quick && phpUp {
		out = append(out, checkMagentoCLI(in))
		out = append(out, checkPlatform(in))
	}
	if c, ok := checkVersionPins(in); ok {
		out = append(out, c)
	}
	if has("phpfpm-xdebug") {
		out = append(out, checkXdebugRouting(in))
	}
	var stores []frontend.Store
	if running[in.b.DBService].State == "running" || in.b.DBService == "" {
		stores = doctorStores(in)
	}
	out = append(out, checkMultistore(in, stores)...)
	return out
}

func checkDMVersion(in doctorInput) docCheck {
	c := docCheck{title: "docker-magento version"}
	local := dm.LocalVersion(in.root)
	cachePath := filepath.Join(in.cacheDir, "docker-magento-latest.json")
	cache := update.ReadCache(cachePath)
	latest := cache.Latest
	if latest == "" || time.Since(cache.Checked) > 12*time.Hour || !in.quick {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		tag, err := dm.LatestTag(ctx)
		cancel()
		if err == nil {
			latest = tag
			update.WriteCache(cachePath, update.Cache{Checked: time.Now(), Latest: tag})
		} else if latest == "" {
			c.state, c.detail = chkUnknown, "could not reach GitHub: "+err.Error()
			return c
		}
	}
	c.fixLabel, c.fix = "Update docker-magento…", func(a *App) tea.Cmd { return a.do(aDMUpdate) }
	switch {
	case local == "":
		c.state, c.detail = chkWarn, "compose.yaml has no \"## Version\" header — an old copy; newest is "+latest
	case update.Newer(latest, local):
		c.state, c.detail = chkWarn, fmt.Sprintf("%s installed, %s available", local, latest)
	default:
		c.state, c.detail, c.fix, c.fixLabel = chkOK, local+" (newest)", nil, ""
	}
	return c
}

func checkContainers(in doctorInput) docCheck {
	c := docCheck{title: "Containers"}
	if len(in.containers) == 0 {
		c.state, c.detail = chkFail, "no containers — the environment is not started"
		c.fixLabel, c.fix = "Start environment", func(a *App) tea.Cmd { return a.do(aStackStart) }
		return c
	}
	var bad []string
	var badSvc []string
	for _, ct := range in.containers {
		if ct.State == "running" && ct.Health != "unhealthy" {
			continue
		}
		s := ct.Service + " " + ct.State
		if ct.Health == "unhealthy" {
			s = ct.Service + " unhealthy"
		}
		bad = append(bad, s)
		badSvc = append(badSvc, ct.Service)
	}
	up := map[string]bool{}
	for _, ct := range in.containers {
		up[ct.Service] = true
	}
	for _, s := range in.services {
		if !up[s] {
			bad = append(bad, s+" missing")
		}
	}
	if len(bad) == 0 {
		c.state, c.detail = chkOK, fmt.Sprintf("all %d running", len(in.containers))
		return c
	}
	sort.Strings(bad)
	c.state, c.detail = chkFail, strings.Join(bad, ", ")
	// The last log lines usually say why a container stopped.
	if len(badSvc) > 0 {
		out, _, _, _ := runner.Capture(in.root, in.b.DirectCompose("logs", "--no-color", "--tail", "3", badSvc[0]), 10*time.Second)
		if l := lastLine(out); l != "" {
			c.detail += " — " + badSvc[0] + ": " + l
		}
	}
	c.fixLabel, c.fix = "Start / recreate the containers", func(a *App) tea.Cmd {
		return a.startRun(a.builder().Compose("up", "-d", "--remove-orphans"), runOpts{kind: config.KindCompose, lifecycle: true, onExit: func(runner.Exited) tea.Cmd { return a.doctorCmd(false) }})
	}
	return c
}

func lastLine(s string) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(ls) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(ls[i]); l != "" {
			if _, rest, ok := strings.Cut(l, "| "); ok {
				l = strings.TrimSpace(rest)
			}
			return truncate(l, 120)
		}
	}
	return ""
}

// configuredImages are the images after all compose files are merged.
func configuredImages(in doctorInput) (map[string]string, error) {
	out, errOut, code, err := runner.Capture(in.root, in.b.DirectCompose("config", "--format", "json"), 20*time.Second)
	if err == nil && code != 0 {
		err = fmt.Errorf("%s", firstLine(errOut))
	}
	if err != nil {
		return nil, err
	}
	return dm.ServiceImages(out)
}

func phpRunning(in doctorInput, svc string) (string, error) {
	out, errOut, code, err := runner.Capture(in.root, in.b.DirectCompose("exec", "-T", svc, "php", "-r", "echo PHP_VERSION;"), 20*time.Second)
	if err == nil && code != 0 {
		err = fmt.Errorf("%s", orNoOutput(firstLine(errOut)))
	}
	return strings.TrimSpace(out), err
}

func checkPHP(in doctorInput, phpUp, xdebug bool) docCheck {
	c := docCheck{title: "PHP", fixLabel: "Switch PHP version…", fix: func(a *App) tea.Cmd { return a.do(aPHPSwitch) }}
	imgs, err := configuredImages(in)
	if err != nil {
		c.state, c.detail = chkUnknown, "could not read the compose config: "+err.Error()
		return c
	}
	_, tag := dm.SplitImage(imgs[in.b.PHPService])
	want := dm.PHPMinor(tag)
	c.detail = "image " + tag
	if !phpUp {
		c.state = chkUnknown
		c.detail += " · " + in.b.PHPService + " is not running"
		return c
	}
	got, err := phpRunning(in, in.b.PHPService)
	if err != nil {
		c.state, c.detail = chkFail, "php does not run in "+in.b.PHPService+": "+err.Error()
		return c
	}
	c.detail = "PHP " + got + " · image " + tag
	recreate := func(a *App) tea.Cmd {
		svcs := []string{a.builder().PHPService}
		if xdebug {
			svcs = append(svcs, "phpfpm-xdebug")
		}
		args := append([]string{"up", "-d", "--no-deps"}, svcs...)
		return a.startRun(a.builder().Compose(args...), runOpts{kind: config.KindCompose, onExit: func(runner.Exited) tea.Cmd { return a.doctorCmd(false) }})
	}
	if want != "" && !strings.HasPrefix(got, want+".") {
		c.state = chkFail
		c.detail = fmt.Sprintf("running PHP %s but the compose files ask for %s — the container was not recreated", got, tag)
		c.fixLabel, c.fix = "Recreate the PHP containers", recreate
		return c
	}
	if xdebug {
		_, xtag := dm.SplitImage(imgs["phpfpm-xdebug"])
		if xm := dm.PHPMinor(xtag); xm != "" && want != "" && xm != want {
			c.state = chkFail
			c.detail = fmt.Sprintf("phpfpm runs PHP %s but phpfpm-xdebug is set to %s — debugging would use another PHP", want, xm)
			c.fixLabel = "Switch PHP version… (sets both)"
			return c
		}
	}
	edition := "magento"
	if b, err := os.ReadFile(filepath.Join(in.root, "src", "composer.json")); err == nil && strings.Contains(string(b), "mage-os/") {
		edition = "mage-os"
	}
	if rec := dm.Recommended(dm.ReadVersions(in.root), edition, in.magento); rec != "" && dm.PHPMinor(rec) != want && want != "" {
		c.state = chkWarn
		c.detail += fmt.Sprintf(" · docker-magento recommends PHP %s for %s", dm.PHPMinor(rec), in.magento)
		return c
	}
	c.state = chkOK
	return c
}

func checkMagentoCLI(in doctorInput) docCheck {
	c := docCheck{title: "Magento CLI"}
	out, errOut, code, err := runner.Capture(in.root, in.b.Magento("--version"), 60*time.Second)
	if err == nil && code == 0 {
		c.state, c.detail = chkOK, strings.TrimSpace(firstLine(out))
		return c
	}
	msg := firstLine(errOut)
	if msg == "" {
		msg = firstLine(out)
	}
	if err != nil {
		msg = err.Error()
	}
	c.state, c.detail = chkFail, "bin/magento fails: "+orNoOutput(msg)
	if strings.Contains(out+errOut, "vendor/autoload.php") || strings.Contains(out+errOut, "Composer detected issues") {
		c.fixLabel, c.fix = "composer install", func(a *App) tea.Cmd {
			return a.startRun(a.builder().Composer("install"), runOpts{kind: config.KindComposer, onExit: func(runner.Exited) tea.Cmd { return a.doctorCmd(false) }})
		}
	} else {
		c.fixLabel, c.fix = "Show the full error", func(a *App) tea.Cmd {
			return a.startRun(a.builder().Magento("--version"), runOpts{kind: config.KindMagento})
		}
	}
	return c
}

func checkPlatform(in doctorInput) docCheck {
	c := docCheck{title: "Composer platform"}
	out, errOut, code, err := runner.Capture(in.root, in.b.Composer("check-platform-reqs", "--no-dev"), 90*time.Second)
	if err != nil {
		c.state, c.detail = chkUnknown, err.Error()
		return c
	}
	if code == 0 {
		c.state, c.detail = chkOK, "PHP and its extensions meet composer.lock"
		return c
	}
	var failing []string
	for _, l := range strings.Split(out+"\n"+errOut, "\n") {
		if strings.Contains(l, "failed") || strings.Contains(l, "missing") {
			f := strings.Fields(l)
			if len(f) > 0 {
				failing = append(failing, f[0])
			}
		}
	}
	c.state = chkFail
	c.detail = "not met: " + orNoOutput(strings.Join(failing, ", "))
	c.fixLabel, c.fix = "Switch PHP version…", func(a *App) tea.Cmd { return a.do(aPHPSwitch) }
	return c
}

func checkVersionPins(in doctorInput) (docCheck, bool) {
	if in.b.Mode == config.ModeDockerOnly {
		return docCheck{}, false
	}
	if _, err := os.Stat(filepath.Join(in.root, "bin", "detect-versions")); err != nil {
		return docCheck{}, false
	}
	c := docCheck{title: "Image pins"}
	out, errOut, code, err := runner.Capture(in.root, []string{"bin/detect-versions", "--check"}, 30*time.Second)
	switch {
	case err != nil:
		c.state, c.detail = chkUnknown, err.Error()
	case code == 0:
		c.state, c.detail = chkOK, "compose.versions.yaml matches your Magento version"
	default:
		c.state = chkWarn
		c.detail = "compose.versions.yaml is missing or out of date: " + orNoOutput(firstLine(stripANSI(errOut+out)))
		c.fixLabel, c.fix = "Regenerate pins (bin/detect-versions) and recreate", func(a *App) tea.Cmd {
			b := a.builder()
			return a.runPlan(&stepPlan{title: "Regenerate image pins", steps: []step{
				{label: "bin/detect-versions", argv: []string{"bin/detect-versions"}, kind: config.KindBin},
				{label: "Pull images", argv: b.Compose("pull"), kind: config.KindCompose},
				{label: "Recreate containers", argv: b.Compose("up", "-d", "--remove-orphans"), kind: config.KindCompose},
			}, onDone: func(bool) tea.Cmd { return tea.Batch(a.refresh(), a.doctorCmd(false)) }})
		}
	}
	return c, true
}

func checkXdebugRouting(in doctorInput) docCheck {
	c := docCheck{title: "Xdebug routing"}
	b, err := os.ReadFile(filepath.Join(in.root, dm.NginxSample))
	if err != nil {
		c.state, c.detail = chkUnknown, dm.NginxSample+" not found"
		return c
	}
	if strings.Contains(string(b), "fastcgi_pass $fastcgi_backend") {
		c.state, c.detail = chkOK, "XDEBUG_SESSION requests go to phpfpm-xdebug"
		return c
	}
	c.state, c.detail = chkWarn, "nginx.conf.sample does not route the Xdebug cookie (Magento upgrades reset it)"
	if _, err := os.Stat(filepath.Join(in.root, "bin", "setup-nginx")); err == nil {
		c.fixLabel, c.fix = "Run bin/setup-nginx", func(a *App) tea.Cmd {
			return a.startRun([]string{"bin/setup-nginx"}, runOpts{kind: config.KindBin, onExit: func(runner.Exited) tea.Cmd { return a.doctorCmd(false) }})
		}
	}
	return c
}

func doctorStores(in doctorInput) []frontend.Store {
	prefix := ""
	if in.b.P != nil {
		prefix = in.b.P.TablePrefix()
	}
	out, _, code, err := runner.Capture(in.root, in.b.DBQuery(frontend.StoresQuery(prefix)), 30*time.Second)
	if err != nil || code != 0 {
		return nil
	}
	return frontend.ParseStores(out)
}

func storeHosts(stores []frontend.Store) []string {
	seen := map[string]bool{}
	var hosts []string
	for _, s := range stores {
		if h := frontend.Host(s.URL); h != "" && !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	return hosts
}

func checkMultistore(in doctorInput, stores []frontend.Store) []docCheck {
	var out []docCheck
	hosts := storeHosts(stores)

	// nginx wiring, only when a store map exists.
	if _, err := os.Stat(filepath.Join(in.root, dm.StoreMapFile)); err == nil {
		c := docCheck{title: "Multistore nginx"}
		var missing []string
		if o, err := dm.LoadOverride(in.root); err != nil || !o.HasVolume("app", dm.StoreMapVolume) {
			missing = append(missing, dm.OverrideFile+" does not mount "+dm.StoreMapFile)
		}
		if b, err := os.ReadFile(filepath.Join(in.root, dm.NginxSample)); err != nil || !dm.HasRunParams(string(b)) {
			missing = append(missing, "nginx.conf.sample does not pass MAGE_RUN_CODE (Magento upgrades reset it)")
		}
		if len(missing) == 0 {
			c.state, c.detail = chkOK, dm.StoreMapFile+" is mounted and passed to PHP"
		} else {
			c.state, c.detail = chkFail, strings.Join(missing, "; ")
			c.fixLabel, c.fix = "Repair the multistore wiring", func(a *App) tea.Cmd { return a.repairMultistore() }
		}
		out = append(out, c)
	}
	if len(hosts) == 0 {
		return out
	}

	// Host names.
	var unresolved, notWin []string
	wsl := dm.IsWSL()
	for _, h := range hosts {
		if !dm.Resolves(h) {
			unresolved = append(unresolved, h)
		}
		if wsl && !strings.HasSuffix(h, ".localhost") && !dm.HostsHas(dm.WindowsHostsFile, h) {
			notWin = append(notWin, h)
		}
	}
	hc := docCheck{title: "Store domains"}
	switch {
	case len(unresolved) > 0:
		hc.state, hc.detail = chkFail, "not in /etc/hosts: "+strings.Join(unresolved, ", ")
		hc.fixLabel, hc.fix = "Add them to /etc/hosts (asks for your password)", func(a *App) tea.Cmd { return a.addHosts(unresolved) }
	case len(notWin) > 0:
		hc.state = chkWarn
		hc.detail = "your Windows browser reads the Windows hosts file, which lacks: " + strings.Join(notWin, ", ")
		hc.fixLabel, hc.fix = "Show the lines to add", func(a *App) tea.Cmd { return a.showWindowsHosts(notWin) }
	default:
		hc.state, hc.detail = chkOK, strings.Join(hosts, ", ")
	}
	out = append(out, hc)

	// Certificate.
	cc := docCheck{title: "SSL certificate"}
	var uncovered []string
	var dialErr error
	for _, h := range hosts {
		names, err := dm.CertHosts(h)
		if err != nil {
			dialErr = err
			break
		}
		if !dm.CertCovers(names, h) {
			uncovered = append(uncovered, h)
		}
	}
	switch {
	case dialErr != nil:
		cc.state, cc.detail = chkUnknown, "could not reach nginx on 127.0.0.1:443"
	case len(uncovered) > 0:
		cc.state, cc.detail = chkFail, "the certificate does not cover "+strings.Join(uncovered, ", ")
		all := hosts
		cc.fixLabel, cc.fix = "bin/setup-ssl for every store domain (restarts the stack)", func(a *App) tea.Cmd {
			return a.startRun(append([]string{"bin/setup-ssl"}, all...), runOpts{kind: config.KindBin, lifecycle: true, onExit: func(runner.Exited) tea.Cmd { return a.doctorCmd(false) }})
		}
	default:
		cc.state, cc.detail = chkOK, "covers every store domain"
	}
	out = append(out, cc)
	return out
}

// --- overlay ---------------------------------------------------------------------

type doctorOverlay struct {
	cursor  int
	loading bool
}

func (a *App) openDoctor() tea.Cmd {
	d := &doctorOverlay{loading: true}
	a.push(d)
	if cmd := a.doctorCmd(false); cmd != nil {
		return cmd
	}
	d.loading = a.doctorBusy
	return nil
}

func (d *doctorOverlay) update(a *App, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "esc", "q", "ctrl+c":
		a.remove(d)
	case "j", "down":
		d.cursor = min(d.cursor+1, max(len(a.doctor)-1, 0))
	case "k", "up":
		d.cursor = max(d.cursor-1, 0)
	case "r":
		d.loading = true
		return a.doctorCmd(false)
	case "enter", "f":
		if d.loading || d.cursor >= len(a.doctor) {
			return nil
		}
		c := a.doctor[d.cursor]
		if c.fix == nil {
			return a.setStatus("nothing to fix here", false)
		}
		if a.run != nil {
			return a.setStatus("a command is already running (ctrl+c cancels it)", true)
		}
		a.remove(d)
		return c.fix(a)
	}
	return nil
}

func (d *doctorOverlay) view(a *App, w, h int) string {
	bw := min(w-2, 120)
	var lines []string
	if d.loading {
		lines = append(lines, sWarn.Render("● checking… (composer and bin/magento checks take a few seconds)"), "")
	}
	for i, c := range a.doctor {
		mark := sOK.Render("✓")
		switch c.state {
		case chkFail:
			mark = sErr.Render("✗")
		case chkWarn:
			mark = sWarn.Render("◐")
		case chkUnknown:
			mark = sFaint.Render("?")
		}
		row := fmt.Sprintf("%s %-22s %s", mark, c.title, c.detail)
		if i == d.cursor {
			lines = append(lines, sSelected.Render(truncate("▶ "+stripANSI(row), bw-4)))
			if c.fix != nil {
				lines = append(lines, "    "+sAccent.Render("enter → "+c.fixLabel))
			}
		} else {
			lines = append(lines, truncateStyled("  "+row, bw-4))
		}
	}
	if len(a.doctor) == 0 && !d.loading {
		lines = append(lines, sFaint.Render("no results yet — r runs the checks"))
	}
	if !a.doctorAt.IsZero() && !d.loading {
		f, w := a.doctorProblems()
		lines = append(lines, "", sFaint.Render(fmt.Sprintf("%d problem(s), %d warning(s) · checked %s ago", f, w, fmtDur(time.Since(a.doctorAt)))))
	}
	return overlayBox("Doctor", strings.Join(clipLines(lines, h-4), "\n"), bw)
}

func (d *doctorOverlay) hints() string {
	return keyHint("enter", "fix", "j/k", "move", "r", "re-check", "esc", "close")
}

// clipLines keeps at most n lines, noting how many were cut.
func clipLines(lines []string, n int) []string {
	n = max(n, 3)
	if len(lines) <= n {
		return lines
	}
	return append(lines[:n-1:n-1], sFaint.Render(fmt.Sprintf("… %d more lines", len(lines)-n+1)))
}

// again re-runs the checks even if they ran before (after a start / restart).
type quickDoctorMsg struct{ again bool }

// quickDoctorTick runs the quick checks once the first status poll is in.
func quickDoctorTick() tea.Cmd {
	return tea.Tick(6*time.Second, func(time.Time) tea.Msg { return quickDoctorMsg{} })
}
