# Admin panel (proposal)

> **Partly built.** The web panel itself isn't built. A terminal panel, `wame admin`
> ([ADMIN.md](../ADMIN.md)), covers status and remote login, chats, tokens, the audit log, limiter
> and webhook health, and runtime settings, all over the API. The server pieces it needed are built
> too (marked **Built** below), so a web panel would reuse them. The [Logging today](#logging-today)
> section describes what exists; the rest is a plan. See the [list of proposals](README.md).

This is a plan for a small web admin panel served by `wame serve`, and a description of the
logging and audit groundwork that already exists for it.

## Goals

- See at a glance whether the bot is connected, and log in (QR or pairing code) from a browser.
- Read chats and messages, and send a message, without the TUI.
- See who did what: the audit log, filterable.
- See whether the webhook is healthy and whether sends are being throttled.
- See the effective configuration.
- One binary, no build step, no extra services.

## Non-goals

- A full WhatsApp Web clone (no media gallery, no group management, no rich composer).
- Multi-account or multi-tenant support. One process is one WhatsApp session.
- Editing configuration that needs a restart (DB path, listen address) from the browser.
- Mobile layout. Desktop browsers only, like the TUI.
- Exposing it to the internet without a reverse proxy and TLS.

## Pages

| Page | Shows / does | Built on |
|---|---|---|
| **Status & login** | state, JID, push name, connection dot; QR image that refreshes, pairing-code form, logout button | `GET /status`, `GET /qr`, `POST /pair`, `POST /logout`, `status` SSE events |
| **Chats & messages** | chat list with last activity; a chat's messages, live; media links; edited/deleted markers | `GET /chats`, `GET /messages?chat=…`, `GET /events?chat=…`, `GET /media/{seq}` |
| **Send** | recipient, text, optional reply-to and file; shows the result or the 429 wait | `POST /send`, `POST /send/file` (with an `Idempotency-Key` per form submit) |
| **Audit log** | table of actions; filters by actor, action, target, ok/failed, time range; paging | `GET /audit` (needs filters, see below) |
| **Webhook health** | URL (secret masked), filter, queue depth, last success/failure, recent failed deliveries with status and attempt count | needs a delivery log (see below) |
| **Rate limit** | configured `-rate`, `-chat-gap`, `-max-wait`; for the send, lookup (3 × rate) and auto-reply (6/min) limiters: usage in the last minute, current wait for the next slot, recent 429s / dropped auto-replies | needs a limiter snapshot (see below) |
| **Settings** | effective flags and their source (flag / env / default), secrets masked; later, editable runtime settings | needs a config snapshot (see below) |
| **Logs** (optional) | recent app log lines, level filter | needs a log ring buffer (see below) |

## What already exists

- **Read and control endpoints:** `/status`, `/qr`, `/pair`, `/logout`, `/chats`, `/messages`,
  `/messages/wait`, `/events`, `/media/{seq}`, `/send`, `/send/file`, `/react`, `/edit`,
  `/delete`, `/read`. The panel can call these from the browser with no new backend code.
- **Live updates:** `/events` streams `message`, `update` and `status` and resumes with
  `Last-Event-ID`, so the panel can use a plain `EventSource`.
- **Audit:** every control action is stored in `wame_audit` and served by `GET /audit`.
- **Structured logs:** `log/slog`, text or JSON, one line per HTTP request and per action.

## What's missing

1. **Webhook delivery log.** *Partly built:* `GET /stats` has delivery counters, queue depth and
   the last 20 failed attempts in memory, and `POST /webhook/replay` resends one of them. Still
   missing: a persistent log. Add a
   `wame_webhook_deliveries` table (`delivery`, `event`, `seq`, `attempt`, `status`, `error`,
   `at`, `done`), written by `Webhook.send`, pruned to the last N days, served by
   `GET /admin/webhook` together with queue length (`len(w.queue)`) and capacity.
2. **Limiter snapshot.** **Built** as `Limiter.Stats()` (use in the last minute, next free slot,
   rejections since startup) for the send, lookup and auto-reply limiters, served by `GET /stats`
   together with per-caller usage.
3. **Config snapshot.** *Partly built:* `GET /settings` shows the runtime-editable settings with
   their flag values and who changed them. For the rest of the configuration: keep the parsed `config` on the `Bot` (or pass it to the panel) and serve
   it with secrets (`token`, `webhookSecret`) replaced by `"set"` / `""`, plus where each value
   came from.
4. **Log ring buffer.** A `slog.Handler` that tees the last ~1000 records into memory, served by
   `GET /admin/logs?level=&after=`. Attributes are already safe to show (see conventions below).
5. **Audit filters.** `GET /audit` only has `limit` and `before`. Add `actor`, `action`, `target`,
   `ok`, `from`, `to` query parameters, and an index on `(action, at)` if it gets slow.
6. **Admin auth, separate from the API tokens.** API tokens are meant for scripts and agents (and
   `?token=` is only accepted on a few GET routes). The panel should instead use:
   - a login form (password from `-admin-password` / `WAME_ADMIN_PASSWORD`, stored hashed if
     persisted), creating a server-side session with an `HttpOnly`, `SameSite=Strict`,
     `Secure`-when-TLS cookie;
   - a CSRF token on every state-changing request (double-submit or per-session token);
   - session expiry and a logout button;
   - API tokens keep working for `/` routes; panel routes live under `/admin/` and accept
     only the session cookie. Today's `admin` scope (see [API.md](../API.md#tokens-and-scopes)) is the
     natural mapping for a full panel user.
7. **Read-only role.** A second password (or user list) for viewers who can see status, chats and
   audit but can't send, delete, pair or log out. Enforced server-side per route, and recorded as
   the audit `actor`. Today the actor for API calls is the plain token name (e.g. `support-bot`), so a
   panel user could be audited the same way, by a name that can't collide with a token name (e.g.
   `maria` for a panel login, with token names and panel names sharing one namespace). Current scopes are
   `read`, `send` and `admin`; a viewer maps to `read`.
8. **Settings persistence.** **Built:** `GET`/`PATCH /settings` for `rate`, `chat_gap`,
   `max_wait`, `auto_reply` and `webhook_filter`, stored in `wame_settings`, overriding flags on
   startup until reset, each change audited (`action=settings`; none of these are secrets).

## Logging today

### App log (`b.Log`, `*slog.Logger`)

Configured by `-log-format text|json`, `-log-file PATH` (stderr otherwise) and `-debug`
(debug level, else info). Discarded in `tui` and `send` modes unless `-log-file` is set.

| Message | Level | Attributes |
|---|---|---|
| `http` | info; debug for `/status`, `/messages/wait`, `/events`; warn for 4xx; error for 5xx | `method`, `path`, `status`, `duration_ms`, `remote` (no query string: it may hold the token) |
| `action` | info | `actor`, `action`, `target`, `detail` |
| `action failed` | warn | same, plus `err` |
| `audit write failed` | error | same, plus `err` |
| `connection state` | info | `state` |
| `login failed` | error | `event`, `err` |
| `save message` | error | `chat`, `id`, `err` |
| `auto-reply failed` | warn | `chat`, `err` |
| `auto-reply dropped` | debug | `chat`, `err` (over the auto-reply budget) |
| `set presence failed` | warn | `err` |
| `delivery failed` | warn | `component=webhook`, `delivery`, `attempt`, `status`, `err`, `will_retry` (the URL is never logged) |
| `delivered after retry` | info | `component=webhook`, `delivery`, `attempt` |
| `giving up on delivery` | error | `component=webhook`, `delivery`, `attempts` |
| `queue full, dropping delivery` | error | `component=webhook`, `delivery` |
| `webhook filter chat resolved` | info | `chat`, `jid` |
| `webhook filter: chat isn't on WhatsApp, keeping it as typed` | warn | `chat`, `err` |
| `webhook filter: chat lookup failed, retrying` | warn | `chat`, `err`, `retry_in` |
| `refresh groups failed` | warn | `err` |
| `cache groups failed` | warn | `err` |
| `API has no token: only local, same-origin requests are accepted` | warn | `addr` |
| `HTTP API listening` | info | `addr`, `docs` |
| `MCP server on stdio` | info | `as` (principal name), `scopes` (`wame mcp` only) |
| `HTTP API stopped` | error | `err` (`tui -addr` only) |

whatsmeow's own logs are separate (stdout, `waLog`), off in `tui` and `send`.

### Audit table (`wame_audit`)

| Column | |
|---|---|
| `id` | autoincrement; cursor for `before` |
| `at` | Unix milliseconds |
| `actor` | who: the token name for API/MCP calls (`api` = owner secret or token-less, `mcp` = stdio without a token; both reserved, so no token can take them), `tui`, `cli`, `autoreply`, `whatsapp` |
| `action` | what (below) |
| `target` | chat JID, when there is one. For failed attempts, the recipient or chat as given (`5511999999999`) |
| `detail` | short, non-sensitive description |
| `ok` | 1 or 0 |
| `error` | error text when `ok` is 0 |

| Action | Written by | `detail` |
|---|---|---|
| `send` | `Bot.SendText` | `text, N chars` |
| `send_file` | `Bot.SendFile`; the queue worker for a queued file | `<file name>, N bytes`; from the queue `<kind>, N bytes, queue #<id>`, without the name |
| `react` | `Bot.React` | `<emoji> on <id>` |
| `edit` | `Bot.Edit` | message id |
| `delete` | `Bot.Delete` | message id |
| `read` | `Bot.MarkRead` | `N messages` (every call, including failures and calls that marked nothing) |
| `pair` | `Bot.PairPhone`: `POST /pair`, TUI `/pair`, `wame login -phone` | |
| `logout` | `POST /logout`, `wame logout` | |
| `logged_out` | WhatsApp event (actor `whatsapp`) | reason code |
| `token_create` | `POST /tokens`, `wame token create` | requested scopes (target = token name) |
| `token_revoke` | `DELETE /tokens/{name}`, `wame token revoke` | (target = token name) |
| `settings` | `PATCH /settings` | the changes: `rate=10, chat_gap reset` (also for rejected ones: the keys) |
| `webhook_test` | `POST /webhook/test` | `HTTP <status> in N ms` |
| `webhook_replay` | `POST /webhook/replay` | delivery id (target = the message's chat) |
| `alert_test` | `POST /alerts/test` | the backend (`gotify`) |
| `queue` | `Bot.Enqueue`: `POST /queue`, MCP `schedule_message` | `N messages` (target = batch) |
| `queue_cancel` | `Bot.CancelQueued`: `DELETE /queue`, `DELETE /queue/{id}`, MCP `cancel_scheduled` | `N canceled` (target = batch or `#id`) |
| `queue_edit` | `Bot.EditQueued`: `PATCH /queue/{id}`, MCP `reschedule_scheduled`, TUI `/reschedule`, admin Queue `t`/`e` | the fields changed, like `send_at,text` (target = `#id`); never the text |
| `queue_file` | `Bot.AddQueueFile`: `POST /queue/files` | `<kind>, N bytes`, plus `, already uploaded` for a repeat; never the name (target = file id) |
| `queue_file_delete` | `Bot.DeleteQueueFile`: `DELETE /queue/files/{id}` | `<kind>, N bytes` (target = file id) |
| `queue_files_wipe` | logout (`wame logout`, `POST /logout`, or WhatsApp logging the device out) | `N files`; only when there was something to delete |
| `optout_add` | `POST /optouts`; an incoming opt-out keyword (actor `whatsapp`) | `added` / `already listed`, or `keyword STOP` (target = chat) |
| `optout_remove` | `DELETE /optouts/{chat}` | (target = chat) |
| `contact_set` | `Bot.SetContact` / `Bot.PatchContact`: `PUT`/`PATCH /contacts/{number}`, TUI `/name`, `/tag` | changed field names (`name, tags`) or `no change`, never values (target = chat) |
| `contact_delete` | `DELETE /contacts/{number}` | (target = chat) |
| `contact_import` | `POST /contacts/import` | `N rows: A added, U updated`, plus ` (dry run)` (target = `csv`) |
| `contact_delete_all` | `DELETE /contacts?all=true` | `N contacts` |
| `contact_export` | `GET /contacts/export` | `N contacts` (target = `csv`) |
| `contact_refresh` | `GET /contacts/{number}?refresh=1` | `N lookups` or the error (target = chat) |
| `contacts_wipe` | `POST /logout`, `wame logout`, `logged_out` event | `N contacts` (target = none) |

`send`, `send_file`, `react`, `edit` and `delete` run through `Bot.audited`, which records every
attempt exactly once, whatever the outcome. Failures (`ok` = 0) include input errors such as empty
text or file, a recipient that isn't on WhatsApp, an unknown or deleted `reply_to`, a message id
that doesn't exist, editing someone else's message or one past the 20-minute window, as well as
429s and WhatsApp rejecting the send.

Each `Bot.Audit` call also writes the `action` / `action failed` log line above, so the log and
the table agree.

## Conventions for new controls

- Every control action (anything that changes state on WhatsApp or in wame) is wrapped in
  `Bot.audited(actor, action, target, detail, fn)`, so it's recorded once with the real error,
  including validation failures. Use `Bot.Audit` directly only where the action doesn't fit that
  shape (pair, logout, read).
  Add the new `action` name to the tables in this file and to the `AuditEntry` enum in
  [`docs/openapi.yaml`](../openapi.yaml).
- `actor` is who asked: the token name for API and MCP calls (`api` for the owner secret, `mcp` for
  stdio without a token), or `tui`, `cli`, `autoreply`, `whatsapp`; later, panel users. Pass it into
  `Bot` methods; don't infer it.
- `target` is the chat JID. `detail` is short and safe: ids, counts, sizes, file names.
- **Never log or audit** message text, captions, tokens, webhook secrets, passwords, session
  cookies or QR/pairing codes.
- Errors go in an `err` attribute, not formatted into the message string.
- Anything that sends a message goes through `Bot.deliver` (or `Bot.sendControl` for edits and
  revokes), so the rate limiter applies to the panel too. Number lookups go through `Bot.lookup`.
- Bad input returns `badInput(...)` (400); WhatsApp failures are returned as-is (502).

## Implementation phases

**Phase 1: read-only panel.** A new `internal/web` package (`internal/admin` is the terminal panel) with static HTML, a little CSS and
[htmx](https://htmx.org) (vendored, not from a CDN), embedded with `//go:embed` and mounted by
`internal/api` at `/admin/`. Pages: status & QR, chats & messages (live via `EventSource`), audit log.
Auth: an API token (ideally a `read`-only one for this phase), entered once and kept in `sessionStorage`; without a token the
panel works only from a browser on the same machine, because the API already rejects non-local
and cross-site requests (403). Serve the panel from the same origin as the API so those checks
pass. No new tables.

**Phase 2: admin auth and actions.** Session login, CSRF, read-only role. Send form,
react/delete buttons, pair/logout. Panel actions audited under the panel user's name.

**Phase 3: operational views.** Webhook delivery table and page, limiter snapshot, config
snapshot, audit filters, optional log ring buffer. (The API side of the limiter snapshot and of
in-memory webhook health exists: `GET /stats`.)

**Phase 4: runtime settings.** Edit form for limiter, auto-reply and webhook filter. (The API side
exists: `GET`/`PATCH /settings` and the `wame_settings` table.)

No JavaScript build step at any phase. Server-rendered fragments for htmx can use
`html/template`.

## Open questions

- Is a separate admin password worth it, or is "API token + bind to localhost + reverse proxy
  auth" enough for most users?
- Should the message viewer be in the panel at all, given it exposes message content to anyone
  with viewer access? Maybe off by default for the viewer role.
- How long to keep audit and webhook delivery rows? Add a `-retention` flag or prune by count?
- Retry a failed webhook delivery from the panel? That needs the body stored, which means
  storing message content in a second place.
- Should the panel reuse `/events` directly, or get its own stream with log and delivery events?
- Serve the panel on a separate `-admin-addr` so the API port can stay private to scripts?
