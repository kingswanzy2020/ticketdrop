// Package schedule is the scheduler service: it runs jobs on a timer, on one
// replica at a time.
package schedule

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/kingswanzy2020/ticketdrop/pkg/platform/correlation"
)

// The key of the advisory lock that makes one replica the leader. An arbitrary
// number; it only has to differ from other advisory locks in this database.
const leaderLock int64 = 7426002

// How long one run of a job may take.
const jobTimeout = 10 * time.Second

// Job is something done at a fixed interval by whichever replica is leader.
//
// A job must be safe to run twice at once. Leader election keeps that from
// happening in normal operation, but for a moment during a failover two
// replicas can both believe they lead. The jobs here only tell another
// service that it is time to do something, and those services make sure the
// work itself happens once.
type Job struct {
	Name     string
	Interval time.Duration
	Run      func(ctx context.Context) error
}

// Post returns a job that sends an empty POST to url and expects a 2xx answer.
func Post(client *http.Client, url string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		// Reading the body to the end lets the connection be reused.
		io.Copy(io.Discard, resp.Body)
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return fmt.Errorf("POST %s: status %d", url, resp.StatusCode)
		}
		return nil
	}
}

// Metrics says which replica leads and how its jobs are going.
type Metrics struct {
	leader prometheus.Gauge
	runs   *prometheus.CounterVec
}

// NewMetrics registers the scheduler metrics on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		leader: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "scheduler_leader",
			Help: "1 on the replica that is running the jobs, 0 on a standby. The sum across replicas should be 1.",
		}),
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "scheduler_job_runs_total",
			Help: "Job runs, by job and result (ok, error).",
		}, []string{"job", "result"}),
	}
	reg.MustRegister(m.leader, m.runs)
	return m
}

// Scheduler runs Jobs while it is the leader, and waits its turn while it is not.
type Scheduler struct {
	Pool    *pgxpool.Pool
	Log     *slog.Logger
	Metrics *Metrics
	Jobs    []Job
	// Retry is how often a standby tries to become the leader. It is also
	// roughly how long the jobs pause when the leader dies.
	Retry time.Duration
}

// Run leads whenever it can, until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) error {
	for {
		if err := s.lead(ctx); err != nil && ctx.Err() == nil {
			s.Log.Warn("not leading", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(s.Retry):
		}
	}
}

// lead tries to become the leader. If another replica already is, it returns
// at once. Otherwise it runs the jobs until ctx is cancelled or leadership is
// lost.
//
// Leadership is a PostgreSQL advisory lock. The lock belongs to one database
// session and is released by the server the moment that session ends, whether
// the process shut down cleanly, was killed, or lost its network. Nothing has
// to be cleaned up for another replica to take over.
func (s *Scheduler) lead(ctx context.Context) error {
	pooled, err := s.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	// The connection is taken out of the pool and closed on the way out, so
	// that the lock always goes with it. Returned to the pool instead, it
	// would stay open and keep the lock with nobody leading.
	conn := pooled.Hijack()
	defer conn.Close(context.WithoutCancel(ctx))

	var leader bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, leaderLock).Scan(&leader); err != nil {
		return fmt.Errorf("try the leader lock: %w", err)
	}
	if !leader {
		return nil
	}

	s.Log.Info("became leader", "jobs", len(s.Jobs))
	s.Metrics.leader.Set(1)
	defer s.Log.Info("no longer leader")
	defer s.Metrics.leader.Set(0)

	jobCtx, stop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for _, j := range s.Jobs {
		wg.Go(func() { s.every(jobCtx, j) })
	}
	defer wg.Wait()
	defer stop()

	// Leadership lasts as long as the session holding the lock. If the
	// database restarts or the network drops, the lock is gone and another
	// replica may already lead, so this one must stop.
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
		if err := conn.Ping(ctx); err != nil {
			return fmt.Errorf("lost the connection holding the leader lock: %w", err)
		}
	}
}

// every runs j now and then once per interval, until ctx is cancelled.
func (s *Scheduler) every(ctx context.Context, j Job) {
	tick := time.NewTicker(j.Interval)
	defer tick.Stop()

	failing := false
	for {
		err := s.once(ctx, j)
		if ctx.Err() != nil {
			return
		}
		// A job that fails every second would fill the log, so only the
		// change is logged. The counter records every run.
		switch {
		case err != nil && !failing:
			s.Log.Warn("job is failing", "job", j.Name, "error", err)
		case err == nil && failing:
			s.Log.Info("job recovered", "job", j.Name)
		}
		failing = err != nil

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// once runs j one time and counts the result.
func (s *Scheduler) once(ctx context.Context, j Job) error {
	// Each run starts a new trail: the service that is called logs under this
	// ID, and so does every event that results.
	ctx, cancel := context.WithTimeout(correlation.With(ctx, correlation.New()), jobTimeout)
	defer cancel()

	err := j.Run(ctx)
	if err != nil {
		s.Metrics.runs.WithLabelValues(j.Name, "error").Inc()
		return err
	}
	s.Metrics.runs.WithLabelValues(j.Name, "ok").Inc()
	return nil
}
