package proxy

import (
	"testing"
)

func TestHashUser_Deterministic(t *testing.T) {
	t.Parallel()

	input := "user-123"
	hash1 := HashUser(input)
	hash2 := HashUser(input)

	if hash1 != hash2 {
		t.Errorf("HashUser is not deterministic: %d != %d", hash1, hash2)
	}
}

func TestHashUser_DifferentInputs(t *testing.T) {
	t.Parallel()

	inputs := []string{
		"user-1",
		"user-2",
		"user-3",
		"admin",
		"test@example.com",
		"a-very-long-user-id-that-is-different",
	}

	hashes := make(map[uint32]string)
	for _, input := range inputs {
		h := HashUser(input)
		if prev, exists := hashes[h]; exists {
			t.Errorf("hash collision: %q and %q both produce %d", prev, input, h)
		}
		hashes[h] = input
	}
}

func TestHashUser_EmptyString(t *testing.T) {
	t.Parallel()

	// Should not panic
	h := HashUser("")
	// CRC32 of empty string is 0
	if h != 0 {
		t.Errorf("expected 0 for empty string, got %d", h)
	}
}

func TestHashUser_ConsistentAcrossCalls(t *testing.T) {
	t.Parallel()

	// Run multiple times to ensure consistency
	const iterations = 1000
	input := "consistency-test-user"
	expected := HashUser(input)

	for i := 0; i < iterations; i++ {
		got := HashUser(input)
		if got != expected {
			t.Fatalf("iteration %d: expected %d, got %d", i, expected, got)
		}
	}
}
