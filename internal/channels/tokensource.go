package channels

import (
	"fmt"
	"os"

	"golang.org/x/oauth2"
)

// realTokenSource adapts oauth2.TokenSource to the small interface FCM uses.
type realTokenSource struct {
	ts oauth2.TokenSource
}

func (r *realTokenSource) Token() (*oauth2Token, error) {
	tok, err := r.ts.Token()
	if err != nil {
		return nil, fmt.Errorf("channels: fetch oauth token: %w", err)
	}
	return &oauth2Token{AccessToken: tok.AccessToken, Expiry: tok.Expiry}, nil
}

// StaticTokenSource returns a fixed token. Tests use it to exercise the send
// path without contacting Google.
func StaticTokenSource(accessToken string) oauth2TokenSource {
	return &staticTokenSource{token: accessToken}
}

type staticTokenSource struct{ token string }

func (s *staticTokenSource) Token() (*oauth2Token, error) {
	return &oauth2Token{AccessToken: s.token}, nil
}

func readFile(path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("channels: no credentials path configured")
	}
	// The path comes from this process's own configuration (FCM_CREDENTIALS_FILE),
	// never from a request, so a variable path here is intended rather than a
	// file-inclusion risk.
	b, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		return nil, fmt.Errorf("channels: read %s: %w", path, err)
	}
	return b, nil
}
