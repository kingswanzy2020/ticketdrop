package stock

import "testing"

func TestHoldRequestValidate(t *testing.T) {
	valid := HoldRequest{
		OrderID:  "0b5c1c0e-6f0a-4f7e-9d3a-2a4f1f5f8a11",
		DropID:   "summer-fest",
		Tier:     "general",
		Quantity: 2,
	}

	tests := []struct {
		name   string
		change func(*HoldRequest)
		ok     bool
	}{
		{"valid", func(*HoldRequest) {}, true},
		{"order ID is not a UUID", func(r *HoldRequest) { r.OrderID = "42" }, false},
		{"missing order ID", func(r *HoldRequest) { r.OrderID = "" }, false},
		{"missing drop", func(r *HoldRequest) { r.DropID = "" }, false},
		{"missing tier", func(r *HoldRequest) { r.Tier = "" }, false},
		{"zero quantity", func(r *HoldRequest) { r.Quantity = 0 }, false},
		// A negative quantity would add tickets to stock instead of taking them.
		{"negative quantity", func(r *HoldRequest) { r.Quantity = -3 }, false},
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
