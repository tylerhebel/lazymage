package dbfile

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGzipRoundTripAndCommit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backups", "x.sql.gz")
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.Repeat("INSERT INTO t VALUES (1);\n", 1000)
	io.WriteString(w, sql)
	if w.Written() != int64(len(sql)) {
		t.Fatalf("written %d, want %d", w.Written(), len(sql))
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("dump must not appear before Commit")
	}
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("dump mode %v, want 0600", st.Mode().Perm())
	}
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	r.Close()
	if string(got) != sql {
		t.Fatal("round trip changed the dump")
	}
	if r.Progress() != 1 {
		t.Errorf("progress %v after reading everything", r.Progress())
	}
}

func TestAbortAndEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.sql")
	w, _ := Create(path)
	io.WriteString(w, "partial")
	w.Abort()
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("abort left files: %v", ents)
	}
	w, _ = Create(path)
	if err := w.Commit(); err == nil {
		t.Fatal("an empty dump must not be committed")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("empty dump left files: %v", ents)
	}
}

func TestListNewestFirst(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, DefaultDir), 0o755)
	write := func(rel string, age time.Duration) {
		p := filepath.Join(root, rel)
		os.WriteFile(p, []byte("x"), 0o644)
		ts := time.Now().Add(-age)
		os.Chtimes(p, ts, ts)
	}
	write("backups/old.sql.gz", 2*time.Hour)
	write("backups/new.sql", time.Minute)
	write("prod.sql.gz", time.Hour)
	write("backups/notes.txt", 0)
	write("backups/x.sql.gz.partial", 0)
	var got []string
	for _, e := range List(root, DefaultDir) {
		got = append(got, e.Path)
	}
	want := "backups/new.sql prod.sql.gz backups/old.sql.gz"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %v, want %s", got, want)
	}
}

func TestDefaultNameAndHuman(t *testing.T) {
	n := DefaultName("my shop", time.Date(2026, 11, 21, 9, 5, 0, 0, time.UTC))
	if n != "backups/my-shop-20261121-090500.sql.gz" {
		t.Fatal(n)
	}
	if HumanBytes(999) != "999 B" || HumanBytes(1_500_000) != "1.5 MB" {
		t.Fatal(HumanBytes(999), HumanBytes(1_500_000))
	}
}
