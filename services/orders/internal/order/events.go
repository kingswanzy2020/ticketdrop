package order

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/kingswanzy2020/ticketdrop/pkg/contracts"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/events"
)

// HandleEvent applies an event from the orders queue to the order it concerns.
// Each event it handles ends the order: afterwards it is no longer pending.
func (s *Service) HandleEvent(ctx context.Context, e events.Event) error {
	switch e.Type {
	case contracts.TicketIssued:
		var t contracts.Tickets
		if err := e.Decode(&t); err != nil {
			return err
		}
		return s.finish(ctx, e, t.OrderID, StatusTicketed, "")
	case contracts.PaymentFailed:
		var p contracts.Payment
		if err := e.Decode(&p); err != nil {
			return err
		}
		return s.finish(ctx, e, p.OrderID, StatusFailed, p.Reason)
	case contracts.HoldExpired:
		var h contracts.Hold
		if err := e.Decode(&h); err != nil {
			return err
		}
		return s.finish(ctx, e, h.OrderID, StatusFailed, "hold expired")
	default:
		// The queue's filter policy admits only the types handled here.
		// Anything else means the subscription is misconfigured. Failing sends
		// the message to the dead-letter queue, where it is seen.
		return fmt.Errorf("unexpected event type %q", e.Type)
	}
}

// finish moves a pending order to its final status and announces how it
// ended. reason says why a failed order failed, and is empty for a ticketed one.
func (s *Service) finish(ctx context.Context, e events.Event, orderID, status, reason string) error {
	return events.Once(ctx, s.Pool, e, func(tx pgx.Tx) error {
		// Only a pending order can finish. The condition makes the final
		// status stick: an event that arrives late or out of order cannot
		// turn a failed order into a ticketed one, or the reverse.
		var o contracts.Order
		err := tx.QueryRow(ctx, `
			UPDATE orders SET status = $2, failure_reason = NULLIF($4, ''), updated_at = now()
			WHERE id = $1 AND status = $3
			RETURNING id::text, drop_id, tier, quantity, customer_email`,
			orderID, status, StatusPending, reason,
		).Scan(&o.OrderID, &o.DropID, &o.Tier, &o.Quantity, &o.CustomerEmail)
		if errors.Is(err, pgx.ErrNoRows) {
			// Either the order has already finished, which is expected when
			// two events end it (a hold that expires and a payment that then
			// fails), or it does not exist. Retrying changes neither, so the
			// event is acknowledged.
			s.Log.InfoContext(ctx, "event for an order that is not pending",
				"order_id", orderID, "event_type", e.Type, "event_id", e.ID)
			return nil
		}
		if err != nil {
			return fmt.Errorf("mark order %s %s: %w", orderID, status, err)
		}

		// The announcement commits with the status change, so an order never
		// ends without one and is never announced twice.
		typ := contracts.OrderTicketed
		if status == StatusFailed {
			typ = contracts.OrderFailed
		}
		ended, err := events.New(ctx, Source, typ, contracts.Result{Order: o, Reason: reason})
		if err != nil {
			return err
		}
		if err := events.Enqueue(ctx, tx, ended); err != nil {
			return err
		}
		s.Log.InfoContext(ctx, "order "+status, "order_id", orderID, "reason", reason)
		return nil
	})
}
