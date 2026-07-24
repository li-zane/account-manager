package providers

import (
	"net/mail"
	"regexp"
	"strings"

	"github.com/li-zane/account-manager/backend/internal/domain"
)

var recipientAddressPattern = regexp.MustCompile(`(?i)[a-z0-9.!#$%&'*+/=?^_{|}~-]+@[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+`)

var recipientHeaderNames = map[string]struct{}{
	"to":                 {},
	"cc":                 {},
	"delivered-to":       {},
	"x-original-to":      {},
	"original-recipient": {},
	"final-recipient":    {},
	"envelope-to":        {},
	"x-envelope-to":      {},
	"x-forwarded-to":     {},
	"x-ms-exchange-organization-originalenveloperecipients": {},
}

// ExtractRecipientAddresses returns normalized, unique recipient addresses
// from structured To/Cc values and envelope headers. Repeated headers are
// intentionally retained by Message.Headers and all their values are scanned.
func ExtractRecipientAddresses(to, cc []string, headers map[string][]string) []string {
	result := make([]string, 0, len(to)+len(cc))
	seen := make(map[string]struct{})
	appendValues := func(values []string) {
		for _, value := range values {
			for _, address := range extractAddresses(value) {
				if _, exists := seen[address]; exists {
					continue
				}
				seen[address] = struct{}{}
				result = append(result, address)
			}
		}
	}

	appendValues(to)
	appendValues(cc)
	for name, values := range headers {
		if _, ok := recipientHeaderNames[strings.ToLower(strings.TrimSpace(name))]; ok {
			appendValues(values)
		}
	}
	return result
}

// MessageMatchesRecipient performs an exact normalized-address match. It does
// not infer a recipient from message bodies or subjects, which keeps split
// mailbox routing fail-closed when upstream envelope evidence is absent.
func MessageMatchesRecipient(message domain.Message, recipient string) bool {
	normalized, err := normalizeAddress(recipient)
	if err != nil {
		return false
	}
	candidates := message.RecipientAddresses
	if len(candidates) == 0 {
		candidates = ExtractRecipientAddresses(message.To, message.Cc, message.Headers)
	}
	for _, candidate := range candidates {
		candidate, err = normalizeAddress(candidate)
		if err == nil && candidate == normalized {
			return true
		}
	}
	return false
}

func extractAddresses(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	result := make([]string, 0, 1)
	seen := make(map[string]struct{})
	appendAddress := func(address string) {
		normalized, err := normalizeAddress(address)
		if err != nil {
			return
		}
		if _, exists := seen[normalized]; exists {
			return
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	if parsed, err := mail.ParseAddressList(value); err == nil {
		for _, address := range parsed {
			appendAddress(address.Address)
		}
	}
	for _, address := range recipientAddressPattern.FindAllString(value, -1) {
		appendAddress(address)
	}
	return result
}
