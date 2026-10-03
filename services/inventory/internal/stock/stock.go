// Package stock is the inventory service: how many tickets each tier of a drop
// has left, and which orders are holding some of them.
package stock

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Source is the name this service publishes events under.
const Source = "inventory"

const (
	statusHeld      = "held"
	statusConfirmed = "confirmed"
	statusReleased  = "released"
)

var (
	// ErrSoldOut means the tier has fewer tickets left than were asked for.
	ErrSoldOut = errors.New("not enough tickets left")
	// ErrNoTier means the drop is not on sale, or has no such tier.
	ErrNoTier = errors.New("no such drop or tier")

	errAlreadyHeld = errors.New("order already has a hold")
)

// Tier is the stock of one tier of a drop.
type Tier struct {
	Tier      string `json:"tier"`
	Capacity  int    `json:"capacity"`
	Available int    `json:"available"`
}

// HoldRequest is the body of POST /internal/v1/holds.
type HoldRequest struct {
	OrderID  string `json:"order_id"`
	DropID   string `json:"drop_id"`
	Tier     string `json:"tier"`
	Quantity int    `json:"quantity"`
}

// Validate returns what is wrong with the request, or "" when it is acceptable.
func (r HoldRequest) Validate() string {
	switch {
	case uuid.Validate(r.OrderID) != nil:
		return "order_id must be a UUID"
	case r.DropID == "" || r.Tier == "":
		return "drop_id and tier are required"
	case r.Quantity < 1:
		return "quantity must be at least 1"
	}
	return ""
}

// Hold is a number of tickets set aside for one order.
type Hold struct {
	ID        string    `json:"hold_id"`
	OrderID   string    `json:"order_id"`
	DropID    string    `json:"drop_id"`
	Tier      string    `json:"tier"`
	Quantity  int       `json:"quantity"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Service holds what the HTTP handlers and the event handler share.
type Service struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	// HoldTTL is how long a hold lasts when no payment outcome arrives.
	HoldTTL time.Duration
	// HoldGrace is how long past its expiry a hold is kept before it is
	// released (see ReleaseExpired).
	HoldGrace time.Duration
}

// Availability returns the stock of every tier of a drop. It returns no tiers
// when the drop is not on sale.
func (s *Service) Availability(ctx context.Context, dropID string) ([]Tier, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT tier, capacity, available FROM stock WHERE drop_id = $1 ORDER BY tier`, dropID)
	if err != nil {
		return nil, fmt.Errorf("read stock of %s: %w", dropID, err)
	}
	tiers, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Tier])
	if err != nil {
		return nil, fmt.Errorf("read stock of %s: %w", dropID, err)
	}
	return tiers, nil
}

// Hold takes tickets out of stock for an order. It reports whether a new hold
// was made: asking again for an order that already has one returns that hold
// and takes nothing more.
func (s *Service) Hold(ctx context.Context, req HoldRequest) (h Hold, created bool, err error) {
	h = Hold{
		ID:        uuid.NewString(),
		OrderID:   req.OrderID,
		DropID:    req.DropID,
		Tier:      req.Tier,
		Quantity:  req.Quantity,
		ExpiresAt: time.Now().Add(s.HoldTTL).UTC(),
	}

	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		// This statement is what prevents overselling. The comparison and the
		// subtraction happen under one row lock, so two orders racing for the
		// last ticket are served one after the other and the second finds
		// none left. Reading the number first and writing it back afterwards
		// would let both succeed.
		tag, err := tx.Exec(ctx, `
			UPDATE stock SET available = available - $3
			WHERE drop_id = $1 AND tier = $2 AND available >= $3`,
			h.DropID, h.Tier, h.Quantity)
		if err != nil {
			return fmt.Errorf("take stock: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrSoldOut
		}

		tag, err = tx.Exec(ctx, `
			INSERT INTO holds (id, order_id, drop_id, tier, quantity, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (order_id) DO NOTHING`,
			h.ID, h.OrderID, h.DropID, h.Tier, h.Quantity, h.ExpiresAt)
		if err != nil {
			return fmt.Errorf("record hold: %w", err)
		}
		if tag.RowsAffected() == 0 {
			// Returning an error rolls the transaction back, which puts the
			// tickets just taken back into stock.
			return errAlreadyHeld
		}
		return nil
	})

	switch {
	case err == nil:
		return h, true, nil
	case errors.Is(err, errAlreadyHeld):
		h, err = s.holdOf(ctx, req.OrderID)
		return h, false, err
	case errors.Is(err, ErrSoldOut):
		return Hold{}, false, s.whyNothingTaken(ctx, req)
	default:
		return Hold{}, false, err
	}
}

// whyNothingTaken explains an UPDATE of stock that matched no row. That has
// two causes, and they deserve different answers.
func (s *Service) whyNothingTaken(ctx context.Context, req HoldRequest) error {
	var exists bool
	err := s.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM stock WHERE drop_id = $1 AND tier = $2)`,
		req.DropID, req.Tier,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("read stock of %s/%s: %w", req.DropID, req.Tier, err)
	}
	if !exists {
		return ErrNoTier
	}
	return ErrSoldOut
}

func (s *Service) holdOf(ctx context.Context, orderID string) (Hold, error) {
	var h Hold
	err := s.Pool.QueryRow(ctx, `
		SELECT id::text, order_id::text, drop_id, tier, quantity, expires_at
		FROM holds WHERE order_id = $1`, orderID,
	).Scan(&h.ID, &h.OrderID, &h.DropID, &h.Tier, &h.Quantity, &h.ExpiresAt)
	if err != nil {
		return Hold{}, fmt.Errorf("read hold of order %s: %w", orderID, err)
	}
	return h, nil
}
