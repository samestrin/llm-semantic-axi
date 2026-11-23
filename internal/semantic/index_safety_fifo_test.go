//go:build unix

package semantic

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestIndexSkipsFIFO (EST-8278 item 7): a FIFO named *.txt blocked index on
// open, waiting for a writer that never came.
func TestIndexSkipsFIFO(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"real.txt": "kept"})
	if err := syscall.Mkfifo(filepath.Join(root, "pipe.txt"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}

	var result *IndexResult
	done := make(chan struct{})
	go func() {
		defer close(done)
		result, _, _ = runSafetyIndex(t, root)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("index hung on a FIFO")
	}
	if result.FilesProcessed != 1 || result.FilesWithheld["not_regular_file"] != 1 {
		t.Errorf("processed %d, withheld %v; want 1 and not_regular_file 1", result.FilesProcessed, result.FilesWithheld)
	}
}
