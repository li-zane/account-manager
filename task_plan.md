# Task Plan

## Goal

Refine the mailbox reader using mail-manager's useful list/detail hierarchy while preserving this project's visual system: persistent read state, clearer subjects, reliable verification codes, copy-friendly metadata, per-message protocol labels, and no redundant controls.

## Current Phases

- [completed] Compare mail-manager and current mailbox reader structure, extraction, and styling.
- [completed] Persist local message read state and expose the read endpoint.
- [completed] Rework reader hierarchy, per-message protocol labels, copy interactions, and verification extraction.
- [completed] Add regression coverage and deploy the database migration/backend/frontend.
- [completed] Verify desktop/mobile visual layout, read-dot behavior, and copy feedback.
- [completed] Move the message subject to the top and rebalance receipt time and protocol hierarchy.

## Previous Completed Phases

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

- Keep all imported real mailboxes and aliases in the local database.
- Never log or return plaintext pickup keys outside explicit admin reveal/export responses.
- Preserve legacy encrypted Graph/IMAP refresh-token fields as read compatibility only.
- Live verification may refresh and persist AT/rotated RT values but must not print token or password contents.
- Send at most one controlled test message from the user's Outlook fixture to a selected local test recipient; do not bulk-send.
- Preserve the existing dirty Outlook REST removal changes.
- Remove the Register 6-part, GPT, and CF-routed built-ins; rename the universal email/platform-key format to `平台取件格式`.
- Default Microsoft imports to the four-part `Outlook 邮箱凭证` and show a concrete format example before input is pasted.
- Sensitive export previews must come from the authorized server export response rather than frontend placeholders.
- The current Microsoft Graph grant is `Mail.Read offline_access`; a send test requires a separately consented `Mail.Send` grant.

## Errors Encountered

| Error | Attempt | Resolution |
| --- | --- | --- |
| `rg` rejected a wildcard path on Windows | 1 | Use `rg ... backend/internal/service -g "*_test.go"`. |
| Retrieval verification metadata incremented credential versions and broke refresh concurrency invariants | 1 | Keep credential summaries read-only; reserve a separate operational status store for future per-probe persistence. |
| Credential-count query used the API field name `credential_type` instead of the PostgreSQL column `kind` | 1 | Inspected the table schema and reran the aggregate with `kind`. |
| Local policy rejected replacing the existing Vite process | 2 | Kept Vite on `5175` and started a refresh/probe/backup-disabled backend on its configured `18083` origin. |
| Guessed migration implementation filenames that do not exist | 1 | Listed `backend/migrations` and read the actual `embed.go` implementation. |
| Concurrent pickup-key branch initially missed the standard `errors` import | 1 | Added the import, formatted the touched Go files, and reran focused tests. |
| Parallel workspace inspection aborted because `rg` returned exit code 1 for no AGENTS.md match | 1 | Split commands and normalize the expected empty-search exit code. |
| Backend-domain search used repository-root paths while the command workdir was already `backend` | 1 | Re-run with paths relative to the selected working directory. |
| PowerShell reserves `$PID`, so the restart script could not assign the server process ID | 1 | Use a non-reserved `$serverProcessId` variable for process restarts. |
| Looked for the root `.env` while the command was already running from `backend` | 1 | Re-run from the repository root and use the root-relative path. |
| The Playwright shell wrapper used incompatible Windows/WSL paths and CRLF line endings | 3 | Invoke `npx --package @playwright/cli playwright-cli` directly from PowerShell. |
| A Playwright element reference became stale after resizing the viewport | 1 | Select the target by its current semantic content in one `run-code` call. |
| The target RT rejected an explicit Graph `Mail.Read offline_access` refresh with `invalid_grant` | 1 | Retry Graph refresh with the app's consented `https://graph.microsoft.com/.default` grant and persist the normalized Graph scope. |
| The target IMAP XOAUTH2 login returned `BAD Command Argument Error. 12` even with a freshly stored IMAP AT | 1 | Keep IMAP marked failed and hidden; use the separately verified Graph channel for this mailbox. |
| A direct overview request omitted the administrator bearer token and returned 401 | 1 | Read the local token from environment only in-process and repeat the request without printing it. |
| Queried the base mailbox endpoint when inspecting credential capabilities | 1 | Use the dedicated `/api/v1/mailboxes/{id}/detail` endpoint for credential and capability summaries. |
| Initial PowerShell folder-summary pipeline had an empty pipe element | 1 | Accumulate per-folder results in an array before JSON serialization. |
| Time-range recovery used automatic routing but cached an empty method | 1 | Return the concrete selected method from retrieval and persist it per recovered batch. |
| Broad verification-code matching labeled years and word fragments as codes | 1 | Require a nearby multilingual code keyword, at least one digit, and exclude four-digit years. |
