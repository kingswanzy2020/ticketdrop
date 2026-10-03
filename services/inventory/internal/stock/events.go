package stock

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/kingswanzy2020/ticketdrop/pkg/contracts"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/events"
)

// HandleEvent puts a drop's tickets on sale when the drop opens, and settles
// the hold of an order once its payment has an outcome.
func (s *Service) HandleEvent(ctx context.Context, e events.Event) error {
	switch e.Type {
	case contracts.DropOpened:
		return s.open(ctx, e)
	case contracts.PaymentSucceeded:
		return s.confirm(ctx, e)
	case contracts.PaymentFailed:
		return s.release(ctx, e)
	default:
		// The queue's filter policy admits only the types handled here.
		// Anything else means the subscription is misconfigured. Failing sends
		// the message to the dead-letter queue, where it is seen.
		return fmt.Errorf("unexpected event type %q", e.Type)
	}
}

// open creates the stock of a drop that has gone on sale. Until this has
// happened the drop has no stock, and a hold for it is refused.
func (s *Service) open(ctx context.Context, e events.Event) error {
	var d contracts.Drop
	if err := e.Decode(&d); err != nil {
		return err
	}
	if d.DropID == "" || len(d.Tiers) == 0 {
		return fmt.Errorf("event %s: a drop_id and at least one tier are required", e.ID)
	}

	err := events.Once(ctx, s.Pool, e, func(tx pgx.Tx) error {
		for _, t := range d.Tiers {
			// A tier that already has stock is left alone. Resetting it would
			// put tickets that are held or sold back on sale.
			_, err := tx.Exec(ctx, `
				INSERT INTO stock (drop_id, tier, capacity, available) VALUES ($1, $2, $3, $3)
				ON CONFLICT (drop_id, tier) DO NOTHING`,
				d.DropID, t.Tier, t.Capacity)
			if err != nil {
				return fmt.Errorf("create stock of %s/%s: %w", d.DropID, t.Tier, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.Log.InfoContext(ctx, "drop on sale", "drop_id", d.DropID, "tiers", len(d.Tiers))
	return nil
}

// confirm turns a held order's tickets into sold ones and announces it.
func (s *Service) confirm(ctx context.Context, e events.Event) error {
	var p contracts.Payment
	if err := e.Decode(&p); err != nil {
		return err
	}
	confirmed, err := events.New(ctx, Source, contracts.OrderConfirmed, p.Order)
	if err != nil {
		return err
	}

	err = events.Once(ctx, s.Pool, e, func(tx pgx.Tx) error {
		// The tickets left stock when the hold was made, so stock is not
		// touched here. Confirming only makes the hold permanent.
		tag, err := tx.Exec(ctx, `
			UPDATE holds SET status = $2, settled_at = now()
			WHERE order_id = $1 AND status = $3`,
			p.OrderID, statusConfirmed, statusHeld)
		if err != nil {
			return fmt.Errorf("confirm hold of order %s: %w", p.OrderID, err)
		}
		if tag.RowsAffected() == 0 {
			// The customer has paid and there are no tickets set aside for
			// them. No retry fixes that, and silently dropping it would keep
			// their money for nothing. The error sends the message to the
			// dead-letter queue, where a person decides.
			return fmt.Errorf("order %s is paid but has no live hold", p.OrderID)
		}
		return events.Enqueue(ctx, tx, confirmed)
	})
	if err != nil {
		return err
	}
	s.Log.InfoContext(ctx, "hold confirmed", "order_id", p.OrderID, "tickets", p.Quantity)
	return nil
}

// release puts a held order's tickets back into stock.
func (s *Service) release(ctx context.Context, e events.Event) error {
	var p contracts.Payment
	if err := e.Decode(&p); err != nil {
		return err
	}

	return events.Once(ctx, s.Pool, e, func(tx pgx.Tx) error {
		var (
			dropID, tier string
			quantity     int
		)
		err := tx.QueryRow(ctx, `
			UPDATE holds SET status = $2, settled_at = now()
			WHERE order_id = $1 AND status = $3
			RETURNING drop_id, tier, quantity`,
			p.OrderID, statusReleased, statusHeld,
		).Scan(&dropID, &tier, &quantity)
		if errors.Is(err, pgx.ErrNoRows) {
			// Nothing is held for this order, so there is nothing to put back.
			// This is expected when the hold expired before the payment failed.
			s.Log.InfoContext(ctx, "payment failed for an order with no live hold",
				"order_id", p.OrderID, "event_id", e.ID)
			return nil
		}
		if err != nil {
			return fmt.Errorf("release hold of order %s: %w", p.OrderID, err)
		}

		// The quantity comes from the hold, not from the event: what goes back
		// is exactly what was taken.
		_, err = tx.Exec(ctx,
			`UPDATE stock SET available = available + $3 WHERE drop_id = $1 AND tier = $2`,
			dropID, tier, quantity)
		if err != nil {
			return fmt.Errorf("return stock of order %s: %w", p.OrderID, err)
		}
		s.Log.InfoContext(ctx, "hold released", "order_id", p.OrderID, "tickets", quantity, "reason", p.Reason)
		return nil
	})
}
