-- Keep lookup one-way while allowing explicit administrator exports to recover
-- an automatically issued platform pickup key through the secret broker.
ALTER TABLE mailbox_pickup_keys
    ADD COLUMN IF NOT EXISTS encrypted_token BYTEA,
    ADD COLUMN IF NOT EXISTS key_version TEXT;

ALTER TABLE mailbox_pickup_keys
    DROP CONSTRAINT IF EXISTS mailbox_pickup_keys_export_secret_pair;

ALTER TABLE mailbox_pickup_keys
    ADD CONSTRAINT mailbox_pickup_keys_export_secret_pair CHECK (
        (encrypted_token IS NULL AND key_version IS NULL)
        OR (octet_length(encrypted_token) > 0 AND btrim(key_version) <> '')
    );
