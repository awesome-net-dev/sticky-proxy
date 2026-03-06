package config

import (
	"testing"
)

func TestWSSwapOnRebalance_DefaultTrue(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("WS_SWAP_ON_REBALANCE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.WSSwapOnRebalance {
		t.Error("expected WSSwapOnRebalance to default to true")
	}
}

func TestWSSwapOnRebalance_ExplicitTrue(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("WS_SWAP_ON_REBALANCE", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.WSSwapOnRebalance {
		t.Error("expected WSSwapOnRebalance to be true")
	}
}

func TestWSSwapOnRebalance_SetFalse(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("WS_SWAP_ON_REBALANCE", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WSSwapOnRebalance {
		t.Error("expected WSSwapOnRebalance to be false")
	}
}
