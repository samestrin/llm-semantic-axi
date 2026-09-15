package mcpserver

import (
	"strings"
	"testing"
)

// The argument builders are the translation layer between an MCP tool call and
// a CLI invocation: a parameter that is not translated is a parameter the model
// can set and nothing happens. These cover the ones the original suite left out.
//
// No t.Parallel in this package — several tests swap package-level state.

// argvHasFlag reports whether argv carries flag immediately followed by value.
func argvHasFlag(argv []string, flag, value string) bool {
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}

func argvHas(argv []string, want string) bool {
	for _, a := range argv {
		if a == want {
			return true
		}
	}
	return false
}

// --- memory stats ---

func TestBuildMemoryStatsArgs_BaseCommand(t *testing.T) {
	// memory_stats maps to the two-word `memory stats` subcommand. With no
	// arguments it must produce exactly that and nothing else, or a bare stats
	// call picks up flags the caller never asked for.
	got := buildMemoryStatsArgs(map[string]interface{}{})
	if strings.Join(got, " ") != "memory stats" {
		t.Errorf("buildMemoryStatsArgs({}) = %v, want [memory stats]", got)
	}
}

func TestBuildMemoryStatsArgs_TranslatesEveryParameter(t *testing.T) {
	// Every parameter the tool schema advertises has to reach the CLI under its
	// flag name. Note the snake_case-to-kebab-case renames (min_retrievals ->
	// --min-retrievals, older_than -> --older-than), which are the easiest thing
	// here to get wrong silently.
	argv := buildMemoryStatsArgs(map[string]interface{}{
		"id":             "mem-42",
		"min_retrievals": float64(3),
		"status":         "active",
		"tags":           "auth,security",
		"limit":          float64(25),
		"history":        true,
		"prune":          true,
		"older_than":     float64(90),
		"yes":            true,
		"storage":        "qdrant",
		"collection":     "llm_memory",
	})

	if argv[0] != "memory" || argv[1] != "stats" {
		t.Fatalf("buildMemoryStatsArgs did not start with [memory stats]: %v", argv)
	}
	for flag, value := range map[string]string{
		"--id":             "mem-42",
		"--min-retrievals": "3",
		"--status":         "active",
		"--tags":           "auth,security",
		"--limit":          "25",
		"--older-than":     "90",
		"--storage":        "qdrant",
		"--collection":     "llm_memory",
	} {
		if !argvHasFlag(argv, flag, value) {
			t.Errorf("missing %s %s in %v", flag, value, argv)
		}
	}
	for _, flag := range []string{"--history", "--prune", "--yes"} {
		if !argvHas(argv, flag) {
			t.Errorf("missing boolean flag %s in %v", flag, argv)
		}
	}
}

func TestBuildMemoryStatsArgs_OmitsUnsetAndFalseParameters(t *testing.T) {
	// A false boolean must produce no flag at all. Emitting `--prune` for
	// prune:false would delete memory entries the caller explicitly declined to
	// prune, and `--yes` would skip the confirmation guarding it.
	argv := buildMemoryStatsArgs(map[string]interface{}{
		"id":         "",
		"status":     "",
		"tags":       "",
		"history":    false,
		"prune":      false,
		"yes":        false,
		"storage":    "",
		"collection": "",
	})
	if strings.Join(argv, " ") != "memory stats" {
		t.Errorf("empty and false parameters produced flags: %v", argv)
	}
}

func TestBuildMemoryStatsArgs_LimitMustBePositiveButOlderThanNeedNotBe(t *testing.T) {
	// The two numeric guards differ on purpose. `--limit 0` would ask for zero
	// results, so a zero limit is dropped; `--older-than 0` means "everything",
	// which is a meaningful prune window and must survive.
	argv := buildMemoryStatsArgs(map[string]interface{}{
		"limit":          float64(0),
		"older_than":     float64(0),
		"min_retrievals": float64(0),
	})
	if argvHas(argv, "--limit") {
		t.Errorf("a zero limit was forwarded: %v", argv)
	}
	if !argvHasFlag(argv, "--older-than", "0") {
		t.Errorf("a zero older_than was dropped: %v", argv)
	}
	if !argvHasFlag(argv, "--min-retrievals", "0") {
		t.Errorf("a zero min_retrievals was dropped: %v", argv)
	}
}

// --- memory store: the provenance fields ---

func TestBuildMemoryStoreArgs_ForwardsTheProvenanceFields(t *testing.T) {
	// file_path, sprints and files are what let a stored memory be traced back
	// to the work that produced it. Dropping them stores the answer with no way
	// to tell which sprint or file it came from.
	argv := buildMemoryStoreArgs(map[string]interface{}{
		"question":  "why sqlite",
		"answer":    "no server to run",
		"file_path": "docs/adr/0001.md",
		"sprints":   "7.0_content_safety",
		"files":     "internal/store/sqlite.go,internal/store/store.go",
	})
	for flag, value := range map[string]string{
		"--file-path": "docs/adr/0001.md",
		"--sprints":   "7.0_content_safety",
		"--files":     "internal/store/sqlite.go,internal/store/store.go",
	} {
		if !argvHasFlag(argv, flag, value) {
			t.Errorf("missing %s %s in %v", flag, value, argv)
		}
	}
}

// --- index / index-update ---

func TestBuildIndexArgs_ForwardsProfileAndCalibrationFlags(t *testing.T) {
	// Calibration is the expensive part of an index run. --recalibrate forces it
	// after a model change and --skip-calibration suppresses it for a quick
	// rebuild; neither can be reached if the flag is not translated.
	argv := buildIndexArgs(map[string]interface{}{
		"path":             "./internal",
		"profile":          "code",
		"recalibrate":      true,
		"skip_calibration": true,
	})
	if !argvHasFlag(argv, "--profile", "code") {
		t.Errorf("--profile was not forwarded: %v", argv)
	}
	for _, flag := range []string{"--recalibrate", "--skip-calibration"} {
		if !argvHas(argv, flag) {
			t.Errorf("missing %s in %v", flag, argv)
		}
	}
}

func TestBuildIndexArgs_OmitsCalibrationFlagsWhenFalse(t *testing.T) {
	// recalibrate:false must not force a recalibration — the inverse of the test
	// above, and the one that would silently make every index run slow.
	argv := buildIndexArgs(map[string]interface{}{
		"recalibrate":      false,
		"skip_calibration": false,
		"profile":          "",
	})
	if argvHas(argv, "--recalibrate") || argvHas(argv, "--skip-calibration") || argvHas(argv, "--profile") {
		t.Errorf("false/empty parameters produced flags: %v", argv)
	}
}

func TestBuildUpdateArgs_ForwardsTheBackendSelection(t *testing.T) {
	// index_update has to be able to target the same backend the index was built
	// against. Without these three flags an update always writes to the default
	// sqlite collection, which is not where the data is.
	argv := buildUpdateArgs(map[string]interface{}{
		"path":       "./pkg",
		"storage":    "qdrant",
		"collection": "llm_code",
		"profile":    "code",
	})
	if argv[0] != "index-update" {
		t.Fatalf("subcommand = %q, want index-update", argv[0])
	}
	for flag, value := range map[string]string{
		"--storage":    "qdrant",
		"--collection": "llm_code",
		"--profile":    "code",
	} {
		if !argvHasFlag(argv, flag, value) {
			t.Errorf("missing %s %s in %v", flag, value, argv)
		}
	}
}

func TestBuildUpdateArgs_ForwardsGlobPatterns(t *testing.T) {
	// include/exclude arrive as a JSON array and each element becomes its own
	// repeated flag. Joining them into one argument would hand the CLI a single
	// pattern that matches nothing.
	argv := buildUpdateArgs(map[string]interface{}{
		"include": []interface{}{"*.go", "*.md", 42}, // the non-string is skipped
		"exclude": []interface{}{"vendor/**"},
	})
	for _, want := range []string{"*.go", "*.md"} {
		if !argvHasFlag(argv, "--include", want) {
			t.Errorf("missing --include %s in %v", want, argv)
		}
	}
	if !argvHasFlag(argv, "--exclude", "vendor/**") {
		t.Errorf("missing --exclude vendor/** in %v", argv)
	}
	if argvHas(argv, "42") {
		t.Errorf("a non-string pattern was forwarded: %v", argv)
	}
}

// --- search prefilter ---

func TestBuildSearchArgs_ForwardsThePrefilterParameters(t *testing.T) {
	// Prefiltering is what keeps a rerank affordable on a large index. Both the
	// switch and its width have to reach the CLI.
	argv := buildSearchArgs(map[string]interface{}{
		"query":         "auth",
		"prefilter":     true,
		"prefilter_top": float64(200),
	})
	if !argvHas(argv, "--prefilter") {
		t.Errorf("missing --prefilter in %v", argv)
	}
	if !argvHasFlag(argv, "--prefilter-top", "200") {
		t.Errorf("missing --prefilter-top 200 in %v", argv)
	}
}

func TestBuildSearchArgs_DropsANonPositivePrefilterWidth(t *testing.T) {
	// `--prefilter-top 0` would prefilter every candidate away and return an
	// empty result set that looks like "nothing matched".
	argv := buildSearchArgs(map[string]interface{}{
		"query":         "auth",
		"prefilter":     false,
		"prefilter_top": float64(0),
	})
	if argvHas(argv, "--prefilter") || argvHas(argv, "--prefilter-top") {
		t.Errorf("a disabled prefilter produced flags: %v", argv)
	}
}

// --- multisearch profile fallback ---

func TestBuildMultisearchArgs_AcceptsASingleProfileAsWellAsAList(t *testing.T) {
	// Callers write either `profiles: ["code","docs"]` or `profile: "code"`.
	// The CLI only understands --profiles, so the singular form has to be
	// converted rather than ignored.
	argv := buildMultisearchArgs(map[string]interface{}{
		"queries": []interface{}{"auth"},
		"profile": "code",
	})
	if !argvHasFlag(argv, "--profiles", "code") {
		t.Errorf("a single profile was not converted to --profiles: %v", argv)
	}
}

func TestBuildMultisearchArgs_PluralProfilesWinOverTheSingular(t *testing.T) {
	// When both are given the list is the more specific request. Emitting both
	// would pass --profiles twice and leave the winner up to flag parsing order.
	argv := buildMultisearchArgs(map[string]interface{}{
		"queries":  []interface{}{"auth"},
		"profiles": []interface{}{"code", "docs"},
		"profile":  "memory",
	})
	if !argvHasFlag(argv, "--profiles", "code,docs") {
		t.Errorf("the profiles list did not win: %v", argv)
	}
	n := 0
	for _, a := range argv {
		if a == "--profiles" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("--profiles appears %d times: %v", n, argv)
	}
}

// --- numeric coercion helpers ---

func TestGetInt_AcceptsEveryNumericShapeJSONCanProduce(t *testing.T) {
	// Arguments arrive from JSON as float64, but the same map is built directly
	// in Go elsewhere with int/int32/int64, and a client may send a numeric
	// string. Every one has to coerce, or a legitimate top_k is dropped.
	for name, tc := range map[string]struct {
		in   interface{}
		want int
		ok   bool
	}{
		"int":             {42, 42, true},
		"int32":           {int32(7), 7, true},
		"int64":           {int64(9), 9, true},
		"float64":         {float64(5), 5, true},
		"float32":         {float32(3), 3, true},
		"float truncates": {float64(4.9), 4, true},
		"numeric string":  {"11", 11, true},
		"non-numeric":     {"eleven", 0, false},
		"bool":            {true, 0, false},
		"missing":         {nil, 0, false},
	} {
		t.Run(name, func(t *testing.T) {
			args := map[string]interface{}{}
			if tc.in != nil {
				args["k"] = tc.in
			}
			got, ok := getInt(args, "k")
			if ok != tc.ok || got != tc.want {
				t.Errorf("getInt(%#v) = (%d, %v), want (%d, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestGetFloat_AcceptsEveryNumericShapeJSONCanProduce(t *testing.T) {
	// Same contract for the float-valued parameters (threshold, fusion_alpha,
	// recency_factor). An int literal in a config or a stringified decimal must
	// not be silently discarded.
	for name, tc := range map[string]struct {
		in   interface{}
		want float64
		ok   bool
	}{
		"float64":        {0.75, 0.75, true},
		"float32":        {float32(0.5), 0.5, true},
		"int":            {1, 1, true},
		"int64":          {int64(2), 2, true},
		"decimal string": {"0.25", 0.25, true},
		"non-numeric":    {"high", 0, false},
		"bool":           {false, 0, false},
		"missing":        {nil, 0, false},
	} {
		t.Run(name, func(t *testing.T) {
			args := map[string]interface{}{}
			if tc.in != nil {
				args["v"] = tc.in
			}
			got, ok := getFloat(args, "v")
			if ok != tc.ok || got != tc.want {
				t.Errorf("getFloat(%#v) = (%g, %v), want (%g, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestGetThreshold_ClampsToTheUnitInterval(t *testing.T) {
	// A similarity threshold outside [0,1] is not an error the model can
	// recover from — it is a value that would make the CLI return everything or
	// nothing. Clamping keeps the call useful instead of failing it.
	for name, tc := range map[string]struct {
		in   interface{}
		want float64
	}{
		"below zero":      {-0.5, 0.0},
		"at zero":         {0.0, 0.0},
		"in range":        {0.42, 0.42},
		"at one":          {1.0, 1.0},
		"above one":       {1.7, 1.0},
		"huge":            {float64(1e9), 1.0},
		"negative string": {"-3", 0.0},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := getThreshold(map[string]interface{}{"threshold": tc.in})
			if !ok {
				t.Fatalf("getThreshold(%#v) reported no value", tc.in)
			}
			if got != tc.want {
				t.Errorf("getThreshold(%#v) = %g, want %g", tc.in, got, tc.want)
			}
		})
	}

	if _, ok := getThreshold(map[string]interface{}{}); ok {
		t.Error("getThreshold reported a value when none was set")
	}
}

func TestValidateMemorySearchArgs_RequiresANonEmptyQuery(t *testing.T) {
	// An empty memory search would embed the empty string and return whatever
	// happens to sit closest to the origin — results that look real and mean
	// nothing.
	for name, args := range map[string]map[string]interface{}{
		"missing":     {},
		"empty":       {"query": ""},
		"whitespace":  {"query": " \t\n"},
		"wrong type":  {"query": 42},
		"nil literal": {"query": nil},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateMemorySearchArgs(args); err == nil {
				t.Error("an unusable query was accepted")
			}
		})
	}

	if err := validateMemorySearchArgs(map[string]interface{}{"query": "auth"}); err != nil {
		t.Errorf("a valid query was rejected: %v", err)
	}
}

func TestRegisteredCommands_MatchesTheRegistry(t *testing.T) {
	// RegisteredCommands is what the tool-definition audit checks against. If it
	// drifted from commandRegistry the audit would pass while the server was
	// advertising something it cannot run.
	got := RegisteredCommands()
	if len(got) != len(commandRegistry) {
		t.Fatalf("RegisteredCommands returned %d names for %d registry entries", len(got), len(commandRegistry))
	}
	for _, name := range got {
		if _, ok := commandRegistry[name]; !ok {
			t.Errorf("RegisteredCommands returned %q, which is not in the registry", name)
		}
	}
}
