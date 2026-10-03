package httpx

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/kingswanzy2020/ticketdrop/pkg/platform/correlation"
)

func wrap(h http.HandlerFunc) (http.Handler, *Metrics) {
	m := NewMetrics(prometheus.NewRegistry())
	return Wrap(h, slog.New(slog.NewTextHandler(io.Discard, nil)), m), m
}

func TestWrapCorrelationID(t *testing.T) {
	var seen string
	h, _ := wrap(func(_ http.ResponseWriter, r *http.Request) {
		seen = correlation.From(r.Context())
	})

	tests := []struct {
		name, sent string
		kept       bool
	}{
		{"valid ID is kept", "req-123", true},
		{"missing ID is generated", "", false},
		{"unsafe ID is replaced", "bad id {with} spaces", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.sent != "" {
				req.Header.Set(correlation.Header, tt.sent)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if !correlation.Valid(seen) {
				t.Fatalf("handler saw ID %q, want a valid one", seen)
			}
			if got := rec.Header().Get(correlation.Header); got != seen {
				t.Errorf("response header = %q, handler saw %q", got, seen)
			}
			if kept := seen == tt.sent; kept != tt.kept {
				t.Errorf("sent %q, handler saw %q, want kept=%v", tt.sent, seen, tt.kept)
			}
		})
	}
}

func TestWrapRecoversPanic(t *testing.T) {
	h, m := wrap(func(http.ResponseWriter, *http.Request) { panic("boom") })

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	if body["error"] == "" || !correlation.Valid(body["correlation_id"]) {
		t.Errorf("body = %v, want an error and a correlation_id", body)
	}
	if got := testutil.ToFloat64(m.requests.WithLabelValues("unmatched", "500")); got != 1 {
		t.Errorf("requests{status=500} = %v, want 1", got)
	}
}

func TestWrapLabelsByRoutePattern(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/orders/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	m := NewMetrics(prometheus.NewRegistry())
	h := Wrap(mux, slog.New(slog.NewTextHandler(io.Discard, nil)), m)

	for _, id := range []string{"a", "b", "c"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/orders/"+id, nil))
	}

	// Three different paths, one label value: the pattern, not the path.
	if got := testutil.ToFloat64(m.requests.WithLabelValues("GET /v1/orders/{id}", "404")); got != 3 {
		t.Errorf("requests for the route pattern = %v, want 3", got)
	}
}
