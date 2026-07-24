-- Provider connection invariants are enforced at the persistence boundary.
-- NOT VALID keeps upgrades compatible with any legacy rows while applying the
-- constraints to all new and updated records.

ALTER TABLE provider_connections
    ADD CONSTRAINT provider_connections_provider_present
        CHECK (btrim(provider) <> '') NOT VALID,
    ADD CONSTRAINT provider_connections_name_present
        CHECK (btrim(name) <> '') NOT VALID,
    ADD CONSTRAINT provider_connections_encrypted_config_present
        CHECK (octet_length(encrypted_config) > 0) NOT VALID,
    ADD CONSTRAINT provider_connections_key_version_present
        CHECK (btrim(key_version) <> '') NOT VALID,
    ADD CONSTRAINT provider_connections_version_positive
        CHECK (version > 0) NOT VALID,
    ADD CONSTRAINT provider_connections_capabilities_object
        CHECK (jsonb_typeof(capabilities) = 'object') NOT VALID,
    ADD CONSTRAINT provider_connections_metadata_object
        CHECK (jsonb_typeof(metadata) = 'object') NOT VALID;
