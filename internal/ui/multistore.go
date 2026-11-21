package ui

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"lazymage/internal/config"
	"lazymage/internal/dm"
	"lazymage/internal/frontend"
	"lazymage/internal/runner"
)

// Multistore setup: give store views their own domains. Applying it does
// what docker-magento's README describes, plus the parts it leaves to you:
// base URLs per store (bin/magento config:set), store.map.conf mounted from
// compose.override.yaml, MAGE_RUN_CODE / MAGE_RUN_TYPE passed by
// nginx.conf.sample, /etc/hosts entries, and a certificate for every domain
// (bin/setup-ssl). If nginx rejects the result, the files are put back.

type msOverlay struct {
	stores  []frontend.Store
	domains []string // desired host per store ("" = keep its current URL)
	cursor  int
	err     string
}

// etcHosts is the hosts file checked and appended to (tests point it elsewhere).
var etcHosts = "/etc/hosts"

var hostRe = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

func (a *App) openMultistore() tea.Cmd {
	if a.fe != nil && a.fe.loaded && len(a.fe.stores) > 0 && a.fe.root == a.proj.Root {
		a.push(newMSOverlay(a.fe.stores))
		return nil
	}
	status := a.setStatus("reading the store views…", false)
	return tea.Batch(status, a.dbQuery(frontend.StoresQuery(a.proj.TablePrefix()), func(out string, err error) tea.Cmd {
		if err != nil {
			return a.setStatus("could not read the stores: "+err.Error(), true)
		}
		ss := frontend.ParseStores(out)
		if len(ss) == 0 {
			return a.setStatus("the database has no store views — is Magento installed?", true)
		}
		a.push(newMSOverlay(ss))
		return nil
	}))
}

func newMSOverlay(ss []frontend.Store) *msOverlay {
	o := &msOverlay{stores: append([]frontend.Store(nil), ss...)}
	for _, s := range ss {
		o.domains = append(o.domains, frontend.Host(s.URL))
	}
	return o
}

func (o *msOverlay) sites() []dm.Site {
	var out []dm.Site
	for i, s := range o.stores {
		out = append(out, dm.Site{StoreCode: s.Code, WebsiteCode: s.Website, Default: s.Default, Host: o.domains[i]})
	}
	return out
}

// changed lists the stores whose domain differs from their current URL.
func (o *msOverlay) changed() []int {
	var out []int
	for i, s := range o.stores {
		if o.domains[i] != "" && o.domains[i] != frontend.Host(s.URL) {
			out = append(out, i)
		}
	}
	return out
}

func (o *msOverlay) update(a *App, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	o.err = ""
	switch k.String() {
	case "esc", "q", "ctrl+c":
		a.remove(o)
	case "j", "down":
		o.cursor = min(o.cursor+1, len(o.stores)-1)
	case "k", "up":
		o.cursor = max(o.cursor-1, 0)
	case "enter", "e":
		i := o.cursor
		s := o.stores[i]
		a.push(newPrompt("Domain for store "+s.Code, argPrompt{
			Label:   fmt.Sprintf("Host name for %s (%s, website %s), e.g. %s.%s", s.Code, s.Name, s.Website, s.Code, suggestBase(o.domains)),
			Default: o.domains[i], Required: true,
		}, func(v string) tea.Cmd {
			v = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(v, "https://"), "http://"), "/")))
			if !hostRe.MatchString(v) {
				o.err = v + " is not a host name (letters, digits, dashes and dots, like fr.magento.test)"
				return nil
			}
			o.domains[i] = v
			return nil
		}))
	case "W":
		// Give every store of the selected website the selected store's domain.
		cur := o.stores[o.cursor]
		for i, s := range o.stores {
			if s.WebsiteID == cur.WebsiteID {
				o.domains[i] = o.domains[o.cursor]
			}
		}
	case "u":
		i := o.cursor
		o.domains[i] = frontend.Host(o.stores[i].URL)
	case "a":
		if _, problems := dm.Mappings(o.sites()); len(problems) > 0 {
			o.err = problems[0]
			return nil
		}
		a.remove(o)
		return a.confirmMultistore(o)
	}
	return nil
}

func suggestBase(domains []string) string {
	for _, d := range domains {
		if d != "" {
			return d
		}
	}
	return "magento.test"
}

func (o *msOverlay) view(a *App, w, h int) string {
	bw := min(w-2, 130)
	lines := []string{
		sFaint.Render("Give store views their own domains. Stores of one website may share a domain (store"),
		sFaint.Render("codes in URLs, or the website's default store). Apply writes everything docker-magento needs."),
		"",
	}
	codeW, webW := 8, 8
	for _, s := range o.stores {
		codeW, webW = max(codeW, len(s.Code)), max(webW, len(s.Website))
	}
	lines = append(lines, sBold.Render(fmt.Sprintf("  %-*s %-*s %-34s %s", codeW, "store", webW, "website", "current URL", "domain")))
	for i, s := range o.stores {
		cur := s.URL
		if cur == "" {
			cur = "—"
		}
		dom := o.domains[i]
		if dom != frontend.Host(s.URL) {
			dom = sWarn.Render(dom + " (new)")
		}
		tag := ""
		if s.Default {
			tag = sFaint.Render(" default")
		}
		row := fmt.Sprintf("%-*s %-*s %-34s %s%s", codeW, s.Code, webW, s.Website, truncate(cur, 34), dom, tag)
		if i == o.cursor {
			lines = append(lines, sSelected.Render(truncate("▶ "+stripANSI(row), bw-4)))
		} else {
			lines = append(lines, truncateStyled("  "+row, bw-4))
		}
	}
	ms, problems := dm.Mappings(o.sites())
	lines = append(lines, "", sBold.Render("nginx run codes")+sFaint.Render(" ("+dm.StoreMapFile+")"))
	if len(ms) == 0 {
		lines = append(lines, sFaint.Render("  none — every store is on the default domain"))
	}
	for _, m := range ms {
		lines = append(lines, fmt.Sprintf("  %-34s → %s %s", m.Host, m.Type, m.Code))
	}
	for _, p := range problems {
		lines = append(lines, sErr.Render("  ! "+p))
	}
	if o.err != "" {
		lines = append(lines, "", sErr.Render("! "+o.err))
	}
	return overlayBox("Multistore setup", strings.Join(clipLines(lines, h-4), "\n"), bw)
}

func (o *msOverlay) hints() string {
	return keyHint("enter/e", "set domain", "W", "same for website", "u", "undo", "a", "apply…", "esc", "close")
}

// --- apply ---------------------------------------------------------------------------

// msFiles is the content Apply writes, and what was there before.
type msFiles struct {
	mapConf      string
	prevMap      []byte
	prevOverride []byte
	prevSample   []byte
	hadMap       bool
	hadSample    bool
}

func (a *App) confirmMultistore(o *msOverlay) tea.Cmd {
	ms, _ := dm.Mappings(o.sites())
	var hosts []string
	seen := map[string]bool{}
	for _, d := range o.domains {
		if d != "" && !seen[d] {
			seen[d] = true
			hosts = append(hosts, d)
		}
	}
	var b strings.Builder
	b.WriteString("This will:\n")
	for _, i := range o.changed() {
		fmt.Fprintf(&b, "  • set the base URLs of store %s to https://%s/\n", o.stores[i].Code, o.domains[i])
	}
	fmt.Fprintf(&b, "  • write %s (%d host mapping(s)) and mount it from %s\n", dm.StoreMapFile, len(ms), dm.OverrideFile)
	fmt.Fprintf(&b, "  • pass MAGE_RUN_CODE / MAGE_RUN_TYPE in %s\n", dm.NginxSample)
	var missing []string
	for _, h := range hosts {
		if !dm.HostsHas(etcHosts, h) {
			missing = append(missing, h)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(&b, "  • add %s to /etc/hosts (sudo asks for your password)\n", strings.Join(missing, ", "))
	}
	fmt.Fprintf(&b, "  • make sure the certificate covers every domain (bin/setup-ssl, restarts the stack)\n")
	b.WriteString("  • check the nginx config — if it is rejected, the files are put back\n")
	b.WriteString("  • flush the cache and open each domain to check it answers\n")
	if dm.IsWSL() {
		b.WriteString("\n" + sWarn.Render("WSL: your Windows browser also needs the domains in the Windows hosts file —\nthe summary lists the lines."))
	}
	a.push(newConfirm("Apply the multistore setup?", b.String(), "Apply", func() tea.Cmd {
		return a.applyMultistore(o, ms, hosts)
	}))
	return nil
}

func (a *App) applyMultistore(o *msOverlay, ms []dm.RunMapping, hosts []string) tea.Cmd {
	b := a.builder()
	root := a.proj.Root
	f := &msFiles{mapConf: dm.StoreMapConf(ms)}
	var steps []step
	for _, i := range o.changed() {
		s, host := o.stores[i], o.domains[i]
		u := "https://" + host + "/"
		// The default store's domain is the default scope's, like bin/setup-domain sets it.
		scope := []string{"--scope=stores", "--scope-code=" + s.Code}
		if s.Default {
			scope = nil
		}
		for _, path := range []string{"web/secure/base_url", "web/unsecure/base_url"} {
			args := append(append([]string{"config:set"}, scope...), path, u)
			steps = append(steps, step{label: path + " of " + s.Code + " → " + u, argv: b.Magento(args...), kind: config.KindMagento})
		}
	}
	steps = append(steps, step{label: "write " + dm.StoreMapFile + ", " + dm.OverrideFile + ", nginx.conf.sample", changes: true, fn: func() error {
		return writeMultistoreFiles(root, f)
	}})
	steps = append(steps, a.hostsSteps(hosts)...)
	sslNeeded := false
	steps = append(steps,
		step{label: "check the certificate covers every domain", fn: func() error {
			sslNeeded = false
			for _, h := range hosts {
				names, err := dm.CertHosts(h)
				if err != nil || !dm.CertCovers(names, h) {
					sslNeeded = true
					break
				}
			}
			return nil
		}},
		step{label: "bin/setup-ssl " + strings.Join(hosts, " "), argv: append([]string{"bin/setup-ssl"}, hosts...), kind: config.KindBin,
			skip: func() bool { return !sslNeeded || !a.proj.HasBin("setup-ssl") }},
		step{label: "recreate the app container with the store map", argv: b.Compose("up", "-d", "--no-deps", "app"), kind: config.KindCompose,
			skip: func() bool { return sslNeeded && a.proj.HasBin("setup-ssl") }},
		step{label: "nginx accepts the config", fn: func() error { return waitNginxOK(root, b.DirectCompose("exec", "-T", "app", "nginx", "-t")) }},
		step{label: "flush the cache", argv: b.Magento("cache:flush"), kind: config.KindMagento, optional: true},
		step{label: "each domain answers", fn: func() error { return checkDomains(hosts) }, optional: true},
	)
	rollback := []step{
		{label: "restore the previous files", fn: func() error { return restoreMultistoreFiles(root, f) }},
		{label: "recreate the app container", argv: b.Compose("up", "-d", "--no-deps", "app"), kind: config.KindCompose},
	}
	return a.runPlan(&stepPlan{title: "Multistore setup", steps: steps, rollback: rollback, onDone: func(ok bool) tea.Cmd {
		cmds := []tea.Cmd{a.feRefresh(true), a.refresh(), a.doctorCmd(false)}
		if ok && dm.IsWSL() {
			cmds = append(cmds, a.showWindowsHosts(hosts))
		}
		return tea.Batch(cmds...)
	}})
}

func writeMultistoreFiles(root string, f *msFiles) error {
	mapPath := filepath.Join(root, dm.StoreMapFile)
	if b, err := os.ReadFile(mapPath); err == nil {
		f.prevMap, f.hadMap = b, true
	}
	ov, err := dm.LoadOverride(root)
	if err != nil {
		return err
	}
	f.prevOverride = ov.Original()
	samplePath := filepath.Join(root, dm.NginxSample)
	sample, err := os.ReadFile(samplePath)
	if err != nil {
		return fmt.Errorf("%s not found — is Magento installed in src/?", dm.NginxSample)
	}
	f.prevSample, f.hadSample = sample, true
	patched, err := dm.AddRunParams(string(sample))
	if err != nil {
		return err
	}
	if err := ov.AddVolume("app", dm.StoreMapVolume); err != nil {
		return err
	}
	if err := dm.WriteFileAtomic(mapPath, []byte(f.mapConf), 0o644); err != nil {
		return err
	}
	if err := ov.Save(); err != nil {
		return err
	}
	if patched != string(sample) {
		if err := os.WriteFile(samplePath+".lazymage.bak", sample, 0o644); err != nil {
			return err
		}
		if err := dm.WriteFileAtomic(samplePath, []byte(patched), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func restoreMultistoreFiles(root string, f *msFiles) error {
	var errs []error
	mapPath := filepath.Join(root, dm.StoreMapFile)
	if f.hadMap {
		errs = append(errs, dm.WriteFileAtomic(mapPath, f.prevMap, 0o644))
	} else if err := os.Remove(mapPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err)
	}
	errs = append(errs, dm.RestoreOverride(root, f.prevOverride))
	if f.hadSample {
		errs = append(errs, dm.WriteFileAtomic(filepath.Join(root, dm.NginxSample), f.prevSample, 0o644))
	}
	return errors.Join(errs...)
}

// waitNginxOK runs nginx -t, retrying while the app container starts.
func waitNginxOK(root string, argv []string) error {
	var last string
	for i := 0; i < 15; i++ {
		out, errOut, code, err := runner.Capture(root, argv, 20*time.Second)
		if err == nil && code == 0 {
			return nil
		}
		last = strings.TrimSpace(errOut + out)
		if strings.Contains(last, "emerg") {
			break // a real config error, not a container still starting
		}
		time.Sleep(2 * time.Second)
	}
	for _, l := range strings.Split(last, "\n") {
		if strings.Contains(l, "emerg") {
			return errors.New(strings.TrimSpace(l))
		}
	}
	return fmt.Errorf("nginx -t failed: %s", firstLine(last))
}

// checkDomains asks the local nginx for each domain's home page.
func checkDomains(hosts []string) error {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // local dev certificate
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, dm.LocalTLS)
		},
	}
	c := &http.Client{Transport: tr, Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var bad []string
	for _, h := range hosts {
		resp, err := c.Get("https://" + h + "/")
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) || strings.Contains(err.Error(), "connection refused") {
				return fmt.Errorf("nginx does not answer on %s: %v", dm.LocalTLS, err)
			}
			bad = append(bad, h+": "+err.Error())
			continue
		}
		resp.Body.Close()
		if resp.StatusCode >= 500 {
			bad = append(bad, fmt.Sprintf("%s: HTTP %d", h, resp.StatusCode))
		}
	}
	if len(bad) > 0 {
		return errors.New(strings.Join(bad, "; "))
	}
	return nil
}

// hostsSteps adds missing domains to /etc/hosts, the way bin/setup-domain does.
func (a *App) hostsSteps(hosts []string) []step {
	var missing []string
	return []step{
		{label: "find domains missing from /etc/hosts", fn: func() error {
			missing = missing[:0]
			for _, h := range hosts {
				if !dm.HostsHas(etcHosts, h) {
					missing = append(missing, h)
				}
			}
			return nil
		}},
		{label: "add them to /etc/hosts (sudo)", term: true, kind: config.KindBin, skip: func() bool { return len(missing) == 0 },
			argvFn: func() []string { return hostsAppendArgv(missing) }},
	}
}

func hostsAppendArgv(hosts []string) []string {
	var lines strings.Builder
	for _, h := range hosts {
		lines.WriteString("127.0.0.1 ::1 " + h + "\n")
	}
	return []string{"sudo", "sh", "-c", `printf '%s' "$1" >> /etc/hosts && echo "added to /etc/hosts:" && printf '%s' "$1"`, "lazymage", lines.String()}
}

func (a *App) addHosts(hosts []string) tea.Cmd {
	return a.runPlan(&stepPlan{title: "Add domains to /etc/hosts", steps: a.hostsSteps(hosts),
		onDone: func(bool) tea.Cmd { return a.doctorCmd(false) }})
}

func (a *App) showWindowsHosts(hosts []string) tea.Cmd {
	var lines []string
	for _, h := range hosts {
		if !dm.HostsHas(dm.WindowsHostsFile, h) {
			lines = append(lines, "127.0.0.1 "+h)
		}
	}
	if len(lines) == 0 {
		return nil
	}
	body := "Your browser runs on Windows, which reads its own hosts file. Open Notepad as\n" +
		"Administrator, open C:\\Windows\\System32\\drivers\\etc\\hosts and add:\n\n" +
		sAccent.Render(strings.Join(lines, "\n")) + "\n\n" +
		"Then trust docker-magento's certificate authority in Windows once\n(bin/setup-ssl-ca prints where it is). y copies the lines.\n\n" + sFaint.Render("enter / esc to close")
	a.push(&copyMessage{messageOverlay: messageOverlay{title: "Windows hosts file", body: body}, text: strings.Join(lines, "\n") + "\n"})
	return nil
}

type copyMessage struct {
	messageOverlay
	text string
}

func (m *copyMessage) update(a *App, msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "y" {
		return a.copyText(m.text)
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc", "enter", "q", "ctrl+c":
			a.remove(m)
		}
	}
	return nil
}

// repairMultistore re-applies the nginx side of an existing store map
// (after a Magento upgrade replaced nginx.conf.sample, or a lost override).
func (a *App) repairMultistore() tea.Cmd {
	root := a.proj.Root
	b := a.builder()
	mapConf, err := os.ReadFile(filepath.Join(root, dm.StoreMapFile))
	if err != nil {
		return a.setStatus("no "+dm.StoreMapFile+" to repair", true)
	}
	f := &msFiles{mapConf: string(mapConf)}
	return a.runPlan(&stepPlan{title: "Repair multistore wiring", steps: []step{
		{label: "mount the store map and pass the run code", changes: true, fn: func() error { return writeMultistoreFiles(root, f) }},
		{label: "recreate the app container", argv: b.Compose("up", "-d", "--no-deps", "app"), kind: config.KindCompose},
		{label: "nginx accepts the config", fn: func() error { return waitNginxOK(root, b.DirectCompose("exec", "-T", "app", "nginx", "-t")) }},
	}, rollback: []step{
		{label: "restore the previous files", fn: func() error { return restoreMultistoreFiles(root, f) }},
		{label: "recreate the app container", argv: b.Compose("up", "-d", "--no-deps", "app"), kind: config.KindCompose},
	}, onDone: func(bool) tea.Cmd { return a.doctorCmd(false) }})
}
