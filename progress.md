# Progress

## 2026-07-24

- Resumed from the existing dirty worktree without reverting prior implementation.
- Confirmed background token refresh is disabled locally and identified remaining request-triggered refresh paths.
- Began the single shared RT model and refresh-gate regression work.
- Added inbox/junk cached viewing and synchronization controls to the implementation scope.
- Added automatic pickup-key issuance and per-mailbox format export to the implementation scope.
- Implemented the refresh hard gate, canonical Microsoft RT detail model, automatic encrypted pickup-key issuance, message cache/sync worker foundations, inbox UI, click-to-copy address interaction, and domain-only forwarding fields.
- Resumed with frontend type validation, capability-state accuracy, Xinlan feature mapping, broader regression tests, and responsive verification still pending.
- Applied PostgreSQL migrations 5 and 6 with all refresh, message-probe, and backup workers disabled.
- Confirmed five of five imported Outlook fixtures use one shared RT with Graph/IMAP configured; both Graph and IMAP AT timestamps are present for every fixture and are currently expired.
- Confirmed five of five Microsoft mailboxes have automatically issued, exportable platform pickup keys.
- Started read-only browser verification against the persistent local database at `http://127.0.0.1:5175` and confirmed the desktop mailbox table, alias expansion, RT labels, and click-target address presentation.
- Verified address-name clipboard copying and feedback in the browser, then opened an Outlook detail drawer through the unique row action.
- Verified the desktop Outlook detail drawer visually and structurally: shared RT, disabled auto-refresh, no fixed RT expiry, three configured channels, per-channel AT timestamps, and no horizontal overflow.
- Verified the 390x844 mailbox card layout: seven visible primary/alias records, wrapped clickable addresses, readable RT/channel state, stable mobile navigation, and no horizontal overflow.
- Verified the Outlook detail drawer at 390px after animation: it occupies the exact viewport width, keeps content readable, and has no horizontal overflow.
- Verified the mobile cached inbox controls without network retrieval: IMAP selection, Junk selection, cache status, and local search all update correctly; the manual pull action was intentionally left untouched.
- Verified per-mailbox export selection and format availability: one mailbox is selected, universal address/pickup-key export is the default, required sensitivity is enforced, and Outlook four-part remains available.
- Re-ran `go test ./...`, `go vet ./...`, `go test -race ./...`, `npm run typecheck`, `npm run build`, and `docker compose config --quiet`; all passed.
- Added concurrent automatic pickup-key coalescing and PostgreSQL migration 7 for cross-instance uniqueness, applied it locally, and reran the complete verification matrix successfully.
- Committed the mailbox retrieval and management work as `a87c4f0` and pushed it to the private `li-zane/account-manager` repository on `main`.

## 2026-07-25

- Started the Microsoft retrieval upgrade from the existing dirty REST-removal worktree.
- Updated the persistent task plan for AT reuse, capability probing, Graph/IMAP incremental sync, cache cleanup, frontend refinements, and controlled live verification.
- Added the revised built-in format set, empty-input examples, sensitive export preview, and deployment verification to the implementation scope.
- Added capability/cursor migrations, method-specific AT repair, Graph delta and IMAP UID sync, parent-mailbox alias reuse, cache purge/cleanup workers, and capability probing workers.
- Moved the backup panel out of mailbox management, centered the inbox dialog, and added format examples with Outlook import defaults.
- Backend `go test ./...`, frontend `npm run typecheck`, and frontend `npm run build` pass.
- Corrected automatic cache cleanup to delete messages that exceed either retention age or per-folder count, with memory-repository regression coverage.
- Removed Register 6-part, GPT simple, and Cloudflare-routed formats from backend and frontend built-in seeds; migration 9 disables legacy database rows.
- Renamed the retained formats to `Outlook 邮箱凭证` and `平台取件格式`, added empty-input examples, and confirmed the selector contains exactly those two built-ins.
- Deployed the rebuilt backend at `http://127.0.0.1:18083` and retained the Vite frontend at `http://127.0.0.1:5175`.
- Final live API verification confirmed one Graph-only mailbox, one Graph+IMAP mailbox, and three IMAP-only mailboxes; all five keep stored Graph and IMAP AT metadata, but the UI exposes only verified methods.
- Repeated Graph and IMAP incremental sync successfully: Graph retained its delta cursor and IMAP retained UIDVALIDITY/highest UID.
- Sensitive preview verification confirmed five current client IDs, five RTs, and five platform pickup keys. Four historical Outlook passwords were restored from a prior local export; the fifth source row has no password.
- Desktop and 390x844 browser checks confirmed centered/full-screen inbox layout, no horizontal overflow, backup settings relocation, server-backed sensitive previews, and zero console errors after adding the favicon.
- Final `go test ./...`, `go vet ./...`, `npm run typecheck`, `npm run build`, `git diff --check`, and `docker compose config --quiet` all pass.
- Added Microsoft Graph refresh compatibility: try the explicit `Mail.Read offline_access` scope first, then retry `https://graph.microsoft.com/.default` on `invalid_grant`; transient empty Graph responses are retried once.
- Re-verified `sandrapickering2638@outlook.com` against the deployed backend. Its Graph refresh, Graph delta request, and saved delta cursor succeed; its current IMAP XOAUTH2 login returns Microsoft `BAD Command Argument Error. 12`.
- Changed Microsoft overview and inbox controls to show only retrieval methods with a persisted `verified` capability. The target mailbox now displays only `Graph`, while a different dual-verified fixture displays both `Graph` and `IMAP`.
- Re-ran `go test ./...`, `go vet ./...`, `npm run typecheck`, `npm run build`, `git diff --check`, and `docker compose config --quiet`; all pass after the final compatibility changes.
- Browser-triggered Graph incremental sync at `07/25 13:56` completed with zero console errors. The target overview response is active with `modes: ["graph"]`.
- Diagnosed the target's empty inbox: direct Graph retrieval sees one May 9 INBOX message, but the default 30-day cache cleanup removed it; Junk contains zero messages and incremental sync correctly reports no newer changes.

## 2026-07-26

- Replaced automatic cache retention cleanup with explicit Settings management; cached mail now remains until an administrator deletes a required time range.
- Added first-import background backfill for INBOX and Junk. Provider-native cursors are checkpointed per batch; providers without native cursors page backward by `before` time and later poll from `last_message_at - 1s` with idempotent upsert.
- Added time-bounded retrieval and recovery across Graph (`receivedDateTime`), Gmail (`after:`/`before:`), and IMAP (`SINCE`/`BEFORE`).
- Added Settings cache query by mailbox/folder/keyword/time, CSV export, required-range deletion, and upstream range recovery.
- Completed HTML message rendering in a sandboxed iframe, inline CID/attachment image materialization, complete message metadata, protocol labels, mailbox copy, and clickable verification-code tags.
- Fixed range recovery to persist the actual automatically selected Graph/IMAP method. Recovered the May 9 target message again and confirmed it now reports `microsoft_graph`.
- Tightened verification-code extraction using Chinese, Japanese, and English keywords; verified a real Japanese ChatGPT code and removed prior year/word false positives.
- Added a 501-message service regression proving initial fallback backfill crosses the 500-message batch boundary, plus Graph-to-IMAP recovery provenance coverage.
- Rebuilt and deployed the backend at `http://127.0.0.1:18083`; retained Vite at `http://127.0.0.1:5175`.
- Verified the centered desktop reader, 390x844 full-screen reader, responsive cache settings, complete remote/inline images, code copy feedback, and zero incoherent overlap.
- Refined the mailbox reader into a non-redundant list/detail layout and removed the Auto/Graph/IMAP request selector; each message now carries only its actual retrieval protocol.
- Added persistent local read state through migration 10 and the authenticated `POST /api/v1/mailboxes/{mailboxID}/cached-messages/{messageID}/viewed` route, including HTTP and provider-upsert regressions.
- Improved code extraction for Chinese, English, and Japanese messages, including numeric, alphanumeric, and separated six-digit values while filtering common URL, UUID, and year noise.
- Confirmed copy feedback changes the verification-code icon to `Check`; mailbox, sender, recipient, and code controls all compute to `cursor: pointer`.
- Browser-tested one real unread cached message: unread marker count changed from 1 to 0 on open and remained 0 after page reload and inbox reopen.
- Removed unused legacy inbox method/sync/search CSS and saved the final desktop inspection at `output/playwright/reader-desktop-final.png`.
- Final `go test ./...`, `go vet ./...`, `npm run typecheck`, `npm run build`, `git diff --check`, and `docker compose config --quiet` checks pass; backend `18083` and frontend `5175` both return HTTP 200.

## 2026-07-27

- Moved the selected message subject to the first row of the reading header and added a restrained pink title accent.
- Repositioned receipt time directly below the subject with a clock icon, explicit label, and stronger date typography; retrieval protocol now follows as secondary metadata.
- Increased cached-list time contrast and weight for faster scanning.
- Verified the updated header at 1440x900 and 390x844. The mobile title wraps above time and protocol, the dialog remains exactly 390px wide without overflow, and the browser console reports zero errors.
- Frontend `npm run typecheck`, `npm run build`, and targeted `git diff --check` pass.
- Added consistent bordered metadata fields for sender, recipients, and CC, with subtle hover feedback while retaining the existing copy interaction.
- Verified the metadata fields as equal desktop columns and stacked mobile rows; long addresses remain visible and the 390px dialog has no horizontal overflow.
