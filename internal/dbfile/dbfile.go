// Package dbfile handles database dump files on the host: writing a dump
// safely (partial file, renamed on success), reading one for import, optional
// gzip on both sides, and byte counters for progress.
package dbfile

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultDir is where dumps go, relative to the project root. It is outside
// src/ so dumps (which hold customer data) are not bind-mounted into the
// containers or picked up by the Magento repository.
const DefaultDir = "backups"

// DefaultName is a timestamped dump file name for a project.
func DefaultName(project string, now time.Time) string {
	project = strings.Map(func(r rune) rune {
		if r == '/' || r == ' ' || r == os.PathSeparator {
			return '-'
		}
		return r
	}, project)
	if project == "" {
		project = "magento"
	}
	return filepath.Join(DefaultDir, project+"-"+now.Format("20060102-150405")+".sql.gz")
}

// IsDump reports whether a file name looks like an SQL dump.
func IsDump(name string) bool {
	n := strings.ToLower(name)
	return strings.HasSuffix(n, ".sql") || strings.HasSuffix(n, ".sql.gz")
}

func gz(path string) bool { return strings.HasSuffix(strings.ToLower(path), ".gz") }

// Writer writes a dump to <path>.partial and renames it to path on Commit.
type Writer struct {
	Path    string
	partial string
	f       *os.File
	bw      *bufio.Writer
	zw      *gzip.Writer
	w       io.Writer
	n       atomic.Int64 // uncompressed bytes received
}

// Create opens a dump for writing, making parent directories as needed.
func Create(path string) (*Writer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	partial := path + ".partial"
	// Dumps hold customer data and password hashes: owner-only.
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	w := &Writer{Path: path, partial: partial, f: f, bw: bufio.NewWriterSize(f, 1<<20)}
	w.w = w.bw
	if gz(path) {
		w.zw = gzip.NewWriter(w.bw)
		w.w = w.zw
	}
	return w, nil
}

func (w *Writer) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.n.Add(int64(n))
	return n, err
}

// Written is the number of uncompressed bytes received so far.
func (w *Writer) Written() int64 { return w.n.Load() }

func (w *Writer) close() error {
	var errs []error
	if w.zw != nil {
		errs = append(errs, w.zw.Close())
	}
	errs = append(errs, w.bw.Flush(), w.f.Close())
	return errors.Join(errs...)
}

// Commit finishes the file and moves it into place. An empty dump is an error.
func (w *Writer) Commit() error {
	if err := w.close(); err != nil {
		_ = os.Remove(w.partial)
		return err
	}
	if w.Written() == 0 {
		_ = os.Remove(w.partial)
		return errors.New("the dump was empty")
	}
	return os.Rename(w.partial, w.Path)
}

// Abort removes the partial file.
func (w *Writer) Abort() {
	_ = w.close()
	_ = os.Remove(w.partial)
}

// Reader reads a dump for import, decompressing .gz files.
type Reader struct {
	Path  string
	Size  int64 // on-disk size
	f     *os.File
	cnt   *countingReader
	r     io.Reader
	close []io.Closer
}

type countingReader struct {
	r io.Reader
	n atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// Open opens a dump for reading.
func Open(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if st.IsDir() {
		f.Close()
		return nil, fmt.Errorf("%s is a directory", path)
	}
	cnt := &countingReader{r: bufio.NewReaderSize(f, 1<<20)}
	r := &Reader{Path: path, Size: st.Size(), f: f, cnt: cnt, r: cnt, close: []io.Closer{f}}
	if gz(path) {
		zr, err := gzip.NewReader(cnt)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("%s: not a gzip file: %w", filepath.Base(path), err)
		}
		r.r = zr
		r.close = append([]io.Closer{zr}, r.close...)
	}
	return r, nil
}

func (r *Reader) Read(p []byte) (int, error) { return r.r.Read(p) }

// Progress is the fraction of the file read so far (0..1).
func (r *Reader) Progress() float64 {
	if r.Size <= 0 {
		return 0
	}
	return min(float64(r.cnt.n.Load())/float64(r.Size), 1)
}

// Close closes the file.
func (r *Reader) Close() error {
	var errs []error
	for _, c := range r.close {
		errs = append(errs, c.Close())
	}
	return errors.Join(errs...)
}

// Entry is a dump file found on disk.
type Entry struct {
	Path    string // relative to the project root when inside it
	Size    int64
	ModTime time.Time
}

// List finds dumps in dir (relative to root) and directly in root, newest first.
func List(root, dir string) []Entry {
	var out []Entry
	seen := map[string]bool{}
	for _, d := range []string{dir, "."} {
		ents, err := os.ReadDir(filepath.Join(root, d))
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.IsDir() || !IsDump(e.Name()) {
				continue
			}
			rel := filepath.Join(d, e.Name())
			if seen[rel] {
				continue
			}
			seen[rel] = true
			info, err := e.Info()
			if err != nil {
				continue
			}
			out = append(out, Entry{Path: rel, Size: info.Size(), ModTime: info.ModTime()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out
}

// HumanBytes formats a byte count ("1.4 GB").
func HumanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}
