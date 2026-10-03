// Package service is the lifecycle every TicketDrop service shares: start,
// serve, and shut down without dropping work.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/kingswanzy2020/ticketdrop/pkg/platform/config"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/httpx"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/logging"
)

// Version is set at build time with -ldflags.
var Version = "dev"

// Service is what a service's run function is handed.
type Service struct {
	Name     string
	Log      *slog.Logger
	Config   *config.Loader
	Health   *httpx.Health
	Registry *prometheus.Registry
}

// Worker is a background loop, such as a queue consumer, that runs for the
// life of the service. Run must return once its context is cancelled.
type Worker struct {
	Name string
	Run  func(ctx context.Context) error
}

// Main is what each service's main() calls. It builds the logger and metrics
// registry, turns SIGTERM and SIGINT into a cancelled context, and exits
// non-zero when run fails so that Kubernetes restarts the pod.
func Main(name string, run func(ctx context.Context, s *Service) error) {
	log := logging.New(name)
	slog.SetDefault(log)

	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	build := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "service_build_info",
		Help: "Always 1; the version label says which build is running.",
	}, []string{"version"})
	build.WithLabelValues(Version).Set(1)
	reg.MustRegister(build)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	s := &Service{
		Name:     name,
		Log:      log,
		Config:   &config.Loader{},
		Health:   &httpx.Health{},
		Registry: reg,
	}
	log.Info("starting", "version", Version)
	if err := run(ctx, s); err != nil {
		log.Error("service failed", "error", err)
		os.Exit(1)
	}
	log.Info("stopped")
}

// Serve runs the admin server, the app server (when app is not nil) and the
// workers until ctx is cancelled or one of them fails.
//
// Shutdown happens in the order that loses no work:
//
//  1. Readiness turns false, so Kubernetes takes the pod out of its Services.
//  2. Wait SHUTDOWN_DELAY. Endpoint removal is not instant; requests keep
//     arriving for a moment and are still served.
//  3. Stop accepting HTTP requests and let the ones in flight finish.
//  4. Stop the workers one at a time, last registered first. Consumers stop
//     receiving, and the messages they are already handling run to
//     completion. A service registers its outbox relay first, so the relay
//     stops last and publishes the events those final handlers wrote.
//  5. Stop the admin server last, so the final metrics can still be scraped.
//
// The pod's terminationGracePeriodSeconds must be longer than SHUTDOWN_DELAY
// plus SHUTDOWN_TIMEOUT, or Kubernetes kills the process partway through.
func (s *Service) Serve(ctx context.Context, app http.Handler, workers ...Worker) error {
	appAddr := s.Config.String("HTTP_ADDR", ":8080")
	adminAddr := s.Config.String("ADMIN_ADDR", ":9090")
	delay := s.Config.Duration("SHUTDOWN_DELAY", 0)
	timeout := s.Config.Duration("SHUTDOWN_TIMEOUT", 25*time.Second)
	if err := s.Config.Err(); err != nil {
		return err
	}

	errc := make(chan error, 2+len(workers))
	listen := func(name string, srv *http.Server) {
		go func() {
			if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				errc <- fmt.Errorf("%s server: %w", name, err)
			}
		}()
	}

	admin := &http.Server{
		Addr:              adminAddr,
		Handler:           httpx.Admin(s.Health, s.Registry),
		ReadHeaderTimeout: 5 * time.Second,
	}
	listen("admin", admin)

	var appSrv *http.Server
	if app != nil {
		appSrv = &http.Server{
			Addr:              appAddr,
			Handler:           app,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
		listen("app", appSrv)
	}

	// Each worker gets its own context, so that workers keep running through
	// steps 1-3 and can then be stopped one at a time.
	type running struct {
		name string
		stop context.CancelFunc
		done chan struct{}
	}
	started := make([]running, 0, len(workers))
	for _, w := range workers {
		workCtx, stop := context.WithCancel(context.Background())
		done := make(chan struct{})
		started = append(started, running{w.Name, stop, done})
		go func() {
			defer close(done)
			if err := w.Run(workCtx); err != nil && !errors.Is(err, context.Canceled) {
				errc <- fmt.Errorf("worker %s: %w", w.Name, err)
			}
		}()
	}

	s.Health.SetReady(true)
	s.Log.Info("ready", "http_addr", appAddr, "admin_addr", adminAddr, "workers", len(workers))

	var runErr error
	select {
	case <-ctx.Done():
		s.Log.Info("shutdown signal received", "delay", delay.String())
		s.Health.SetReady(false)
		time.Sleep(delay)
	case runErr = <-errc:
		s.Health.SetReady(false)
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if appSrv != nil {
		if err := appSrv.Shutdown(shutCtx); err != nil {
			s.Log.Warn("app server did not drain in time", "error", err)
		}
	}

	for i := len(started) - 1; i >= 0; i-- {
		w := started[i]
		w.stop()
		select {
		case <-w.done:
		case <-shutCtx.Done():
			s.Log.Warn("worker did not stop before the shutdown timeout", "worker", w.name)
		}
	}

	admin.Shutdown(shutCtx)
	return runErr
}
