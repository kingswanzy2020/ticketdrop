package catalog

import (
	"strings"
	"testing"
	"time"
)

func TestRequestValidate(t *testing.T) {
	valid := Request{
		Name:    "Summer Fest",
		Venue:   "Accra Arena",
		OpensAt: time.Date(2027, 6, 1, 10, 0, 0, 0, time.UTC),
		Tiers: []Tier{
			{Tier: "general", PriceCents: 2500, Capacity: 1000},
			{Tier: "vip", PriceCents: 9000, Capacity: 50},
		},
	}

	tests := []struct {
		name   string
		change func(*Request)
		ok     bool
	}{
		{"valid", func(*Request) {}, true},
		{"free tier", func(r *Request) { r.Tiers[0].PriceCents = 0 }, true},
		{"missing name", func(r *Request) { r.Name = "" }, false},
		{"missing venue", func(r *Request) { r.Venue = "" }, false},
		{"missing opening time", func(r *Request) { r.OpensAt = time.Time{} }, false},
		{"no tiers", func(r *Request) { r.Tiers = nil }, false},
		{"unnamed tier", func(r *Request) { r.Tiers[1].Tier = "" }, false},
		{"long tier name", func(r *Request) { r.Tiers[1].Tier = strings.Repeat("x", 65) }, false},
		{"same tier twice", func(r *Request) { r.Tiers[1].Tier = "general" }, false},
		{"empty tier", func(r *Request) { r.Tiers[0].Capacity = 0 }, false},
		{"negative capacity", func(r *Request) { r.Tiers[0].Capacity = -10 }, false},
		{"negative price", func(r *Request) { r.Tiers[0].PriceCents = -1 }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := valid
			req.Tiers = append([]Tier(nil), valid.Tiers...) // each case changes its own copy
			tt.change(&req)
			problem := req.Validate()
			if ok := problem == ""; ok != tt.ok {
				t.Errorf("Validate() = %q, want ok=%v", problem, tt.ok)
			}
		})
	}
}

func TestValidID(t *testing.T) {
	tests := map[string]bool{
		"summer-fest-2027":      true,
		"a":                     true,
		strings.Repeat("a", 64): true,
		"":                      false,
		strings.Repeat("a", 65): false,
		"Summer-Fest":           false, // upper case
		"summer fest":           false,
		"summer/fest":           false, // would change the URL's shape
		"summer_fest":           false,
	}
	for id, want := range tests {
		if got := ValidID(id); got != want {
			t.Errorf("ValidID(%q) = %v, want %v", id, got, want)
		}
	}
}
