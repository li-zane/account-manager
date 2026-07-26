ALTER TABLE mailbox_cached_messages
    ADD COLUMN IF NOT EXISTS viewed_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_mailbox_cached_messages_unviewed
    ON mailbox_cached_messages (mailbox_id, folder, received_at DESC)
    WHERE unread = TRUE AND viewed_at IS NULL;
