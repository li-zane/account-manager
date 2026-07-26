# Account Manager Backend

This directory contains the provider-neutral Go backend for Account Manager. It
is consumed by the React client under `frontend/` and has no runtime dependency
on the legacy reference application.

## Boundaries

- `internal/domain`: stable mailbox, alias, platform-account, pickup-key, and backup entities.
- `internal/ports`: provider, retrieval, secret, repository, routing, and object-storage interfaces.
- `internal/providers`: Microsoft Graph/Outlook IMAP, Gmail API/IMAP, Cloudflare Email Routing, S3-compatible, and WebDAV adapters.
- `internal/repository/memory`: race-safe local/test repository with database-like constraints.
- `internal/repository/postgres`: `pgxpool` repository matching `migrations/000001_mail_platform.sql`.
- `internal/security`: AES-GCM secret broker and HMAC-digested opaque pickup keys.
- `internal/service`: mailbox, alias, account routing, retrieval, token refresh, import/export, and backup target/run use cases.
- `internal/httpapi`: standard `net/http` REST transport.

Raw provider credentials and backup target configuration are encrypted before
persistence. Raw pickup tokens are displayed once and only their keyed digest
is stored. A platform account may be created before any mailbox route exists;
when linked, it routes through `mailbox_id`. Provider-native IDs and email
addresses remain attributes rather than relational keys.

## Run locally

The server uses the in-memory repository when `DATABASE_URL` is empty:

```text
go test ./...
go run ./cmd/server
```

The default address is `http://127.0.0.1:8080` and health is available at
`GET /healthz`.

For PostgreSQL, apply the schema and provide a persistent 32-byte AES key:

```text
DATABASE_URL=postgres://USER:PASSWORD@HOST:5432/DB go run ./cmd/migrate
DATABASE_URL=postgres://USER:PASSWORD@HOST:5432/DB APP_ENCRYPTION_KEY_BASE64=BASE64_KEY PICKUP_KEY_PEPPER_BASE64=BASE64_PEPPER ADMIN_API_TOKEN=TOKEN go run ./cmd/server
```

`APP_ENCRYPTION_KEY_VERSION` defaults to `v1`. `HTTP_ADDR` changes the listen
address. PostgreSQL mode requires `APP_ENCRYPTION_KEY_BASE64`; memory mode uses
an ephemeral development key. Persistent mode also requires a stable
`PICKUP_KEY_PEPPER_BASE64` and `ADMIN_API_TOKEN`; clients send the latter as a
bearer token. Keeping the pickup-key pepper separate allows encryption-key
rotation without invalidating existing platform pickup keys. PostgreSQL startup applies embedded migrations
under an advisory lock unless `AUTO_MIGRATE=false` is set.

Provider endpoints use their public defaults. The following variables enable
or override runtime integrations without placing credentials in source files:

```text
CLOUDFLARE_API_TOKEN=TOKEN
CLOUDFLARE_ACCOUNT_ID=ACCOUNT_ID
CLOUDFLARE_ZONE_ID=ZONE_ID
CLOUDFLARE_ZONE_NAME=example.com
CLOUDFLARE_API_BASE_URL=https://api.cloudflare.com/client/v4
MICROSOFT_TOKEN_ENDPOINT=https://login.microsoftonline.com/common/oauth2/v2.0/token
MICROSOFT_GRAPH_BASE_URL=https://graph.microsoft.com/v1.0
GOOGLE_TOKEN_ENDPOINT=https://oauth2.googleapis.com/token
GMAIL_API_BASE_URL=https://gmail.googleapis.com/gmail/v1
TOKEN_REFRESH_WORKER_ENABLED=true
TOKEN_REFRESH_WORKER_INTERVAL=1m
TOKEN_REFRESH_WORKER_CONCURRENCY=4
TOKEN_REFRESH_WORKER_ITEM_TIMEOUT=45s
TOKEN_REFRESH_WORKER_ERROR_BACKOFF=15m
BACKUP_WORKER_ENABLED=true
BACKUP_WORKER_POLL_INTERVAL=2s
BACKUP_SCHEDULER_INTERVAL=30s
PG_DUMP_PATH=pg_dump
PG_RESTORE_PATH=pg_restore
```

Microsoft and Gmail mailbox credentials remain encrypted records imported or
created through the application. Cloudflare is reported as configured only
when its API token, account ID, and zone ID are all present.

The token-refresh worker reads the persisted switch and 1-30 minute lead time
before every pass, so settings changes apply without a restart. The environment
variables above are operational limits and an optional process-level kill
switch; the UI setting remains the normal control.

Microsoft dual-channel credentials use one canonical rotating RT shared by
Graph and IMAP. Legacy split Graph/IMAP RT fields remain read-compatible
and are collapsed into the canonical chain on the next successful refresh.
Refresh runs Graph first, applies any rotated RT, seals a durable checkpoint,
and then refreshes IMAP with the current RT. If the IMAP call fails, the service
persists the Graph checkpoint so a consumed rotation is not lost.

## REST surface

- `GET /healthz`
- `GET /api/v1/providers`
- `GET /api/v1/provider-connections`
- `GET|PUT /api/v1/provider-connections/{provider}`
- `GET|PUT /api/v1/settings/token-refresh`
- `GET|POST /api/v1/mailboxes`
- `GET /api/v1/mailboxes/overview`
- `GET /api/v1/mailboxes/{id}/detail`
- `POST /api/v1/mailboxes/{id}/credentials/reveal`
- `GET|POST /api/v1/mailboxes/{id}/aliases`
- `GET /api/v1/mailboxes/{id}/messages`
- `GET /api/v1/mailbox-aliases/{alias_id}/messages`
- `GET|POST /api/v1/mailboxes/{id}/pickup-keys`
- `DELETE /api/v1/mailboxes/{id}/pickup-keys/{key_id}`
- `GET /api/v1/pickup/messages`
- `GET|POST /api/v1/mailbox-formats`
- `GET|PUT /api/v1/mailbox-formats/{format_id}`
- `POST /api/v1/mailboxes/import/preview`
- `POST /api/v1/mailboxes/import`
- `POST /api/v1/mailboxes/export/preview`
- `POST /api/v1/mailboxes/export`
- `POST /api/v1/platform-accounts`
- `GET /api/v1/platform-accounts?platform=chatgpt`
- `GET /api/v1/platform-accounts/{id}/mailbox`
- `GET|POST /api/v1/backups/targets`
- `GET|PUT /api/v1/backups/targets/{target_id}`
- `GET|POST /api/v1/backups/runs`
- `GET /api/v1/backups/runs/{run_id}`
- `POST /api/v1/backups/runs/{run_id}/restore`
- `GET /api/v1/backups/restores/{restore_id}`
- `POST /api/v1/backups` (queues the first enabled target for the initial UI)

Mailbox and alias message endpoints use the administrator bearer token. They
accept `after` (RFC3339), `limit`, `unread`, `folder` (`INBOX` or `Junk`),
`method`, `page_size`, and `max_pages` query parameters. Alias retrieval always
applies exact recipient filtering for that alias.

`GET /api/v1/pickup/messages` instead accepts a platform pickup token as its
bearer token. It supports the same message query parameters and an optional
`alias_id`; the alias must belong to the mailbox that issued the pickup token.
Expired and revoked pickup tokens return `410 Gone`, while invalid tokens
return `401 Unauthorized`.

## Import and export formats

The service seeds these `----`-delimited compatibility formats:

- `fmt_builtin_outlook4`: address, mailbox password, Microsoft client ID, RT.
- `fmt_builtin_registered6`: address, platform password, mailbox password,
  Microsoft client ID, RT, platform access token.
- `fmt_builtin_cf_routed3`: address, platform password, legacy mail access key.
  This format is import-only.
- `fmt_builtin_simple3`: address, platform password, mailbox password. It
  infers Microsoft or Gmail only for recognized provider domains; other domains
  require a fixed provider or provider field.

The format API also supports delimited, JSON, and template formats. JSON fields
may use dotted paths and a configurable `records_path`. Templates support
mapped `{{field}}` and JSON-safe `{{field_json}}` variables, multiline records,
and one `%begin ... %end%` repeat block with an optional `sep` attribute.
Imports provide a row-level preview before transactional apply and accept
`skip`, `update`, or `error` conflict handling.

The Cloudflare-routed third field is prepared as a platform pickup key: the
supplied value is HMAC-digested before persistence and plaintext is discarded.
Raw issued keys are likewise returned only once. Pickup keys are intentionally
absent from later exports because no reconstructable value is stored.

In PostgreSQL mode, the backup worker claims pending jobs atomically, creates a
custom-format `pg_dump`, encrypts and uploads it to the target S3/WebDAV store,
and persists a terminal state even when shutdown cancels the command. Automatic
targets accept five-field cron expressions, descriptors such as `@every 6h`,
and the aliases `daily`, `weekly`, and `six-hours`. Concurrent schedulers insert
at most one run for each target and due window, while a PostgreSQL advisory lock
serializes snapshots and restores across server instances. The runtime image
includes `pg_dump` and `pg_restore`; restore verifies the object checksum and
decrypts it before a single-transaction `pg_restore`.

Create target requests default `retention_count` to 14 when it is zero; accepted
values are 1-365. Target updates require the current `version` for
compare-and-swap. Omit `config` to preserve the sealed connection configuration.
S3/WebDAV responses include only non-secret location summaries and
credential-presence flags. Start a restore with `{"confirm":"RESTORE"}`; the
API returns `202 Accepted` with an operation that can be polled until
`running`, `succeeded`, or `failed`. Restore endpoints return
`501 Not Implemented` when the PostgreSQL worker runtime is disabled.

## Verification

```text
go test ./...
go vet ./...
go test -race ./...
```

All three commands pass for the current tree. Coverage includes Microsoft
Graph/IMAP and shared-RT dual-channel refresh, pickup-key scope and lifecycle,
fail-closed alias recipient filtering, legacy/template import, S3/WebDAV
transports, target CAS, scheduler/worker concurrency, snapshot integrity, and
asynchronous restore HTTP.

The redacted live regression has passed Outlook Graph, Outlook IMAP, Gmail IMAP,
Cloudflare-to-Gmail delivery, platform pickup-key retrieval, and strict
split-address isolation against live forwarded mail in both INBOX and Junk
queries. A new Cloudflare provisioning write remains a deployment check; no
address or secret is recorded by this repository. The located x1 HTTP proxy
uses port 443 but does not accept a usable CONNECT tunnel from the local test
host, so that endpoint remains environment-specific; direct Gmail IMAP passed.

## Docker Compose

From the repository root, set strong values for `ACCOUNT_MANAGER_POSTGRES_PASSWORD`,
`APP_ENCRYPTION_KEY_BASE64`, `PICKUP_KEY_PEPPER_BASE64`, and `ADMIN_API_TOKEN`,
then run `docker compose up --build`. The UI binds to
`http://127.0.0.1:18019` by default.
