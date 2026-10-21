# CLI reference

```
wame <command> [flags] [args]
```

Flags go right after the command and before any other arguments, e.g.
`wame serve -addr 127.0.0.1:9000 -debug` or `wame send -db other.db 5511999999999 hi`.
Boolean flags take `=`: `-autoreply=false`. Durations use Go syntax: `500ms`, `2s`, `1m`.

Pass secrets (`WAME_TOKEN`, `WAME_WEBHOOK_SECRET`, `WAME_ALERT_GOTIFY_TOKEN`) as environment variables rather than flags:
command-line arguments are visible to every user on the machine through `ps`.

## Commands

### `wame login [-phone NUMBER]`

Links the bot to your WhatsApp account as a "Linked device".

- Without `-phone`: prints a QR in the terminal. Scan it from **WhatsApp → Settings →
  Linked devices → Link a device**. Codes refresh about every 20 s.
- With `-phone +5511999999999`: prints an 8-character code. On the phone choose
  **Link with phone number instead** and type it in.

After `✅ logged in` it waits a few seconds for the first sync, then exits. Run it once;
the session lives in the database file from then on. If you're already logged in it says so
and exits.

### `wame serve [-addr 127.0.0.1:8080]`

Runs the [HTTP API](API.md) until Ctrl+C. If the session isn't logged in, it prints QR codes
in the terminal and serves them at `/qr`, so you can log in from a browser.

It listens on `127.0.0.1:8080` unless you pass `-addr`. It also serves the [MCP endpoint](MCP.md)
at `/mcp`.

It refuses to start when:

- the owner secret (`WAME_TOKEN` / `-token`) is shorter than 16 characters
  (generate one with `openssl rand -hex 24`);
- the address isn't loopback (`127.0.0.1`, `::1`, `localhost`) and there's neither an owner secret
  nor any token ever created.

Without any token it only accepts same-origin requests from and to localhost; see
[API.md → Auth](API.md#auth).

### `wame tui [-addr ADDR]`

Interactive terminal UI. See [TUI.md](TUI.md). With `-addr` it also runs the HTTP API in the
same process, under the same rules (a non-loopback address needs a token; the owner secret must be
at least 16 characters).

### `wame send NUMBER|JID TEXT...`

Sends one message and exits. Handy for scripts and cron:

```sh
./wame send 5511999999999 "Backup finished ✅"
./wame send 120363012345678901@g.us "Deploy done"
```

Needs an existing login. Auto-replies are disabled for this command. Prints
`sent <id> to <chat>` on success.

#### `wame send -at WHEN [-before WHEN] NUMBER|JID TEXT...`

Queues the message for later and exits. `wame send` opens the WhatsApp session itself and can't
wait for the time, so with `-at` it goes over the HTTP API to a running server (`POST /send` with
`send_at`), like `wame queue import`:
`-profile`, `-server`, `-token` and `-insecure`, or `WAME_SERVER` / `WAME_TOKEN` (the token needs
the `send` scope). The server's queue sends it, so quiet hours and opt-outs apply. A server older than scheduled sends would ignore `send_at` and send at once, so it checks first and refuses with "upgrade it first". Without `-at`
(and `-before`), `wame send` works as above.

```sh
wame send -at 09:00 5511999999999 "Good morning!"          # the next 09:00
wame send -at "tomorrow 09:00" -before 2h 5511999999999 "Good morning!"
wame send -at 2026-10-21T09:00 5511999999999 "See you"     # 09:00 on the recipient's clock
# queued #42 for Wed 21 Oct 09:00 (-03)
```

| Flag | |
|---|---|
| `-at WHEN` | when to send: a delay (`90m`, `2h`, `3d`), a time of day (`09:00`: the next one; `tomorrow 09:00`), RFC 3339 with offset (an instant), or a date and time without offset (the recipient's clock: their contact `time_zone`, else `queue_timezone`). An instant more than a minute in the past is refused; at most 30 days ahead |
| `-before WHEN` | don't send after this: a duration after `-at` (`2h`) or a time. Past it, the item ends `expired` instead of going out (at most 7 days after) |

A delay is turned into an instant here, and a time of day into a date and time without offset
(the day taken from this machine's clock), before it goes to the server. See
[QUEUE.md](QUEUE.md#when-it-goes-out).

### `wame token create NAME [-scopes read,send] [-chats N,...] [-rate N] [-expires 720h]`

Creates an API token for an agent, bot or script and prints it **once**:

```sh
./wame token create support-bot -scopes read,send -chats 5511999999999,120363012345678901@g.us -expires 720h
```

| Flag | Default | |
|---|---|---|
| `-scopes` | `read` | comma-separated: `read`, `send`, `admin` (admin implies the others) |
| `-chats` | all chats | comma-separated phone numbers or JIDs the token is limited to. Not allowed with `admin` |
| `-rate` | `10` | the token's own sends per minute, on top of the global `-rate`; `0` = global limit only |
| `-expires` | never | lifetime as a Go duration, e.g. `720h` |

Names are 1–40 of `a-z 0-9 . _ -`, unique forever (revoked names stay taken). `api`, `owner`,
`local`, `mcp`, `tui`, `cli`, `autoreply`, `whatsapp` and `relink` are reserved. Queued messages belong to
the actor name, so a token named `mcp` from before that name was reserved could see and cancel what
token-less `wame mcp` queued: revoke it and create one under another name. This is the only way to create an
`admin` token besides the owner secret: tokens can't create admin tokens over the API.

**From the first token on, the API requires a token on every request, from localhost too, forever**
(even after every token is revoked). Prefer one token per agent over sharing the owner secret. See
[API.md → Tokens and scopes](API.md#tokens-and-scopes).

The command only touches the database; it doesn't connect to WhatsApp, so it can run while `serve`
is running.

### `wame token list`

Prints every token: name, scopes, rate, status (active, revoked, expired), expiry, last use, the token
that created it (`-` for the CLI or owner; revoking that one revokes this one too) and chats.
Secrets are never shown.

### `wame token revoke NAME`

Revokes the token and every token it created over the API. Open streams using it close within
10 seconds. Fails if there's no active token with that name.

### `wame mcp [-allow-send] [-mcp-token TOKEN]`

Runs an [MCP](MCP.md) server on stdio for AI agents. Read-only by default; `-allow-send` adds
`send_message`, `react` and `mark_read`. `-mcp-token TOKEN` (or `WAME_MCP_TOKEN`) makes it act as
that API token, with its scopes and chat allowlist. It needs an existing login and opens its own
WhatsApp connection, so don't run it alongside `serve` or `tui` on the same database; connect the
agent to `http://127.0.0.1:8080/mcp` instead. stdout carries the protocol; logs go to stderr.

### `wame admin [-profile NAME] [-server URL] [-insecure]`

Opens a terminal [admin panel](ADMIN.md) for a server running elsewhere: status and remote login
(QR or pairing code), chats, tokens (create, rotate, revoke), the audit log and limiter / webhook
health, updated live. `-profile NAME` uses a server saved in `~/.config/wame/servers/NAME.env`. It only talks to the HTTP API, so
it needs no database and can run next to `serve`. `-server` (or `WAME_SERVER`, default
`http://127.0.0.1:8080`) is the server's URL; the token comes from `WAME_TOKEN` (or `-token`) and
must be the owner secret or have the admin scope. Both can also be saved in `./.env` or
`~/.config/wame/admin.env` ([details](ADMIN.md#saving-the-server-and-token)). Plain `http://` is refused for non-loopback
addresses unless you pass `-insecure`.

### `wame queue import [-batch NAME] [-text DEFAULT] [-send-at TIME] [-send-before WHEN] [-spread DURATION] [-key KEY] [-template] [-dedupe] [-skip-sent DURATION|batch:NAME] [-file PATH|ID] [-dry-run] FILE.csv`

Queues the messages of a CSV file on a running server ([format](QUEUE.md#csv-import): a header
line with `to` and optionally `text`, `send_at`, `reply_to`, `name`, `send_before`; one message per line; `to`
may be `tag:NAME` for everyone with that contact tag). It checks every
row's format locally first (columns, numbers, times, text length), then sends them in requests of
1000 under one batch, printing each request's result and a summary. The token's allowlist and its
limit of waiting messages are checked by the server per request, after the earlier requests were
queued, so an import can still stop halfway:

```sh
wame queue import -batch promo-october -text "Hi! Our store opens at 9." promo.csv
wame queue import -dry-run promo.csv          # check and show the requests, send nothing
```

| Flag | |
|---|---|
| `-batch NAME` | the batch for every row (letters, digits, `.`, `_`, `-`); default `csv-` plus a hash of the key (so reruns share it) |
| `-text DEFAULT` | text for rows without one |
| `-send-at TIME` | time for rows without one: RFC 3339 like `2026-10-13T09:00:00-03:00`, or a date and time without offset like `2026-10-13T09:00` (the recipient's clock) |
| `-send-before WHEN` | `send_before` for rows without one: a duration after each row's time (`2h`) or a time. Past it, the item ends `expired` |
| `-spread DURATION` | space the rows evenly over this window (at most `24h`) from their `send_at`, over the **whole file**; each row is sent with its own `send_at`, so a rerun gives the same times. Needs a `send_at` on every row (column or `-send-at`); refuses `tag:` rows ([details](QUEUE.md#csv-import)) |
| `-key KEY` | base of the idempotency keys (`KEY.1`, `KEY.2`, … per request); default `csv-` plus a hash of the file's full path |
| `-template` | fill `{name}`, `{first_name}` and `{push_name}` per recipient ([templates](QUEUE.md#name-templates)); a `name` column wins. Needs a token with `read` |
| `-dedupe` | leave out rows whose recipient, text and `name` appeared earlier in the file (and let the server skip repeats per request, like a person in two tags) |
| `-skip-sent DURATION` | skip people who already got a message within `48h`, `3d`… (at most 90 days; 7 for a token without `read`), or in `batch:NAME` ([details](QUEUE.md#skipping-repeats-dedupe-and-skip_sent)) |
| `-file PATH\|ID` | send one image or document to every row, the row's text being its caption (which may be empty). A path is checked locally (a regular, non-empty file of at most 64 MB), uploaded once through `POST /queue/files` before the first request and its id passed on every request; it prints `uploaded menu.pdf (document, 482.0 KiB)`, or `already uploaded` when the server has the same bytes from your token, so a rerun is safe. An id (`f_` and 32 hex digits) skips the upload. The default key and batch also depend on the file, so the same CSV with another file is a new import ([details](QUEUE.md#files)) |
| `-dry-run` | parse and check the file locally and print how it would be sent (tags show as `tag:NAME (expanded by the server)`); a `-file` path is checked but not uploaded |

The summary counts what was skipped: `queued 2328 messages, skipped 12 (8 sent in the last 48h, 4
duplicates), batch promo-october`.

After a failure, fix what the error names (in the failed request's lines or later ones, never the
lines before it) and run the command the error prints: requests that went through before are
recognized by their key and not queued twice. Rows that were already queued can't be changed by
a rerun (409), and a new import from the same path needs a new `-key`; see
[running an import again](QUEUE.md#running-an-import-again). Like
`wame admin`, it only talks to the HTTP API (the server may be in Docker or elsewhere) and takes
`-profile`, `-server`, `-token` and `-insecure`, or `WAME_SERVER` / `WAME_TOKEN` from the
environment, `./.env` or `~/.config/wame/admin.env`; the token needs the `send` scope. Files up to
64 MB.

### `wame queue status [-batch NAME]`

Prints the queue's counts (queued, sending, sent, delivered, read, failed, canceled, expired), for one batch
with `-batch`. A token without `admin` sees only what it queued. Same server flags as `queue import`.

### `wame backup [-keep N] [PATH]`

Copies the database into a new file, safely, even while `serve` or `tui` has it open: it opens the
database read-only (by the same absolute path the server uses) and runs SQLite's `VACUUM INTO`, which
writes a consistent snapshot of everything committed so far. It never upgrades the database or
connects to WhatsApp, so it's fine to run next to a running server, including with
`docker exec` ([Docker backups](DOCKER.md#backups)).

```sh
wame backup                       # ~/.config/wame/wame-20261013-093005.db, next to the database
wame backup ~/backups/            # a new wame-YYYYMMDD-HHMMSS.db in that folder (created 0700 if missing)
wame backup ~/backups/before-upgrade.db
wame backup -keep 7 ~/backups/    # then delete all but the 7 newest wame-*.db backups there
```

`-db` (or `WAME_DB`) picks the database, as for the other commands. PATH ends in `/` or is an
existing folder: the copy goes inside it with the timestamped name. The copy is created with mode
`0600`, and an existing file is never overwritten. Afterwards wame checks it (`PRAGMA quick_check`
and that `wame_messages` is there), deletes it if the check fails, and prints its path and size:

```
backed up /home/you/.config/wame/wame.db to /home/you/backups/wame-20261013-093005.db (12.4 MiB)
```

`-keep N` only touches files in the copy's folder named exactly like `wame-YYYYMMDD-HHMMSS.db`;
anything else there (including copies you named yourself) is left alone. It's off (`0`) by default.

The copy holds your session keys, like the database itself: keep it private and encrypt it before it
leaves the machine. To restore, stop wame and put the copy in place of `wame.db` (remove the old
`wame.db-wal` and `wame.db-shm` files too).

### `wame logout`

Unlinks the device from your phone and deletes the session. If that worked, it also deletes your
own contacts (names, tags, notes, language, time zone), the WhatsApp profile cache, and group
names, admins and members. Messages and the audit log are kept; delete the database file to remove
everything. Opt-outs are kept, so a logout can't let anyone who opted out be messaged again. Run `wame backup` first if you want to keep your notes.

### `wame version`

Prints `wame <version>`. Release builds carry the tag (e.g. `wame 0.3.0`); anything built
locally prints `wame dev`. `--version` works too.

### `wame help`

Prints usage (also `-h` / `--help`).

## Flags

| Flag | Env | Default | |
|---|---|---|---|
| `-db PATH` | `WAME_DB` | `~/.config/wame/wame.db` | session and message database (SQLite). See [The database](#the-database) |
| `-addr ADDR` | `WAME_ADDR` | `127.0.0.1:8080` for `serve`, off for `tui` | HTTP listen address. A non-loopback address requires a token |
| `-token TOKEN` | `WAME_TOKEN` | none | owner secret: full access with `Authorization: Bearer TOKEN`. At least 16 characters. Prefer per-agent tokens from `wame token create` |
| `-webhook URL` | `WAME_WEBHOOK` | none | POST incoming messages as JSON to this http(s) URL |
| `-webhook-secret S` | `WAME_WEBHOOK_SECRET` | none | sign webhook bodies with HMAC-SHA256 ([how to verify](BOT.md#verifying-webhook-signatures)) |
| `-webhook-filter Q` | `WAME_WEBHOOK_FILTER` | none | only deliver matching messages, e.g. `type=text&from_me=false` ([syntax](API.md#filters)). A `chat=<number>` is resolved on WhatsApp after login |
| `-rate N` | `WAME_RATE` | `20` | max messages sent per minute; `0` = unlimited. Number lookups get 3 × this. `WAME_RATE` must be a non-negative integer or wame exits with code 2 |
| `-chat-gap D` | | `1s` | min time between two messages to the same chat |
| `-max-wait D` | | `30s` | queue a send at most this long before answering 429; `0` = wait as long as needed |
| `-typing` | | `false` | show "typing…" before each text message |
| `-online` | | `false` | appear "online" while connected. Off by default so your phone keeps showing notifications; typing indicators still switch to "available" briefly |
| `-autoreply` | | `true` | answer `!ping`, `!time`, `!help` |
| `-queue-jitter D` | `WAME_QUEUE_JITTER` | `2s` | wait a random 0–D more between [queued](QUEUE.md) sends; `0` = none |
| `-queue-quiet-hours W` | `WAME_QUEUE_QUIET_HOURS` | off | send nothing from the queue in this daily window, e.g. `21:00-08:00` (may cross midnight). See [quiet hours](QUEUE.md#quiet-hours) |
| `-queue-timezone TZ` | `WAME_QUEUE_TIMEZONE` | `Local` | IANA time zone the quiet hours are read in, e.g. `America/Sao_Paulo`; `Local` is the server's zone (UTC in Docker unless `TZ` is set) |
| `-alert-gotify URL` | `WAME_ALERT_GOTIFY` | none | `serve` and `tui`: push [alerts](API.md#alerts) (disconnected, logged out, queue and webhook trouble, recovered) to this Gotify server, e.g. `http://10.0.0.5:8070`. No query string: the token goes in the next flag |
| `-alert-gotify-token T` | `WAME_ALERT_GOTIFY_TOKEN` | none | the Gotify app token, sent as `X-Gotify-Key`; required with `-alert-gotify`, never logged or shown |
| `-alert-delay D` | `WAME_ALERT_DELAY` | `2m` | alert when WhatsApp stays disconnected this long (at least `30s`: shorter values, `0` included, count as `30s`); a logout alerts at once |
| `-log-format F` | `WAME_LOG_FORMAT` | `text` | app log format: `text` or `json` |
| `-log-file PATH` | `WAME_LOG_FILE` | stderr | write app logs to PATH (appended, mode 0600) |
| `-debug` | | `false` | debug-level logs, including whatsmeow's |
| `-phone NUMBER` | | | `login` only: use a pairing code instead of a QR |

All flags are accepted by every command, so `-rate`, `-chat-gap` and `-typing` also apply to
`wame send`. A negative `-chat-gap`, `-max-wait`, `-queue-jitter` or `-alert-delay` is refused.

`-rate`, `-chat-gap`, `-max-wait`, `-autoreply`, `-webhook-filter`, `-queue-jitter`,
`-queue-quiet-hours` and `-queue-timezone` can also be changed while wame runs, with [`PATCH /settings`](API.md#get-settings-patch-settings) or the admin panel's
Settings tab. Two more runtime settings have no flag: `queue_media_ttl` (how long one upload to WhatsApp
is reused for the later messages with the same [queue file](QUEUE.md#files): `24h` by default, up to
`168h`, `off` to upload for every message) and `queue_files_quota` (the bytes all queue files may take
together: `2GB` by default, also `500MB`, `64KB`, `1TB` or a byte count; `0` refuses new uploads).
Those changes are stored in the database and **win over the flags**, on later starts
too, until they're reset there. See [API.md → Rate limiting](API.md#rate-limiting) for how the limiter behaves
and why it's on by default.

## Logs

There are two log streams:

- **App logs** (`log/slog`): HTTP requests, actions, webhook deliveries, connection state.
  Text or JSON (`-log-format`), to stderr or `-log-file`. Level is info, or debug with `-debug`.
- **whatsmeow logs**: protocol-level messages printed to stdout, info level, debug with `-debug`.

In `tui` and `send` modes whatsmeow logs are always off, and app logs are discarded unless you
set `-log-file`, so they don't break the screen or a script's output.

Each HTTP request produces one line with `method`, `path`, `status`, `duration_ms` and
`remote`. The query string is left out on purpose because it may contain the token.
`GET` requests to the polled routes (`/status`, `/messages/wait`, `/events`, `/logs`, `/stats`,
`/metrics`, `/queue`, `/tokens`, `/settings`, `/audit`, `/optouts`) log at debug level unless they fail; writes
to them, like `POST /queue` or `PATCH /settings`, stay at info.

```
time=2026-10-10T22:41:03.120-03:00 level=INFO msg=http method=POST path=/send status=200 duration_ms=812 remote=127.0.0.1:52144
time=2026-10-10T22:41:03.119-03:00 level=INFO msg=action actor=api action=send target=5511999999999@s.whatsapp.net detail="text, 6 chars"
```

The newest 1000 app log records are also kept in memory for admins: `GET /logs`
([API](API.md#get-logs)) and the [admin panel](ADMIN.md)'s Logs tab. That copy is taken before
`-log-file`, `-log-format` or the quiet modes apply, so it's there even when the output goes nowhere,
and it follows the same level (debug only with `-debug`). whatsmeow's logs aren't in it. It's lost on
restart.

Message text, the token and the webhook URL are never logged. `*.log` files are in `.gitignore`. More on the log fields and the audit table in
[proposed-features/ADMIN-PANEL.md](proposed-features/ADMIN-PANEL.md#logging-today).

## The database

One SQLite file (`wame.db` plus its `-wal`/`-shm` companions) holds everything. By default it
lives in `~/.config/wame/` on both macOS and Linux, or in `$XDG_CONFIG_HOME/wame/` when that
variable is set to an absolute path (on Windows: `%AppData%\wame\`). `-db` and `WAME_DB` override
it. The directory is created with mode `0700` if it doesn't exist.

Older versions kept `wame.db` in the current directory. If the default file doesn't exist yet but
a `./wame.db` does, wame prints a one-line notice to stderr and starts a fresh session at the
default path. It never moves anything by itself; to keep your session, either move it:

```sh
mkdir -p ~/.config/wame && mv wame.db* ~/.config/wame/
```

or keep using it where it is with `-db wame.db` (or `WAME_DB=wame.db`).

The file holds:

- whatsmeow's tables (`whatsmeow_*`): your encryption keys, session, contacts, and
  phone-number ↔ hidden-ID links. **Anyone with this file can act as your WhatsApp
  account.** Keep it private and out of git (the included `.gitignore` already excludes it).
wame's own tables:

| Table | Holds |
|---|---|
| `wame_messages` | every message the bot has seen or sent, which powers `/messages`, `/events`, `/chats` and the TUI (columns below) |
| `wame_messages_fts` | full-text index of `wame_messages.text` (SQLite FTS5, accents folded) for `q=` and `/search`. Kept in sync by triggers on insert, update of `text` and delete |
| `wame_chat_state` | per chat, `read_seq`: the read position behind `unread` in `/chats` and the TUI badges |
| `wame_groups` | cached group names and admin lists (`jid`, `name`, `admins` as JSON, `updated`), so names show before connecting; deleted on logout |
| `wame_group_members` | who is in each group: `group_jid`, `member`, `role`, `first_seen`, `last_seen`; deleted on logout |
| `wame_contacts` | your own names, tags, notes, language and time zone per person; deleted on logout |
| `wame_contact_cache` | what WhatsApp last said about a person (about, picture id, business info, LID), filled by an admin refresh; deleted on logout |
| `wame_outbox` | the [send queue](QUEUE.md) |
| `wame_outbox_files` | metadata of the [files](QUEUE.md#files) uploaded for the queue (the bytes are in `wame-queue-files/` next to the database); deleted on logout |
| `wame_optouts` | chats that asked not to get queued messages; kept on logout |
| `wame_audit` | one row per control action (send, react, edit, delete, read, pair, logout…), served by `GET /audit` |
| `wame_tokens` | API tokens: id, name, SHA-256 hash of the token (never the token itself), scopes, chats, rate, created/expires/last-used times, revoked flag, `created_by`. Rows are never deleted |
| `wame_settings` | runtime overrides from `PATCH /settings` or the admin panel (`key`, `value`, `updated_by`, `updated_at`). They win over the flags on every start; deleting a row (or resetting it there) brings the flag back |

`wame_messages` columns:

| Column | |
|---|---|
| `seq` | `INTEGER PRIMARY KEY AUTOINCREMENT`: the cursor used by `since` and `/media/{seq}`. Never reused, even after deletes |
| `chat`, `id` | `UNIQUE`; a message WhatsApp sends twice is stored once |
| `sender`, `push_name`, `from_me`, `is_group`, `type`, `text`, `ts` | the basics; `ts` is Unix milliseconds |
| `reply_to` | quoted message id, or the target of a reaction |
| `mimetype`, `file_name`, `file_size` | media metadata (empty / 0 when there's no media) |
| `edited`, `deleted` | `1` after an edit or delete |
| `raw` | the original message protobuf. Needed to quote a message in a reply and to download its media. Cleared when the message is deleted |
| `raw_chat`, `raw_sender` | the JIDs exactly as WhatsApp sent them (possibly `…@lid`), needed for replies, reactions, deletes and read receipts |

Databases from older versions are upgraded automatically on startup, with no command to run:
`wame_messages` is rebuilt with the new columns and the explicit `seq` column (keeping every
message's existing number), missing tables are created, and the search index is built from the
stored messages. When `wame_chat_state` is first created, every chat's read position is set to
its newest message, so nothing already stored shows as unread.

When WhatsApp reveals which phone number a hidden `…@lid` ID belongs to, messages stored under
the `@lid` chat are moved to the phone-number chat, and leftover duplicates are removed.

At startup wame sets the database file and its `-wal`/`-shm` files to mode `0600` (owner only).

`wame_audit` columns: `id`, `at` (Unix ms), `actor`, `action`, `target`, `detail`, `ok`, `error`.

You can inspect the database with any SQLite tool, e.g.
`sqlite3 ~/.config/wame/wame.db "select seq, chat, type, ts from wame_messages order by seq desc limit 10"`.

Only one process can use a database at a time, so don't run `serve`, `tui` and `send` on
the same file at once. To copy it while it's in use, use [`wame backup`](#wame-backup--keep-n-path)
rather than `cp`, which can catch the file halfway through a write.

## Exit codes

`0` on success, `1` on any error (printed to stderr), `2` when called without a command, with an
unknown flag, or with an invalid `WAME_RATE`.
