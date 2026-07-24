package domain

import "errors"

var (
	ErrNotFound      = errors.New("not found")
	ErrConflict      = errors.New("conflict")
	ErrInvalid       = errors.New("invalid input")
	ErrNotConfigured = errors.New("integration not configured")
	ErrKeyExpired    = errors.New("pickup key expired")
	ErrKeyRevoked    = errors.New("pickup key revoked")
	ErrUnauthorized  = errors.New("authentication required")
)
