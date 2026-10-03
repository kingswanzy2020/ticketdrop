package order

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

var (
	// ErrSoldOut means inventory has fewer tickets left than the order asks for.
	ErrSoldOut = errors.New("sold out")
	// ErrNoTier means the order names a drop that is not on sale, or a tier it
	// does not have.
	ErrNoTier = errors.New("no such drop or tier")
)

// Inventory is the client for the inventory service's hold API.
type Inventory struct {
	// BaseURL is the inventory service's address, such as "http://inventory:8080".
	BaseURL string
	// Client must have a timeout: this call is made while a customer waits.
	Client *http.Client
}

// Hold asks inventory to set the order's tickets aside, and returns when the
// hold expires. It returns ErrSoldOut or ErrNoTier when inventory refuses,
// and any other error when inventory could not be asked.
func (i *Inventory) Hold(ctx context.Context, o Order) (expiresAt time.Time, err error) {
	body, err := json.Marshal(map[string]any{
		"order_id": o.ID,
		"drop_id":  o.DropID,
		"tier":     o.Tier,
		"quantity": o.Quantity,
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("encode hold request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.BaseURL+"/internal/v1/holds", bytes.NewReader(body))
	if err != nil {
		return time.Time{}, fmt.Errorf("build hold request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := i.Client.Do(req)
	if err != nil {
		return time.Time{}, fmt.Errorf("ask inventory for a hold: %w", err)
	}
	defer resp.Body.Close()
	// Reading the body to the end lets the connection be reused.
	defer io.Copy(io.Discard, resp.Body)

	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK:
		var hold struct {
			ExpiresAt time.Time `json:"expires_at"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&hold); err != nil {
			return time.Time{}, fmt.Errorf("read inventory's hold: %w", err)
		}
		return hold.ExpiresAt, nil
	case http.StatusConflict:
		return time.Time{}, ErrSoldOut
	case http.StatusNotFound:
		return time.Time{}, ErrNoTier
	default:
		return time.Time{}, fmt.Errorf("ask inventory for a hold: status %d", resp.StatusCode)
	}
}
