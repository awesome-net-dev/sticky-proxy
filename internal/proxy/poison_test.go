package proxy

import (
	"testing"
	"time"
)

func TestPoisonPill_NoQuarantineBelowThreshold(t *testing.T) {
	t.Parallel()
	pp := NewPoisonPillDetector(3, time.Minute)
	t.Cleanup(pp.Stop)

	pp.RecordReassignment("user-1")
	pp.RecordReassignment("user-1")

	if pp.IsQuarantined("user-1") {
		t.Fatal("should not be quarantined with only 2 reassignments")
	}
}

func TestPoisonPill_QuarantineAtThreshold(t *testing.T) {
	t.Parallel()
	pp := NewPoisonPillDetector(3, time.Minute)
	t.Cleanup(pp.Stop)

	pp.RecordReassignment("user-1")
	pp.RecordReassignment("user-1")
	quarantined := pp.RecordReassignment("user-1")

	if !quarantined {
		t.Fatal("3rd reassignment should trigger quarantine")
	}
	if !pp.IsQuarantined("user-1") {
		t.Fatal("user-1 should be quarantined")
	}
}

func TestPoisonPill_WindowExpiry(t *testing.T) {
	t.Parallel()
	pp := NewPoisonPillDetector(3, 50*time.Millisecond)
	t.Cleanup(pp.Stop)

	pp.RecordReassignment("user-1")
	pp.RecordReassignment("user-1")

	// Wait for window to expire.
	time.Sleep(80 * time.Millisecond)

	// These are within a new window — only 1 event.
	quarantined := pp.RecordReassignment("user-1")

	if quarantined {
		t.Fatal("old events should have expired, only 1 in current window")
	}
	if pp.IsQuarantined("user-1") {
		t.Fatal("user-1 should not be quarantined")
	}
}

func TestPoisonPill_Unquarantine(t *testing.T) {
	t.Parallel()
	pp := NewPoisonPillDetector(2, time.Minute)
	t.Cleanup(pp.Stop)

	pp.RecordReassignment("user-1")
	pp.RecordReassignment("user-1")

	if !pp.IsQuarantined("user-1") {
		t.Fatal("should be quarantined")
	}

	pp.Unquarantine("user-1")

	if pp.IsQuarantined("user-1") {
		t.Fatal("should no longer be quarantined")
	}
}

func TestPoisonPill_QuarantinedAccounts(t *testing.T) {
	t.Parallel()
	pp := NewPoisonPillDetector(2, time.Minute)
	t.Cleanup(pp.Stop)

	pp.RecordReassignment("user-1")
	pp.RecordReassignment("user-1")
	pp.RecordReassignment("user-2")
	pp.RecordReassignment("user-2")

	accounts := pp.QuarantinedAccounts()
	if len(accounts) != 2 {
		t.Fatalf("expected 2 quarantined accounts, got %d", len(accounts))
	}

	found := make(map[string]bool)
	for _, a := range accounts {
		found[a] = true
	}
	if !found["user-1"] || !found["user-2"] {
		t.Fatalf("expected user-1 and user-2 in quarantine, got %v", accounts)
	}
}

func TestPoisonPill_IsolationBetweenUsers(t *testing.T) {
	t.Parallel()
	pp := NewPoisonPillDetector(3, time.Minute)
	t.Cleanup(pp.Stop)

	pp.RecordReassignment("user-1")
	pp.RecordReassignment("user-1")
	pp.RecordReassignment("user-2")

	// user-1 has 2, user-2 has 1 — neither at threshold.
	if pp.IsQuarantined("user-1") || pp.IsQuarantined("user-2") {
		t.Fatal("neither user should be quarantined yet")
	}
}

func TestPoisonPill_AlreadyQuarantinedReturnsTrue(t *testing.T) {
	t.Parallel()
	pp := NewPoisonPillDetector(2, time.Minute)
	t.Cleanup(pp.Stop)

	pp.RecordReassignment("user-1")
	pp.RecordReassignment("user-1") // quarantined

	// Further calls still return true.
	if !pp.RecordReassignment("user-1") {
		t.Fatal("should return true for already-quarantined account")
	}
}

func TestPoisonPill_UnquarantineNonExistent(t *testing.T) {
	t.Parallel()
	pp := NewPoisonPillDetector(3, time.Minute)
	t.Cleanup(pp.Stop)

	// Should not panic.
	pp.Unquarantine("nonexistent")
}

func TestPoisonPill_NotQuarantinedByDefault(t *testing.T) {
	t.Parallel()
	pp := NewPoisonPillDetector(3, time.Minute)
	t.Cleanup(pp.Stop)

	if pp.IsQuarantined("unknown") {
		t.Fatal("unknown key should not be quarantined")
	}
}
