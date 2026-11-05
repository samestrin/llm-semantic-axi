package benchmarks

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func loadTestTokenizer(t *testing.T) *Tokenizer {
	t.Helper()
	tok, err := LoadCL100K(RanksFile)
	if err != nil {
		t.Fatalf("LoadCL100K: %v", err)
	}
	return tok
}

// Defends the "cl100k-compatible" claim in results-tokens.md: the IDs below
// are what OpenAI's tiktoken returns for cl100k_base. A different vocabulary
// or split pattern produces different IDs.
func TestCL100KMatchesKnownTokenIDs(t *testing.T) {
	tok := loadTestTokenizer(t)
	cases := map[string][]int{
		"hello world":        {15339, 1917},
		"tiktoken is great!": {83, 1609, 5963, 374, 2294, 0},
		"1234567":            {4513, 10961, 22}, // digits split in groups of at most three
	}
	for text, want := range cases {
		if got := tok.Encode(text); !reflect.DeepEqual(got, want) {
			t.Errorf("Encode(%q) = %v, want %v", text, got, want)
		}
		if got := tok.Count(text); got != len(want) {
			t.Errorf("Count(%q) = %d, want %d", text, got, len(want))
		}
	}
}

// Defends against counting special-token text as one token: output that
// happens to contain "<|endoftext|>" is ordinary text to a reader.
func TestCountTreatsSpecialTokenTextAsOrdinary(t *testing.T) {
	tok := loadTestTokenizer(t)
	if got := tok.Count("<|endoftext|>"); got <= 1 {
		t.Errorf("Count(<|endoftext|>) = %d, want more than 1", got)
	}
}

// Defends against silently counting with a different vocabulary: an edited
// ranks file must be refused, not loaded.
func TestLoadCL100KRefusesAModifiedRanksFile(t *testing.T) {
	data, err := os.ReadFile(RanksFile)
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "cl100k_base.tiktoken")
	if err := os.WriteFile(bad, append(data, []byte("IQ== 999999\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = LoadCL100K(bad)
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("LoadCL100K(modified) error = %v, want a sha256 mismatch", err)
	}
}

func TestLoadCL100KMissingFile(t *testing.T) {
	if _, err := LoadCL100K(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("LoadCL100K(missing) returned no error")
	}
}

// writeOutputs builds a minimal benchmark directory: a query list and one
// output file per query per format.
func writeOutputs(t *testing.T, queries []string, body func(f Format, i int) string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, QueriesFile), []byte(strings.Join(queries, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range Formats {
		if err := os.MkdirAll(filepath.Join(dir, OutputsDir, f.Dir), 0o755); err != nil {
			t.Fatal(err)
		}
		for i := range queries {
			if err := os.WriteFile(OutputPath(dir, f, i), []byte(body(f, i)), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dir
}

func TestCountSumsEachFormat(t *testing.T) {
	tok := loadTestTokenizer(t)
	dir := writeOutputs(t, []string{"first query", "second query"}, func(f Format, i int) string {
		if f.Dir == "json" {
			return "hello world" // 2 tokens
		}
		return "hello" // 1 token
	})
	res, err := Count(tok, dir)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if len(res.Queries) != 2 {
		t.Fatalf("queries = %d, want 2", len(res.Queries))
	}
	want := map[string]int{"json": 4, "axi": 2, "plain": 2}
	for _, f := range Formats {
		if got := res.Total(f).Tokens; got != want[f.Dir] {
			t.Errorf("%s tokens = %d, want %d", f.Dir, got, want[f.Dir])
		}
	}
	if got := res.Total(Formats[0]).Bytes; got != 22 {
		t.Errorf("json bytes = %d, want 22", got)
	}
}

// Defends against a total that compares different query sets: a query whose
// output is missing in one format must stop the count, not shrink that total.
func TestCountRefusesAMissingOutput(t *testing.T) {
	tok := loadTestTokenizer(t)
	dir := writeOutputs(t, []string{"a", "b"}, func(Format, int) string { return "x" })
	if err := os.Remove(OutputPath(dir, Formats[1], 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := Count(tok, dir); err == nil {
		t.Fatal("Count with a missing output returned no error")
	}
}

// Defends against an output with no file behind a query: a stray file for a
// query that is not in the list means the list and the outputs disagree.
func TestCountRefusesAStrayOutput(t *testing.T) {
	tok := loadTestTokenizer(t)
	dir := writeOutputs(t, []string{"a"}, func(Format, int) string { return "x" })
	if err := os.WriteFile(OutputPath(dir, Formats[2], 1), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Count(tok, dir); err == nil {
		t.Fatal("Count with a stray output returned no error")
	}
}

// Defends against an empty output counting as 0 tokens and making a format
// look free: an empty file is a failed render, not a cheap one.
func TestCountRefusesAnEmptyOutput(t *testing.T) {
	tok := loadTestTokenizer(t)
	dir := writeOutputs(t, []string{"a"}, func(f Format, _ int) string {
		if f.Dir == "axi" {
			return ""
		}
		return "x"
	})
	if _, err := Count(tok, dir); err == nil {
		t.Fatal("Count with an empty output returned no error")
	}
}

func TestCountRefusesAnEmptyQueryList(t *testing.T) {
	tok := loadTestTokenizer(t)
	dir := writeOutputs(t, nil, nil)
	if _, err := Count(tok, dir); err == nil {
		t.Fatal("Count with no queries returned no error")
	}
}

func TestRenderNumbersAndRatios(t *testing.T) {
	res := &Result{
		Queries: []string{"q one", "q|two"},
		Counts: map[string][]Measure{
			"json":  {{Tokens: 100, Bytes: 400}, {Tokens: 300, Bytes: 1200}},
			"axi":   {{Tokens: 50, Bytes: 150}, {Tokens: 50, Bytes: 150}},
			"plain": {{Tokens: 80, Bytes: 300}, {Tokens: 120, Bytes: 500}},
		},
	}
	got := Render(res)
	for _, want := range []string{
		"| JSON | `--json` | 400 | 1600 | 200.0 | 100.0% |",
		"| TOON | `--axi` | 100 | 300 | 50.0 | 25.0% |",
		"| Plain | *(none)* | 200 | 800 | 100.0 | 50.0% |",
		"| 1 | q one | 100 | 50 | 80 |",
		`| 2 | q\|two | 300 | 50 | 120 |`,
		"On these 2 queries, `--axi` printed 25.0% of the tokens `--json` printed, and plain output printed 50.0%.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Render missing line %q in:\n%s", want, got)
		}
	}
}

// Defends the task's "Done when": the numbers in results-tokens.md must be
// what the checked-in outputs re-count to, offline. Edit an output or the
// table by hand and this goes red.
func TestResultsMarkdownMatchesRecount(t *testing.T) {
	tok := loadTestTokenizer(t)
	res, err := Count(tok, ".")
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	block, err := ReadBlock(ResultsFile)
	if err != nil {
		t.Fatalf("ReadBlock: %v", err)
	}
	if want := Render(res); block != want {
		t.Fatalf("%s is out of date with the checked-in outputs; run `make bench-tokens-write`.\n--- in file ---\n%s\n--- recounted ---\n%s", ResultsFile, block, want)
	}
}

func TestWriteBlockReplacesOnlyTheMarkedRegion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.md")
	orig := "intro\n" + BlockBegin + "\nold\n" + BlockEnd + "\noutro\n"
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteBlock(path, "new table\n"); err != nil {
		t.Fatalf("WriteBlock: %v", err)
	}
	data, _ := os.ReadFile(path)
	if want := "intro\n" + BlockBegin + "\nnew table\n" + BlockEnd + "\noutro\n"; string(data) != want {
		t.Fatalf("file = %q, want %q", data, want)
	}
	got, err := ReadBlock(path)
	if err != nil || got != "new table\n" {
		t.Fatalf("ReadBlock = %q, %v", got, err)
	}
}

// Defends against a results file with no marked region passing the check
// because there was nothing to compare.
func TestReadBlockRefusesAFileWithoutMarkers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.md")
	if err := os.WriteFile(path, []byte("no markers here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBlock(path); err == nil {
		t.Fatal("ReadBlock without markers returned no error")
	}
	if err := WriteBlock(path, "x"); err == nil {
		t.Fatal("WriteBlock without markers returned no error")
	}
}

func TestRunCountReportsMatchAndMismatch(t *testing.T) {
	tok := loadTestTokenizer(t)
	dir := writeOutputs(t, []string{"a"}, func(Format, int) string { return "hello" })
	ranks, _ := filepath.Abs(RanksFile)
	if err := os.Symlink(ranks, filepath.Join(dir, RanksFile)); err != nil {
		t.Fatal(err)
	}
	res, err := Count(tok, dir)
	if err != nil {
		t.Fatal(err)
	}
	results := filepath.Join(dir, ResultsFile)
	stale := "# r\n" + BlockBegin + "\nstale\n" + BlockEnd + "\n"
	if err := os.WriteFile(results, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := RunCount(dir, false, &out); err == nil {
		t.Fatal("RunCount against a stale results file returned no error")
	}
	if !strings.Contains(out.String(), Render(res)) {
		t.Errorf("RunCount did not print the recounted table:\n%s", out.String())
	}

	out.Reset()
	if err := RunCount(dir, true, &out); err != nil {
		t.Fatalf("RunCount -write: %v", err)
	}
	out.Reset()
	if err := RunCount(dir, false, &out); err != nil {
		t.Fatalf("RunCount after -write: %v", err)
	}
	if !strings.Contains(out.String(), "matches") {
		t.Errorf("RunCount did not say the file matches:\n%s", out.String())
	}
}
