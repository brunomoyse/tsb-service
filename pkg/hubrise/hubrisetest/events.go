package hubrisetest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"time"

	"tsb-service/pkg/hubrise"
)

// emit queues an event for every client whose callback asks for resource.eventType, except source:
// HubRise never tells a client about its own changes. fill sets the ids and states, per recipient
// (the state of an order depends on who reads it).
func (s *Server) emit(source, resource, eventType string, fill func(e *hubrise.Event, recipient string)) {
	tokens := make([]string, 0, len(s.clients))
	for t := range s.clients {
		tokens = append(tokens, t)
	}
	sort.Strings(tokens)
	for _, token := range tokens {
		c := s.clients[token]
		if token == source || c.callback == nil || !slices.Contains(c.callback.Events[resource], eventType) {
			continue
		}
		e := hubrise.Event{
			ID: s.nextID("ev"), ResourceType: resource, EventType: eventType, CreatedAt: s.now(),
			AccountID: AccountID, LocationID: LocationID,
		}
		fill(&e, token)
		c.events = append(c.events, e)
	}
}

// ---- callback registration ----

func getCallback(_ *Server, c *client, _ *http.Request, _ []byte, _ http.Header) (int, any, *apiError) {
	if c.callback == nil {
		return http.StatusOK, hubrise.Callback{}, nil
	}
	return http.StatusOK, *c.callback, nil
}

func postCallback(_ *Server, c *client, _ *http.Request, body []byte, _ http.Header) (int, any, *apiError) {
	var cb hubrise.Callback
	if err := decodeStrict(body, &cb); err != nil {
		return 0, nil, err
	}
	for resource, types := range cb.Events {
		for _, t := range types {
			if !hubrise.ValidCallbackEvent(resource, t) {
				return 0, nil, errField("events", fmt.Sprintf("%s.%s is not a known event", resource, t))
			}
		}
	}
	if cb.URL != "" {
		if err := checkHTTPURL(cb.URL); err != nil {
			return 0, nil, errField("url", "must be an http(s) URL or null")
		}
	}
	c.callback = &cb
	return http.StatusOK, cb, nil
}

func deleteCallback(_ *Server, c *client, _ *http.Request, _ []byte, _ http.Header) (int, any, *apiError) {
	c.callback = nil
	return http.StatusNoContent, nil, nil
}

// ---- events ----

// stripped is an event as GET /callback/events lists it: without state fields.
func stripped(e hubrise.Event) hubrise.Event {
	e.PreviousState, e.NewState, e.StateChange = nil, nil, nil
	return e
}

func listEvents(_ *Server, c *client, r *http.Request, _ []byte, h http.Header) (int, any, *apiError) {
	q := r.URL.Query()
	events := make([]hubrise.Event, 0, len(c.events))
	for _, e := range c.events {
		events = append(events, stripped(e))
	}
	return page(h, q.Get("count"), q.Get("cursor"), events)
}

func ackEvent(_ *Server, c *client, r *http.Request, _ []byte, _ http.Header) (int, any, *apiError) {
	id := r.PathValue("id")
	i := slices.IndexFunc(c.events, func(e hubrise.Event) bool { return e.ID == id })
	if i < 0 {
		return 0, nil, errStatus(http.StatusNotFound, "event not found")
	}
	c.events = slices.Delete(c.events, i, i+1)
	return http.StatusNoContent, nil, nil
}

// Events returns the events queued for the client with the access token (Token or EPOSToken), with
// their state fields, oldest first.
func (s *Server) Events(token string) []hubrise.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.clients[token]
	if c == nil {
		return nil
	}
	return slices.Clone(c.events)
}

// Callback returns the callback registered by the client with the token, nil when none.
func (s *Server) Callback(token string) *hubrise.Callback {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.clients[token]
	if c == nil || c.callback == nil {
		return nil
	}
	cp := *c.callback
	return &cp
}

// Dispatcher posts queued events to a registered callback URL, signed like HubRise signs them.
type Dispatcher struct {
	// Token selects the client whose events are delivered; the system under test by default.
	Token string
	// Secret is the OAuth client secret the signature is keyed with.
	Secret string
	// Client is the HTTP client posting the events (http.DefaultClient by default).
	Client *http.Client
	// BadSignature corrupts the signature, to test the rejection of a forged callback.
	BadSignature bool
	// Keep leaves the events queued even when delivered, which is how a redelivery after a lost
	// response looks to the receiver.
	Keep bool
	// Timeout bounds one delivery (HubRise waits 20 s).
	Timeout time.Duration
}

// Delivery is the outcome of the delivery of one event.
type Delivery struct {
	Event hubrise.Event
	// Status is the receiver's HTTP status, 0 when the request failed.
	Status int
	Err    error
}

// Acknowledged reports whether HubRise would consider the event delivered: a status 200 to 499.
func (d Delivery) Acknowledged() bool { return d.Err == nil && d.Status >= 200 && d.Status < 500 }

// Dispatch posts every queued event of the client to the callback URL it registered, oldest first.
// An event the receiver acknowledged (status 200 to 499) leaves the queue; one it did not (5xx, a
// transport error) stays and is sent again by the next Dispatch, as HubRise retries. With no
// callback URL registered nothing is sent.
func (s *Server) Dispatch(ctx context.Context, d Dispatcher) []Delivery {
	token := d.Token
	if token == "" {
		token = Token
	}
	hc := d.Client
	if hc == nil {
		hc = http.DefaultClient
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}

	s.mu.Lock()
	c := s.clients[token]
	if c == nil || c.callback == nil || c.callback.URL == "" {
		s.mu.Unlock()
		return nil
	}
	url := c.callback.URL
	queued := slices.Clone(c.events)
	s.mu.Unlock()

	var out []Delivery
	for _, e := range queued {
		del := Delivery{Event: e}
		body, _ := json.Marshal(e)
		sig := hubrise.Sign(d.Secret, body)
		if d.BadSignature {
			sig = hubrise.Sign(d.Secret+"-forged", body)
		}
		rctx, cancel := context.WithTimeout(ctx, timeout)
		req, err := http.NewRequestWithContext(rctx, http.MethodPost, url, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(hubrise.SignatureHeader, sig)
			var resp *http.Response
			if resp, err = hc.Do(req); err == nil {
				del.Status = resp.StatusCode
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}
		cancel()
		del.Err = err
		out = append(out, del)
		if del.Acknowledged() && !d.Keep {
			s.mu.Lock()
			c.events = slices.DeleteFunc(c.events, func(x hubrise.Event) bool { return x.ID == e.ID })
			s.mu.Unlock()
		}
	}
	return out
}
