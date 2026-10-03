// Command fulfillment issues tickets. It has no public API: it consumes
// order.confirmed from its queue, writes the tickets, and publishes
// ticket.issued through its outbox.
package main

import (
	"context"
	"fmt"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/kingswanzy2020/ticketdrop/pkg/platform/db"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/events"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/service"
	"github.com/kingswanzy2020/ticketdrop/services/fulfillment/internal/ticket"
	"github.com/kingswanzy2020/ticketdrop/services/fulfillment/migrations"
)

func main() {
	service.Main(ticket.Source, run)
}

func run(ctx context.Context, s *service.Service) error {
	var (
		dbURL    = s.Config.Required("DATABASE_URL")
		topicARN = s.Config.Required("EVENTS_TOPIC_ARN")
		queueURL = s.Config.Required("EVENTS_QUEUE_URL")

		concurrency = max(s.Config.Int("CONSUMER_CONCURRENCY", 8), 1)
		waitTime    = s.Config.Duration("CONSUMER_WAIT_TIME", 20*time.Second)
		// Must stay below the queue's visibility timeout.
		timeout = s.Config.Duration("CONSUMER_TIMEOUT", 20*time.Second)

		relayInterval = s.Config.Duration("OUTBOX_INTERVAL", 250*time.Millisecond)
		relayBatch    = max(s.Config.Int("OUTBOX_BATCH", 100), 1)

		renderTime = s.Config.Duration("SIMULATED_RENDER_TIME", 0)
	)
	if err := s.Config.Err(); err != nil {
		return err
	}

	pool, err := db.Connect(ctx, dbURL, s.Log)
	if err != nil {
		return err
	}
	defer pool.Close()
	err = db.Migrate(ctx, pool, s.Log, events.Schema(), db.Source{Name: ticket.Source, FS: migrations.FS})
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

	issuer := &ticket.Issuer{Pool: pool, Log: s.Log, RenderTime: renderTime}
	busMetrics := events.NewMetrics(s.Registry)
	relay := &events.Relay{
		Pool:      pool,
		Publisher: &events.SNSPublisher{Client: sns.NewFromConfig(awsCfg), TopicARN: topicARN, Metrics: busMetrics},
		Log:       s.Log,
		Metrics:   busMetrics,
		Interval:  relayInterval,
		Batch:     relayBatch,
	}
	consumer := &events.Consumer{
		Client:      sqs.NewFromConfig(awsCfg),
		QueueURL:    queueURL,
		Handler:     issuer.HandleEvent,
		Log:         s.Log,
		Metrics:     busMetrics,
		Concurrency: concurrency,
		WaitTime:    waitTime,
		Timeout:     timeout,
	}

	// No app server: the admin port (probes and metrics) is the only listener.
	// The relay is registered first so that it stops last (see Serve).
	return s.Serve(ctx, nil,
		service.Worker{Name: "outbox-relay", Run: relay.Run},
		service.Worker{Name: "consumer", Run: consumer.Run},
	)
}
