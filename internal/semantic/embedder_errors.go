package semantic

import (
	"context"
	"errors"
	"fmt"
)

// EmbedderUnreachableError is a request to the embedding API that never got a
// response: the connection was refused, the host did not resolve, or the dial
// timed out. Its message names the endpoint in plain words instead of Go's
// transport error (`Post "...": dial tcp ...: connect: connection refused`),
// and AXIHelp says what to change, so an agent can fix its setup in one step.
type EmbedderUnreachableError struct {
	URL string
	// Remote is true for a hosted provider (cohere, huggingface, openrouter),
	// whose URL --api-url does not set.
	Remote bool
	Err    error
}

func (e *EmbedderUnreachableError) Error() string {
	return fmt.Sprintf("cannot reach the embedding API at %s (%s)", RedactURL(e.URL), rootCause(e.Err))
}

func (e *EmbedderUnreachableError) Unwrap() error { return e.Err }

// AXIHelp satisfies output.AXIHelper, so the next step reaches TOON output
// wherever in the chain this error sits.
func (e *EmbedderUnreachableError) AXIHelp() []string {
	shown := RedactURL(e.URL)
	if e.Remote {
		return []string{
			fmt.Sprintf("Check that %s is reachable from this machine and that `--embedder` names the provider you meant", shown),
			"Or use a local OpenAI-compatible embedder: `--embedder openai --api-url <url>` (or set `LLM_SEMANTIC_API_URL`)",
		}
	}
	return []string{
		fmt.Sprintf("Start the embedding API at %s, or set `--api-url` / `LLM_SEMANTIC_API_URL` to one that is running", shown),
		"Indexing and searching need an OpenAI-compatible /v1/embeddings endpoint; `callers`, `refs` and `index-status` work without one on the default SQLite storage",
	}
}

// unreachable wraps a transport error from the embedding API. A cancelled
// request was stopped by the caller, not refused by the endpoint.
func unreachable(url string, remote bool, err error) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("failed to send request: %w", redactURLError(err))
	}
	return &EmbedderUnreachableError{URL: url, Remote: remote, Err: err}
}

// rootCause is the innermost error in err's chain: "connection refused" rather
// than the url.Error and net.OpError wrapped around it.
func rootCause(err error) string {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err.Error()
		}
		err = next
	}
}
