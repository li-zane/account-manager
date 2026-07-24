package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"regexp"
	"strings"
)

var namespacePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NormalizeNamespace validates provider and platform namespaces used in IDs.
func NormalizeNamespace(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if !namespacePattern.MatchString(value) {
		return "", fmt.Errorf("%w: namespace must match %s", ErrInvalid, namespacePattern)
	}
	return value, nil
}

// NewMailboxID creates a deterministic provider-scoped identifier. The 160-bit
// digest and database uniqueness constraints make collision handling explicit.
func NewMailboxID(provider, normalizedAddress string) (string, error) {
	namespace, err := NormalizeNamespace(provider)
	if err != nil {
		return "", err
	}
	address := strings.ToLower(strings.TrimSpace(normalizedAddress))
	if address == "" {
		return "", fmt.Errorf("%w: address is required", ErrInvalid)
	}
	digest := sha256.Sum256([]byte(namespace + "\x00" + address))
	return "mbx_" + namespace + "_" + strings.ToLower(idEncoding.EncodeToString(digest[:20])), nil
}

// NewPlatformAccountID creates a platform-scoped identifier from a stable
// external account reference. Call NewRandomID when no stable reference exists.
func NewPlatformAccountID(platform, externalReference string) (string, error) {
	namespace, err := NormalizeNamespace(platform)
	if err != nil {
		return "", err
	}
	reference := strings.TrimSpace(externalReference)
	if reference == "" {
		return "", fmt.Errorf("%w: external account reference is required", ErrInvalid)
	}
	digest := sha256.Sum256([]byte(namespace + "\x00" + reference))
	return "acct_" + namespace + "_" + strings.ToLower(idEncoding.EncodeToString(digest[:20])), nil
}

func NewRandomID(prefix string) (string, error) {
	prefix, err := NormalizeNamespace(prefix)
	if err != nil {
		return "", err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return prefix + "_" + strings.ToLower(idEncoding.EncodeToString(random)), nil
}
