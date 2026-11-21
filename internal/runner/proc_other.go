//go:build !unix

package runner

import "os/exec"

// lazymage targets macOS and Linux; these keep other platforms compiling.
func setProcessGroup(cmd *exec.Cmd) {}
func interruptGroup(cmd *exec.Cmd)  { killGroup(cmd) }
func terminateGroup(cmd *exec.Cmd)  { killGroup(cmd) }
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
