package catalog

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/kingswanzy2020/ticketdrop/pkg/platform/httpx"
)

// The largest request body accepted.
const maxBody = 64 << 10

// Routes registers the catalog API on mux.
func (s *Service) Routes(mux *http.ServeMux) {
	// Public: listed in the gateway.
	mux.HandleFunc("GET /v1/drops", s.list)
	mux.HandleFunc("GET /v1/drops/{drop}", s.get)

	// Internal: the gateway does not list these, so they cannot be reached
	// from outside. Saving a drop is for whoever runs the box office; opening
	// due drops is called on a timer by the scheduler.
	mux.HandleFunc("PUT /internal/v1/drops/{drop}", s.save)
	mux.HandleFunc("POST /internal/v1/drops/open-due", s.openDue)
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	drops, err := s.List(r.Context())
	if err != nil {
		s.Log.ErrorContext(r.Context(), "list drops", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "could not list the drops")
		return
	}
	if drops == nil {
		drops = []Drop{} // an empty list, not null, for whoever reads the JSON
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"drops": drops})
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	d, err := s.Get(r.Context(), r.PathValue("drop"))
	if errors.Is(err, ErrNotFound) {
		httpx.Error(w, r, http.StatusNotFound, "no such drop")
		return
	}
	if err != nil {
		s.Log.ErrorContext(r.Context(), "get drop", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "could not read the drop")
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}

func (s *Service) save(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("drop")
	if !ValidID(id) {
		httpx.Error(w, r, http.StatusBadRequest, "a drop ID is 1 to 64 lowercase letters, digits and hyphens")
		return
	}
	var req Request
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "request body must be a JSON drop")
		return
	}
	if problem := req.Validate(); problem != "" {
		httpx.Error(w, r, http.StatusBadRequest, problem)
		return
	}

	d, err := s.Save(r.Context(), id, req)
	if errors.Is(err, ErrOpen) {
		httpx.Error(w, r, http.StatusConflict, ErrOpen.Error())
		return
	}
	if err != nil {
		s.Log.ErrorContext(r.Context(), "save drop", "drop_id", id, "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "could not save the drop")
		return
	}
	s.Log.InfoContext(r.Context(), "drop saved", "drop_id", id, "opens_at", d.OpensAt, "tiers", len(d.Tiers))
	httpx.JSON(w, http.StatusOK, d)
}

// openDue is called on a timer by the scheduler. Calling it more often, or
// from two places at once, does no harm.
func (s *Service) openDue(w http.ResponseWriter, r *http.Request) {
	opened, err := s.OpenDue(r.Context())
	if err != nil {
		s.Log.ErrorContext(r.Context(), "open due drops", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "could not open the due drops")
		return
	}
	if opened == nil {
		opened = []string{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"opened": opened})
}
