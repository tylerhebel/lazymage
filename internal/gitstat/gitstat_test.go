package gitstat

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParsePorcelainV2(t *testing.T) {
	out := "# branch.oid 1234567890abcdef\n# branch.head feature/x\n# branch.upstream origin/feature/x\n# branch.ab +2 -1\n" +
		"1 .M N... 100644 100644 100644 aaa bbb app/design/frontend/Acme/default/web/css/source/_theme.less\n" +
		"1 A. N... 000000 100644 100644 000 ccc app/code/Acme/New/registration.php\n" +
		"2 R. N... 100644 100644 100644 ddd eee R100 new name.php\told name.php\n" +
		"u UU N... 100644 100644 100644 100644 f g h composer.lock\n" +
		"? notes.txt\n"
	s := Parse(out)
	if s.Branch != "feature/x" || s.Upstream != "origin/feature/x" || s.Ahead != 2 || s.Behind != 1 || s.Head != "1234567" {
		t.Fatalf("%+v", s)
	}
	want := []struct{ path, label string }{
		{"app/design/frontend/Acme/default/web/css/source/_theme.less", "modified"},
		{"app/code/Acme/New/registration.php", "added (staged)"},
		{"new name.php", "renamed (staged)"},
		{"composer.lock", "conflict"},
		{"notes.txt", "untracked"},
	}
	if len(s.Files) != len(want) {
		t.Fatalf("files %+v", s.Files)
	}
	for i, w := range want {
		if s.Files[i].Path != w.path || s.Files[i].Label() != w.label {
			t.Errorf("file %d: %q %q, want %q %q", i, s.Files[i].Path, s.Files[i].Label(), w.path, w.label)
		}
	}
	if s.Files[2].From != "old name.php" {
		t.Error("rename source")
	}
}

func TestReadRealRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	src := filepath.Join(root, "src")
	os.MkdirAll(src, 0o755)
	run := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", src}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	run("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(src, "a.php"), []byte("<?php\n"), 0o644)
	run("add", ".")
	run("commit", "-q", "-m", "first")
	os.WriteFile(filepath.Join(src, "a.php"), []byte("<?php echo 1;\n"), 0o644)
	os.WriteFile(filepath.Join(src, "b.less"), []byte("@x: 1;\n"), 0o644)

	repo, err := FindRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := filepath.EvalSymlinks(src); repo != r && repo != src {
		t.Fatalf("repo %s, want %s", repo, src)
	}
	s, err := Read(repo)
	if err != nil {
		t.Fatal(err)
	}
	if s.Branch != "main" || len(s.Files) != 2 || len(s.Commits) != 1 {
		t.Fatalf("%+v", s)
	}
	if _, err := FindRepo(t.TempDir()); err == nil {
		t.Fatal("a directory without git should report it")
	}
}
