package domain_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/li-zane/account-manager/backend/internal/domain"
)

func TestProviderConnectionExcludesSecretsFromJSON(t *testing.T) {
	connection := domain.ProviderConnection{
		EncryptedConfig: []byte("sealed-cloudflare-config"),
		KeyVersion:      "sensitive-key-version",
	}

	encoded, err := json.Marshal(connection)
	if err != nil {
		t.Fatal(err)
	}
	for _, protected := range []string{"sealed-cloudflare-config", "sensitive-key-version", "encrypted_config", "key_version"} {
		if strings.Contains(string(encoded), protected) {
			t.Fatalf("provider connection JSON contains protected value %q: %s", protected, encoded)
		}
	}
}
