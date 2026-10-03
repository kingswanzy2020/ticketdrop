// Package httpx holds what every service's HTTP surface shares: the request
// middleware, JSON responses, health probes and request metrics.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kingswanzy2020/ticketdrop/pkg/platform/correlation"
)

// Metrics records request rate, errors and duration: the three numbers the
// dashboards and alerts for every HTTP service are built from.
type Metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// NewMetrics registers the HTTP server metrics on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_server_requests_total",
			Help: "HTTP requests handled, by route and status code.",
		}, []string{"route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_server_request_duration_seconds",
			Help:    "Time taken to handle an HTTP request, by route.",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		}, []string{"route"}),
	}
	reg.MustRegister(m.requests, m.duration)
	return m
}

// Wrap adds the behaviour every route gets: a correlation ID, panic recovery,
// metrics and a log line.
func Wrap(next http.Handler, log *slog.Logger, m *Metrics) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(correlation.Header)
		if !correlation.Valid(id) {
			id = correlation.New()
		}
		ctx := correlation.With(r.Context(), id)
		r = r.WithContext(ctx)
		w.Header().Set(correlation.Header, id)

		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				log.ErrorContext(ctx, "panic in handler", "panic", p, "stack", string(debug.Stack()))
				if !rec.wrote {
					Error(rec, r, http.StatusInternalServerError, "internal error")
				}
				rec.status = http.StatusInternalServerError
			}

			// The mux records the pattern it matched ("GET /v1/orders/{id}") on
			// the request. Using the pattern rather than the path keeps the
			// label set small no matter how many distinct IDs are requested.
			route := r.Pattern
			if route == "" {
				route = "unmatched"
			}
			elapsed := time.Since(start)
			m.requests.WithLabelValues(route, strconv.Itoa(rec.status)).Inc()
			m.duration.WithLabelValues(route).Observe(elapsed.Seconds())

			level := slog.LevelDebug
			if rec.status >= 500 {
				level = slog.LevelError
			}
			log.Log(ctx, level, "request",
				"method", r.Method, "path", r.URL.Path, "status", rec.status,
				"duration_ms", elapsed.Milliseconds())
		}()

		next.ServeHTTP(rec, r)
	})
}

// recorder remembers the status code a handler wrote.
type recorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *recorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *recorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// JSON writes v as a JSON response.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// Error writes a JSON error body carrying the correlation ID, so that a user
// who reports a failure hands over the key to its logs.
func Error(w http.ResponseWriter, r *http.Request, status int, msg string) {
	JSON(w, status, map[string]string{
		"error":          msg,
		"correlation_id": correlation.From(r.Context()),
	})
}
