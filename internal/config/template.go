package config

import (
	"fmt"
	"regexp"
	"strings"
)

var placeholderRe = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_\-]*)\s*\}\}`)

// Placeholders returns the unique {{key}} names in s, in order of first use.
func Placeholders(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range placeholderRe.FindAllStringSubmatch(s, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// SplitArgs splits a command string into arguments the way a POSIX shell
// would for simple words: whitespace separates, single quotes are literal,
// double quotes allow \" and \\ escapes, backslash escapes outside quotes.
// It performs no expansion of any kind.
func SplitArgs(s string) ([]string, error) {
	var (
		args    []string
		cur     strings.Builder
		inWord  bool
		quote   rune
		escaped bool
	)
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			if r == '"' {
				quote = 0
			} else if r == '\\' {
				escaped = true
			} else {
				cur.WriteRune(r)
			}
		case r == '\\':
			escaped, inWord = true, true
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				args = append(args, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated %c quote", quote)
	}
	if escaped {
		return nil, fmt.Errorf("trailing backslash")
	}
	if inWord {
		args = append(args, cur.String())
	}
	return args, nil
}

// ExpandArgs splits template into words first and then substitutes
// placeholders inside each word, so user-supplied values can never be
// re-split or interpreted: each value stays inside the argument it was typed into.
func ExpandArgs(template string, values map[string]string) ([]string, error) {
	words, err := SplitArgs(template)
	if err != nil {
		return nil, err
	}
	for i, w := range words {
		words[i] = placeholderRe.ReplaceAllStringFunc(w, func(m string) string {
			return values[placeholderRe.FindStringSubmatch(m)[1]]
		})
	}
	return words, nil
}

// ExpandShell substitutes placeholders in a host-shell command string with
// single-quoted values, so they are passed to sh as literal words.
func ExpandShell(template string, values map[string]string) string {
	return placeholderRe.ReplaceAllStringFunc(template, func(m string) string {
		return quoteStrict(values[placeholderRe.FindStringSubmatch(m)[1]])
	})
}

var safeWordRe = regexp.MustCompile(`^[A-Za-z0-9_./:=@%+,-]+$`)

// quoteStrict quotes for execution by sh: anything outside a small safe set
// is single-quoted.
func quoteStrict(s string) string {
	if safeWordRe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ShellQuote quotes s for display only (never for execution).
func ShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	// "***" is the redaction marker; show it bare for readability.
	if !strings.ContainsAny(strings.ReplaceAll(s, "***", ""), " \t\n'\"\\$`!*?[]{}()<>|&;#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// JoinArgs renders argv as a copy-pasteable shell command line.
func JoinArgs(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = ShellQuote(a)
	}
	return strings.Join(q, " ")
}

var destructiveRe = regexp.MustCompile(`(?i)(\brm\b|\bdrop\b|\bdelete\b|\bremove\b|\breset\b|\btruncate\b|\bdown\b|uninstall|deploy:mode:set|--force|\bprune\b|\bkill\b|db:import|\bimport\b|setup:rollback|\bdd\b|mkfs)`)

// LooksDestructive is the default for a custom command's "Needs confirmation?".
func LooksDestructive(kind, command string) bool {
	return kind == KindHostShell || destructiveRe.MatchString(command)
}
