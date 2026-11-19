package stock

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
)

// Holds are granted in batches.
//
// Every hold for a tier has to change the same stock row, and Postgres lets
// one transaction at a time do that, keeping the row locked until its commit
// has reached the disk. Granting one hold per transaction therefore caps a tier
// at one hold per commit, which a load test measured at about 120 a second
// (docs/PROOF.md, 17 November 2026). So each pod runs one batcher. While it is
// committing a batch, new requests queue up; it then takes all of them and
// grants them in a single transaction: one row lock and one flush to disk for
// the whole batch. When requests are rare a batch is a single request, so
// nothing waits on a timer.

// maxBatch is the most hold requests granted in one transaction.
const maxBatch = 500

// batchTimeout bounds one batch's transaction.
const batchTimeout = 5 * time.Second

// holdCall is one request waiting for the batcher, and where to send its answer.
type holdCall struct {
	ctx   context.Context
	req   HoldRequest
	reply chan holdResult
}

type holdResult struct {
	hold    Hold
	created bool
	err     error
}

// Metrics describes the batches.
type Metrics struct {
	batchSize prometheus.Histogram
}

// NewMetrics registers the inventory metrics on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		batchSize: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "inventory_hold_batch_size",
			Help:    "Hold requests granted or refused together in one transaction.",
			Buckets: []float64{1, 2, 5, 10, 20, 50, 100, 200, 500},
		}),
	}
	reg.MustRegister(m.batchSize)
	return m
}

// Hold takes tickets out of stock for an order. It reports whether a new hold
// was made: asking again for an order that already has one returns that hold
// and takes nothing more. It returns ErrSoldOut or ErrNoTier when it cannot.
//
// The request is granted by the batcher (RunBatcher), which must be running.
func (s *Service) Hold(ctx context.Context, req HoldRequest) (Hold, bool, error) {
	call := holdCall{ctx: ctx, req: req, reply: make(chan holdResult, 1)}
	select {
	case s.calls() <- call:
	case <-ctx.Done():
		return Hold{}, false, ctx.Err()
	}
	select {
	case r := <-call.reply:
		return r.hold, r.created, r.err
	case <-ctx.Done():
		return Hold{}, false, ctx.Err()
	}
}

func (s *Service) calls() chan holdCall {
	s.once.Do(func() { s.queue = make(chan holdCall, maxBatch) })
	return s.queue
}

// RunBatcher grants hold requests until ctx is cancelled. Register it as a
// worker: it stops after the HTTP server has drained, so no request is left
// waiting for it.
func (s *Service) RunBatcher(ctx context.Context) error {
	queue := s.calls()
	for {
		var first holdCall
		select {
		case <-ctx.Done():
			return nil
		case first = <-queue:
		}
		s.grant(collect(first, queue, maxBatch))
	}
}

// collect returns first and whatever else is already waiting, up to max. It
// does not wait for more.
func collect(first holdCall, queue <-chan holdCall, max int) []holdCall {
	batch := []holdCall{first}
	for len(batch) < max {
		select {
		case c := <-queue:
			batch = append(batch, c)
		default:
			return batch
		}
	}
	return batch
}

// allocate decides, in arrival order, which of the requested quantities fit in
// what is available. A request that does not fit is refused, and a smaller one
// after it may still fit: the same answers as granting them one at a time.
func allocate(available int, quantities []int) (granted []bool, left int) {
	granted = make([]bool, len(quantities))
	for i, q := range quantities {
		if q <= available {
			granted[i] = true
			available -= q
		}
	}
	return granted, available
}

// tierKey names one tier of one drop.
type tierKey struct{ drop, tier string }

// grant answers one batch of hold requests with one transaction.
func (s *Service) grant(calls []holdCall) {
	if s.Metrics != nil {
		s.Metrics.batchSize.Observe(float64(len(calls)))
	}

	// A caller that has already given up gets nothing held. Holding tickets
	// for it would only keep them out of sale until the hold expired.
	live := calls[:0]
	for _, c := range calls {
		if err := c.ctx.Err(); err != nil {
			c.reply <- holdResult{err: err}
			continue
		}
		live = append(live, c)
	}
	if len(live) == 0 {
		return
	}

	// The batch belongs to no single caller, so it is not cancelled when one
	// of them goes away.
	ctx, cancel := context.WithTimeout(context.Background(), batchTimeout)
	defer cancel()

	results := make([]holdResult, len(live))
	lookUp := map[int]bool{} // answered after the commit with the order's existing hold
	dupOf := map[int]int{}   // the same order earlier in this batch: same answer
	expires := time.Now().Add(s.HoldTTL).UTC()

	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		clear(results)
		clear(lookUp)
		clear(dupOf)

		// An order asked for again gets the hold it already has.
		ids := make([]string, len(live))
		for i, c := range live {
			ids[i] = c.req.OrderID
		}
		held, err := heldFor(ctx, tx, ids)
		if err != nil {
			return err
		}

		// The rest, by tier. An order that appears twice in the batch is
		// granted once; the second gets the first's hold.
		groups := map[tierKey][]int{}
		seen := map[string]int{}
		for i, c := range live {
			if held[c.req.OrderID] {
				lookUp[i] = true
				continue
			}
			if first, ok := seen[c.req.OrderID]; ok {
				dupOf[i] = first
				continue
			}
			seen[c.req.OrderID] = i
			k := tierKey{c.req.DropID, c.req.Tier}
			groups[k] = append(groups[k], i)
		}

		// Tiers are locked in a fixed order, the same order ReleaseExpired
		// uses, so two batches can never each wait for a row the other holds.
		tiers := slices.SortedFunc(maps.Keys(groups), func(a, b tierKey) int {
			return cmp.Or(cmp.Compare(a.drop, b.drop), cmp.Compare(a.tier, b.tier))
		})
		for _, k := range tiers {
			if err := s.grantTier(ctx, tx, k, live, groups[k], expires, results, lookUp); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.Log.Error("grant holds", "requests", len(live), "error", err)
		for _, c := range live {
			c.reply <- holdResult{err: err}
		}
		return
	}

	for i, c := range live {
		if lookUp[i] {
			h, err := s.holdOf(ctx, c.req.OrderID)
			results[i] = holdResult{hold: h, err: err}
		}
	}
	for i, first := range dupOf {
		results[i] = results[first]
		results[i].created = false
	}
	for i, c := range live {
		c.reply <- results[i]
	}
}

// grantTier grants what fits of one tier's requests, inside the batch's
// transaction.
func (s *Service) grantTier(ctx context.Context, tx pgx.Tx, k tierKey, live []holdCall, group []int,
	expires time.Time, results []holdResult, lookUp map[int]bool) error {

	// The row stays locked until the batch commits. This lock, together with
	// the CHECK on available, is what prevents overselling: no other batch
	// can read or change this tier's count until this one is done.
	var available int
	err := tx.QueryRow(ctx,
		`SELECT available FROM stock WHERE drop_id = $1 AND tier = $2 FOR UPDATE`,
		k.drop, k.tier).Scan(&available)
	if errors.Is(err, pgx.ErrNoRows) {
		for _, i := range group {
			results[i] = holdResult{err: ErrNoTier}
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock stock of %s/%s: %w", k.drop, k.tier, err)
	}

	quantities := make([]int, len(group))
	for n, i := range group {
		quantities[n] = live[i].req.Quantity
	}
	granted, _ := allocate(available, quantities)

	var holds []Hold
	for n, i := range group {
		if !granted[n] {
			results[i] = holdResult{err: ErrSoldOut}
			continue
		}
		r := live[i].req
		h := Hold{ID: uuid.NewString(), OrderID: r.OrderID, DropID: r.DropID, Tier: r.Tier, Quantity: r.Quantity, ExpiresAt: expires}
		holds = append(holds, h)
		results[i] = holdResult{hold: h, created: true}
	}
	if len(holds) == 0 {
		return nil
	}

	// Another pod may have held one of these orders since it was looked up.
	// Such a row is skipped here, its tickets are not taken, and the caller
	// gets the hold the other pod made.
	inserted, err := insertHolds(ctx, tx, holds)
	if err != nil {
		return err
	}
	taken := 0
	for n, i := range group {
		if !granted[n] {
			continue
		}
		if inserted[live[i].req.OrderID] {
			taken += live[i].req.Quantity
		} else {
			lookUp[i] = true
		}
	}

	_, err = tx.Exec(ctx,
		`UPDATE stock SET available = available - $3 WHERE drop_id = $1 AND tier = $2`,
		k.drop, k.tier, taken)
	if err != nil {
		return fmt.Errorf("take stock of %s/%s: %w", k.drop, k.tier, err)
	}
	return nil
}

// heldFor reports which of these orders already have a hold.
func heldFor(ctx context.Context, tx pgx.Tx, orderIDs []string) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `SELECT order_id::text FROM holds WHERE order_id = ANY($1::uuid[])`, orderIDs)
	if err != nil {
		return nil, fmt.Errorf("look up existing holds: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("look up existing holds: %w", err)
	}
	held := make(map[string]bool, len(ids))
	for _, id := range ids {
		held[id] = true
	}
	return held, nil
}

// insertHolds writes holds in one statement and reports which orders' rows
// went in. A row whose order already has a hold is skipped.
func insertHolds(ctx context.Context, tx pgx.Tx, holds []Hold) (map[string]bool, error) {
	var (
		ids, orders, drops, tiers []string
		quantities                []int
		expires                   []time.Time
	)
	for _, h := range holds {
		ids = append(ids, h.ID)
		orders = append(orders, h.OrderID)
		drops = append(drops, h.DropID)
		tiers = append(tiers, h.Tier)
		quantities = append(quantities, h.Quantity)
		expires = append(expires, h.ExpiresAt)
	}
	rows, err := tx.Query(ctx, `
		INSERT INTO holds (id, order_id, drop_id, tier, quantity, expires_at)
		SELECT * FROM unnest($1::uuid[], $2::uuid[], $3::text[], $4::text[], $5::int[], $6::timestamptz[])
		ON CONFLICT (order_id) DO NOTHING
		RETURNING order_id::text`,
		ids, orders, drops, tiers, quantities, expires)
	if err != nil {
		return nil, fmt.Errorf("record holds: %w", err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("record holds: %w", err)
	}
	inserted := make(map[string]bool, len(got))
	for _, id := range got {
		inserted[id] = true
	}
	return inserted, nil
}
