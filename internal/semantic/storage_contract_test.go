package semantic

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/qdrant/go-client/qdrant"
)

// These tests pin docs/storage-contract.md (contract version 1) to the code
// that writes the Qdrant collection. External consumers such as atcr read
// that collection directly, so a renamed, removed, added or retyped payload
// key, or a changed vector size or distance, breaks them silently. Each test
// writes points through QdrantStorage against the in-process fake Qdrant and
// compares what landed on the wire with the contract below; a second test
// checks the contract below against the tables in the doc, so the code, the
// test and the doc cannot drift apart one at a time.
//
// If one of these fails, the storage contract changed. Do not edit the
// expectations to make it pass without also updating the doc and following
// its version-bump rule.

// payloadKind is the documented type of a payload value.
type payloadKind string

const (
	kindString payloadKind = "string"
	kindInt    payloadKind = "int"
	kindDouble payloadKind = "double"
)

// contractPayloads is the payload contract of every point kind: the exact key
// set, and the value type of each key. Keys are grouped by the doc section
// (### N.) that documents them.
var contractPayloads = map[string]map[string]payloadKind{
	"chunk": {
		"chunk_id":     kindString,
		"file_path":    kindString,
		"type":         kindString,
		"name":         kindString,
		"signature":    kindString,
		"content":      kindString,
		"start_line":   kindInt,
		"end_line":     kindInt,
		"language":     kindString,
		"domain":       kindString,
		"content_hash": kindString,
	},
	"file_hash": {
		"type":         kindString,
		"file_path":    kindString,
		"content_hash": kindString,
	},
	"calibration_metadata": {
		"type":             kindString,
		"calibration_json": kindString,
	},
	// Memory points: keys written at creation plus the retrieval-stat keys
	// TrackMemoryRetrieval adds later (the doc's two memory tables).
	"memory": {
		"memory_id":       kindString,
		"question":        kindString,
		"answer":          kindString,
		"tags":            kindString,
		"source":          kindString,
		"status":          kindString,
		"occurrences":     kindInt,
		"entry_type":      kindString,
		"created_at":      kindString,
		"updated_at":      kindString,
		"file_path":       kindString,
		"sprints":         kindString,
		"files":           kindString,
		"retrieval_count": kindInt,
		"avg_score":       kindDouble,
		"last_retrieved":  kindString,
	},
	"retrieval_log": {
		"entry_type": kindString,
		"memory_id":  kindString,
		"query":      kindString,
		"score":      kindDouble,
		"timestamp":  kindString,
	},
}

// memoryCreationOnlyKeys are the memory keys absent until the first tracked
// retrieval, per the doc ("absent until the first one").
var memoryCreationOnlyKeys = map[string]bool{
	"retrieval_count": true,
	"avg_score":       true,
	"last_retrieved":  true,
}

// contractSections maps each point kind to the heading prefix of its section
// in docs/storage-contract.md.
var contractSections = map[string]string{
	"chunk":                "### 1.",
	"file_hash":            "### 2.",
	"calibration_metadata": "### 3.",
	"memory":               "### 4.",
	"retrieval_log":        "### 5.",
}

// kindOf reports the documented type of a stored Qdrant value, or "" for a
// value of any other kind (bool, list, struct, null).
func kindOf(v *qdrant.Value) payloadKind {
	switch v.GetKind().(type) {
	case *qdrant.Value_StringValue:
		return kindString
	case *qdrant.Value_IntegerValue:
		return kindInt
	case *qdrant.Value_DoubleValue:
		return kindDouble
	}
	return ""
}

// assertPayloadContract fails unless payload has exactly the keys in want,
// each holding a value of the documented type.
func assertPayloadContract(t *testing.T, pointKind string, payload map[string]*qdrant.Value, want map[string]payloadKind) {
	t.Helper()
	if payload == nil {
		t.Fatalf("%s point: not found in the collection", pointKind)
	}
	for key, kind := range want {
		v, ok := payload[key]
		if !ok {
			t.Errorf("%s point: documented payload key %q is missing (docs/storage-contract.md)", pointKind, key)
			continue
		}
		if got := kindOf(v); got != kind {
			t.Errorf("%s point: payload key %q has type %q, contract says %q (docs/storage-contract.md)", pointKind, key, got, kind)
		}
	}
	for key := range payload {
		if _, ok := want[key]; !ok {
			t.Errorf("%s point: payload key %q is not in the storage contract (docs/storage-contract.md)", pointKind, key)
		}
	}
}

// without returns a copy of m minus the given keys.
func without(m map[string]payloadKind, drop map[string]bool) map[string]payloadKind {
	out := make(map[string]payloadKind, len(m))
	for k, v := range m {
		if !drop[k] {
			out[k] = v
		}
	}
	return out
}

// Defends: the collection's vector parameters, which atcr's REST query relies
// on — one unnamed dense vector of the embedding dimension, cosine distance
// (storage_qdrant.go:193-197). Changing the distance or the size breaks every
// consumer's scores silently.
func TestStorageContract_CollectionVectorParameters(t *testing.T) {
	const dim = 7
	_, fq := newFakeQdrantStorage(t, "contract_vectors", dim)

	if got := fq.collectionDistance("contract_vectors"); got != qdrant.Distance_Cosine {
		t.Errorf("collection distance = %v, contract says Cosine", got)
	}
	if got := fq.collectionDim("contract_vectors"); got != dim {
		t.Errorf("collection vector size = %d, contract says the embedding dimension %d", got, dim)
	}
}

// Defends: the payload keys and types of each of the five point kinds that
// share one collection. A consumer filtering chunks by `chunk_id`, file hashes
// by `type`, or memories by `entry_type` breaks the moment a key is renamed,
// removed or retyped; an undocumented key added here is a contract change too.
func TestStorageContract_PayloadKeysAndTypesPerPointKind(t *testing.T) {
	const (
		collection = "contract_payloads"
		dim        = 4
	)
	storage, fq := newFakeQdrantStorage(t, collection, dim)
	ctx := context.Background()
	vec := []float32{0.1, 0.2, 0.3, 0.4}

	chunk := Chunk{
		ID:          "pkg/a.go:Foo:1",
		FilePath:    "/repo/pkg/a.go",
		Type:        ChunkFunction,
		Name:        "Foo",
		Signature:   "func Foo()",
		Content:     "func Foo() {}",
		StartLine:   1,
		EndLine:     3,
		Language:    "go",
		ContentHash: "abc123",
	}
	if err := storage.Create(ctx, chunk, vec); err != nil {
		t.Fatalf("Create chunk: %v", err)
	}
	if err := storage.SetFileHash(ctx, "/repo/pkg/a.go", "filehash"); err != nil {
		t.Fatalf("SetFileHash: %v", err)
	}
	if err := storage.SetCalibrationMetadata(ctx, &CalibrationMetadata{
		EmbeddingModel:    "m",
		CalibrationDate:   time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC),
		PerfectMatchScore: 0.9,
		BaselineScore:     0.1,
		ScoreRange:        0.8,
		HighThreshold:     0.7,
		MediumThreshold:   0.5,
		LowThreshold:      0.3,
	}); err != nil {
		t.Fatalf("SetCalibrationMetadata: %v", err)
	}
	entry := MemoryEntry{
		ID:          "mem-2026-10-31-abcdef",
		Question:    "q?",
		Answer:      "a.",
		Tags:        []string{"x", "y"},
		Source:      "manual",
		Status:      MemoryStatusPending,
		Occurrences: 1,
		CreatedAt:   "2026-10-31T00:00:00Z",
		UpdatedAt:   "2026-10-31T00:00:00Z",
		FilePath:    "notes.md",
		Sprints:     []string{"s1"},
		Files:       []string{"a.go"},
	}
	if err := storage.StoreMemory(ctx, entry, vec); err != nil {
		t.Fatalf("StoreMemory: %v", err)
	}

	assertPayloadContract(t, "chunk", fq.payloadOf(collection, stringToUUID(chunk.ID)), contractPayloads["chunk"])
	assertPayloadContract(t, "file_hash", fq.payloadOf(collection, fileHashID("/repo/pkg/a.go")), contractPayloads["file_hash"])
	assertPayloadContract(t, "calibration_metadata", fq.payloadOf(collection, calibrationMetadataID()), contractPayloads["calibration_metadata"])
	assertPayloadContract(t, "memory (before any retrieval)", fq.payloadOf(collection, memoryPointID(entry.ID)),
		without(contractPayloads["memory"], memoryCreationOnlyKeys))

	if err := storage.TrackMemoryRetrieval(ctx, entry.ID, "the query", 0.75); err != nil {
		t.Fatalf("TrackMemoryRetrieval: %v", err)
	}
	assertPayloadContract(t, "memory (after a retrieval)", fq.payloadOf(collection, memoryPointID(entry.ID)), contractPayloads["memory"])

	logs := fq.payloadsWhere(collection, func(p map[string]*qdrant.Value) bool {
		return p["entry_type"].GetStringValue() == "retrieval_log"
	})
	if len(logs) != 1 {
		t.Fatalf("retrieval_log points = %d, want 1", len(logs))
	}
	assertPayloadContract(t, "retrieval_log", logs[0], contractPayloads["retrieval_log"])

	// The documented discriminator values, which the doc's filter rule tells
	// consumers to select on.
	discriminators := []struct {
		pointKind, key, want string
		payload              map[string]*qdrant.Value
	}{
		{"file_hash", "type", "file_hash", fq.payloadOf(collection, fileHashID("/repo/pkg/a.go"))},
		{"calibration_metadata", "type", "calibration_metadata", fq.payloadOf(collection, calibrationMetadataID())},
		{"memory", "entry_type", "memory", fq.payloadOf(collection, memoryPointID(entry.ID))},
		{"retrieval_log", "entry_type", "retrieval_log", logs[0]},
		{"chunk", "domain", "code", fq.payloadOf(collection, stringToUUID(chunk.ID))},
	}
	for _, d := range discriminators {
		if got := d.payload[d.key].GetStringValue(); got != d.want {
			t.Errorf("%s point: %s = %q, contract says %q", d.pointKind, d.key, got, d.want)
		}
	}

	// calibration_json carries the documented CalibrationMetadata fields.
	var calib map[string]any
	raw := fq.payloadOf(collection, calibrationMetadataID())["calibration_json"].GetStringValue()
	if err := json.Unmarshal([]byte(raw), &calib); err != nil {
		t.Fatalf("calibration_json is not JSON: %v", err)
	}
	var calibKeys []string
	for k := range calib {
		calibKeys = append(calibKeys, k)
	}
	sort.Strings(calibKeys)
	wantCalib := []string{"baseline_score", "calibration_date", "embedding_model", "high_threshold",
		"low_threshold", "medium_threshold", "perfect_match_score", "score_range"}
	if strings.Join(calibKeys, ",") != strings.Join(wantCalib, ",") {
		t.Errorf("calibration_json fields = %v, contract says %v", calibKeys, wantCalib)
	}
}

// docRowRE matches a payload-table row: | `key` | type — description | ... |
var docRowRE = regexp.MustCompile("^\\|\\s*`([a-z_]+)`\\s*\\|\\s*([a-z]+)")

// docPayloadTables parses docs/storage-contract.md into point kind -> key ->
// documented type, reading every payload table under each "### N." section.
func docPayloadTables(t *testing.T) map[string]map[string]payloadKind {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "storage-contract.md"))
	if err != nil {
		t.Fatalf("read storage contract: %v", err)
	}
	headingKind := make(map[string]string, len(contractSections))
	for k, prefix := range contractSections {
		headingKind[prefix] = k
	}

	out := make(map[string]map[string]payloadKind)
	current := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "#") {
			current = ""
			for prefix, k := range headingKind {
				if strings.HasPrefix(line, prefix) {
					current = k
				}
			}
			continue
		}
		if current == "" {
			continue
		}
		m := docRowRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if out[current] == nil {
			out[current] = make(map[string]payloadKind)
		}
		key, kind := m[1], payloadKind(m[2])
		if prev, seen := out[current][key]; seen && prev != kind {
			t.Errorf("doc section %s lists %q twice with types %q and %q", contractSections[current], key, prev, kind)
		}
		out[current][key] = kind
	}
	return out
}

// Defends: the doc and this test's contract staying the same list. Without it
// a key could be renamed in both the code and the expectations above while
// the doc atcr reads still promises the old name.
func TestStorageContract_DocTablesMatchContract(t *testing.T) {
	doc := docPayloadTables(t)
	for pointKind, want := range contractPayloads {
		got := doc[pointKind]
		if len(got) == 0 {
			t.Errorf("doc section %s (%s): no payload table found", contractSections[pointKind], pointKind)
			continue
		}
		for key, kind := range want {
			if g, ok := got[key]; !ok {
				t.Errorf("%s: key %q is in the contract test but not in the doc", pointKind, key)
			} else if g != kind {
				t.Errorf("%s: key %q has type %q in the doc, %q in the contract test", pointKind, key, g, kind)
			}
		}
		for key := range got {
			if _, ok := want[key]; !ok {
				t.Errorf("%s: key %q is in the doc but not in the contract test", pointKind, key)
			}
		}
	}
}
