package controllers

import (
	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
)

// Handler struct to hold the Mollie client
type Handler struct {
	client *mollie.Client
}

// NewHandler returns a new handler with the Mollie client
func NewHandler(client *mollie.Client) *Handler {
	return &Handler{client: client}
}
