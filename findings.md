# Findings

- The local PostgreSQL refresh setting and ignored `.env` worker switch are already disabled.
- `MessageRetrievalService.Retrieve` still calls `ensureFreshCredential` and can force refresh after `ErrUnauthorized` without consulting settings.
- `MailboxDetailService` publicly exposes generic, Graph, and IMAP refresh-token fields even though Microsoft uses one shared RT.
- Existing real-data validation completed before this change; this session must use only local structural and automated tests.
- New scope requires a durable mail cache with inbox/junk views and incremental/manual/periodic synchronization.
- Forwarding-recipient information belongs only to domain-routing mailboxes, not Outlook mailboxes.
- Existing pickup keys are digest-only, so format export requires a separately encrypted token copy; plaintext storage is not acceptable.
- Pickup-key issuance must become an idempotent mailbox lifecycle operation instead of a row action.
- The overview endpoint currently derives access modes only from the mailbox provider, so every Microsoft mailbox is presented as if the same capabilities were confirmed.
- The detail endpoint derives retrieval methods from credential kind and the React drawer labels each method as configured; neither layer distinguishes configured, verified, failed, and unknown.
- Microsoft RTs generally do not expose a fixed expiry timestamp. Stored Graph/IMAP expiry values are access-token lifetimes and must not populate the RT expiry column.
- A method-specific cached access token with an expiry is usable evidence of prior channel verification without issuing a new network request; credential kind alone is only configuration evidence.
- Xinlan's public product/help pages describe the relevant mailbox-management baseline as bulk import/validation/login/retrieval/export, INBOX plus junk/all-folder IMAP retrieval, scheduled polling, local mail storage, sender/subject/body search and extraction, custom servers/proxies, and an HTTP API. Account Manager already covers the provider-neutral API, imports/exports, cached polling, and folder viewing; local search and explicit channel verification state are the immediate UI gaps.
- Xinlan accepts Microsoft address/password/client-id/refresh-token imports and treats Microsoft token login separately from normal POP3/IMAP passwords. This matches Account Manager's canonical shared RT plus capability labels; it does not justify modeling two independent RT strings.
- The local database contains five Microsoft primary mailboxes and two aliases. All five credential rows are `microsoft_dual_token`, so all five are configured for the shared-RT dual-channel model; prior redacted live results were Graph 5/5 and IMAP 4/5, so configuration must not be presented as universal per-mailbox verification.
- The persisted token-refresh setting is `enabled=false`, and the process kill switch is also disabled in `.env`.
- Recording every retrieval result on `mailbox_credentials` conflicts with that row's token-refresh optimistic version. Per-channel operational verification needs its own status record; credential metadata is read-compatible, but retrieval does not mutate it in this change.
- Migrations 5 and 6 applied successfully. All five Microsoft mailboxes now have an active exportable encrypted pickup-key copy.
- Read-only API inspection confirms all five imported Microsoft fixtures use `microsoft_dual_token`, contain one shared RT, expose Graph/IMAP as configured capabilities, and keep automatic refresh disabled.
- All five Graph and IMAP short-lived AT expiry fields are populated but currently expired. These timestamps are channel-specific AT evidence, not RT expiry dates.
- Desktop browser inspection at 1440x900 confirms that the mailbox address itself is a button, the table shows Graph/IMAP, aliases remain expanded under their parent, and RT status reads `no fixed expiry` without inventing an expiration date.
- Browser interaction confirms clicking an address writes the exact address to the clipboard and shows a visible copy-success toast. The row-scoped details action resolves uniquely and opens the Outlook detail drawer.
- Desktop detail inspection confirms one masked shared RT, automatic refresh off, and Graph/IMAP configured retrieval capabilities. Each capability renders its own short-lived AT timestamp; the drawer stays within the viewport and introduces no page-level horizontal overflow.
- At 390x844 the app switches to mailbox cards, keeps all seven visible primary/alias records, preserves clickable wrapped addresses and fixed bottom navigation, and has no page-level horizontal overflow.
- The 390px detail drawer renders the shared RT and all three channels. Its first animation frame temporarily extends the transformed box; after the 220ms transition it settles exactly at `left=0`, `right=390`, with `scrollWidth=390` and no horizontal overflow.
- The cached inbox opens without upstream retrieval. At 390px it exposes Auto/Graph/IMAP channel controls, INBOX/Junk tabs, a manual pull button, cache status, and local search. Switching to IMAP and Junk and filling a search query updates local UI state without invoking the pull action.
- Per-mailbox export opens with exactly one main mailbox selected. The default universal format is `Email and platform pickup key`, automatically locks sensitive export on because the pickup key is required, and previews `邮箱----[由服务端导出]`; Microsoft-only Outlook four-part is also listed.
- Automatic pickup-key issuance now coalesces concurrent calls per mailbox and migration 7 adds a cross-instance partial unique index. The local database reports zero duplicate active encrypted automatic keys and still has one exportable key for each of the five Microsoft mailboxes.

## 2026-07-25 Microsoft Retrieval Upgrade

- The working tree contains the completed Outlook REST removal and Graph scope normalization changes; these must remain intact.
- The current project already stores encrypted Graph/IMAP AT values and expiry timestamps, but capability state, durable delta/UID cursors, automatic fallback, and cleanup still require implementation.
- The user explicitly requested live verification in this task, replacing the previous session's no-upstream-call constraint. Secret values must stay out of logs and reports.
- Existing building blocks include encrypted per-method AT fields, `mailbox_cached_messages`, `mailbox_message_sync_states`, a probe/polling worker, cache HTTP APIs, and a full `BackupSettings` React component.
- The implementation gaps align with the approved design: capability state has no dedicated writer/table, request routing is not capability-aware, sync state is timestamp-only, and cache cleanup/purge is absent.
- Backup relocation is primarily an App/navigation composition change rather than new backup functionality.
- `MessageRetrievalService.Retrieve` currently gates request-time refresh and unauthorized retry on the proactive refresh setting; request-time token repair therefore stops when the background switch is disabled.
- Microsoft `MailRetriever.Refresh` refreshes a whole credential without receiving the selected retrieval method, so a dual credential cannot independently refresh Graph and IMAP through the current port.
- Cache synchronization persists only `LastMessageAt`; Graph delta links, IMAP UIDVALIDITY/highest UID, provider method, and deletion tombstones are not modeled.
- `MessageProbeWorker` schedules every parent and alias independently. The parent mailbox should be the sole upstream sync target while aliases remain local cache filters.
- Import results already include the affected `MailboxIDs`, making them suitable for seeding asynchronous capability rows immediately after the database commit.
- The frontend contains separate `BackupPanel` and `BackupSettings` components; the mailbox view currently receives backup callbacks, while settings can host the complete backup surface.
- The import dialog initializes to the first built-in format and does not choose a Microsoft-specific default. The built-in Outlook four-part label is currently `Outlook 4-part` in migration 2 and frontend fallback data.
- The latest UI scope removes Register 6-part, GPT, and Cloudflare-routed built-ins; renames the universal address/platform-key format to `平台取件格式`; adds empty-input examples; and requires real sensitive values in export previews.
- The third removable GPT format is `fmt_builtin_simple3` (`Email, GPT password and mailbox password`). The retained built-ins are `fmt_builtin_outlook4` and `fmt_builtin_pickup2`.
- `go-imap` exposes `SearchCriteria.Uid`, and read-only `Select` returns `UidValidity` and `UidNext`, which supports durable incremental UID synchronization without another dependency.
- Backend unit tests and frontend typecheck/build pass after the retrieval, capability, cache cleanup, layout, and format changes.
- Deprecated format definitions are absent from backend and frontend built-in seeds. Migration 9 disables old database rows, and service-level filtering prevents legacy rows from reappearing.
- Automatic cache cleanup now uses `received_at < retention_cutoff OR folder_rank > max_per_folder`; the previous query ranked only old rows and required both constraints.
- Live deployment reports exactly two formats. Sensitive server previews contain all five current client IDs, RTs, and pickup keys; four of five Outlook passwords were recovered from a prior local export, while the remaining original row has an empty password.
- Live Graph incremental sync returns eight cached messages with a retained delta cursor. Live IMAP incremental sync returns cached data with retained UIDVALIDITY and highest UID.
- Current capability results are one Graph-only verified, one Graph+IMAP verified, and three Graph-failed/IMAP-verified mailboxes. Graph-first routing therefore has both a real Graph path and real IMAP fallback fixtures in the current database.
- The current Graph scope is `Mail.Read offline_access`; sending through Graph needs new `Mail.Send` consent before a controlled send test.
- Browser QA confirms desktop inbox center delta `[0,0]`, mobile inbox bounds `390x844`, no horizontal overflow, backup controls only under Settings, and zero console errors after the favicon addition.
- `sandrapickering2638@outlook.com` has a matching Microsoft identity and a reusable shared RT. Its consented app grant accepts Graph `/.default`, so the project now falls back to that scope when an explicit Graph `Mail.Read offline_access` refresh returns `invalid_grant`.
- The target's Graph delta endpoint succeeds and stores a durable cursor. A later incremental request can legitimately return zero new messages while still advancing the last-probed timestamp; this is successful incremental behavior, not an empty-mailbox diagnosis.
- A stored field named IMAP AT is not proof that Microsoft accepts it for IMAP. The target's refreshed IMAP XOAUTH2 attempt against `outlook.office365.com:993` returned `BAD Command Argument Error. 12`, so its persisted IMAP capability remains failed.
- X1's legacy Microsoft "Graph" branch can fall back to deprecated Outlook REST for Outlook-resource scopes. Successful retrieval there therefore does not by itself prove that the same token works with Microsoft Graph or IMAP XOAUTH2.
- Microsoft Graph and Outlook IMAP use different audiences/scopes and normally require separate AT values: Graph uses `https://graph.microsoft.com/...`; IMAP uses `https://outlook.office.com/IMAP.AccessAsUser.All`. The same shared RT may mint each only when the original app/user consent includes the corresponding permission.
- Final browser QA confirms capability-based presentation: the target's table cell is `Graph`, and its inbox selector contains `Auto` plus `Graph` with no IMAP control. A separately dual-verified mailbox continues to show both methods.
- The target mailbox's empty cached inbox is explained by retention, not Graph retrieval: a direct Graph read returns one INBOX message dated `2026-05-09T09:36:36Z` and no Junk messages, while the cache worker retains 30 days. The old cached row was deleted and the valid delta cursor intentionally remained at the latest upstream position.

## 2026-07-26 Message Synchronization And Cache

- Graph delta links are mailbox-folder change cursors, independent of whether another client reads a message. Read-state changes can appear as updates; a newly delivered message is not skipped because it was opened elsewhere.
- IMAP incremental synchronization uses UIDVALIDITY plus the highest processed UID. A UIDVALIDITY change invalidates the cursor and triggers a clean folder rescan.
- Gmail API supports native mailbox history IDs; the current bounded retrieval path also supports provider-side `after:` and `before:` filters. Generic fallback synchronization remains available for adapters without native history.
- Non-native incremental synchronization requires an overlap window and database identity deduplication. This implementation pages initial history backward with a `before` cursor, then polls from one second before the newest cached receipt time.
- Graph, Gmail API, and IMAP all accept bounded date queries, so manually deleted cache ranges can be restored from upstream while the provider still retains those messages.
- Range recovery must carry the concrete automatically selected channel into cache provenance. Returning only messages from an auto-routed retrieval loses this information when Graph falls back to IMAP.
- Email HTML needs provider-specific attachment completion before rendering: Graph inline attachments, Gmail attachment IDs, and IMAP CID MIME parts are materialized before a sandboxed iframe displays the document.
- Verification-code extraction is proximity-based rather than a generic numeric scan. Four-digit years and alphabetic fragments are excluded; Chinese, Japanese, and English code labels are recognized.

## 2026-07-26 Mailbox Reader Refinement

- The reference mail-manager uses a useful two-column list/detail hierarchy, but repeats code, sender, time, and copy actions between its list, tags, metadata, and action row.
- The current reader already has the correct full-HTML iframe boundary. The refinement should keep that renderer and improve the surrounding information hierarchy instead of replacing it.
- `CachedMessage.Unread` currently mirrors only upstream state. Selecting a cached message performs no write, so its red dot remains. A separate persisted `viewed_at` is needed so later provider upserts do not restore the local unread marker.
- The current method segmented control is request-oriented UI. Automatic routing already selects the preferred verified channel, so the reader only needs the concrete `retrieval_method` provenance attached to each cached message.
- Copyable controls currently use CSS `cursor: copy`, which displays a plus-shaped copy cursor on some platforms. Standard `cursor: pointer` gives the requested interaction without that glyph.
- A local `viewed_at` override must be preserved during provider upserts because upstream unread state and this application's viewed state are separate concerns.
- The authenticated viewed route returns the persisted timestamp on the next cache query; browser reload and inbox reopen both preserve the removed unread marker.
- Keeping the message-opening button separate from the verification-code copy button avoids nested interactive controls while preserving the entire subject/body preview as a large click target.
- Final desktop and 390x844 browser checks show no channel selector, no duplicated metadata block, and no layout overlap; copy controls expose the standard pointer cursor and immediate check-icon feedback.
- Putting the subject before all metadata makes the selected message immediately identifiable. Receipt time works best as the first secondary datum with its own icon and typographic contrast, while retrieval protocol remains a compact trailing badge.
- At 390px, stacking time and protocol below a wrapping subject preserves the intended order and keeps both the page and dialog at `scrollWidth=390`.
- Sender and recipient metadata benefits from restrained individual outlines: it makes copyable values discoverable without competing with the subject or verification code. The existing two-column-to-one-column grid handles long addresses without another responsive breakpoint.
