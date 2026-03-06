package proxy

import (
	"context"
	"testing"
)

func TestMemoryStore_BackendManagement(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore()

	ctx := context.Background()

	// Initially empty.
	backends, err := s.ActiveBackends(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(backends) != 0 {
		t.Fatalf("expected 0 backends, got %d", len(backends))
	}

	// Add backends.
	if err := s.AddBackend(ctx, "http://b2:8080"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddBackend(ctx, "http://b1:8080"); err != nil {
		t.Fatal(err)
	}

	backends, _ = s.ActiveBackends(ctx)
	if len(backends) != 2 {
		t.Fatalf("expected 2 backends, got %d", len(backends))
	}
	// Should be sorted.
	if backends[0] != "http://b1:8080" || backends[1] != "http://b2:8080" {
		t.Errorf("expected sorted backends, got %v", backends)
	}

	// Idempotent add.
	if err := s.AddBackend(ctx, "http://b1:8080"); err != nil {
		t.Fatal(err)
	}
	backends, _ = s.ActiveBackends(ctx)
	if len(backends) != 2 {
		t.Fatalf("duplicate add should be no-op, got %d", len(backends))
	}

	// Remove.
	if err := s.RemoveBackend(ctx, "http://b1:8080"); err != nil {
		t.Fatal(err)
	}
	backends, _ = s.ActiveBackends(ctx)
	if len(backends) != 1 || backends[0] != "http://b2:8080" {
		t.Errorf("expected [http://b2:8080], got %v", backends)
	}

	// Remove non-existent is no-op.
	if err := s.RemoveBackend(ctx, "http://b99:8080"); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryStore_AssignmentMethodsReturnError(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore()
	ctx := context.Background()

	if _, err := s.AssignLeastLoaded(ctx, "user"); err == nil {
		t.Error("expected error from AssignLeastLoaded")
	}
	if _, err := s.GetAssignment(ctx, "user"); err == nil {
		t.Error("expected error from GetAssignment")
	}
	if _, err := s.GetAllAssignments(ctx); err == nil {
		t.Error("expected error from GetAllAssignments")
	}
	if _, err := s.GetBackendUsers(ctx, "b"); err == nil {
		t.Error("expected error from GetBackendUsers")
	}
	if _, err := s.BulkAssign(ctx, map[string]string{"a": "b"}); err == nil {
		t.Error("expected error from BulkAssign")
	}
	if err := s.BulkDeleteAssignments(ctx, []string{"a"}); err == nil {
		t.Error("expected error from BulkDeleteAssignments")
	}
}

func TestMemoryStore_PingAndClose(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore()

	if err := s.Ping(context.Background()); err != nil {
		t.Errorf("expected nil from Ping, got %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("expected nil from Close, got %v", err)
	}
}
