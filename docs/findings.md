# Account Manager Findings

## Existing Project

- The current branch is a Python/FastAPI application with a large compatibility host in `app/legacy_main.py` and a React app shell in `src/App.tsx`.
- The earlier `dev-refactor` introduced Workspace concepts, but its `WorkspaceAccount` still assumes one mailbox row per service account. The new design needs mailbox identities and alias routing as first-class concepts.
- Existing mailbox code contains useful Microsoft Graph, routed-mail, and IMAP fallback behavior. Reuse the behavior through adapters rather than importing the old module graph into the Go service.

## Requested Product Shape

- Mailbox management is a primary area with provider sub-pages and expandable aliases under a primary mailbox.
- Workspaces are platform-specific account views selected by dropdown; pinned platforms appear in navigation.
- Settings are split into sub-pages because provider, security, backup, import/export, and automation options will grow independently.
- Initial providers are Microsoft mail, Cloudflare-routed domain mail, and Gmail.
- Retrieval must support Outlook/Hotmail token variants, IMAP/Graph/both modes, refresh-token status, and configurable automatic refresh.
- Import/export formats must be user-configurable.
- Backup targets are S3-compatible object storage and WebDAV, with scheduled backup and restore.

## Style Reference

The supplied Kawaii Minimal page calls for warm white backgrounds (`#FFF7ED`), pastel pink (`#F9A8D4`), lavender (`#A78BFA`), cyan (`#67E8F9`), and yellow (`#FDE68A`), rounded controls, generous spacing, and subtle motion. The implementation should retain readable dark text and accessible focus states, avoiding dark backgrounds and decorative gradients.

## Integration Review Resolution

- Platform accounts can now be created before a mailbox route exists and receive random platform-scoped IDs when no external reference is available.
- Cloudflare addresses are represented as forwarding aliases under a selected managed destination mailbox. Successful adapter provisioning records the upstream rule ID and verification state without changing the parent mailbox route.
- Pickup-key overview prefers any still-valid key over a newer expired key, and raw values remain one-time responses.
- Backup overview follows the newest run rather than all historical failures, while a target with no runs reports pending.
- Persistent deployments require a bearer token, an encryption key, and a pickup-key pepper. The browser-facing Compose proxy injects the bearer token server-side.
- Microsoft Graph/Outlook IMAP, Gmail API/IMAP, Cloudflare Email Routing, S3-compatible, and WebDAV transports are implemented behind ports. Token-refresh and backup workers are wired by the server composition root.
- Token-refresh settings are server-persisted and versioned. The worker reloads the switch and lead time before every scan, so UI changes take effect without a process restart.
- Backup targets expose only non-secret provider summaries. Updates use compare-and-swap and preserve the sealed configuration when the request omits `config`.

## Current Adapter Acceptance

- Mailbox totals count only primary mailbox identities. Split and forwarding aliases are displayed as child counts on their parent.
- Mailbox details expose provider metadata, credential kind, expiry and refresh state. Client IDs are displayable; refresh tokens remain masked until an explicit authenticated reveal action.
- Import formats define kind, direction, provider, delimiter/header behavior, ordered field mappings, and parser configuration. Import runs support preview/validation and an explicit conflict policy before transactional apply.
- Export excludes upstream provider secrets by default. Platform pickup keys remain one-time values and therefore cannot be reconstructed for export after issuance.
- Split-address retrieval must use exact normalized-recipient matching across provider-native recipients and headers such as `Delivered-To`, `X-Original-To`, `Envelope-To`, `X-Envelope-To`, and `To`/`Cc` as a lower-confidence fallback.
- Custom template formats now support reversible single-line and multiline records, JSON-encoded variables, and one `%begin ... %end%` repeat block with an optional quoted separator.
- Desktop content uses the available viewport width. Operational detail/preview/backup text has a 14px floor, controls are at least 44px where practical, and the 390px layout remains free of page-level horizontal overflow.

## Token and Retrieval Semantics

- A current Microsoft dual-token credential may store distinct Graph and IMAP refresh/access tokens and per-method expiries.
- A legacy Outlook record carries one rotating refresh-token chain. Compatibility refresh therefore calls Graph first, updates all shared RT fields with any rotated token, seals a checkpoint, and then calls IMAP with that current token. If the second call fails or times out, the Graph checkpoint is persisted instead of losing the rotation.
- Refresh-token strings do not contain trustworthy expiry metadata. Status is derived from access-token expiry, `refresh_after`, and recorded refresh results; the UI does not invent an RT expiration date.
- On-demand retrieval coalesces concurrent refresh attempts for the same credential. The background worker separately bounds scan pagination, job concurrency, item timeout, and error retry backoff.
- Administrator retrieval and pickup-key retrieval share the same routing service. The platform key replaces upstream credentials at the HTTP boundary and never grants access to an alias owned by another mailbox.

## Backup Runtime

- S3-compatible targets support endpoint, region, bucket, prefix, static/session credentials, and path-style addressing. WebDAV targets support base URL, prefix, basic/bearer authentication, and an explicit TLS verification override.
- Target configuration is sealed before persistence; GET/list responses contain a location summary and credential-presence flags rather than connection secrets.
- Schedulers deduplicate each target/due-window pair, workers atomically claim pending jobs, and a repository-wide operation lock serializes snapshots and restores across server instances.
- PostgreSQL snapshots use custom-format `pg_dump`, are encrypted before upload, and carry a checksum. Restore verifies the object and decrypts it before a single-transaction `pg_restore`.
- Restore HTTP requires an exact confirmation value, returns `202 Accepted`, and exposes a pollable process-local operation. A second active operation reports conflict rather than waiting behind the first.

## Real Environment Discovery

- A small redacted Outlook fixture was transferred through an ephemeral runtime path; no address, provider secret, cookie, or message body was written to source or test reports.
- Aggregate real-environment checks passed for Outlook Graph retrieval, Outlook IMAP retrieval, platform pickup-key retrieval, and exact split-address recipient isolation.
- A saved Cloudflare session and existing Email Routing configuration were confirmed. The current pass did not create or modify a real routing rule, so live provisioning write behavior remains unverified.
- Cloudflare Email Routing is enabled for the requested domain and an active catch-all already targets the located Gmail fixture. This permits a live delivery/isolation check without creating or mutating a production routing rule.
- A uniquely marked Outlook message to a random address under the requested domain appeared in Cloudflare Activity Log as forwarded. Gmail classified it into Junk: the new API returned one exact match there and zero for a same-domain sibling alias, while the INBOX query correctly returned zero.
- A real Gmail-forwarding IMAP connection was imported through a custom ten-field format. Direct TLS IMAP through the new Go backend returned live messages; an observed forwarded recipient returned only six exact matches, while a sibling address in the same domain returned zero.
- The existing x1 proxy is declared as HTTP on port 443. It succeeds from x1 but local CONNECT probes time out or close before a usable response, while the same local host reaches Gmail IMAP directly. Treat that proxy as source-environment-specific rather than a credential or parser failure.
- Real test records retain only opaque fixture IDs, timestamps, aggregate counts, and pass/fail results.

## Legacy Import/Export Compatibility

- The legacy `mail-manager` behavior reference includes `Original`, `Registered`, `cf_routed`, `simple`, and Sub2api JSON shapes. The rebuild currently seeds four text built-ins: Outlook four-part, registered six-part, Cloudflare-routed three-part, and simple three-part; generic JSON formats are configured through field paths and `records_path`.
- `cf_routed` and `simple` have the same three-part text shape but different semantics. The routed format fixes the mailbox provider to Cloudflare routing and treats the third value as the forwarded-mail access credential; the simple format treats it as the mailbox password. Format selection must therefore remain explicit when the shape is ambiguous.
- The routed third value is imported as a platform pickup-key digest, not as an IMAP password. Its raw value is not persisted, so the format is import-only and later exports cannot reconstruct it.
- The simple built-in infers Microsoft for recognized Outlook/Hotmail domains and Gmail for recognized Gmail/Googlemail domains. Unknown domains produce a row error requiring a fixed provider or explicit provider field.
- The legacy Outlook import persists one mailbox `refresh_token`, not separate Graph and IMAP refresh tokens. The rebuild initializes the dual adapter from that shared chain unless the input explicitly contains independently authorized grants.
- The legacy behavior reference accepts an optional trailing payment URL for selected account formats. The current seeded Go formats map exactly four, six, or three columns, so an extra value is reported in preview rather than silently discarded; malformed rows are excluded from transactional apply.
- The Sub2api reference accepts a single account object, an array, or an object with an `accounts` array, with platform and mailbox credentials in separate nested domains. The current generic JSON importer handles an array or an object containing an array at `records_path`; a dedicated single-object Sub2api built-in remains migration work.
- Custom formats support delimited, JSON, and template parsing. Template validation rejects unmapped/duplicate variables, adjacent captures without a literal separator, malformed repeat blocks, and invalid `_json` captures before data is applied.

## Verification Status

- Backend verification passes with `go test ./...`, `go vet ./...`, and `go test -race ./...`.
- The covered aggregate includes shared dual-token ordering/checkpointing, refresh settings and worker bounds, Graph/IMAP retrieval, pickup-key scope/revocation/expiry, strict recipient filtering, legacy/template imports, S3/WebDAV transports, target CAS, scheduler/worker concurrency, checksums, and asynchronous restore HTTP.
- Frontend verification passes with `npm run typecheck` and `npm run build`; browser checks cover 390px mobile plus 1440px and 1920px desktop layouts.
