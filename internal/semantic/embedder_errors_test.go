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

// EST-8278 item 1: a base URL may carry credentials (user info, or a key in
// the query). The README promises secrets are named, never shown, so the
// unreachable message and its help must not print them.
func TestUnreachableErrorDoesNotPrintURLCredentials(t *testing.T) {
	base := strings.Replace(closedServerURL(t), "http://", "http://user:S3CRETPASS@", 1) + "/?api-key=S3CRETKEY#frag"
	e, err := NewEmbedder(EmbedderConfig{APIURL: base, Model: "m"})
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}

	_, err = e.Embed(context.Background(), "x")

	var u *EmbedderUnreachableError
	if !errors.As(err, &u) {
		t.Fatalf("error is not an EmbedderUnreachableError: %v", err)
	}
	shown := err.Error() + "\n" + strings.Join(u.AXIHelp(), "\n")
	for _, secret := range []string{"S3CRETPASS", "S3CRETKEY", "user:", "frag"} {
		if strings.Contains(shown, secret) {
			t.Errorf("unreachable error shows %q:\n%s", secret, shown)
		}
	}
	if !strings.Contains(shown, "cannot reach the embedding API at http://127.0.0.1:") {
		t.Errorf("unreachable error no longer names the host:\n%s", shown)
	}
}

// EST-8278 item 1: a cancelled request is reported with Go's url.Error, which
// prints the request URL including its query; the key must not reach it.
func TestCancelledEmbedRequestDoesNotPrintURLCredentials(t *testing.T) {
	e, err := NewEmbedder(EmbedderConfig{APIURL: closedServerURL(t) + "/?api-key=S3CRETKEY", Model: "m"})
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = e.Embed(ctx, "x")

	if err == nil || strings.Contains(err.Error(), "S3CRETKEY") {
		t.Errorf("cancelled request error shows the key: %v", err)
	}
}

// EST-8278 item 1: appending "/v1/embeddings" to a base URL with a query put
// the path inside the query (`/?api-key=K/v1/embeddings`), and a trailing
// slash doubled it. The path must be joined and the query kept.
func TestEmbedderJoinsPathBeforeQuery(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2],"index":0}]}`))
	}))
	defer srv.Close()

	cases := []struct{ base, path, query string }{
		{srv.URL, "/v1/embeddings", ""},
		{srv.URL + "/", "/v1/embeddings", ""},
		{srv.URL + "/?api-key=K", "/v1/embeddings", "api-key=K"},
		{srv.URL + "/proxy/?api-key=K", "/proxy/v1/embeddings", "api-key=K"},
	}
	for _, c := range cases {
		e, err := NewEmbedder(EmbedderConfig{APIURL: c.base, Model: "m"})
		if err != nil {
			t.Fatalf("NewEmbedder: %v", err)
		}
		if _, err := e.Embed(context.Background(), "x"); err != nil {
			t.Fatalf("%s: Embed: %v", c.base, err)
		}
		if gotPath != c.path || gotQuery != c.query {
			t.Errorf("%s: request path %q query %q, want %q %q", c.base, gotPath, gotQuery, c.path, c.query)
		}
	}
}

// TestRedactURL pins what may be shown of a base URL: scheme, host and path.
func TestRedactURL(t *testing.T) {
	cases := map[string]string{
		"http://localhost:8080":         "http://localhost:8080",
		"http://u:p@host:1/?api-key=K":  "http://host:1/",
		"https://host/base/path?x=1#f":  "https://host/base/path",
		"https://token@host":            "https://host",
		"http://u:p@host/%zz?api-key=K": "<unparseable URL>",
		"localhost:8080":                "localhost:8080",
	}
	for in, want := range cases {
		if got := RedactURL(in); got != want {
			t.Errorf("RedactURL(%q) = %q, want %q", in, got, want)
		}
	}
}
