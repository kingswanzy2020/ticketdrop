// Package contracts defines the events services exchange. A change here is a
// change to the agreement between a publisher and every consumer of that
// event, so fields are added, never renamed or removed.
package contracts

import "time"

// Event types. These strings are also what the SNS filter policies match on.
//
// A drop goes on sale with the first one. An order then passes through the
// rest, each published by the service named beside it:
//
//	drop.opened        catalog      a drop went on sale; its tickets can be held
//	order.created      orders       the order was accepted and its tickets are held
//	payment.succeeded  payments     the customer was charged
//	payment.failed     payments     the order was not charged; it ends here
//	order.confirmed    inventory    the held tickets now belong to the order
//	hold.expired       inventory    no payment outcome came in time; it ends here
//	ticket.issued      fulfillment  the tickets exist
//	order.ticketed     orders       the order ended well; the customer can be told
//	order.failed       orders       the order ended badly; the customer can be told
const (
	DropOpened       = "drop.opened"
	OrderCreated     = "order.created"
	PaymentSucceeded = "payment.succeeded"
	PaymentFailed    = "payment.failed"
	OrderConfirmed   = "order.confirmed"
	HoldExpired      = "hold.expired"
	TicketIssued     = "ticket.issued"
	OrderTicketed    = "order.ticketed"
	OrderFailed      = "order.failed"
)

// Drop is the data of drop.opened: the drop that went on sale, and how many
// tickets each of its tiers has.
type Drop struct {
	DropID string `json:"drop_id"`
	Tiers  []Tier `json:"tiers"`
}

// Tier is one tier of a drop.
type Tier struct {
	Tier     string `json:"tier"`
	Capacity int    `json:"capacity"`
}

// Order is the data of every order lifecycle event.
type Order struct {
	OrderID       string `json:"order_id"`
	DropID        string `json:"drop_id"`
	Tier          string `json:"tier"`
	Quantity      int    `json:"quantity"`
	CustomerEmail string `json:"customer_email"`
	// HoldExpiresAt is when the order's tickets stop being set aside for it.
	// The order must not be charged after this time.
	HoldExpiresAt time.Time `json:"hold_expires_at,omitzero"`
}

// Payment is the data of payment.succeeded and payment.failed: the order
// concerned and, for a failure, why. A consumer that needs only the order can
// decode it as an Order.
type Payment struct {
	Order
	Reason string `json:"reason,omitempty"`
}

// Hold is the data of hold.expired: the tickets that had been set aside for an
// order and have gone back into stock.
type Hold struct {
	OrderID  string `json:"order_id"`
	DropID   string `json:"drop_id"`
	Tier     string `json:"tier"`
	Quantity int    `json:"quantity"`
}

// Result is the data of order.ticketed and order.failed: the order as it
// ended and, for a failure, why. It is the one event that says how an order
// turned out, published by the service that owns the order.
type Result struct {
	Order
	Reason string `json:"reason,omitempty"`
}

// Tickets is the data of ticket.issued.
type Tickets struct {
	OrderID string `json:"order_id"`
	Count   int    `json:"count"`
}
