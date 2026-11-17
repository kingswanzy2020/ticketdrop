// Package stock is the inventory service: how many tickets each tier of a drop
// has left, and which orders are holding some of them.
package stock

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
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
	// Metrics may be nil.
	Metrics *Metrics

	once  sync.Once
	queue chan holdCall
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
