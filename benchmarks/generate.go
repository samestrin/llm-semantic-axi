package benchmarks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// PinnedCommit is the commit of this repository the outputs were generated
// from: both the indexed source and the CLI that rendered the results.
const PinnedCommit = "9ff21f649235f6cccc26ee86a6f8d7c84aecd853"

// CheckoutDir is where Generate checks the pinned commit out. It is fixed
// because the CLI hashes each chunk's absolute path into its id and embeds
// the path with the chunk, so a checkout at a fresh random path changed every
// id and moved scores on every run. The outputs show it as /work/llm-semantic-axi.
const CheckoutDir = "/tmp/llm-semantic-axi-bench"

// HashEmbedder is the stand-in embedding endpoint used to generate the
// outputs. Text is split into words (identifiers at camelCase and _
// boundaries), lower-cased, stopwords dropped, and each word hashed (FNV-1a)
// into one of Dims buckets; the counts are normalised to a unit vector. It is
// deterministic and local; it is a keyword match, not a semantic model, which
// is enough here because the benchmark compares formats over identical
// result sets.
type HashEmbedder struct {
	Dims int
}

var stopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "at": true, "be": true, "by": true,
	"does": true, "for": true, "from": true, "how": true, "in": true, "is": true, "it": true,
	"of": true, "on": true, "or": true, "the": true, "to": true, "what": true, "where": true,
	"which": true, "with": true,
}

// words splits text into lower-cased words, breaking identifiers at
// lower-to-upper case changes.
func words(text string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			if w := strings.ToLower(string(cur)); !stopwords[w] {
				out = append(out, w)
			}
			cur = cur[:0]
		}
	}
	var prev rune
	for _, r := range text {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r) && unicode.IsLower(prev):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
		prev = r
	}
	flush()
	return out
}

// Vector embeds text.
func (h HashEmbedder) Vector(text string) []float64 {
	v := make([]float64, h.Dims)
	for _, w := range words(text) {
		f := fnv.New32a()
		f.Write([]byte(w))
		v[f.Sum32()%uint32(h.Dims)]++
	}
	var norm float64
	for _, x := range v {
		norm += x * x
	}
	if norm > 0 {
		norm = math.Sqrt(norm)
		for i := range v {
			v[i] /= norm
		}
	}
	return v
}

// ServeHTTP answers an OpenAI-compatible /v1/embeddings request.
func (h HashEmbedder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Input json.RawMessage `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var inputs []string
	if err := json.Unmarshal(req.Input, &inputs); err != nil {
		var one string
		if err := json.Unmarshal(req.Input, &one); err != nil {
			http.Error(w, "input must be a string or an array of strings", http.StatusBadRequest)
			return
		}
		inputs = []string{one}
	}
	type item struct {
		Object    string    `json:"object"`
		Index     int       `json:"index"`
		Embedding []float64 `json:"embedding"`
	}
	data := make([]item, len(inputs))
	for i, s := range inputs {
		data[i] = item{Object: "embedding", Index: i, Embedding: h.Vector(s)}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data, "model": "hash"})
}

// countLine finds the result count each non-JSON mode states: `total: N`
// in --axi and `N results for "<query>"` in plain output.
var countLine = map[string]*regexp.Regexp{
	"axi":   regexp.MustCompile(`(?m)^total: (\d+)$`),
	"plain": regexp.MustCompile(`(?m)^(\d+) results for "`),
}

// indexedAt finds the time of the index run that --axi reports with its
// staleness.
var indexedAt = regexp.MustCompile(`(?m)^(\s*indexed_at: )"[^"]*"$`)

// RunConfig drives RunQueries.
type RunConfig struct {
	CLI     string   // llm-semantic binary
	Dir     string   // working directory: the indexed checkout
	APIURL  string   // embedding endpoint
	Model   string   // embedding model name
	Queries []string // the fixed query list
	OutDir  string   // benchmark directory; outputs land under OutDir/outputs
	Roots   []string // spellings of the checkout path, rewritten to RootAs
	RootAs  string   // the path the outputs show instead
	Leak    string   // a path that must not survive the rewrite
	Env     []string // the CLI's environment; nil inherits this process's
	// IndexedAt, if set, replaces the time of the index run in --axi's
	// indexed_at, which would otherwise differ on every run.
	IndexedAt string
}

// RunQueries runs every query in every format and writes the stdout of each.
// It fails rather than write an output for a search that errored or found
// nothing, so no checked-in number measures an empty answer.
func RunQueries(ctx context.Context, cfg RunConfig) error {
	for _, f := range Formats {
		if err := os.MkdirAll(filepath.Join(cfg.OutDir, OutputsDir, f.Dir), 0o755); err != nil {
			return err
		}
	}
	for i, q := range cfg.Queries {
		var want int // results in --json; Formats runs it first
		for _, f := range Formats {
			args := append([]string{"search", q, "--api-url", cfg.APIURL, "--model", cfg.Model}, f.Flags...)
			cmd := exec.CommandContext(ctx, cfg.CLI, args...)
			cmd.Dir = cfg.Dir
			cmd.Env = cfg.Env
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("query %d %q %s: %w: %s", i+1, q, f.Dir, err, stderr.String())
			}
			if f.Dir == "json" {
				var results []json.RawMessage
				if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
					return fmt.Errorf("query %d %q: --json output is not a result array: %w", i+1, q, err)
				}
				if len(results) == 0 {
					return fmt.Errorf("query %d %q: no results; an empty search is not a benchmark", i+1, q)
				}
				want = len(results)
			} else {
				m := countLine[f.Dir].FindStringSubmatch(stdout.String())
				if m == nil {
					return fmt.Errorf("query %d %q: %s output states no result count; --json has %d", i+1, q, f.Dir, want)
				}
				if n, _ := strconv.Atoi(m[1]); n != want {
					return fmt.Errorf("query %d %q: %s output has %d results; --json has %d", i+1, q, f.Dir, n, want)
				}
			}
			out := stdout.String()
			for _, root := range cfg.Roots {
				out = strings.ReplaceAll(out, root, cfg.RootAs)
			}
			if cfg.IndexedAt != "" {
				out = indexedAt.ReplaceAllString(out, `${1}"`+cfg.IndexedAt+`"`)
			}
			if cfg.Leak != "" && strings.Contains(out, cfg.Leak) {
				return fmt.Errorf("query %d %q %s: output still holds the temporary path %s", i+1, q, f.Dir, cfg.Leak)
			}
			if err := os.WriteFile(OutputPath(cfg.OutDir, f, i), []byte(out), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// Generate regenerates the outputs under benchDir from the pinned commit of
// the repository at repo: it checks the commit out into a worktree at
// checkout (normally CheckoutDir), builds the CLI from it, indexes it against
// a loopback HashEmbedder and runs the query list. Two runs at the same
// checkout write the same bytes: the index time --axi reports is rewritten to
// the commit's time. Nothing leaves the machine.
func Generate(ctx context.Context, repo, commit, checkout, benchDir string, log func(string)) (err error) {
	queries, err := ReadQueries(filepath.Join(benchDir, QueriesFile))
	if err != nil {
		return err
	}
	if len(queries) == 0 {
		return errors.New("no queries")
	}
	src, err := filepath.Abs(checkout)
	if err != nil {
		return err
	}
	// The worktree is removed when the run ends, so never take over a path
	// that is already there.
	if _, err := os.Lstat(src); !os.IsNotExist(err) {
		return fmt.Errorf("checkout %s already exists: another run is using it, or one died; remove it and run `git worktree prune`", src)
	}
	tmp, err := os.MkdirTemp("", "llm-semantic-bench-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	// Index calibration picks its probe chunks with the global math/rand,
	// and the probes set the thresholds behind every relevance label; the CLI
	// gets a fixed seed so the labels repeat.
	seeded := append(os.Environ(), "GODEBUG=randautoseed=0")
	runEnv := func(env []string, dir, name string, args ...string) (string, error) {
		log(fmt.Sprintf("$ %s %s", name, strings.Join(args, " ")))
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir
		cmd.Env = env
		var out, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("%s %s: %w\n%s%s", name, strings.Join(args, " "), err, out.String(), stderr.String())
		}
		return out.String(), nil
	}
	run := func(dir, name string, args ...string) (string, error) {
		return runEnv(nil, dir, name, args...)
	}

	committed, err := run(repo, "git", "show", "-s", "--format=%ct", commit)
	if err != nil {
		return err
	}
	unix, err := strconv.ParseInt(strings.TrimSpace(committed), 10, 64)
	if err != nil {
		return fmt.Errorf("commit time of %s: %w", commit, err)
	}
	if _, err := run(repo, "git", "worktree", "add", "--detach", src, commit); err != nil {
		return err
	}
	defer func() {
		if _, rmErr := run(repo, "git", "worktree", "remove", "--force", src); rmErr != nil && err == nil {
			err = rmErr
		}
	}()
	cli := filepath.Join(tmp, "llm-semantic")
	if _, err := run(src, "go", "build", "-o", cli, "./cmd/llm-semantic"); err != nil {
		return err
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: HashEmbedder{Dims: 4096}}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	apiURL := "http://" + ln.Addr().String()
	const model = "hash-4096"

	if _, err := runEnv(seeded, src, cli, "index", ".", "--api-url", apiURL, "--model", model); err != nil {
		return err
	}
	roots := []string{src}
	if real, err := filepath.EvalSymlinks(src); err == nil && real != src {
		roots = append([]string{real}, roots...)
	}
	log(fmt.Sprintf("running %d queries in %d formats", len(queries), len(Formats)))
	return RunQueries(ctx, RunConfig{
		CLI: cli, Dir: src, APIURL: apiURL, Model: model, Queries: queries,
		OutDir: benchDir, Roots: roots, RootAs: "/work/llm-semantic-axi", Leak: src,
		Env: seeded, IndexedAt: time.Unix(unix, 0).UTC().Format(time.RFC3339),
	})
}
