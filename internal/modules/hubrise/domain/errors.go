package domain

import "errors"

var (
	// ErrNotFound is returned when a row does not exist.
	ErrNotFound = errors.New("hubrise: not found")
	// ErrNotConnected means there is no connection row, or it holds no access token.
	ErrNotConnected = errors.New("hubrise: not connected")
	// ErrInvalidLink means a catalog link does not name exactly the local row of its entity type.
	ErrInvalidLink = errors.New("hubrise: invalid catalog link")
)
