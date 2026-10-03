// Package notify is the notifications service: it tells each customer how
// their order ended.
package notify

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kingswanzy2020/ticketdrop/pkg/contracts"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/events"
)

// Message is one notification to one customer.
type Message struct {
	// ID is the ID of the event that caused the message, so the same event
	// always gives the same ID. A provider that accepts an idempotency key
	// can use it to drop a repeat.
	ID      string
	To      string
	Subject string
	Body    string
}

// Sender delivers a message.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// LogSender delivers a message by writing it to the log. It stands in for an
// email provider.
type LogSender struct {
	Log *slog.Logger
}

func (s LogSender) Send(ctx context.Context, m Message) error {
	s.Log.InfoContext(ctx, "notification sent",
		"message_id", m.ID, "to", m.To, "subject", m.Subject, "body", m.Body)
	return nil
}

// Metrics counts the notifications sent.
type Metrics struct {
	sent *prometheus.CounterVec
}

// NewMetrics registers the notification metrics on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		sent: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "notifications_sent_total",
			Help: "Notifications delivered, by kind (ticketed, failed).",
		}, []string{"kind"}),
	}
	reg.MustRegister(m.sent)
	return m
}

// Notifier turns the end of an order into a message to its customer.
type Notifier struct {
	Sender  Sender
	Metrics *Metrics
}

// HandleEvent sends the notification for one order.ticketed or order.failed
// event.
//
// Unlike every other consumer, this one does not record the events it has
// handled, and so keeps no database. A message delivered to the queue twice is
// therefore sent twice. That is accepted here because a repeated email is a
// nuisance, where a repeated charge or a repeated ticket would be a loss, and
// because a record could not prevent it anyway: sending an email cannot be
// part of a database transaction.
func (n *Notifier) HandleEvent(ctx context.Context, e events.Event) error {
	var kind string
	switch e.Type {
	case contracts.OrderTicketed:
		kind = "ticketed"
	case contracts.OrderFailed:
		kind = "failed"
	default:
		// The queue's filter policy admits only the types handled here.
		// Anything else means the subscription is misconfigured. Failing sends
		// the message to the dead-letter queue, where it is seen.
		return fmt.Errorf("unexpected event type %q", e.Type)
	}
	var r contracts.Result
	if err := e.Decode(&r); err != nil {
		return err
	}
	if r.CustomerEmail == "" {
		return fmt.Errorf("event %s: no customer_email to notify", e.ID)
	}

	// An error from the sender leaves the message on the queue, so it is
	// tried again, and dead-lettered if the provider keeps refusing it.
	if err := n.Sender.Send(ctx, compose(e.ID, kind, r)); err != nil {
		return fmt.Errorf("notify %s about order %s: %w", r.CustomerEmail, r.OrderID, err)
	}
	n.Metrics.sent.WithLabelValues(kind).Inc()
	return nil
}

// compose writes the message for an order that ended as kind.
func compose(id, kind string, r contracts.Result) Message {
	m := Message{ID: id, To: r.CustomerEmail}
	if kind == "ticketed" {
		m.Subject = fmt.Sprintf("Your tickets for %s are confirmed", r.DropID)
		m.Body = fmt.Sprintf("Order %s: %s for %s. See you there.",
			r.OrderID, tickets(r.Quantity, r.Tier), r.DropID)
		return m
	}

	m.Subject = fmt.Sprintf("Your order for %s did not go through", r.DropID)
	var why string
	switch r.Reason {
	case "card declined":
		why = "Your card was declined."
	case "hold expired":
		why = "We could not confirm your payment in time."
	default:
		why = "Something went wrong on our side."
	}
	m.Body = fmt.Sprintf("Order %s: %s You have not been charged, and the %s you asked for %s been released.",
		r.OrderID, why, tickets(r.Quantity, r.Tier), hasOrHave(r.Quantity))
	return m
}

// tickets writes a number of tickets in words a customer would use.
func tickets(n int, tier string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s ticket", tier)
	}
	return fmt.Sprintf("%d %s tickets", n, tier)
}

func hasOrHave(n int) string {
	if n == 1 {
		return "has"
	}
	return "have"
}
