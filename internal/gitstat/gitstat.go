// Package gitstat reads a repository's state with the git CLI on the host:
// branch, upstream, ahead / behind, changed files, recent commits.
package gitstat

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// File is one changed path.
type File struct {
	Path string
	From string // rename source
	X, Y byte   // index and worktree status (porcelain v2), '?' for untracked
}

// Staged reports a change in the index.
func (f File) Staged() bool { return f.X != '.' && f.X != '?' && f.X != 0 }

// Label is a short human description ("modified", "new, staged", …).
func (f File) Label() string {
	if f.X == '?' {
		return "untracked"
	}
	if f.X == 'U' || f.Y == 'U' || (f.X == 'A' && f.Y == 'A') || (f.X == 'D' && f.Y == 'D') {
		return "conflict"
	}
	word := func(c byte) string {
		switch c {
		case 'M':
			return "modified"
		case 'A':
			return "added"
		case 'D':
			return "deleted"
		case 'R':
			return "renamed"
		case 'C':
			return "copied"
		case 'T':
			return "type changed"
		}
		return ""
	}
	var parts []string
	if w := word(f.X); w != "" {
		parts = append(parts, w+" (staged)")
	}
	if w := word(f.Y); w != "" {
		parts = append(parts, w)
	}
	return strings.Join(parts, ", ")
}

// Status is the state of a repository.
type Status struct {
	Repo          string // worktree top level
	Branch        string // "" when detached
	Head          string // short commit
	Upstream      string
	Ahead, Behind int
	Files         []File
	Commits       []string // "abc1234 subject (2 days ago)"
}

// Clean reports no changes.
func (s Status) Clean() bool { return len(s.Files) == 0 }

// Parse reads `git status --porcelain=v2 --branch` output.
func Parse(out string) Status {
	var s Status
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "# branch.oid "):
			if oid := strings.TrimPrefix(l, "# branch.oid "); len(oid) >= 7 {
				s.Head = oid[:7]
			}
		case strings.HasPrefix(l, "# branch.head "):
			if h := strings.TrimPrefix(l, "# branch.head "); h != "(detached)" {
				s.Branch = h
			}
		case strings.HasPrefix(l, "# branch.upstream "):
			s.Upstream = strings.TrimPrefix(l, "# branch.upstream ")
		case strings.HasPrefix(l, "# branch.ab "):
			f := strings.Fields(strings.TrimPrefix(l, "# branch.ab "))
			if len(f) == 2 {
				s.Ahead, _ = strconv.Atoi(strings.TrimPrefix(f[0], "+"))
				s.Behind, _ = strconv.Atoi(strings.TrimPrefix(f[1], "-"))
			}
		case strings.HasPrefix(l, "1 "):
			if f := strings.SplitN(l, " ", 9); len(f) == 9 && len(f[1]) == 2 {
				s.Files = append(s.Files, File{Path: f[8], X: f[1][0], Y: f[1][1]})
			}
		case strings.HasPrefix(l, "2 "):
			if f := strings.SplitN(l, " ", 10); len(f) == 10 && len(f[1]) == 2 {
				path, from, _ := strings.Cut(f[9], "\t")
				s.Files = append(s.Files, File{Path: path, From: from, X: f[1][0], Y: f[1][1]})
			}
		case strings.HasPrefix(l, "u "):
			if f := strings.SplitN(l, " ", 11); len(f) == 11 && len(f[1]) == 2 {
				s.Files = append(s.Files, File{Path: f[10], X: f[1][0], Y: f[1][1]})
			}
		case strings.HasPrefix(l, "? "):
			s.Files = append(s.Files, File{Path: l[2:], X: '?', Y: '?'})
		}
	}
	return s
}

func git(dir string, timeout time.Duration, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(cmd.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C") // don't take index.lock while polling
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.Output()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		return "", errors.New("git timed out")
	}
	if ee, ok := err.(*exec.ExitError); ok {
		msg := strings.TrimSpace(string(ee.Stderr))
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		return "", errors.New(msg)
	}
	return string(out), err
}

// FindRepo returns the work tree for a docker-magento project: the
// repository containing src/ (often src/ itself), else the project root.
func FindRepo(root string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", errors.New("git is not installed")
	}
	for _, dir := range []string{filepath.Join(root, "src"), root} {
		if out, err := git(dir, 5*time.Second, "rev-parse", "--show-toplevel"); err == nil {
			return strings.TrimSpace(out), nil
		}
	}
	return "", errors.New("src/ is not in a git repository")
}

// Read collects the repository's status and recent commits.
func Read(repo string) (Status, error) {
	out, err := git(repo, 10*time.Second, "status", "--porcelain=v2", "--branch", "--untracked-files=normal")
	if err != nil {
		return Status{Repo: repo}, err
	}
	s := Parse(out)
	s.Repo = repo
	if log, err := git(repo, 5*time.Second, "log", "-8", "--format=%h %s (%cr)"); err == nil {
		for _, l := range strings.Split(strings.TrimSpace(log), "\n") {
			if l != "" {
				s.Commits = append(s.Commits, l)
			}
		}
	}
	return s, nil
}

// DiffArgv shows a file's changes against HEAD (staged and unstaged).
func DiffArgv(repo string, f File) []string {
	return []string{"git", "-C", repo, "--no-pager", "diff", "--color=never", "HEAD", "--", f.Path}
}
