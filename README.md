# Account Manager

Account Manager is an interface-driven mailbox and platform-account management
system. It combines mailbox inventory, provider-neutral message retrieval,
platform-account routing, import/export, and encrypted database backups without
coupling platform accounts to a single mailbox provider.

## Current Scope

- Microsoft mailboxes through one shared RT with Graph/REST and IMAP OAuth retrieval channels.
- Gmail through Gmail API and IMAP OAuth.
- Cloudflare Email Routing aliases forwarded to managed destination mailboxes.
- Primary mailboxes with expandable split/forwarding addresses and exact-recipient filtering.
- Independent ChatGPT, Grok, and future platform-account identities routed by mailbox ID.
- Platform-owned pickup keys that avoid exporting upstream provider tokens.
- Preview-first legacy/custom import and export formats.
- Encrypted PostgreSQL backups to S3-compatible or WebDAV storage.
- Full-width responsive React administration UI.

## Repository Layout

```text
backend/                 Go API, workers, provider adapters, repositories
frontend/                React + TypeScript administration UI
docs/architecture.md     Stable domain and interface boundaries
docs/refactor-intent.md  Product intent, decisions, phases, and open work
docs/progress.md         Implemented behavior and verification history
docs/findings.md         Legacy compatibility and provider findings
compose.yml              PostgreSQL, backend, and frontend stack
```

The previous `mail-manager` project is a behavior reference only. A local clone
may live under `references/mail-manager/`; `references/` is excluded from this
repository. See `docs/legacy-reference.md` for the boundary.

## Run With Docker

Create `.env` from `.env.example`, replace every required placeholder, then run:

```bash
docker compose up --build
```

The frontend binds to `http://127.0.0.1:18019` by default.

## Local Development

```bash
cd backend
go test ./...
go run ./cmd/server
```

```bash
cd frontend
npm install
npm run dev
```

The backend uses an in-memory repository when `DATABASE_URL` is empty. The
frontend defaults to `http://127.0.0.1:8080`; its `.env.example` documents local
API overrides.

## Verification

```bash
cd backend && go test ./... && go test -race ./... && go vet ./...
cd frontend && npm run typecheck && npm run build
docker compose config --quiet
```

Credentials, live mailbox addresses, provider tokens, and backup secrets stay
outside Git. Persistent deployments require independent encryption, pickup-key,
database, and administrator secrets.
