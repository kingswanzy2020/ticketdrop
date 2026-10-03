// Command gateway is the only service reachable from outside. It forwards the
// public API routes to the services behind it.
package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/kingswanzy2020/ticketdrop/pkg/platform/httpx"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/service"
	"github.com/kingswanzy2020/ticketdrop/services/gateway/internal/proxy"
)

func main() {
	service.Main("gateway", run)
}

func run(ctx context.Context, s *service.Service) error {
	catalogURL := s.Config.Required("CATALOG_URL")
	ordersURL := s.Config.Required("ORDERS_URL")
	inventoryURL := s.Config.Required("INVENTORY_URL")
	if err := s.Config.Err(); err != nil {
		return err
	}
	catalog, err := upstream("CATALOG_URL", catalogURL)
	if err != nil {
		return err
	}
	orders, err := upstream("ORDERS_URL", ordersURL)
	if err != nil {
		return err
	}
	inventory, err := upstream("INVENTORY_URL", inventoryURL)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	proxy.Routes(mux, proxy.Upstreams{Catalog: catalog, Orders: orders, Inventory: inventory}, s.Log)

	// Readiness has no upstream check on purpose. If orders is down, taking
	// every gateway pod out of service too would turn a partial outage into a
	// total one; the gateway stays up and answers 502 for the affected routes.
	return s.Serve(ctx, httpx.Wrap(mux, s.Log, httpx.NewMetrics(s.Registry)))
}

// upstream parses the address of a service the gateway forwards to.
func upstream(key, value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("%s: %q is not an absolute URL", key, value)
	}
	return u, nil
}
