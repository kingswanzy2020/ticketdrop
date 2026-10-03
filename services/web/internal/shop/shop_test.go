package shop

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// box is a stand-in for the public API with one drop.
type box struct {
	mu       sync.Mutex
	drop     Drop
	stock    []Stock // nil: the drop is not on sale
	orders   map[string]Order
	answer   int // status for POST /v1/orders
	received []OrderRequest
}

func (b *box) handler() http.Handler {
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /v1/drops", func(w http.ResponseWriter, _ *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		write(w, 200, map[string]any{"drops": []Drop{b.drop}})
	})
	mux.HandleFunc("GET /v1/drops/{drop}", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		if r.PathValue("drop") != b.drop.ID {
			write(w, 404, map[string]string{"error": "no such drop"})
			return
		}
		write(w, 200, b.drop)
	})
	mux.HandleFunc("GET /v1/drops/{drop}/availability", func(w http.ResponseWriter, _ *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.stock == nil {
			write(w, 404, map[string]string{"error": "no such drop on sale"})
			return
		}
		write(w, 200, map[string]any{"tiers": b.stock})
	})
	mux.HandleFunc("POST /v1/orders", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		var req OrderRequest
		json.NewDecoder(r.Body).Decode(&req)
		b.received = append(b.received, req)
		if b.answer != 201 {
			write(w, b.answer, map[string]string{"error": "customer_email must be a valid email address"})
			return
		}
		o := Order{ID: "0b5c1c0e-6f0a-4f7e-9d3a-2a4f1f5f8a11", DropID: req.DropID, Tier: req.Tier,
			Quantity: req.Quantity, CustomerEmail: req.CustomerEmail, Status: "pending"}
		b.orders[o.ID] = o
		write(w, 201, o)
	})
	mux.HandleFunc("GET /v1/orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		o, ok := b.orders[r.PathValue("id")]
		if !ok {
			write(w, 404, map[string]string{"error": "order not found"})
			return
		}
		write(w, 200, o)
	})
	return mux
}

// front returns a running shopfront over a box office with one open drop.
func front(t *testing.T) (*httptest.Server, *box) {
	t.Helper()
	b := &box{
		drop: Drop{
			ID: "summer-fest", Name: "Summer Fest", Venue: "Accra Arena", Status: "open",
			OpensAt: time.Date(2027, 6, 1, 10, 0, 0, 0, time.UTC),
			Tiers:   []Tier{{Tier: "general", PriceCents: 2500, Capacity: 100}, {Tier: "vip", PriceCents: 9000, Capacity: 10}},
		},
		stock:  []Stock{{Tier: "general", Available: 42}, {Tier: "vip", Available: 0}},
		orders: map[string]Order{},
		answer: 201,
	}
	api := httptest.NewServer(b.handler())
	t.Cleanup(api.Close)

	s, err := New(&API{BaseURL: api.URL, Client: api.Client()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Routes(mux)
	web := httptest.NewServer(mux)
	t.Cleanup(web.Close)
	return web, b
}

// fetch makes a request the way a browser would (htmx false) or the way htmx
// would, without following redirects, and returns the status, body and
// Location header.
func fetch(t *testing.T, method, target string, form url.Values, htmx bool) (int, string, string) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out), resp.Header.Get("Location")
}

func contains(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("page does not contain %q\n%s", w, body)
		}
	}
}

func lacks(t *testing.T, body string, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(body, u) {
			t.Errorf("page should not contain %q\n%s", u, body)
		}
	}
}

func order() url.Values {
	return url.Values{"tier": {"general"}, "quantity": {"2"}, "email": {"ama@example.com"}}
}

func TestHomeListsDrops(t *testing.T) {
	web, _ := front(t)

	status, body, _ := fetch(t, "GET", web.URL+"/", nil, false)

	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	contains(t, body, "<!doctype html>", "Summer Fest", "Accra Arena", `href="/drops/summer-fest"`, "On sale")
}

func TestOpenDropShowsWhatIsLeftAndAForm(t *testing.T) {
	web, _ := front(t)

	status, body, _ := fetch(t, "GET", web.URL+"/drops/summer-fest", nil, false)

	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	contains(t, body, "Summer Fest", "$25.00", "$90.00", ">42<", "Sold out",
		`action="/drops/summer-fest/orders"`, `hx-get="/drops/summer-fest/tiers"`)
	// An open drop no longer asks whether it has opened.
	lacks(t, body, `hx-get="/drops/summer-fest/sale"`)
}

func TestDropThatHasNotOpenedWaitsAndOffersNoForm(t *testing.T) {
	// Twice: before catalog opens it, and in the moment after, when catalog
	// says open but inventory has no stock for it yet.
	for _, status := range []string{"scheduled", "open"} {
		t.Run(status, func(t *testing.T) {
			web, b := front(t)
			b.drop.Status = status
			b.stock = nil

			code, body, _ := fetch(t, "GET", web.URL+"/drops/summer-fest", nil, false)

			if code != 200 {
				t.Fatalf("status = %d", code)
			}
			contains(t, body, "Tickets go on sale", "1 Jun 2027 at 10:00 UTC", "$25.00",
				`hx-get="/drops/summer-fest/sale"`, `hx-trigger="every 1s"`)
			lacks(t, body, "<form", "Buy tickets")
		})
	}
}

func TestSaleFragmentTurnsIntoTheFormOnceTheDropOpens(t *testing.T) {
	web, b := front(t)
	b.drop.Status = "scheduled"
	b.stock = nil
	_, before, _ := fetch(t, "GET", web.URL+"/drops/summer-fest/sale", nil, true)

	b.mu.Lock()
	b.drop.Status = "open"
	b.stock = []Stock{{Tier: "general", Available: 100}, {Tier: "vip", Available: 10}}
	b.mu.Unlock()
	_, after, _ := fetch(t, "GET", web.URL+"/drops/summer-fest/sale", nil, true)

	contains(t, before, "Tickets go on sale")
	lacks(t, before, "<!doctype html>", "<form")
	contains(t, after, "<form", ">100<")
	lacks(t, after, "Tickets go on sale", `hx-get="/drops/summer-fest/sale"`)
}

func TestPlacingAnOrder(t *testing.T) {
	t.Run("through htmx returns the order, which then follows its own progress", func(t *testing.T) {
		web, b := front(t)

		status, body, _ := fetch(t, "POST", web.URL+"/drops/summer-fest/orders", order(), true)

		if status != 200 {
			t.Fatalf("status = %d", status)
		}
		contains(t, body, "Order received", "2 general tickets",
			`hx-get="/orders/0b5c1c0e-6f0a-4f7e-9d3a-2a4f1f5f8a11/status"`, `hx-trigger="every 1s"`)
		lacks(t, body, "<!doctype html>")
		want := OrderRequest{DropID: "summer-fest", Tier: "general", Quantity: 2, CustomerEmail: "ama@example.com"}
		if len(b.received) != 1 || b.received[0] != want {
			t.Errorf("the API received %+v, want one %+v", b.received, want)
		}
	})

	t.Run("without htmx redirects to the order's own page", func(t *testing.T) {
		web, _ := front(t)

		status, _, location := fetch(t, "POST", web.URL+"/drops/summer-fest/orders", order(), false)

		if status != http.StatusSeeOther || location != "/orders/0b5c1c0e-6f0a-4f7e-9d3a-2a4f1f5f8a11" {
			t.Errorf("status = %d, Location = %q; want 303 to the order page", status, location)
		}
	})
}

func TestOrderThatIsRefused(t *testing.T) {
	tests := []struct {
		name       string
		answer     int
		form       func(url.Values)
		wantStatus int // what a browser sees; htmx always gets 200 with the same words
		wantText   string
		reachesAPI bool
	}{
		{"sold out", 409, nil, 409, "Not enough general tickets are left for this order", true},
		{"not on sale", 404, nil, 404, "These tickets are not on sale", true},
		{"refused by the API", 400, nil, 400, "The box office refused it", true},
		{"API failing", 500, nil, 502, "Your order may not have been placed", true},
		{"no tickets asked for", 201, func(f url.Values) { f.Set("quantity", "0") }, 400, "between 1 and 8 tickets", false},
		{"too many tickets", 201, func(f url.Values) { f.Set("quantity", "9") }, 400, "between 1 and 8 tickets", false},
		{"not an email address", 201, func(f url.Values) { f.Set("email", "ama") }, 400, "working email address", false},
		{"no ticket chosen", 201, func(f url.Values) { f.Del("tier") }, 400, "Pick which kind of ticket", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, viaHTMX := range []bool{true, false} {
				web, b := front(t)
				b.answer = tt.answer
				form := order()
				if tt.form != nil {
					tt.form(form)
				}

				status, body, _ := fetch(t, "POST", web.URL+"/drops/summer-fest/orders", form, viaHTMX)

				want := tt.wantStatus
				if viaHTMX {
					// htmx throws away the body of an error response, so the
					// message has to arrive with a 200.
					want = 200
				}
				if status != want {
					t.Errorf("htmx=%v: status = %d, want %d", viaHTMX, status, want)
				}
				contains(t, body, tt.wantText)
				lacks(t, body, "Order received")
				if reached := len(b.received) > 0; reached != tt.reachesAPI {
					t.Errorf("htmx=%v: reached the API = %v, want %v", viaHTMX, reached, tt.reachesAPI)
				}
			}
		})
	}
}

func TestOrderStatusStopsAskingOnceTheOrderHasEnded(t *testing.T) {
	const id = "0b5c1c0e-6f0a-4f7e-9d3a-2a4f1f5f8a11"
	base := Order{ID: id, DropID: "summer-fest", Tier: "general", Quantity: 1, CustomerEmail: "ama@example.com"}

	tests := []struct {
		status, reason string
		polls          bool
		want           []string
	}{
		{"pending", "", true, []string{"Order received", "1 general ticket for you"}},
		{"ticketed", "", false, []string{"You're in", "1 general ticket, confirmed", "ama@example.com"}},
		{"failed", "card declined", false, []string{"did not go through", "Your card was declined", "not been charged"}},
		{"failed", "hold expired", false, []string{"did not go through", "could not be confirmed in time"}},
	}
	for _, tt := range tests {
		t.Run(tt.status+" "+tt.reason, func(t *testing.T) {
			web, b := front(t)
			o := base
			o.Status, o.FailureReason = tt.status, tt.reason
			b.orders[id] = o

			_, fragment, _ := fetch(t, "GET", web.URL+"/orders/"+id+"/status", nil, true)
			status, page, _ := fetch(t, "GET", web.URL+"/orders/"+id, nil, false)

			if status != 200 {
				t.Fatalf("page status = %d", status)
			}
			contains(t, page, "<!doctype html>", "Your order")
			lacks(t, fragment, "<!doctype html>")
			for _, body := range []string{fragment, page} {
				contains(t, body, tt.want...)
				if polls := strings.Contains(body, `hx-trigger="every 1s"`); polls != tt.polls {
					t.Errorf("keeps asking = %v, want %v", polls, tt.polls)
				}
			}
		})
	}
}

func TestWhatIsNotThere(t *testing.T) {
	web, _ := front(t)

	for _, path := range []string{"/drops/winter-fest", "/orders/nope", "/nothing-here"} {
		if status, _, _ := fetch(t, "GET", web.URL+path, nil, false); status != 404 {
			t.Errorf("GET %s = %d, want 404", path, status)
		}
	}
	// A fragment that cannot be built answers with an error, which makes htmx
	// leave the page alone.
	if status, body, _ := fetch(t, "GET", web.URL+"/orders/nope/status", nil, true); status != 404 || strings.Contains(body, "<div") {
		t.Errorf("status fragment for a missing order = %d %q, want a bare 404", status, body)
	}
}

func TestBoxOfficeDown(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	s, err := New(&API{BaseURL: down.URL, Client: &http.Client{Timeout: time.Second}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Routes(mux)
	web := httptest.NewServer(mux)
	defer web.Close()

	status, body, _ := fetch(t, "GET", web.URL+"/", nil, false)
	if status != http.StatusBadGateway {
		t.Errorf("home with the API down = %d, want 502", status)
	}
	contains(t, body, "could not reach the box office")

	// The stylesheet and htmx come from the binary, so they still load.
	for _, file := range []string{"/static/shop.css", "/static/htmx.min.js"} {
		if status, body, _ := fetch(t, "GET", web.URL+file, nil, false); status != 200 || len(body) < 1000 {
			t.Errorf("GET %s = %d with %d bytes, want the file", file, status, len(body))
		}
	}
}

func TestPageEscapesWhatTheCatalogSays(t *testing.T) {
	web, b := front(t)
	b.drop.Name = `<script>alert("x")</script>`

	_, body, _ := fetch(t, "GET", web.URL+"/drops/summer-fest", nil, false)

	lacks(t, body, `<script>alert`)
	contains(t, body, "&lt;script&gt;")
}
