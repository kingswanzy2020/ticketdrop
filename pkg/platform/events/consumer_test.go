package events

import (
	"context"
	"testing"
)

func TestReserve(t *testing.T) {
	ctx := context.Background()
	sem := make(chan struct{}, 4)

	if n := reserve(ctx, sem, 10); n != 4 {
		t.Fatalf("empty pool of 4, max 10: reserved %d, want 4", n)
	}
	release(sem, 3)
	if n := reserve(ctx, sem, 2); n != 2 {
		t.Fatalf("3 free, max 2: reserved %d, want 2", n)
	}
	if n := reserve(ctx, sem, 10); n != 1 {
		t.Fatalf("1 free, max 10: reserved %d, want 1", n)
	}

	// Every slot is taken. Reserve must wait, and give up once ctx is cancelled.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if n := reserve(cancelled, sem, 1); n != 0 {
		t.Fatalf("full pool, cancelled context: reserved %d, want 0", n)
	}
}
