-- Mailbox detail and custom import/export support.

ALTER TABLE mailbox_credentials
    ADD COLUMN IF NOT EXISTS client_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS refresh_status TEXT NOT NULL DEFAULT 'unknown',
    ADD COLUMN IF NOT EXISTS last_refreshed_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS last_refresh_error TEXT NOT NULL DEFAULT '';

ALTER TABLE mailbox_formats
    ADD COLUMN IF NOT EXISTS has_header BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'delimited',
    ADD COLUMN IF NOT EXISTS template TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS parser_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS builtin BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS platform_account_credentials (
    id TEXT PRIMARY KEY,
    platform_account_id TEXT NOT NULL REFERENCES platform_accounts(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    encrypted_secret BYTEA NOT NULL,
    key_version TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (platform_account_id, kind)
);

CREATE INDEX IF NOT EXISTS idx_mailbox_formats_enabled_direction
    ON mailbox_formats (enabled, direction, name);

CREATE INDEX IF NOT EXISTS idx_platform_accounts_mailbox_created
    ON platform_accounts (mailbox_id, created_at DESC)
    WHERE mailbox_id IS NOT NULL;

INSERT INTO mailbox_formats
    (id, name, kind, direction, delimiter, fields, provider, has_header, template, parser_config, builtin, enabled)
VALUES
    (
        'fmt_builtin_outlook4', 'Outlook 4-part', 'delimited', 'both', '----',
        '[{"column":"email","target":"address","required":true},{"column":"password","target":"password","sensitive":true},{"column":"client_id","target":"client_id"},{"column":"refresh_token","target":"refresh_token","sensitive":true}]'::jsonb,
        'microsoft', FALSE, '', '{}'::jsonb, TRUE, TRUE
    ),
    (
        'fmt_builtin_registered6', 'Registered 6-part', 'delimited', 'both', '----',
        '[{"column":"email","target":"address","required":true},{"column":"gpt_password","target":"platform_account_password","sensitive":true},{"column":"password","target":"password","sensitive":true},{"column":"client_id","target":"client_id"},{"column":"refresh_token","target":"refresh_token","sensitive":true},{"column":"access_token","target":"platform_access_token","sensitive":true}]'::jsonb,
        'microsoft', FALSE, '', '{"platform":"chatgpt"}'::jsonb, TRUE, TRUE
    )
ON CONFLICT (id) DO NOTHING;

INSERT INTO schema_migrations (version)
VALUES (2)
ON CONFLICT (version) DO NOTHING;
