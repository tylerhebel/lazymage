package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		tag, cur string
		want     bool
	}{
		{"v0.2.0", "v0.1.0", true},
		{"v0.10.0", "v0.9.3", true},
		{"v1.0.0", "v1.0.0", false},
		{"v0.1.0", "v0.2.0", false},
		{"v0.2.0", "dev", false},
		{"v0.2.0-rc1", "v0.1.9", true},
		{"garbage", "v0.1.0", false},
	} {
		if got := Newer(c.tag, c.cur); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.tag, c.cur, got)
		}
	}
}

func fakeGitHub(t *testing.T, binary []byte, sum string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/"+Repo+"/releases/latest":
			fmt.Fprint(w, `{"tag_name":"v9.9.9","html_url":"https://example.invalid/r"}`)
		case strings.HasSuffix(r.URL.Path, "/v9.9.9/checksums.txt"):
			fmt.Fprintf(w, "%s  lazymage-other-os\n%s  %s\n", strings.Repeat("0", 64), sum, Asset())
		case strings.HasSuffix(r.URL.Path, "/v9.9.9/"+Asset()):
			w.Write(binary)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	oldAPI, oldDL := APIBase, DownloadBase
	APIBase, DownloadBase = srv.URL, srv.URL
	t.Cleanup(func() { APIBase, DownloadBase = oldAPI, oldDL })
}

func TestInstallVerifiesAndReplaces(t *testing.T) {
	bin := []byte("#!/bin/sh\necho new\n")
	h := sha256.Sum256(bin)
	fakeGitHub(t, bin, hex.EncodeToString(h[:]))
	rel, err := Latest(context.Background())
	if err != nil || rel.Tag != "v9.9.9" {
		t.Fatalf("Latest: %+v %v", rel, err)
	}
	exe := filepath.Join(t.TempDir(), "lazymage")
	os.WriteFile(exe, []byte("old"), 0o755)
	if err := Install(context.Background(), rel.Tag, exe); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(exe)
	st, _ := os.Stat(exe)
	if string(got) != string(bin) || st.Mode().Perm() != 0o755 {
		t.Fatalf("binary not replaced: %q %v", got, st.Mode())
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".lazymage-update-*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

func TestInstallRejectsBadChecksum(t *testing.T) {
	fakeGitHub(t, []byte("tampered"), strings.Repeat("a", 64))
	exe := filepath.Join(t.TempDir(), "lazymage")
	os.WriteFile(exe, []byte("old"), 0o755)
	err := Install(context.Background(), "v9.9.9", exe)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want checksum error, got %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Fatal("a failed update must leave the binary alone")
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".lazymage-update-*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}
