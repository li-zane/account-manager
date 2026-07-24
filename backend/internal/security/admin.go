package security

import (
	"crypto/sha256"
	"crypto/subtle"
	"strings"
)

// AdminAuthenticator compares fixed-size digests to avoid leaking token
// length or prefix through comparison timing. The raw configured token is not
// retained after construction.
type AdminAuthenticator struct {
	expected   [sha256.Size]byte
	configured bool
}

func NewAdminAuthenticator(token string) *AdminAuthenticator {
	token = strings.TrimSpace(token)
	if token == "" {
		return &AdminAuthenticator{}
	}
	return &AdminAuthenticator{expected: sha256.Sum256([]byte(token)), configured: true}
}

func (a *AdminAuthenticator) Verify(token string) bool {
	if a == nil || !a.configured || strings.TrimSpace(token) == "" {
		return false
	}
	presented := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return subtle.ConstantTimeCompare(a.expected[:], presented[:]) == 1
}

func (a *AdminAuthenticator) Configured() bool { return a != nil && a.configured }
