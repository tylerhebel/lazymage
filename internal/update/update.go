// Package update checks GitHub for a newer lazymage release and replaces the
// running binary with it. The download is verified against the release's
// checksums.txt before anything on disk changes.
package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Repo is the GitHub repository releases come from.
const Repo = "tylerhebel/lazymage"

// Base URLs; tests point these at a local server.
var (
	APIBase      = "https://api.github.com"
	DownloadBase = "https://github.com"
	Client       = &http.Client{Timeout: 5 * time.Minute}
)

// Release is the newest published release.
type Release struct {
	Tag string `json:"tag_name"`
	URL string `json:"html_url"`
}

// Asset is the release file for this OS and CPU.
func Asset() string { return "lazymage-" + runtime.GOOS + "-" + runtime.GOARCH }

func get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "lazymage-updater")
	resp, err := Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return resp, nil
}

// Latest asks GitHub for the newest release.
func Latest(ctx context.Context) (Release, error) {
	resp, err := get(ctx, APIBase+"/repos/"+Repo+"/releases/latest")
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	var r Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return Release{}, fmt.Errorf("reading the release: %w", err)
	}
	if r.Tag == "" {
		return Release{}, errors.New("the latest release has no tag")
	}
	return r, nil
}

// Newer reports whether tag is a newer version than current. A current of
// "dev" (a source build) or anything unparseable is never considered older,
// so source builds are not nagged; they can still update by hand.
func Newer(tag, current string) bool {
	a, ok1 := parse(tag)
	b, ok2 := parse(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	v, _, _ = strings.Cut(v, "-") // pre-release suffix: compare the core only
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Executable is the real path of the running binary (symlinks resolved).
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// Install downloads tag's binary for this platform, checks it against the
// release's checksums.txt, and atomically replaces the binary at exe.
func Install(ctx context.Context, tag, exe string) error {
	dir := filepath.Dir(exe)
	base := DownloadBase + "/" + Repo + "/releases/download/" + tag + "/"
	want, err := checksum(ctx, base+"checksums.txt", Asset())
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".lazymage-update-*")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("cannot write to %s (permission denied) — reinstall with the install script, or move lazymage somewhere you own, such as ~/.local/bin", dir)
		}
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename

	resp, err := get(ctx, base+Asset())
	if err != nil {
		tmp.Close()
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, h), resp.Body)
	resp.Body.Close()
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("checksum mismatch for %s (got %s, want %s) — nothing was changed", Asset(), got[:12], want[:12])
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	// rename(2) over a running binary is safe on macOS and Linux: the running
	// process keeps the old inode, new launches get the new file.
	return os.Rename(tmp.Name(), exe)
}

// checksum finds asset's SHA-256 in a `shasum -a 256` listing.
func checksum(ctx context.Context, url, asset string) (string, error) {
	resp, err := get(ctx, url)
	if err != nil {
		return "", fmt.Errorf("checksums: %w", err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 1<<20))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset && len(f[0]) == 64 {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("the release has no checksum for %s (no build for this platform?)", asset)
}

// Cache remembers the last check so lazymage asks GitHub at most once a day.
type Cache struct {
	Checked time.Time `json:"checked"`
	Latest  string    `json:"latest"`
	URL     string    `json:"url"`
}

func ReadCache(path string) Cache {
	var c Cache
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	return c
}

func WriteCache(path string, c Cache) {
	if b, err := json.Marshal(c); err == nil {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, b, 0o644)
	}
}
