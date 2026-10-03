// Package ticket is the fulfillment service: it turns an order into tickets.
package ticket

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kingswanzy2020/ticketdrop/pkg/contracts"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/events"
)

// Source is the name this service publishes events under.
const Source = "fulfillment"

// Issuer issues the tickets of each order it is told about.
type Issuer struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	// RenderTime stands in for rendering the ticket (QR code and PDF), which
	// is the slow part of this service and is not built yet. It gives the
	// queue a backlog to scale on and a window in which to kill the service.
	RenderTime time.Duration
}

// HandleEvent issues the tickets for one order.confirmed event: an order that
// has been paid for and whose tickets inventory has set aside for good.
func (i *Issuer) HandleEvent(ctx context.Context, e events.Event) error {
	// The queue's filter policy admits only the types handled here. Anything
	// else means the subscription is misconfigured. Failing sends the message
	// to the dead-letter queue, where it is seen, instead of dropping it.
	if e.Type != contracts.OrderConfirmed {
		return fmt.Errorf("unexpected event type %q", e.Type)
	}
	var o contracts.Order
	if err := e.Decode(&o); err != nil {
		return err
	}
	if o.OrderID == "" || o.Quantity < 1 {
		return fmt.Errorf("event %s: order_id and a positive quantity are required", e.ID)
	}

	// The slow work happens before the transaction opens, so no database
	// connection or row lock is held while it runs.
	select {
	case <-time.After(i.RenderTime):
	case <-ctx.Done():
		return ctx.Err()
	}

	issued, err := events.New(ctx, Source, contracts.TicketIssued, contracts.Tickets{
		OrderID: o.OrderID,
		Count:   o.Quantity,
	})
	if err != nil {
		return err
	}

	// Three writes commit together: the record that this event was handled,
	// the tickets, and the ticket.issued event. A crash before the commit
	// leaves none of them and the redelivery starts clean. A crash after it
	// leaves all of them and the redelivery is recognised as a duplicate.
	err = events.Once(ctx, i.Pool, e, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO tickets (order_id, drop_id, tier, seq)
			SELECT $1, $2, $3, n FROM generate_series(1, $4::int) AS n`,
			o.OrderID, o.DropID, o.Tier, o.Quantity)
		if err != nil {
			return fmt.Errorf("issue tickets for order %s: %w", o.OrderID, err)
		}
		return events.Enqueue(ctx, tx, issued)
	})
	if err != nil {
		return err
	}
	i.Log.InfoContext(ctx, "tickets issued", "order_id", o.OrderID, "tickets", o.Quantity)
	return nil
}
