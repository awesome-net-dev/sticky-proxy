package proxy

import (
	"log/slog"
	"sync"
	"time"
)

// PoisonPillDetector tracks reassignment frequency per routing key. When an
// account exceeds the threshold within the configured window, it is
// quarantined — the proxy returns 503 instead of assigning it to another
// backend, preventing one bad account from cascading failures across the
// cluster.
type PoisonPillDetector struct {
	mu         sync.RWMutex
	events     map[string][]time.Time
	quarantine map[string]struct{}
	threshold  int
	window     time.Duration
	stopCh     chan struct{}
}

// NewPoisonPillDetector creates a detector with the given threshold and window.
// A background goroutine prunes stale events; call Stop to release it.
func NewPoisonPillDetector(threshold int, window time.Duration) *PoisonPillDetector {
	p := &PoisonPillDetector{
		events:     make(map[string][]time.Time),
		quarantine: make(map[string]struct{}),
		threshold:  threshold,
		window:     window,
		stopCh:     make(chan struct{}),
	}
	go p.pruneLoop()
	return p
}

// RecordReassignment records a forced reassignment for the routing key.
// Returns true if the account was just quarantined (threshold exceeded).
func (p *PoisonPillDetector) RecordReassignment(routingKey string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.quarantine[routingKey]; ok {
		return true // already quarantined
	}

	now := time.Now()
	cutoff := now.Add(-p.window)

	events := p.events[routingKey]
	events = append(events, now)

	// Drop events outside the window.
	i := 0
	for i < len(events) && events[i].Before(cutoff) {
		i++
	}
	if i > 0 {
		events = events[i:]
	}
	p.events[routingKey] = events

	if len(events) >= p.threshold {
		p.quarantine[routingKey] = struct{}{}
		delete(p.events, routingKey)
		IncPoisonPillDetections()
		IncQuarantinedAccounts()
		slog.Warn("poison pill: account quarantined",
			"routingKey", routingKey,
			"reassignments", len(events),
			"window", p.window)
		return true
	}
	return false
}

// IsQuarantined returns true if the routing key has been quarantined.
func (p *PoisonPillDetector) IsQuarantined(routingKey string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.quarantine[routingKey]
	return ok
}

// Unquarantine removes a routing key from quarantine, allowing it to be
// assigned again. Intended for admin/operator use.
func (p *PoisonPillDetector) Unquarantine(routingKey string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.quarantine[routingKey]; ok {
		delete(p.quarantine, routingKey)
		DecQuarantinedAccounts()
		slog.Info("poison pill: account unquarantined", "routingKey", routingKey)
	}
}

// QuarantinedAccounts returns the list of currently quarantined routing keys.
func (p *PoisonPillDetector) QuarantinedAccounts() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	accounts := make([]string, 0, len(p.quarantine))
	for key := range p.quarantine {
		accounts = append(accounts, key)
	}
	return accounts
}

// Stop releases the background prune goroutine.
func (p *PoisonPillDetector) Stop() {
	close(p.stopCh)
}

func (p *PoisonPillDetector) pruneLoop() {
	ticker := time.NewTicker(p.window)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.prune()
		case <-p.stopCh:
			return
		}
	}
}

func (p *PoisonPillDetector) prune() {
	p.mu.Lock()
	defer p.mu.Unlock()
	cutoff := time.Now().Add(-p.window)
	for key, events := range p.events {
		i := 0
		for i < len(events) && events[i].Before(cutoff) {
			i++
		}
		if i == len(events) {
			delete(p.events, key)
		} else if i > 0 {
			events = events[i:]
			if len(events) == 0 {
				delete(p.events, key)
			} else {
				p.events[key] = events
			}
		}
	}
}
