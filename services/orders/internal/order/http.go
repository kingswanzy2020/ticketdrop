package order

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/kingswanzy2020/ticketdrop/pkg/platform/httpx"
)

// The largest request body accepted. An order is a few hundred bytes.
const maxBody = 16 << 10

// Routes registers the orders API on mux.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/orders", s.create)
	mux.HandleFunc("GET /v1/orders/{id}", s.get)
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	var req Request
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "request body must be a JSON order")
		return
	}
	if problem := req.Validate(); problem != "" {
		httpx.Error(w, r, http.StatusBadRequest, problem)
		return
	}

	o, err := s.Create(r.Context(), req)
	switch {
	case errors.Is(err, ErrSoldOut):
		httpx.Error(w, r, http.StatusConflict, "sold out")
		return
	case errors.Is(err, ErrNoTier):
		httpx.Error(w, r, http.StatusNotFound, "no such drop or tier")
		return
	case err != nil:
		// 503, not 500: the usual cause is a dependency that is down or slow,
		// and the same request may succeed in a moment.
		s.Log.ErrorContext(r.Context(), "create order", "error", err)
		w.Header().Set("Retry-After", "1")
		httpx.Error(w, r, http.StatusServiceUnavailable, "could not place the order; try again")
		return
	}
	s.Log.InfoContext(r.Context(), "order created",
		"order_id", o.ID, "drop_id", o.DropID, "tier", o.Tier, "quantity", o.Quantity)
	httpx.JSON(w, http.StatusCreated, o)
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	o, err := s.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, ErrNotFound) {
		httpx.Error(w, r, http.StatusNotFound, "order not found")
		return
	}
	if err != nil {
		s.Log.ErrorContext(r.Context(), "get order", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "could not read the order")
		return
	}
	httpx.JSON(w, http.StatusOK, o)
}
