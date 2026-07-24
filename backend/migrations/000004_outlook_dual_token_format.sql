-- Outlook four-part records contain a shared Microsoft refresh-token chain.
-- Import them as dual credentials so Graph/Outlook REST and IMAP can be
-- selected independently without changing the external four-field format.

UPDATE mailbox_formats
SET parser_config = jsonb_set(
        COALESCE(parser_config, '{}'::jsonb),
        '{credential_kind}',
        '"microsoft_dual_token"'::jsonb,
        TRUE
    ),
    version = version + 1,
    updated_at = now()
WHERE id = 'fmt_builtin_outlook4'
  AND builtin = TRUE
  AND parser_config->>'credential_kind' IS DISTINCT FROM 'microsoft_dual_token';
