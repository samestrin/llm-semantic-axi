//go:build unix

package commands

import "syscall"

// mkfifo makes a named pipe; ok is false where there are none.
func mkfifo(path string) (ok bool, err error) { return true, syscall.Mkfifo(path, 0o644) }
