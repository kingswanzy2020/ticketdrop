// Package shop is the web service: the pages a customer sees.
//
// Pages are rendered here, on the server, and sent as HTML. The little that
// changes without a reload (tickets left, the moment a drop opens, an order's
// progress) is fetched by htmx as a fragment of HTML and swapped into the
// page. There is no JavaScript of our own and nothing to build.
package shop

import (
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

// htmx.min.js is htmx 2.0.11, from https://unpkg.com/htmx.org@2.0.11/dist/htmx.min.js
// (sha256 d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717).
// It is served from the binary, so no page depends on a CDN being up.
//
//go:embed static
var staticFS embed.FS

// MaxQuantity is the most tickets the order form offers. The API has the
// final say; this only keeps the form from offering what it would refuse.
const MaxQuantity = 8

const (
	statusOpen     = "open"
	statusPending  = "pending"
	statusTicketed = "ticketed"
)

// Shop serves the shopfront.
type Shop struct {
	API *API
	Log *slog.Logger

	pages     map[string]*template.Template
	fragments *template.Template
}

// New returns a Shop with its templates parsed. A template that does not
// parse stops the service at start, not on a customer's first visit.
func New(api *API, log *slog.Logger) (*Shop, error) {
	funcs := template.FuncMap{
		"when":    func(t time.Time) string { return t.UTC().Format("Mon 2 Jan 2006 at 15:04 UTC") },
		"rfc3339": func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
	}
	s := &Shop{API: api, Log: log, pages: map[string]*template.Template{}}

	var err error
	s.fragments, err = template.New("fragments").Funcs(funcs).ParseFS(templateFS, "templates/fragments.html")
	if err != nil {
		return nil, fmt.Errorf("parse fragments: %w", err)
	}
	for _, page := range []string{"home", "drop", "order", "notice"} {
		s.pages[page], err = template.New(page).Funcs(funcs).ParseFS(templateFS,
			"templates/layout.html", "templates/fragments.html", "templates/"+page+".html")
		if err != nil {
			return nil, fmt.Errorf("parse page %s: %w", page, err)
		}
	}
	return s, nil
}

// Routes registers the shopfront on mux.
func (s *Shop) Routes(mux *http.ServeMux) {
	// Pages.
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /drops/{drop}", s.drop)
	mux.HandleFunc("GET /orders/{id}", s.order)
	mux.HandleFunc("POST /drops/{drop}/orders", s.placeOrder)

	// Fragments: pieces of a page that htmx fetches again on a timer.
	mux.HandleFunc("GET /drops/{drop}/sale", s.sale)
	mux.HandleFunc("GET /drops/{drop}/tiers", s.tiers)
	mux.HandleFunc("GET /orders/{id}/status", s.orderStatus)

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	files := http.StripPrefix("/static/", http.FileServerFS(static))
	mux.HandleFunc("GET /static/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		files.ServeHTTP(w, r)
	})
}

// --- what the templates are given ------------------------------------------------

type tierView struct {
	Name      string
	Price     string
	Available int
	SoldOut   bool
}

type dropView struct {
	Drop Drop
	// Open is true once the drop can be ordered: catalog says it is open and
	// inventory has its tickets.
	Open        bool
	Tiers       []tierView
	MaxQuantity int
}

type orderView struct {
	Order    Order
	Pending  bool
	Ticketed bool
	Tickets  string
	// Why says, for a failed order, what went wrong.
	Why string
}

type noticeView struct {
	Title string
	Text  string
	// Back is where the customer can go from here.
	Back string
}

// price writes an amount of cents the way a price tag would.
func price(cents int) string {
	if cents == 0 {
		return "Free"
	}
	return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
}

// tickets writes a number of tickets in words a customer would use.
func tickets(n int, tier string) string {
	if n == 1 {
		return "1 " + tier + " ticket"
	}
	return fmt.Sprintf("%d %s tickets", n, tier)
}

func viewOrder(o Order) orderView {
	v := orderView{
		Order:    o,
		Pending:  o.Status == statusPending,
		Ticketed: o.Status == statusTicketed,
		Tickets:  tickets(o.Quantity, o.Tier),
	}
	switch o.FailureReason {
	case "card declined":
		v.Why = "Your card was declined."
	case "hold expired":
		v.Why = "Your payment could not be confirmed in time."
	default:
		v.Why = "Something went wrong on our side."
	}
	return v
}

// loadDrop reads a drop and, when it is on sale, how many tickets are left.
func (s *Shop) loadDrop(r *http.Request, id string) (dropView, error) {
	d, err := s.API.Drop(r.Context(), id)
	if err != nil {
		return dropView{}, err
	}
	v := dropView{Drop: d, MaxQuantity: MaxQuantity}

	left := map[string]int{}
	if d.Status == statusOpen {
		stock, err := s.API.Availability(r.Context(), id)
		switch {
		case err == nil:
			v.Open = true
			for _, t := range stock {
				left[t.Tier] = t.Available
			}
		case errors.Is(err, ErrNotFound):
			// Catalog has opened the drop and inventory has not heard yet.
			// That lasts a fraction of a second; the page keeps waiting.
		default:
			return dropView{}, err
		}
	}
	for _, t := range d.Tiers {
		v.Tiers = append(v.Tiers, tierView{
			Name:      t.Tier,
			Price:     price(t.PriceCents),
			Available: left[t.Tier],
			SoldOut:   v.Open && left[t.Tier] == 0,
		})
	}
	return v, nil
}

// --- pages ----------------------------------------------------------------------

func (s *Shop) home(w http.ResponseWriter, r *http.Request) {
	drops, err := s.API.Drops(r.Context())
	if err != nil {
		s.unreachable(w, r, err)
		return
	}
	s.page(w, r, http.StatusOK, "home", map[string]any{"Drops": drops})
}

func (s *Shop) drop(w http.ResponseWriter, r *http.Request) {
	v, err := s.loadDrop(r, r.PathValue("drop"))
	if errors.Is(err, ErrNotFound) {
		s.notice(w, r, http.StatusNotFound, noticeView{Title: "No such drop", Text: "There is nothing on sale at this address.", Back: "/"})
		return
	}
	if err != nil {
		s.unreachable(w, r, err)
		return
	}
	s.page(w, r, http.StatusOK, "drop", v)
}

func (s *Shop) order(w http.ResponseWriter, r *http.Request) {
	o, err := s.API.Order(r.Context(), r.PathValue("id"))
	if errors.Is(err, ErrNotFound) {
		s.notice(w, r, http.StatusNotFound, noticeView{Title: "No such order", Text: "There is no order at this address.", Back: "/"})
		return
	}
	if err != nil {
		s.unreachable(w, r, err)
		return
	}
	s.page(w, r, http.StatusOK, "order", viewOrder(o))
}

func (s *Shop) placeOrder(w http.ResponseWriter, r *http.Request) {
	drop := r.PathValue("drop")
	back := "/drops/" + drop

	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		s.notice(w, r, http.StatusBadRequest, noticeView{Title: "That order could not be read", Text: "Go back and try again.", Back: back})
		return
	}
	quantity, _ := strconv.Atoi(r.PostFormValue("quantity"))
	req := OrderRequest{
		DropID:        drop,
		Tier:          r.PostFormValue("tier"),
		Quantity:      quantity,
		CustomerEmail: strings.TrimSpace(r.PostFormValue("email")),
	}

	// The API checks all of this again. Checking here first is only so that
	// the customer gets an answer in their own words.
	switch addr, err := mail.ParseAddress(req.CustomerEmail); {
	case req.Tier == "":
		s.notice(w, r, http.StatusBadRequest, noticeView{Title: "Choose a ticket", Text: "Pick which kind of ticket you want.", Back: back})
		return
	case quantity < 1 || quantity > MaxQuantity:
		s.notice(w, r, http.StatusBadRequest, noticeView{Title: "Choose how many", Text: fmt.Sprintf("You can buy between 1 and %d tickets in one order.", MaxQuantity), Back: back})
		return
	case err != nil || addr.Address != req.CustomerEmail:
		s.notice(w, r, http.StatusBadRequest, noticeView{Title: "Check your email address", Text: "We need a working email address to send your tickets to.", Back: back})
		return
	}

	o, err := s.API.PlaceOrder(r.Context(), req)
	var rejected *Rejected
	switch {
	case err == nil:
		if htmx(r) {
			s.fragment(w, r, "order", viewOrder(o))
			return
		}
		// Without htmx the form is an ordinary post. Redirecting to the order's
		// own page means a reload shows the order, and does not buy again.
		http.Redirect(w, r, "/orders/"+o.ID, http.StatusSeeOther)
	case errors.Is(err, ErrSoldOut):
		s.notice(w, r, http.StatusConflict, noticeView{
			Title: "Sold out",
			Text:  fmt.Sprintf("Not enough %s tickets are left for this order. Try fewer, or another kind of ticket.", req.Tier),
			Back:  back,
		})
	case errors.Is(err, ErrNotFound):
		s.notice(w, r, http.StatusNotFound, noticeView{Title: "Not on sale", Text: "These tickets are not on sale.", Back: back})
	case errors.As(err, &rejected):
		s.notice(w, r, http.StatusBadRequest, noticeView{Title: "That order could not be placed", Text: "The box office refused it: " + rejected.Message + ".", Back: back})
	default:
		// The request may have reached the API before the failure, so the
		// customer is not told that nothing happened.
		s.Log.ErrorContext(r.Context(), "place order", "drop_id", drop, "error", err)
		s.notice(w, r, http.StatusBadGateway, noticeView{
			Title: "We could not reach the box office",
			Text:  "Your order may not have been placed. Check your email before trying again.",
			Back:  back,
		})
	}
}

// --- fragments ------------------------------------------------------------------

// A fragment that cannot be built answers with an error status and no body.
// htmx leaves the page as it is and asks again on its next tick, so a brief
// outage shows as numbers that stop moving, not as a broken page.

func (s *Shop) sale(w http.ResponseWriter, r *http.Request) {
	v, err := s.loadDrop(r, r.PathValue("drop"))
	if err != nil {
		s.fragmentFailed(w, r, err)
		return
	}
	s.fragment(w, r, "sale", v)
}

func (s *Shop) tiers(w http.ResponseWriter, r *http.Request) {
	v, err := s.loadDrop(r, r.PathValue("drop"))
	if err != nil {
		s.fragmentFailed(w, r, err)
		return
	}
	s.fragment(w, r, "tiers", v)
}

func (s *Shop) orderStatus(w http.ResponseWriter, r *http.Request) {
	o, err := s.API.Order(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fragmentFailed(w, r, err)
		return
	}
	s.fragment(w, r, "order", viewOrder(o))
}

// --- rendering ------------------------------------------------------------------

// htmx reports whether the request was made by htmx, which wants a fragment
// back, and not by the browser itself, which wants a page.
func htmx(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

func (s *Shop) page(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	s.render(w, r, status, s.pages[name], "layout", data)
}

func (s *Shop) fragment(w http.ResponseWriter, r *http.Request, name string, data any) {
	s.render(w, r, http.StatusOK, s.fragments, name, data)
}

func (s *Shop) render(w http.ResponseWriter, r *http.Request, status int, t *template.Template, name string, data any) {
	// Rendered into memory first: a template that fails halfway must not
	// leave half a page already sent with a 200.
	var out strings.Builder
	if err := t.ExecuteTemplate(&out, name, data); err != nil {
		s.Log.ErrorContext(r.Context(), "render", "template", name, "error", err)
		http.Error(w, "this page could not be shown", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Every page shows live numbers or one customer's order.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write([]byte(out.String()))
}

// notice tells the customer something went differently than they asked. To
// htmx it is a fragment with status 200, because htmx discards the body of an
// error response and the customer would see nothing. To a browser it is a
// page with the real status.
func (s *Shop) notice(w http.ResponseWriter, r *http.Request, status int, v noticeView) {
	if htmx(r) {
		s.fragment(w, r, "notice", v)
		return
	}
	s.page(w, r, status, "notice", v)
}

func (s *Shop) unreachable(w http.ResponseWriter, r *http.Request, err error) {
	s.Log.ErrorContext(r.Context(), "api request failed", "path", r.URL.Path, "error", err)
	s.notice(w, r, http.StatusBadGateway, noticeView{
		Title: "We could not reach the box office",
		Text:  "Nothing is wrong on your side. Try again in a moment.",
		Back:  "/",
	})
}

func (s *Shop) fragmentFailed(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	s.Log.WarnContext(r.Context(), "api request failed", "path", r.URL.Path, "error", err)
	http.Error(w, "box office unreachable", http.StatusBadGateway)
}
