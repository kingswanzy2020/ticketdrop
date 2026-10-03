// Command catalog keeps the list of drops: what is on sale, where, from when,
// and how many tickets of each kind. It answers over HTTP, and publishes
// drop.opened through its outbox when a drop goes on sale.
package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sns"

	"github.com/kingswanzy2020/ticketdrop/pkg/platform/db"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/events"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/httpx"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/service"
	"github.com/kingswanzy2020/ticketdrop/services/catalog/internal/catalog"
	"github.com/kingswanzy2020/ticketdrop/services/catalog/migrations"
)

func main() {
	service.Main(catalog.Source, run)
}

func run(ctx context.Context, s *service.Service) error {
	var (
		dbURL    = s.Config.Required("DATABASE_URL")
		topicARN = s.Config.Required("EVENTS_TOPIC_ARN")

		relayInterval = s.Config.Duration("OUTBOX_INTERVAL", 250*time.Millisecond)
		relayBatch    = max(s.Config.Int("OUTBOX_BATCH", 100), 1)
	)
	if err := s.Config.Err(); err != nil {
		return err
	}

	pool, err := db.Connect(ctx, dbURL, s.Log)
	if err != nil {
		return err
	}
	defer pool.Close()
	err = db.Migrate(ctx, pool, s.Log, events.Schema(), db.Source{Name: catalog.Source, FS: migrations.FS})
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	s.Health.AddCheck("postgres", pool.Ping)

	// Locally AWS_ENDPOINT_URL points the SDK at the emulator. On EKS the
	// variable is unset and credentials come from Pod Identity.
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("load AWS config: %w", err)
	}

	// Catalog publishes and consumes nothing, so it has a relay and no queue.
	busMetrics := events.NewMetrics(s.Registry)
	relay := &events.Relay{
		Pool:      pool,
		Publisher: &events.SNSPublisher{Client: sns.NewFromConfig(awsCfg), TopicARN: topicARN, Metrics: busMetrics},
		Log:       s.Log,
		Metrics:   busMetrics,
		Interval:  relayInterval,
		Batch:     relayBatch,
	}

	drops := &catalog.Service{Pool: pool, Log: s.Log}
	mux := http.NewServeMux()
	drops.Routes(mux)
	app := httpx.Wrap(mux, s.Log, httpx.NewMetrics(s.Registry))

	return s.Serve(ctx, app, service.Worker{Name: "outbox-relay", Run: relay.Run})
}
