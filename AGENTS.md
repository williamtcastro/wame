# AGENTS.md

Guidance for AI coding agents working on this repo. Human-facing docs are in `README.md` and `docs/`.

## What this is

`wame` (WhatsApp manager: TUI & bot) is a single Go binary that links to a WhatsApp
account through [whatsmeow](https://github.com/tulir/whatsmeow) and exposes it as an HTTP API, an MCP
server, a Bubble Tea TUI and a one-shot CLI. `wame admin` is a second Bubble Tea app: a remote admin
panel that talks to a running server only over the HTTP API. All server state lives in one SQLite file
(default `~/.config/wame/wame.db`); it also ships as a Docker image.

## Layout

Go's "server project" layout: the `wame` command at the root, everything else under `internal/`.

| Path | Package | Responsibility |
|---|---|---|
| `main.go` | `main` | subcommands (`login`, `serve`, `tui`, `send`, `token`, `mcp`, `admin`, `queue`, `backup`, `version`, `logout`; `admin`, `queue` and `backup` never call `bot.New`; `serverFlags.resolve` is the one server/token resolution for `admin` and `queue`: `-profile`, flags → env → `./.env` → `admin.env`), flags, env vars, `usage`, the default DB path (`defaultDB`), `newLogger` (slog setup), `run` (shared setup: logger, `bot.New` with `defer b.Close()`, `StartWebhook`, then `Bot.InitSettings`: stored runtime settings win over flags) and one function per command (`serveCommand`, `tuiCommand`, `sendCommand`, `logoutCommand`, …), `serve` (graceful: `Shutdown` with a 5 s limit, `RegisterOnShutdown(StopStreams)` so open streams end at once), `adminCommand` (`-profile`, flags → env → `./.env` → `admin.env`, never opens the DB), the startup guard `guardPublicAddr` (owner secret ≥ 16 chars; non-loopback needs an owner secret or any token ever created; `isLoopbackAddr`), `tokenCommand`, `mcpCommand` (stdio MCP), `sendAtCommand` / `scheduledSend` (`wame send -at WHEN [-before WHEN]` goes over HTTP to a running server through `serverFlags.resolve`, like `queue`, since a process can't wait for the time; without `-at` the old `sendCommand` runs), starting `Bot.RunOutbox` (through `b.Go`) in `serve` and `tui` |
| `queue.go` | `main` | `wame queue import|status` over the HTTP API (never the DB): local checks with `bot.ReadQueueCSV` and `CheckQueueRequest`; `chunkRows` (≤ 1000 rows and 15 MB, deterministic); per-chunk keys `KEY.n` (`-key`, else a hash of the file's absolute path; the default batch comes from the key, so fixing a row and running again replays what's queued instead of sending it twice, while changing an already-queued row is a 409); `fileError` maps chunk lines back to file lines; prints "already queued N" from the response's `replayed`; `-template`, `-dedupe` (over the whole file), `-skip-sent`, and the skipped report; `-send-before` (default `send_before`) and `-spread` (`spreadRows`: offsets over the whole file before chunking, so a rerun gives the same `send_at`s; needs a `send_at` on every row, refuses `tag:` rows); `pick` drops the optional CSV columns no row uses (`name`, `send_before`) so older servers still take the file; `queue status` prints `expired`; `-file PATH|ID` (`checkAttachment` before any network call, `uploadAttachment` once per run, the file's identity in `importKey` so another file is another batch) |
| `docs/docs.go` | `docs` | `//go:embed openapi.yaml` as `docs.OpenAPI` (embed can't reach a parent directory, so it lives next to the file) |
| `internal/store/store.go` | `store` | `Store` (wraps `*sql.DB`, owns the LID relink cache key), `Open` (each table's migration), `wame_messages` table and its migration from the old rowid schema, `Save`, `Import` (history sync, incl. edits), `Query`/`Messages`/`Get`/`GetBySeq`/`LastSeq`, `MarkEdited` (never on a deleted message)/`MarkDeleted`, `relinkLIDs` (one transaction, one at a time; `relinkLater` marks it due), `Chats` (names resolved after the cursor closes, since that may hit the network). Read methods return their errors; `placeholders` builds IN lists |
| `internal/store/message.go` | `store` | `Message` (raw fields exported but `json:"-"`), `Media`, `MessageUpdate`, `Chat`, `ChatsChanged`, `ErrNotFound`, `Message.MediaFileName` |
| `internal/store/filter.go` | `store` | `Filter`, `ParseFilter`, `Match` and its SQL twin `where` (shared by `/messages`, `/messages/wait`, `/events`, webhook), including the `q` text search (`textMatches` in Go mirrors the FTS query) |
| `internal/store/state.go` | `store` | `migrateState`: `wame_chat_state` (read positions, `MarkReadUpTo`/`MarkChatRead`/`ReadSeq`), `wame_groups` and its `wame_group_members` pivot (`SaveGroups` syncs members: upsert, drop the ones that left; `Groups`, `GroupsWith`), the `wame_messages_fts` index and its triggers, `ftsQuery`; the `ChatRead` event |
| `internal/store/audit.go` | `store` | `wame_audit` table, `AuditEntry`, `Store.Audit`, `Store.AuditLog` |
| `internal/store/settings.go` | `store` | `wame_settings` (runtime overrides: key, value, updated_by, updated_at), `Settings`, `SetSettings` (nil deletes) |
| `internal/store/tokens.go` | `store` | `wame_tokens` table, `Token`, `Token.Usable` (the one revoked/expired check, used by auth, open streams and the outbox), `InsertToken` (`ErrTokenName` / `ErrTokenID`), `TokenByID`, `Tokens`, `RevokeToken` (cascades to tokens it created), `TouchToken`, `HasTokens` (any row ever) |
| `internal/store/outbox.go` | `store` | `wame_outbox` (the send queue): `OutboxItem`, states, `OutboxFilter`, `OutboxCounts` (`Of`; includes `expired`), `Enqueue` / `EnqueueWith` (`INSERT … ON CONFLICT DO NOTHING RETURNING`, per-actor keys; new columns `recipient_key`, `source_hash` and the file columns `file_id`, `file_kind`, `file_size`; an `EnqueueCheck` runs inside the transaction with the replay flags and the open count, so a 409 or the open limit rolls everything back; `send_at` keeps the requested time for replay comparisons, `RequestedSendAt`; scheduling columns `expires_at` (`OutboxItem.SendBefore`), `spread_ms` and `edited_at`), `ListOutbox`, `CountOutbox`, `DueOutbox`, `ClaimOutbox` (queued → sending, fixes the message id; refuses an item past its `expires_at`), `ExpireOutbox` (queued past `send_before` → `expired`, one `UPDATE … RETURNING`), `EditOutbox` (`OutboxEdit`: due, `send_before`, text, one `UPDATE … WHERE status = 'queued' RETURNING`; keeps the old text's hash in `source_hash`), `FinishOutbox` (`OutboxResult`; only on open items, so a cancel is never overwritten), `CancelOutbox` (one `UPDATE … RETURNING`), `RequeueSending`, `PruneOutbox` (sent, failed, canceled and expired) |
| `internal/store/outbox_skip.go` | `store` | `EnqueueOptions`/`EnqueueRow`, the skip-sent query over the outbox and (for callers with read) `wame_messages`, dedupe on recipient + file + text, `ErrKeyTaken`, the `recipient_key` index and its backfill |
| `internal/store/outbox_files.go` | `store` | `wame_outbox_files` (uploads for queued items), `InsertOutboxFile` (dedupe per actor before the quota, both in one INSERT), `OutboxFileBySHA`, `DeleteOutboxFile`, `ExpireOutboxFiles`, `DeleteOutboxFilesOf`, `WipeOutboxFiles`, `PruneOutboxFiles`, `LiveOutboxFiles`, the `wame_outbox_file_live` trigger, `IsOutboxFileGone` |
| `internal/store/optout.go` | `store` | `wame_optouts` (chats that asked not to get queued messages): `OptOut`, `AddOptOut` (keeps the first entry), `RemoveOptOut`, `OptedOut`, `OptOuts` |
| `internal/store/contacts.go` | `store` | `wame_contacts` (your own name, tags, notes, language, time zone per person, keyed by phone JID): `OwnContact`, `SetContact(ContactEdit)` (write lock first), `ImportContacts` (one tx, counts each person once), `DeleteContact`, `DeleteAllContacts`, `Contacts(ContactFilter)`, `ContactTags`, `ContactsGen` (bumped by every write and by relink, so the bot's name map knows when to reload), `relinkContacts`/`mergeContacts` (an `@lid` row merges into the phone row: phone values win, notes joined, tags capped at 20, by `relink`) |
| `internal/store/contactcache.go` | `store` | `wame_contact_cache` (what WhatsApp last said about a person, incl. the LID): `CachedProfile`, `PutCachedProfile` (refreshes), `CachedProfileOf`, `CacheAbout`/`CachePicture`/`CacheVerifiedName` (notifications: existing rows only), `PruneContactCache`, `ClearContactState` (logout: own contacts, cache, `wame_groups` and group members in one tx; opt-outs stay), `RelinkLater`. `ContactStats` (in `contacts.go`): messages, unread, groups in common, 7-day queue counts in one read tx |
| `internal/store/backup.go` | `store` | `Backup` (read-only open of the same path, `VACUUM INTO` a new 0600 file, never overwrites, checks the copy and removes a bad one), `BackupName`, `PruneBackups` (exact `wame-YYYYMMDD-HHMMSS.db` names only) |
| `internal/auth/auth.go` | `auth` | `Scope` (`read`/`send`/`admin`), `Principal` (`Has`, `Restricted`), `Owner()`, `Issue` (token format, reserved names, `DefaultRate`, child expiry), `Authenticator` (`Required` fails closed, `Authenticate`), `CheckOwnerSecret` (`MinOwnerSecret` = 16) |
| `internal/bot/bot.go` | `bot` | `Bot` type, `New`, `Start`, `PairPhone`, `Status`, pub/sub (`Subscribe`, `publish`), `Go` (every background goroutine runs through it) and `Close` (cancel, disconnect, wait, close the DB), `InputError`/`BadInput`, `SleepUntil`, DB file permissions |
| `internal/bot/events.go` | `bot` | whatsmeow event handler, incoming edits/deletes (`handleProtocol`), history import, auto-replies |
| `internal/bot/message.go` | `bot` | `classify`, `toMessage`, `PhoneJID`, `save` (store + publish) |
| `internal/bot/send.go` | `bot` | `SendText`, `SendFile` (`sendFile` split into `uploadMedia`, swappable for tests, and `fileMessage`), replies (`prepare`, `quote`), `deliver`, `sendControl`, throttling |
| `internal/bot/actions.go` | `bot` | `React`, `Edit`, `Delete`, `MarkRead`, `SetTyping` |
| `internal/bot/contacts.go` | `bot` | `Contacts`, `Check`, `Resolve`, `ParseRecipient`, the lookup limiter, `Chats`/`ChatName`, the group cache and admin list (`refreshGroups`, `loadCachedGroups`, the `NamesChanged` event) |
| `internal/bot/media.go` | `bot` | `DownloadMedia`, `limitedTransport` / `cappedBody` (100 MB cap) |
| `internal/bot/presence.go` | `bot` | presence (`-online`) and typing indicators |
| `internal/bot/audit.go` | `bot` | `Bot.Audit`, `Bot.audited` |
| `internal/bot/allow.go` | `bot` | chat allowlists for restricted tokens: `LocalChat` (no network), `ChatAllowed`, `ExpandAllowlist` (resolves the list's own phone entries, cached 10 min), `CanonicalChat` (unrestricted callers) |
| `internal/bot/limit.go` | `bot` | `Limiter`: per-minute and per-chat spacing (`plan` computes, `commit` books), `ReserveN`, `Peek` and `TryReserve` (the outbox: never waits, never counts as a 429), `RateLimitError` (429), `Set` (limits change at runtime; every read is under the lock), `Stats`, `LookupsPerMinute`, `ThrottleToken` / `tokenLimiter` (per-token rate). Three instances: `Bot.Limiter` (sends), `Bot.Lookups` (number checks, presence; 3 × rate), `autoLimit` (auto-replies, drop instead of queue) |
| `internal/bot/webhook.go` | `bot` | webhook queue and workers, retries with backoff, HMAC signing (`Sign`), `WebhookStats` (in memory), `Retry-After` on 429/503 (capped at 5 min), `StartWebhook`, `setWebhookFilter` (only when the raw filter changes; a `chat=` number is resolved in the background by `resolveWebhookChat`, however the filter was set), a short drain on `Close`, `TestWebhook`, `ReplayWebhook` (only ids in the recent failures) |
| `internal/bot/settings.go` | `bot` | runtime settings: `Settings`, `SettingKeys`, `InitSettings` (flags + stored overrides), `UpdateSettings` (validate all, store, apply, audit once), `SettingsView`, `ParseWebhookFilter`, the auto-reply switch (`SetAutoReply`/`AutoReplyOn`, atomic) |
| `internal/bot/outbox.go` | `bot` | the send queue: `Enqueue` / `QueueMessages` (wraps `Queue`; `sameRequest` compares `source_hash` and the file id; validation via `CheckQueueRequest`, per-actor cap and replay comparison inside the insert transaction, `ErrKeyReused` → 409, `Enqueued.Replayed`), `CancelQueued`, `EditQueued` / `QueueEdit` (`PATCH /queue/{id}`: reschedule, `send_before`, text; own items via the filter; `NotQueuedError{Edit}` → 409; audited `queue_edit` with field names only; `sameRequest` also compares an edited item's `editHash`), `QueueStats`, `RunOutbox` (one step per wake or timer: `expireQueued` first, on every step, also while paused, quiet or offline; resolve the recipient, take a slot under both the token's and the global limit, wait for it, `sendQueued` through `SendText` with the item's fixed message id; a file is uploaded before the slot, and a fresh upload ends the step), retries with backoff, the whole-queue cooldown after repeated errors, `QueueEvent` (published and sent to the webhook, never with text). See docs/QUEUE.md |
| `internal/bot/names.go` | `bot` | `NamesOf`: the one name rule (own > address book > business > `~push` > `+number`; groups and `Me` first), never a lookup; `ChatName` delegates to it. The in-memory own-name map (`loadOwn`, reloaded on `ContactsGen` changes), `phoneVariants` (Brazil's 9th digit, mobiles only, never a lookup), `InvisibleRune` / `StripInvisible` |
| `internal/bot/contactbook.go` | `bot` | contacts on top of whatsmeow's address book: `Contacts(ContactQuery)`, `ContactOf` (no lookup), `SetContact` (PUT: at most one lookup, keeps the typed number when WhatsApp doesn't know it), `PatchContact` (no lookup), `DeleteContact`, `DeleteAllContacts`, `ImportContacts`, `ExportContacts` (all audited, never with values), `ContactInput` (null vs absent, object bodies only), `localJID` (device suffix dropped) |
| `internal/bot/profile.go` | `bot` | `Profile(ctx, to, ProfileOptions{Full, Details, Refresh, Actor})`: names, own fields, LID (local map, then the cache), cached WhatsApp profile with `stale`, activity, queue, opt-out, tokens limited to the chat; never a lookup unless `Refresh` (admin: 2-4 lookups via `b.lookup`, once per person per 5 min, audited `contact_refresh`). The four whatsmeow calls sit behind `profileClient` (tests use a fake) |
| `internal/bot/sendtime.go` | `bot` | send times: `SendTime` (an instant, or `Local`: a date and time read on the recipient's clock; `Resolve(loc)`), `ParseSendTime(s, now, relative)` (RFC 3339, date-time without offset; with `relative` also `90m`/`3d` and `09:00`/`tomorrow 09:00`, refusing an instant more than a minute past), `ParseSendAt` (the API's fixed forms only), `Deadline` / `ParseDeadline` (`send_before`: a duration after the item's time or a time; `expiry` enforces after `send_at` and ≤ 7 days), `ParseSpread` / `SpreadOffset` (≤ `MaxSpread` = 24 h), `Bot.QueueLocation`, `zones` (contact `time_zone`, else `queue_timezone`; no lookups, one read per chat) |
| `internal/bot/queuecsv.go` | `bot` | `ReadQueueCSV` (named columns `to,text,send_at,reply_to,name,send_before` in any order, BOM/CRLF/quotes, `;` separators sniffed from the header, short rows, blank and whitespace rows skipped, unnamed columns ignored but values outside the named columns refused, line-numbered errors, refuses spreadsheet scientific notation and decimals in `to`), `QueueRecord`, `QueueColumns`, (send times are parsed by `sendtime.go`) |
| `internal/bot/queueopts.go` | `bot` | `Bot.Queue`/`QueueOptions` (tag expansion that respects the allowlist, `ParseSkipSent`, `rowKeys` with `KEY#i` / `KEY#i:CHAT`, fallback keys for a member's other forms, the number↔tag change refused as a 409); `scheduleRow` sets each item's `due`, spread offset and `send_before` on the recipient's clock; `attachQueueFiles` is called here after expansion |
| `internal/bot/template.go` | `bot` | `parseTemplate`, `CheckTemplate`, `renderTemplate`, `namesForTemplate` (never a push name for `{name}`), `sourceHash` |
| `internal/bot/outbox_files.go` | `bot` | queue files: `AddQueueFile` (upload limits, MIME sniffing, `CleanFileName`), `DeleteQueueFile`, `QueueFiles`, `attachQueueFiles`, `sameQueueFile` (at the quota, a hash-only read with no disk write returns the actor's identical file), the upload cache (`queueMediaFor`, `queuedFile`, `queuedFileFailed`; per-item ready uploads `setReady`/`readyFor`/`clearReady`/`clearAllReady`, at most `maxReadyMedia`; `dropCachedUpload` after a failed send, also from other items' ready entries; not on shutdown, a rate limit or offline), `sendQueuedItem`, `fileMessage`, `uploadError` (scrubs the upload URL's auth token), `pruneQueueFiles`/`sweepQueueFiles`, `wipeQueueFiles`, `ParseBytes`/`FormatBytes` |
| `internal/bot/optout.go` | `bot` | `optOutKeywords`, `noteOptOut` (an incoming 1:1 text that is only a keyword adds the chat; never answered), `checkOptOut` (worker and `deliver` for queued sends), `AddOptOut` / `RemoveOptOut` (audited) |
| `internal/bot/alerts.go` | `bot` | `Notifier` (pluggable backend), `Gotify` (token only in `X-Gotify-Key`; errors cleaned of the address), the alerter (disconnected after `-alert-delay`, logged out at once, recovered, rate-limited queue and webhook alerts), `SetAlerts`, `RunAlerts`, `AlertStats`, `TestAlert` |
| `internal/bot/logs.go` | `bot` | `LogBuffer`: ring of the newest app (slog) records behind `GET /logs`; `Wrap` passes records through unchanged; secret-looking keys redacted, `Scrub` replaces the owner secret and webhook URL/secret anywhere |
| `internal/bot/usage.go` | `bot` | per-caller usage in hourly buckets for 24 h (`RecordRequest` from the API/MCP auth, sends from `audited`), `Usage` |
| `internal/api/api.go` | `api` | `NewHandler` (the route table: every route wrapped in `need(scope, …)`), handlers, long-poll, SSE (with token re-checks), `/media`, `/mcp` mount, the `/docs` page, `sendText` (a `send_at`, `send_before` or `batch` makes it a queue item: `Bot.Queue` with key `send:KEY`, answered 202 by `writeQueued`; `/send/file` has no `send_at`), and the admin-only `/stats` (`Stats`, `CallerStats`), `/settings` (GET/PATCH; values may be strings, numbers, booleans or null) and `/webhook/test`, `/webhook/replay` (`webhookResult`: 200 with the attempt's outcome even when the receiver failed); SSE `queue` events (opt-in) |
| `internal/api/queue.go` | `api` | `/queue` routes: `enqueue` (16 MB body, per-message allowlist check; `template`/`dedupe`/`skip_sent`/`file` in, `skipped`/`skipped_rows` out), list/get/cancel, `editQueued` (`PATCH /queue/{id}`; `send_before` null or `""` clears it), `writeQueued` (the 202 answer shared with `/send`), `spread` from the body or `?spread=`; `own` (via `store.OutboxFilter.For`) limits callers without admin to their own items; `queueID` refuses ids ≤ 0 (404) before anything runs, since id 0 would mean "every queued item"; a cancel of an item that isn't queued is `bot.NotQueuedError` → 409; `/optouts` handlers |
| `internal/api/queuefiles.go` | `api` | `POST`/`GET /queue/files`, `DELETE /queue/files/{id}`, `writeQueueFileError` (413/415/409/507) |
| `internal/api/contacts.go` | `api` | `/contacts`: list (filtered to the allowlist for restricted tokens), tags (unrestricted only), one person as a `bot.Profile` (`authorizeChat`, never a lookup; full only for unrestricted admin; `?refresh=1` only for unrestricted admin, 403 before `Bot` is called), PUT/PATCH/DELETE, `DELETE /contacts?all=true`, CSV import (4 MB → 413) and export (audited) |
| `internal/api/metrics.go` | `api` | `GET /metrics`: hand-written Prometheus text format (`promWriter`); no per-token series |
| `internal/api/logs.go` | `api` | `GET /logs` (admin; `level`, `since`, `before`, `limit`) |
| `internal/api/authz.go` | `api` | `withAuth` (header/`?token=` rules, failed-login throttle, `seen` callback → `Bot.RecordRequest`), `need`, `unrestricted`, `visibleStatus`, `authorizeChat`, `restrictFilter`, `allowedChat` (the non-writing check), `throttle` (the per-token rate; sends call it inside the idempotent closure, so a retried key doesn't spend a slot), the failed-login limiter (never adds an entry on lookup, prunes stale IPs instead of resetting), stream cap (per token ID) and open-stream counts (per name, owner included) and `stillValid`, `/tokens` handlers, `NormalizeChats` |
| `internal/api/mcp.go` | `api` | `/mcp`: streamable HTTP MCP handler; bearer verification puts the `Principal` in the SDK's `TokenInfo`; token-less mode via `localOnly` |
| `internal/api/middleware.go` | `api` | `localOnly` (token-less mode: loopback RemoteAddr and Host, no cross-site), request logging |
| `internal/mcpserver/server.go` | `mcpserver` | MCP tools (`status`, `list_chats`, `read_messages`, `search_messages`, `find_contact`, `send_message`, `react`, `mark_read`), server instructions, `require`/`chat` checks, `clean()` of untrusted text |
| `internal/mcpserver/contacts.go` | `mcpserver` | `contact_profile` (read scope, `Profile` with `Details` only, never a refresh or `CanonicalChat`: `personChat`; `lid` at the top level through `cleanID`, names, about and group names under `untrusted`; restricted callers see only allowlisted groups; never notes, picture URL, business, queue, opt-out, tokens or WhatsApp errors) |
| `internal/mcpserver/queue.go` | `mcpserver` | MCP queue tools (`schedule_message` with `tag:`, `template` and `file`; `list_scheduled` with `kind`/`file_size`; `cancel_scheduled`; `send_before`, `spread` and local-time `send_at` on `schedule_message`; `reschedule_scheduled`: `EditQueued` with the caller's own items only, even for admins, `send_before: "none"` clears) over `Bot.Queue` / `CancelQueued`: send scope, `queueTarget` (allowlist without lookups), `ownFilter` (own items unless admin), `parseSendAt` (`bot.ParseSendTime` with the relative forms, on the queue's clock); text only for the caller's own items, under `untrusted` |
| `internal/api/idempotency.go` | `api` | idempotency keys (bound to a request fingerprint; reuse for a different request → 409 `errConflict`) |
| `internal/api/respond.go` | `api` | `readJSON` / `readJSONMax` (`http.MaxBytesReader`, 413 when too large), `writeJSON`, error mapping (`writeError`) |
| `internal/tui/model.go` | `tui` | `Run`, the Bubble Tea model, `Update`, loading chats and messages |
| `internal/tui/keys.go` | `tui` | key routing (`key`), focus, per-panel keys |
| `internal/tui/commands.go` | `tui` | `tuiHelp`, `tuiCommands`, slash commands, sending, saving media; `/later`, `/sendat`, `/scheduled`, `/reschedule`, `/unschedule` (`schedule`, `listScheduled`: `Bot.Queue` / `EditQueued` / `CancelQueued` with actor `tui`) |
| `internal/tui/clipboard.go` | `tui` | `copyTarget` / `copyNumber` (copy the selected chat's number) and `CopyToClipboard`, a wrapper of `termui.CopyToClipboard` |
| `internal/tui/complete.go` | `tui` | autocomplete (`suggest`, `pathSuggestions`, `chatTarget`) |
| `internal/tui/view.go` | `tui` | rendering: panes, sidebar, context bar, footer, styles |
| `internal/tui/format.go` | `tui` | text helpers; `safe()` / `safeLine()` wrap `termui` |
| `internal/termui/termui.go` | `termui` | shared by both terminal apps: `Safe` / `SafeLine` (strip ANSI, control and bidi characters from text strangers wrote), `UserOf`, the colour palette, `ListMove` (j/k, paging, g/G for any list), `Debounce` (coalesce reloads after bursts of events), `CopyToClipboard`, `ExpandHome`, `WriteNew` (O_EXCL 0600, never leaves a partial file), `SafeLines`, `IsPersonChat` |
| `internal/admin/client.go` | `admin` | `wame admin`: HTTP client for a remote server (`NewClient` refuses plain http beyond loopback), typed on the server's own types (`bot.Status`, `bot.SettingView`, `bot.WebhookResult`, `store.*`, `api.Stats`), `APIError`, and `Events`: the SSE reader (separate client without a timeout, 60 s idle cut-off; the server pings every 25 s), `UploadQueueFile` (streamed multipart; 200 vs 201 tells a repeat), `ListQueueFiles` (names blanked), `DeleteQueueFile` |
| `internal/admin/config.go` | `admin` | `ReadEnvFile` (`WAME_SERVER`, `WAME_TOKEN`, `WAME_INSECURE`), `Resolve` (precedence; a file's server and insecure flag need that file's token), `Profiles` (`~/.config/wame/servers/*.env`, both keys required) |
| `internal/admin/model.go` | `admin` | the panel's Bubble Tea model: `Options`, tabs, modes, polling, `call` (every server answer is tagged with the client generation in a `genMsg`, so answers from a server the user switched away from are dropped), `applyStatus` (bell on a healthy → unhealthy change), server switching |
| `internal/admin/live.go` | `admin` | the `/events` follower: reconnects with backoff, feeds `liveMsg`/`eventMsg` through a channel closed on stop; keeps time/chat/sender (never text) of the last hour's messages for the counts |
| `internal/admin/chats.go` | `admin` | Chats tab: list, messages, search, sending (a leading `@WHEN ` schedules: `splitSendAt`, `Client.SendAt` → `POST /send`), and `safe()` for text other people wrote |
| `internal/admin/tokens.go` | `admin` | Tokens tab keys, the create form, rotation (`nextName`, `rotationRequest`, create-then-revoke) and `unused` |
| `internal/admin/settings.go` | `admin` | Settings tab (`GET`/`PATCH /settings`), the Health tab's webhook keys (test, replay), and per-token usage from `/stats` callers |
| `internal/admin/queue.go` | `admin` | Queue tab (`GET`/`DELETE`/`PATCH /queue`: `t` reschedule, `e` replace the text through `Client.EditQueued`; an `expired` filter and a `before` column; pause via the `queue_paused` setting), queue events debounced to one reload per second; the files view (`F`) is in `queuefiles.go` |
| `internal/admin/health.go` | `admin` | Health tab (`GET /stats`), meters, and `grid`: panels laid out in equal-height rows |
| `internal/admin/logs.go` | `admin` | Logs tab: level cycle, `/` filter (shared `filterKey` with Audit), follow mode polling `/logs?since=` with a generation-tagged tick while the tab is open |
| `internal/admin/optouts.go` | `admin` | the Queue tab's opt-outs view (`o`): `GET`/`POST`/`DELETE /optouts`, shared list filter, add prompt, a remove confirm that captures the chat; the count in the Send queue card |
| `internal/admin/contacts.go` | `admin` | Contacts tab (9): list with `/` filter (sent to the server as `q` when the list is capped) and tag filter, the profile card beside the list (`bot.Profile`, everything through `safeProfile`; never the picture URL; a phase-1 answer is converted by the client), `W` refresh from WhatsApp (`RefreshContact`, `Retry-After` on 429, next refresh time), the edit form (PATCH with changed fields only, tags as add/remove; notes with line breaks are left out, never flattened), delete confirm capturing the JID, import (dry run, confirm, same bytes, regular files only) and export (`termui.WriteNew`), `p` from Chats; read-only when `/tokens` or a write answers 403 |
| `internal/admin/view.go` | `admin` | rendering: header (degrades by width), dialogs, the Status grid, tokens and audit tables |
| `Dockerfile`, `compose.yaml` | | static build into distroless `nonroot`; `/data` volume with `WAME_DB=/data/wame.db`, `WAME_ADDR=0.0.0.0:8080` (so a token is required). CI builds it and smoke-tests `version` and the no-token refusal. Docs: `docs/DOCKER.md` |
| `_demo/` | `main` | README screenshot: seeds a scratch DB with fake chats and opens the TUI without connecting; `screenshot.sh` records it with VHS into `docs/wame-tui.png`. Ignored by `./...`, built by CI. Rerun it after visible TUI changes |
| `docs/openapi.yaml` | | OpenAPI 3 spec, embedded into the binary (rebuild after editing) |
| `docs/*.md` | | user docs: CLI, API, TUI, MCP, ADMIN (the panel), DOCKER, writing bot logic, SENDING (a task-oriented guide to every message type) |
| `docs/security/` | | security reviews (`SCOPED-TOKENS.md`: token system threat model, findings and fixes) |
| `docs/proposed-features/` | | deferred proposals: a web admin panel (its terminal counterpart, `wame admin`, is built; the doc also holds the audit action table), TUI image previews, LLM context, drafts approval, rules and state, group tracking (`GROUPS.md`), contacts phase 4, and `BACKLOG.md`; contacts phases 1–3 (`CONTACTS.md`) queue files (`QUEUE-FILES.md`) and scheduled sends (`SCHEDULED-SENDS.md`) are built and kept as design records |
| `.github/workflows/ci.yml` | | CI on every push to `main` and every PR: gofmt, `go vet`, `go test -race` on Ubuntu and macOS, `CGO_ENABLED=0` build plus `_demo`; a `docker` job builds the image and smoke-tests it |
| `.env.example` | | the keys Compose reads (`.env` itself is git-ignored) and `WAME_SERVER` for `wame admin` |
| `.github/workflows/release.yml`, `.goreleaser.yaml` | | on a `v*` tag: tests, then GoReleaser builds linux/darwin × amd64/arm64 `tar.gz` archives plus `checksums.txt` on the GitHub release, stamping `main.version` via `-ldflags` |

**Dependency direction:** `main` → `admin`, `api`, `mcpserver`, `auth`, `tui`, `bot`, `store`; `admin` →
`api`, `bot`, `store` (for their types only: it never opens a database or a WhatsApp connection);
`api` → `mcpserver`, `auth`, `bot`, `store`, `docs`; `mcpserver` → `auth`, `bot`, `store`; `tui` → `bot`,
`store`, `termui`; `admin` and `tui` → `termui` (sanitizing, colours, list keys, debounce, clipboard;
it imports nothing internal); `auth` → `store`; `bot` → `store`; `store` → nothing internal. Never import upwards (Go
rejects cycles anyway). `store` knows nothing about whatsmeow's client: it deals in plain strings.

**Where new code goes:**

- SQL, schema, filters, anything that reads or writes `wame_*` tables: `internal/store`.
- Anything that talks to WhatsApp, enforces limits, audits or publishes events: `internal/bot`,
  in the file that matches (a new action goes in `actions.go`, a new event in `events.go`).
- HTTP concerns (parsing requests, status codes, headers, scopes per route): `internal/api`. Handlers
  stay thin and call `Bot` methods.
- Token issuing and verification: `internal/auth`. MCP tools: `internal/mcpserver`.
- Screen and keys: `internal/tui`. The remote admin panel: `internal/admin`, one file per tab; anything
  it shows must come from an API endpoint, so a new panel feature usually starts as a route.
- Helpers that both `api` and `tui` need belong in the lowest package that makes sense (e.g.
  `store.Message.MediaFileName`), never copied.
- Export only what another package uses; keep the rest unexported. Avoid stutter (`bot.Status`,
  not `bot.BotStatus`).

## Build and check

```sh
go build -o wame .      # or: go install .
go vet ./...
go test -race ./...
gofmt -l .               # must print nothing
```

No cgo: SQLite is `modernc.org/sqlite`, registered as `"sqlite"` and handed to whatsmeow with the `"sqlite3"` dialect.
Don't switch to `mattn/go-sqlite3`.

## Adversarial review (every change and every fix)

Green tests aren't the bar. After a feature or a fix is written, and before it's committed, review
the whole diff as an attacker and a skeptic would, then fix what holds up:

1. **Review the diff, not the intent.** Read every changed file (`git diff`, plus new files). For
   larger changes, run reviewers in parallel, one per area (store, bot, api/auth/mcp, tui/admin), each
   read-only, each told what was just reviewed so it doesn't repeat work.
2. **Look for, in this order:**
   - **Bugs:** races (run `-race`), lost or doubled sends, state transitions that can overwrite each
     other (a late result vs a cancel), timers, goroutines without an owner (`b.Go`), shutdown
     behaviour, SQL that reads then writes without a transaction, errors swallowed into empty
     results.
   - **Security and privacy:** scope and allowlist bypasses on every route and MCP tool, text from
     strangers reaching a terminal unsanitized (`safe()`), message text, tokens, secrets or the
     webhook URL leaking into logs, events, stats, alerts or error bodies.
   - **Resource limits:** unbounded tables, maps, request bodies or goroutines per request.
   - **DRY:** the same check written twice (token validity, allowlists, error bodies, list keys), and
     helpers that already exist elsewhere in the repo.
   - **Go idioms** for the version in `go.mod`: `errors.AsType`, `go fix` modernizers, `t.Context()`,
     `b.Loop`, timers without drain dances, `rows.Err()`, `defer tx.Rollback()`.
3. **Verify each finding against the code** before acting; drop what can't be confirmed. Note refuted
   leads too, so the next review doesn't chase them.
4. **Fix with a test.** Every confirmed bug gets a regression test that fails without the fix.
5. **Parallel work:** when several agents change the code at once, give each a disjoint set of files
   (or its own worktree), and review the merged result again: bugs often live where two changes meet.
6. **Match the model to the work.** Always dispatch less demanding work to subagents on a smaller
   model (Sonnet or Haiku): fact-checking docs against the code, adding links and doc rows, grepping
   for leaked data, mechanical renames, running tests and reporting failures. Keep design,
   security reviews and tricky fixes on the main model. Give the subagent a self-contained prompt
   with this file's rules (fake data, explicit staging, no commit unless asked).

## Tests

Each package has `_test.go` files in the same package (so they can reach unexported code). They
run offline: scratch databases in `t.TempDir()`, `bot.New` on a fresh DB (no login, no
connection), `events.Message` values built by hand, `httptest` for the API, and the TUI model
driven by feeding it `tea.WindowSizeMsg` / `tea.KeyMsg` and running the returned `Cmd`s
synchronously. Never point a test at a real `wame.db` or connect to WhatsApp. Use only the fake
numbers and names below.

- `internal/store`: the outbox lifecycle and `BenchmarkEnqueue` / `BenchmarkDueAndClaim` (the numbers
  in docs/QUEUE.md), v1 → current migration keeps rowids as `seq` and is idempotent, `Filter` SQL vs
  `Match`, `ChatOnly`, edit/delete/import, chats and audit.
- `internal/store`, queue files and options: `outbox_files_test.go` (dedupe, quota race, the trigger, expiry, migration) and `outbox_skip_test.go` (skip-sent, dedupe, keys).
- `internal/bot`: `Limiter` (`ReserveN` is all-or-nothing, `Stats`), the outbox (`outbox_test.go`: validation and
  idempotency, requeue while offline with the same message id, token rate and revocation, a late
  result never overwriting a cancel, backoff, pause), `ParseRecipient`, `handleProtocol`
  ignoring forged edits and non-admin revokes, `audited`, `cappedBody` / `limitedTransport`, media
  checks, webhook signing, filtering and stats (never the URL or message text).
- `internal/api`: token auth, token-less `localOnly`, idempotency (replay, 409, panic), error
  mapping, `/messages` cursors, `/events` `Last-Event-ID` replay, scoped tokens and allowlists
  (`TestScopedTokens`, `TestPerTokenRate`, `TestTokenAdminEndpointsAndRevocation`), MCP over HTTP with
  token scopes, `/stats` (admin only, callers), `/settings` and the webhook routes, `/queue` (scopes, allowlist, own items only, 409, 413, queue options, restricted tag expansion), `/queue/files` (scopes, 404 for foreign files, 413/415, no content route, no names), and one regression
  test per security finding in `security_test.go` (`TestF1…`–`TestF10…`).
- `internal/bot`, queue options and files (`queueopts_test.go`, `outbox_files_test.go`): tags, templates, dedupe, `skip_sent` (including concurrency), replay after rename/relink, number↔tag 409; file sniffing and bypasses, quotas, upload reuse/TTL, upload errors and refunds, slot wait, prune and sweep, logout, error scrubbing, re-upload at the quota, per-item ready uploads, cache drop on a failed send; `queue_merge_test.go`: files with tags/templates/dedupe; `queue_review_test.go`: wipe keeps foreign files, uploads racing a wipe or sweep, lookups that fail never send, token lookup errors. Root `queuefile_test.go` and `queuefile_review_test.go`: `queue import -file` (one upload, rerun, dry run, local checks, keys, an answer without an id or with another file's hash, the pre-`-file` key pinned).
- `internal/auth`: token issuing/parsing, `TestF2RequiredFailsClosed`.
- `internal/mcpserver`: read-only and restricted callers, cleaning of untrusted text, `TestScheduleFile`, queue options.
- `internal/tui`: 110×24 renders exactly 24 lines (with and without the context bar), autocomplete,
  reaction divider, vim-style keys, half-page scroll, `safe()`.
- `internal/bot` (`settings_test.go`): runtime settings (validation, apply, restart, reset, audit),
  the webhook filter setting vs a late number resolution, webhook test and replay, usage counting.
- `internal/admin`: the client against the real `api.NewHandler` (tokens, audit paging, 401/403
  messages), the http guard, env files and profiles, and the panel: create → copy → revoke →
  filter, rotation, the Chats tab (sanitizing), the Settings tab, the Queue tab (no text shown, TYPE column, cancel, batch cancel, pause, filter), webhook test and replay, usage columns, the live stream, the bell, Health, server switching
  (stale answers dropped) and that Status cards line up at 120 and 180 columns. Tests that build a
  model use `newTestModel`, which stops the live stream on cleanup (otherwise the test server
  can't close).
- `main`: default DB path, `isLoopbackAddr`, env fallbacks.
- Also: `internal/store/state_test.go` (read positions, upgrade marks history read, FTS search incl.
  accents/prefix/deleted/edited, group cache) and `internal/bot/state_test.go` (read-self receipts,
  group cache loading); the TUI tests cover the new-messages divider, search jump and badges.

**CI must stay green.** Run `gofmt -l .`, `go vet ./...` and `go test -race ./...` before pushing;
CI runs the same on Linux and macOS.

## Releases

```sh
git tag v0.x.y && git push --tags
```

The `release` workflow runs the tests and then GoReleaser, which publishes the archives and
`checksums.txt` to a GitHub release. The version printed by `wame version` comes from the tag
(without the `v`); local builds print `dev`. Don't edit `version` in `main.go` by hand.

Add a test with every behavior change. To eyeball the TUI, print `ansi.Strip(m.View())` from a
throwaway `zz_*_test.go` (ignored by `.gitignore`) using the helpers in `internal/tui/tui_test.go`;
for the admin panel, `newServer` + `newTestModel` + `press` in `internal/admin/admin_test.go` give a
real API with fake data (run with `-v` to see the output).

## Conventions

- **Few comments, more docs.** Code comments only for non-obvious "why". Explanations go in `docs/*.md`.
- **Keep the docs in sync.** A change to an endpoint means updating `internal/api/api.go`, `docs/openapi.yaml` *and*
  `docs/API.md`. A TUI key or command means updating `tuiHelp` in `internal/tui/commands.go`, the
  footer `hints` in `internal/tui/view.go` and `docs/TUI.md` (which quotes `tuiHelp` verbatim). A flag means
  updating `usage` in `main.go`, `docs/CLI.md` and the README table. An admin panel key means
  updating the footer `keys` in `internal/admin/view.go` and `docs/ADMIN.md`. A new runtime setting
  goes in `SettingKeys`, `Settings.get`/`set` and `applySettings` (`internal/bot/settings.go`), the
  `SettingView` key enum and PATCH body in `docs/openapi.yaml`, `docs/API.md` and `docs/CLI.md`. A new TUI command goes in
  `tuiCommands` (autocomplete and hint bar), `tuiHelp` and `docs/TUI.md`.
- Errors from the API are always `{"error": "..."}` via `writeError` (401/403 come from the auth
  middleware), which maps errors to codes:
  `bot.InputError` (build it with `bot.BadInput(...)`) → 400, `store.ErrNotFound` → 404, `errConflict` → 409,
  `bot.RateLimitError` → 429 with `Retry-After`, context cancel/deadline → 504, a database error
  (`*sqlite.Error`) or `errInternal` (a recovered panic) → 500 with only "internal error" (the cause is
  logged, never sent), anything else → 502 (WhatsApp failure). Write error bodies with `writeErr`,
  and read JSON with `readJSON` / `readJSONMax` (one value only; too large → 413). Return
  `BadInput` from `Bot` methods for caller mistakes so the API, TUI and CLI all report them the same way.
- **Everything that sends a message goes through `Bot.deliver`** (or `Bot.sendControl` for edits and
  revokes, which don't create a stored message), so the rate limiter applies. Never call
  `b.Client.SendMessage` directly from new code paths. The limits exist because bursts get numbers banned.
  The outbox is the one caller that books its slot itself (`outboxSlot`) and passes `queued` so
  `deliver` doesn't book a second one. Bulk sends belong in the outbox, not in loops over `/send`.
- **Number lookups and presence updates go through `Bot.lookup`** (`Resolve` on a cache miss, `/check`,
  `SetTyping`). Mass `IsOnWhatsApp` calls are another ban trigger.
- **Background work goes through `b.Go`**, never a bare `go`, so `Close` can stop and wait for it.
  Never block the whatsmeow event handler: network calls (group refreshes, admin checks for a revoke)
  run in `b.Go`. Auto-replies are picked and budgeted (`autoLimit`) synchronously; only the send runs
  in the background.
- Presence: default is "unavailable" so the owner's phone keeps notifications. Use `chatPresence` for
  typing indicators; it goes available and back when `-online` is off. Presence is sent through
  `sendPresence` (serialized, latest state wins), so quick typing/paused pairs can't arrive out of order.
- **Wrap every control action in `Bot.audited(actor, action, target, detail, fn)`.** It runs `fn` and calls
  `Bot.Audit` exactly once with the outcome, so input-validation failures (empty text, not your message,
  edit window) are recorded too. Put the validation inside `fn`, not before it. Call `Bot.Audit` directly
  only for actions that don't return a `Message` (pair, logout, read, token_create, token_revoke,
  settings, webhook_test, webhook_replay, queue, queue_edit, queue_cancel, qr, check, optout_add, optout_remove, alert_test, contact_set, contact_delete, contact_delete_all, contact_import, contact_export, contact_refresh, contacts_wipe, queue_file, queue_file_delete, queue_files_wipe). `actor`
  is the caller's token name for API and MCP calls (`principal(r).Name`; `api` for the owner secret and
  token-less mode, `mcp` for stdio without a token), or `tui`, `cli`, `autoreply`, `whatsapp`. Those
  fixed names (plus `owner`, `local`) are reserved so no token can impersonate them. Pass the actor into
  `Bot` methods rather than guessing. Never put message text,
  tokens or secrets in `detail` or in log attributes. New actions go in the tables in
  `docs/proposed-features/ADMIN-PANEL.md` and the `AuditEntry` enum in `docs/openapi.yaml`.
- Logging is `log/slog` via `b.Log`. Errors go in an `err` attribute. Don't log query strings (the token
  can be there).
- Events published today: `store.Message`, `store.MessageUpdate`, `bot.Status`, `store.ChatsChanged`
  (history import), `store.ChatRead` (a chat's read position moved: TUI open, `/read`, read-self
  receipts) and `bot.NamesChanged` (group names refreshed). The TUI reloads the chat list on the
  last two.
- New events for subscribers go through `Bot.publish`. The SSE handler forwards `store.Message` (as `message`,
  read back from the DB so none are lost), `store.MessageUpdate` (as `update`) and `bot.Status` (as `status`).
  `store.ChatsChanged` isn't forwarded but triggers the same DB catch-up as `Message`; other types are dropped.
  A numeric `Last-Event-ID` beats `?since=`, so `EventSource` reconnects resume correctly.
- Messages are only published when `Store.Save` actually inserted them (`Bot.save`). Incoming ones
  (`Bot.handle`) then also go to the webhook; messages wame sends itself don't. `seq` is an explicit
  `INTEGER PRIMARY KEY AUTOINCREMENT` column; never reuse or renumber it.
- Fill `Raw`, `RawChat` and `RawSender` when building a `store.Message` to store: quoting, reactions, deletes,
  read receipts and media download all need the original protobuf and the JIDs as WhatsApp sent them.
  They're tagged `json:"-"` so they never reach API clients or webhooks. `Store.MarkDeleted` clears `raw` on purpose.
- Incoming edits/revokes (`handleProtocol`) are applied only when they come from the original author, or
  are a delete by someone who is actually a group admin (checked against the cached participant list in
  `Bot.group`). Keep that check; otherwise anyone could rewrite stored messages.
- Classify message variants in one place (`classify` in `internal/bot/message.go`).
- Schema changes to `wame_messages`: new columns only need to be added to `messagesTable` (`internal/store/store.go`)
  (`addMissingColumns` adds them to existing databases on startup, so give them a DEFAULT). Anything else
  needs an upgrade step in `migrate` that keeps the `seq` numbers. Other tables go through
  `migrateState` (`internal/store/state.go`), `migrateAudit`, `migrateTokens`, `migrateSettings` or `migrateOutbox`: create them idempotently and handle the
  first-run backfill there (as `wame_chat_state` does: existing history counts as read). Upgrades must
  run automatically on startup; there is no separate migrate command.
- Tables: `wame_messages`, `wame_messages_fts` (FTS5 over `text`, external content, kept in sync by
  the `wame_messages_fts_insert/_delete/_update` triggers, so write `text` only through normal
  UPDATE/INSERT), `wame_chat_state`, `wame_groups`, `wame_audit`, `wame_tokens`, `wame_settings`, `wame_outbox` (new columns go through `addColumns` in `migrateOutbox`: `file_id`, `file_kind`, `file_size`, `recipient_key`, `source_hash`), `wame_outbox_files`, `wame_optouts`, `wame_contacts`, `wame_contact_cache`, `wame_group_members`. Unread counts and search must
  agree with `Filter.Match` / `textMatches`, which the live paths use.
- The TUI is designed for desktop terminals (~100+ columns). Don't spend effort on narrow layouts.
- **Don't block in `Update`.** Network and DB calls go in a `tea.Cmd` that returns a message. Capture the
  values a `Cmd` needs (chat, message) before returning it instead of reading or changing `m` inside the
  goroutine. Chat loads carry `openSeq` so only the latest one is applied.
- Pass any text that strangers control (message text, push names, file names, group names) through `safe()`
  before rendering it in the TUI, and through `admin.safe()` in the admin panel.
- **Admin panel (`internal/admin`):** every server call goes through `m.call`, which runs on a
  captured client and tags the answer with the client generation; never read `m.c` or change `m`
  inside a `Cmd`. Switching servers bumps `gen` and replaces the model, so late answers are dropped.
  The live stream sends through a channel that's closed on stop; tests must stop it (`newTestModel`
  does). What the panel keeps from streamed messages is time, chat and sender name, never text.
  Layout: `grid` for cards side by side (equal heights), `besides` only for a tall card like the QR;
  the header drops detail by width instead of truncating. Older servers answer 404 on newer routes:
  show "update the server" rather than an error. The Queue tab's files view (`F`, `queuefiles.go`) shows ids, kinds and sizes, never a file name, not even the caller's own: `ListQueueFiles` blanks `Name`.
- **Runtime settings** (`/settings`) are stored in `wame_settings` and win over flags at every start
  until reset; `InitSettings` must run after the limiters and webhook are built. Limits change through
  `Limiter.Set` only, and `Limiter` reads its fields under its lock; auto-reply is atomic
  (`SetAutoReply` / `AutoReplyOn`), never a plain field.

## WhatsApp gotchas (learned the hard way)

- **LIDs.** WhatsApp may identify a user by a hidden `…@lid` ID instead of a phone JID, especially for
  messages sent from the user's own phone. `toMessage` prefers `SenderAlt` / `RecipientAlt`, `Bot.PhoneJID`
  maps through whatsmeow's `Client.Store.LIDs`, and `store.Store.relinkLIDs` fixes stored rows once the mapping is learned. Any new code
  that stores or compares chat JIDs must go through the same normalization, or chats split in two.
- **Phone numbers aren't canonical.** Always send via `Bot.Resolve`, which asks WhatsApp (`IsOnWhatsApp`)
  for the real JID. For example, Brazilian numbers may be registered with or without the 9th digit.
- **Before login, `Client.Store.Contacts` and `Client.Store.LIDs` are nil.** Guard every use.
- **One connection per session.** Two processes on the same DB kick each other off. `tui -addr` exists
  so the TUI and the API can share one process.
- whatsmeow logs go to stdout. In `tui` and `send` modes the whatsmeow logger is `waLog.Noop` and the app
  logger writes to `io.Discard` (unless `-log-file` is set) so they don't break the screen.
- Use whatsmeow's current API (context-first signatures: `sqlstore.New(ctx, …)`, `GetFirstDevice(ctx)`,
  `GetQRChannel(ctx)`). Check the module source in `go env GOMODCACHE` rather than relying on old examples.

## Safety and privacy

- **Never commit `wame.db*`.** (The default location is now `~/.config/wame/`, but older checkouts may still have one in the repo directory.) It holds the account's encryption keys and message history. `.gitignore`
  covers it; don't weaken that.
- **No real data in the repo.** Docs and examples use fake numbers (`5511999999999`, `5511988887777`,
  `552187654321`) and fake names (Maria, John, Ana). Before committing, grep the staged diff for real phone
  numbers, names and tokens.
- Don't run `wame` against the user's real `wame.db` while they may have it open. Copy it to a scratch
  directory to inspect it, and treat message contents as private.
- The bot can message real people. Don't send test messages to anyone except the account owner's own
  number, and only when asked.
- **Every new route is wrapped in `need(scope, …)`** in `NewHandler`, and must handle tokens limited to
  chats: check targets with `authorizeChat` (sends also call `throttle` for the per-token rate), limit
  queries with `restrictFilter`, or wrap the route in `unrestricted` if it can reveal data beyond the
  allowlist. Add it to the route table in `docs/API.md` and to `docs/openapi.yaml`.
- **Never resolve a restricted caller's input on WhatsApp.** Use `Bot.LocalChat` + `Bot.ChatAllowed`
  (or `ExpandAllowlist`); `Resolve` / `CanonicalChat` are for unrestricted callers only. Error text for
  chats outside the list stays fixed ("this token can't access that chat") so it can't be used as an
  oracle.
- Tokens are required forever once any row exists in `wame_tokens`, and `Required()` fails closed. Don't
  add any path back to token-less mode. Token-less `localOnly` checks loopback `RemoteAddr`, loopback
  `Host` and same-origin; keep all three. Only `/docs` and `/openapi.yaml` are public.
- Tokens can't mint `admin` tokens over the API; children carry `created_by`, can't outlive the parent and
  are revoked with it. Long-lived handlers (`/events`, `/messages/wait`) must re-check `stillValid` and
  respect the per-token stream cap. `?token=` is accepted only on `GET /events`, `/qr`, `/media/{seq}`.
- **New MCP tools** call `require(req, scope)` (which also re-checks the token is still valid) and, for
  any chat argument, `chat()` (allowlist, no lookup for restricted callers); send-type tools use
  `sendTarget`, which adds the per-token rate (`Bot.ThrottleToken`, shared with the HTTP API). Anything written by third parties (text, names, captions, file names) goes
  under an `untrusted` field and through `clean()`. Mark read-only tools `ReadOnlyHint` and sending ones
  open-world / destructive.
- `wame mcp` speaks the protocol on **stdout**: never print to stdout in that mode (logs go to stderr,
  whatsmeow logging stays off).
- See [docs/security/SCOPED-TOKENS.md](docs/security/SCOPED-TOKENS.md) for the threat model and the
  findings these rules come from; add a regression test for any new auth behaviour.
- `/media/{seq}` serves files received from other people: keep the inline allow-list (`inlineSafe`: images
  except SVG, audio, video, PDF; everything else as an `attachment`), `nosniff`, `CSP: sandbox;
  default-src 'none'` and `Referrer-Policy: no-referrer`. The 100 MB cap counts actual bytes.
- The DB and its `-wal`/`-shm` files are chmod 0600 at startup; files saved from the TUI are 0600 and never
  overwrite existing ones.
- Never log the webhook URL (it may carry a secret) or the request query string.
- `/docs` loads `swagger-ui-dist` from jsDelivr with Subresource Integrity hashes (`docsPage` in `internal/api/api.go`).
  Bumping the version means recomputing both `integrity` attributes, e.g.
  `curl -fsSL URL | openssl dgst -sha384 -binary | openssl base64 -A`, otherwise the page won't load.
- In-memory admin data (`/stats`: webhook failures, limiter counts, per-caller usage with last IP)
  never holds the webhook URL, its secret or message text, and resets on restart. `/webhook/replay`
  only accepts delivery ids from the recent failures, so it can't push messages the filter excluded.
- **Alerts** (`-alert-gotify`) never carry message text, contacts' numbers (the account shows only
  its last 4 digits), tokens, the webhook URL or the Gotify URL; disconnect alerts wait at least 30 s,
  and failure counts are kept at the source (`finishQueued`), never rebuilt from events; the Gotify token goes only in the `X-Gotify-Key` header. `/metrics` never exports
  per-token series. `/logs` redacts secret-looking keys and scrubs known secrets, but that's defence
  in depth: never log secrets in the first place.
- **Contacts:** your own fields (notes above all) reach admin callers only: never MCP, events, alerts,
  logs, webhooks or audit details (`error` may quote a rejected tag, language, time zone or field
  name). Restricted tokens get `GET /contacts` filtered to their allowlist and a fixed 403 for anyone
  else, never a lookup. Names come from `NamesOf` everywhere; don't build display names by hand.
  Brazil's 9th digit is matched only for mobiles (`phoneVariants`), so two people can't merge.
  The WhatsApp profile (`Bot.Profile`) is asked of WhatsApp only on an admin `refresh=1`, through
  `b.lookup`; notifications update existing cache rows only (strangers can't grow the table); the
  cache is pruned after 90 days. **Logout wipes contact data** (`Bot.Logout` for `wame logout` and
  `POST /logout`, and `events.LoggedOut`): own contacts, the cache, group names, admins and
  members, audited as `contacts_wipe`; queued files are deleted too (`wame-queue-files/`, audited `queue_files_wipe`); messages, opt-outs (a do-not-contact list) and the audit
  log stay. About, verified and business text is
  third-party text: `safe()` in terminals, `clean()` under `untrusted` in MCP, never logged.
- **Opt-outs** are kept quiet on purpose: never answered, never mentioned in the README. They apply
  to the queue only, never to `/send`. One helper, `optedOut`, checks the phone JID, its `@lid` and the number as typed;
  `relinkLIDs` moves `wame_optouts` rows like messages. Queue ownership is by actor name, so
  `store.OutboxFilter.For(actor, admin)` is the one place the "own items unless admin" rule lives,
  `bot.NotQueuedError` is the one way to say "exists but can't be canceled" (409), and the token
  name `mcp` is reserved because stdio `wame mcp` acts under it.
- **Queue options:** `tag:` and templates need the read scope. Tag members outside a restricted token's
  allowlist are dropped silently; errors name tag members by position, never by number. `{name}` is
  never a push name. `skip_sent` counts only the caller's own items unless admin, and counts sent
  messages only with read.
- **Queue files** sit on disk under `<db dir>/wame-queue-files/` (0700/0600, server-chosen names) until cleanup
  and are never served back. File names never reach events, the webhook, audit details, MCP or the
  admin panel; the cached media key never leaves the DB; upload errors go through `uploadError`.
  Another token's file id is 404, never 403. `wame backup` doesn't include `wame-queue-files/`. Uploads are
  limited to 2 per token and 8 in all.
- Make backups with `wame backup` (read-only, same path as the server), never by copying `wame.db`
  while it's open.
- The outbox stores message text until the item is pruned (7 days after it finishes). Queue events,
  the webhook `queue` delivery, the enqueue response and the admin panel never carry it; only
  `GET /queue` and `GET /queue/{id}` do, and only to the token that queued it (or admin). Items from a
  token revoked or expired before they go out fail instead of being sent.
- `wame admin` sends the token on every request: keep `NewClient`'s refusal of plain http beyond
  loopback, and `Resolve`'s rule that a server (or insecure flag) read from a file is only used with
  a token from that same file, so a stray `.env` can't redirect a token from elsewhere.
- Webhook signing is HMAC-SHA256 over `"<timestamp>.<body>"`. Changing that breaks every receiver; if it
  must change, add a new header version instead.

## Commits

Small, focused commits with a plain summary line. The repo is public under MIT.

- **Stage files by path, never `git add -A` / `git add .`.** The owner keeps private, uncommitted
  files in the working tree (deployment configs such as `unraid/`, `.env`); both are git-ignored now,
  but check `git status --short` for anything you didn't write before every commit.
- Before committing, grep the staged diff for real phone numbers, names, tokens, hostnames, private
  IPs and domains.
