//go:build !unix

package commands

func mkfifo(string) (bool, error) { return false, nil }
