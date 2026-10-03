// Command scheduler is the clock of the platform. On a timer it tells catalog
// to open the drops that are due, and inventory to release the holds that have
// expired. It does none of that work itself.
//
// Several replicas can run. One leads and the others stand by, so the timer
// keeps going when a replica is lost.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/kingswanzy2020/ticketdrop/pkg/platform/db"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/httpx"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/service"
	"github.com/kingswanzy2020/ticketdrop/services/scheduler/internal/schedule"
)

func main() {
	service.Main("scheduler", run)
}

func run(ctx context.Context, s *service.Service) error {
	var (
		// The database holds no tables. It is where the leader lock lives.
		dbURL        = s.Config.Required("DATABASE_URL")
		catalogURL   = s.Config.Required("CATALOG_URL")
		inventoryURL = s.Config.Required("INVENTORY_URL")

		// How late a drop can open, at most.
		openInterval = s.Config.Duration("OPEN_INTERVAL", time.Second)
		// How long an expired hold can wait, at most, before its tickets
		// return to stock.
		expireInterval = s.Config.Duration("EXPIRE_INTERVAL", 5*time.Second)
		leaderRetry    = s.Config.Duration("LEADER_RETRY", 5*time.Second)
	)
	if err := s.Config.Err(); err != nil {
		return err
	}
	for name, d := range map[string]time.Duration{
		"OPEN_INTERVAL": openInterval, "EXPIRE_INTERVAL": expireInterval, "LEADER_RETRY": leaderRetry,
	} {
		if d <= 0 {
			return fmt.Errorf("%s: %v is not a positive duration", name, d)
		}
	}

	pool, err := db.Connect(ctx, dbURL, s.Log)
	if err != nil {
		return err
	}
	defer pool.Close()
	s.Health.AddCheck("postgres", pool.Ping)

	client := httpx.NewClient(5 * time.Second)
	scheduler := &schedule.Scheduler{
		Pool:    pool,
		Log:     s.Log,
		Metrics: schedule.NewMetrics(s.Registry),
		Retry:   leaderRetry,
		Jobs: []schedule.Job{
			{
				Name:     "open-due-drops",
				Interval: openInterval,
				Run:      schedule.Post(client, catalogURL+"/internal/v1/drops/open-due"),
			},
			{
				Name:     "release-expired-holds",
				Interval: expireInterval,
				Run:      schedule.Post(client, inventoryURL+"/internal/v1/holds/expire"),
			},
		},
	}

	// No app server: the admin port (probes and metrics) is the only listener.
	// A standby is as ready as the leader; readiness says the process is
	// healthy, not that it leads.
	return s.Serve(ctx, nil, service.Worker{Name: "scheduler", Run: scheduler.Run})
}
