// Command notifications tells customers how their orders ended. It has no
// public API and no database: it consumes order.ticketed and order.failed from
// its queue and sends a message for each. Sending is a log line for now.
package main

import (
	"context"
	"fmt"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/kingswanzy2020/ticketdrop/pkg/platform/events"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/service"
	"github.com/kingswanzy2020/ticketdrop/services/notifications/internal/notify"
)

func main() {
	service.Main("notifications", run)
}

func run(ctx context.Context, s *service.Service) error {
	var (
		queueURL = s.Config.Required("EVENTS_QUEUE_URL")

		concurrency = max(s.Config.Int("CONSUMER_CONCURRENCY", 8), 1)
		waitTime    = s.Config.Duration("CONSUMER_WAIT_TIME", 20*time.Second)
		// Must stay below the queue's visibility timeout.
		timeout = s.Config.Duration("CONSUMER_TIMEOUT", 20*time.Second)
	)
	if err := s.Config.Err(); err != nil {
		return err
	}

	// Locally AWS_ENDPOINT_URL points the SDK at the emulator. On EKS the
	// variable is unset and credentials come from Pod Identity.
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("load AWS config: %w", err)
	}

	notifier := &notify.Notifier{
		Sender:  notify.LogSender{Log: s.Log},
		Metrics: notify.NewMetrics(s.Registry),
	}
	consumer := &events.Consumer{
		Client:      sqs.NewFromConfig(awsCfg),
		QueueURL:    queueURL,
		Handler:     notifier.HandleEvent,
		Log:         s.Log,
		Metrics:     events.NewMetrics(s.Registry),
		Concurrency: concurrency,
		WaitTime:    waitTime,
		Timeout:     timeout,
	}

	// No app server and nothing to publish: one consumer, and the admin port
	// (probes and metrics) as the only listener.
	return s.Serve(ctx, nil, service.Worker{Name: "consumer", Run: consumer.Run})
}
