package hubrisetest

import (
	"fmt"

	"tsb-service/pkg/hubrise"
)

// The control methods play the rest of the world: the EPOS, another sales channel. They act as the
// EPOS client (EPOSToken), so the system under test is told through its callback, if it registered.

// InjectOrder creates an order as the EPOS or another sales channel would, and returns it as stored.
func (s *Server) InjectOrder(o hubrise.Order) (hubrise.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out, err := s.createAs(s.clients[EPOSToken], o)
	if err != nil {
		return hubrise.Order{}, fmt.Errorf("hubrisetest: inject order: %s", errString(err))
	}
	return out, nil
}

// SetOrderStatus changes the status of an order as the EPOS, which is how a restaurant accepts,
// prepares or cancels an order.
func (s *Server) SetOrderStatus(id string, status hubrise.OrderStatus) error {
	_, err := s.PatchOrderAs(EPOSToken, id, hubrise.OrderPatch{Status: status})
	return err
}

// PatchOrderAs applies a PATCH as the client with the token and returns the order as that client
// sees it.
func (s *Server) PatchOrderAs(token, id string, patch hubrise.OrderPatch) (hubrise.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clients[token] == nil {
		return hubrise.Order{}, fmt.Errorf("hubrisetest: unknown client token")
	}
	out, err := s.applyPatch(token, id, &patch)
	if err != nil {
		return hubrise.Order{}, fmt.Errorf("hubrisetest: patch order %s: %s", id, errString(err))
	}
	return out, nil
}

// Order returns an order as stored, private refs included.
func (s *Server) Order(id string) (hubrise.Order, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	so := s.orders[id]
	if so == nil {
		return hubrise.Order{}, false
	}
	return cloneOrder(so.order), true
}

// Orders returns every order as stored, oldest first.
func (s *Server) Orders() []hubrise.Order {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]hubrise.Order, 0, len(s.orderID))
	for _, id := range s.orderID {
		out = append(out, cloneOrder(s.orders[id].order))
	}
	return out
}

// OrderByPrivateRef returns the order the system under test created with the private ref.
func (s *Server) OrderByPrivateRef(privateRef string) (hubrise.Order, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.orderID {
		if so := s.orders[id]; so.owner == Token && so.order.PrivateRef == privateRef {
			return cloneOrder(so.order), true
		}
	}
	return hubrise.Order{}, false
}

// DeleteOrder removes an order, as if it had been deleted at HubRise, so that the system under test
// meets a 404 for an order it believes it created. It reports whether the order existed.
func (s *Server) DeleteOrder(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.orders[id] == nil {
		return false
	}
	delete(s.orders, id)
	for i, known := range s.orderID {
		if known == id {
			s.orderID = append(s.orderID[:i], s.orderID[i+1:]...)
			break
		}
	}
	return true
}

// Customers returns every customer of the customer list as stored, private refs included, oldest
// first.
func (s *Server) Customers() []hubrise.Customer {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]hubrise.Customer, 0, len(s.customerID))
	for _, id := range s.customerID {
		out = append(out, cloneCustomer(s.customers[id].customer))
	}
	return out
}

// Customer returns a customer as stored.
func (s *Server) Customer(id string) (hubrise.Customer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := s.customers[id]
	if sc == nil {
		return hubrise.Customer{}, false
	}
	return cloneCustomer(sc.customer), true
}

// DeleteCustomer removes a customer, as if it had been deleted at HubRise, so that the system under
// test meets a 404 for a customer it believes exists. It reports whether the customer existed.
func (s *Server) DeleteCustomer(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.customers[id] == nil {
		return false
	}
	delete(s.customers, id)
	for i, known := range s.customerID {
		if known == id {
			s.customerID = append(s.customerID[:i], s.customerID[i+1:]...)
			break
		}
	}
	return true
}
