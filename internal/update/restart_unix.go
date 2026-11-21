//go:build unix

package update

import (
	"os"
	"syscall"
)

// Restart replaces the current process with exe, keeping arguments and environment.
func Restart(exe string) error { return syscall.Exec(exe, os.Args, os.Environ()) }
