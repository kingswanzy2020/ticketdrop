package schedule

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/kingswanzy2020/ticketdrop/pkg/platform/correlation"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/httpx"
)

func TestPost(t *testing.T) {
	var method, id atomic.Value
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method.Store(r.Method)
		id.Store(r.Header.Get(correlation.Header))
		w.WriteHeader(status)
	}))
	defer srv.Close()
	job := Post(httpx.NewClient(time.Second), srv.URL+"/internal/v1/holds/expire")

	if err := job(correlation.With(context.Background(), "run-1")); err != nil {
		t.Fatalf("job against a healthy service = %v, want nil", err)
	}
	if method.Load() != http.MethodPost {
		t.Errorf("service saw method %v, want POST", method.Load())
	}
	if id.Load() != "run-1" {
		t.Errorf("service saw correlation ID %v, want run-1", id.Load())
	}

	status = http.StatusInternalServerError
	if err := job(context.Background()); err == nil {
		t.Error("job against a failing service = nil, want an error")
	}
}

func TestEveryRunsUntilCancelledAndCountsResults(t *testing.T) {
	s := &Scheduler{
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics: NewMetrics(prometheus.NewRegistry()),
	}
	ctx, cancel := context.WithCancel(context.Background())

	// Fails on its second run, succeeds otherwise, and stops the test after five.
	var runs atomic.Int32
	var ids []string
	job := Job{Name: "test", Interval: time.Millisecond, Run: func(ctx context.Context) error {
		ids = append(ids, correlation.From(ctx))
		switch runs.Add(1) {
		case 2:
			return errors.New("boom")
		case 5:
			cancel()
		}
		return nil
	}}

	done := make(chan struct{})
	go func() { s.every(ctx, job); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("every did not return after its context was cancelled")
	}

	if n := runs.Load(); n != 5 {
		t.Errorf("job ran %d times, want 5", n)
	}
	if ok, failed := testutil.ToFloat64(s.Metrics.runs.WithLabelValues("test", "ok")),
		testutil.ToFloat64(s.Metrics.runs.WithLabelValues("test", "error")); ok != 4 || failed != 1 {
		t.Errorf("counted %v ok and %v error, want 4 and 1", ok, failed)
	}
	// A failure does not stop the timer, and every run has its own trail.
	seen := map[string]bool{}
	for _, id := range ids {
		if !correlation.Valid(id) || seen[id] {
			t.Errorf("run correlation IDs = %v, want five distinct valid IDs", ids)
			break
		}
		seen[id] = true
	}
}
