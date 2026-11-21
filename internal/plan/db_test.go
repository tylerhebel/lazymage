package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lazymage/internal/config"
	"lazymage/internal/project"
)

func TestDBCommandsKeepSecretsInContainer(t *testing.T) {
	b := New(&project.Project{Root: "/x"}, config.ModeHybrid, "phpfpm")
	for name, argv := range map[string][]string{"dump": b.DBDump(), "import": b.DBImport(), "query": b.DBQuery("SELECT 1")} {
		s := strings.Join(argv, " ")
		if !strings.Contains(s, "exec -T db sh -c") {
			t.Errorf("%s: %s", name, s)
		}
		if strings.Contains(s, "-p\"$p\"") || strings.Contains(s, "-p$") {
			t.Errorf("%s passes the password as an argument", name)
		}
		if !strings.Contains(s, "MYSQL_PWD") {
			t.Errorf("%s should pass the password through MYSQL_PWD", name)
		}
	}
	q := b.DBQuery("SELECT 'a b'")
	if q[len(q)-1] != "SELECT 'a b'" || q[len(q)-2] != "lazymage" {
		t.Fatalf("SQL must be a positional argument, got %q", q[len(q)-2:])
	}
	b.DBService = "mysql"
	if !strings.Contains(strings.Join(b.DBDump(), " "), "exec -T mysql sh") {
		t.Error("custom db service not used")
	}
}

func TestDBShellPrefersBinMysql(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "bin"), 0o755)
	os.WriteFile(filepath.Join(root, "bin", "mysql"), []byte("#!/bin/sh\n"), 0o755)
	b := New(&project.Project{Root: root}, config.ModeHybrid, "")
	if got := b.DBShell(); len(got) != 1 || got[0] != "bin/mysql" {
		t.Fatalf("got %v", got)
	}
	b.Mode = config.ModeDockerOnly
	if got := strings.Join(b.DBShell(), " "); !strings.Contains(got, "exec db sh -c") {
		t.Fatalf("docker-only shell should exec with a TTY: %s", got)
	}
}

func TestSQLQuoting(t *testing.T) {
	if got := SQLString(`it's \ x`); got != `'it\'s \\ x'` {
		t.Fatal(got)
	}
	got := URLReplace("m2_", "shop_1.com", "shop.test")
	for _, want := range []string{"`m2_core_config_data`", `REPLACE(value, 'shop_1.com', 'shop.test')`, `LIKE '%shop\\_1.com%'`} {
		if !strings.Contains(got, want) {
			t.Errorf("%s\nmissing %s", got, want)
		}
	}
	if DBServiceFrom([]string{"app", "mariadb", "redis"}) != "mariadb" || DBServiceFrom([]string{"app"}) != "" {
		t.Error("DBServiceFrom")
	}
}
