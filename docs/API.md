# HTTP API

Base URL: whatever you passed to `-addr` (default `http://127.0.0.1:8080` for `serve`).

> **Interactive docs:** open `http://localhost:8080/docs` while the server runs. It's a
> Swagger UI page generated from [`openapi.yaml`](openapi.yaml), where you can click
> **Authorize**, paste your token and try every endpoint from the browser. The raw spec is at
> `/openapi.yaml`, ready to import into Postman, Insomnia or Bruno. Neither of those two URLs
> needs the token. The page loads Swagger UI from jsDelivr with Subresource Integrity (sha384)
> hashes, so a tampered CDN file won't run.

## Endpoints at a glance

| Method | Path | What it does |
|---|---|---|
| GET | `/status` | connection and login state |
| GET | `/qr` | login QR as a PNG |
| POST | `/pair` | log in with a pairing code |
| POST | `/logout` | unlink the device |
| POST | `/send` | send text, optionally as a reply |
| POST | `/send/file` | send an image or document |
| POST | `/react` | react to a message (or remove your reaction) |
| POST | `/edit` | edit one of your text messages |
| POST | `/delete` | delete a message for everyone |
| POST | `/read` | send read receipts |
| POST | `/typing` | show "typing…" / "recording…" in a chat |
| POST | `/queue` | queue up to 1000 texts, or a file with captions, for paced sending in the background, now or at a set time |
| GET | `/queue`, `/queue/{id}` | queued, sent, failed and expired items with counts |
| PATCH | `/queue/{id}` | reschedule a queued item, set or clear its `send_before`, or replace its text |
| DELETE | `/queue/{id}`, `/queue?batch=` | cancel queued items |
| POST | `/queue/files` | upload an image or document once, to queue it to many chats |
| GET | `/queue/files` | your uploaded files and their usage |
| DELETE | `/queue/files/{id}` | delete an uploaded file |
| GET, POST | `/optouts` | chats that get no queued messages; add one |
| DELETE | `/optouts/{chat}` | take a chat off that list |
| GET | `/messages` | stored messages, filtered; newest first or cursor-paged |
| GET | `/messages/wait` | long-poll until new matching messages arrive |
| GET | `/events` | live SSE stream with resume |
| GET | `/media/{seq}` | download the photo/video/audio/document of a message |
| GET | `/chats` | conversations by most recent activity |
| GET | `/contacts` | people: the phone's address book and your own contact list |
| GET | `/contacts/tags` | your tags and how many people have each |
| GET, PUT, PATCH, DELETE | `/contacts/{number}` | one person's profile (`GET`); set or clear your own name, tags, notes, language, time zone |
| GET | `/contacts/{number}?refresh=1` | ask WhatsApp about one person now (admin, no chat limit; 2 to 4 lookups) |
| DELETE | `/contacts?all=true` | empty your own contact list |
| POST | `/contacts/import` | import your own contact list from a CSV |
| GET | `/contacts/export` | your own contact list as a CSV |
| GET | `/groups` | your groups |
| POST | `/check` | which numbers have WhatsApp |
| GET | `/audit` | log of control actions (sends, edits, logins…) |
| GET | `/stats` | limiter, webhook, queue and alert health, per-token usage |
| GET | `/metrics` | Prometheus metrics |
| GET | `/logs` | the server's recent app logs, secrets removed |
| GET, PATCH | `/settings` | read or change rate limits, auto-replies, the webhook filter and the send queue without a restart |
| POST | `/webhook/test` | send a signed test delivery |
| POST | `/webhook/replay` | send a recently failed delivery again |
| POST | `/alerts/test` | send a test alert to Gotify |
| GET | `/tokens` | list API tokens |
| POST | `/tokens` | create an API token |
| DELETE | `/tokens/{name}` | revoke a token (and the tokens it created) |
| | `/mcp` | [MCP server](MCP.md) for AI agents (streamable HTTP) |
| GET | `/docs`, `/openapi.yaml` | these docs (no token needed) |

## Auth

Every API route needs a scope (below). Who you are is decided by the request's token:

- **API tokens** (recommended): created with `wame token create` or `POST /tokens`, each with its own
  scopes, optional chat allowlist, send rate and expiry. Give every agent, script or bot its own.
- **The owner secret**: `WAME_TOKEN` (or `-token`), at least 16 characters. It has every scope and
  no limits beyond the global ones. Generate it with `openssl rand -hex 24` and keep it for yourself.
- **Token-less local mode**: only while there's no owner secret **and no token has ever been
  created**. See [below](#token-less-local-mode).

Send the token as a header:

```
Authorization: Bearer <token>
```

The scheme is case-insensitive (`bearer` works). The header always wins. As a last resort for
clients that can't set headers (a browser tab, `EventSource`, `<img>`), `?token=<token>` is accepted
on **`GET /events`, `GET /qr` and `GET /media/{seq}` only**. Anywhere else a query token is rejected
(401). Tokens in URLs end up in logs and history, so prefer the header.

The examples below use `$T` for a token: `T=wame_…` (or your owner secret).

`/docs` and `/openapi.yaml` are always public. `/mcp` serves the [MCP server](MCP.md) with the same
tokens.

Prefer the environment variables (`WAME_TOKEN`, `WAME_WEBHOOK_SECRET`) over the flags for
secrets: command-line flags are visible to other users in `ps`.

### Token-less local mode

With no owner secret and before the first token is ever created, the API runs without tokens, as the
owner, but only for local use:

- `serve` and `tui -addr` refuse to start on a non-loopback address (anything other than
  `127.0.0.1`, `::1` or `localhost`).
- A request must come **from** a loopback address, **to** a loopback `Host`, and not be cross-site:
  an `Origin` header must match `Host`, and `Sec-Fetch-Site` must not be `cross-site`. Anything else
  gets **403**. This stops web pages you visit from calling the API through your browser (CSRF, DNS
  rebinding). `curl localhost:8080/...` works as usual.

Once any token has been created, tokens are required on every request, from localhost too, **for
good**: even after every token is revoked or expired, and even if the token table can't be read.
There's no way back to token-less mode short of deleting the rows in `wame_tokens` by hand.

## Tokens and scopes

### Format and storage

Tokens look like `wame_<id>_<secret>`: an 8-character id and 43 characters of random secret (256
bits). Only a SHA-256 hash is stored, in `wame_tokens`. The plaintext is shown **once**, when the
token is created; it can't be recovered. The `wame_` prefix makes leaked tokens easy to scan for
(`wame_[a-z2-7]{8}_[A-Za-z0-9_-]{43}`).

Names are 1–40 characters (`a-z`, `0-9`, `.`, `_`, `-`), unique, and never reused, even after
revocation. `api`, `owner`, `local`, `mcp`, `tui`, `cli`, `autoreply`, `whatsapp` and `relink` are reserved,
because the token name is what the [audit log](#get-audit) records as the actor and what owns
[queued messages](#post-queue). An existing token named `mcp` (from before it was reserved) shares
its queue items with token-less `wame mcp`: revoke it and issue one under another name.

### Scopes

| Scope | Allows |
|---|---|
| `read` | reading status, chats, messages, events, media, contacts (names and tags), groups |
| `send` | sending and the other actions that write to WhatsApp (react, edit, delete, read receipts, typing, number checks) |
| `admin` | everything, including login/logout, the audit log, managing tokens and your own contact list (and its notes). Implies `read` and `send` |

| Route | Scope | Tokens limited to chats |
|---|---|---|
| `GET /status` | read | allowed; `jid` and `push_name` are hidden. `qr` is shown to `admin` only |
| `GET /qr`, `POST /pair`, `POST /logout` | admin | n/a (admin tokens can't be limited) |
| `POST /send`, `POST /send/file` | send | only to allowed chats (403 otherwise) |
| `POST /react`, `/edit`, `/delete`, `/read`, `/typing` | send | only in allowed chats; message ids are looked up in that chat only |
| `GET /messages`, `/messages/wait`, `/events` | read | results limited to allowed chats; `chat=` outside the list is 403, and a person's `chat=` matches only the 1:1 chat |
| `GET /media/{seq}` | read | 404 for messages outside the allowed chats |
| `GET /chats` | read | only allowed chats are listed |
| `GET /contacts` | read | only allowed chats are listed |
| `GET /contacts/{number}` | read (the full profile needs an unrestricted admin token) | only allowed chats (403 otherwise), the basic view |
| `GET /contacts/{number}?refresh=1` | admin | 403, before anything is looked up |
| `GET /contacts/tags`, `GET /groups` | read | 403 |
| `PUT`, `PATCH`, `DELETE /contacts/{number}`, `DELETE /contacts`, `POST /contacts/import`, `GET /contacts/export` | admin | n/a |
| `POST /check` | send | 403 |
| `GET /audit`, `GET /stats`, `GET /metrics`, `GET /logs`, `GET /settings`, `PATCH /settings`, `POST /webhook/test`, `POST /webhook/replay`, `POST /alerts/test` | admin | n/a |
| `GET /optouts`, `POST /optouts`, `DELETE /optouts/{chat}` | admin | n/a |
| `GET /tokens`, `POST /tokens`, `DELETE /tokens/{name}` | admin | n/a |
| `GET /docs`, `GET /openapi.yaml` | none | public |
| `/mcp` | per tool (see [MCP.md](MCP.md)) | same rules per tool call |

A missing scope is 403 `this token lacks the <scope> scope`.

### Chat allowlists

`-chats` / `chats` limits a token to specific chats: phone numbers (with country code) or JIDs
(`…@s.whatsapp.net`, `…@g.us`).

- A group JID allows the whole group. A person's number allows only the 1:1 chat, not what they
  write in groups.
- Phone entries also match the JID WhatsApp actually uses for that number (for example with or
  without Brazil's 9th digit). That resolution happens for the allowlist entries only, is cached for
  10 minutes (30 seconds when a lookup failed because the server was offline or busy, so a token
  isn't locked out after a restart), and is never driven by the caller's input.
- What a restricted token sends you (`to`, `chat`, `?chat=`) is **never looked up on WhatsApp**. It's
  parsed locally and compared with the list, so it can't probe numbers or spend the shared lookup
  budget. Anything outside the list gets the same 403: `this token can't access that chat`.
- The tag counts, the group list and number checks reveal data beyond the list, so they're 403:
  `not available to tokens limited to specific chats`. `GET /contacts` lists only the allowed chats.
- Admin tokens can't have an allowlist.

### Rate, expiry, revocation

- **Rate**: each token has its own send limit per minute, **10 by default**, on top of the global
  [limits](#rate-limiting). It counts sends, files, reactions, edits, deletes, read receipts and
  typing. `wame token create -rate 0`, or `"rate": 0` over the API, means global limit only.
  Over the limit (more than 30 s wait) → 429.
- **Expiry**: `-expires 720h` / `"expires_in": "720h"`. Expired tokens get 401 `token expired`.
- **Revocation**: `wame token revoke NAME` or `DELETE /tokens/{name}`. It takes effect on the next
  request, and open `/events` and `/messages/wait` connections close within 10 seconds. Revoking a
  token also revokes every token it created, recursively. Revoked tokens get 401 `token revoked`.
- **Streams**: at most 4 open `/events` + `/messages/wait` connections per token; more get 429.

### Failed logins

401 means the token is missing, malformed, unknown, revoked or expired (`unauthorized`,
`token revoked`, `token expired`, or `use 'Authorization: Bearer <token>'`). After 10 failed attempts
in a minute from one IP, that IP gets **429** (`Retry-After: 60`) for a minute, even with a valid
token.

## Errors

Non-2xx responses always look like this:

```json
{ "error": "not logged in" }
```

| Code | Meaning |
|---|---|
| 400 | bad input: missing fields, invalid JSON (or anything after the JSON value), bad recipient, a number that isn't on WhatsApp, unknown or deleted `reply_to`, editing someone else's message or one older than 20 minutes, idempotency key over 200 characters, media over the 100 MB download cap |
| 401 | missing, malformed, unknown, revoked or expired token; or `?token=` on a route that doesn't accept it |
| 403 | the token lacks the scope, or is limited to chats and the request is outside them (or uses `/contacts/tags`, `/groups`, `/check`, `?refresh=1`); a token tried to create an admin token; or token-less mode and the request isn't a local same-origin one |
| 404 | not found: no QR pending, unknown message `id` in that chat, unknown `seq`, a message without media, a deleted message's media, or `DELETE /contacts/{number}` for someone without your own fields |
| 409 | an idempotency key was reused for a different request (a different `file` counts); canceling a queued message that is already sending or finished; deleting a queue file that queued messages still need |
| 410 | media expired on WhatsApp's servers (`/media`) |
| 413 | the body is too large: over 1 MB of JSON (16 MB for `/queue`, 64 KB for `/contacts/{number}`), a contacts CSV over 4 MB, a file over 64 MB, or a queue file over a quota (the message says which one) |
| 415 | a queue file whose content isn't an allowed type (see [queue files](#post-queuefiles-get-queuefiles-delete-queuefilesid)) |
| 429 | rate limited (global sends or lookups, or the token's own rate), too many open streams for the token, or too many failed logins from your IP; see `Retry-After` (seconds) |
| 500 | an internal error (the database, for instance). The message is just `internal error`; the details are in the server log |
| 502 | WhatsApp rejected or failed the request (also "not logged in") |
| 507 | the server couldn't write a queue file to disk |
| 504 | the request timed out or was cancelled while waiting |

## The Message object

Returned by `/send`, `/send/file`, `/react`, `/edit`, `/delete`, `/messages`, `/messages/wait`,
the `message` and `update` SSE events and the webhook:

```json
{
  "seq": 1042,
  "id": "3EB0C767D26A1D0F5E2A",
  "chat": "5511999999999@s.whatsapp.net",
  "sender": "5511999999999@s.whatsapp.net",
  "push_name": "Maria",
  "from_me": false,
  "is_group": false,
  "type": "image",
  "text": "look at this",
  "reply_to": "3EB0A1B2C3D4E5F60718",
  "media": {
    "mimetype": "image/jpeg",
    "size": 183204,
    "path": "/media/1042"
  },
  "timestamp": "2026-10-10T22:40:00-03:00"
}
```

| Field | |
|---|---|
| `seq` | monotonic number assigned when wame stores the message. Use it as a cursor (`since`) |
| `id` | WhatsApp message ID. Unique within a chat; use it with `reply_to`, `/react`, `/edit`, `/delete`, `/read` |
| `chat` | the conversation: the person's JID in a 1:1 chat, the group JID in a group |
| `sender` | who wrote it. In groups that's the person, not the group |
| `push_name` | the sender's WhatsApp profile name |
| `type` | `text`, `image`, `video` (including round video notes), `document`, `audio`, `sticker`, `reaction`, `location` |
| `text` | body, caption, file name for documents without a caption, the emoji for reactions, `lat,long` for locations. Empty after a delete |
| `reply_to` | omitted unless set: the `id` of the quoted message, or for reactions the message reacted to |
| `media` | omitted for non-media types. `path` is the download URL relative to the API base |
| `edited`, `deleted` | omitted unless `true`. A deleted message keeps its `media` metadata, but the file can't be downloaded any more |

Messages are stored in the `wame_messages` table of the session database, so they survive
restarts. Messages from before the bot was linked aren't there, except for whatever WhatsApp
sends in its one-time history sync right after a fresh login. Those imported messages also get
new `seq` values (with old timestamps), so a cursor reader started before the import will see them.
Edits found in the history sync are applied to the stored message instead of being stored twice.

`seq` comes from an `AUTOINCREMENT` column, so it only grows and is never reused, even if rows are
deleted.

Edits and deletes made by other people update the stored message only when they come from the
message's original author, or (for deletes) from a group admin removing someone's message.
Anything else is ignored.

Reactions are stored as their own messages (`type: "reaction"`, `reply_to` = the target).
An empty `text` means the reaction was removed.

## Filters

`/messages`, `/messages/wait` and `/events` accept the same query parameters. The webhook
filter (`-webhook-filter`) uses the same syntax.

| Query | |
|---|---|
| `chat` | phone number or JID. Matches the chat **or** the sender, so a person's number also matches what they write in groups |
| `type` | comma-separated list, e.g. `text,image` |
| `from_me` | `true` or `false` |
| `is_group` | `true` or `false` |
| `since` | a `seq`; only messages with a higher `seq` match |
| `q` | text search: words that must all appear in the message text (see below) |

Unknown types or non-boolean values are a 400.

**Text search (`q`).** Every word must match a whole word of the message text, except the last one,
which matches as a prefix, so `q=bom di` finds "bom dia" while you're still typing. Case and accents
are ignored (`cafe` finds "café"). Punctuation is ignored. Captions and document file names are
searched too, since they're stored as the message text. Deleted messages aren't found (their text
is cleared), and edited messages are found by their current text. In SQL this uses an FTS5 index
(`wame_messages_fts`); for live `/events`, `/messages/wait` wake-ups and the webhook the same rule
is applied in Go.

```sh
curl -H "Authorization: Bearer $T" "localhost:8080/messages?q=bom%20dia&chat=5511999999999"
```

---

## `GET /status`

```sh
curl -H "Authorization: Bearer $T" localhost:8080/status
```

```json
{
  "connected": true,
  "logged_in": true,
  "jid": "5511988887777@s.whatsapp.net",
  "push_name": "Maria",
  "state": "connected"
}
```

`state` is one of `starting`, `connecting`, `waiting_for_qr_scan`, `paired`, `connected`,
`disconnected`, `qr_timeout`, `logged_out`, `login_error: …`. While waiting for a scan, `qr`
holds the raw QR string.

## `GET /qr`

Returns the current login QR as a PNG. 404 if you're already logged in or no code is pending.
QR codes rotate about every 20 seconds, so reload if it expires. After all codes time out the
bot starts a new round by itself.

```sh
open "http://localhost:8080/qr?token=$T"
```

## `POST /pair`

Log in with a pairing code instead of a QR. Must be called while not logged in (400 otherwise).

```sh
curl -H "Authorization: Bearer $T" -X POST localhost:8080/pair -d '{"phone":"+5511988887777"}'
```

```json
{ "code": "ABCD-EFGH" }
```

On the phone: **Linked devices → Link a device → Link with phone number instead**.

## `POST /logout`

Unlinks the device from your phone and wipes the session from `wame.db`. If the unlink worked, it
also deletes your own contacts (names, tags, notes, language, time zone), the WhatsApp profile
cache, and group names, admins and members, in one transaction (messages and the audit log are
kept). Opt-outs are kept, so a logout can't let anyone who opted out be messaged again.
The same happens when the phone unlinks the device. A disconnect or restart while still logged in
keeps everything. The state becomes `logged_out` (sent on `/events` as a status). The wipe is
audited as `contacts_wipe` (detail `N contacts`, never names). Run `wame backup` first if you want to keep your notes. You'll need to log in again afterwards
(restart the process to get a new QR). Returns `{"ok": true}`.

## `POST /send`

Send a text message.

```sh
curl -H "Authorization: Bearer $T" -X POST localhost:8080/send \
  -d '{"to":"5511999999999","text":"Hello!"}'

# reply to (quote) a message, safe to retry
curl -H "Authorization: Bearer $T" -H "Idempotency-Key: order-1234-shipped" -X POST localhost:8080/send \
  -d '{"to":"5511999999999","text":"Shipped ✅","reply_to":"3EB0C767D26A1D0F5E2A"}'
```

| Field | Required | |
|---|---|---|
| `to` | yes | phone number or JID (see README → Recipients). Numbers are checked with WhatsApp first, so `5521987654321` and `552187654321` both reach the same person. A number that isn't on WhatsApp is a 400 |
| `text` | yes | message body; WhatsApp formatting like `*bold*` and `_italic_` works, see [SENDING.md](SENDING.md#formatting) |
| `reply_to` | no | `id` of a stored message in the same chat to quote. Unknown or deleted → 400. If the message was edited, the quote shows its current text |
| `idempotency_key` | no | up to 200 characters, see below. The `Idempotency-Key` header does the same and takes precedence |
| `send_at` | no | send later: RFC 3339 with offset (`2026-10-21T09:00:00-03:00`, that instant; a time in the past goes out as soon as possible), or a date and time without offset (`2026-10-21T09:00`, 09:00 on the recipient's clock: their contact `time_zone`, else `queue_timezone`). At most 30 days ahead. See [below](#scheduled-sends) |
| `send_before` | no | with `send_at`: don't send after this. A duration after `send_at` (`2h`, `1d`) or a time as above; at most 7 days after it. Past it the item ends `expired` |
| `batch` | no | the queue batch of a scheduled send (letters, digits, `.`, `_`, `-`); generated (`b-…`) when left out |

Returns the sent Message, or, with `send_at`, `send_before` or `batch`, a **202** (see below).

**Idempotency.** If you send the same key again within 24 hours, you get the first result back
and nothing is sent again. A request that arrives while the first one is still in flight waits
for it. Failed sends aren't remembered, so you can retry them with the same key. Keys are kept
in memory (lost on restart) and are separate per token and for `/send` and `/send/file`.

A key is bound to the request it was first used with: `to`, `text` and `reply_to` for `/send`;
`to`, file name, `caption`, `reply_to` and the file's SHA-256 for `/send/file`. Reusing a key for
a different request within 24 hours is a **409**:

```json
{ "error": "idempotency key was already used for a different request" }
```

### Scheduled sends

With `send_at`, `send_before` or `batch`, the message goes through the [send queue](QUEUE.md) as one
item, and the answer is the `POST /queue` one: **202** with `{batch, queued, replayed, items,
skipped, skipped_rows}` (the item has no text). Quiet hours, opt-outs, the rate limits and retries
apply, as for any queued message. Without those fields nothing changes.

```sh
curl -H "Authorization: Bearer $T" -H "Idempotency-Key: hello-1" -X POST localhost:8080/send \
  -d '{"to":"5511999999999","text":"Good morning!","send_at":"2026-10-21T09:00","send_before":"2h"}'
```

- The token needs `send` and, if it has an allowlist, the chat must be on it, as for an immediate send.
- `Idempotency-Key` is kept as a queue key (`send:` plus the SHA-256 of the key, so any key the API
  accepts works): it lasts as long as the queue keeps the
  item (7 days after it finishes), survives a restart, and can't collide with a `POST /queue` key
  from the same token. The same key with a different `to`, `text`, `reply_to`, `send_at` or `batch` is a 409.
- `POST /send/file` has no `send_at`: a request with `send_at` or `send_before` is refused with 400 (never sent at once). Schedule a file with [`POST /queue`](#post-queue) and a queue file.
- Only the two fixed time forms are taken here. A delay or "next 09:00" would mean something
  different on a retry; `wame send -at`, the MCP tools, the TUI and the admin panel do take them.

## `POST /send/file`

Multipart upload. Images (detected from the file content) are sent as photos, everything
else (including videos and audio) as a document. Max 64 MB (413 above that). The recipient and
the token's chat allowlist are checked before the file is read.

```sh
curl -H "Authorization: Bearer $T" -X POST localhost:8080/send/file \
  -F to=5511999999999 -F caption="invoice" -F file=@invoice.pdf
```

| Field | Required | |
|---|---|---|
| `to` | yes | recipient |
| `file` | yes | the file |
| `caption` | no | caption text |
| `reply_to` | no | `id` of a message in the same chat to quote |
| `idempotency_key` | no | same as `/send`; the `Idempotency-Key` header also works |

Returns the sent Message.

## `POST /react`

```sh
curl -H "Authorization: Bearer $T" -X POST localhost:8080/react \
  -d '{"chat":"5511999999999","id":"3EB0C767D26A1D0F5E2A","emoji":"👍"}'
```

| Field | Required | |
|---|---|---|
| `chat` | yes | the chat the message is in |
| `id` | yes | the message to react to (must be stored; 404 otherwise) |
| `emoji` | no | the reaction. Empty removes your reaction |

Returns the reaction as a Message (`type: "reaction"`). Counts against the rate limit.

## `POST /edit`

```sh
curl -H "Authorization: Bearer $T" -X POST localhost:8080/edit \
  -d '{"chat":"5511999999999","id":"3EB0C767D26A1D0F5E2A","text":"fixed typo"}'
```

Only your own **text** messages can be edited, and only within 20 minutes of sending (WhatsApp's
edit window, `whatsmeow.EditWindow`); otherwise 400. Returns the updated Message with
`edited: true`. Counts against the rate limit.

## `POST /delete`

```sh
curl -H "Authorization: Bearer $T" -X POST localhost:8080/delete \
  -d '{"chat":"5511999999999","id":"3EB0C767D26A1D0F5E2A"}'
```

Deletes the message for everyone. You can delete your own messages, and other people's
messages only in groups where you're an admin (wame doesn't check admin rights; WhatsApp
does). Deleting someone else's message in a 1:1 chat is a 400. Returns the updated Message
with `deleted: true` and empty `text`. wame also drops its stored copy of the original message,
so its media can't be downloaded afterwards (404) and it can't be quoted. Counts against the
rate limit.

## `POST /read`

Sends read receipts (blue ticks) for incoming messages.

```sh
curl -H "Authorization: Bearer $T" -X POST localhost:8080/read -d '{"chat":"5511999999999"}'
curl -H "Authorization: Bearer $T" -X POST localhost:8080/read \
  -d '{"chat":"5511999999999","ids":["3EB0C767D26A1D0F5E2A"]}'
```

| Field | Required | |
|---|---|---|
| `chat` | yes | the chat |
| `id` / `ids` | no | one id or a list. Without either, the latest 50 incoming messages in the chat are marked |

Your own messages, reactions and unknown ids are skipped. Returns `{"marked": 3}`.

It also moves the chat's **read position** used for `unread` in [`/chats`](#get-chats): without
ids, the whole chat counts as read; with ids, everything up to the newest of those messages.

## `POST /typing`

```sh
curl -H "Authorization: Bearer $T" -X POST localhost:8080/typing \
  -d '{"chat":"5511999999999","state":"typing"}'
```

`state` is `typing` (the default), `recording` (voice message) or `paused`. WhatsApp clears
the indicator by itself after a while. Returns `{"ok": true}`. Each call counts against the
number-lookup limit (see [Rate limiting](#rate-limiting)).

WhatsApp only relays typing indicators while the account is "available". Unless wame runs with
`-online`, it switches to available while any chat shows an indicator and back to unavailable
once every indicator is `paused` or 25 seconds old, so a client that never sends `paused` can't
leave you online.

## `POST /queue`

Queues texts (or a file with captions, see [queue files](#post-queuefiles-get-queuefiles-delete-queuefilesid)) in the database and answers **202** at once; a worker sends them in the background
within the rate limits, retrying and backing off on errors, and keeps going after a restart. For
bulk sends, where `POST /send` would hold the request open for minutes. Requires `send`; a token
limited to some chats can only queue to those.

```sh
curl -H "$H" -X POST localhost:8080/queue -H "Idempotency-Key: promo-1" -d '{
  "batch": "promo-october", "text": "Hi! Our store opens at 9 tomorrow.",
  "messages": [{"to": "5511999999999"}, {"to": "5511988887777", "text": "Hi John!", "send_at": "2026-10-13T09:00:00-03:00"}]
}'
curl -H "$H" "localhost:8080/queue?batch=promo-october&status=failed"
curl -H "$H" -X DELETE "localhost:8080/queue?batch=promo-october"     # cancel what hasn't gone out
```

The answer has `batch`, the `items` (without text), `queued` (how many items) and `replayed` (how
many of them were already queued under the same `Idempotency-Key`, so a retry shows what it
didn't add). The same key with different messages (`to`, `text`, `reply_to`, `send_at`, `spread` or a named
`batch`) is a 409 and queues nothing.

**When it goes out.** `send_at` (top level, or per message) takes two forms: RFC 3339 with an
offset (that instant), or a date and time without offset like `2026-10-21T09:00`, which is 09:00
**on the recipient's clock**: the contact's `time_zone`, else `queue_timezone` (groups use
`queue_timezone`; each member of a `tag:` row is resolved on their own). An absolute time in the
past is accepted and goes out as soon as possible; more than 30 days ahead is a 400. Details in
[QUEUE.md](QUEUE.md#when-it-goes-out).

- `send_before` (top level as a default, or per message): a duration after the item's own time
  (`2h`, `+2h`, `1d`) or a time. It must be after `send_at` and at most 7 days after it. If the
  item hasn't been sent by then it ends in the final state `expired` instead of going out (checked
  about every minute, also while the queue is paused or in quiet hours). Expiring isn't a failure and
  doesn't count toward alerts; the webhook `queue` event fires for it. It isn't part of a request's
  identity for idempotent retries.
- `spread` (a duration of at most `24h`, JSON field or `?spread=` for CSV): the rows' times are spread
  evenly over the window, row `i` of `n` due at its `send_at` (or now) plus `i × spread / (n − 1)`.
  A retry compares `send_at` and `spread`.

**Tags, templates and skips** ([details](QUEUE.md#tags-templates-and-skips)):

- A `to` of `tag:clients` queues one item per person with that [contact tag](#contacts), at enqueue
  time. Needs `read` as well as `send`; a token limited to chats reaches only the members on its
  list (the others are dropped without a trace). An empty tag is a 400.
- `"template": true` fills `{name}`, `{first_name}` and `{push_name}` (`{name|there}` for a
  fallback, `{{` and `}}` for braces) per recipient; a message's own `name` wins. Needs `read`.
- `"dedupe": true` skips a row whose recipient, file and text appeared earlier in the request;
  `"skip_sent": "48h"` (or `"3d"`, at most 90 days; 7 without `read`) or `"batch:promo-october"`
  skips recipients who already got a message.
- Skipped rows are counted in `skipped` (`{"duplicate": 4, "recently_sent": 8}`) and listed in
  `skipped_rows` (the first 100: `line` for CSV, `index`, `to`, `reason`). A request where every row
  was skipped is still a 202, with `queued: 0`.

**As CSV.** With `Content-Type: text/csv` (or `application/csv`) the body is a CSV file instead: a
header line naming the columns (`to` required; `text`, `send_at`, `reply_to`, `name`, `send_before`; any order;
`,` or `;` between fields) and one message per line, with `batch`, `send_at`, `send_before`, `spread`, `text`, `template`,
`dedupe` and `skip_sent` in the query. Same limits (1000 rows, 16 MB, allowlist per row,
`Idempotency-Key`); an unknown column is a 400 that names it, and errors name the file's line
(`line 7: invalid recipient "12"`). Put the text in the file rather than in `?text=`: query
strings are kept in proxy logs and shell history. See [CSV import](QUEUE.md#csv-import), and
`wame queue import` for files of any length.

```sh
curl -H "$H" -H "Content-Type: text/csv" --data-binary @promo.csv "localhost:8080/queue?batch=promo-october"
```

**With a file.** `file` (the id from `POST /queue/files`) at the top level is the default for every
message, or per message; with a file, `text` is the caption and may be empty. A CSV takes `?file=`
as the default. The file must be your own (admin included), or the answer is 404; an id that isn't
`f_` plus 32 hex digits is a 400. The idempotency check compares the file too.

```sh
curl -H "$H" -X POST localhost:8080/queue -d '{
  "batch": "menu-october", "file": "f_0123456789abcdef0123456789abcdef", "text": "Our new menu",
  "messages": [{"to": "5511999999999"}, {"to": "5511988887777", "text": "Hi John, the menu"}]
}'
```

`GET /queue` lists items newest first (`status`: `queued`, `sending`, `sent`, `failed`, `canceled` or `expired`; `batch`, `actor`, `before`, `limit` ≤ 500) with
`counts` by state (including `expired`); `GET /queue/{id}` shows one; `DELETE /queue/{id}` cancels one (409 once it's
sending or finished). Without `admin`, a token sees and cancels only its own items.
An item with a `send_before` shows it, and one changed with `PATCH` has `edited_at`.
Items that carry a file also have `file` (its id), `kind` (`image` or `document`) and `file_size`;
text items have none of the three. Never the file's name.
Sent items gain `delivered_at` and `read_at` as WhatsApp's receipts come in, and `counts` has
`delivered` and `read` among the sent ones.
[QUEUE.md](QUEUE.md) has the details: states, receipts, retries, pacing, pausing, quiet hours, and
the webhook `queue` event.

### `PATCH /queue/{id}`

Changes a queued item before it goes out. Requires `send`; without `admin`, only your own items
(someone else's is a 404).

```sh
curl -H "$H" -X PATCH localhost:8080/queue/41 -d '{"send_at":"2026-10-22T09:00","send_before":"2h"}'
curl -H "$H" -X PATCH localhost:8080/queue/41 -d '{"send_before":null}'      # no deadline any more
curl -H "$H" -X PATCH localhost:8080/queue/41 -d '{"text":"See you at 10 instead."}'
```

| Field | |
|---|---|
| `send_at` | the new time, in either of the two forms above (a date and time without offset is read on the recipient's clock). At most 30 days ahead; a past time sends as soon as possible |
| `send_before` | a new deadline (a duration after the new time, or a time), or `null` or `""` to remove it |
| `text` | the new text: not empty (unless the item is a file, whose caption may be), at most the queue's text limit |

Send any of them. Answers 200 with the item (with its text, like `GET /queue/{id}`) and an `edited_at`.
It's a **409** when the item is no longer `queued` (sending, sent, failed, canceled or expired), and
a 400 when no field is given, or the new `send_at` isn't before the item's existing `send_before` (change
or clear it in the same request). The change is audited as `queue_edit` with the field names, never the text.

The `send_at` the item was queued with stays stored, so running the same `POST /queue` again still
replays instead of answering 409; an edited text is compared through a hash of the original.

## `POST /queue/files`, `GET /queue/files`, `DELETE /queue/files/{id}`

Files for the [send queue](QUEUE.md#files): upload an image or document once, then reference its
id from `POST /queue` for any number of messages. Requires `send`. A file belongs to the token
that uploaded it (its name); you only see, use and delete your own, and admin sees every actor's
files, without the other actors' `name`. No route returns the content.

```sh
curl -H "$H" -F file=@menu.pdf localhost:8080/queue/files
curl -H "$H" localhost:8080/queue/files
curl -H "$H" -X DELETE localhost:8080/queue/files/f_0123456789abcdef0123456789abcdef
```

```json
{ "id": "f_0123456789abcdef0123456789abcdef", "actor": "promo-bot", "sha256": "9f86d0…", "size": 482113,
  "mime": "application/pdf", "kind": "document", "name": "menu.pdf", "created_at": "2026-10-15T10:00:00-03:00",
  "items": 0, "open": 0, "expires_at": "2026-10-16T10:00:00-03:00" }
```

- **`POST`** takes `multipart/form-data` with one part named `file`, streamed to disk, up to 64 MB
  (the same limit as `/send/file`). It answers **201** with the file, or **200** when you already
  have a live file with the same `sha256` (never matched across tokens). The type comes from the
  bytes, not the client's `Content-Type`: JPEG, PNG and WebP as images; PDF, plain text, CSV and
  `.docx` `.xlsx` `.pptx` `.odt` `.ods` `.odp` as documents; anything else is a 415, text starting
  with `<` (HTML, SVG, XML) too. The file name's extension has to match the content (415 if not;
  a name without one gets one). Images over 16 MB, or over 16384 px a side or 100 MP, go as
  documents.
- **`GET`** answers `{"files": [...], "usage": {"files": 2, "bytes": 482900}}`: your live files and
  what they take (admin: everyone's, and the total). `items` is how many queue items use the file,
  `open` how many of those are still queued or sending, and `expires_at` appears only while no item
  has used the file yet (24 hours after upload it is deleted; see [cleanup](QUEUE.md#files)).
- **`DELETE`** answers `{"deleted": true}`. 409 while queued or sending items use it (cancel them
  first); 404 for an unknown id or another token's file (not 403, so ids don't leak). Admin may
  delete any.
- **Errors**: 413 over 64 MB or over a quota (50 files and 500 MB per token, and `queue_files_quota`
  in all; the message says which, checked before anything is written), 415 type (also text that
  isn't UTF-8, and Office archives over 10,000 entries), 429 with `Retry-After` when two uploads of
  this token (or eight in all) are already running, 507 when the server can't write the file.
  Each upload is audited as `queue_file`, each delete as `queue_file_delete`.

## `GET /optouts`, `POST /optouts`, `DELETE /optouts/{chat}`

Chats that don't get [queued](#post-queue) messages. Requires `admin`. A person is added when they
send a 1:1 message that is only `STOP`, `SAIR`, `PARAR`, `CANCELAR`, `UNSUBSCRIBE` or
`DESCADASTRAR` (case and accents ignored, so `Sáir` counts); wame doesn't reply. Queued items to a
listed chat are still accepted, then fail with `recipient opted out` when their turn comes (the
check runs on the resolved chat, so either form of a Brazilian number matches). `/send`, the TUI
and the CLI aren't affected.

```sh
curl -H "$H" localhost:8080/optouts
curl -H "$H" -X POST localhost:8080/optouts -d '{"chat": "5511999999999"}'
curl -H "$H" -X DELETE localhost:8080/optouts/5511999999999
```

```json
[ { "chat": "5511999999999@s.whatsapp.net", "reason": "STOP", "at": "2026-10-12T21:40:02-03:00", "by": "whatsapp" } ]
```

- `reason` is the keyword they sent, or `manual`; `by` is `whatsapp` or the token that added it.
- `POST` takes a number or JID (numbers are checked with WhatsApp when logged in) and answers
  201 `{"chat": "…", "added": true}`, or 200 with `"added": false` when it was already listed. A
  group is a 400. `DELETE` answers 404 when the chat isn't listed.
- Audited as `optout_add` (actor `whatsapp` for keywords) and `optout_remove`.

## `GET /messages`

Stored messages (incoming and sent), filtered with the [shared filters](#filters).

- **Without `since`:** newest first by timestamp. Good for "show me the last 20".
- **With `since`:** oldest first by `seq`, starting after `since`. Good for paging through
  everything or polling for new messages: pass the last `seq` you got as the next `since`.

| Query | Default | |
|---|---|---|
| `limit` | 50 | max items (up to 1000) |
| `chat`, `type`, `from_me`, `is_group`, `since`, `q` | | see [Filters](#filters) |

```sh
curl -H "Authorization: Bearer $T" "localhost:8080/messages?chat=5511999999999&limit=20"

# search every chat, newest first
curl -H "Authorization: Bearer $T" "localhost:8080/messages?q=invoice"

# page through every incoming text, oldest first
curl -H "Authorization: Bearer $T" "localhost:8080/messages?since=0&type=text&from_me=false&limit=500"
```

## `GET /messages/wait`

Long-poll: returns as soon as at least one message matching the filters exists after `since`,
or after `timeout` seconds with an empty list.

| Query | Default | |
|---|---|---|
| `since` | the newest stored `seq` | start after this `seq`. Leave it out to wait only for messages that arrive after the request |
| `timeout` | 30 | seconds, max 120 |
| `limit` | 100 | max items (up to 1000) |
| `chat`, `type`, `from_me`, `is_group`, `q` | | see [Filters](#filters) |

```json
{ "messages": [ { "seq": 1043, "...": "..." } ], "next_since": 1043 }
```

Pass `next_since` as `since` on the next call and you never miss or repeat a message, even
across reconnects. Set your HTTP client timeout above `timeout`.

```sh
since=0
while true; do
  r=$(curl -s -m 70 -H "Authorization: Bearer $T" \
      "localhost:8080/messages/wait?since=$since&timeout=60&from_me=false")
  echo "$r" | jq -c '.messages[]'
  since=$(echo "$r" | jq '.next_since')
done
```

## `GET /events`

[Server-Sent Events](https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events)
stream. Three event types, and a fourth on request:

```
id: 1043
event: message
data: {"seq":1043,"id":"…","chat":"…","text":"oi!", …}

event: update
data: {"seq":1040,"id":"…","text":"fixed typo","edited":true, …}

event: status
data: {"connected":true,"logged_in":true,"state":"connected", …}
```

| Event | When | Notes |
|---|---|---|
| `message` | a new message is stored (incoming, sent from your phone, or sent through wame) | carries `id: <seq>`; filtered |
| `update` | a stored message is edited or deleted, by you or by someone else | the whole updated Message; filtered (except `since`); no `id`, not replayed |
| `status` | connection or login state changes | a Status object; not filtered |
| `queue` | a [queued](#post-queue) item changes state (sending, sent, failed, canceled, retry) or a sent one is delivered or read | only with `events=queue`; the item without its text; your own items unless the token has `admin` |

| Query | |
|---|---|
| `events` | comma-separated subset of `message,update,status,queue`. Default: `message,update,status` |
| `chat`, `type`, `from_me`, `is_group`, `since`, `q` | see [Filters](#filters) |

**Resuming.** The stream first replays every stored matching message after a starting `seq`,
then continues live. The starting point is, in order:

1. the `Last-Event-ID` header, if it's a number. Browsers' `EventSource` sends it automatically
   when it reconnects, so a dropped connection picks up where it left off, even if the URL has
   `since`. A non-numeric value is ignored.
2. `?since=`
3. otherwise "now": only messages stored after you connect.

Messages imported by a history sync while you're connected are streamed as well.

`message` events are read from the database, so a slow client doesn't lose them. `update` and
`status` events can be dropped if the client falls far behind.

A `: ping` comment goes out every 25 s to keep proxies from closing the connection. When the
server shuts down it ends open streams at once (and answers open `/messages/wait` calls with no
messages), so reconnect with `Last-Event-ID` or your cursor.

```sh
curl -N -H "Authorization: Bearer $T" "localhost:8080/events?from_me=false&events=message"
```

```js
const es = new EventSource("http://localhost:8080/events?token=T&type=text");
es.addEventListener("message", e => console.log(e.lastEventId, JSON.parse(e.data)));
es.addEventListener("update", e => console.log("edited/deleted", JSON.parse(e.data)));
```

## `GET /media/{seq}`

Downloads and decrypts the media of a stored message (photo, video, voice message, sticker,
document). `seq` comes from the Message; `media.path` already has the full path.

```sh
curl -H "Authorization: Bearer $T" -OJ localhost:8080/media/1042
```

Images (except SVG), audio, video and PDF are served `inline` with their original
`Content-Type`. Everything else is served as `application/octet-stream` with
`Content-Disposition: attachment`, so a browser downloads it instead of rendering it. The file
name comes from the message, or is `wame-<type>-<seq>.<ext>` when there isn't one. Every
response also has `X-Content-Type-Options: nosniff`, `Content-Security-Policy: sandbox;
default-src 'none'` and `Referrer-Policy: no-referrer`.

| Code | |
|---|---|
| 400 | file is over the 100 MB download cap. The cap applies to the bytes actually downloaded, not just the size the sender declared |
| 404 | unknown `seq`, the message has no media, or it was deleted |
| 410 | WhatsApp no longer has the file (it answered 403, 404 or 410; media expires on their servers after a while) |

Media is fetched from WhatsApp on each request; wame doesn't keep a copy.

## `GET /chats`

Conversations ordered by last activity, built from stored messages. Reactions don't count
as activity.

| Query | Default | |
|---|---|---|
| `limit` | 50 | max chats (up to 1000) |

```sh
curl -H "Authorization: Bearer $T" "localhost:8080/chats?limit=10"
```

```json
[
  {
    "jid": "552187654321@s.whatsapp.net",
    "name": "Maria",
    "is_group": false,
    "last_text": "tudo e vc",
    "last_type": "text",
    "last_from_me": true,
    "last_at": "2026-10-10T20:45:50-03:00",
    "unread": 2
  }
]
```

`name` is the group subject, `Me` for your own chat, or a person's name picked like in
[`/contacts`](#contacts): your own name, the address-book name, the business name,
`~ProfileName`, then `+number`. Group names and admin lists are cached in the database
(`wame_groups`), so they're available right away at startup, even offline; they're refreshed
in the background on connect and when older than 10 minutes. Logout deletes them. A group that was never fetched
shows as `group <id>` until then.

`unread` counts incoming messages (not reactions, not deleted) stored after the chat's read
position. The read position moves forward when:

- you send a message in the chat (through wame),
- you open the chat in the TUI (locally, even when offline),
- you call `POST /read` (without ids, the whole chat),
- you read the chat on your phone or another linked device (WhatsApp's "read-self" receipts).

It's kept in `wame_chat_state`, so it survives restarts. When a database is upgraded to this
version, everything already stored counts as read, and so does history imported after a fresh
login.

## `/contacts`

People from two places, merged: the phone's address book (synced a few seconds after the first
connect) and **your own contact list**, which wame keeps in its database (`wame_contacts`) and
never writes back to WhatsApp or the phone. For each person you can keep your own name, tags,
notes, a language and a time zone.

Every view shows one name, picked in this order: your own name, the address-book name, the
business name, the name the person set in WhatsApp (shown as `~Name`), then `+number`.
`name_source` says which one it is (`own`, `book`, `business`, `push`, `number`).

None of these routes ever asks WhatsApp anything, except a `PUT` by an admin while logged in,
which resolves a number once so your fields land under the chat WhatsApp uses, and
`GET /contacts/{number}?refresh=1`.

### `GET /contacts`

Everyone in the address book or your own list, sorted by name. Notes are never in the list.

| Query | Default | |
|---|---|---|
| `q` | | part of a name, a tag, or part of the number |
| `tag` | | only people with this tag; repeat it (or separate with commas) to require several |
| `known` | | `1`: leave out people known only by the name they set in WhatsApp or by number |
| `limit` | 1000 | up to 5000 |

```sh
curl -H "Authorization: Bearer $T" "localhost:8080/contacts?tag=clients&q=maria"
```

```json
[ { "jid": "5511999999999@s.whatsapp.net", "phone": "+5511999999999", "name": "Maria",
    "name_source": "own", "own_name": "Maria", "book_name": "Maria Silva", "push_name": "Maria",
    "tags": ["clients", "vip"] } ]
```

`book_name`, `business_name` and `push_name` are there when known. A token limited to chats gets
only those chats, filtered the same way.

### `GET /contacts/tags`

```json
[ { "tag": "clients", "count": 12 }, { "tag": "vip", "count": 3 } ]
```

403 for tokens limited to chats: the counts span every contact.

### `GET /contacts/{number}`

One person's profile, by number (`5511999999999`, `+5511999999999`) or URL-escaped JID. It never
asks WhatsApp unless you pass `?refresh=1` (see below): everything comes from wame's own tables and
what WhatsApp said last time. Someone wame knows nothing about is still a 200, with only the number.
Groups are 400 `contacts are people, not groups`.

The **basic view** is for `read` tokens and for every token limited to chats:

```json
{ "jid": "5511988887777@s.whatsapp.net", "phone": "+5511988887777",
  "lid": "100000000000001@lid",
  "names": { "name": "John", "name_source": "own", "own_name": "John", "push_name": "John" },
  "own": { "tags": ["clients"] },
  "whatsapp": { "on_whatsapp": true, "stale": false } }
```

`lid` is the person's hidden WhatsApp id when wame's map knows it, else an empty string. `names`
has `name` (the one name every view shows), `name_source`, and `own_name`, `book_name`,
`business_name` and `push_name` when known. `on_whatsapp` is `null` when unknown; `stale` (in every
view) says the cached WhatsApp data is older than its limit or was never fetched.

The **full view** needs an unrestricted `admin` token (wame never issues admin tokens limited to
chats, and the handler checks this again as a safety net). It adds more to `own` and `whatsapp`,
and these top-level fields:

```json
{ "jid": "5511988887777@s.whatsapp.net", "phone": "+5511988887777",
  "lid": "100000000000001@lid",
  "names": { "name": "John", "name_source": "own", "own_name": "John", "push_name": "John" },
  "own": { "tags": ["clients"], "notes": "prefers mornings", "language": "en",
           "time_zone": "America/New_York",
           "created_at": "2026-10-13T09:12:00-03:00", "created_by": "admin-panel",
           "updated_at": "2026-10-13T09:12:00-03:00", "updated_by": "admin-panel" },
  "whatsapp": { "on_whatsapp": true, "about": "Available", "about_at": "2026-10-14T08:00:00-03:00",
                "picture": { "id": "1700000000", "url": "https://example.invalid/pic.jpg",
                             "hidden": false, "as_of": "2026-10-14T08:00:00-03:00" },
                "as_of": "2026-10-14T08:00:00-03:00", "stale": false },
  "activity": { "first_at": "2026-09-01T10:00:00-03:00", "last_at": "2026-10-14T18:30:00-03:00",
                "sent": 12, "received": 15, "unread": 1,
                "groups_in_common": [ { "jid": "120363000000000001@g.us", "name": "Team" } ] },
  "queue": { "window": "7d", "total": 2, "open": 0, "sent": 1, "delivered": 1, "read": 0,
             "failed": 0, "last_at": "2026-10-14T12:00:00-03:00" },
  "opted_out": false,
  "tokens": ["reports"] }
```

| Field | |
|---|---|
| `own` | adds `notes`, `language`, `time_zone`, `created_at`, `created_by`, `updated_at`, `updated_by` |
| `whatsapp` | adds `about`, `about_at`, `verified_name`, `verified_level`, `picture` (`id`, `url`, `hidden`, `as_of`), `business` (`address`, `email`, `categories`, `options`, `time_zone`, `as_of`), `as_of`, `error`, `error_at` |
| `activity` | from stored messages: `first_at`, `last_at`, `sent`, `received`, `unread`, `groups_in_common` (`jid`, `name`) |
| `queue` | the person's queue items in the last 7 days (`window` is `"7d"`): `total`, `open`, `sent`, `delivered`, `read`, `failed`, `last_at` |
| `opted_out` | whether the person is on the opt-out list |
| `tokens` | names of usable tokens whose chat limit names this person |

The picture `url` is a signed WhatsApp CDN link that wame drops after 6 hours.

A token limited to chats can ask only about those chats; anyone else gets the usual 403 `this
token can't access that chat`, and the number is never looked up.

#### `?refresh=1`

`refresh` takes `1`, `true`, `0` or `false`; anything else is a 400. A refresh needs an
unrestricted `admin` token (or the owner secret, or token-less local mode). Any other caller gets
403 `refresh=1 needs an admin token without a chat allowlist`, checked before anything is looked up.

Because a refresh spends lookups from a `GET`, it is also refused with 403 `refresh=1 is refused
from another site or port` when the request says it came from another page: `Sec-Fetch-Site` is
present and isn't `same-origin` or `none`, or an `Origin` or `Referer` header names a host other
than the request's `Host` (another localhost port counts). That stops a web page from triggering
refreshes with an `<img>`, above all in token-less mode. It applies with tokens too; `curl`, the CLI
and `wame admin` send none of these headers.

It asks WhatsApp now and spends 2 to 4 lookups from the shared lookup budget: whether the number
is on WhatsApp, the profile info (about, verified name), the picture when it changed, and the
business profile when there is a verified name. It runs at most once per person per 5 minutes. The
answer is the full view plus `refreshed` and `next_refresh_at`:

```json
{ "jid": "5511988887777@s.whatsapp.net", "refreshed": true,
  "next_refresh_at": "2026-10-15T10:05:00-03:00" }
```

(Shown without the other full-view fields.) A second refresh inside the 5 minutes returns the
cached profile with `refreshed: false`. When WhatsApp answers with an error, it is stored in
`whatsapp.error` and `whatsapp.error_at`, and the answer is still a 200 with `refreshed: false`.
Other errors: 429 with `Retry-After` when the lookup budget is spent, 502 when wame isn't logged
in. A refresh is audited as `contact_refresh` (`target` the chat JID, `detail` `N lookups` or the
error).

```sh
curl -H "Authorization: Bearer $T" "localhost:8080/contacts/5511988887777?refresh=1"
```

### `PUT /contacts/{number}`, `PATCH /contacts/{number}`

Admin only. The body (at most 64 KB) has any of:

| Field | |
|---|---|
| `name` | up to 100 characters, one line |
| `tags` | up to 20 tags of `a-z`, `0-9`, `_` and `-` (1 to 32 characters, lowercased) |
| `notes` | up to 2000 characters |
| `language` | a language tag like `pt-BR` |
| `time_zone` | an IANA name like `America/Sao_Paulo` |
| `add_tags`, `remove_tags` | `PATCH` only: tags to add or take away, without overwriting the others |

`PUT` replaces all your fields: one left out is cleared. `PATCH` changes only the fields given:
`null` or `""` clears one. Unknown fields are a 400, and so is a body that isn't a JSON object
(`null` included). When every field ends up empty, the person is removed from your list. Both
answer with the contact view (the list fields plus `own`), not the profile that `GET` returns.

While wame is logged in, `PUT` with a number (not a JID) looks it up once on WhatsApp, so the row
lands under the JID messages use (for example the form with or without Brazil's 9th digit). A number
WhatsApp doesn't know is stored as typed, as `PATCH` and the import do; the lookup isn't cached, so
each such `PUT` spends one. `PATCH` never looks anything up. A row found under an `@lid` is moved to
the phone number once WhatsApp tells wame which number it is; when both rows exist they are merged:
the phone row's name, language and time zone win, both notes are kept (the phone row's first,
joined by a line break; the result may be over 2000 characters, and a `PUT` then has to shorten
it), tags are united, the phone row's first, up to 20, and `updated_by` is `relink`.

```sh
curl -H "Authorization: Bearer $T" -X PATCH localhost:8080/contacts/5511999999999 \
  -d '{"name": "Maria", "add_tags": ["vip"], "notes": null}'
```

### `DELETE /contacts/{number}`, `DELETE /contacts?all=true`

Admin only. The first removes one person's own fields (404 when there are none) and answers
`{"deleted": true}`. The second empties your whole list and answers `{"deleted": 3}`; without
`?all=true` it's a 400, so a stray `DELETE /contacts` can't do it. The phone's address book is
never touched.

### `POST /contacts/import`, `GET /contacts/export`

Admin only. The import takes a CSV (at most 4 MB and 5000 rows) with a header line:

```csv
number,name,tags,notes
5511999999999,Maria,clients vip,
5511988887777,John,clients,"prefers mornings"
552187654321,,,
```

- Columns in any order and case: `number` (required; `to` and `phone` work too), `name`, `tags`
  (separated by spaces, commas, `;` or `|`), `notes`, `language`, `time_zone`. Unknown columns are
  refused by name. Same CSV rules as [the queue](QUEUE.md) (BOM, CRLF, `;` files, numbers in
  scientific notation refused).
- A column present overwrites that field (an empty cell clears it); absent columns are left alone.
- Numbers are never looked up: a row updates the person stored under that number or its variant
  with or without Brazil's 9th digit, otherwise it's stored as typed.
- The answer counts people, not lines: two lines for the same person count once, by how their row
  ended up (`added` when it was new, `unchanged` when the lines cancel out).
- One transaction: any bad line is a 400 naming it (`line 3: …`), and nothing is stored.
- `?add_tags=imported` adds tags to every row; `?dry_run=1` checks and counts without storing.

```sh
curl -H "Authorization: Bearer $T" -H "Content-Type: text/csv" \
  --data-binary @contacts.csv "localhost:8080/contacts/import?dry_run=1"
```

```json
{ "added": 2, "updated": 1, "unchanged": 0 }
```

The export writes your own list with the same columns (`number,name,tags,notes,language,time_zone`),
so importing it gives back the same list. It holds your notes: keep the file private. Cells are
written as stored; a spreadsheet may treat one starting with `=`, `+`, `-` or `@` as a formula.

Writes are audited as `contact_set` (the changed field names), `contact_delete`,
`contact_import` (`N rows: A added, U updated`) and `contact_delete_all` (`N contacts`), a refresh
as `contact_refresh` (`N lookups` or the error), and the export as `contact_export` (`N contacts`). Names, notes and tags are never in the audit `detail`;
the `error` of a rejected write may quote the value that was refused (a tag, language, time zone or
unknown field name, cut to at most 64 characters), and `/audit` is admin-only.

## `GET /groups`

```json
[ { "jid": "120363012345678901@g.us", "name": "Family", "members": 12 } ]
```

## `POST /check`

Check which numbers have WhatsApp (1 to 50 per call). Each number counts against the
number-lookup limit, so a large batch can get a 429.

```sh
curl -H "Authorization: Bearer $T" -X POST localhost:8080/check \
  -d '{"phones":["+5511999999999","15551234567"]}'
```

```json
[
  { "query": "+5511999999999", "on_whatsapp": true, "jid": "5511999999999@s.whatsapp.net" },
  { "query": "+15551234567", "on_whatsapp": false, "jid": "" }
]
```

## `GET /audit`

Control actions taken through wame, newest first: sends, files, reactions, edits, deletes,
read receipts, pairing and logout, serving the login QR (`qr`), number checks (`check`, with the
count but not the numbers), queuing, changing and canceling (`queue`, `queue_edit`, `queue_cancel`), the opt-out list (`optout_add`, `optout_remove`;
a chat that opted out by keyword is recorded with actor `whatsapp` and detail `keyword STOP`),
changes to and exports of your own contact list (`contact_set`, `contact_delete`, `contact_import`,
`contact_delete_all`, `contact_export`),
settings, tokens and webhook tests, plus WhatsApp logging the device out. Every send, file,
reaction, edit and delete attempt is recorded exactly once, including ones rejected for bad
input (`ok: false` with the error). Message text is never stored here.

| Query | Default | |
|---|---|---|
| `limit` | 100 | max entries (up to 1000) |
| `before` | latest | only entries with an `id` lower than this; pass the last `id` you got to page back |

```json
[
  {
    "id": 57,
    "at": "2026-10-10T22:41:03-03:00",
    "actor": "api",
    "action": "send",
    "target": "5511999999999@s.whatsapp.net",
    "detail": "text, 6 chars",
    "ok": true
  },
  {
    "id": 56,
    "at": "2026-10-10T22:40:12-03:00",
    "actor": "tui",
    "action": "edit",
    "target": "5511988887777@s.whatsapp.net",
    "detail": "3EB0C767D26A1D0F5E2A",
    "ok": false,
    "error": "…"
  }
]
```

`actor` is the token's name for API and MCP calls (`api` for the owner secret and token-less
mode, `mcp` for `wame mcp` without a token), or `tui`, `cli`, `autoreply` or `whatsapp`.
`action` is `send`, `send_file` (from the queue the detail is `kind, N bytes, queue #id`, without the name), `react`, `edit`, `delete`, `read`, `pair`, `logout`,
`logged_out`, `contacts_wipe` (`detail` `N contacts`), `token_create`, `token_revoke` (for the token ones, `target` is the token name and
`detail` the requested scopes), `settings` (`detail` lists the changes, e.g. `rate=10, chat_gap
reset`), `webhook_test` or `webhook_replay` (`detail` is the delivery id), `alert_test`
(`detail` is the backend, `gotify`), `qr`, `check`, `queue` (`target` is the batch, `detail` `N
messages`, plus `, M skipped` when rows were skipped), `queue_cancel` (`target` is the batch or `#id`, `detail` `N canceled`), `queue_edit` (`target` is `#id`, `detail` the field names changed, e.g. `send_at,text`),
`queue_file` (`target` is the file id, `detail` `kind, N bytes`, plus `, already uploaded` for a repeat; never the name), `queue_file_delete` (same), `queue_files_wipe` (logout; `detail` `N files`), `optout_add`
(`target` is the chat; `detail` is `added`, `already listed`, or `keyword STOP` with actor
`whatsapp` when the chat sent the keyword), `optout_remove`, `contact_set` (`target` is the chat,
`detail` the changed fields like `name, tags`, or `no change`), `contact_delete`, `contact_import`
(`target` `csv`, `detail` `N rows: A added, U updated`, plus ` (dry run)`), `contact_delete_all`
or `contact_export` (`target` `csv`, `detail` `N contacts`). See
[proposed-features/ADMIN-PANEL.md](proposed-features/ADMIN-PANEL.md#logging-today) for what each one records.

## `GET /stats`

Health of the parts that can quietly fall behind: the limiters, the webhook, the send queue and
alerts. Requires `admin`;
it's what the [admin panel](ADMIN.md)'s Health tab shows. Counters are kept in memory and reset
when wame restarts. The webhook URL, its secret and message content are never included.

```json
{
  "version": "0.3.0",
  "started": "2026-10-12T09:00:00-03:00",
  "auto_reply": true,
  "limits": {
    "send": { "per_minute": 20, "chat_gap_ms": 1000, "max_wait_ms": 30000, "last_minute": 4, "rejected": 1, "last_rejected": "2026-10-12T21:40:02-03:00" },
    "lookups": { "per_minute": 60, "chat_gap_ms": 0, "max_wait_ms": 30000, "last_minute": 0, "rejected": 0 },
    "auto_reply": { "per_minute": 6, "chat_gap_ms": 10000, "max_wait_ms": 1, "last_minute": 0, "rejected": 0 }
  },
  "webhook": {
    "queued": 0, "capacity": 1000,
    "delivered": 812, "retried": 3, "failed": 1, "dropped": 0,
    "last_success": "2026-10-12T21:41:00-03:00",
    "last_failure": "2026-10-12T20:02:11-03:00",
    "recent_failures": [
      { "at": "2026-10-12T20:02:11-03:00", "delivery": "4711-message-a1b2c3d4", "event": "message", "attempt": 1, "status": 400, "gave_up": true }
    ]
  },
  "queue": {
    "queued": 120, "sending": 1, "sent": 880, "failed": 3, "canceled": 0, "expired": 0, "delivered": 871, "read": 640,
    "oldest_queued": "2026-10-12T21:00:00-03:00", "next_due": "2026-10-12T21:41:03-03:00",
    "paused": false, "running": true, "jitter_ms": 2000,
    "quiet_hours": "21:00-08:00", "timezone": "America/Sao_Paulo", "quiet_until": "2026-10-13T08:00:00-03:00",
    "files": 2, "file_bytes": 482900, "files_quota": 2147483648
  },
  "alerts": {
    "configured": true, "backend": "gotify", "delay_ms": 120000, "sent": 2, "failed": 0,
    "last_sent": "2026-10-12T21:30:00-03:00", "last_title": "wame: WhatsApp is back"
  }
}
```

- `limits.*.last_minute` counts slots reserved in the last minute, including sends still waiting
  their turn; `next_free` appears when the per-minute budget is used up.
- `rejected` counts 429s for `send` and `lookups`, and dropped replies for `auto_reply`.
- `callers` lists everyone who called the API or `/mcp` since startup, by token name (`api` for the
  owner secret): requests, successful and failed sends in the last 24 hours, when and from which
  IP they were last seen, and how many `/events` / `/messages/wait` connections they hold open.
- `webhook` is `null` without `-webhook`. `failed` counts deliveries given up on (after 6 attempts,
  or at once on a 4xx other than 429); `recent_failures` keeps the last 20 failed attempts, newest
  first, including ones that were retried later.
- `queue` counts [send queue](#post-queue) items by state (finished ones are kept 7 days) and says
  whether it's `paused`, whether a worker is `running` in this process, and `cooldown_until` /
  `last_error` while it backs off after WhatsApp errors. `delivered` and `read` count sent items
  with those receipts; `quiet_until` appears while [quiet hours](QUEUE.md#quiet-hours) hold it.
- `alerts` is `{"configured": false, "sent": 0, "failed": 0}` without
  [`-alert-gotify`](#alerts). Otherwise it shows how many alerts went out or failed, the title of the
  last one sent and `last_error` / `last_error_at` from the last failure (with the Gotify address
  replaced by `gotify`). The URL and the app token are never included.

## `GET /logs`

The server's recent app logs (the `log/slog` lines it writes to stderr or `-log-file`), newest
first. Requires `admin`; it's what the [admin panel](ADMIN.md)'s Logs tab shows. The newest 1000
records are kept in memory, at the server's log level (debug only with `-debug`), and lost on
restart. whatsmeow's own protocol logs go to stdout separately and aren't included.

| Parameter | |
|---|---|
| `level` | lowest level to return: `debug` (default), `info`, `warn` or `error` |
| `since` | only entries with a higher `id`: pass the newest `id` you have to tail |
| `before` | only entries with a lower `id`: pass the oldest you have to page back |
| `limit` | at most this many (default 200, max 1000) |

```sh
curl -H "Authorization: Bearer $WAME_TOKEN" "http://127.0.0.1:8080/logs?level=warn&limit=50"
```

```json
{
  "entries": [
    { "id": 812, "time": "2026-10-13T09:41:03.120-03:00", "level": "warn", "msg": "http",
      "attrs": { "method": "POST", "path": "/send", "status": "429", "duration_ms": "30001", "remote": "127.0.0.1:52144" } },
    { "id": 790, "time": "2026-10-13T09:40:11.002-03:00", "level": "warn", "msg": "delivery failed",
      "attrs": { "component": "webhook", "delivery": "4711-message-a1b2c3d4", "attempt": "1", "status": "500", "will_retry": "true" } }
  ]
}
```

`id`s grow by one per record (across all levels), so a gap means entries filtered out or dropped
from the buffer. Attribute values are strings; groups become dotted keys (`req.status`). Nothing
here is more than what the log output already has, minus secrets: message text is never logged,
attributes whose key contains `token`, `secret`, `password`, `authorization`, `cookie`, `key`,
`url` or `webhook` are replaced with `[redacted]`, and the owner secret, webhook URL and webhook
secret are replaced wherever they appear (an error message, for example). Values over 1 KiB are cut.
Successful `GET`s of the routes that get polled (`/status`, `/messages/wait`, `/events`, `/logs`,
and the admin panel's and Prometheus' `/stats`, `/metrics`, `/queue`, `/tokens`, `/settings`,
`/audit`) are logged at debug level, so polling doesn't fill the buffer (unless the server runs with
`-debug`).

## `GET /settings`, `PATCH /settings`

Some flags can be changed while wame runs, without a restart: `rate`, `chat_gap`, `max_wait`,
`auto_reply`, `webhook_filter`, `queue_paused`, `queue_jitter`, `queue_quiet_hours`, `queue_quiet`,
`queue_timezone`, `queue_media_ttl` and `queue_files_quota`. Requires `admin`.

```sh
curl -H "$H" localhost:8080/settings
curl -H "$H" -X PATCH localhost:8080/settings -d '{"rate": 10, "auto_reply": false}'
curl -H "$H" -X PATCH localhost:8080/settings -d '{"rate": null}'     # back to the -rate flag
```

```json
[
  { "key": "rate", "value": "10", "default": "20", "overridden": true, "updated_by": "ops-laptop", "updated_at": "2026-10-12T22:10:00-03:00" },
  { "key": "chat_gap", "value": "1s", "default": "1s", "overridden": false },
  { "key": "max_wait", "value": "30s", "default": "30s", "overridden": false },
  { "key": "auto_reply", "value": "false", "default": "true", "overridden": true, "updated_by": "ops-laptop", "updated_at": "2026-10-12T22:10:00-03:00" },
  { "key": "webhook_filter", "value": "", "default": "", "overridden": false },
  { "key": "queue_paused", "value": "false", "default": "false", "overridden": false },
  { "key": "queue_jitter", "value": "2s", "default": "2s", "overridden": false },
  { "key": "queue_quiet_hours", "value": "21:00-08:00", "default": "", "overridden": true, "updated_by": "ops-laptop", "updated_at": "2026-10-12T22:10:00-03:00" },
  { "key": "queue_quiet", "value": "true", "default": "true", "overridden": false },
  { "key": "queue_timezone", "value": "America/Sao_Paulo", "default": "Local (UTC)", "overridden": true, "updated_by": "ops-laptop", "updated_at": "2026-10-12T22:10:00-03:00" },
  { "key": "queue_media_ttl", "value": "24h0m0s", "default": "24h0m0s", "overridden": false },
  { "key": "queue_files_quota", "value": "2GB", "default": "2GB", "overridden": false }
]
```

- Values may be strings, numbers or booleans; `null` removes the override. `rate` is 0 (unlimited) to
  1000, and number lookups follow at 3×; `chat_gap`, `max_wait` and `queue_jitter` are durations up to `10m`;
  `webhook_filter` uses the [filter syntax](#filters) and only matters with `-webhook` (a `chat=`
  number is taken as typed; use the JID if WhatsApp knows it differently). `queue_paused` holds the
  [send queue](#post-queue); it has no flag. `queue_quiet_hours` is a daily window like
  `21:00-08:00` (it may cross midnight; `""` turns it off) when the queue sends nothing, read on the
  clock of `queue_timezone`, an IANA name like `America/Sao_Paulo` (`Local`, the default, is the
  server's zone). `queue_quiet` (`true`/`false`) switches the window off and back on without losing
  it. See [QUEUE.md](QUEUE.md#quiet-hours). `queue_media_ttl` is how long one upload to WhatsApp is
  reused for later messages with the same file: a duration up to `168h`, default `24h`, `off` to
  upload for every message. `queue_files_quota` caps the bytes of all [queue files](#post-queuefiles-get-queuefiles-delete-queuefilesid)
  together: `2GB` (the default), `500MB`, `64KB`, `1TB` or a byte count, powers of 1024; `0`
  refuses new uploads. Neither has a flag. See [QUEUE.md](QUEUE.md#files).
- Every value is checked before any is applied, so a bad one changes nothing (400).
- Changes apply at once and are stored in the `wame_settings` table, so they **win over the flags
  on the next start too**, until reset with `null`. `default` shows the flag's value.
- Each change is audited as `settings`, with the changes in `detail`.

## `POST /webhook/test`, `POST /webhook/replay`

`/webhook/test` posts a signed delivery with `X-Wame-Event: test` and the body
`{"event":"test","at":"…","note":"a test delivery from wame; there is no message"}` once, to check
the receiver and its signature check. It isn't counted in `/stats`.

`/webhook/replay` sends one of the `recent_failures` from [`/stats`](#get-stats) again, once, with
the same `X-Wame-Delivery` id (so a receiver that de-duplicates by it stays correct). The body is
rebuilt from the stored message as it is now (an edit since then shows). Ids that aren't among the
recent failures are refused, so this can't send messages the webhook filter left out.

```sh
curl -H "$H" -X POST localhost:8080/webhook/test
curl -H "$H" -X POST localhost:8080/webhook/replay -d '{"delivery": "4711-message-a1b2c3d4"}'
```

```json
{ "delivery": "4711-message-a1b2c3d4", "status": 200, "duration_ms": 84, "ok": true }
```

Both answer 200 with the attempt's outcome even when the receiver failed (`ok: false`, `status`,
`error`); 400 without a webhook, 404 for an unknown delivery. Both require `admin` and are audited
as `webhook_test` / `webhook_replay`.

## Alerts

With `-alert-gotify URL` and `-alert-gotify-token TOKEN` (`WAME_ALERT_GOTIFY`,
`WAME_ALERT_GOTIFY_TOKEN`), `wame serve` and `wame tui` push alerts to a
[Gotify](https://gotify.net) server: `POST {URL}/message` with the app token in the `X-Gotify-Key`
header (never in the URL) and the JSON body `{"title", "message", "priority"}`.

| Alert | When | Priority |
|---|---|---|
| WhatsApp disconnected | the linked account has been disconnected (or not logged in) for `-alert-delay` (default `2m`) | 7 |
| WhatsApp logged out | at once, when the device is unlinked | 8 |
| WhatsApp is back | the connection is up again after either of the two above | 2 |
| send queue backing off | the [send queue](#post-queue) entered a cooldown after repeated WhatsApp errors | 5 |
| queued messages failed | queue items failed for good (the count since the last alert) | 5 |
| webhook deliveries lost | webhook deliveries were given up on or dropped (the count since the last alert) | 5 |

- The last three are sent at most once per kind every 15 minutes; failures in between are added to
  the next one.
- Alerts carry the account's own number, never message text, contacts' numbers, tokens or the
  webhook URL.
- A failed alert is retried after a minute. `GET /stats` shows the outcome under `alerts`.
- A server that was never linked doesn't alert. Alerts are checked every 5 seconds and on every
  connection change, in the background.

### `POST /alerts/test`

Sends a low-priority test alert. Requires `admin`; audited as `alert_test`.

```sh
curl -H "$H" -X POST localhost:8080/alerts/test
```

```json
{ "ok": true }
```

400 when no alerts are configured, 502 when Gotify refused it or couldn't be reached (the error says
why, with the address replaced by `gotify`).

## `GET /metrics`

Prometheus metrics in the text format (version 0.0.4). Requires `admin`: give Prometheus an admin
token through its `authorization` setting (see [DOCKER.md](DOCKER.md#prometheus)).

```sh
curl -H "$H" localhost:8080/metrics
```

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `wame_up` | gauge | | always 1 |
| `wame_build_info` | gauge | `version`, `goversion` | always 1 |
| `wame_start_time_seconds`, `wame_uptime_seconds` | gauge | | when wame started, and since how long |
| `wame_connected`, `wame_logged_in` | gauge | | 1 while connected to WhatsApp / logged in |
| `wame_sends_total` | counter | `result` (`ok`, `failed`) | texts and files sent by any caller (API, MCP, TUI, queue, auto-replies) |
| `wame_limiter_last_minute` | gauge | `limiter` (`send`, `lookups`, `auto_reply`) | slots taken in the last minute |
| `wame_limiter_per_minute` | gauge | `limiter` | the configured limit (0 = unlimited) |
| `wame_limiter_rejected_total` | counter | `limiter` | 429s (dropped replies for `auto_reply`) |
| `wame_webhook_configured` | gauge | | 1 with `-webhook` |
| `wame_webhook_delivered_total`, `_retried_total`, `_failed_total`, `_dropped_total` | counter | | webhook deliveries, as in `/stats` |
| `wame_webhook_queue_depth` | gauge | | deliveries waiting |
| `wame_queue_items` | gauge | `state` | send queue items by state: `queued`, `sending`, `sent`, `failed`, `canceled`, `expired` (they add up to the total; finished ones are pruned after 7 days) |
| `wame_queue_sent_receipts` | gauge | `receipt` | sent queue items with a `delivered` or `read` receipt (a subset of `state="sent"`, kept apart so the states add up) |
| `wame_queue_paused`, `wame_queue_cooldown` | gauge | | 1 while paused / backing off |
| `wame_alerts_configured` | gauge | | 1 with `-alert-gotify` |
| `wame_alerts_sent_total`, `wame_alerts_failed_total` | counter | | alerts delivered / not delivered |
| `wame_open_streams` | gauge | | open `/events` and `/messages/wait` connections |

Counters start at zero when wame restarts. Per-token numbers are left out on purpose (they'd make
a series per token and show who uses the API); they're in `/stats` under `callers`.

## `GET /tokens`

Lists every token, oldest first, including revoked and expired ones. The secret and its hash are never
returned. Requires `admin`.

```json
[
  {
    "id": "k3x7q2ma",
    "name": "support-bot",
    "scopes": ["read", "send"],
    "chats": ["5511999999999@s.whatsapp.net"],
    "rate": 10,
    "created": "2026-10-11T09:12:00-03:00",
    "expires": "2026-11-10T09:12:00-03:00",
    "last_used": "2026-10-11T10:02:41-03:00"
  }
]
```

`chats`, `rate`, `expires`, `last_used`, `revoked` and `created_by` are omitted when empty. `last_used`
is updated at most once a minute.

## `POST /tokens`

Creates a token. Requires `admin`.

```sh
curl -H "Authorization: Bearer $T" -X POST localhost:8080/tokens \
  -d '{"name":"support-bot","scopes":["read","send"],"chats":["5511999999999"],"expires_in":"720h"}'
```

| Field | Required | |
|---|---|---|
| `name` | yes | unique, 1–40 of `a-z 0-9 . _ -`; reserved names are refused |
| `scopes` | yes | any of `read`, `send`, `admin` |
| `chats` | no | phone numbers or JIDs; empty means every chat. Not allowed with `admin` |
| `rate` | no | sends per minute for this token. Omitted → 10; `0` → global limit only |
| `expires_in` | no | Go duration, e.g. `720h`. Omitted → never expires |

Response `201`, the only time the plaintext token is shown:

```json
{
  "token": "wame_k3x7q2ma_…",
  "details": { "id": "k3x7q2ma", "name": "support-bot", "scopes": ["read", "send"], "…": "…" },
  "note": "store this token now; it can't be shown again"
}
```

When the caller is a **token** (not the owner secret or token-less mode):

- it can't create `admin` tokens (403); use `wame token create` or the owner secret for that;
- the new token gets `created_by: <caller's name>`, can't expire later than the caller, and is revoked
  when the caller is revoked.

Errors: 400 for an invalid or taken name, unknown scope, bad chat entry or `expires_in`.

## `DELETE /tokens/{name}`

Revokes the token and every token it created (recursively). Rows are kept, so names stay reserved and
the audit log stays readable. Requires `admin`.

```json
{ "revoked": 2 }
```

404 if no active token has that name.

---

## Rate limiting

Sending many messages quickly is the most common way automated WhatsApp numbers get banned.
wame spaces out everything it sends: texts, files, reactions, edits, deletes and auto-replies.

- `-rate N` (default 20, env `WAME_RATE`): at most N messages in any rolling minute. `0` turns
  this cap off.
- `-chat-gap D` (default `1s`): at least this long between two messages to the same chat.
  `0` turns it off.
- `-max-wait D` (default `30s`): a send waits in line for its slot. If the slot is further away
  than this, the API answers **429** with `Retry-After: <seconds>` instead of waiting. `0` means
  always wait.
- `-typing`: show "typing…" for a moment (scaled to the text length, 0.8–4 s) before each text.

For bulk sends, use the [send queue](#post-queue) instead of many `/send` calls: it takes only free
slots (so interactive sends go first), adds a random gap (`-queue-jitter`), and backs off on its
own when WhatsApp starts returning errors. It also keeps quiet during `-queue-quiet-hours`.

`-rate`, `-chat-gap`, `-max-wait` and `-autoreply` can also be changed while wame runs with
[`PATCH /settings`](#get-settings-patch-settings) (or the admin panel); a change applies to the
next send and is kept over restarts. [`GET /stats`](#get-stats) shows how much of each limit is in
use and how many 429s were answered.

The limiter is shared by every client in the process (API, TUI, CLI, MCP, auto-replies). API
tokens also have their own per-token rate, 10 per minute by default (see [Tokens and scopes](#rate-expiry-revocation)). `/read`
isn't limited.

**Number lookups** have a separate limit of 3 × `-rate` per minute (60 by default; unlimited
when `-rate 0`), with the same `-max-wait` and 429 behaviour. A lookup is: resolving a phone
number that isn't cached yet (any `to` / `chat` given as a number), each number in `/check`,
and each `/typing` call. Checking many numbers that never wrote to you is another pattern that
gets accounts flagged.

**Auto-replies** have their own small budget on top of that: at most 6 per minute and one per
10 s per chat. Replies over that budget are dropped, not queued, so a flood of `!ping` can't
fill the send queue. They run in the background and don't hold up incoming messages.

**Presence.** By default wame tells WhatsApp the account is "unavailable", so your phone keeps
showing notifications. `-online` marks it "available" while connected instead.

## Webhook

With `-webhook URL`, wame `POST`s each newly stored message to `URL` as a Message JSON body,
and each edit or delete of a stored message as an update.

What gets delivered:

- incoming messages, and messages you type on your phone (`from_me: true`)
- **not** messages sent through wame itself (API, TUI, CLI, auto-replies)
- **not** duplicates WhatsApp sends again, protocol messages, or history-sync imports
- edits and deletes, as `X-Wame-Event: update` with the updated Message
- [queued](#post-queue) items when they're sent or fail, and when a sent one is delivered or read,
  as `X-Wame-Event: queue` with the item (without its text). The filter doesn't apply to these

Headers on every request:

| Header | |
|---|---|
| `Content-Type` | `application/json` |
| `X-Wame-Event` | `message`, `update` or `queue`; `test` for a [test delivery](#post-webhooktest-post-webhookreplay), whose body has no message. Ignore event types you don't know |
| `X-Wame-Delivery` | unique ID for this delivery, the same on every retry. Use it to dedupe |
| `X-Wame-Timestamp` | Unix seconds when this attempt was sent |
| `X-Wame-Signature` | `sha256=<hex>`, only with `-webhook-secret` |

**Signature.** With `-webhook-secret S`, the signature is
`hex(HMAC-SHA256(S, "<X-Wame-Timestamp>.<raw body>"))`. Verify it with a constant-time
comparison and reject old timestamps. [BOT.md](BOT.md#verifying-webhook-signatures) has
Python and Node code.

**Delivery.** Any 2xx counts as delivered. Network errors, timeouts (10 s), 429 and 5xx are
retried with exponential backoff and jitter (about 1, 2, 4, 8, 16 s), 6 attempts in total; a
`Retry-After` on a 429 or 503 is honoured (up to 5 minutes).
Other statuses aren't retried. Redirects aren't followed: a 3xx counts as a failed delivery,
so point `-webhook` at the final URL. Failures are logged with the delivery ID, status and
error, but not the URL. Four workers send in parallel from an in-memory queue of 1000;
when the queue is full, new deliveries are dropped and logged. So delivery is **at-least-once
and unordered**. When the process stops, queued deliveries get one attempt within 3 seconds and
the rest are lost. If you can't afford
gaps, use `/messages/wait` or `/events` with a cursor instead, or as a backfill.

[`GET /stats`](#get-stats) counts deliveries and keeps the last 20 failed attempts (since the last
start); [`POST /webhook/replay`](#post-webhooktest-post-webhookreplay) sends one of those again,
with the same `X-Wame-Delivery`.

**Filter.** `-webhook-filter` takes the [filter](#filters) syntax as a query string:

```sh
./wame serve -webhook https://example.com/hook -webhook-secret "$SECRET" \
  -webhook-filter "type=text,image&from_me=false"
```

`since` is ignored there. A `chat` given as a phone number is looked up on WhatsApp once the bot
is logged in, the same way `/send` resolves recipients (so Brazil's optional 9th digit and hidden
`@lid` IDs are handled). Until then, or if the lookup fails, it's matched as typed and a warning
is logged. A JID (`…@s.whatsapp.net`, `…@g.us`) is used as is. The filter can also be changed at
runtime as the `webhook_filter` [setting](#get-settings-patch-settings); a number set that way is
used as typed, so prefer the JID there.
