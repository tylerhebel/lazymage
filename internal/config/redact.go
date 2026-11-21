package config

import (
	"regexp"
	"strings"
)

const Redacted = "***"

var (
	secretNameRe = regexp.MustCompile(`(?i)(pass(word|wd)?|pwd|token|secret|api[-_]?key|auth|credential|private[-_]?key)`)
	urlCredsRe   = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)([^/@\s:]+):([^/@\s]+)@`)
	// Composer: `config http-basic.repo.magento.com USER PASS`, `config github-oauth.github.com TOKEN`, etc.
	composerAuthRe = regexp.MustCompile(`(?i)^(http-basic|github-oauth|gitlab-oauth|gitlab-token|bitbucket-oauth|bearer)\.`)
	// Short flags that conventionally carry a secret (mysql -p).
	secretShortFlags = map[string]bool{"-p": true}
)

// RedactArgs returns a copy of argv with secret-looking values replaced by ***.
//
//	--admin-password=x      -> --admin-password=***
//	--db-password x         -> --db-password ***
//	-psecret                -> -p***  (mysql style)
//	https://user:pw@host    -> https://user:***@host
//	config http-basic.X u p -> config http-basic.X *** ***
//	KEY_TOKEN=x (env style) -> KEY_TOKEN=***
//	config:set some/path/password value -> value redacted
func RedactArgs(argv []string) []string {
	out := make([]string, len(argv))
	copy(out, argv)
	for i := 0; i < len(out); i++ {
		a := out[i]
		out[i] = urlCredsRe.ReplaceAllString(a, "${1}${2}:"+Redacted+"@")
		a = out[i]
		switch {
		case strings.HasPrefix(a, "-") && strings.Contains(a, "="):
			k, _, _ := strings.Cut(a, "=")
			if secretNameRe.MatchString(k) {
				out[i] = k + "=" + Redacted
			}
		case strings.HasPrefix(a, "--") && secretNameRe.MatchString(a):
			if i+1 < len(out) && !strings.HasPrefix(out[i+1], "-") {
				out[i+1] = Redacted
				i++
			}
		case secretShortFlags[a]:
			if i+1 < len(out) && !strings.HasPrefix(out[i+1], "-") {
				out[i+1] = Redacted
				i++
			}
		case len(a) > 2 && strings.HasPrefix(a, "-p") && !strings.HasPrefix(a, "--"):
			out[i] = "-p" + Redacted
		case !strings.HasPrefix(a, "-") && strings.Contains(a, "="):
			k, _, _ := strings.Cut(a, "=")
			if secretNameRe.MatchString(k) && !strings.ContainsAny(k, " /") {
				out[i] = k + "=" + Redacted
			}
		case composerAuthRe.MatchString(a):
			for j := i + 1; j < len(out) && !strings.HasPrefix(out[j], "-"); j++ {
				out[j] = Redacted
				i = j
			}
		case strings.Contains(a, "/") && secretNameRe.MatchString(a) && i > 0 && out[i-1] == "config:set":
			// bin/magento config:set path/to/password VALUE
			if i+1 < len(out) {
				out[i+1] = Redacted
				i++
			}
		}
	}
	return out
}

// RedactString redacts a free-form command line (used for host-shell commands
// and the debug log), by splitting it into words where possible.
func RedactString(s string) string {
	words, err := SplitArgs(s)
	if err != nil {
		words = strings.Fields(s)
	}
	return JoinArgs(RedactArgs(words))
}
