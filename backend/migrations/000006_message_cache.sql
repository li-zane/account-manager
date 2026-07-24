CREATE TABLE IF NOT EXISTS mailbox_cached_messages (
    id TEXT PRIMARY KEY,
    mailbox_id TEXT NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    external_id TEXT NOT NULL,
    provider_message_id TEXT NOT NULL,
    internet_message_id TEXT NOT NULL DEFAULT '',
    folder TEXT NOT NULL CHECK (folder IN ('INBOX', 'Junk')),
    from_address TEXT NOT NULL DEFAULT '',
    to_addresses TEXT[] NOT NULL DEFAULT '{}',
    cc_addresses TEXT[] NOT NULL DEFAULT '{}',
    recipient_addresses TEXT[] NOT NULL DEFAULT '{}',
    subject TEXT NOT NULL DEFAULT '',
    text_content TEXT NOT NULL DEFAULT '',
    html_content TEXT NOT NULL DEFAULT '',
    received_at TIMESTAMPTZ NOT NULL,
    unread BOOLEAN NOT NULL DEFAULT FALSE,
    headers JSONB NOT NULL DEFAULT '{}'::jsonb,
    discovered_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (mailbox_id, folder, external_id)
);

CREATE INDEX IF NOT EXISTS idx_mailbox_cached_messages_folder_time
    ON mailbox_cached_messages (mailbox_id, folder, received_at DESC, id);

CREATE INDEX IF NOT EXISTS idx_mailbox_cached_messages_recipients
    ON mailbox_cached_messages USING GIN (recipient_addresses);

CREATE TABLE IF NOT EXISTS mailbox_message_sync_states (
    target_id TEXT NOT NULL,
    mailbox_id TEXT NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    alias_id TEXT REFERENCES mailbox_aliases(id) ON DELETE CASCADE,
    folder TEXT NOT NULL CHECK (folder IN ('INBOX', 'Junk')),
    last_message_at TIMESTAMPTZ,
    last_synced_at TIMESTAMPTZ NOT NULL,
    last_error TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (target_id, folder),
    CHECK ((alias_id IS NULL AND target_id = mailbox_id) OR alias_id = target_id)
);

CREATE INDEX IF NOT EXISTS idx_mailbox_message_sync_states_due
    ON mailbox_message_sync_states (last_synced_at, target_id, folder);

INSERT INTO app_settings (key, value)
VALUES ('mailbox.message_probe', '{"enabled": false, "interval_minutes": 10}'::jsonb)
ON CONFLICT (key) DO NOTHING;
