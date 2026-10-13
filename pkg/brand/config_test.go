package brand

import "testing"

func TestDeliveryEnabledFromEnv(t *testing.T) {
	cases := []struct {
		env  string
		want bool
	}{
		{"", true}, {"true", true}, {"1", true}, {"false", false}, {"0", false}, {"nonsense", true},
	}
	for _, c := range cases {
		t.Setenv("RESTAURANT_DELIVERY_ENABLED", c.env)
		if got := NewFromEnv().DeliveryEnabled; got != c.want {
			t.Errorf("RESTAURANT_DELIVERY_ENABLED=%q: DeliveryEnabled = %v, want %v", c.env, got, c.want)
		}
	}
}

func TestInvoicesEnabledFromEnv(t *testing.T) {
	cases := []struct {
		env  string
		want bool
	}{
		{"", true}, {"true", true}, {"false", false}, {"0", false}, {"nonsense", true},
	}
	for _, c := range cases {
		t.Setenv("RESTAURANT_INVOICES_ENABLED", c.env)
		if got := NewFromEnv().InvoicesEnabled; got != c.want {
			t.Errorf("RESTAURANT_INVOICES_ENABLED=%q: InvoicesEnabled = %v, want %v", c.env, got, c.want)
		}
	}
}
