# Scheduled sends everywhere

**Built.** Usage is in [QUEUE.md](../QUEUE.md#when-it-goes-out), [SENDING.md](../SENDING.md#scheduled-and-bulk-text-the-queue),
[API.md](../API.md#scheduled-sends), [CLI.md](../CLI.md#wame-send-numberjid-text), [MCP.md](../MCP.md),
[TUI.md](../TUI.md) and [ADMIN.md](../ADMIN.md); this page keeps the reasoning.

The send queue already sent at a time (`send_at`). This work brought that to every place a message
is typed (`POST /send`, `wame send -at`, the TUI, the admin panel), let a queued message be changed
before it goes out (`PATCH /queue/{id}`), and added the recipient's own clock, `send_before` expiry,
`spread` and text edits. No second scheduler: a timed message is a queue item, so quiet hours,
opt-outs, rates, retries, receipts and the `queue` webhook event all apply.

## Decisions

- **One parser, `bot.ParseSendTime(s, now, relative)`.** It replaced the CSV/API parser and the MCP
  one. It returns a `SendTime{Time, Local}`: an instant, or a date and time read on the recipient's
  clock.
- **The API takes only fixed times** (RFC 3339 with offset, or a date and time without offset).
  A delay or "next 09:00" means something different when a request is retried a minute later, and
  a retry with the same idempotency key must be the same request. The MCP tools, `wame send -at`,
  the TUI and the admin panel take the relative forms; the CLI and the admin panel turn them into a
  fixed time before they send.
- **Past times.** The relative and typed forms refuse an instant more than a minute in the past.
  The API doesn't: an absolute past `send_at` is accepted and goes out as soon as the limits allow,
  as it always did.
- **Recipient's clock.** Resolved at enqueue from the contact's `time_zone` (a phone JID, never a
  lookup), else `queue_timezone`. Each `tag:` member is resolved on their own. `due` is an instant,
  so a zone edited later moves nothing already queued. A time on the recipient's clock stores its
  wall clock as if UTC (`SendTime.wallMilli`, `RequestedSendAt`) in `send_at`, so a zone changed
  between a request and its retry doesn't make the retry a 409.
- **A typed time of day is a `Clock` time.** `09:00` and `tomorrow 09:00` (MCP, TUI) carry `Days`
  and are placed by `SendTime.Resolve(loc, now)` on the recipient's own calendar: the next 09:00
  in their zone, "tomorrow" being their tomorrow. Resolving on the typist's calendar would send
  the wrong day across zones. The admin panel and `wame send -at` can only send a fixed time over
  the API, so they pick the day on their own calendar (`SendTime.String`) and the recipient's
  clock reads it.
- **A new item past its `send_before` is a 400, a retry replays.** `expiry` doesn't compare with
  now; `Bot.Queue`'s check refuses only newly inserted items (`send_before has already passed`),
  so a retry after the deadline still returns the stored item.
- **`ClaimOutbox` claims only what is due** (`due <= now`), so a `PATCH` that moves an item later
  just after the worker read it can't be sent early.
- **`POST /send` with `send_at`, `send_before` or `batch`** calls `Bot.Queue` with one row and
  answers 202 with the `POST /queue` shape. The batch is the queue's generated `b-…` name unless
  `batch` is given. The idempotency key is stored as `send:` + its SHA-256 (any key the API
  accepts fits the queue's key rules): persistent (a plain `/send` key is in
  memory) and apart from the token's `POST /queue` keys. `/send/file` doesn't read `send_at`; a
  scheduled file goes through queue files.
- **`send_at` stays as requested on an edit.** `PATCH` changes `due` only. Otherwise rerunning the
  same import would no longer match what is stored and would answer 409 instead of replaying. An
  edited text keeps the hash of the original in `source_hash` (`editHash`, which can't equal a
  template's hash) and `edited_at` says when, so a re-import still replays.
- **`expired` is its own final state**, not a `failed` with a reason. It isn't a fault (no alert, no
  retry), people filter on it, and it is a count of its own in `/stats`, `/metrics` and the Queue
  tab. It is checked at the start of every worker step (`ExpireOutbox`, also while paused, in quiet
  hours or offline) and in `ClaimOutbox`, so an item can't be claimed in the gap. The step costs a
  read, not a write: the partial index `wame_outbox_expires` answers "is anything past its
  deadline" cheaply, and the `UPDATE` runs only when it is, so it doesn't contend with message
  saves. It sends a `queue`
  event and webhook. `send_before` isn't part of a request's identity: a retry that differs only
  there replays.
- **`spread` is per request, and per row from that row's own `send_at`** (or now): row `i` of `n`
  is due at `send_at + i·spread/(n−1)`, with `tag:` members in expansion order; the limiter and
  jitter still apply on top. It joins the request's identity through `spread_ms`. `wame queue
  import` sends chunks of at most 1000 rows, so `-spread` is computed by the CLI over the **whole
  file** and each row goes out with its own `send_at`; that needs a `send_at` on every row and
  can't spread a `tag:` row's members. The server never sees `spread` from the CLI.
- **Edits are own-items-only for MCP, even for admins**, since the text is the caller's to change;
  the API follows the queue's usual rule (own items unless `admin`).

## Not built

- **Shifting a whole batch** (`PATCH /queue?batch=NAME` with `{"shift": "+1h"}`).
- **A scheduled `/send/file`.** Use `POST /queue/files` and `POST /queue`.
- **`spread` as a per-batch setting** for items added later. It applies only within one request.
- **Per-recipient quiet hours** ([CONTACTS.md](CONTACTS.md) phase 4) would reuse the same zone
  resolution.
