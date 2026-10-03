package payment

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kingswanzy2020/ticketdrop/pkg/contracts"
)

func TestCharge(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name         string
		p            Processor
		wantDeclined bool
		wantErr      error
	}{
		{"healthy provider approves", Processor{}, false, nil},
		{"every charge declined", Processor{DeclineRate: 1}, true, nil},
		{"provider down", Processor{ErrorRate: 1}, false, ErrProviderDown},
		// An outage gives no answer at all, so it wins over a decline.
		{"provider down and declining", Processor{ErrorRate: 1, DeclineRate: 1}, false, ErrProviderDown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			declined, err := tt.p.charge(ctx)
			if declined != tt.wantDeclined || !errors.Is(err, tt.wantErr) {
				t.Errorf("charge() = %v, %v; want %v, %v", declined, err, tt.wantDeclined, tt.wantErr)
			}
		})
	}
}

func TestChargeTakesBetweenOneAndTwoLatencies(t *testing.T) {
	p := Processor{Latency: 20 * time.Millisecond}

	start := time.Now()
	if _, err := p.charge(context.Background()); err != nil {
		t.Fatal(err)
	}

	if took := time.Since(start); took < p.Latency || took > 4*p.Latency {
		t.Errorf("charge() took %v, want between %v and %v", took, p.Latency, 2*p.Latency)
	}
}

func TestChargeStopsWhenCancelled(t *testing.T) {
	p := Processor{Latency: time.Minute}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := p.charge(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("charge() = %v, want context.Canceled", err)
	}
}

func TestExpired(t *testing.T) {
	deadline := time.Date(2027, 6, 1, 10, 10, 0, 0, time.UTC)

	tests := []struct {
		name string
		hold time.Time
		now  time.Time
		want bool
	}{
		{"well before the deadline", deadline, deadline.Add(-time.Minute), false},
		{"a moment before", deadline, deadline.Add(-time.Millisecond), false},
		{"exactly at the deadline", deadline, deadline, true},
		{"after the deadline", deadline, deadline.Add(time.Hour), true},
		// An event published before the field existed carries no deadline.
		{"no deadline at all", time.Time{}, deadline.Add(time.Hour), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := contracts.Order{OrderID: "x", HoldExpiresAt: tt.hold}
			if got := expired(o, tt.now); got != tt.want {
				t.Errorf("expired() = %v, want %v", got, tt.want)
			}
		})
	}
}
