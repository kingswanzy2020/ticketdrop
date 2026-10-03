package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/kingswanzy2020/ticketdrop/pkg/platform/correlation"
)

// Handler processes one event. Returning nil acknowledges it. Returning an
// error leaves the message on the queue to be delivered again.
//
// A handler must be idempotent: SQS delivers at least once, so the same event
// can arrive twice even when nothing has gone wrong. Doing the work inside
// Once takes care of that; the ErrDuplicate it returns also acknowledges.
type Handler func(ctx context.Context, e Event) error

// Consumer reads one SQS queue and runs Handler for each message.
type Consumer struct {
	Client   *sqs.Client
	QueueURL string
	Handler  Handler
	Log      *slog.Logger
	Metrics  *Metrics
	// Concurrency is the most messages handled at the same time.
	Concurrency int
	// WaitTime is how long one receive call waits for messages (long polling;
	// SQS allows up to 20s). Longer waits mean fewer empty, billed requests.
	WaitTime time.Duration
	// Timeout bounds one handler call. Keep it below the queue's visibility
	// timeout, or SQS redelivers a message that is still being worked on.
	Timeout time.Duration
}

// The most messages SQS returns from one receive call.
const maxReceive = 10

// Run consumes until ctx is cancelled, then waits for the handlers already
// running to finish before it returns.
func (c *Consumer) Run(ctx context.Context) error {
	sem := make(chan struct{}, c.Concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()

	backoff := time.Second
	for {
		// Reserve handler slots before asking for messages, and ask only for
		// as many as were reserved. A message received with no free handler
		// would sit invisible on the queue with its visibility timeout running.
		n := reserve(ctx, sem, min(maxReceive, c.Concurrency))
		if n == 0 {
			return nil
		}

		out, err := c.Client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:              aws.String(c.QueueURL),
			MaxNumberOfMessages:   int32(n),
			WaitTimeSeconds:       int32(c.WaitTime.Seconds()),
			MessageAttributeNames: []string{"All"},
		})
		if err != nil {
			release(sem, n)
			if ctx.Err() != nil {
				return nil
			}
			// Queue trouble is usually temporary (network, throttling, the
			// emulator still starting). Retry instead of exiting the service.
			c.Log.Error("receive failed", "queue", c.QueueURL, "error", err, "retry_in", backoff.String())
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second

		release(sem, n-len(out.Messages))
		for _, m := range out.Messages {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer release(sem, 1)
				c.handle(ctx, m)
			}()
		}
	}
}

func (c *Consumer) handle(ctx context.Context, m sqstypes.Message) {
	// Shutdown cancels ctx to stop the receive loop. A handler that has
	// already started keeps going: finishing now is cheaper than having the
	// message redelivered and handled from the start by another pod.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.Timeout)
	defer cancel()

	var e Event
	if err := json.Unmarshal([]byte(aws.ToString(m.Body)), &e); err != nil || e.ID == "" || e.Type == "" {
		// A poison message: no retry will ever parse it. It is left on the
		// queue, and after the queue's maxReceiveCount SQS moves it to the
		// dead-letter queue, where it waits for a person instead of blocking
		// the consumer forever.
		c.Log.Error("unreadable message", "message_id", aws.ToString(m.MessageId), "error", err)
		c.Metrics.consumed.WithLabelValues("unknown", "invalid").Inc()
		return
	}
	ctx = correlation.With(ctx, e.CorrelationID)

	start := time.Now()
	err := c.safely(ctx, e)
	c.Metrics.duration.WithLabelValues(e.Type).Observe(time.Since(start).Seconds())
	result := "ok"
	if errors.Is(err, ErrDuplicate) {
		// Already handled on an earlier delivery: there is nothing to redo,
		// so acknowledge it like any other success.
		result, err = "duplicate", nil
	}
	if err != nil {
		c.Log.ErrorContext(ctx, "handler failed; message will be redelivered",
			"event_id", e.ID, "event_type", e.Type, "error", err)
		c.Metrics.consumed.WithLabelValues(e.Type, "error").Inc()
		return
	}

	// Deleting is the acknowledgement. If it fails, the message comes back
	// and the handler's idempotency makes the second run harmless.
	_, err = c.Client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(c.QueueURL),
		ReceiptHandle: m.ReceiptHandle,
	})
	if err != nil {
		c.Log.WarnContext(ctx, "delete failed; message will be redelivered", "event_id", e.ID, "error", err)
	}
	c.Metrics.consumed.WithLabelValues(e.Type, result).Inc()
	c.Log.DebugContext(ctx, "handled", "event_id", e.ID, "event_type", e.Type,
		"result", result, "duration_ms", time.Since(start).Milliseconds())
}

// safely runs the handler and turns a panic into an error, so one bad message
// cannot take the whole consumer down.
func (c *Consumer) safely(ctx context.Context, e Event) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v\n%s", p, debug.Stack())
		}
	}()
	return c.Handler(ctx, e)
}

// reserve blocks until one slot is free, then takes up to max-1 more without
// waiting. It returns the number taken, or 0 when ctx is cancelled first.
func reserve(ctx context.Context, sem chan struct{}, max int) int {
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return 0
	}
	n := 1
	for n < max {
		select {
		case sem <- struct{}{}:
			n++
		default:
			return n
		}
	}
	return n
}

func release(sem chan struct{}, n int) {
	for range n {
		<-sem
	}
}
