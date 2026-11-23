package semantic

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// RedactURL is what may be printed of a configured endpoint: its scheme, host
// and path. User info and the query can carry a password or an API key
// (`http://u:pass@host/?api-key=K`), and the README promises secrets are never
// shown, so both are dropped, with the fragment.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<unparseable URL>"
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}

// joinURL appends path to base's path, keeping base's query: appending the
// strings put the path inside a query (`/?api-key=K/v1/embeddings`) and
// doubled the slash after a trailing one. Its errors never print base.
func joinURL(base, path string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid embedding API URL %s", RedactURL(base))
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawPath = ""
	return u.String(), nil
}

// redactURLError rewrites the URL in a *url.Error, which net/http returns for
// a failed request and which prints the request URL, query and all.
func redactURLError(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	return &url.Error{Op: ue.Op, URL: RedactURL(ue.URL), Err: ue.Err}
}
