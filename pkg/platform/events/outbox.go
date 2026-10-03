package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Publisher sends an event to the bus.
type Publisher interface {
	Publish(ctx context.Context, e Event) error
}

// SNSPublisher publishes to one SNS topic. Each consumer has its own SQS queue
// subscribed to that topic, with a filter policy on the event_type attribute
// choosing which events it receives.
type SNSPublisher struct {
	Client   *sns.Client
	TopicARN string
	Metrics  *Metrics
}

func (p *SNSPublisher) Publish(ctx context.Context, e Event) error {
	body, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode event %s: %w", e.ID, err)
	}
	_, err = p.Client.Publish(ctx, &sns.PublishInput{
		TopicArn: aws.String(p.TopicARN),
		Message:  aws.String(string(body)),
		MessageAttributes: map[string]snstypes.MessageAttributeValue{
			"event_type": {DataType: aws.String("String"), StringValue: aws.String(e.Type)},
		},
	})
	if err != nil {
		p.Metrics.published.WithLabelValues(e.Type, "error").Inc()
		return fmt.Errorf("publish %s %s: %w", e.Type, e.ID, err)
	}
	p.Metrics.published.WithLabelValues(e.Type, "ok").Inc()
	return nil
}

// Enqueue writes e to the outbox inside the caller's transaction.
//
// This is the point of the outbox. A service that changes its database and
// then publishes has two writes that can disagree: a crash between them leaves
// an order nobody was told about. Writing the event in the same transaction as
// the change means both commit or neither does; the relay publishes later.
func Enqueue(ctx context.Context, tx pgx.Tx, e Event) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode event %s: %w", e.ID, err)
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO outbox (event_id, event_type, payload) VALUES ($1, $2, $3)`,
		e.ID, e.Type, payload)
	if err != nil {
		return fmt.Errorf("write %s to outbox: %w", e.Type, err)
	}
	return nil
}

// MarkProcessed records that eventID has been handled, inside the caller's
// transaction. It returns false when the event was already recorded, which
// means this delivery is a duplicate and the handler should do nothing.
func MarkProcessed(ctx context.Context, tx pgx.Tx, eventID string) (bool, error) {
	tag, err := tx.Exec(ctx,
		`INSERT INTO processed_events (event_id) VALUES ($1) ON CONFLICT DO NOTHING`, eventID)
	if err != nil {
		return false, fmt.Errorf("record event %s: %w", eventID, err)
	}
	return tag.RowsAffected() == 1, nil
}

// ErrDuplicate is what Once returns for an event that was handled before. A
// handler passes it on, and the consumer acknowledges the message and counts
// it as a duplicate.
var ErrDuplicate = errors.New("event already processed")

// Once runs fn in a transaction, unless e has been handled before, in which
// case it returns ErrDuplicate without calling fn.
//
// The record that e was handled commits together with whatever fn writes. A
// crash at any point therefore leaves both or neither, and a redelivery either
// repeats the whole of the work or none of it.
func Once(ctx context.Context, pool *pgxpool.Pool, e Event, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		fresh, err := MarkProcessed(ctx, tx, e.ID)
		if err != nil {
			return err
		}
		if !fresh {
			return ErrDuplicate
		}
		return fn(tx)
	})
}

// Relay publishes outbox rows and marks them published.
//
// Delivery is at least once: if the process dies after publishing and before
// the row is marked, the event goes out again on the next pass. Consumers
// handle that with MarkProcessed.
type Relay struct {
	Pool      *pgxpool.Pool
	Publisher Publisher
	Log       *slog.Logger
	Metrics   *Metrics
	// Interval is how long to wait when the outbox is empty.
	Interval time.Duration
	// Batch is the most rows published in one pass.
	Batch int
}

// Run publishes until ctx is cancelled, then makes a final pass.
func (r *Relay) Run(ctx context.Context) error {
	defer r.drain(ctx)

	lastSample := time.Time{}
	for {
		n, err := r.flush(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			r.Log.Error("outbox relay", "error", err)
		}

		if time.Since(lastSample) > 5*time.Second {
			r.housekeep(ctx)
			lastSample = time.Now()
		}

		// A full batch means more is waiting; go straight round again.
		if err == nil && n == r.Batch {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(r.Interval):
		}
	}
}

// drain publishes what is still in the outbox when the relay is told to stop:
// the events written by requests and handlers that finished during shutdown.
// Without it they would wait for another replica or for the next start.
//
// It is bounded. Whatever it cannot send in time stays in the outbox, which is
// safe, only late.
func (r *Relay) drain(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for {
		n, err := r.flush(ctx)
		if err != nil {
			r.Log.Warn("outbox drain", "error", err)
			return
		}
		if n < r.Batch {
			return
		}
	}
}

type outboxRow struct {
	id      int64
	payload []byte
}

func (r *Relay) flush(ctx context.Context) (int, error) {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	// SKIP LOCKED lets several replicas relay at once: each takes rows the
	// others have not locked, so none of them waits and none publishes the
	// same row.
	rows, err := tx.Query(ctx, `
		SELECT id, payload FROM outbox
		WHERE published_at IS NULL
		ORDER BY id
		LIMIT $1
		FOR UPDATE SKIP LOCKED`, r.Batch)
	if err != nil {
		return 0, fmt.Errorf("read outbox: %w", err)
	}
	batch, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (outboxRow, error) {
		var o outboxRow
		err := row.Scan(&o.id, &o.payload)
		return o, err
	})
	if err != nil {
		return 0, fmt.Errorf("read outbox: %w", err)
	}
	if len(batch) == 0 {
		return 0, nil
	}

	// Publish the batch a few at a time rather than one by one: during a drop
	// the outbox fills faster than sequential publishing can empty it.
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		sent   []int64
		pubErr error
		slots  = make(chan struct{}, 8)
	)
	for _, row := range batch {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-slots }()

			var e Event
			err := json.Unmarshal(row.payload, &e)
			if err == nil {
				err = r.Publisher.Publish(ctx, e)
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				pubErr = err
				return
			}
			sent = append(sent, row.id)
		}()
	}
	wg.Wait()

	if len(sent) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE outbox SET published_at = now() WHERE id = ANY($1)`, sent); err != nil {
			return 0, fmt.Errorf("mark published: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return 0, fmt.Errorf("commit: %w", err)
		}
	}
	return len(sent), pubErr
}

// housekeep refreshes the backlog gauges and deletes rows published over an
// hour ago.
func (r *Relay) housekeep(ctx context.Context) {
	var pending int64
	var oldest float64
	err := r.Pool.QueryRow(ctx, `
		SELECT count(*), coalesce(extract(epoch FROM now() - min(created_at)), 0)::float8
		FROM outbox WHERE published_at IS NULL`).Scan(&pending, &oldest)
	if err != nil {
		if ctx.Err() == nil {
			r.Log.Warn("outbox backlog query", "error", err)
		}
		return
	}
	r.Metrics.outboxPending.Set(float64(pending))
	r.Metrics.outboxOldest.Set(oldest)

	if _, err := r.Pool.Exec(ctx, `DELETE FROM outbox WHERE published_at < now() - interval '1 hour'`); err != nil && ctx.Err() == nil {
		r.Log.Warn("outbox prune", "error", err)
	}
}
