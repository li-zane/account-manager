package migrations

import (
	"strings"
	"testing"
)

func TestProviderConnectionMigrationsPersistOnlyEncryptedConfiguration(t *testing.T) {
	base, err := files.ReadFile("000001_mail_platform.sql")
	if err != nil {
		t.Fatal(err)
	}
	hardening, err := files.ReadFile("000003_provider_connections_hardening.sql")
	if err != nil {
		t.Fatal(err)
	}
	baseSQL := strings.ToLower(string(base))
	hardeningSQL := strings.ToLower(string(hardening))

	for _, required := range []string{
		"create table if not exists provider_connections",
		"encrypted_config bytea not null",
		"key_version text not null",
		"unique (provider, name)",
	} {
		if !strings.Contains(baseSQL, required) {
			t.Fatalf("base migration is missing provider connection invariant %q", required)
		}
	}
	for _, plaintextColumn := range []string{"api_token text", "access_token text", "refresh_token text"} {
		if strings.Contains(baseSQL, plaintextColumn) || strings.Contains(hardeningSQL, plaintextColumn) {
			t.Fatalf("provider connection migration contains plaintext secret column %q", plaintextColumn)
		}
	}
	for _, required := range []string{
		"octet_length(encrypted_config) > 0",
		"btrim(key_version) <> ''",
		"version > 0",
		"jsonb_typeof(capabilities) = 'object'",
		"jsonb_typeof(metadata) = 'object'",
	} {
		if !strings.Contains(hardeningSQL, required) {
			t.Fatalf("hardening migration is missing provider connection invariant %q", required)
		}
	}
}
