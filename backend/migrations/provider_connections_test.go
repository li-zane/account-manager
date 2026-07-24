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

func TestOutlookFourPartMigrationDefaultsToDualToken(t *testing.T) {
	script, err := files.ReadFile("000004_outlook_dual_token_format.sql")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.ToLower(string(script))
	for _, required := range []string{
		"fmt_builtin_outlook4",
		"credential_kind",
		"microsoft_dual_token",
		"builtin = true",
	} {
		if !strings.Contains(normalized, required) {
			t.Fatalf("Outlook format migration is missing %q", required)
		}
	}
}

func TestPickupKeyExportMigrationUsesCiphertextAndPairedKeyVersion(t *testing.T) {
	script, err := files.ReadFile("000005_pickup_key_export.sql")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.ToLower(string(script))
	for _, required := range []string{
		"encrypted_token bytea",
		"key_version text",
		"mailbox_pickup_keys_export_secret_pair",
		"octet_length(encrypted_token) > 0",
		"btrim(key_version) <> ''",
	} {
		if !strings.Contains(normalized, required) {
			t.Fatalf("pickup-key export migration is missing %q", required)
		}
	}
	if strings.Contains(normalized, "plaintext") || strings.Contains(normalized, "raw_token") {
		t.Fatal("pickup-key export migration introduced a plaintext token column")
	}
}

func TestMessageCacheMigrationSeparatesTargetsAndFolders(t *testing.T) {
	script, err := files.ReadFile("000006_message_cache.sql")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.ToLower(string(script))
	for _, required := range []string{
		"create table if not exists mailbox_cached_messages",
		"unique (mailbox_id, folder, external_id)",
		"recipient_addresses text[]",
		"using gin (recipient_addresses)",
		"create table if not exists mailbox_message_sync_states",
		"primary key (target_id, folder)",
		"alias_id text references mailbox_aliases",
		"mailbox.message_probe",
		`"enabled": false`,
	} {
		if !strings.Contains(normalized, required) {
			t.Fatalf("message-cache migration is missing %q", required)
		}
	}
}

func TestAutomaticPickupKeyMigrationEnforcesCrossInstanceUniqueness(t *testing.T) {
	script, err := files.ReadFile("000007_pickup_key_automatic_uniqueness.sql")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.ToLower(string(script))
	for _, required := range []string{
		"row_number() over (partition by mailbox_id",
		"set revoked_at = now()",
		"create unique index if not exists idx_mailbox_pickup_keys_one_active_automatic",
		"label = 'automatic'",
		"encrypted_token is not null",
	} {
		if !strings.Contains(normalized, required) {
			t.Fatalf("automatic pickup-key migration is missing %q", required)
		}
	}
}
