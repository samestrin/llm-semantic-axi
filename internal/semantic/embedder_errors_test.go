package semantic

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// EST-8171 item 1: a transport failure must read as an unreachable endpoint,
// not as Go's dial error, for every embedder.

func closedServerURL(t *testing.T) string {
	t.Helper()
	s := httptest.NewServer(http.NotFoundHandler())
	url := s.URL
	s.Close()
	return url
}

// TestHostedEmbedderUnreachableHelpDoesNotPointAtAPIURL: --api-url does not
// set a hosted provider's URL, so telling the caller to change it would send
// them the wrong way.
func TestHostedEmbedderUnreachableHelpDoesNotPointAtAPIURL(t *testing.T) {
	url := closedServerURL(t)
	e, err := NewCohereEmbedder(CohereConfig{APIKey: "k", BaseURL: url, Model: "m"})
	if err != nil {
		t.Fatalf("NewCohereEmbedder: %v", err)
	}

	_, err = e.Embed(context.Background(), "x")

	var u *EmbedderUnreachableError
	if !errors.As(err, &u) {
		t.Fatalf("error is not an EmbedderUnreachableError: %v", err)
	}
	if !strings.Contains(err.Error(), "cannot reach the embedding API at "+url) {
		t.Errorf("message = %q", err)
	}
	help := strings.Join(u.AXIHelp(), "\n")
	if strings.Contains(help, "Start the embedding API") || !strings.Contains(help, "--embedder") {
		t.Errorf("hosted-provider help is wrong:\n%s", help)
	}
}

// TestCancelledEmbedRequestIsNotReportedAsUnreachable: the caller stopped the
// request; blaming the endpoint would send them to fix a working setup.
func TestCancelledEmbedRequestIsNotReportedAsUnreachable(t *testing.T) {
	e, err := NewEmbedder(EmbedderConfig{APIURL: closedServerURL(t), Model: "m"})
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = e.Embed(ctx, "x")

	var u *EmbedderUnreachableError
	if err == nil || errors.As(err, &u) {
		t.Errorf("a cancelled request was reported as unreachable: %v", err)
	}
}
