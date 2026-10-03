package proxy

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kingswanzy2020/ticketdrop/pkg/platform/correlation"
	"github.com/kingswanzy2020/ticketdrop/pkg/platform/httpx"
)

// gateway returns a running gateway in front of the three upstream addresses.
func gateway(t *testing.T, catalog, orders, inventory string) *httptest.Server {
	t.Helper()
	parse := func(s string) *url.URL {
		u, err := url.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	Routes(mux, Upstreams{Catalog: parse(catalog), Orders: parse(orders), Inventory: parse(inventory)}, log)
	gw := httptest.NewServer(httpx.Wrap(mux, log, httpx.NewMetrics(prometheus.NewRegistry())))
	t.Cleanup(gw.Close)
	return gw
}

// counter returns an upstream that counts the requests it receives.
func counter(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestForwardsListedRoutes(t *testing.T) {
	var gotID, gotRoute atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID.Store(r.Header.Get(correlation.Header))
		gotRoute.Store(r.Method + " " + r.URL.Path)
		// Like every service, the upstream echoes the ID on its response.
		w.Header().Set(correlation.Header, r.Header.Get(correlation.Header))
		w.WriteHeader(http.StatusCreated)
	}))
	defer upstream.Close()
	gw := gateway(t, upstream.URL, upstream.URL, upstream.URL)

	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/orders", strings.NewReader(`{}`))
	req.Header.Set(correlation.Header, "req-123")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want the upstream's 201", resp.StatusCode)
	}
	if got := gotRoute.Load(); got != "POST /v1/orders" {
		t.Errorf("upstream saw %q", got)
	}
	if got := gotID.Load(); got != "req-123" {
		t.Errorf("upstream saw correlation ID %q, want req-123", got)
	}
	if got := resp.Header.Values(correlation.Header); len(got) != 1 || got[0] != "req-123" {
		t.Errorf("response correlation headers = %q, want exactly one req-123", got)
	}
}

func TestEachRouteGoesToItsOwnService(t *testing.T) {
	catalog, catalogCalls := counter(t)
	orders, orderCalls := counter(t)
	inventory, inventoryCalls := counter(t)
	gw := gateway(t, catalog.URL, orders.URL, inventory.URL)

	paths := []string{
		"/v1/drops",                          // catalog
		"/v1/drops/summer-fest",              // catalog
		"/v1/drops/summer-fest/availability", // inventory
		"/v1/orders/123",                     // orders
	}
	for _, path := range paths {
		resp, err := http.Get(gw.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	if c, o, i := catalogCalls.Load(), orderCalls.Load(), inventoryCalls.Load(); c != 2 || o != 1 || i != 1 {
		t.Errorf("catalog was called %d times, orders %d and inventory %d; want 2, 1 and 1", c, o, i)
	}
}

func TestUnlistedRouteNeverReachesUpstream(t *testing.T) {
	upstream, calls := counter(t)
	gw := gateway(t, upstream.URL, upstream.URL, upstream.URL)

	requests := []struct{ method, path string }{
		{http.MethodGet, "/metrics"},
		{http.MethodGet, "/v1/orders"},
		{http.MethodGet, "/v1/orders/1/refund"},
		// Routes that exist on a service, for other services only.
		{http.MethodPost, "/internal/v1/holds"},
		{http.MethodPost, "/internal/v1/holds/expire"},
		{http.MethodPut, "/internal/v1/drops/summer-fest"},
		{http.MethodPost, "/internal/v1/drops/open-due"},
		// A public path, with a method only the internal API offers.
		{http.MethodPut, "/v1/drops/summer-fest"},
	}
	for _, rq := range requests {
		req, _ := http.NewRequest(rq.method, gw.URL+rq.path, strings.NewReader(`{}`))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 404 or 405", rq.method, rq.path, resp.StatusCode)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("upstream was called %d times, want 0", n)
	}
}

func TestUpstreamDownIsBadGateway(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	upstream.Close() // nothing listens on this address any more
	gw := gateway(t, upstream.URL, upstream.URL, upstream.URL)

	resp, err := http.Get(gw.URL + "/v1/orders/123")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !correlation.Valid(body["correlation_id"]) {
		t.Errorf("body = %v, want a correlation_id", body)
	}
}
