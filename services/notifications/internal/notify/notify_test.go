package notify

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/kingswanzy2020/ticketdrop/pkg/contracts"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/events"
)

// outbox is a Sender that keeps what it is given, or refuses it.
type outbox struct {
	sent []Message
	err  error
}

func (o *outbox) Send(_ context.Context, m Message) error {
	if o.err != nil {
		return o.err
	}
	o.sent = append(o.sent, m)
	return nil
}

func event(t *testing.T, typ string, data any) events.Event {
	t.Helper()
	e, err := events.New(context.Background(), "orders", typ, data)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func order(quantity int) contracts.Order {
	return contracts.Order{
		OrderID: "0b5c1c0e", DropID: "summer-fest", Tier: "general",
		Quantity: quantity, CustomerEmail: "ama@example.com",
	}
}

func TestHandleEventSendsOneMessagePerOrder(t *testing.T) {
	tests := []struct {
		name     string
		typ      string
		result   contracts.Result
		kind     string
		subject  string
		bodyHas  []string
		bodyLack string
	}{
		{
			name: "ticketed", typ: contracts.OrderTicketed, result: contracts.Result{Order: order(2)},
			kind: "ticketed", subject: "Your tickets for summer-fest are confirmed",
			bodyHas: []string{"0b5c1c0e", "2 general tickets"},
		},
		{
			name: "one ticket reads as singular", typ: contracts.OrderTicketed, result: contracts.Result{Order: order(1)},
			kind: "ticketed", subject: "Your tickets for summer-fest are confirmed",
			bodyHas: []string{"1 general ticket for"}, bodyLack: "tickets for",
		},
		{
			name: "declined", typ: contracts.OrderFailed, result: contracts.Result{Order: order(2), Reason: "card declined"},
			kind: "failed", subject: "Your order for summer-fest did not go through",
			bodyHas: []string{"card was declined", "not been charged", "2 general tickets you asked for have been released"},
		},
		{
			name: "expired", typ: contracts.OrderFailed, result: contracts.Result{Order: order(1), Reason: "hold expired"},
			kind: "failed", subject: "Your order for summer-fest did not go through",
			bodyHas: []string{"could not confirm your payment in time", "1 general ticket you asked for has been released"},
		},
		{
			name: "a reason this service has not heard of", typ: contracts.OrderFailed, result: contracts.Result{Order: order(1), Reason: "meteor"},
			kind: "failed", subject: "Your order for summer-fest did not go through",
			bodyHas: []string{"Something went wrong on our side", "not been charged"}, bodyLack: "meteor",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := &outbox{}
			n := &Notifier{Sender: out, Metrics: NewMetrics(prometheus.NewRegistry())}
			e := event(t, tt.typ, tt.result)

			if err := n.HandleEvent(context.Background(), e); err != nil {
				t.Fatalf("HandleEvent() = %v", err)
			}

			if len(out.sent) != 1 {
				t.Fatalf("sent %d messages, want 1", len(out.sent))
			}
			m := out.sent[0]
			if m.To != "ama@example.com" || m.ID != e.ID || m.Subject != tt.subject {
				t.Errorf("message = %+v", m)
			}
			for _, want := range tt.bodyHas {
				if !strings.Contains(m.Body, want) {
					t.Errorf("body %q does not contain %q", m.Body, want)
				}
			}
			if tt.bodyLack != "" && strings.Contains(m.Body, tt.bodyLack) {
				t.Errorf("body %q should not contain %q", m.Body, tt.bodyLack)
			}
			if got := testutil.ToFloat64(n.Metrics.sent.WithLabelValues(tt.kind)); got != 1 {
				t.Errorf("sent{kind=%s} = %v, want 1", tt.kind, got)
			}
		})
	}
}

func TestHandleEventFailsWhatItCannotSend(t *testing.T) {
	down := errors.New("provider down")

	tests := []struct {
		name   string
		e      func(t *testing.T) events.Event
		sender *outbox
	}{
		{"an event type it does not handle", func(t *testing.T) events.Event {
			return event(t, contracts.OrderCreated, order(1))
		}, &outbox{}},
		{"no email address", func(t *testing.T) events.Event {
			o := order(1)
			o.CustomerEmail = ""
			return event(t, contracts.OrderTicketed, contracts.Result{Order: o})
		}, &outbox{}},
		// The error keeps the message on the queue, so it is tried again.
		{"the provider refuses", func(t *testing.T) events.Event {
			return event(t, contracts.OrderTicketed, contracts.Result{Order: order(1)})
		}, &outbox{err: down}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := &Notifier{Sender: tt.sender, Metrics: NewMetrics(prometheus.NewRegistry())}

			if err := n.HandleEvent(context.Background(), tt.e(t)); err == nil {
				t.Fatal("HandleEvent() = nil, want an error")
			}
			if len(tt.sender.sent) != 0 {
				t.Errorf("sent %d messages, want 0", len(tt.sender.sent))
			}
			for _, kind := range []string{"ticketed", "failed"} {
				if got := testutil.ToFloat64(n.Metrics.sent.WithLabelValues(kind)); got != 0 {
					t.Errorf("sent{kind=%s} = %v, want 0", kind, got)
				}
			}
		})
	}
}
