// Package ui is the lazymage terminal UI (Bubble Tea v2).
package ui

import (
	"os"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
)

// noColor follows https://no-color.org: any non-empty NO_COLOR disables color.
var noColor = os.Getenv("NO_COLOR") != ""

func fg(c string) lipgloss.Style {
	if noColor {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(c))
}

var (
	sBold     = lipgloss.NewStyle().Bold(true)
	sFaint    = lipgloss.NewStyle().Faint(true)
	sAccent   = fg("12").Bold(true)
	sOK       = fg("10")
	sWarn     = fg("11")
	sErr      = fg("9")
	sNote     = fg("14")
	sSelected = func() lipgloss.Style {
		if noColor {
			return lipgloss.NewStyle().Reverse(true)
		}
		return lipgloss.NewStyle().Background(lipgloss.Color("237")).Bold(true)
	}()
	sMatch = func() lipgloss.Style {
		if noColor {
			return lipgloss.NewStyle().Underline(true)
		}
		return lipgloss.NewStyle().Background(lipgloss.Color("3")).Foreground(lipgloss.Color("0"))
	}()
	sTab       = lipgloss.NewStyle().Padding(0, 1)
	sTabActive = func() lipgloss.Style {
		if noColor {
			return lipgloss.NewStyle().Padding(0, 1).Reverse(true)
		}
		return lipgloss.NewStyle().Padding(0, 1).Bold(true).Background(lipgloss.Color("12")).Foreground(lipgloss.Color("0"))
	}()
	sBorder = func() lipgloss.Style {
		s := lipgloss.NewStyle().Border(lipgloss.RoundedBorder())
		if !noColor {
			s = s.BorderForeground(lipgloss.Color("8"))
		}
		return s
	}()
	sOverlay = func() lipgloss.Style {
		s := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
		if !noColor {
			s = s.BorderForeground(lipgloss.Color("12"))
		}
		return s
	}()
)

func newInput(prompt, placeholder string) textinput.Model {
	ti := textinput.New()
	ti.Prompt = prompt
	ti.Placeholder = placeholder
	st := ti.Styles()
	st.Cursor.Blink = false
	ti.SetStyles(st)
	return ti
}

// truncate cuts s to width w display cells, adding an ellipsis.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > w {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// padRight pads s with spaces to width w (s is assumed unstyled or measured by lipgloss).
func padRight(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// wrap breaks plain text into rows of at most w cells.
func wrap(s string, w int) []string {
	if w <= 0 {
		return []string{""}
	}
	r := []rune(s)
	if len(r) == 0 {
		return []string{""}
	}
	var rows []string
	for len(r) > 0 {
		n, width := 0, 0
		for n < len(r) {
			cw := lipgloss.Width(string(r[n]))
			if width+cw > w {
				break
			}
			width += cw
			n++
		}
		if n == 0 {
			n = 1
		}
		rows = append(rows, string(r[:n]))
		r = r[n:]
	}
	return rows
}

// box renders content in a bordered box of exactly w x h cells (outer size).
func box(content string, w, h int) string {
	inner := w - 4
	lines := strings.Split(content, "\n")
	for len(lines) < h-2 {
		lines = append(lines, "")
	}
	lines = lines[:max(h-2, 0)]
	for i, l := range lines {
		lines[i] = " " + padRight(truncateStyled(l, inner), inner) + " "
	}
	return sBorder.Render(strings.Join(lines, "\n"))
}

// truncateStyled shortens a possibly styled line to w cells.
func truncateStyled(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(w).Render(s)
}

// overlayBox renders a titled overlay of outer width w.
func overlayBox(title, body string, w int) string {
	inner := w - 4
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		lines[i] = padRight(truncateStyled(l, inner), inner)
	}
	head := sAccent.Render(truncate(title, inner))
	return sOverlay.Render(padRight(head, inner) + "\n" + strings.Join(lines, "\n"))
}

// center places an overlay over the screen area of w x h.
func center(s string, w, h int) string {
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, s)
}

// keyHint renders "key action" pairs for footers.
func keyHint(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, sBold.Render(pairs[i])+" "+sFaint.Render(pairs[i+1]))
	}
	return strings.Join(parts, "  ")
}
