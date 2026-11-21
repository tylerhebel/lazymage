//go:build unix

package runner

import (
	"os/exec"
	"syscall"
)

func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil {
		return
	}
	// Negative pid targets the whole process group (bin script + docker CLI).
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil {
		_ = cmd.Process.Signal(sig)
	}
}

func interruptGroup(cmd *exec.Cmd) { signalGroup(cmd, syscall.SIGINT) }
func terminateGroup(cmd *exec.Cmd) { signalGroup(cmd, syscall.SIGTERM) }
func killGroup(cmd *exec.Cmd)      { signalGroup(cmd, syscall.SIGKILL) }
