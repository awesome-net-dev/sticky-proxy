package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// HTTPAccountSource fetches account IDs from an HTTP endpoint.
// The endpoint must return a JSON array of strings.
type HTTPAccountSource struct {
	url    string
	client *http.Client
}

// NewHTTPAccountSource creates a source that fetches account IDs from a URL.
func NewHTTPAccountSource(url string) *HTTPAccountSource {
	return &HTTPAccountSource{
		url:    url,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// FetchAccounts makes a GET request and parses the JSON array response.
func (s *HTTPAccountSource) FetchAccounts(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP account source returned %d", resp.StatusCode)
	}

	var accounts []string
	if err := json.NewDecoder(resp.Body).Decode(&accounts); err != nil {
		return nil, err
	}
	return accounts, nil
}
