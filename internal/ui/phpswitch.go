package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"lazymage/internal/config"
	"lazymage/internal/dm"
	"lazymage/internal/docker"
	"lazymage/internal/project"
	"lazymage/internal/runner"
)

// --- PHP version switch -------------------------------------------------------------
//
// The new image is set in compose.override.yaml (it wins over compose.yaml
// and compose.versions.yaml, and bin/update keeps it). Before anything
// changes, the image is pulled and composer's platform check runs in it
// against your vendor/, so an incompatible PHP is caught up front. After the
// switch, PHP and bin/magento are verified; if either fails, the previous
// override is restored and the containers recreated again.

type phpInfoMsg struct {
	root    string
	current string // phpfpm image
	err     error
}

func (a *App) startPHPSwitch() tea.Cmd {
	b := a.builder()
	root := a.proj.Root
	status := a.setStatus("reading the current PHP image…", false)
	return tea.Batch(status, func() tea.Msg {
		out, errOut, code, err := runner.Capture(root, b.DirectCompose("config", "--format", "json"), 20*time.Second)
		if err == nil && code != 0 {
			err = errors.New(firstLine(errOut))
		}
		if err != nil {
			return phpInfoMsg{root: root, err: err}
		}
		imgs, err := dm.ServiceImages(out)
		return phpInfoMsg{root: root, current: imgs[b.PHPService], err: err}
	})
}

func (a *App) onPHPInfo(m phpInfoMsg) tea.Cmd {
	if a.proj == nil || a.proj.Root != m.root {
		return nil
	}
	if m.err != nil {
		return a.setStatus("could not read the compose config: "+m.err.Error(), true)
	}
	repo, cur := dm.SplitImage(m.current)
	if repo == "" {
		repo = dm.PHPRepo
	}
	rows := dm.ReadVersions(a.proj.Root)
	tags := dm.PHPTags(rows)
	edition := "magento"
	if b, err := os.ReadFile(filepath.Join(a.proj.Root, "src", "composer.json")); err == nil && strings.Contains(string(b), "mage-os/") {
		edition = "mage-os"
	}
	rec := dm.Recommended(rows, edition, a.facts.MagentoVersion)
	var cs []choice
	for _, t := range tags {
		t := t
		label := "PHP " + dm.PHPMinor(t) + "  (" + t + ")"
		switch {
		case t == cur:
			label += "  · current"
		case dm.PHPMinor(t) == dm.PHPMinor(cur):
			label += "  · newer build of the current PHP"
		}
		if t == rec {
			label += "  · recommended for Magento " + a.facts.MagentoVersion
		}
		cs = append(cs, choice{label: label, run: func() tea.Cmd { return a.confirmPHPSwitch(repo, cur, t) }})
	}
	cs = append(cs, choice{label: "Other tag…", run: func() tea.Cmd {
		a.push(newPrompt("PHP image tag", argPrompt{Label: "Tag of " + repo + " (e.g. 8.3-fpm-8)", Default: cur, Required: true}, func(v string) tea.Cmd {
			return a.confirmPHPSwitch(repo, cur, strings.TrimSpace(v))
		}))
		return nil
	}})
	body := "Current image: " + m.current
	if rec != "" {
		body += "\ndocker-magento recommends " + rec + " for Magento " + a.facts.MagentoVersion
	}
	c := &listPicker{title: "Switch PHP version", body: body, items: cs}
	for i, t := range tags {
		if t == rec || (rec == "" && t == cur) {
			c.cursor = i
		}
	}
	a.push(c)
	return nil
}

func (a *App) confirmPHPSwitch(repo, cur, tag string) tea.Cmd {
	if tag == cur {
		return a.setStatus("PHP is already "+tag, false)
	}
	xdebug := contains(a.services, "phpfpm-xdebug")
	body := fmt.Sprintf("%s:%s → %s:%s\n\n", repo, cur, repo, tag)
	body += "1. pull the new image" + map[bool]string{true: "s (phpfpm and phpfpm-xdebug)", false: ""}[xdebug] + "\n"
	body += "2. check composer's platform requirements in the new PHP against your vendor/\n"
	body += "   — nothing changes if they are not met\n"
	body += "3. set the image in " + dm.OverrideFile + " (kept by bin/update)\n"
	body += "4. recreate the PHP container" + map[bool]string{true: "s", false: ""}[xdebug] + ", then check PHP and bin/magento\n"
	body += "   — if either fails, the previous PHP is restored automatically\n"
	body += "5. restart cron if it was on, and flush the cache"
	a.push(newConfirm("Switch to PHP "+dm.PHPMinor(tag)+"?", body, "Switch", func() tea.Cmd {
		return a.switchPHP(repo, tag, xdebug, true)
	}))
	return nil
}

func (a *App) switchPHP(repo, tag string, xdebug, preflight bool) tea.Cmd {
	b := a.builder()
	root := a.proj.Root
	dockerBin := docker.Bin()
	php := b.PHPService
	img := repo + ":" + tag
	ximg := repo + ":" + dm.XdebugTag(tag)
	svcs := []string{php}
	if xdebug {
		svcs = append(svcs, "phpfpm-xdebug")
	}
	var prevOverride []byte
	phpContainer := ""
	for _, c := range a.containers {
		if c.Service == php && c.State == "running" {
			phpContainer = c.Name
		}
	}
	cronOn := false
	if _, err := os.Stat(filepath.Join(root, "src", "var", ".cron-enabled")); err == nil {
		cronOn = true
	}
	steps := []step{
		{label: "pull " + img, argv: []string{dockerBin, "pull", img}, kind: config.KindCompose},
	}
	if xdebug {
		steps = append(steps, step{label: "pull " + ximg, argv: []string{dockerBin, "pull", ximg}, kind: config.KindCompose})
	}
	if preflight {
		steps = append(steps, step{
			label: "platform check in PHP " + dm.PHPMinor(tag) + " (composer check-platform-reqs)",
			argv:  []string{dockerBin, "run", "--rm", "--volumes-from", phpContainer, "-w", "/var/www/html", img, "composer", "check-platform-reqs", "--no-dev"},
			kind:  config.KindCompose,
			skip:  func() bool { return phpContainer == "" },
		})
	}
	steps = append(steps,
		step{label: "set the image in " + dm.OverrideFile, changes: true, fn: func() error {
			o, err := dm.LoadOverride(root)
			if err != nil {
				return err
			}
			prevOverride = o.Original()
			if err := o.SetImage(php, img); err != nil {
				return err
			}
			if xdebug {
				if err := o.SetImage("phpfpm-xdebug", ximg); err != nil {
					return err
				}
			}
			return o.Save()
		}},
		step{label: "recreate " + strings.Join(svcs, ", "), argv: b.Compose(append([]string{"up", "-d", "--no-deps", "--force-recreate"}, svcs...)...), kind: config.KindCompose},
		step{label: "PHP " + dm.PHPMinor(tag) + " is running", fn: func() error {
			return waitPHP(root, b.DirectCompose("exec", "-T", php, "php", "-r", "echo PHP_VERSION;"), dm.PHPMinor(tag))
		}},
		step{label: "bin/magento works", argv: b.Magento("--version"), kind: config.KindMagento},
		step{label: "restart cron", argv: b.DirectCompose("exec", "-T", "-u", "root", php, "service", "cron", "start"), kind: config.KindCompose,
			optional: true, skip: func() bool { return !cronOn }},
		step{label: "flush the cache", argv: b.Magento("cache:flush"), kind: config.KindMagento, optional: true},
	)
	rollback := []step{
		{label: "restore " + dm.OverrideFile, fn: func() error { return dm.RestoreOverride(root, prevOverride) }},
		{label: "recreate " + strings.Join(svcs, ", ") + " with the previous PHP", argv: b.Compose(append([]string{"up", "-d", "--no-deps", "--force-recreate"}, svcs...)...), kind: config.KindCompose},
	}
	p := &stepPlan{title: "Switch to PHP " + dm.PHPMinor(tag), steps: steps, rollback: rollback}
	p.onDone = func(ok bool) tea.Cmd {
		cmds := []tea.Cmd{a.refresh(), a.psCmd(), a.doctorCmd(false)}
		// The platform check failed before anything changed: offer to go ahead.
		if !ok && !p.rolling && preflight && len(p.results) > 0 && strings.HasPrefix(p.results[len(p.results)-1].label, "platform check") {
			a.remove(top(a))
			a.push(newConfirm("PHP "+dm.PHPMinor(tag)+" does not meet the platform requirements",
				"composer check-platform-reqs failed in the new image — see the output (esc, then v).\n"+
					"Nothing has changed. Usually this means composer.lock needs packages that\n"+
					"don't support this PHP yet, or an extension is missing.\n\n"+
					"Switch anyway? It is still verified afterwards and rolled back if Magento breaks.",
				"Switch anyway", func() tea.Cmd { return a.switchPHP(repo, tag, xdebug, false) }))
		}
		return tea.Batch(cmds...)
	}
	return a.runPlan(p)
}

// waitPHP waits for the recreated container to run the wanted PHP.
func waitPHP(root string, argv []string, want string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var last string
	for ctx.Err() == nil {
		out, errOut, code, err := runner.Capture(root, argv, 15*time.Second)
		got := strings.TrimSpace(out)
		if err == nil && code == 0 {
			if strings.HasPrefix(got, want+".") || got == want {
				return nil
			}
			return fmt.Errorf("the container runs PHP %s, not %s", got, want)
		}
		last = firstLine(errOut)
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("the PHP container did not come up: %s", orNoOutput(last))
}

// --- docker-magento update ------------------------------------------------------------

type dmFetchMsg struct {
	root string
	up   *dm.Upstream
	plan []dm.Change
	err  error
}

func (a *App) startDMUpdate() tea.Cmd {
	root := a.proj.Root
	status := a.setStatus("checking docker-magento's newest release on GitHub…", false)
	return tea.Batch(status, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		tag, err := dm.LatestTag(ctx)
		if err != nil {
			tag = "master"
		}
		up, err := dm.Fetch(ctx, tag)
		if err != nil {
			return dmFetchMsg{root: root, err: err}
		}
		return dmFetchMsg{root: root, up: up, plan: up.Plan(root)}
	})
}

func (a *App) onDMFetch(m dmFetchMsg) tea.Cmd {
	if a.proj == nil || a.proj.Root != m.root {
		return nil
	}
	if m.err != nil {
		return a.setStatus("could not download docker-magento: "+m.err.Error(), true)
	}
	local := dm.LocalVersion(m.root)
	var newF, changed, merged, kept []string
	for _, c := range m.plan {
		switch c.Kind {
		case dm.New:
			newF = append(newF, c.Path)
		case dm.Changed:
			changed = append(changed, c.Path)
		case dm.Merged:
			merged = append(merged, c.Path+" (+"+strings.Join(c.AddKeys, ", ")+")")
		case dm.Kept:
			kept = append(kept, c.Path)
		}
	}
	if len(newF)+len(changed)+len(merged) == 0 {
		return a.setStatus("docker-magento is up to date ("+dmVer(local)+" matches "+m.up.Version+")", false)
	}
	list := func(name string, ps []string) string {
		if len(ps) == 0 {
			return ""
		}
		shown := ps
		if len(shown) > 8 {
			shown = append(append([]string(nil), ps[:8]...), fmt.Sprintf("… %d more", len(ps)-8))
		}
		return fmt.Sprintf("%s (%d): %s\n", name, len(ps), strings.Join(shown, ", "))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "docker-magento %s → %s\n\n", dmVer(local), m.up.Version)
	b.WriteString(list("Replaced", changed))
	b.WriteString(list("New", newF))
	b.WriteString(list("Env files — your values kept, new keys added", merged))
	b.WriteString(list("Kept as yours", kept))
	ts := time.Now().Format("20060102-150405")
	backup := filepath.Join("backups", "docker-magento-"+ts)
	fmt.Fprintf(&b, "\nEvery replaced file is copied to %s/ first. src/, %s and\ncompose.versions.yaml are not touched. Then images are pulled and the\ncontainers recreated; if that fails, the old files are put back.\n", backup, dm.OverrideFile)
	if contains(changed, "compose.yaml") {
		b.WriteString(sWarn.Render("\ncompose.yaml is replaced: edits you made there belong in "+dm.OverrideFile+".") + "\n")
	}
	if cl := m.up.ChangesSince(local, 14); cl != "" {
		b.WriteString("\n" + sBold.Render("What's new") + "\n" + sFaint.Render(cl) + "\n")
	}
	a.push(newConfirm("Update docker-magento?", b.String(), "Update", func() tea.Cmd {
		return a.applyDMUpdate(m.up, m.plan, filepath.Join(m.root, backup))
	}))
	return nil
}

func (a *App) applyDMUpdate(up *dm.Upstream, changes []dm.Change, backup string) tea.Cmd {
	root := a.proj.Root
	b := a.builder()
	routed := a.xdebugRouting()
	steps := []step{
		{label: "write docker-magento " + up.Version + " (backup in " + a.relPath(backup) + ")", changes: true, fn: func() error { return up.Apply(root, backup, changes) }},
		{label: "pull the images", argv: b.Compose("pull"), kind: config.KindCompose},
		{label: "recreate the containers", argv: b.Compose("up", "-d", "--remove-orphans"), kind: config.KindCompose},
		{label: "bin/setup-nginx (Xdebug cookie routing)", argv: []string{"bin/setup-nginx"}, kind: config.KindBin, optional: true,
			skip: func() bool { return routed || !a.proj.HasBin("setup-nginx") || !contains(a.services, "phpfpm-xdebug") }},
		{label: "bin/magento works", argv: b.Magento("--version"), kind: config.KindMagento, optional: true},
	}
	rollback := []step{
		{label: "put the previous files back", fn: func() error { return restoreDMFiles(root, backup, changes) }},
		{label: "recreate the containers", argv: b.Compose("up", "-d", "--remove-orphans"), kind: config.KindCompose},
	}
	return a.runPlan(&stepPlan{title: "Update docker-magento to " + up.Version, steps: steps, rollback: rollback, onDone: func(bool) tea.Cmd {
		if a.proj != nil {
			if p, ok := reloadProject(a.proj.Root); ok {
				a.proj = p
			}
		}
		return tea.Batch(a.servicesCmd(), a.refresh(), a.doctorCmd(false))
	}})
}

func restoreDMFiles(root, backup string, changes []dm.Change) error {
	var errs []error
	for _, c := range changes {
		dst := filepath.Join(root, filepath.FromSlash(c.Path))
		switch c.Kind {
		case dm.New:
			if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
		case dm.Changed, dm.Merged:
			old, err := os.ReadFile(filepath.Join(backup, filepath.FromSlash(c.Path)))
			if err != nil {
				errs = append(errs, err)
				continue
			}
			mode := os.FileMode(0o644)
			if st, err := os.Stat(dst); err == nil {
				mode = st.Mode().Perm()
			}
			errs = append(errs, dm.WriteFileAtomic(dst, old, mode))
		}
	}
	return errors.Join(errs...)
}

// listPicker is a vertical list of choices (j/k, enter).
type listPicker struct {
	title, body string
	items       []choice
	cursor      int
}

func (l *listPicker) update(a *App, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "esc", "q", "ctrl+c":
		a.remove(l)
	case "j", "down":
		l.cursor = min(l.cursor+1, len(l.items)-1)
	case "k", "up":
		l.cursor = max(l.cursor-1, 0)
	case "enter":
		a.remove(l)
		if r := l.items[l.cursor].run; r != nil {
			return r()
		}
	}
	return nil
}

func (l *listPicker) view(a *App, w, h int) string {
	bw := min(w-4, 100)
	lines := []string{l.body, ""}
	for i, it := range l.items {
		if i == l.cursor {
			lines = append(lines, sSelected.Render("▶ "+it.label))
		} else {
			lines = append(lines, "  "+it.label)
		}
	}
	return overlayBox(l.title, strings.Join(clipLines(lines, h-4), "\n"), bw)
}

func (l *listPicker) hints() string {
	return keyHint("j/k", "move", "enter", "choose", "esc", "cancel")
}

func reloadProject(root string) (*project.Project, bool) { return project.FindRoot(root) }

func dmVer(s string) string {
	if s == "" {
		return "unknown version"
	}
	return s
}
