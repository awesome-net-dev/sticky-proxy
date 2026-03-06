package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// FetchAccounts makes a GET request and parses the JSON response.
// Accepts either [{"id":"x","weight":5},...] or ["id1","id2",...] format.
func (s *HTTPAccountSource) FetchAccounts(ctx context.Context) ([]DiscoveredAccount, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP account source returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Try structured format first.
	var accounts []DiscoveredAccount
	if err := json.Unmarshal(body, &accounts); err == nil && (len(accounts) == 0 || accounts[0].ID != "") {
		return accounts, nil
	}

	// Fall back to plain string array.
	var ids []string
	if err := json.Unmarshal(body, &ids); err != nil {
		return nil, err
	}
	accounts = make([]DiscoveredAccount, len(ids))
	for i, id := range ids {
		accounts[i] = DiscoveredAccount{ID: id}
	}
	return accounts, nil
}
