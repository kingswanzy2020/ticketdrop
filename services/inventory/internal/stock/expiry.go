package stock

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/kingswanzy2020/ticketdrop/pkg/contracts"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/events"
)

// ReleaseExpired releases holds whose time ran out with no payment outcome,
// returns their tickets to stock, and announces each one with hold.expired.
// It releases at most limit holds and reports how many.
//
// A hold is released only once it is HoldGrace past its expiry. Payments
// stops charging at the expiry itself, so the grace period is the time a
// charge that began just before it has to finish and be confirmed here.
//
// It is safe to call from several places at once: SKIP LOCKED gives each
// caller different holds, so no hold is released twice.
func (s *Service) ReleaseExpired(ctx context.Context, limit int) (int, error) {
	var released int
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE holds SET status = $1, settled_at = now()
			WHERE id IN (
				SELECT id FROM holds
				WHERE status = $2 AND expires_at < now() - make_interval(secs => $3)
				ORDER BY expires_at
				LIMIT $4
				FOR UPDATE SKIP LOCKED)
			RETURNING order_id::text, drop_id, tier, quantity`,
			statusReleased, statusHeld, s.HoldGrace.Seconds(), limit)
		if err != nil {
			return fmt.Errorf("release expired holds: %w", err)
		}
		expired, err := pgx.CollectRows(rows, pgx.RowToStructByPos[contracts.Hold])
		if err != nil {
			return fmt.Errorf("release expired holds: %w", err)
		}

		// Tickets go back one tier at a time, in a fixed order. Two callers
		// returning stock to the same tiers in opposite orders would
		// otherwise each wait for a row the other has locked.
		type tier struct{ drop, name string }
		back := map[tier]int{}
		for _, h := range expired {
			back[tier{h.DropID, h.Tier}] += h.Quantity
		}
		tiers := slices.SortedFunc(maps.Keys(back), func(a, b tier) int {
			return cmp.Or(cmp.Compare(a.drop, b.drop), cmp.Compare(a.name, b.name))
		})
		for _, t := range tiers {
			_, err := tx.Exec(ctx,
				`UPDATE stock SET available = available + $3 WHERE drop_id = $1 AND tier = $2`,
				t.drop, t.name, back[t])
			if err != nil {
				return fmt.Errorf("return stock to %s/%s: %w", t.drop, t.name, err)
			}
		}

		for _, h := range expired {
			e, err := events.New(ctx, Source, contracts.HoldExpired, h)
			if err != nil {
				return err
			}
			if err := events.Enqueue(ctx, tx, e); err != nil {
				return err
			}
		}
		released = len(expired)
		return nil
	})
	if err != nil {
		return 0, err
	}
	if released > 0 {
		s.Log.InfoContext(ctx, "expired holds released", "holds", released)
	}
	return released, nil
}
