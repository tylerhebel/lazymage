package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRoundTripPreservesUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	in := `runtime: orbstack
integration_mode: hybrid
future_option: 42
logs:
  tail_lines: 300
  follow_on_open: false
  color: never
custom_commands:
  - name: "Reindex catalog"
    category: "Magento — setup"
    kind: magento
    command: "indexer:reindex catalog_product_price"
    prompts: []
    confirm: false
    shortcut: "ctrl+r"
    pinned: true
    team_note: keep me
  - name: "Broken"
    kind: nonsense
    command: "x"
  - name: "Missing prompt"
    kind: composer
    command: "require {{package}}"
projects:
  /p/one:
    history: []
    pinned_tab: logs
    custom_commands: []
`
	if err := os.WriteFile(p, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.CustomCommands) != 1 || len(c.Warnings) != 2 {
		t.Fatalf("want 1 command and 2 warnings, got %d / %v", len(c.CustomCommands), c.Warnings)
	}
	if c.Logs.TailLines != 300 || c.Logs.FollowOnOpen {
		t.Fatalf("logs not read: %+v", c.Logs)
	}
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(p)
	for _, want := range []string{"future_option: 42", "color: never", "team_note: keep me", "pinned_tab: logs", "shortcut: ctrl+r"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("saved config lost %q:\n%s", want, out)
		}
	}
}

func TestMalformedIsParseError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(p, []byte("runtime: [unterminated\n"), 0o644)
	_, err := Load(p)
	if _, ok := err.(*ParseError); !ok {
		t.Fatalf("want ParseError, got %T %v", err, err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "runtime: [unterminated\n" {
		t.Fatal("malformed config was modified")
	}
}

func TestMissingIsNotExist(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if !IsNotExist(err) {
		t.Fatalf("want not-exist, got %v", err)
	}
}

func TestHistoryCap(t *testing.T) {
	c := Defaults("")
	for i := 0; i < 60; i++ {
		c.AddHistory("/r", HistoryEntry{Command: "x", ExitCode: i})
	}
	h := c.Projects["/r"].History
	if len(h) != HistoryCap || h[0].ExitCode != 10 {
		t.Fatalf("cap wrong: len=%d first=%d", len(h), h[0].ExitCode)
	}
}

func TestEffectiveCommandsOverride(t *testing.T) {
	c := Defaults("")
	c.CustomCommands = []CustomCommand{{Name: "Deploy", Kind: KindBin, Command: "deploy"}}
	c.Project("/r").CustomCommands = []CustomCommand{{Name: "deploy", Kind: KindBin, Command: "deploy --fast"}}
	eff := c.EffectiveCommands("/r")
	if len(eff) != 2 {
		t.Fatalf("want 2, got %d", len(eff))
	}
	var sawOverridden bool
	for _, s := range eff {
		if !s.Project && s.Overridden {
			sawOverridden = true
		}
	}
	if !sawOverridden {
		t.Fatal("global not flagged overridden")
	}
	// Deleting the project command leaves the global intact.
	cmds := c.Commands("/r", true)
	*cmds = nil
	if eff := c.EffectiveCommands("/r"); len(eff) != 1 || eff[0].Overridden {
		t.Fatalf("global should remain, unshadowed: %+v", eff)
	}
}

func TestSplitAndExpand(t *testing.T) {
	got, err := ExpandArgs(`require "{{package}}" --dev 'a b'`, map[string]string{"package": "vendor/pkg; rm -rf /"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"require", "vendor/pkg; rm -rf /", "--dev", "a b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
	if s := ExpandShell("echo {{x}}", map[string]string{"x": "a'b; rm"}); s != `echo 'a'\''b; rm'` {
		t.Fatalf("shell expand: %s", s)
	}
	if ph := Placeholders("a {{x}} {{ y }} {{x}}"); !reflect.DeepEqual(ph, []string{"x", "y"}) {
		t.Fatalf("placeholders: %v", ph)
	}
	if _, err := SplitArgs(`"open`); err == nil {
		t.Fatal("want unterminated quote error")
	}
}

func TestRedact(t *testing.T) {
	cases := map[string]string{
		"bin/magento admin:user:create --admin-password=hunter2 --admin-user=x": "bin/magento admin:user:create --admin-password=*** --admin-user=x",
		"bin/magento setup:install --db-password hunter2":                       "bin/magento setup:install --db-password ***",
		"bin/mysql -phunter2": "bin/mysql -p***",
		"composer config http-basic.repo.magento.com pubkey privkey": "composer config http-basic.repo.magento.com *** ***",
		"git clone https://me:tok123@github.com/x/y":                 "git clone https://me:***@github.com/x/y",
		"env API_TOKEN=abc cmd":                                      "env API_TOKEN=*** cmd",
		"bin/magento config:set payment/x/password s3cr3t":           "bin/magento config:set payment/x/password ***",
		"bin/magento cache:flush":                                    "bin/magento cache:flush",
	}
	for in, want := range cases {
		if got := RedactString(in); got != want {
			t.Errorf("%q:\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestLooksDestructive(t *testing.T) {
	if LooksDestructive(KindMagento, "cache:flush") {
		t.Error("cache:flush is not destructive")
	}
	if !LooksDestructive(KindMagento, "deploy:mode:set production") || !LooksDestructive(KindHostShell, "ls") {
		t.Error("expected destructive")
	}
}
