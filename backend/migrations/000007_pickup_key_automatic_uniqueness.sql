-- Preserve one active encrypted automatic key per mailbox across server
-- instances. Keep the newest key if an earlier deployment created duplicates.
WITH ranked_automatic_keys AS (
    SELECT id,
           row_number() OVER (PARTITION BY mailbox_id ORDER BY created_at DESC, id DESC) AS position
    FROM mailbox_pickup_keys
    WHERE label = 'automatic'
      AND revoked_at IS NULL
      AND encrypted_token IS NOT NULL
)
UPDATE mailbox_pickup_keys
SET revoked_at = now()
WHERE id IN (
    SELECT id FROM ranked_automatic_keys WHERE position > 1
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_mailbox_pickup_keys_one_active_automatic
    ON mailbox_pickup_keys (mailbox_id)
    WHERE label = 'automatic'
      AND revoked_at IS NULL
      AND encrypted_token IS NOT NULL;
