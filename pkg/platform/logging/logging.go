// Package logging builds the structured logger every service uses.
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/kingswanzy2020/ticketdrop/pkg/platform/correlation"
)

// New returns a JSON logger writing to stdout. Every line carries the service
// name, and the correlation ID when the logging call is given a context that
// has one. LOG_LEVEL selects debug, info (default), warn or error.
func New(service string) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(ctxHandler{h}).With("service", service)
}

// ctxHandler adds the correlation ID from the context to each record.
type ctxHandler struct {
	slog.Handler
}

func (h ctxHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := correlation.From(ctx); id != "" {
		r.AddAttrs(slog.String("correlation_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h ctxHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return ctxHandler{h.Handler.WithAttrs(attrs)}
}

func (h ctxHandler) WithGroup(name string) slog.Handler {
	return ctxHandler{h.Handler.WithGroup(name)}
}
