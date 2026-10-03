package order

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kingswanzy2020/ticketdrop/pkg/platform/correlation"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/httpx"
)

func TestInventoryHold(t *testing.T) {
	o := Order{ID: "0b5c1c0e-6f0a-4f7e-9d3a-2a4f1f5f8a11", DropID: "summer-fest", Tier: "general", Quantity: 2}
	expiry := time.Date(2027, 6, 1, 10, 10, 0, 0, time.UTC)

	tests := []struct {
		name   string
		status int
		want   error // nil, a sentinel, or errOther for "some other error"
	}{
		{"new hold", http.StatusCreated, nil},
		{"hold already existed", http.StatusOK, nil},
		{"sold out", http.StatusConflict, ErrSoldOut},
		{"unknown tier", http.StatusNotFound, ErrNoTier},
		{"inventory failing", http.StatusInternalServerError, errOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sent map[string]any
			var sentID string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sentID = r.Header.Get(correlation.Header)
				json.NewDecoder(r.Body).Decode(&sent)
				w.WriteHeader(tt.status)
				if tt.status < 300 {
					io.WriteString(w, `{"hold_id":"h1","expires_at":"2027-06-01T10:10:00Z"}`)
				} else {
					io.WriteString(w, `{"error":"no"}`)
				}
			}))
			defer srv.Close()
			inv := &Inventory{BaseURL: srv.URL, Client: httpx.NewClient(time.Second)}

			got, err := inv.Hold(correlation.With(context.Background(), "req-123"), o)

			switch {
			case tt.want == nil && err != nil:
				t.Errorf("Hold() = %v, want nil", err)
			case tt.want == nil && !got.Equal(expiry):
				t.Errorf("Hold() expiry = %v, want %v", got, expiry)
			case tt.want == errOther && (err == nil || errors.Is(err, ErrSoldOut) || errors.Is(err, ErrNoTier)):
				t.Errorf("Hold() = %v, want an error that is neither sold out nor unknown tier", err)
			case tt.want != nil && tt.want != errOther && !errors.Is(err, tt.want):
				t.Errorf("Hold() = %v, want %v", err, tt.want)
			}
			if sent["order_id"] != o.ID || sent["quantity"] != float64(o.Quantity) {
				t.Errorf("inventory was sent %v", sent)
			}
			if sentID != "req-123" {
				t.Errorf("inventory saw correlation ID %q, want req-123", sentID)
			}
		})
	}
}

func TestInventoryHoldWhenInventoryIsSlow(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)
	inv := &Inventory{BaseURL: srv.URL, Client: httpx.NewClient(50 * time.Millisecond)}

	start := time.Now()
	_, err := inv.Hold(context.Background(), Order{ID: "x"})

	if err == nil || errors.Is(err, ErrSoldOut) || errors.Is(err, ErrNoTier) {
		t.Errorf("Hold() = %v, want a timeout error", err)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Errorf("Hold() took %v, want it to give up at the 50ms timeout", waited)
	}
}

var errOther = errors.New("other")
