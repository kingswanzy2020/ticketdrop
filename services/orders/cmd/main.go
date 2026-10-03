// Command orders owns the order lifecycle. It accepts an order over HTTP once
// inventory has held its tickets, publishes order.created through its outbox,
// and closes the order when ticket.issued or payment.failed comes back.
package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/kingswanzy2020/ticketdrop/pkg/platform/db"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/events"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/httpx"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/service"
	"github.com/kingswanzy2020/ticketdrop/services/orders/internal/order"
	"github.com/kingswanzy2020/ticketdrop/services/orders/migrations"
)

func main() {
	service.Main(order.Source, run)
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

		inventoryURL = s.Config.Required("INVENTORY_URL")
		// A customer is waiting on this call, so it is kept short. When
		// inventory is slower than this, the order is refused with a 503.
		inventoryTimeout = s.Config.Duration("INVENTORY_TIMEOUT", 2*time.Second)
	)
	if err := s.Config.Err(); err != nil {
		return err
	}

	pool, err := db.Connect(ctx, dbURL, s.Log)
	if err != nil {
		return err
	}
	defer pool.Close()
	err = db.Migrate(ctx, pool, s.Log, events.Schema(), db.Source{Name: order.Source, FS: migrations.FS})
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

	orders := &order.Service{
		Pool:      pool,
		Log:       s.Log,
		Inventory: &order.Inventory{BaseURL: inventoryURL, Client: httpx.NewClient(inventoryTimeout)},
	}
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
		Handler:     orders.HandleEvent,
		Log:         s.Log,
		Metrics:     busMetrics,
		Concurrency: concurrency,
		WaitTime:    waitTime,
		Timeout:     timeout,
	}

	mux := http.NewServeMux()
	orders.Routes(mux)
	app := httpx.Wrap(mux, s.Log, httpx.NewMetrics(s.Registry))

	// The relay is registered first so that it stops last (see Serve).
	return s.Serve(ctx, app,
		service.Worker{Name: "outbox-relay", Run: relay.Run},
		service.Worker{Name: "consumer", Run: consumer.Run},
	)
}
