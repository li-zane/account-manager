# Legacy Reference Boundary

Account Manager is a new project. The previous private repository
`li-zane/mail-manager` is used only to compare existing behavior and import
formats while Account Manager evolves its own domain model and APIs.

## Local Reference

- Local path: `references/mail-manager/`
- Reference branch: `dev-refactor`
- Baseline at extraction: `f1bd354`
- Repository status: excluded by the root `.gitignore`

The reference clone is not a Git submodule and is not included in Account
Manager commits or Docker build contexts.

## Reusable Behavior

- Outlook/Hotmail import formats and Graph/IMAP retrieval behavior.
- Existing mailbox-code parsing and proxy compatibility findings.
- Cloudflare-routed and forwarding-mail workflows.
- Account and mailbox fields needed for migration-compatible import.

## New-Project Ownership

- Provider-neutral mailbox, alias, account, and pickup-key identities.
- Go service/port/adapter boundaries and PostgreSQL migrations.
- Account Manager REST contracts and React UI.
- Secret encryption, key hashing, concurrency policy, backup, and restore behavior.

Compatibility work should be implemented through adapters or explicit import
formats. New application code must not import or execute modules from the
reference repository.
