ALTER TABLE mailbox_cached_messages
    ADD COLUMN IF NOT EXISTS retrieval_method TEXT NOT NULL DEFAULT '';

ALTER TABLE mailbox_message_sync_states
    ADD COLUMN IF NOT EXISTS retrieval_method TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS cursor TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS uid_validity BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS highest_uid BIGINT NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_mailbox_cached_messages_provider_identity
    ON mailbox_cached_messages (mailbox_id, folder, retrieval_method, provider_message_id);

CREATE TABLE IF NOT EXISTS mailbox_retrieval_capabilities (
    mailbox_id TEXT NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    method TEXT NOT NULL CHECK (method IN ('microsoft_graph', 'imap_oauth')),
    status TEXT NOT NULL CHECK (status IN ('pending', 'available', 'unavailable', 'error')),
    preferred BOOLEAN NOT NULL DEFAULT FALSE,
    token_expires_at TIMESTAMPTZ,
    checked_at TIMESTAMPTZ,
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (mailbox_id, method)
);

CREATE INDEX IF NOT EXISTS idx_mailbox_retrieval_capabilities_pending
    ON mailbox_retrieval_capabilities (status, checked_at, mailbox_id);

INSERT INTO mailbox_retrieval_capabilities (mailbox_id, method, status)
SELECT m.id, methods.method, 'pending'
FROM mailboxes m
JOIN mailbox_credentials c ON c.mailbox_id = m.id
JOIN (VALUES ('microsoft_graph'), ('imap_oauth')) AS methods(method) ON TRUE
WHERE m.provider = 'microsoft'
  AND c.kind IN ('microsoft_dual_token', 'microsoft_graph_oauth', 'microsoft_imap_oauth')
ON CONFLICT (mailbox_id, method) DO NOTHING;
