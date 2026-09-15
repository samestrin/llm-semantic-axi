package commands

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// ===== runIndexStatus over a real index =====

// TestIndexStatusReportsARealIndex covers the populated branch, which the
// existing suite could not reach: every prior test ran in an empty directory
// and only exercised the "index not found" path.
func TestIndexStatusReportsARealIndex(t *testing.T) {
	indexedWorkspace(t)

	out := captureStdout(t, func() {
		if err := runIndexStatus(context.Background(), false); err != nil {
			t.Fatalf("runIndexStatus: %v", err)
		}
	})

	if !strings.Contains(out, "Semantic Index Status") {
		t.Errorf("the status header is missing:\n%s", out)
	}
	if !strings.Contains(out, filepath.Join(".index", "semantic.db")) {
		t.Errorf("the index path is missing:\n%s", out)
	}
	if strings.Contains(out, "Files indexed: 0") {
		t.Errorf("a populated index reported zero files:\n%s", out)
	}
	if strings.Contains(out, "Total chunks:  0") {
		t.Errorf("a populated index reported zero chunks:\n%s", out)
	}
	if !strings.Contains(out, "Status:        Not performed") {
		t.Errorf("an uncalibrated index should say so:\n%s", out)
	}
}

// TestIndexStatusShowsCalibrationWhenPresent is the other half of the
// calibration block, and the only place the stored thresholds are surfaced to
// a human.
func TestIndexStatusShowsCalibrationWhenPresent(t *testing.T) {
	dir := newWorkspace(t)
	useEmbedder(t, newFakeEmbedder(t))
	writeFile(t, filepath.Join(dir, "sample.go"), sampleGoSource)
	captureStdout(t, func() {
		if err := runIndex(context.Background(), ".", indexOpts{}); err != nil {
			t.Fatalf("runIndex: %v", err)
		}
	})

	out := captureStdout(t, func() {
		if err := runIndexStatus(context.Background(), false); err != nil {
			t.Fatalf("runIndexStatus: %v", err)
		}
	})

	if strings.Contains(out, "Not performed") {
		t.Errorf("a calibrated index reported no calibration:\n%s", out)
	}
	for _, want := range []string{"Model:", "Calibrated:", "Perfect match:", "Baseline:", "Thresholds:"} {
		if !strings.Contains(out, want) {
			t.Errorf("the calibration block is missing %q:\n%s", want, out)
		}
	}
}

// TestIndexStatusJSONOverARealIndex pins the populated machine payload, which
// is a different struct from the compact "not found" map.
func TestIndexStatusJSONOverARealIndex(t *testing.T) {
	indexedWorkspace(t)

	out := captureStdout(t, func() {
		if err := runIndexStatus(context.Background(), true); err != nil {
			t.Fatalf("runIndexStatus: %v", err)
		}
	})

	var payload struct {
		Indexed      bool   `json:"indexed"`
		Storage      string `json:"storage"`
		FilesIndexed int    `json:"files_indexed"`
		ChunksTotal  int    `json:"chunks_total"`
		Path         string `json:"path"`
		Collection   string `json:"collection"`
	}
	decodeJSON(t, out, &payload)
	if !payload.Indexed {
		t.Errorf("indexed = false over a real index:\n%s", out)
	}
	if payload.Storage != "sqlite" {
		t.Errorf("storage = %q, want sqlite", payload.Storage)
	}
	if payload.FilesIndexed == 0 || payload.ChunksTotal == 0 {
		t.Errorf("payload = %+v, want non-zero counts", payload)
	}
	if payload.Path == "" {
		t.Errorf("the sqlite path is missing from the payload:\n%s", out)
	}
	// Collection belongs to Qdrant only; emitting it for SQLite would tell a
	// consumer the wrong backend is in use.
	if payload.Collection != "" {
		t.Errorf("collection = %q, want it omitted for sqlite", payload.Collection)
	}
}

// TestIndexStatusAXIOverARealIndex: the populated branch routes through
// emitStdout rather than the compact emitJSON the empty branch uses, so --axi
// has to be honoured in both.
func TestIndexStatusAXIOverARealIndex(t *testing.T) {
	indexedWorkspace(t)
	withAXI(t)

	out := captureStdout(t, func() {
		if err := runIndexStatus(context.Background(), false); err != nil {
			t.Fatalf("runIndexStatus: %v", err)
		}
	})

	if strings.Contains(out, "Semantic Index Status") {
		t.Errorf("--axi emitted the human status block:\n%s", out)
	}
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("--axi emitted JSON rather than TOON:\n%s", out)
	}
	if !strings.Contains(out, "indexed: true") {
		t.Errorf("the TOON payload is missing indexed:\n%s", out)
	}
}

// TestIndexStatusFailsOnAnUnreadableBackend: a status command that swallowed a
// storage error would report a healthy index that cannot be opened.
func TestIndexStatusFailsOnAnUnreadableBackend(t *testing.T) {
	indexedWorkspace(t)
	t.Cleanup(SetStorageTypeForTesting("cassandra"))

	err := runIndexStatus(context.Background(), true)
	if err == nil {
		t.Fatal("an unknown storage backend reported a healthy index")
	}
	if !strings.Contains(err.Error(), "failed to open index") {
		t.Errorf("error = %v, want it wrapped as an index-open failure", err)
	}
}
