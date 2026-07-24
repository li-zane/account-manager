# Account Manager Progress

## Project Extraction 2026-07-24

- Declared the Go/React implementation as the standalone `Account Manager` project instead of a branch-local rebuild inside `mail-manager`.
- Moved the application to the repository root with `backend/`, `frontend/`, `docs/`, and `compose.yml` as first-class project directories.
- Changed the Go module to `github.com/li-zane/account-manager/backend`, the frontend package to `account-manager-frontend`, the pickup-key prefix to `am_pk_`, and Docker/backup identifiers to `account-manager`.
- Preserved the original product intent, architecture, findings, and verification history under `docs/`.
- Moved the clean `mail-manager` reference clone to the ignored local path `references/mail-manager/` at baseline `f1bd354`; the new application has no runtime dependency on it.
- Re-ran `go test ./...`, `go test -race ./...`, `go vet ./...`, `npm run typecheck`, `npm run build`, and `docker compose config --quiet` successfully after extraction.

## 2026-07-24

- Confirmed the source repository is `li-zane/mail-manager` and checked out the `dev-refactor` branch as a reference copy.
- Confirmed Go 1.26.5, Node 24.13.0, npm 11.6.2, and Docker 29.4.2 are available.
- Read the supplied StyleKit Kawaii Minimal reference and recorded its tokens and accessibility caveats.
- Created the rebuild as a separate modular-monolith application under `backend/` and `frontend/`, with its own PostgreSQL migration path and Compose stack.
- Added the first PostgreSQL migration for provider connections, mailboxes, aliases, encrypted credentials, platform pickup keys, independent platform accounts/routes, backup targets/runs, pinned workspace definitions, custom import/export formats, and settings.
- Added AES-GCM secret sealing and HMAC-based platform pickup-key issuance/validation. Raw pickup keys are returned once and never persisted.
- Added provider-neutral Cloudflare-style domain routing ports and persisted forwarding routes so a managed Gmail/Microsoft mailbox can be selected as a destination by internal ID.
- Added versioned PostgreSQL migrations with an advisory lock and automatic application on persistent backend startup.
- Made platform-account storage accept accounts before a mailbox route exists and added platform-wide listing; mailbox routes remain independent relations.
- Completed the first vertical slice: live mailbox creation, Cloudflare forwarding aliases routed to managed destinations, one-time pickup-key issuance, provider filtering, and backup run queuing are connected end to end.
- Added persistent-deployment bearer authentication, a separate stable pickup-key pepper, credential optimistic version checks, complete overview pagination, and regression coverage for redaction and status aggregation.
- Added `compose.yml` with isolated Go backend, React/nginx frontend, and PostgreSQL services; the legacy Python Compose remains unchanged.

### Mail retrieval and credentials

- Implemented Microsoft Graph and Outlook IMAP retrieval, Gmail API and IMAP retrieval, refresh-token rotation, pagination bounds, folder/query mapping, and redacted upstream errors.
- Implemented separate Graph/IMAP token fields plus legacy shared-token compatibility. Shared dual refresh runs Graph first, persists a rotated-token checkpoint, then runs IMAP with the current chain token.
- Added persisted token-refresh settings (`enabled`, lead time, version) and a worker that reloads them each pass, paginates mailbox scans, bounds concurrency and per-item time, backs off errors, and coalesces concurrent on-demand refreshes.
- Added administrator and platform pickup-key retrieval endpoints. Pickup keys are HMAC-digested, scoped to one mailbox, revocable/expirable, and may select only aliases belonging to that mailbox.
- Enforced fail-closed alias filtering against exact normalized recipients from provider-native recipient fields and accepted envelope/original-recipient headers. A parent mailbox match alone is insufficient.
- Real-environment aggregate checks passed for Outlook Graph, Outlook IMAP, platform pickup-key access, and strict split-address isolation. Reports retain only opaque fixture identifiers and aggregate pass/fail state.

### Import and export

- Added preview-first transactional import with per-row validation, duplicate detection, and `skip`, `update`, or `error` conflict policies.
- Added built-in legacy formats for Outlook four-part, registered six-part, Cloudflare-routed three-part, and simple three-part records. The simple format infers Microsoft/Gmail only from recognized address domains and reports unknown domains explicitly.
- Added custom delimited, JSON, and reversible template import/export. Templates support single-line, multiline, JSON-encoded variables, and one repeat block with a declared separator.
- Cloudflare-routed legacy access keys are imported directly as pickup-key HMAC digests. The plaintext is not retained, the format is import-only, and the value is not reconstructable during export.
- Added mailbox details with credential type, client ID, per-method token presence/expiry, masked RT fields, explicit reveal, aliases, and linked platform accounts.

### Backup and restore

- Added encrypted S3-compatible and WebDAV target configuration with strict provider-specific validation and redacted response summaries.
- Added target create/list/get/update APIs. Updates require the current `version` for compare-and-swap; omitting `config` preserves the encrypted connection details.
- Added manual and scheduled backup queuing, atomic run claiming, due-window deduplication, retention settings, bounded failure details, and repository-wide advisory locking across snapshots/restores.
- The PostgreSQL worker creates a custom-format `pg_dump`, encrypts it before upload, verifies checksum/decryption on restore, and runs `pg_restore` in a single transaction.
- Added restore confirmation, `202 Accepted` asynchronous start, and pollable `running`/`succeeded`/`failed` operation state. Disabled runtime, invalid confirmation, and operation conflicts have explicit HTTP behavior.

### Frontend and verification

- Connected token-refresh settings, mailbox details, import/export, Cloudflare connection settings, and full S3/WebDAV target/history/restore workflows to server APIs.
- Expanded the application content to the available desktop width, raised operational detail text to at least 14px, enlarged controls, and preserved wrapping for mailbox and forwarding addresses.
- Browser QA passed at 390px with no body-level horizontal overflow and a 14px minimum visible text size; desktop checks at 1440px and 1920px confirmed that the main view uses the available width.
- `npm run typecheck` and `npm run build` pass. The backend suite passes with `go test ./...`, `go vet ./...`, and `go test -race ./...`.

### Remaining real-environment checks

- A signed-in Cloudflare environment was inspected and existing Email Routing configuration was confirmed. No new provisioning write was executed during this pass.
- A real Gmail-forwarding IMAP connection passed through the new Go backend over direct TLS. The live alias query returned six exact-recipient messages and a same-domain sibling query returned zero, confirming fail-closed isolation without recording addresses or content.
- An Outlook fixture sent through the existing Cloudflare catch-all was recorded as forwarded and appeared in Gmail Junk. The Go alias query returned one exact match and a same-domain sibling returned zero; no Cloudflare rule or destination was changed.
- Real S3/WebDAV credentials and a deployment PostgreSQL restore drill remain environment-specific acceptance work; adapter, worker, CAS, scheduling, checksum, and restore HTTP behavior are covered by automated tests.
