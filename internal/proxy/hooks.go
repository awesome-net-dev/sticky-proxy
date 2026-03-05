package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// HookClient sends assign/unassign webhook notifications to backends.
type HookClient struct {
	client  *http.Client
	retries int
}

type hookPayload struct {
	User string `json:"user"`
}

// NewHookClient creates a HookClient with the given timeout and retry count.
func NewHookClient(timeout time.Duration, retries int) *HookClient {
	return &HookClient{
		client:  &http.Client{Timeout: timeout},
		retries: retries,
	}
}

// SendAssign notifies a backend that a user has been assigned to it.
func (h *HookClient) SendAssign(ctx context.Context, backend, routingKey string) {
	if err := h.send(ctx, backend, "/hooks/assign", routingKey); err != nil {
		IncHookFailures()
		slog.Warn("assign hook failed", "backend", backend, "user", routingKey, "error", err)
		return
	}
	IncHookAssigns()
}

// SendUnassign notifies a backend that a user has been unassigned from it.
// This call blocks until the backend responds or the context is cancelled.
func (h *HookClient) SendUnassign(ctx context.Context, backend, routingKey string) {
	if err := h.send(ctx, backend, "/hooks/unassign", routingKey); err != nil {
		IncHookFailures()
		slog.Warn("unassign hook failed", "backend", backend, "user", routingKey, "error", err)
		return
	}
	IncHookUnassigns()
}

func (h *HookClient) send(ctx context.Context, backend, path, routingKey string) error {
	body, err := json.Marshal(hookPayload{User: routingKey})
	if err != nil {
		return err
	}

	url := backend + path
	var lastErr error
	for attempt := 0; attempt <= h.retries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := h.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		_ = resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		lastErr = fmt.Errorf("hook %s returned %d", path, resp.StatusCode)
	}
	return lastErr
}
