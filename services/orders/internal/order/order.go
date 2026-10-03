// Package order is the orders service: the order record, its HTTP API, and the
// events that move an order through its lifecycle.
package order

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kingswanzy2020/ticketdrop/pkg/contracts"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/events"
)

// Source is the name this service publishes events under.
const Source = "orders"

// An order is pending until one of two things happens: its tickets are issued,
// or its payment fails.
const (
	StatusPending  = "pending"
	StatusTicketed = "ticketed"
	StatusFailed   = "failed"
)

// MaxQuantity is the most tickets one order may ask for.
const MaxQuantity = 8

// ErrNotFound is returned when no order has the requested ID.
var ErrNotFound = errors.New("order not found")

// Order is an order as stored and as returned by the API.
type Order struct {
	ID            string `json:"order_id"`
	DropID        string `json:"drop_id"`
	Tier          string `json:"tier"`
	Quantity      int    `json:"quantity"`
	CustomerEmail string `json:"customer_email"`
	Status        string `json:"status"`
	// FailureReason says why a failed order failed. It is empty otherwise.
	FailureReason string    `json:"failure_reason,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// Request is the body of POST /v1/orders.
type Request struct {
	DropID        string `json:"drop_id"`
	Tier          string `json:"tier"`
	Quantity      int    `json:"quantity"`
	CustomerEmail string `json:"customer_email"`
}

// Validate returns what is wrong with the request, or "" when it is acceptable.
func (r Request) Validate() string {
	switch {
	case r.DropID == "" || len(r.DropID) > 64:
		return "drop_id is required and must be at most 64 characters"
	case r.Tier == "" || len(r.Tier) > 64:
		return "tier is required and must be at most 64 characters"
	case r.Quantity < 1 || r.Quantity > MaxQuantity:
		return fmt.Sprintf("quantity must be between 1 and %d", MaxQuantity)
	}
	// ParseAddress also accepts "Name <a@b.c>"; only the bare address is wanted.
	addr, err := mail.ParseAddress(r.CustomerEmail)
	if err != nil || addr.Address != r.CustomerEmail || len(r.CustomerEmail) > 254 {
		return "customer_email must be a valid email address"
	}
	return ""
}

// Service holds what the HTTP handlers and the event handler share.
type Service struct {
	Pool      *pgxpool.Pool
	Log       *slog.Logger
	Inventory *Inventory
}

// Create holds the order's tickets, then stores the pending order and its
// order.created event. It returns ErrSoldOut or ErrNoTier when inventory
// refuses the hold.
//
// The order and the event are written in one transaction. An order that exists
// always has its event waiting in the outbox, and a request that fails leaves
// neither.
func (s *Service) Create(ctx context.Context, req Request) (Order, error) {
	o := Order{
		ID:            uuid.NewString(),
		DropID:        req.DropID,
		Tier:          req.Tier,
		Quantity:      req.Quantity,
		CustomerEmail: req.CustomerEmail,
		Status:        StatusPending,
	}

	// The hold comes first, so that an order is never accepted for tickets
	// that are gone. The call crosses a service boundary and cannot share the
	// transaction below. If that transaction fails, the hold is left with no
	// order behind it, and inventory releases it when it expires.
	holdExpiresAt, err := s.Inventory.Hold(ctx, o)
	if err != nil {
		return Order{}, err
	}

	e, err := events.New(ctx, Source, contracts.OrderCreated, contracts.Order{
		OrderID:       o.ID,
		DropID:        o.DropID,
		Tier:          o.Tier,
		Quantity:      o.Quantity,
		CustomerEmail: o.CustomerEmail,
		HoldExpiresAt: holdExpiresAt,
	})
	if err != nil {
		return Order{}, err
	}

	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO orders (id, drop_id, tier, quantity, customer_email, status)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING created_at`,
			o.ID, o.DropID, o.Tier, o.Quantity, o.CustomerEmail, o.Status,
		).Scan(&o.CreatedAt)
		if err != nil {
			return fmt.Errorf("insert order: %w", err)
		}
		return events.Enqueue(ctx, tx, e)
	})
	if err != nil {
		return Order{}, err
	}
	return o, nil
}

// Get returns the order with the given ID, or ErrNotFound.
func (s *Service) Get(ctx context.Context, id string) (Order, error) {
	// Anything that is not a UUID cannot be an order ID. Answering here spares
	// the database a query that could only fail.
	if uuid.Validate(id) != nil {
		return Order{}, ErrNotFound
	}
	var o Order
	err := s.Pool.QueryRow(ctx, `
		SELECT id::text, drop_id, tier, quantity, customer_email, status,
		       coalesce(failure_reason, ''), created_at
		FROM orders WHERE id = $1`, id,
	).Scan(&o.ID, &o.DropID, &o.Tier, &o.Quantity, &o.CustomerEmail, &o.Status,
		&o.FailureReason, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, fmt.Errorf("read order %s: %w", id, err)
	}
	return o, nil
}
