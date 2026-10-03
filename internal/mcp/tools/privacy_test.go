package tools_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"tsb-service/internal/mcp/fakeupstream"
	"tsb-service/internal/mcp/upstream"
)

// Customers' full last names, phone numbers and emails must never reach the
// assistant, even when the backend returns them: the fake over-returns them
// with every order and customer.
func TestCustomerContactsNeverReachTools(t *testing.T) {
	h := newHarness(t)
	var outputs []string
	collect := func(name string, args map[string]any) map[string]any {
		t.Helper()
		out := h.call(name, args)
		b, _ := json.Marshal(out)
		outputs = append(outputs, name+": "+string(b))
		return out
	}

	orders := list(collect("list_orders", map[string]any{"limit": 100})["orders"])
	collect("list_orders", map[string]any{"from": "2026-10-02", "to": "2026-10-03"})
	if len(orders) == 0 {
		t.Fatal("no orders listed")
	}
	names := map[string]string{}
	for _, o := range orders {
		m := o.(map[string]any)
		names[str(m["order_id"])] = str(m["customer"])
		collect("get_order", map[string]any{"order_id": str(m["order_id"])})
	}
	collect("get_daily_summary", map[string]any{"date": "2026-10-03"})
	collect("get_customer_stats", map[string]any{})
	collect("get_customer_stats", map[string]any{"query": "marie"})

	if names["o-1"] != "Marie D." || names["o-2"] != "Li W." || names["o-3"] != "Paul M." {
		t.Errorf("customer names = %v, want first name and last-name initial", names)
	}
	o3 := h.call("get_order", map[string]any{"order_id": "o-3"})
	if !strings.Contains(str(o3["note"]), "code porte 1234") || !strings.Contains(str(o3["note"]), "[hidden") {
		t.Errorf("note = %q, want contacts masked and the rest kept", o3["note"])
	}

	all := strings.Join(outputs, "\n")
	f := fakeupstream.New()
	defer f.Close()
	for id, c := range f.Contacts {
		digits := regexp.MustCompile(`\D`).ReplaceAllString(c.Phone, "")
		for _, leak := range []string{c.LastName, strings.ToUpper(c.LastName), c.Email, c.Phone, digits[len(digits)-8:]} {
			if strings.Contains(all, leak) {
				t.Errorf("%s: %q leaked into a tool output", id, leak)
			}
		}
	}
	if regexp.MustCompile(`@[\w.-]+\.\w{2,}`).MatchString(all) {
		t.Errorf("an email address leaked:\n%s", all)
	}
	for _, key := range []string{"phone", "email", "last_name", "lastName"} {
		if strings.Contains(all, `"`+key) {
			t.Errorf("tool output has a %q field", key)
		}
	}
}

// The upstream queries never ask for contact details. lastName is requested
// only to keep its initial (upstream.Initial drops the rest at decode).
func TestDocumentsNeverRequestContacts(t *testing.T) {
	forbidden := regexp.MustCompile(`\b(phoneNumber|phone|email|displayCustomerName|address\s*\{[^}]*\b(phone|email))\b`)
	for op, doc := range upstream.Documents {
		if m := forbidden.FindString(doc); m != "" {
			t.Errorf("%s requests %q", op, m)
		}
	}
}

// The assistant's tool schemas describe no contact field either.
func TestToolSchemasHaveNoContactFields(t *testing.T) {
	h := newHarness(t)
	res, err := h.cs.ListTools(h.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	field := regexp.MustCompile(`"(phone\w*|email\w*|last_?name\w*|customer_phone)"\s*:`)
	for _, tl := range res.Tools {
		b, _ := json.Marshal(tl.OutputSchema)
		if m := field.FindString(string(b)); m != "" {
			t.Errorf("%s output schema has %s", tl.Name, m)
		}
	}
}

func TestInitialKeepsOnlyTheFirstLetter(t *testing.T) {
	for raw, want := range map[string]string{`"Dupont"`: "D", `" de la Tour"`: "D", `"王"`: "王", `"élise"`: "É", `""`: "", `null`: ""} {
		var i upstream.Initial
		if err := json.Unmarshal([]byte(raw), &i); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if string(i) != want {
			t.Errorf("%s: got %q, want %q", raw, i, want)
		}
	}
	for _, c := range []struct{ first, last, want string }{{"Marie", "D", "Marie D."}, {"Marie", "", "Marie"}, {"", "D", "D."}, {" Li ", "W", "Li W."}} {
		if got := upstream.Name(c.first, upstream.Initial(c.last)); got != c.want {
			t.Errorf("Name(%q, %q) = %q, want %q", c.first, c.last, got, c.want)
		}
	}
}
