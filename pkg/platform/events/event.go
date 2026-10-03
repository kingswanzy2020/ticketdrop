// Package events is how services talk to each other asynchronously: an event
// envelope, a transactional outbox to publish from, and a queue consumer.
package events

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/kingswanzy2020/ticketdrop/pkg/platform/correlation"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/db"
)

// Event is the envelope every message on the bus uses.
type Event struct {
	// ID identifies this event. Consumers use it to recognise a redelivery.
	ID string `json:"id"`
	// Type says what happened, such as "order.created". Queues filter on it.
	Type string `json:"type"`
	// Source is the service that published the event.
	Source        string          `json:"source"`
	OccurredAt    time.Time       `json:"occurred_at"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	Data          json.RawMessage `json:"data"`
}

// New builds an event carrying data, with the correlation ID from ctx.
func New(ctx context.Context, source, typ string, data any) (Event, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return Event{}, fmt.Errorf("encode %s data: %w", typ, err)
	}
	return Event{
		ID:            uuid.NewString(),
		Type:          typ,
		Source:        source,
		OccurredAt:    time.Now().UTC(),
		CorrelationID: correlation.From(ctx),
		Data:          raw,
	}, nil
}

// Decode unmarshals the event's data into v.
func (e Event) Decode(v any) error {
	if err := json.Unmarshal(e.Data, v); err != nil {
		return fmt.Errorf("decode %s data: %w", e.Type, err)
	}
	return nil
}

//go:embed schema/*.sql
var schemaFS embed.FS

// Schema is the migration source for the outbox and processed_events tables.
// Every service that publishes or consumes events applies it to its database.
func Schema() db.Source {
	sub, err := fs.Sub(schemaFS, "schema")
	if err != nil {
		panic(err)
	}
	return db.Source{Name: "events", FS: sub}
}

// Metrics covers both directions of the bus.
type Metrics struct {
	published     *prometheus.CounterVec
	consumed      *prometheus.CounterVec
	duration      *prometheus.HistogramVec
	outboxPending prometheus.Gauge
	outboxOldest  prometheus.Gauge
}

// NewMetrics registers the event metrics on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		published: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "events_published_total",
			Help: "Events sent to the topic, by type and result.",
		}, []string{"event_type", "result"}),
		consumed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "events_consumed_total",
			Help: "Messages taken from the queue, by event type and result (ok, duplicate, error, invalid).",
		}, []string{"event_type", "result"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "events_handle_duration_seconds",
			Help:    "Time taken to handle one message, by event type.",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
		}, []string{"event_type"}),
		outboxPending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "outbox_pending_events",
			Help: "Events written to the outbox and not yet published.",
		}),
		outboxOldest: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "outbox_oldest_pending_seconds",
			Help: "Age of the oldest unpublished outbox event. Growth means the relay is stuck.",
		}),
	}
	reg.MustRegister(m.published, m.consumed, m.duration, m.outboxPending, m.outboxOldest)
	return m
}
