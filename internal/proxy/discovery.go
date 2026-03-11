package proxy

import (
	"context"
	"log/slog"
	"sort"
	"time"
)

// DiscoveredAccount represents an account returned by an AccountSource.
// Weight 0 means default (treated as 1).
type DiscoveredAccount struct {
	ID     string `json:"id"`
	Weight int    `json:"weight,omitempty"`
}

// AccountSource provides a list of accounts that need active routing.
type AccountSource interface {
	FetchAccounts(ctx context.Context) ([]DiscoveredAccount, error)
}

// AccountDiscovery periodically fetches known accounts and ensures they
// are pre-assigned to backends via the assignment table.
type AccountDiscovery struct {
	source   AccountSource
	interval time.Duration
	store    Store
	hooks    *HookClient
	stopCh   chan struct{}
}

// NewAccountDiscovery creates an AccountDiscovery.
func NewAccountDiscovery(source AccountSource, interval time.Duration, store Store, hooks *HookClient) *AccountDiscovery {
	return &AccountDiscovery{
		source:   source,
		interval: interval,
		store:    store,
		hooks:    hooks,
		stopCh:   make(chan struct{}),
	}
}

// Start runs the discovery loop. Call from a goroutine.
func (d *AccountDiscovery) Start(ctx context.Context) {
	d.reconcile(ctx)

	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stopCh:
			return
		case <-ticker.C:
			d.reconcile(ctx)
		}
	}
}

// Stop terminates the discovery loop.
func (d *AccountDiscovery) Stop() {
	close(d.stopCh)
}

func (d *AccountDiscovery) reconcile(ctx context.Context) {
	accounts, err := d.source.FetchAccounts(ctx)
	if err != nil {
		slog.Error("discovery: failed to fetch accounts", "error", err)
		return
	}

	// Get current assignments to find unassigned accounts.
	current, err := d.store.GetAllAssignments(ctx)
	if err != nil {
		slog.Error("discovery: failed to get assignments", "error", err)
		return
	}

	backends, err := d.store.ActiveBackends(ctx)
	if err != nil || len(backends) == 0 {
		slog.Error("discovery: no active backends for assignment", "error", err)
		return
	}
	sort.Strings(backends)

	activeSet := make(map[string]struct{}, len(backends))
	for _, b := range backends {
		activeSet[b] = struct{}{}
	}

	var unassigned []DiscoveredAccount
	var staleKeys []string
	for _, acct := range accounts {
		assignment, exists := current[acct.ID]
		if !exists {
			unassigned = append(unassigned, acct)
			continue
		}
		// Detect stale assignments pointing to backends that are no longer active.
		if _, healthy := activeSet[assignment.Backend]; !healthy {
			staleKeys = append(staleKeys, acct.ID)
			unassigned = append(unassigned, acct)
		}
	}

	// Clean up stale assignments so they can be reassigned.
	if len(staleKeys) > 0 {
		if delErr := d.store.BulkDeleteAssignments(ctx, staleKeys); delErr != nil {
			slog.Error("discovery: failed to delete stale assignments", "error", delErr)
		} else {
			slog.Info("discovery: cleaned stale assignments", "count", len(staleKeys))
		}
	}

	if len(unassigned) == 0 {
		return
	}

	// Round-robin across backends.
	assignments := make(map[string]BulkAssignEntry, len(unassigned))
	for i, acct := range unassigned {
		assignments[acct.ID] = BulkAssignEntry{
			Backend: backends[i%len(backends)],
			Weight:  acct.Weight,
		}
	}

	assigned, err := d.store.BulkAssign(ctx, assignments)
	if err != nil {
		slog.Error("discovery: bulk assign failed", "error", err)
	}

	if d.hooks != nil && len(assigned) > 0 {
		byBackend := make(map[string][]string)
		for routingKey, backend := range assigned {
			byBackend[backend] = append(byBackend[backend], routingKey)
		}
		for backend, keys := range byBackend {
			d.hooks.SendAssign(ctx, backend, keys)
		}
	}

	if len(assigned) > 0 {
		slog.Info("discovery: pre-assigned accounts", "count", len(assigned))
	}
}
