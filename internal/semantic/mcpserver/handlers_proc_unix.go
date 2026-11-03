//go:build unix

package mcpserver

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// killProcessGroupOnCancel starts cmd as the leader of its own process group
// and makes the context deadline kill that whole group. Killing only the
// direct child leaves any grandchild alive holding the inherited output pipe,
// and the caller blocks until it exits instead of at the deadline.
func killProcessGroupOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
