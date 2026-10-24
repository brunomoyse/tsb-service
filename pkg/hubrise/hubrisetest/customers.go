package hubrisetest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"tsb-service/pkg/hubrise"
)

type storedCustomer struct {
	customer hubrise.Customer
	owner    string
}

// cloneCustomer deep copies a customer.
func cloneCustomer(c hubrise.Customer) hubrise.Customer {
	raw, _ := json.Marshal(c)
	var out hubrise.Customer
	_ = json.Unmarshal(raw, &out)
	return out
}

// viewCustomer is the customer as the client with the token sees it: private_ref is only visible to
// the client that set it.
func viewCustomer(sc *storedCustomer, token string) hubrise.Customer {
	c := cloneCustomer(sc.customer)
	if token != sc.owner {
		c.PrivateRef = ""
	}
	return c
}

// validateCustomer checks the fields of a customer the way the API does.
func validateCustomer(c *hubrise.Customer) *apiError {
	var errs []hubrise.FieldError
	add := func(field, msg string) { errs = append(errs, hubrise.FieldError{Field: field, Message: msg}) }
	if c.Email != "" && !strings.Contains(c.Email, "@") {
		add("email", fmt.Sprintf("%q is not a valid email address", c.Email))
	}
	if c.Country != "" && len(c.Country) != 2 {
		add("country", fmt.Sprintf("%q is not an ISO 3166-1 alpha-2 code", c.Country))
	}
	return errFields(errs)
}

// customerList finds the customer list of the path; the mock serves one.
func customerList(r *http.Request) *apiError {
	if r.PathValue("list") != CustomerListID {
		return errStatus(http.StatusNotFound, "customer list not found")
	}
	return nil
}

func createCustomer(s *Server, c *client, r *http.Request, body []byte, _ http.Header) (int, any, *apiError) {
	if err := customerList(r); err != nil {
		return 0, nil, err
	}
	var in hubrise.Customer
	if err := decodeStrict(body, &in); err != nil {
		return 0, nil, err
	}
	if err := validateCustomer(&in); err != nil {
		return 0, nil, err
	}
	in.ID = s.nextID("cu")
	in.CustomerListID = CustomerListID
	in.Anonymised = false
	in.CreatedAt = s.now()
	sc := &storedCustomer{customer: in, owner: c.token}
	s.customers[in.ID] = sc
	s.customerID = append(s.customerID, in.ID)
	return http.StatusOK, viewCustomer(sc, c.token), nil
}

func listCustomers(s *Server, c *client, r *http.Request, _ []byte, h http.Header) (int, any, *apiError) {
	if err := customerList(r); err != nil {
		return 0, nil, err
	}
	q := r.URL.Query()
	var matches []hubrise.Customer
	for i := len(s.customerID) - 1; i >= 0; i-- { // newest first
		sc := s.customers[s.customerID[i]]
		switch {
		case q.Get("private_ref") != "" && (sc.owner != c.token || sc.customer.PrivateRef != q.Get("private_ref")):
			continue
		case q.Get("email") != "" && !strings.EqualFold(sc.customer.Email, q.Get("email")):
			continue
		}
		matches = append(matches, viewCustomer(sc, c.token))
	}
	return page(h, q.Get("count"), q.Get("cursor"), matches)
}

func patchCustomer(s *Server, c *client, r *http.Request, body []byte, _ http.Header) (int, any, *apiError) {
	if err := customerList(r); err != nil {
		return 0, nil, err
	}
	sc := s.customers[r.PathValue("id")]
	if sc == nil {
		return 0, nil, errStatus(http.StatusNotFound, "customer not found")
	}
	if sc.customer.Anonymised {
		return 0, nil, errField("customer", "an anonymised customer cannot be modified")
	}
	var patch hubrise.Customer
	if err := decodeStrict(body, &patch); err != nil {
		return 0, nil, err
	}
	// Merge on the JSON form: the fields sent replace the stored ones, the rest stays.
	var merged, sent map[string]any
	raw, _ := json.Marshal(sc.customer)
	_ = json.Unmarshal(raw, &merged)
	raw, _ = json.Marshal(patch)
	_ = json.Unmarshal(raw, &sent)
	for k, v := range sent {
		if k == "id" || k == "customer_list_id" || k == "anonymised" || k == "created_at" {
			continue
		}
		merged[k] = v
	}
	raw, _ = json.Marshal(merged)
	var next hubrise.Customer
	_ = json.Unmarshal(raw, &next)
	if err := validateCustomer(&next); err != nil {
		return 0, nil, err
	}
	sc.customer = next
	return http.StatusOK, viewCustomer(sc, c.token), nil
}

func anonymiseCustomer(s *Server, c *client, r *http.Request, _ []byte, _ http.Header) (int, any, *apiError) {
	if err := customerList(r); err != nil {
		return 0, nil, err
	}
	sc := s.customers[r.PathValue("id")]
	if sc == nil {
		return 0, nil, errStatus(http.StatusNotFound, "customer not found")
	}
	// Everything personal goes, the customer stays so that its orders keep a valid reference.
	sc.customer = hubrise.Customer{
		ID: sc.customer.ID, CustomerListID: sc.customer.CustomerListID, Anonymised: true, CreatedAt: sc.customer.CreatedAt,
	}
	return http.StatusOK, viewCustomer(sc, c.token), nil
}
