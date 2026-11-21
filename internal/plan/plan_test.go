package plan

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"lazymage/internal/config"
	"lazymage/internal/project"
)

func proj(t *testing.T, bins ...string) *project.Project {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "bin"), 0o755)
	os.MkdirAll(filepath.Join(root, "src"), 0o755)
	os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services: {}\n"), 0o644)
	for _, b := range append(bins, "magento") {
		os.WriteFile(filepath.Join(root, "bin", b), []byte("#!/bin/sh\n"), 0o755)
	}
	p, ok := project.FindRoot(root)
	if !ok {
		t.Fatal("no project")
	}
	return p
}

func TestModes(t *testing.T) {
	p := proj(t, "clinotty", "composer", "start", "stop", "docker-compose")
	h := New(p, config.ModeHybrid, "phpfpm")
	d := New(p, config.ModeDockerOnly, "phpfpm")
	bo := New(p, config.ModeBinOnly, "phpfpm")
	check := func(name string, got, want []string) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %q\nwant %q", name, got, want)
		}
	}
	check("hybrid magento", h.Magento("cache:flush"), []string{"bin/clinotty", "bin/magento", "cache:flush"})
	check("docker magento", d.Magento("cache:flush"), []string{"docker", "compose", "exec", "-T", "phpfpm", "bin/magento", "cache:flush"})
	check("hybrid start", h.Lifecycle("start"), []string{"bin/start"})
	check("hybrid restart fallback", h.Lifecycle("restart"), []string{"docker", "compose", "restart"})
	check("docker start", d.Lifecycle("start"), []string{"docker", "compose", "up", "-d", "--remove-orphans"})
	ps, js := h.PS()
	check("hybrid ps", ps, []string{"docker", "compose", "ps", "-a", "--format", "json"})
	ps2, js2 := bo.PS()
	check("bin ps", ps2, []string{"bin/docker-compose", "ps", "-a"})
	if !js || js2 {
		t.Error("json flags wrong")
	}
	check("logs", h.Logs("db", 200, true, ""), []string{"docker", "compose", "logs", "--no-color", "--tail", "200", "-f", "db"})
}

func TestCustom(t *testing.T) {
	p := proj(t)
	b := New(p, config.ModeHybrid, "phpfpm")
	argv, err := b.Custom(config.CustomCommand{Kind: config.KindComposer, Command: "require {{package}}"}, map[string]string{"package": "a/b c"})
	if err != nil || !reflect.DeepEqual(argv, []string{"docker", "compose", "exec", "-T", "phpfpm", "composer", "require", "a/b c"}) {
		t.Fatalf("%q %v", argv, err)
	}
	if _, err := b.Custom(config.CustomCommand{Kind: config.KindBin, Command: "../../etc/x"}, nil); err == nil {
		t.Fatal("bin path traversal accepted")
	}
	argv, _ = b.Custom(config.CustomCommand{Kind: config.KindHostShell, Command: "echo {{x}}"}, map[string]string{"x": "$(id)"})
	if argv[2] != "echo '$(id)'" {
		t.Fatalf("host shell not quoted: %q", argv)
	}
}
