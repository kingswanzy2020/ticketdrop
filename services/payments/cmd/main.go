// Command payments charges orders. It has no public API: it consumes
// order.created from its queue, calls a simulated payment provider, and
// publishes payment.succeeded or payment.failed through its outbox.
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
	"github.com/kingswanzy2020/ticketdrop/services/payments/internal/payment"
	"github.com/kingswanzy2020/ticketdrop/services/payments/migrations"
)

func main() {
	service.Main(payment.Source, run)
}

func run(ctx context.Context, s *service.Service) error {
	var (
		dbURL    = s.Config.Required("DATABASE_URL")
		topicARN = s.Config.Required("EVENTS_TOPIC_ARN")
		queueURL = s.Config.Required("EVENTS_QUEUE_URL")

		concurrency = max(s.Config.Int("CONSUMER_CONCURRENCY", 8), 1)
		waitTime    = s.Config.Duration("CONSUMER_WAIT_TIME", 20*time.Second)
		// Must stay below the queue's visibility timeout, and above twice
		// PAYMENT_LATENCY, the longest a provider call takes.
		timeout = s.Config.Duration("CONSUMER_TIMEOUT", 20*time.Second)

		relayInterval = s.Config.Duration("OUTBOX_INTERVAL", 250*time.Millisecond)
		relayBatch    = max(s.Config.Int("OUTBOX_BATCH", 100), 1)

		latency     = s.Config.Duration("PAYMENT_LATENCY", 100*time.Millisecond)
		declineRate = s.Config.Float("PAYMENT_DECLINE_RATE", 0)
		errorRate   = s.Config.Float("PAYMENT_ERROR_RATE", 0)
	)
	if err := s.Config.Err(); err != nil {
		return err
	}
	for name, rate := range map[string]float64{"PAYMENT_DECLINE_RATE": declineRate, "PAYMENT_ERROR_RATE": errorRate} {
		if rate < 0 || rate > 1 {
			return fmt.Errorf("%s: %v is not between 0 and 1", name, rate)
		}
	}

	pool, err := db.Connect(ctx, dbURL, s.Log)
	if err != nil {
		return err
	}
	defer pool.Close()
	err = db.Migrate(ctx, pool, s.Log, events.Schema(), db.Source{Name: payment.Source, FS: migrations.FS})
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

	processor := &payment.Processor{
		Pool:        pool,
		Log:         s.Log,
		Latency:     latency,
		DeclineRate: declineRate,
		ErrorRate:   errorRate,
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
		Handler:     processor.HandleEvent,
		Log:         s.Log,
		Metrics:     busMetrics,
		Concurrency: concurrency,
		WaitTime:    waitTime,
		Timeout:     timeout,
	}
	s.Log.Info("payment provider simulation",
		"latency", latency.String(), "decline_rate", declineRate, "error_rate", errorRate)

	// No app server: the admin port (probes and metrics) is the only listener.
	// The relay is registered first so that it stops last (see Serve).
	return s.Serve(ctx, nil,
		service.Worker{Name: "outbox-relay", Run: relay.Run},
		service.Worker{Name: "consumer", Run: consumer.Run},
	)
}
