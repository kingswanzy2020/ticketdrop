package db

import "testing"

const testURL = "postgres://u:p@localhost:5432/d"

func TestPoolConfigCapsThePool(t *testing.T) {
	cfg, err := poolConfig(testURL, 7)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConns != 7 {
		t.Fatalf("MaxConns = %d, want 7", cfg.MaxConns)
	}
}

func TestPoolConfigZeroKeepsWhatTheURLSays(t *testing.T) {
	cfg, err := poolConfig(testURL+"?pool_max_conns=3", 0)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConns != 3 {
		t.Fatalf("MaxConns = %d, want 3 from the URL", cfg.MaxConns)
	}
}

func TestPoolConfigKeepsMinConnsWithinTheCap(t *testing.T) {
	cfg, err := poolConfig(testURL+"?pool_min_conns=10", 4)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MinConns != 4 {
		t.Fatalf("MinConns = %d, want 4", cfg.MinConns)
	}
}

func TestPoolConfigRefusesANegativeCap(t *testing.T) {
	if _, err := poolConfig(testURL, -1); err == nil {
		t.Fatal("a negative DATABASE_MAX_CONNS was accepted")
	}
}
