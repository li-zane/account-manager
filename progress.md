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
- Confirmed five of five imported Outlook fixtures use one shared RT with Graph/REST/IMAP configured; both Graph and IMAP AT timestamps are present for every fixture and are currently expired.
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
