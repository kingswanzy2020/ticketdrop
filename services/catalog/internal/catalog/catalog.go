// Package catalog is the catalog service: what is for sale, where, when it
// goes on sale, and how many tickets of each kind there are.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kingswanzy2020/ticketdrop/pkg/contracts"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/events"
)

// Source is the name this service publishes events under.
const Source = "catalog"

const (
	statusScheduled = "scheduled"
	statusOpen      = "open"
)

// The most drops returned by one listing.
const listLimit = 200

var (
	// ErrNotFound is returned when no drop has the requested ID.
	ErrNotFound = errors.New("drop not found")
	// ErrOpen is returned when a change is asked for on a drop that is on sale.
	ErrOpen = errors.New("drop is already on sale and can no longer be changed")
)

// Tier is one kind of ticket a drop sells.
type Tier struct {
	Tier       string `json:"tier"`
	PriceCents int    `json:"price_cents"`
	Capacity   int    `json:"capacity"`
}

// Drop is a drop as stored and as returned by the API.
type Drop struct {
	ID      string    `json:"drop_id"`
	Name    string    `json:"name"`
	Venue   string    `json:"venue"`
	OpensAt time.Time `json:"opens_at"`
	Status  string    `json:"status"`
	// Tiers is filled in when one drop is read, and left out of a listing.
	Tiers []Tier `json:"tiers,omitempty"`
}

// Request is the body of PUT /internal/v1/drops/{drop}.
type Request struct {
	Name    string    `json:"name"`
	Venue   string    `json:"venue"`
	OpensAt time.Time `json:"opens_at"`
	Tiers   []Tier    `json:"tiers"`
}

// Validate returns what is wrong with the request, or "" when it is acceptable.
func (r Request) Validate() string {
	switch {
	case r.Name == "" || len(r.Name) > 200:
		return "name is required and must be at most 200 characters"
	case r.Venue == "" || len(r.Venue) > 200:
		return "venue is required and must be at most 200 characters"
	case r.OpensAt.IsZero():
		return "opens_at is required, as a time such as 2027-06-01T10:00:00Z"
	case len(r.Tiers) == 0:
		return "at least one tier is required"
	}
	seen := map[string]bool{}
	for _, t := range r.Tiers {
		switch {
		case t.Tier == "" || len(t.Tier) > 64:
			return "each tier needs a name of at most 64 characters"
		case seen[t.Tier]:
			return fmt.Sprintf("tier %q is listed twice", t.Tier)
		case t.Capacity < 1:
			return fmt.Sprintf("tier %q needs a capacity of at least 1", t.Tier)
		case t.PriceCents < 0:
			return fmt.Sprintf("tier %q has a negative price", t.Tier)
		}
		seen[t.Tier] = true
	}
	return ""
}

// ValidID reports whether id can name a drop: 1 to 64 lowercase letters,
// digits and hyphens. The ID appears in URLs and in every order.
func ValidID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// Service holds what the HTTP handlers share.
type Service struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
}

// Save creates a drop, or replaces the details of one that has not opened
// yet. It returns ErrOpen for a drop that is on sale: by then inventory is
// counting its tickets, and changing them here would make the two disagree.
func (s *Service) Save(ctx context.Context, id string, req Request) (Drop, error) {
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		// The WHERE applies when the drop already exists. It lets the update
		// through only for a drop that is still scheduled, so a drop that is
		// on sale matches no row.
		tag, err := tx.Exec(ctx, `
			INSERT INTO drops (id, name, venue, opens_at) VALUES ($1, $2, $3, $4)
			ON CONFLICT (id) DO UPDATE
			SET name = EXCLUDED.name, venue = EXCLUDED.venue, opens_at = EXCLUDED.opens_at
			WHERE drops.status = $5`,
			id, req.Name, req.Venue, req.OpensAt, statusScheduled)
		if err != nil {
			return fmt.Errorf("save drop %s: %w", id, err)
		}
		if tag.RowsAffected() == 0 {
			return ErrOpen
		}

		if _, err := tx.Exec(ctx, `DELETE FROM tiers WHERE drop_id = $1`, id); err != nil {
			return fmt.Errorf("replace tiers of %s: %w", id, err)
		}
		for _, t := range req.Tiers {
			_, err := tx.Exec(ctx,
				`INSERT INTO tiers (drop_id, tier, price_cents, capacity) VALUES ($1, $2, $3, $4)`,
				id, t.Tier, t.PriceCents, t.Capacity)
			if err != nil {
				return fmt.Errorf("save tier %s/%s: %w", id, t.Tier, err)
			}
		}
		return nil
	})
	if err != nil {
		return Drop{}, err
	}
	return s.Get(ctx, id)
}

// Get returns one drop with its tiers, or ErrNotFound.
func (s *Service) Get(ctx context.Context, id string) (Drop, error) {
	d := Drop{ID: id}
	err := s.Pool.QueryRow(ctx,
		`SELECT name, venue, opens_at, status FROM drops WHERE id = $1`, id,
	).Scan(&d.Name, &d.Venue, &d.OpensAt, &d.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Drop{}, ErrNotFound
	}
	if err != nil {
		return Drop{}, fmt.Errorf("read drop %s: %w", id, err)
	}

	rows, err := s.Pool.Query(ctx,
		`SELECT tier, price_cents, capacity FROM tiers WHERE drop_id = $1 ORDER BY price_cents, tier`, id)
	if err != nil {
		return Drop{}, fmt.Errorf("read tiers of %s: %w", id, err)
	}
	d.Tiers, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Tier])
	if err != nil {
		return Drop{}, fmt.Errorf("read tiers of %s: %w", id, err)
	}
	return d, nil
}

// List returns the drops, soonest opening first, without their tiers.
func (s *Service) List(ctx context.Context) ([]Drop, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT id, name, venue, opens_at, status FROM drops ORDER BY opens_at, id LIMIT $1`, listLimit)
	if err != nil {
		return nil, fmt.Errorf("list drops: %w", err)
	}
	drops, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Drop, error) {
		var d Drop
		err := row.Scan(&d.ID, &d.Name, &d.Venue, &d.OpensAt, &d.Status)
		return d, err
	})
	if err != nil {
		return nil, fmt.Errorf("list drops: %w", err)
	}
	return drops, nil
}

// OpenDue puts on sale every scheduled drop whose opening time has come, and
// returns their IDs. Each one is announced with drop.opened, which is what
// makes inventory create its stock.
//
// It is safe to call from several places at once. The UPDATE takes each drop
// from scheduled to open exactly once: a second caller waits for the first,
// then finds the drop already open and skips it.
func (s *Service) OpenDue(ctx context.Context) ([]string, error) {
	var opened []string
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE drops SET status = $1, opened_at = now()
			WHERE status = $2 AND opens_at <= now()
			RETURNING id`,
			statusOpen, statusScheduled)
		if err != nil {
			return fmt.Errorf("open due drops: %w", err)
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("open due drops: %w", err)
		}

		for _, id := range ids {
			rows, err := tx.Query(ctx,
				`SELECT tier, capacity FROM tiers WHERE drop_id = $1 ORDER BY tier`, id)
			if err != nil {
				return fmt.Errorf("read tiers of %s: %w", id, err)
			}
			tiers, err := pgx.CollectRows(rows, pgx.RowToStructByPos[contracts.Tier])
			if err != nil {
				return fmt.Errorf("read tiers of %s: %w", id, err)
			}
			e, err := events.New(ctx, Source, contracts.DropOpened, contracts.Drop{DropID: id, Tiers: tiers})
			if err != nil {
				return err
			}
			if err := events.Enqueue(ctx, tx, e); err != nil {
				return err
			}
		}
		opened = ids
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, id := range opened {
		s.Log.InfoContext(ctx, "drop opened", "drop_id", id)
	}
	return opened, nil
}
