package shop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// The most of an API response that is read.
const maxResponse = 1 << 20

var (
	// ErrNotFound means the API has no such drop or order, or that the drop
	// is not on sale.
	ErrNotFound = errors.New("not found")
	// ErrSoldOut means the API refused an order because too few tickets are left.
	ErrSoldOut = errors.New("sold out")
)

// Rejected is an order the API refused as invalid, with the reason it gave.
type Rejected struct {
	Message string
}

func (r *Rejected) Error() string { return "order rejected: " + r.Message }

// Drop is a drop as the API describes it.
type Drop struct {
	ID      string    `json:"drop_id"`
	Name    string    `json:"name"`
	Venue   string    `json:"venue"`
	OpensAt time.Time `json:"opens_at"`
	Status  string    `json:"status"`
	Tiers   []Tier    `json:"tiers"`
}

// Tier is one kind of ticket a drop sells.
type Tier struct {
	Tier       string `json:"tier"`
	PriceCents int    `json:"price_cents"`
	Capacity   int    `json:"capacity"`
}

// Stock is how many tickets of one tier are left.
type Stock struct {
	Tier      string `json:"tier"`
	Available int    `json:"available"`
}

// OrderRequest is what the API needs to place an order.
type OrderRequest struct {
	DropID        string `json:"drop_id"`
	Tier          string `json:"tier"`
	Quantity      int    `json:"quantity"`
	CustomerEmail string `json:"customer_email"`
}

// Order is an order as the API describes it.
type Order struct {
	ID            string `json:"order_id"`
	DropID        string `json:"drop_id"`
	Tier          string `json:"tier"`
	Quantity      int    `json:"quantity"`
	CustomerEmail string `json:"customer_email"`
	Status        string `json:"status"`
	FailureReason string `json:"failure_reason"`
}

// API is the client for TicketDrop's public API. The shopfront uses the same
// API, through the same gateway, as any other client would: it has no private
// way in, so it can do nothing the API does not allow.
type API struct {
	// BaseURL is the gateway's address, such as "http://gateway:8080".
	BaseURL string
	// Client must have a timeout: these calls are made while a customer waits.
	Client *http.Client
}

// Drops lists the drops.
func (a *API) Drops(ctx context.Context) ([]Drop, error) {
	var body struct {
		Drops []Drop `json:"drops"`
	}
	if err := a.get(ctx, "/v1/drops", &body); err != nil {
		return nil, err
	}
	return body.Drops, nil
}

// Drop returns one drop with its tiers, or ErrNotFound.
func (a *API) Drop(ctx context.Context, id string) (Drop, error) {
	var d Drop
	err := a.get(ctx, "/v1/drops/"+url.PathEscape(id), &d)
	return d, err
}

// Availability returns the tickets left in each tier of a drop, or
// ErrNotFound while the drop is not on sale.
func (a *API) Availability(ctx context.Context, id string) ([]Stock, error) {
	var body struct {
		Tiers []Stock `json:"tiers"`
	}
	if err := a.get(ctx, "/v1/drops/"+url.PathEscape(id)+"/availability", &body); err != nil {
		return nil, err
	}
	return body.Tiers, nil
}

// Order returns one order, or ErrNotFound.
func (a *API) Order(ctx context.Context, id string) (Order, error) {
	var o Order
	err := a.get(ctx, "/v1/orders/"+url.PathEscape(id), &o)
	return o, err
}

// PlaceOrder places an order. It returns ErrSoldOut, ErrNotFound when the drop
// is not on sale, or a *Rejected when the API finds the order invalid. Any
// other error means the outcome is unknown: the order may have been placed.
func (a *API) PlaceOrder(ctx context.Context, req OrderRequest) (Order, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return Order{}, fmt.Errorf("encode order: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/v1/orders", bytes.NewReader(payload))
	if err != nil {
		return Order{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.Client.Do(httpReq)
	if err != nil {
		return Order{}, fmt.Errorf("place order: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return Order{}, fmt.Errorf("place order: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusCreated:
		var o Order
		if err := json.Unmarshal(body, &o); err != nil {
			return Order{}, fmt.Errorf("place order: read the answer: %w", err)
		}
		return o, nil
	case http.StatusConflict:
		return Order{}, ErrSoldOut
	case http.StatusNotFound:
		return Order{}, ErrNotFound
	case http.StatusBadRequest:
		var problem struct {
			Error string `json:"error"`
		}
		json.Unmarshal(body, &problem)
		return Order{}, &Rejected{Message: problem.Error}
	default:
		return Order{}, fmt.Errorf("place order: status %d", resp.StatusCode)
	}
}

// get reads a JSON document from the API into v.
func (a *API) get(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.BaseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := a.Client.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		if err := json.Unmarshal(body, v); err != nil {
			return fmt.Errorf("GET %s: read the answer: %w", path, err)
		}
		return nil
	case http.StatusNotFound:
		return ErrNotFound
	default:
		return fmt.Errorf("GET %s: status %d", path, resp.StatusCode)
	}
}
