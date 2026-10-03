package httpx

import (
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Health answers the two questions Kubernetes asks of a pod.
//
// Liveness (/healthz): is the process alive? It never looks at dependencies. A
// database outage must not make Kubernetes restart every pod, because
// restarting cannot fix it.
//
// Readiness (/readyz): should this pod receive traffic right now? It is false
// while the service starts, false again as soon as shutdown begins, and false
// while a registered check (such as a database ping) is failing.
type Health struct {
	ready  atomic.Bool
	mu     sync.RWMutex
	checks []check
}

type check struct {
	name string
	fn   func(context.Context) error
}

// AddCheck registers a dependency that must respond for the pod to be ready.
func (h *Health) AddCheck(name string, fn func(context.Context) error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checks = append(h.checks, check{name, fn})
}

// SetReady marks the service as able, or no longer able, to take traffic.
func (h *Health) SetReady(ready bool) {
	h.ready.Store(ready)
}

func (h *Health) livez(w http.ResponseWriter, _ *http.Request) {
	io.WriteString(w, "ok\n")
}

func (h *Health) readyz(w http.ResponseWriter, r *http.Request) {
	if !h.ready.Load() {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	h.mu.RLock()
	checks := h.checks
	h.mu.RUnlock()
	for _, c := range checks {
		if err := c.fn(ctx); err != nil {
			http.Error(w, c.name+": "+err.Error(), http.StatusServiceUnavailable)
			return
		}
	}
	io.WriteString(w, "ready\n")
}

// Admin returns the handler for the admin port: probes and Prometheus metrics.
// It is served on its own port so that these endpoints are never reachable
// through the public route to the service.
func Admin(h *Health, reg *prometheus.Registry) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.livez)
	mux.HandleFunc("GET /readyz", h.readyz)
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	return mux
}
