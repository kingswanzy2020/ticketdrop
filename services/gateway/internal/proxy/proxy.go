// Package proxy is the gateway's routing table: which public routes exist and
// which service answers each of them.
package proxy

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/kingswanzy2020/ticketdrop/pkg/platform/correlation"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/httpx"
)

// Upstreams are the services the gateway forwards to.
type Upstreams struct {
	Catalog   *url.URL
	Orders    *url.URL
	Inventory *url.URL
}

// Routes registers the public API on mux.
//
// Routes are listed one by one instead of forwarding a whole path prefix. A
// path that is not listed gets a 404 here and never reaches a service, so
// adding an internal endpoint to a service does not publish it.
func Routes(mux *http.ServeMux, up Upstreams, log *slog.Logger) {
	catalog := to(up.Catalog, log)
	mux.Handle("GET /v1/drops", catalog)
	mux.Handle("GET /v1/drops/{drop}", catalog)

	orders := to(up.Orders, log)
	mux.Handle("POST /v1/orders", orders)
	mux.Handle("GET /v1/orders/{id}", orders)

	inventory := to(up.Inventory, log)
	mux.Handle("GET /v1/drops/{drop}/availability", inventory)
}

// to returns a handler that forwards requests to target.
func to(target *url.URL, log *slog.Logger) http.Handler {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// The default keeps 2 idle connections per host. Under a drop the gateway
	// sends hundreds of concurrent requests to one upstream, and would open
	// and close a connection for almost every one of them.
	transport.MaxIdleConnsPerHost = 100
	transport.ResponseHeaderTimeout = 10 * time.Second

	return &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.SetXForwarded()
			// The ID was accepted or created by httpx.Wrap. Passing it on puts
			// the upstream's log lines under the same ID as the gateway's.
			r.Out.Header.Set(correlation.Header, correlation.From(r.In.Context()))
		},
		ModifyResponse: func(resp *http.Response) error {
			// The gateway has already set this header on its own response;
			// copying the upstream's as well would send it twice.
			resp.Header.Del(correlation.Header)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.ErrorContext(r.Context(), "upstream request failed", "upstream", target.Host, "error", err)
			httpx.Error(w, r, http.StatusBadGateway, "upstream unavailable")
		},
	}
}
