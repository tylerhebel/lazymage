// Command lazymage is a lazygit-style TUI for Mark Shust's docker-magento.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"

	"lazymage/internal/ui"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	setup := flag.Bool("setup", false, "re-run the first-time setup wizard")
	debug := flag.Bool("debug", false, "write a debug log to ~/.config/lazymage/debug.log (secrets redacted)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "lazymage — a TUI for Mark Shust's docker-magento\n\nRun it from inside a docker-magento project.\n\nUsage: lazymage [flags]\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *showVersion {
		fmt.Println("lazymage", version)
		return
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "lazymage: cannot read the current directory:", err)
		os.Exit(1)
	}

	app := ui.New(ui.Options{
		Version:    version,
		StartDir:   cwd,
		ForceSetup: *setup,
		Debug:      *debug,
		LoginShell: loginShell(),
	})
	p := tea.NewProgram(app)
	app.SetSender(p.Send)
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "lazymage:", err)
		os.Exit(1)
	}
}

// loginShell detects the user's login shell: $SHELL, then the passwd entry.
func loginShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	if runtime.GOOS != "windows" {
		if u, err := user.Current(); err == nil {
			if b, err := os.ReadFile("/etc/passwd"); err == nil {
				for _, l := range strings.Split(string(b), "\n") {
					f := strings.Split(l, ":")
					if len(f) >= 7 && f[0] == u.Username && f[6] != "" {
						return filepath.Clean(f[6])
					}
				}
			}
		}
	}
	return "bash"
}
