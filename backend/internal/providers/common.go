package providers

import (
	"fmt"
	"net/mail"
	"strings"

	"github.com/li-zane/account-manager/backend/internal/domain"
)

func normalizeAddress(address string) (string, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", fmt.Errorf("%w: email address is required", domain.ErrInvalid)
	}
	parsed, err := mail.ParseAddress(address)
	if err != nil || !strings.Contains(parsed.Address, "@") {
		return "", fmt.Errorf("%w: invalid email address", domain.ErrInvalid)
	}
	parts := strings.SplitN(parsed.Address, "@", 2)
	local := strings.TrimSpace(parts[0])
	domainPart := strings.ToLower(strings.TrimSpace(parts[1]))
	if local == "" || domainPart == "" {
		return "", fmt.Errorf("%w: invalid email address", domain.ErrInvalid)
	}
	return strings.ToLower(local) + "@" + domainPart, nil
}

func notConfigured(provider domain.ProviderKey, operation string) error {
	return fmt.Errorf("%w: %s %s adapter has no external credentials", domain.ErrNotConfigured, provider, operation)
}
