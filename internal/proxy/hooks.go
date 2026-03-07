package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	Users []string `json:"users"`
}

// NewHookClient creates a HookClient with the given timeout and retry count.
func NewHookClient(timeout time.Duration, retries int) *HookClient {
	return &HookClient{
		client:  &http.Client{Timeout: timeout},
		retries: retries,
	}
}

// SendAssign notifies a backend that users have been assigned to it.
func (h *HookClient) SendAssign(ctx context.Context, backend string, routingKeys []string) {
	if err := h.send(ctx, backend, "/hooks/assign", routingKeys); err != nil {
		IncHookFailures()
		slog.Warn("assign hook failed", "backend", backend, "users", len(routingKeys), "error", err)
		return
	}
	IncHookAssigns()
}

// SendUnassign notifies a backend that users have been unassigned from it.
func (h *HookClient) SendUnassign(ctx context.Context, backend string, routingKeys []string) {
	if err := h.send(ctx, backend, "/hooks/unassign", routingKeys); err != nil {
		IncHookFailures()
		slog.Warn("unassign hook failed", "backend", backend, "users", len(routingKeys), "error", err)
		return
	}
	IncHookUnassigns()
}

func (h *HookClient) send(ctx context.Context, backend, path string, routingKeys []string) error {
	body, err := json.Marshal(hookPayload{Users: routingKeys})
	if err != nil {
		return err
	}

	url := backend + path
	var lastErr error
	for attempt := 0; attempt <= h.retries; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
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
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		lastErr = fmt.Errorf("hook %s returned %d", path, resp.StatusCode)
	}
	return lastErr
}
