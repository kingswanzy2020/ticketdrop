// Package correlation carries one ID through every hop a request makes: the
// HTTP calls between services, the events they publish, and every log line
// written along the way.
package correlation

import (
	"context"

	"github.com/google/uuid"
)

// Header is the HTTP header the ID travels in.
const Header = "X-Correlation-ID"

type ctxKey struct{}

// With returns a context carrying id.
func With(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// From returns the ID carried by ctx, or "" when there is none.
func From(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// New returns a fresh ID.
func New() string {
	return uuid.NewString()
}

// Valid reports whether id is safe to accept from a caller and write to logs:
// short, and made only of letters, digits, '-', '_' and '.'.
func Valid(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}
