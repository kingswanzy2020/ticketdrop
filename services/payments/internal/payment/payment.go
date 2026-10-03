// Package payment is the payments service: it charges the customer for each
// new order and announces whether the charge went through.
package payment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kingswanzy2020/ticketdrop/pkg/contracts"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/events"
)

// Source is the name this service publishes events under.
const Source = "payments"

const (
	statusSucceeded = "succeeded"
	statusDeclined  = "declined"
	// The order reached payments after its hold had run out, and was not charged.
	statusExpired = "expired"
)

// ErrProviderDown is returned when the payment provider could not be reached.
// It is a temporary failure: the message stays on the queue and is retried.
var ErrProviderDown = errors.New("payment provider unavailable")

// Processor charges orders through a simulated payment provider. The three
// settings below are the provider's behaviour, there to be turned up in a
// failure exercise.
type Processor struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	// Latency is the provider's response time. A call takes between Latency
	// and twice Latency.
	Latency time.Duration
	// DeclineRate is the share of charges the provider declines, 0 to 1. A
	// decline is an answer, not a fault: the order fails and nothing retries.
	DeclineRate float64
	// ErrorRate is the share of calls that fail outright, 0 to 1, as during a
	// provider outage. These are retried, and dead-lettered if they keep
	// failing.
	ErrorRate float64
}

// HandleEvent charges the customer for one order.created event.
func (p *Processor) HandleEvent(ctx context.Context, e events.Event) error {
	// The queue's filter policy admits only the types handled here. Anything
	// else means the subscription is misconfigured. Failing sends the message
	// to the dead-letter queue, where it is seen, instead of dropping it.
	if e.Type != contracts.OrderCreated {
		return fmt.Errorf("unexpected event type %q", e.Type)
	}
	var o contracts.Order
	if err := e.Decode(&o); err != nil {
		return err
	}
	if o.OrderID == "" {
		return fmt.Errorf("event %s: order_id is required", e.ID)
	}

	typ, status, reason := contracts.PaymentSucceeded, statusSucceeded, ""
	if expired(o, time.Now()) {
		// The tickets are no longer set aside for this order and may already
		// belong to someone else. Charging now would take money for nothing,
		// so the order fails without the provider being called. This is what
		// an order.created retried long after an outage runs into.
		typ, status, reason = contracts.PaymentFailed, statusExpired, "hold expired"
	} else {
		// The provider is called before the transaction opens, so no database
		// connection is held while waiting for it.
		//
		// That leaves a window: a crash after the provider has charged and
		// before the outcome is stored means the redelivery calls the provider
		// again. A real provider closes it with an idempotency key. Given the
		// order ID as that key, a second call returns the first outcome
		// instead of charging again. Here, the second call's outcome is
		// discarded by Once below.
		declined, err := p.charge(ctx)
		if err != nil {
			return err
		}
		if declined {
			typ, status, reason = contracts.PaymentFailed, statusDeclined, "card declined"
		}
	}
	outcome, err := events.New(ctx, Source, typ, contracts.Payment{Order: o, Reason: reason})
	if err != nil {
		return err
	}

	err = events.Once(ctx, p.Pool, e, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO payments (order_id, status, reason) VALUES ($1, $2, NULLIF($3, ''))`,
			o.OrderID, status, reason)
		if err != nil {
			return fmt.Errorf("record payment for order %s: %w", o.OrderID, err)
		}
		return events.Enqueue(ctx, tx, outcome)
	})
	if err != nil {
		return err
	}
	p.Log.InfoContext(ctx, "payment "+status, "order_id", o.OrderID)
	return nil
}

// expired reports whether the order's hold has run out at the time given. An
// order with no expiry, as published before the field existed, never has.
func expired(o contracts.Order, now time.Time) bool {
	return !o.HoldExpiresAt.IsZero() && !now.Before(o.HoldExpiresAt)
}

// charge stands in for the call to a payment provider. It reports whether the
// charge was declined, or ErrProviderDown when the provider gave no answer.
func (p *Processor) charge(ctx context.Context) (declined bool, err error) {
	wait := p.Latency
	if wait > 0 {
		wait += rand.N(wait)
	}
	select {
	case <-time.After(wait):
	case <-ctx.Done():
		return false, ctx.Err()
	}

	if rand.Float64() < p.ErrorRate {
		return false, ErrProviderDown
	}
	return rand.Float64() < p.DeclineRate, nil
}
