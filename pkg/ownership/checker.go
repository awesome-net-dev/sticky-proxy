package ownership

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Checker periodically verifies that this backend still owns the users it is
// performing background work for. Ownership is determined by reading the
// existing sticky:{userId} keys in Redis — if a key is missing or points to a
// different backend, the OnEvict callback fires and the user is untracked.
type Checker struct {
	rdb    *redis.Client
	myAddr string

	// OnEvict is called when this backend loses ownership of a user.
	// It is called from the checker goroutine — implementations must be safe
	// for concurrent use if they interact with shared state.
	OnEvict func(userID string)

	interval  time.Duration
	batchSize int

	mu    sync.RWMutex
	users map[string]struct{}

	stopCh chan struct{}
}

// New creates an ownership checker.
//   - rdb: Redis client (can be shared with the rest of the application)
//   - myAddr: this backend's address as it appears in sticky:* values
//     (e.g. "http://backend1:5678")
//   - onEvict: called when ownership of a userID is lost
func New(rdb *redis.Client, myAddr string, onEvict func(userID string)) *Checker {
	return &Checker{
		rdb:       rdb,
		myAddr:    myAddr,
		OnEvict:   onEvict,
		interval:  30 * time.Second,
		batchSize: 1000,
		users:     make(map[string]struct{}),
		stopCh:    make(chan struct{}),
	}
}

// Track registers a user as being served by this backend.
// Duplicate calls for the same userID are safe.
func (c *Checker) Track(userID string) {
	c.mu.Lock()
	c.users[userID] = struct{}{}
	c.mu.Unlock()
}

// Untrack stops monitoring ownership for a user.
func (c *Checker) Untrack(userID string) {
	c.mu.Lock()
	delete(c.users, userID)
	c.mu.Unlock()
}

// TrackedCount returns the number of currently tracked users.
func (c *Checker) TrackedCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.users)
}

// Start launches the background ownership-check loop.
// It blocks until Stop is called, so callers should invoke it in a goroutine.
func (c *Checker) Start() {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.check()
		}
	}
}

// Stop terminates the background loop.
func (c *Checker) Stop() {
	close(c.stopCh)
}

// check performs a single ownership verification pass.
func (c *Checker) check() {
	// Snapshot current tracked users.
	c.mu.RLock()
	if len(c.users) == 0 {
		c.mu.RUnlock()
		return
	}
	userIDs := make([]string, 0, len(c.users))
	for uid := range c.users {
		userIDs = append(userIDs, uid)
	}
	c.mu.RUnlock()

	// Process in batches to avoid huge MGET commands.
	for i := 0; i < len(userIDs); i += c.batchSize {
		end := i + c.batchSize
		if end > len(userIDs) {
			end = len(userIDs)
		}
		c.checkBatch(userIDs[i:end])
	}
}

func (c *Checker) checkBatch(userIDs []string) {
	keys := make([]string, len(userIDs))
	for i, uid := range userIDs {
		keys[i] = "sticky:" + uid
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	vals, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		slog.Error("ownership check: redis MGET failed", "error", err)
		return
	}

	for i, val := range vals {
		uid := userIDs[i]
		owner, _ := val.(string)

		if owner == c.myAddr {
			continue
		}

		// Ownership lost: key is missing, expired, or points elsewhere.
		slog.Info("ownership lost", "userId", uid, "currentOwner", owner, "myAddr", c.myAddr)
		c.mu.Lock()
		delete(c.users, uid)
		c.mu.Unlock()

		if c.OnEvict != nil {
			c.OnEvict(uid)
		}
	}
}
