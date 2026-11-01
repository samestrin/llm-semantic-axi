package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/samestrin/llm-semantic-axi/internal/semantic/config"
	"github.com/spf13/cobra"
)

// ===== getAPIKey =====

// TestGetAPIKeyPrefersTheFlag pins the one rule this accessor encodes: the
// flag wins, and the environment fallback is the caller's job.
func TestGetAPIKeyPrefersTheFlag(t *testing.T) {
	newWorkspace(t)

	if got := getAPIKey(); got != "" {
		t.Errorf("getAPIKey with no flag = %q, want an empty string", got)
	}
	apiKey = "sk-from-flag"
	if got := getAPIKey(); got != "sk-from-flag" {
		t.Errorf("getAPIKey = %q, want the flag value", got)
	}
}

// ===== createEmbedder =====

// TestCreateEmbedderBuildsEachProvider walks every branch of the --embedder
// switch. An unmapped provider name falls through to the default, which would
// silently send a Cohere key to an OpenAI endpoint.
func TestCreateEmbedderBuildsEachProvider(t *testing.T) {
	tests := []struct {
		provider string
		envVar   string
	}{
		{"cohere", "COHERE_API_KEY"},
		{"huggingface", "HUGGING_FACE_API_KEY"},
		{"openrouter", "OPENROUTER_API_KEY"},
		{"openai", "LLM_SEMANTIC_API_KEY"},
		{"", "LLM_SEMANTIC_API_KEY"},
	}
	for _, tt := range tests {
		name := tt.provider
		if name == "" {
			name = "empty defaults to openai"
		}
		t.Run(name, func(t *testing.T) {
			newWorkspace(t)
			t.Setenv(tt.envVar, "key-from-env")
			t.Cleanup(SetEmbedderTypeForTesting(tt.provider))

			embedder, err := createEmbedder()
			if err != nil {
				t.Fatalf("createEmbedder(%q): %v", tt.provider, err)
			}
			if embedder == nil {
				t.Fatalf("createEmbedder(%q) returned nil", tt.provider)
			}
			if embedder.Model() == "" {
				t.Errorf("createEmbedder(%q) produced an embedder with no model", tt.provider)
			}
		})
	}
}

// TestCreateEmbedderDefaultsTheOpenAIModel: an empty --model must resolve to a
// working default rather than an empty model name the API rejects.
func TestCreateEmbedderDefaultsTheOpenAIModel(t *testing.T) {
	newWorkspace(t)
	model = ""

	embedder, err := createEmbedder()
	if err != nil {
		t.Fatalf("createEmbedder: %v", err)
	}
	if embedder.Model() != "nomic-embed-text" {
		t.Errorf("model = %q, want the documented default", embedder.Model())
	}
}

// TestCreateEmbedderHonoursAnExplicitModel is the other half.
func TestCreateEmbedderHonoursAnExplicitModel(t *testing.T) {
	newWorkspace(t)
	model = "mxbai-embed-large"

	embedder, err := createEmbedder()
	if err != nil {
		t.Fatalf("createEmbedder: %v", err)
	}
	if embedder.Model() != "mxbai-embed-large" {
		t.Errorf("model = %q, want the flag value", embedder.Model())
	}
}

// TestCreateEmbedderRejectsAnUnknownProvider names the value it did not
// understand.
func TestCreateEmbedderRejectsAnUnknownProvider(t *testing.T) {
	newWorkspace(t)
	t.Cleanup(SetEmbedderTypeForTesting("gemini"))

	_, err := createEmbedder()
	if err == nil {
		t.Fatal("an unknown embedder provider was accepted")
	}
	if !strings.Contains(err.Error(), "gemini") {
		t.Errorf("error = %v, want it to quote the rejected value", err)
	}
}

// TestCreateEmbedderFallsBackThroughTheHuggingFaceKeyNames covers the three
// environment variables the provider accepts; missing one sends an
// unauthenticated request that fails much later.
func TestCreateEmbedderFallsBackThroughTheHuggingFaceKeyNames(t *testing.T) {
	for _, name := range []string{"HUGGING_FACE_API_KEY", "HUGGINGFACE_API_KEY", "HF_TOKEN"} {
		t.Run(name, func(t *testing.T) {
			newWorkspace(t)
			for _, clear := range []string{"HUGGING_FACE_API_KEY", "HUGGINGFACE_API_KEY", "HF_TOKEN"} {
				t.Setenv(clear, "")
			}
			t.Setenv(name, "hf-key")
			t.Cleanup(SetEmbedderTypeForTesting("huggingface"))

			if _, err := createEmbedder(); err != nil {
				t.Fatalf("createEmbedder with %s set: %v", name, err)
			}
		})
	}
}

// ===== probeEmbeddingDim =====

// TestProbeEmbeddingDimSkipsSQLite: SQLite stores variable-length blobs and
// never reads the dimension, so probing would cost a live embedder call on
// every command for nothing.
func TestProbeEmbeddingDimSkipsSQLite(t *testing.T) {
	newWorkspace(t)
	fe := newFakeEmbedder(t)
	useEmbedder(t, fe)
	embedder, err := createEmbedder()
	if err != nil {
		t.Fatalf("createEmbedder: %v", err)
	}

	dim, err := probeEmbeddingDim(context.Background(), embedder)
	if err != nil {
		t.Fatalf("probeEmbeddingDim: %v", err)
	}
	if dim != 0 {
		t.Errorf("dim = %d, want 0 for sqlite", dim)
	}
	if fe.callCount() != 0 {
		t.Errorf("sqlite made %d embedding calls, want none", fe.callCount())
	}
}

// TestProbeEmbeddingDimSurfacesAProbeFailureForQdrant: creating a collection
// with a zero dimension is unrecoverable, so the probe must fail loudly.
func TestProbeEmbeddingDimSurfacesAProbeFailureForQdrant(t *testing.T) {
	newWorkspace(t)
	useEmbedder(t, newBrokenEmbedder(t))
	t.Cleanup(SetStorageTypeForTesting("qdrant"))
	embedder, err := createEmbedder()
	if err != nil {
		t.Fatalf("createEmbedder: %v", err)
	}

	_, probeErr := probeEmbeddingDim(context.Background(), embedder)
	if probeErr == nil {
		t.Fatal("a failing probe returned nil")
	}
	if !strings.Contains(probeErr.Error(), "probe embedder for dimensions") {
		t.Errorf("error = %v, want it to name the probe", probeErr)
	}
}

// ===== resolveCollectionName =====

// TestResolveCollectionNamePriority walks the documented order. Getting it
// wrong points two profiles at one Qdrant collection, which silently merges
// their indexes.
func TestResolveCollectionNamePriority(t *testing.T) {
	t.Run("flag wins", func(t *testing.T) {
		newWorkspace(t)
		t.Cleanup(SetConfigForTesting(&config.SemanticConfig{CodeCollection: "from_config"}))
		t.Cleanup(SetCollectionNameForTesting("from_flag"))
		indexDir = "indexes/derived"
		profile = "code"

		if got := resolveCollectionName(); got != "from_flag" {
			t.Errorf("resolveCollectionName = %q, want the flag value", got)
		}
	})

	t.Run("config beats the derived name", func(t *testing.T) {
		newWorkspace(t)
		t.Cleanup(SetConfigForTesting(&config.SemanticConfig{CodeCollection: "from_config"}))
		indexDir = "indexes/derived"
		profile = "code"

		if got := resolveCollectionName(); got != "from_config" {
			t.Errorf("resolveCollectionName = %q, want the config value", got)
		}
	})

	t.Run("derived from a custom index dir", func(t *testing.T) {
		newWorkspace(t)
		t.Setenv("QDRANT_COLLECTION", "from_env")
		indexDir = "indexes/derived"

		if got := resolveCollectionName(); got != "derived" {
			t.Errorf("resolveCollectionName = %q, want the name derived from --index-dir", got)
		}
	})

	t.Run("environment beats the default", func(t *testing.T) {
		newWorkspace(t)
		t.Setenv("QDRANT_COLLECTION", "from_env")

		if got := resolveCollectionName(); got != "from_env" {
			t.Errorf("resolveCollectionName = %q, want the environment value", got)
		}
	})

	t.Run("falls back to the documented default", func(t *testing.T) {
		newWorkspace(t)
		t.Setenv("QDRANT_COLLECTION", "")

		if got := resolveCollectionName(); got != "llm_semantic" {
			t.Errorf("resolveCollectionName = %q, want llm_semantic", got)
		}
	})
}

// ===== resolveStorageType =====

// TestResolveStorageTypePriority: a config that overrode an explicitly passed
// --storage would send a caller's data to the wrong backend.
func TestResolveStorageTypePriority(t *testing.T) {
	storageFlagCmd := func(value string, changed bool) *cobra.Command {
		cmd := &cobra.Command{Use: "probe"}
		var s string
		cmd.Flags().StringVar(&s, "storage", "sqlite", "")
		if changed {
			if err := cmd.Flags().Set("storage", value); err != nil {
				t.Fatalf("Set storage: %v", err)
			}
		}
		return cmd
	}

	t.Run("explicit flag wins", func(t *testing.T) {
		newWorkspace(t)
		t.Cleanup(SetConfigForTesting(&config.SemanticConfig{CodeStorage: "qdrant"}))
		t.Cleanup(SetStorageTypeForTesting("sqlite"))
		profile = "code"

		if got := resolveStorageType(storageFlagCmd("sqlite", true)); got != "sqlite" {
			t.Errorf("resolveStorageType = %q, want the explicit flag", got)
		}
	})

	t.Run("config applies when the flag is untouched", func(t *testing.T) {
		newWorkspace(t)
		t.Cleanup(SetConfigForTesting(&config.SemanticConfig{CodeStorage: "qdrant"}))
		profile = "code"

		if got := resolveStorageType(storageFlagCmd("", false)); got != "qdrant" {
			t.Errorf("resolveStorageType = %q, want the configured backend", got)
		}
	})

	t.Run("default survives with no config", func(t *testing.T) {
		newWorkspace(t)
		t.Cleanup(SetConfigForTesting(nil))

		if got := resolveStorageType(storageFlagCmd("", false)); got != "sqlite" {
			t.Errorf("resolveStorageType = %q, want sqlite", got)
		}
	})
}

// ===== default accessors =====

// TestGetDefaultAPIURLAndModelReadTheEnvironment pins both accessors, which
// are evaluated once at flag-registration time.
func TestGetDefaultAPIURLAndModelReadTheEnvironment(t *testing.T) {
	t.Run("from the environment", func(t *testing.T) {
		t.Setenv("LLM_SEMANTIC_API_URL", "http://tei.invalid:9999")
		t.Setenv("LLM_SEMANTIC_MODEL", "custom-embed")

		if got := getDefaultAPIURL(); got != "http://tei.invalid:9999" {
			t.Errorf("getDefaultAPIURL = %q, want the environment value", got)
		}
		if got := getDefaultModel(); got != "custom-embed" {
			t.Errorf("getDefaultModel = %q, want the environment value", got)
		}
	})

	t.Run("documented fallbacks", func(t *testing.T) {
		t.Setenv("LLM_SEMANTIC_API_URL", "")
		t.Setenv("LLM_SEMANTIC_MODEL", "")

		if got := getDefaultAPIURL(); got != "http://localhost:8080" {
			t.Errorf("getDefaultAPIURL = %q, want the TEI default", got)
		}
		// Empty on purpose: the embedder picks its own default, which differs
		// per provider.
		if got := getDefaultModel(); got != "" {
			t.Errorf("getDefaultModel = %q, want an empty string", got)
		}
	})
}

// ===== testing helpers =====

// TestSettersRestoreThePreviousValue: every one of these returns a cleanup, and
// a cleanup that does not restore leaks the override into later tests. That is
// exactly the cross-test corruption these helpers exist to prevent.
func TestSettersRestoreThePreviousValue(t *testing.T) {
	newWorkspace(t)
	embedderType, apiURL, collectionName, storageType = "openai", "http://a", "", "sqlite"

	restoreEmbedder := SetEmbedderTypeForTesting("cohere")
	restoreURL := SetAPIURLForTesting("http://b")
	restoreCollection := SetCollectionNameForTesting("custom")
	restoreStorage := SetStorageTypeForTesting("qdrant")

	if embedderType != "cohere" || apiURL != "http://b" || collectionName != "custom" || storageType != "qdrant" {
		t.Fatalf("setters did not apply: %q %q %q %q", embedderType, apiURL, collectionName, storageType)
	}

	restoreEmbedder()
	restoreURL()
	restoreCollection()
	restoreStorage()

	if embedderType != "openai" || apiURL != "http://a" || collectionName != "" || storageType != "sqlite" {
		t.Errorf("cleanups did not restore: %q %q %q %q", embedderType, apiURL, collectionName, storageType)
	}
}

// ===== RootCmd PersistentPreRunE =====

// TestRootCmdLoadsAConfigFile proves the auto-load actually happens before a
// subcommand runs, which is what makes profile-scoped settings work at all.
func TestRootCmdLoadsAConfigFile(t *testing.T) {
	dir := newWorkspace(t)
	cfgPath := writeFile(t, dir+"/semantic.yaml", "semantic:\n  code_collection: configured_code\n")

	root := RootCmd()
	// the error is expected (no index); only the config load matters here.
	if _, err := executeCommand(root, "--config", cfgPath, "index-status"); err == nil {
		t.Fatal("index-status with no index should error")
	}
	if loadedConfig == nil {
		t.Fatal("the config file was never loaded")
	}
	if got := loadedConfig.GetProfileConfig("code").Collection; got != "configured_code" {
		t.Errorf("loaded collection = %q, want configured_code", got)
	}
}

// TestRootCmdRejectsAMissingConfigFile: silently ignoring a --config typo runs
// the command with defaults the caller did not ask for.
func TestRootCmdRejectsAMissingConfigFile(t *testing.T) {
	newWorkspace(t)

	root := RootCmd()
	_, err := executeCommand(root, "--config", "no-such-config.yaml", "index-status")
	if err == nil {
		t.Fatal("a missing --config file was accepted")
	}
}

// TestRootCmdRejectsAnUnknownProfile: an unrecognised profile would otherwise
// fall through to the default collection and index into the wrong place.
func TestRootCmdRejectsAnUnknownProfile(t *testing.T) {
	dir := newWorkspace(t)
	cfgPath := writeFile(t, dir+"/semantic.yaml", "semantic:\n  code_collection: configured_code\n")

	root := RootCmd()
	_, err := executeCommand(root, "--config", cfgPath, "--profile", "nonsense", "index-status")
	if err == nil {
		t.Fatal("an unknown --profile was accepted")
	}
	if !strings.Contains(err.Error(), "nonsense") {
		t.Errorf("error = %v, want it to quote the rejected profile", err)
	}
}

// TestRootCmdPromotesALocalJSONFlagToTheGlobal pins the sync block in
// PersistentPreRunE: subcommands declare their own --json, and the global is
// what the error formatter reads.
func TestRootCmdPromotesALocalJSONFlagToTheGlobal(t *testing.T) {
	// A real index: with none, index-status now fails, which this test is not about.
	indexedWorkspace(t)
	if GlobalJSONOutput {
		t.Fatal("GlobalJSONOutput leaked in from an earlier test")
	}

	root := RootCmd()
	captureStdout(t, func() {
		if _, err := executeCommand(root, "index-status", "--json"); err != nil {
			t.Fatalf("index-status --json: %v", err)
		}
	})

	if !GlobalJSONOutput {
		t.Error("a subcommand's local --json was not promoted to the global flag")
	}
}
