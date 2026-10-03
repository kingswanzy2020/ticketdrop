package stock

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/kingswanzy2020/ticketdrop/pkg/platform/httpx"
)

// The largest request body accepted.
const maxBody = 16 << 10

// The most expired holds released by one call. More are picked up by the next.
const expiryBatch = 500

// Routes registers the inventory API on mux.
func (s *Service) Routes(mux *http.ServeMux) {
	// Public: listed in the gateway.
	mux.HandleFunc("GET /v1/drops/{drop}/availability", s.availability)

	// Internal: called by other services. The gateway does not list these, so
	// they cannot be reached from outside.
	mux.HandleFunc("POST /internal/v1/holds", s.hold)
	mux.HandleFunc("POST /internal/v1/holds/expire", s.expire)
}

func (s *Service) availability(w http.ResponseWriter, r *http.Request) {
	drop := r.PathValue("drop")
	tiers, err := s.Availability(r.Context(), drop)
	if err != nil {
		s.Log.ErrorContext(r.Context(), "read availability", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "could not read availability")
		return
	}
	if len(tiers) == 0 {
		httpx.Error(w, r, http.StatusNotFound, "no such drop on sale")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"drop_id": drop, "tiers": tiers})
}

func (s *Service) hold(w http.ResponseWriter, r *http.Request) {
	var req HoldRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "request body must be a JSON hold request")
		return
	}
	if problem := req.Validate(); problem != "" {
		httpx.Error(w, r, http.StatusBadRequest, problem)
		return
	}

	h, created, err := s.Hold(r.Context(), req)
	switch {
	case errors.Is(err, ErrSoldOut):
		httpx.Error(w, r, http.StatusConflict, "sold out")
	case errors.Is(err, ErrNoTier):
		httpx.Error(w, r, http.StatusNotFound, "no such drop or tier")
	case err != nil:
		s.Log.ErrorContext(r.Context(), "hold tickets", "order_id", req.OrderID, "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "could not hold the tickets")
	case created:
		httpx.JSON(w, http.StatusCreated, h)
	default:
		httpx.JSON(w, http.StatusOK, h)
	}
}

// expire is called on a timer by the scheduler. Calling it more often, or
// from two places at once, does no harm.
func (s *Service) expire(w http.ResponseWriter, r *http.Request) {
	released, err := s.ReleaseExpired(r.Context(), expiryBatch)
	if err != nil {
		s.Log.ErrorContext(r.Context(), "release expired holds", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "could not release expired holds")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]int{"released": released})
}
