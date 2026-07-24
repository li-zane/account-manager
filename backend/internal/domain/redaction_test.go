package domain_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/li-zane/account-manager/backend/internal/domain"
)

func TestPersistedSecretsAreExcludedFromJSON(t *testing.T) {
	value := struct {
		Credential         domain.MailboxCredential         `json:"credential"`
		PlatformCredential domain.PlatformAccountCredential `json:"platform_credential"`
		PickupKey          domain.MailboxPickupKey          `json:"pickup_key"`
		Target             domain.BackupTarget              `json:"target"`
	}{
		Credential:         domain.MailboxCredential{EncryptedSecret: []byte("provider-secret")},
		PlatformCredential: domain.PlatformAccountCredential{EncryptedSecret: []byte("platform-secret")},
		PickupKey:          domain.MailboxPickupKey{Digest: []byte("pickup-digest")},
		Target:             domain.BackupTarget{EncryptedConfig: []byte("storage-secret")},
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"provider-secret", "platform-secret", "pickup-digest", "storage-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("JSON contains protected value %q: %s", secret, encoded)
		}
	}
}
