package httpx

import (
	"net/http"
	"time"

	"github.com/kingswanzy2020/ticketdrop/pkg/platform/correlation"
)

// NewClient returns the HTTP client a service uses to call another service.
//
// Every call is bounded by timeout: a slow dependency must not hold a request
// open indefinitely. Every call also carries the correlation ID of the request
// being served, so both services log under the same ID.
func NewClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// The default keeps 2 idle connections per host. A service calling one
	// dependency for every request would open and close a connection for
	// almost every call.
	transport.MaxIdleConnsPerHost = 100
	return &http.Client{Timeout: timeout, Transport: correlate{transport}}
}

// correlate adds the correlation ID from the request's context to its headers.
type correlate struct {
	next http.RoundTripper
}

func (c correlate) RoundTrip(r *http.Request) (*http.Response, error) {
	if id := correlation.From(r.Context()); id != "" {
		// A RoundTripper must not modify the request it was given.
		r = r.Clone(r.Context())
		r.Header.Set(correlation.Header, id)
	}
	return c.next.RoundTrip(r)
}
