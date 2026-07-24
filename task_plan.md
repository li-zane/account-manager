# Task Plan

## Goal

Complete the mailbox-management foundation while protecting the imported x1 refresh tokens: one shared Microsoft RT, explicit Graph/REST/IMAP capability states, accurate token lifetime labels, click-to-copy addresses, cached inbox/junk viewing, automatic pickup keys, and format-aware export.

## Phases

- [completed] Trace refresh and credential response paths; define compatibility behavior.
- [completed] Enforce the refresh setting in message retrieval and add regression tests.
- [completed] Collapse public credential details to one refresh token and preserve legacy read compatibility.
- [completed] Restrict forwarding-recipient UI to domain mailboxes.
- [completed] Auto-issue encrypted/recoverable platform pickup keys on create/import and backfill existing mailboxes.
- [completed] Add per-mailbox format-aware export, including a universal address/pickup-key format.
- [completed] Add persisted message cache, incremental sync, folders, manual refresh, and polling settings/runtime.
- [completed] Finish the React inbox, copy, details, export, and probe-setting integration and repair type contracts.
- [completed] Add trustworthy Outlook capability states and distinguish RT status from Graph/IMAP access-token expiry.
- [completed] Map public Xinlan Outlook retrieval features to the provider-neutral implementation and document gaps.
- [completed] Add cache, pickup-key, worker, migration, and HTTP regression coverage.
- [completed] Run backend/frontend/static and responsive UI verification without real mailbox retrieval.
- [completed] Update project progress, commit, and push.

## Constraints

- Do not call any real mailbox retrieval or token refresh endpoint.
- Keep all imported real mailboxes and aliases in the local database.
- Never log or return plaintext pickup keys outside explicit admin reveal/export responses.
- Preserve legacy encrypted Graph/IMAP refresh-token fields as read compatibility only.

## Errors Encountered

| Error | Attempt | Resolution |
| --- | --- | --- |
| `rg` rejected a wildcard path on Windows | 1 | Use `rg ... backend/internal/service -g "*_test.go"`. |
| Retrieval verification metadata incremented credential versions and broke refresh concurrency invariants | 1 | Keep credential summaries read-only; reserve a separate operational status store for future per-probe persistence. |
| Credential-count query used the API field name `credential_type` instead of the PostgreSQL column `kind` | 1 | Inspected the table schema and reran the aggregate with `kind`. |
| Local policy rejected replacing the existing Vite process | 2 | Kept Vite on `5175` and started a refresh/probe/backup-disabled backend on its configured `18083` origin. |
| Guessed migration implementation filenames that do not exist | 1 | Listed `backend/migrations` and read the actual `embed.go` implementation. |
| Concurrent pickup-key branch initially missed the standard `errors` import | 1 | Added the import, formatted the touched Go files, and reran focused tests. |
