package stock

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestAllocateMatchesGrantingOneAtATime(t *testing.T) {
	tests := []struct {
		name       string
		available  int
		quantities []int
		granted    []bool
		left       int
	}{
		{"all fit", 10, []int{2, 3, 4}, []bool{true, true, true}, 1},
		{"the last ticket goes to the first who asked", 1, []int{1, 1, 1}, []bool{true, false, false}, 0},
		// A request too big for what is left is refused, and a smaller one
		// after it still gets in, as it would have one at a time.
		{"a smaller request after a refused one", 3, []int{2, 4, 1}, []bool{true, false, true}, 0},
		{"nothing left", 0, []int{1, 2}, []bool{false, false}, 0},
		{"exactly sold out", 6, []int{4, 2}, []bool{true, true}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			granted, left := allocate(tt.available, tt.quantities)
			if !slices.Equal(granted, tt.granted) || left != tt.left {
				t.Errorf("allocate(%d, %v) = %v, %d; want %v, %d",
					tt.available, tt.quantities, granted, left, tt.granted, tt.left)
			}
			taken := 0
			for i, g := range granted {
				if g {
					taken += tt.quantities[i]
				}
			}
			if taken > tt.available {
				t.Errorf("granted %d tickets of %d", taken, tt.available)
			}
		})
	}
}

func TestCollectTakesWhatIsWaitingWithoutWaitingForMore(t *testing.T) {
	queue := make(chan holdCall, 10)
	for i := 0; i < 4; i++ {
		queue <- holdCall{req: HoldRequest{Quantity: i}}
	}

	start := time.Now()
	batch := collect(holdCall{req: HoldRequest{Quantity: 99}}, queue, 3)

	if len(batch) != 3 || batch[0].req.Quantity != 99 || batch[1].req.Quantity != 0 || batch[2].req.Quantity != 1 {
		t.Errorf("batch = %v, want the first call and the next two in order", batch)
	}
	if len(queue) != 2 {
		t.Errorf("%d calls left waiting, want 2", len(queue))
	}
	if alone := collect(holdCall{}, make(chan holdCall), 10); len(alone) != 1 {
		t.Errorf("with nothing waiting, batch has %d calls, want 1", len(alone))
	}
	if waited := time.Since(start); waited > 100*time.Millisecond {
		t.Errorf("collect took %v; it must not wait", waited)
	}
}

func TestHoldGivesUpWithItsCaller(t *testing.T) {
	// No batcher is running, so nothing will ever answer.
	s := &Service{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, _, err := s.Hold(ctx, HoldRequest{})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Hold() = %v, want the caller's deadline", err)
	}
}

func TestGrantAnswersACallerWhoHasGoneWithoutTouchingTheDatabase(t *testing.T) {
	// No Pool: reaching the database would panic.
	s := &Service{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	call := holdCall{ctx: ctx, req: HoldRequest{Quantity: 2}, reply: make(chan holdResult, 1)}

	s.grant([]holdCall{call})

	if r := <-call.reply; !errors.Is(r.err, context.Canceled) || r.created {
		t.Errorf("answer = %+v, want context.Canceled and no hold", r)
	}
}
