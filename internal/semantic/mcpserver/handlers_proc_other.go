//go:build !unix

package mcpserver

import "os/exec"

// killProcessGroupOnCancel has no process group to use off Unix, so the
// deadline kills the direct child only (exec.CommandContext's default) and
// cmd.WaitDelay in ExecuteHandler stops a surviving grandchild from holding
// the call open past the deadline.
func killProcessGroupOnCancel(cmd *exec.Cmd) {}
