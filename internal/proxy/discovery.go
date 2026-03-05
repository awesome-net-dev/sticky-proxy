package proxy

import (
	"context"
	"log/slog"
	"time"
)

// AccountSource provides a list of account IDs that need active routing.
type AccountSource interface {
	FetchAccounts(ctx context.Context) ([]string, error)
}

// AccountDiscovery periodically fetches known accounts and ensures they
// are pre-assigned to backends via the assignment table.
type AccountDiscovery struct {
	source   AccountSource
	interval time.Duration
	redis    *Redis
	hooks    *HookClient
	stopCh   chan struct{}
}

// NewAccountDiscovery creates an AccountDiscovery.
func NewAccountDiscovery(source AccountSource, interval time.Duration, r *Redis, hooks *HookClient) *AccountDiscovery {
	return &AccountDiscovery{
		source:   source,
		interval: interval,
		redis:    r,
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
	current, err := d.redis.GetAllAssignments(ctx)
	if err != nil {
		slog.Error("discovery: failed to get assignments", "error", err)
		return
	}

	var assigned int
	for _, acct := range accounts {
		if _, exists := current[acct]; exists {
			continue
		}
		// Assign via the assignment table Lua script (least-loaded).
		a, err := d.redis.AssignViaTable(ctx, acct)
		if err != nil || a == nil {
			slog.Warn("discovery: failed to assign account", "account", acct, "error", err)
			continue
		}
		if d.hooks != nil {
			d.hooks.SendAssign(ctx, a.Backend, acct)
		}
		assigned++
	}

	if assigned > 0 {
		slog.Info("discovery: pre-assigned accounts", "count", assigned)
	}
}
