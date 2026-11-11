package benchmarks

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// samplePerf is a complete, valid result set: every case has its timings.
func samplePerf() *PerfResults {
	return &PerfResults{
		Version: "v9.9.9-test",
		Date:    "2026-11-11",
		Machine: Machine{CPU: "Test CPU 3000", Threads: 8, RAMBytes: 16 << 30, OS: "Test OS 1 (linux/amd64)", Kernel: "6.1.0-test"},
		Embedder: PerfEmbedder{
			LatencyMicros: 1000,
			Dims:          PerfDims,
		},
		Corpus: PerfCorpus{Source: "generated", Files: 3000},
		Cases: []PerfCase{
			{Name: "cold start", Command: "llm-semantic --version", Warmup: 2, Runs: 3, TimingsNS: []int64{4_000_000, 5_000_000, 6_000_000}},
			{Name: "typical command", Command: `llm-semantic search "q" --json`, Warmup: 2, Runs: 3, TimingsNS: []int64{20_000_000, 21_000_000, 25_000_000}},
			{Name: "large input", Command: "llm-semantic index <corpus> --json", Warmup: 1, Runs: 2, TimingsNS: []int64{9_000_000_000, 9_500_000_000}},
		},
	}
}

func writePerfJSON(t *testing.T, dir string, r *PerfResults) {
	t.Helper()
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, PerfJSONFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Defends the rule the epic exists for: a case with no timings is a run that
// measured nothing, and the table must never print it as a number. Both a
// case with an empty list and a case holding a zero duration are refused.
func TestValidateRefusesACaseWithNoTimings(t *testing.T) {
	if err := samplePerf().Validate(); err != nil {
		t.Fatalf("valid results refused: %v", err)
	}
	empty := samplePerf()
	empty.Cases[1].TimingsNS = nil
	if err := empty.Validate(); err == nil || !strings.Contains(err.Error(), "typical command") {
		t.Errorf("a case with no timings passed validation: %v", err)
	}
	zero := samplePerf()
	zero.Cases[0].TimingsNS[1] = 0
	if err := zero.Validate(); err == nil {
		t.Error("a zero timing passed validation")
	}
	short := samplePerf()
	short.Cases[2].Runs = 5
	if err := short.Validate(); err == nil {
		t.Error("a case with fewer timings than runs passed validation")
	}
	none := samplePerf()
	none.Cases = nil
	if err := none.Validate(); err == nil {
		t.Error("results with no cases passed validation")
	}
}

// Defends the stamp lines: a table without the version, date or machine
// cannot be compared with anything, so each is required.
func TestValidateRefusesAMissingStamp(t *testing.T) {
	for name, mut := range map[string]func(*PerfResults){
		"version": func(r *PerfResults) { r.Version = "" },
		"date":    func(r *PerfResults) { r.Date = "" },
		"cpu":     func(r *PerfResults) { r.Machine.CPU = "" },
		"threads": func(r *PerfResults) { r.Machine.Threads = 0 },
		"ram":     func(r *PerfResults) { r.Machine.RAMBytes = 0 },
		"os":      func(r *PerfResults) { r.Machine.OS = "" },
		"kernel":  func(r *PerfResults) { r.Machine.Kernel = "" },
		"files":   func(r *PerfResults) { r.Corpus.Files = 0 },
	} {
		r := samplePerf()
		mut(r)
		if err := r.Validate(); err == nil {
			t.Errorf("missing %s passed validation", name)
		}
	}
}

// Defends the table's header: version, date, machine (CPU, threads, RAM, OS,
// kernel) and method (warmup and run counts, wall clock per process, the fake
// embedder) must all be printed, and no figure may come from anywhere but the
// JSON.
func TestRenderPerfStampsVersionDateMachineAndMethod(t *testing.T) {
	md, err := RenderPerf(samplePerf())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"**Version:** `v9.9.9-test`",
		"**Date:** 2026-11-11",
		"Test CPU 3000", "8 threads", "16.0 GiB RAM", "Test OS 1 (linux/amd64)", "kernel 6.1.0-test",
		"**Method:**", "wall clock", "warmup",
		"fixed-latency local fake", "1.0 ms", "measure the tool and not a model",
		"3,000 files",
		"| cold start | `llm-semantic --version` | 2 | 3 | 4.0 | 5.0 | 5.0 | 6.0 |",
		"| large input | `llm-semantic index <corpus> --json` | 1 | 2 | 9000.0 | 9250.0 | 9250.0 | 9500.0 |",
		PerfJSONFile,
	} {
		if !strings.Contains(md, want) {
			t.Errorf("table lacks %q:\n%s", want, md)
		}
	}
	bad := samplePerf()
	bad.Cases[0].TimingsNS = nil
	if _, err := RenderPerf(bad); err == nil {
		t.Error("RenderPerf rendered a case with no timings")
	}
}

// Defends "no figure is typed by hand": bench-perf rewrites the table from
// the JSON, so a hand edit is overwritten and new timings show up.
func TestWritePerfTableRewritesTheTableFromTheJSON(t *testing.T) {
	dir := t.TempDir()
	r := samplePerf()
	writePerfJSON(t, dir, r)
	var out bytes.Buffer
	if err := WritePerfTable(dir, &out); err != nil {
		t.Fatal(err)
	}
	mdPath := filepath.Join(dir, PerfResultsFile)
	first, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mdPath, []byte(strings.Replace(string(first), "| 4.0 |", "| 1.0 |", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WritePerfTable(dir, &out); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(mdPath); string(again) != string(first) {
		t.Errorf("a hand edit survived a second run:\n%s", again)
	}
	r.Cases[0].TimingsNS = []int64{7_000_000, 7_000_000, 7_000_000}
	writePerfJSON(t, dir, r)
	if err := WritePerfTable(dir, &out); err != nil {
		t.Fatal(err)
	}
	if third, _ := os.ReadFile(mdPath); !strings.Contains(string(third), "| 2 | 3 | 7.0 | 7.0 | 7.0 | 7.0 |") {
		t.Errorf("new timings did not reach the table:\n%s", third)
	}
}

// Defends the committed table against drift: it must be exactly what the
// committed JSON renders to, so nobody can edit one without the other.
func TestCommittedPerformanceTableMatchesItsJSON(t *testing.T) {
	r, err := ReadPerf(filepath.Join(".", PerfJSONFile))
	if err != nil {
		t.Fatal(err)
	}
	if r.Corpus.Files < PerfMinFiles {
		t.Errorf("committed large input has %d files, want at least %d", r.Corpus.Files, PerfMinFiles)
	}
	want, err := RenderPerf(r)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(PerfResultsFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("%s does not match %s; run `make bench-perf`", PerfResultsFile, PerfJSONFile)
	}
}

// Defends bench-perf against regenerating from a broken JSON: a case with no
// timings fails the run and leaves the old table in place.
func TestWritePerfTableFailsOnACaseWithNoTimings(t *testing.T) {
	dir := t.TempDir()
	r := samplePerf()
	r.Cases[2].TimingsNS = []int64{}
	writePerfJSON(t, dir, r)
	mdPath := filepath.Join(dir, PerfResultsFile)
	if err := os.WriteFile(mdPath, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WritePerfTable(dir, &bytes.Buffer{}); err == nil {
		t.Fatal("WritePerfTable accepted a case with no timings")
	}
	if got, _ := os.ReadFile(mdPath); string(got) != "old\n" {
		t.Errorf("table overwritten from a broken JSON: %q", got)
	}
	if err := WritePerfTable(t.TempDir(), &bytes.Buffer{}); err == nil {
		t.Error("WritePerfTable with no JSON returned no error")
	}
}

// fakePerfCLI writes a shell script standing in for llm-semantic: --version
// prints a version, index prints $FAKE_INDEX_JSON (or a clean result for
// $FAKE_FILES files), search prints $FAKE_SEARCH_JSON (or one result), and
// $FAKE_FAIL names a subcommand that exits 1.
func fakePerfCLI(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "llm-semantic")
	script := `#!/bin/sh
case "$1" in
  --version) echo "v0.0.0-fake" ;;
  index)
    [ "$FAKE_FAIL" = index ] && { echo "index broke" >&2; exit 1; }
    if [ -n "$FAKE_INDEX_JSON" ]; then echo "$FAKE_INDEX_JSON"
    else echo "{\"files_processed\":${FAKE_FILES:-0},\"files_skipped\":0,\"chunks_created\":9,\"errors\":[]}"; fi ;;
  search)
    [ "$FAKE_FAIL" = search ] && { echo "search broke" >&2; exit 1; }
    if [ -n "$FAKE_SEARCH_JSON" ]; then echo "$FAKE_SEARCH_JSON"
    else echo '[{"chunk":{"file_path":"a.go"},"score":0.9}]'; fi ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func perfConfig(t *testing.T, cli string, files int) PerfConfig {
	return PerfConfig{
		CLI: cli, OutDir: t.TempDir(), Files: files, MinFiles: files,
		Warmup: 1, Runs: 2, IndexWarmup: 0, IndexRuns: 1,
		Latency: time.Millisecond,
		Now:     func() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) },
		Machine: func() (Machine, error) { return samplePerf().Machine, nil },
		Log:     func(string) {},
	}
}

// Defends the measuring path end to end against a stand-in CLI: three cases,
// each with its runs timed, written to the JSON and rendered to the table.
func TestRunPerfWritesTheJSONAndTheTable(t *testing.T) {
	t.Setenv("FAKE_FILES", "12")
	cfg := perfConfig(t, fakePerfCLI(t), 12)
	if err := RunPerf(context.Background(), cfg, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	r, err := ReadPerf(filepath.Join(cfg.OutDir, PerfJSONFile))
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "v0.0.0-fake" || r.Date != "2026-11-11" || r.Corpus.Files != 12 || r.Corpus.Source != "generated" {
		t.Errorf("stamps wrong: %+v", r)
	}
	if len(r.Cases) != 3 {
		t.Fatalf("got %d cases, want 3", len(r.Cases))
	}
	for _, c := range r.Cases {
		if len(c.TimingsNS) != c.Runs || c.Runs == 0 {
			t.Errorf("case %q: %d timings for %d runs", c.Name, len(c.TimingsNS), c.Runs)
		}
	}
	md, err := os.ReadFile(filepath.Join(cfg.OutDir, PerfResultsFile))
	if err != nil || !strings.Contains(string(md), "**Version:** `v0.0.0-fake`") {
		t.Errorf("table not written: %v\n%s", err, md)
	}
}

// Defends the epic's success criterion directly: when a case records zero
// timings the program's run fails, and neither file is written, so no table
// can show a case that never ran.
func TestRunPerfFailsWhenACaseRecordsZeroTimings(t *testing.T) {
	t.Setenv("FAKE_FILES", "12")
	cfg := perfConfig(t, fakePerfCLI(t), 12)
	cfg.IndexRuns = 0
	err := RunPerf(context.Background(), cfg, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "large input") {
		t.Fatalf("RunPerf with a case of zero timings: %v", err)
	}
	for _, f := range []string{PerfJSONFile, PerfResultsFile} {
		if _, err := os.Stat(filepath.Join(cfg.OutDir, f)); !os.IsNotExist(err) {
			t.Errorf("%s written by a failed run", f)
		}
	}
}

// Defends against timing a command that did not do its job: a failed
// command, an empty search, an index that processed fewer files than the
// corpus holds, or one that reported errors is a failure, not a fast run.
func TestRunPerfRefusesARunThatDidNotWork(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"index exits 1":     {"FAKE_FAIL": "index"},
		"search exits 1":    {"FAKE_FAIL": "search"},
		"empty search":      {"FAKE_SEARCH_JSON": "[]"},
		"search not json":   {"FAKE_SEARCH_JSON": "nope"},
		"index short":       {"FAKE_FILES": "3"},
		"index errors":      {"FAKE_INDEX_JSON": `{"files_processed":12,"errors":["x"]}`},
		"index skipped":     {"FAKE_INDEX_JSON": `{"files_processed":12,"files_skipped":1,"errors":[]}`},
		"index not json":    {"FAKE_INDEX_JSON": "nope"},
		"index zero chunks": {"FAKE_INDEX_JSON": `{"files_processed":12,"chunks_created":0,"errors":[]}`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("FAKE_FILES", "12")
			for k, v := range env {
				t.Setenv(k, v)
			}
			cfg := perfConfig(t, fakePerfCLI(t), 12)
			if err := RunPerf(context.Background(), cfg, &bytes.Buffer{}); err == nil {
				t.Fatal("RunPerf timed a run that did not work")
			}
			if _, err := os.Stat(filepath.Join(cfg.OutDir, PerfJSONFile)); !os.IsNotExist(err) {
				t.Error("JSON written by a failed run")
			}
		})
	}
}

// Defends the "at least 3,000 files" claim when BENCH_DIR supplies the input:
// a smaller directory is refused rather than reported as the large case.
func TestRunPerfRefusesASmallBenchDir(t *testing.T) {
	t.Setenv("FAKE_FILES", "12")
	cfg := perfConfig(t, fakePerfCLI(t), 0)
	cfg.MinFiles = 3000
	cfg.Corpus = t.TempDir()
	if err := RunPerf(context.Background(), cfg, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "3000") {
		t.Fatalf("a 12-file BENCH_DIR was accepted as the large input: %v", err)
	}
}

// Defends the generated corpus: the requested number of files, in several
// languages, identical on every run so two machines index the same input.
func TestBuildCorpusIsDeterministic(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	for _, d := range []string{a, b} {
		if err := BuildCorpus(d, 120); err != nil {
			t.Fatal(err)
		}
	}
	n, err := CountFiles(a)
	if err != nil || n != 120 {
		t.Fatalf("CountFiles = %d, %v; want 120", n, err)
	}
	exts := map[string]int{}
	_ = filepath.WalkDir(a, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		exts[filepath.Ext(p)]++
		rel, _ := filepath.Rel(a, p)
		x, _ := os.ReadFile(p)
		y, _ := os.ReadFile(filepath.Join(b, rel))
		if !bytes.Equal(x, y) || len(x) == 0 {
			t.Errorf("%s differs between builds or is empty", rel)
		}
		return nil
	})
	for _, e := range []string{".go", ".py", ".md"} {
		if exts[e] == 0 {
			t.Errorf("corpus has no %s files: %v", e, exts)
		}
	}
}

// Defends the method line's "fixed latency": every request waits at least
// the stated time before the fake answers.
func TestLatencyEmbedderWaitsBeforeAnswering(t *testing.T) {
	srv := httptest.NewServer(LatencyEmbedder{Latency: 30 * time.Millisecond, Next: HashEmbedder{Dims: 8}})
	defer srv.Close()
	start := time.Now()
	resp, err := http.Post(srv.URL+"/v1/embeddings", "application/json", strings.NewReader(`{"input":["x"]}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if d := time.Since(start); d < 30*time.Millisecond || resp.StatusCode != http.StatusOK {
		t.Errorf("answered in %v with %d; want at least 30ms and 200", d, resp.StatusCode)
	}
}

// Defends the machine stamp on the platform CI runs: every field is read,
// none left blank.
func TestReadMachineFillsEveryField(t *testing.T) {
	m, err := ReadMachine()
	if err != nil {
		t.Skipf("machine facts unreadable here: %v", err)
	}
	if m.CPU == "" || m.Threads == 0 || m.RAMBytes == 0 || m.OS == "" || m.Kernel == "" {
		t.Errorf("blank machine field: %+v", m)
	}
}

// Defends the program against the real binary: the fake embedder speaks the
// shape llm-semantic expects, index and search both succeed through it, and
// every case records timings. It needs ./build/llm-semantic, which CI builds
// before the tests (make build).
func TestRunPerfAgainstTheBuiltBinary(t *testing.T) {
	cli, err := filepath.Abs(filepath.Join("..", "build", "llm-semantic"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cli); err != nil {
		t.Skip("no ./build/llm-semantic; run make build")
	}
	cfg := perfConfig(t, cli, 30)
	cfg.Latency = 0
	if err := RunPerf(context.Background(), cfg, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	r, err := ReadPerf(filepath.Join(cfg.OutDir, PerfJSONFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(r.Version, "v") && r.Version != "dev" {
		t.Errorf("version stamp %q", r.Version)
	}
}
