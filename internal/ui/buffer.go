package ui

import (
	"regexp"
	"strings"
)

// Line is one logical line of streamed output.
type Line struct {
	Text string
	Err  bool // from stderr
	Note bool // lazymage's own annotation (restarts, exit codes)
}

// Buffer accumulates streamed output into lines, handling partial lines
// (prompts without a newline), \r progress rewrites, and ANSI escapes.
type Buffer struct {
	Lines   []Line
	open    bool // last line is still being written
	openErr bool
	Max     int
	Dropped int // lines trimmed from the front (keeps match indexes stable)
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-Z\\-_]`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// Append adds streamed data from stdout or stderr.
func (b *Buffer) Append(data string, isErr bool) {
	data = stripANSI(data)
	for len(data) > 0 {
		i := strings.IndexByte(data, '\n')
		seg := data
		if i >= 0 {
			seg = data[:i]
		}
		b.write(seg, isErr)
		if i < 0 {
			break
		}
		b.open = false
		data = data[i+1:]
	}
	b.trim()
}

func (b *Buffer) write(seg string, isErr bool) {
	seg = strings.ReplaceAll(seg, "\t", "    ")
	if b.open && b.openErr != isErr {
		b.open = false
	}
	if !b.open {
		b.Lines = append(b.Lines, Line{Err: isErr})
		b.open, b.openErr = true, isErr
	}
	last := &b.Lines[len(b.Lines)-1]
	// A carriage return rewrites the current line (progress bars).
	if j := strings.LastIndexByte(seg, '\r'); j >= 0 {
		rest := seg[j+1:]
		if rest == "" && j == len(seg)-1 {
			// "\r" right before "\n" (CRLF) or a pending rewrite: keep text so far.
			seg = seg[:j]
			if k := strings.LastIndexByte(seg, '\r'); k >= 0 {
				last.Text = seg[k+1:]
				return
			}
			last.Text += seg
			return
		}
		last.Text = rest
		return
	}
	last.Text += seg
}

// Note appends an annotation line.
func (b *Buffer) Note(s string) {
	b.open = false
	b.Lines = append(b.Lines, Line{Text: s, Note: true})
	b.trim()
}

func (b *Buffer) Clear() {
	b.Dropped += len(b.Lines)
	b.Lines = nil
	b.open = false
}

func (b *Buffer) trim() {
	if b.Max > 0 && len(b.Lines) > b.Max {
		n := len(b.Lines) - b.Max
		b.Lines = append([]Line(nil), b.Lines[n:]...)
		b.Dropped += n
	}
}

// render draws the last lines of idx (indexes into lines) that fit in h rows
// of width w, wrapping long lines, with offset lines scrolled up from the end.
// highlight, when non-empty, marks case-insensitive matches.
func renderLines(lines []Line, idx []int, offset, w, h int, highlight string, current int) []string {
	if h <= 0 {
		return nil
	}
	end := len(idx) - offset
	if end < 0 {
		end = 0
	}
	var rows []string
	for i := end - 1; i >= 0 && len(rows) < h; i-- {
		ln := lines[idx[i]]
		prefix := "  "
		style := lipgloss_noop
		switch {
		case ln.Note:
			prefix, style = "» ", sNote.Render
		case ln.Err:
			// Color is not the only signal: stderr lines carry a "!" gutter mark.
			prefix, style = "! ", sErr.Render
		}
		wrapped := wrap(ln.Text, max(w-2, 1))
		for j := len(wrapped) - 1; j >= 0 && len(rows) < h; j-- {
			p := "  "
			if j == 0 {
				p = prefix
			}
			text := wrapped[j]
			var out string
			if highlight != "" {
				out = highlightMatches(text, highlight, style, idx[i] == current)
			} else {
				out = style(text)
			}
			if j == 0 && (ln.Err || ln.Note) {
				p = style(p)
			}
			rows = append(rows, p+out)
		}
	}
	// Reverse to top-down order, and pad the top.
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	for len(rows) < h {
		rows = append([]string{""}, rows...)
	}
	return rows
}

func lipgloss_noop(s ...string) string { return strings.Join(s, " ") }

func highlightMatches(text, q string, style func(...string) string, current bool) string {
	lt, lq := strings.ToLower(text), strings.ToLower(q)
	if len(lt) != len(text) || len(lq) != len(q) {
		return style(text) // case folding changed byte offsets; skip highlighting
	}
	var b strings.Builder
	pos := 0
	for {
		i := strings.Index(lt[pos:], lq)
		if i < 0 || lq == "" {
			b.WriteString(style(text[pos:]))
			break
		}
		i += pos
		b.WriteString(style(text[pos:i]))
		m := text[i : i+len(q)]
		if current {
			b.WriteString(sMatch.Bold(true).Underline(true).Render(m))
		} else {
			b.WriteString(sMatch.Render(m))
		}
		pos = i + len(q)
	}
	return b.String()
}
