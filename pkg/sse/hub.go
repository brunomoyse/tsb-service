package sse

import (
	"fmt"
	"net/http"
	"sync"
)

// Hub is a globally available SSEHub instance.
var Hub = NewSSEHub()

// SSEHub manages client connections and broadcasts events.
type SSEHub struct {
	clients map[chan string]bool
	mu      sync.Mutex
}

// NewSSEHub initializes a new SSEHub.
func NewSSEHub() *SSEHub {
	return &SSEHub{
		clients: make(map[chan string]bool),
	}
}

// Subscribe registers a new client and returns its channel.
func (hub *SSEHub) Subscribe() chan string {
	hub.mu.Lock()
	defer hub.mu.Unlock()

	ch := make(chan string, 10) // buffered channel
	hub.clients[ch] = true
	return ch
}

// Unsubscribe removes a client channel from the hub.
func (hub *SSEHub) Unsubscribe(ch chan string) {
	hub.mu.Lock()
	defer hub.mu.Unlock()

	delete(hub.clients, ch)
	close(ch)
}

// Broadcast sends a message to all connected clients.
func (hub *SSEHub) Broadcast(msg string) {
	hub.mu.Lock()
	defer hub.mu.Unlock()

	for ch := range hub.clients {
		// Use non-blocking send to avoid blocking if a client is slow.
		select {
		case ch <- msg:
		default:
		}
	}
}

// ServeHTTP implements the http.Handler interface, streaming SSE events.
func (hub *SSEHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Set SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Ensure the writer supports flushing.
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Subscribe the client.
	clientChan := hub.Subscribe()
	defer hub.Unsubscribe(clientChan)

	// Listen for client disconnect.
	notify := w.(http.CloseNotifier).CloseNotify()

	// Stream events.
	for {
		select {
		case <-notify:
			return
		case msg := <-clientChan:
			// Write SSE data format: "data: <message>\n\n"
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		}
	}
}
