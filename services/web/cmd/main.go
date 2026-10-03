// Command web is the shopfront: the pages a customer sees. It renders HTML on
// the server and gets everything it shows from the public API, through the
// gateway. It has no database and no events.
package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/kingswanzy2020/ticketdrop/pkg/platform/httpx"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/service"
	"github.com/kingswanzy2020/ticketdrop/services/web/internal/shop"
)

func main() {
	service.Main("web", run)
}

func run(ctx context.Context, s *service.Service) error {
	apiURL := s.Config.Required("API_URL")
	// A customer is waiting on every one of these calls.
	apiTimeout := s.Config.Duration("API_TIMEOUT", 5*time.Second)
	if err := s.Config.Err(); err != nil {
		return err
	}
	if u, err := url.Parse(apiURL); err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("API_URL: %q is not an absolute URL", apiURL)
	}

	front, err := shop.New(&shop.API{BaseURL: apiURL, Client: httpx.NewClient(apiTimeout)}, s.Log)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	front.Routes(mux)

	// Readiness has no check on the API, for the same reason the gateway's has
	// none on its services: if the API is down, taking every web pod out of
	// service as well would replace an error page with no page at all.
	return s.Serve(ctx, httpx.Wrap(mux, s.Log, httpx.NewMetrics(s.Registry)))
}
