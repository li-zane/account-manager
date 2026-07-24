# Interface-Driven Architecture

## Boundaries

```text
HTTP / worker adapters
        |
application services
        |
domain entities + ports
        |
PostgreSQL / Microsoft / Gmail / Cloudflare / S3 / WebDAV adapters
```

Business services import domain ports only. Provider adapters may import an external SDK, but external OAuth, IMAP, Graph, Cloudflare, S3, or WebDAV payloads must stop at the adapter boundary.

## Mailbox Identity

A mailbox is an addressable resource, not a platform account and not an OAuth token.

```text
Mailbox
  id: mbx_<provider>_<160-bit digest>
  provider: microsoft | gmail | cloudflare_route
  address
  status
  aliases[]
  credentials[] (encrypted)
  pickup_keys[] (hashed)
```

The mailbox ID digest input is `provider_namespace + NUL + normalized_address`. The PostgreSQL `(provider, normalized_address)` unique constraint is the final collision guard.

Aliases use random public IDs and belong to a primary mailbox for display and retrieval routing. A Cloudflare source alias may point at a platform-managed Gmail or Microsoft destination mailbox through `forwarding_routes`.

## Platform Accounts

Platform accounts use independent public IDs:

```text
acct_<platform>_<160-bit digest>
```

They can exist before a mailbox route is attached. `platform_account_mailbox_routes` relates login, recovery, notification, or other address roles to a primary mailbox or alias. This supports:

- one mailbox registering many ChatGPT/Grok accounts;
- one account moving to another mailbox;
- split-address registration;
- future accounts with more than one mailbox role.

## Retrieval Keys

External consumers receive a platform token rather than an upstream token:

```text
am_pk_<random 256-bit value>
```

The raw token is displayed once. PostgreSQL stores only an HMAC-SHA-256 digest, a short display prefix, expiry, label, and revocation time. The lookup flow is:

1. Authenticate the platform pickup token.
2. Resolve its mailbox ID.
3. Load the encrypted provider credential.
4. Select a retrieval adapter from provider capability and configured method.
5. Retrieve messages and return a provider-neutral message model.

This prevents exported credentials from exposing Microsoft/Gmail refresh tokens and lets the platform change retrieval methods without changing consumer configuration.

## Provider Capabilities

| Provider | Retrieval | Refresh | Alias/provisioning | Forwarding |
|---|---|---|---|---|
| Microsoft | Graph, IMAP OAuth, dual-token | yes | adapter seam | no |
| Gmail | Gmail API, IMAP OAuth | yes | adapter seam | destination mailbox |
| Cloudflare route | destination mailbox | no | Email Routing API | yes |

Cloudflare configuration is stored in `provider_connections.encrypted_config`. The adapter should accept scoped API tokens with zone/DNS and Email Routing permissions, then create an idempotent routing rule whose destination is selected by internal mailbox ID.

## Backup Flow

```text
consistent PostgreSQL snapshot
  -> encrypted snapshot envelope
  -> SHA-256 checksum
  -> BackupStore.Put
  -> S3-compatible or WebDAV adapter
```

Restore performs the reverse sequence, verifies the checksum and envelope version, decrypts locally, and invokes a database-specific `SnapshotRestorer`. Only one restore may run at a time; the production restorer must acquire an advisory lock and place the API in maintenance mode before replacing data.

## Concurrency State

Implemented in this slice:

- Repository writes use database uniqueness constraints for mailbox/provider identity and backup target names.
- Memory repositories use read/write locks and are covered by the Go race detector.
- Credential writes compare the persisted `version` and reject stale updates.
- Schema migration uses a PostgreSQL advisory lock.
- A queued backup run is executed with the same run ID, and cancellation still records terminal failure state.

Required in the worker/provider phase:

- Claim scheduled backup runs with `FOR UPDATE SKIP LOCKED`.
- Derive provider-provisioning idempotency keys from internal resource IDs.
- Obtain a per-credential advisory lock around remote token refresh.
- Use a global restore advisory lock and maintenance mode while replacing database state.
