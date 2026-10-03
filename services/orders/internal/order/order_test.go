package order

import (
	"strings"
	"testing"
)

func TestRequestValidate(t *testing.T) {
	valid := Request{DropID: "summer-fest", Tier: "general", Quantity: 2, CustomerEmail: "ama@example.com"}

	tests := []struct {
		name   string
		change func(*Request)
		ok     bool
	}{
		{"valid", func(*Request) {}, true},
		{"largest quantity", func(r *Request) { r.Quantity = MaxQuantity }, true},
		{"missing drop", func(r *Request) { r.DropID = "" }, false},
		{"long drop", func(r *Request) { r.DropID = strings.Repeat("x", 65) }, false},
		{"missing tier", func(r *Request) { r.Tier = "" }, false},
		{"zero quantity", func(r *Request) { r.Quantity = 0 }, false},
		{"negative quantity", func(r *Request) { r.Quantity = -1 }, false},
		{"too many", func(r *Request) { r.Quantity = MaxQuantity + 1 }, false},
		{"missing email", func(r *Request) { r.CustomerEmail = "" }, false},
		{"not an email", func(r *Request) { r.CustomerEmail = "ama" }, false},
		{"email with display name", func(r *Request) { r.CustomerEmail = "Ama <ama@example.com>" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := valid
			tt.change(&req)
			problem := req.Validate()
			if ok := problem == ""; ok != tt.ok {
				t.Errorf("Validate() = %q, want ok=%v", problem, tt.ok)
			}
		})
	}
}
