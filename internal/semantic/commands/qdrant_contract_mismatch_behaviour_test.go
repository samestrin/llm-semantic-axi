package commands

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/qdrant/go-client/qdrant"
	"github.com/samestrin/llm-semantic-axi/v2/internal/semantic"
	"github.com/samestrin/llm-semantic-axi/v2/pkg/output"
	"google.golang.org/grpc"
)

// Epic 38 T2: a Qdrant collection stamped with another storage contract
// version must fail through createStorage as an error in plain, --json and
// --axi, never as an empty result list. The storage-level check shipped in
// #91, but nothing proved the error survives the command layer: a search that
// swallowed it and printed zero results would report "found nothing" when the
// index was never read — the exact success-when-it-did-not-succeed bug this
// repo exists to prevent.

// mismatchedQdrant is the smallest fake Qdrant that reaches the contract
// check: it answers the client's health check, lists one existing collection,
// accepts the chunk_id index, and serves the storage_contract marker point
// stamped with a caller-chosen version. The semantic package's full fake is
// test-only and cannot be imported here; opening the storage fails at the
// contract read, so no other RPC is ever reached.
type mismatchedQdrant struct {
	collection string
	version    int64
}

// contractCollections serves the Collections service. Collections and Points
// both declare Get, so one receiver cannot serve both (same split as the
// semantic package's fake).
type contractCollections struct {
	qdrant.UnimplementedCollectionsServer
	f *mismatchedQdrant
}

func (s *contractCollections) List(_ context.Context, _ *qdrant.ListCollectionsRequest) (*qdrant.ListCollectionsResponse, error) {
	return &qdrant.ListCollectionsResponse{
		Collections: []*qdrant.CollectionDescription{{Name: s.f.collection}},
	}, nil
}

// contractPoints serves the Points service.
type contractPoints struct {
	qdrant.UnimplementedPointsServer
	f *mismatchedQdrant
}

func (s *contractPoints) CreateFieldIndex(_ context.Context, _ *qdrant.CreateFieldIndexCollection) (*qdrant.PointsOperationResponse, error) {
	return &qdrant.PointsOperationResponse{Result: &qdrant.UpdateResult{Status: qdrant.UpdateStatus_Completed}}, nil
}

// Get answers the contract read. ensureContractVersion asks for exactly one
// point (the storage_contract marker) with its payload; that shape gets the
// stamped marker back. Any other read gets nothing, so a future extra read
// cannot accidentally be fed the marker too.
func (s *contractPoints) Get(_ context.Context, req *qdrant.GetPoints) (*qdrant.GetResponse, error) {
	if len(req.GetIds()) != 1 {
		return &qdrant.GetResponse{}, nil
	}
	return &qdrant.GetResponse{Result: []*qdrant.RetrievedPoint{{
		Id: req.GetIds()[0],
		Payload: map[string]*qdrant.Value{
			"type":             qdrant.NewValueString("storage_contract"),
			"contract_version": qdrant.NewValueInt(s.f.version),
		},
	}}}, nil
}

// contractHealth answers the go-client's compatibility probe so it does not
// warn about an unknown server version.
type contractHealth struct {
	qdrant.UnimplementedQdrantServer
}

func (contractHealth) HealthCheck(context.Context, *qdrant.HealthCheckRequest) (*qdrant.HealthCheckReply, error) {
	return &qdrant.HealthCheckReply{Title: "qdrant-fake", Version: "1.16.0"}, nil
}

// startMismatchedQdrant serves the fake on a loopback port and points the
// Qdrant environment at it, so createStorage opens a collection stamped with
// the given contract version without any network beyond loopback.
func startMismatchedQdrant(t *testing.T, collection string, version int64) {
	t.Helper()

	f := &mismatchedQdrant{collection: collection, version: version}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	qdrant.RegisterQdrantServer(srv, contractHealth{})
	qdrant.RegisterCollectionsServer(srv, &contractCollections{f: f})
	qdrant.RegisterPointsServer(srv, &contractPoints{f: f})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(lis)
	}()
	t.Cleanup(func() {
		srv.Stop()
		<-done
	})

	t.Setenv("QDRANT_API_URL", "http://"+lis.Addr().String())
	t.Setenv("QDRANT_API_KEY", "test-key")
	t.Setenv("QDRANT_INSECURE", "")
	t.Setenv("QDRANT_COLLECTION", collection)
}

// assertNamesBothVersions checks the mismatch wording names the stored
// version and the version this build reads, so the caller knows which side
// moved. The numbers come from the constant, not literals, so raising
// StorageContractVersion does not quietly invalidate the test.
func assertNamesBothVersions(t *testing.T, msg string, stored int64) {
	t.Helper()
	for _, want := range []string{
		fmt.Sprintf("storage contract version %d", stored),
		fmt.Sprintf("reads version %d", semantic.StorageContractVersion),
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q: %s", want, msg)
		}
	}
}

// TestCreateStorageQdrantContractMismatch pins the seam every command shares:
// createStorage is the only caller of NewQdrantStorage, and the mismatch must
// come back as an error there, not as a usable empty storage.
func TestCreateStorageQdrantContractMismatch(t *testing.T) {
	newWorkspace(t)
	other := int64(semantic.StorageContractVersion + 1)
	startMismatchedQdrant(t, "contract_mismatch", other)
	t.Cleanup(SetStorageTypeForTesting("qdrant"))

	storage, err := createStorage("", fakeEmbedderDim)

	if err == nil {
		_ = storage.Close()
		t.Fatal("createStorage accepted a collection stamped with another contract version")
	}
	assertNamesBothVersions(t, err.Error(), other)
}

// TestSearchQdrantContractMismatchFailsInEveryFormat runs the CLI as a user
// would, in each output format, and renders the failure exactly as
// cmd/llm-semantic/main.go does. Every format must carry the mismatch error
// naming both versions, and none may emit result rows: an empty list here
// would read as "searched, found nothing" when the index was never opened.
func TestSearchQdrantContractMismatchFailsInEveryFormat(t *testing.T) {
	for _, format := range []string{"--plain", "--json", "--axi"} {
		t.Run(format, func(t *testing.T) {
			newWorkspace(t)
			fe := newFakeEmbedder(t)
			other := int64(semantic.StorageContractVersion + 1)
			startMismatchedQdrant(t, "contract_mismatch", other)

			out, err := runRootErr(t, "search", "authenticate", "--storage", "qdrant", "--api-url", fe.URL, format)

			if err == nil {
				t.Fatalf("search against a mismatched collection exited 0:\n%s", out)
			}
			if code := output.ExitCodeFor(err); code != 1 {
				t.Errorf("exit code %d, want 1", code)
			}
			assertNamesBothVersions(t, err.Error(), other)
			if strings.TrimSpace(out) != "" {
				t.Errorf("search printed output before failing; a failure must not emit result rows:\n%s", out)
			}

			// Render the error the way main() does: same formatter, same
			// resolved mode, so the assertion covers what a user sees.
			var sb strings.Builder
			f := output.New(GlobalJSONOutput, GlobalMinOutput, &sb)
			if code := f.PrintError(err); code != 1 {
				t.Errorf("PrintError exit code %d, want 1", code)
			}
			rendered := sb.String()
			assertNamesBothVersions(t, rendered, other)

			switch format {
			case "--plain":
				if !strings.HasPrefix(rendered, "Error: ") {
					t.Errorf("plain failure lacks the Error: prefix:\n%s", rendered)
				}
			case "--json":
				var payload map[string]any
				decodeJSON(t, rendered, &payload)
				if payload["error"] != true {
					t.Errorf("--json failure is not marked error: true:\n%s", rendered)
				}
				if _, ok := payload["results"]; ok {
					t.Errorf("--json failure carries a results key:\n%s", rendered)
				}
			case "--axi":
				if !strings.Contains(rendered, "error: true") {
					t.Errorf("--axi failure is not marked error: true:\n%s", rendered)
				}
				if strings.Contains(rendered, "results[") {
					t.Errorf("--axi failure carries result rows:\n%s", rendered)
				}
			}
		})
	}
}
