package semantic

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// EST-8336 Low 5: when every retry got a 5xx, the error said "http: read on
// closed response body" and lost the status code, because the last response's
// body was closed before it was read. The error now carries the last status
// and the API's message, and every attempt sends the whole request body.
func TestEmbedderRetriesExhaustedReportTheLastStatus(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":{"message":"model overloaded"}}`))
	}))
	defer srv.Close()

	e, err := NewEmbedder(EmbedderConfig{APIURL: srv.URL, MaxRetries: 2})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.EmbedBatch(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("a 503 on every attempt was reported as success")
	}
	if strings.Contains(err.Error(), "closed") || !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), "model overloaded") {
		t.Errorf("err = %q, want the last status (503) and the API's message", err)
	}
	if len(bodies) != 3 {
		t.Fatalf("%d attempts, want 3", len(bodies))
	}
	for i, b := range bodies {
		if !strings.Contains(b, "hello") {
			t.Errorf("attempt %d sent body %q, want the request", i+1, b)
		}
	}
}
