# Account Manager Refactor Intent

## Objective

Build Account Manager as an interface-driven modular monolith for mailbox,
message-retrieval, and platform-account workflows:

- Go backend with PostgreSQL and explicit domain ports/adapters.
- React + TypeScript frontend with mailbox management, workspaces, and settings navigation.
- Provider-neutral mailbox identities and platform-owned retrieval keys.
- Mailbox providers (Microsoft, Cloudflare-routed domain mail, Gmail) behind adapters.
- Versioned, encrypted backup providers for S3-compatible storage and WebDAV.

The existing Python application remains a behavior reference under the ignored
local path `references/mail-manager/`. Account Manager owns the code under
`backend/` and `frontend/`, its PostgreSQL schema, and its Compose stack.

## Phases

1. [completed] Establish repository layout, contracts, migrations, local PostgreSQL compose, and planning artifacts.
2. [completed] Implement Go domain model, provider registry, retrieval-key vault, mailbox/sub-address APIs, and backup service.
3. [completed] Implement React mailbox workspace shell, provider filters, expandable aliases, and backup settings view.
4. [completed] Add Microsoft Graph/Outlook IMAP, Gmail API/IMAP, and Cloudflare Email Routing adapters with fail-closed exact-recipient filtering for aliases.
5. [completed] Add mailbox detail/reveal APIs, custom and legacy import/export formats, primary-mailbox-only counts, and a readable full-width UI.
6. [in_progress] Complete redacted real-environment verification: Outlook Graph, Outlook IMAP, Gmail IMAP, Cloudflare-to-Gmail delivery, platform pickup-key retrieval, and strict split-address isolation against live forwarded mail have passed; a new Cloudflare provisioning write remains pending.
7. [completed] Add optimistic concurrency controls, token-refresh and backup workers, encrypted S3/WebDAV snapshots, asynchronous restore orchestration, and race-tested integration coverage.

## First Vertical Slice Acceptance

- `go test ./...` passes for backend contracts, key hashing/encryption, and backup manifests.
- API can create/list a mailbox without exposing the upstream OAuth/IMAP token.
- API can create a platform retrieval key, return it once, revoke it, and validate it through a provider-neutral interface.
- Imported legacy pickup keys are converted directly into keyed digests; their raw values are neither persisted nor reconstructable for export.
- A mailbox can have multiple aliases and platform accounts are related by mailbox identity, not a hard foreign-key ownership assumption.
- Legacy four-part/six-part/three-part formats, custom delimited/JSON formats, and reversible single-record, multiline, or repeat-block templates can be previewed and imported.
- The token-refresh setting is persisted with compare-and-swap semantics and is reloaded by the bounded-concurrency worker on each pass.
- Backup service can configure redacted S3-compatible or WebDAV targets, claim scheduled/manual jobs, create encrypted PostgreSQL snapshots, and track asynchronous restores.
- React app renders mailbox provider navigation, expandable aliases, retrieval-key status, import/export dialogs, token-refresh settings, and complete backup target/run/restore workflows.
- Desktop content uses the available page width, operational text is at least 14px, and the 390px layout has no page-level horizontal overflow.
- PostgreSQL schema uses UUID/public IDs, unique constraints, optimistic version columns, and transaction-safe writes.

## Decisions

- Use UUIDs for public identifiers; provider-native identifiers remain metadata, never primary keys.
- Store upstream secrets encrypted at rest; platform retrieval keys are random opaque secrets hashed for lookup.
- A mailbox identity is independent of a provider account. Aliases are children of a mailbox identity, while platform accounts reference a mailbox identity by route metadata.
- Provider adapters expose capabilities (`graph`, `imap`, `refresh`, `create_alias`, etc.) instead of leaking provider payload shapes to HTTP handlers.
- Backup snapshots are encrypted before leaving the process and use a provider-neutral object store port.
- A legacy Microsoft dual credential follows one rotating OAuth chain in Graph-then-IMAP order. The rotated Graph token is checkpointed before the IMAP call so a partial success can be persisted.
- Start with a modular monolith. Keep ports and service boundaries explicit so adapters can later be moved without rewriting domain logic.

## Risks / Open Work

- Microsoft and Gmail OAuth consent flows still need deployment-specific credentials and callback configuration; imported provider credentials already use the production adapters.
- The Cloudflare adapter and encrypted provider-connection settings are implemented, but no new rule was written during the current real-environment verification. Perform a small idempotency and rollback drill before enabling provisioning broadly.
- The Gmail adapter and forwarding-aware IMAP connection envelope have automated coverage and a redacted live pass through the new backend. The x1 HTTP-on-443 proxy did not accept a usable CONNECT tunnel from the local host, while direct Gmail IMAP passed, so that proxy endpoint remains deployment-specific.
- Direct database migration from the Python schema is not automatic. The supported migration path is currently the previewed format importer.
- PostgreSQL backup/restore, scheduling, CAS, and locking behavior has automated coverage; a deployment should still run a restore drill against its own S3/WebDAV target and PostgreSQL version.
- Real retrieval tests must record only opaque fixture IDs, timestamps, and pass/fail results. Addresses, refresh tokens, access tokens, cookies, and message bodies must stay out of source and logs.
- Alias retrieval is accepted only when the normalized requested recipient exactly matches an envelope/original-recipient header or a provider-native recipient field; a parent mailbox match alone is insufficient.
- Remote deployments need an authenticated reverse proxy in addition to the persistent backend bearer-token boundary.

## Errors Encountered

| Error | Attempt | Resolution |
|---|---:|---|
| `defuddle` command missing | 1 | Installed the CLI globally and extracted the requested style reference. |
| `go test ./...` reported missing `go.sum` entries for pgx | 1 | Run `go mod tidy` after repository packages settle, then rerun the full suite. |
| Combined PowerShell `Start-Process` command was rejected by command policy | 1 | Start API and frontend as separate hidden processes with simpler argument lists. |
| Ports `8080` and `18080` were already owned by Steam and another local app | 2 | Run the Go API on available port `127.0.0.1:18081` and point the Vite proxy there. |
| Docker Linux engine was not running during the final image check | 1 | `docker compose config --quiet` passed; image compilation remains a runtime-environment check. |
| Cloudflare direct sign-in selector used the prior Chinese label after the page switched to English | 1 | Re-read the visible page and used the current English button label; saved sign-in state restored the domain-owning account. |
| Browser page sandbox did not expose `fetch` or `XMLHttpRequest` for the legacy API | 2 | Use the console's selected-record export path and pipe the clipboard directly into the local importer, without inspecting cookies or persisting plaintext. |
| Local Gmail retrieval through the x1 HTTP-on-443 proxy ended during CONNECT | 1 | Verified the proxy behaves differently from the local source network, then reran the same ephemeral mailbox fixture through direct TLS IMAP; live retrieval and exact-recipient isolation passed. |
