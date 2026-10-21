# Send queue

`POST /send` holds the HTTP request open until the message goes out, which is fine for one reply
but not for a bulk send: a thousand messages at 20 per minute take 50 minutes, and a restart in the
middle loses whatever was still waiting. The **send queue** (the outbox) is for that case.
`POST /queue` stores the messages in the database and answers at once; a worker in the server
sends them, one at a time, within the same rate limits as everything else.

```sh
curl -H "Authorization: Bearer $T" -X POST localhost:8080/queue -d '{
  "batch": "promo-october",
  "text": "Hi! Our store opens at 9 tomorrow.",
  "messages": [
    {"to": "5511999999999"},
    {"to": "5511988887777", "text": "Hi John! Your order is ready."},
    {"to": "5521987654321", "send_at": "2026-10-13T09:00:00-03:00"}
  ]
}'
```

```json
{ "batch": "promo-october", "queued": 3, "replayed": 0, "items": [ { "id": 41, "status": "queued", "to": "5511999999999", … }, … ] }
```

## What it guarantees

- **Survives restarts.** Items live in the `wame_outbox` table. One that was being sent when the
  process stopped goes back in the queue on the next start.
- **Same message id on every attempt.** Each item gets its WhatsApp message id before the first
  attempt and keeps it on retries. WhatsApp apps de-duplicate by message id, so if a send did
  reach WhatsApp just before a crash, the resend after the restart isn't shown as a second message.
- **Paced like everything else.** The worker uses the global limiter (`rate`, `chat_gap`) and the
  token's own rate. It only takes a slot that is free right now, so interactive sends
  (`/send`, the TUI, auto-replies) are never pushed back behind a long batch: they go first.
- **Less robotic.** Between queued sends the worker waits an extra random 0 to `queue_jitter`
  (default `2s`; `-queue-jitter`, `WAME_QUEUE_JITTER`, or the setting).
- **Quiet at night, if you want.** With [quiet hours](#quiet-hours) set, nothing queued goes out
  in that window; it waits for the morning.
- **Backs off when WhatsApp pushes back.** One failed send is retried later on its own (30 s, 1 min,
  2 min… up to 15 min, 5 attempts in all). Two or more failures in a row pause the whole queue for
  30 s, then 1 min, 2 min… up to 10 min, until a send works again. Sends wait while the server
  is disconnected or logged out, without using up attempts.
- **Token rules still apply.** A token limited to some chats can only queue to those. Items from a
  token that is revoked or expires before they go out fail with that reason instead of being sent.

It sends text and, since [files](#files), images and documents; reactions and edits go through the direct routes. It takes at most 1000 messages
per request (repeat the request with the same `batch` for more), and at most 10,000 waiting per
token, so a misbehaving client can't fill the disk.

## When it goes out

`send_at` (top level, or on a message) says when. Without it the item goes as soon as the limits
allow. The API (JSON and CSV) takes two forms:

| Form | Example | Means |
|---|---|---|
| RFC 3339 with offset | `2026-10-21T09:00:00-03:00` | that instant |
| Date and time without offset | `2026-10-21T09:00` (seconds optional; a space instead of `T` works too) | 09:00 on that day **on the recipient's clock** |

The MCP tools, `wame send -at`, the TUI and the admin panel also take a delay (`90m`, `2h`, `3d`) and
a time of day (`09:00`: the next one; `tomorrow 09:00`), on the recipient's clock. The API doesn't:
a retry with the same idempotency key must be the same request, and "in 2 hours" isn't.

**Which calendar a time of day uses.** In the MCP tools and the TUI, `09:00` and `tomorrow 09:00`
are resolved on the **recipient's own calendar**: `09:00` is the next 09:00 in their zone, and
"tomorrow" is their tomorrow, so a message to someone already in the next day isn't a day late. In
the admin panel and `wame send -at`, which reach the server through the HTTP API (fixed times
only), the day is picked on the admin's or the CLI machine's own calendar and then read on the
recipient's clock.

- **Past times.** The API still takes an absolute `send_at` in the past; the item goes out as soon
  as the limits allow. The typed forms (a delay, a time of day, an RFC 3339 time given to the MCP
  tools, `wame send -at` or the TUI) refuse an instant more than a minute in the past.
- **At most 30 days ahead**, checked after the time is resolved for the recipient.
- A wall-clock time that doesn't exist (a daylight-saving gap) moves forward to the next valid
  minute; one that happens twice takes the first.

**The recipient's clock.** A date and time without offset is read in the zone of the person it goes
to: their contact's `time_zone` (see [contacts](API.md#contacts)), else `queue_timezone`. A group,
and a person with no zone, use `queue_timezone`. Each member of a `tag:` row is resolved
separately, so a tag that spans zones reaches everyone at their own 09:00. `due` is stored as an
instant: changing a contact's zone later doesn't move items already queued. A retry compares a
time on the recipient's clock by its wall-clock time (`2026-10-21T09:00`), not the instant it
resolved to, so changing the contact's zone between a request and its retry doesn't make the retry
a 409.

### `send_before`: don't send too late

`send_before` is when a message stops being worth sending: a duration after the item's own time
(`2h`, `+2h`, `1d`) or a time (either form above). If the item hasn't gone out by then (downtime,
quiet hours, a back-off, the rate limit) it isn't sent and ends as **`expired`**, so a "good
morning" isn't delivered at 3 pm.

- It must be after the item's time and at most 7 days after it. A top-level `send_before` is the
  default for messages that leave theirs out.
- A *new* item whose fixed `send_before` has already passed is refused with 400 `send_before has
  already passed`. A retry of a request whose `send_before` has passed since replays the stored
  item instead.
- The worker checks at the start of every step, also while the queue is paused, in quiet hours or
  offline, so an item shows as `expired` within about a minute of its deadline. An item that is
  already `sending` finishes normally; a retry after a failed attempt still honors the deadline.
- `expired` is a final state of its own, not a failure: it doesn't count toward the "queued
  messages failed" alert. It sends a `queue` event and a webhook `queue` delivery, and shows in
  `counts.expired`, `GET /queue?status=expired`, `/metrics` (`wame_queue_items{state="expired"}`)
  and `wame queue status`.
- It isn't part of a request's identity: a retry that differs only in `send_before` replays.

### `spread`: space a batch out

`spread` (a duration of at most `24h`, like `30m`) on `POST /queue` (a JSON field, or `?spread=`
for CSV) and on the MCP `schedule_message` sets the rows' times evenly across the window: row `i`
of `n` (counting `tag:` members after they're expanded) is due at its own `send_at` (or now) plus
`i × spread / (n − 1)`. The limiter and jitter still apply on top: `spread` only sets the earliest
time. `send_before` counts from each row's spread time.

A retry compares the requested `send_at` and `spread`, so it replays. `wame queue import -spread`
works differently: see [CSV import](#csv-import).

### Changing a queued message

`PATCH /queue/{id}` changes an item that is still `queued`; once it is sending or finished, it's a
409.

```sh
curl -H "Authorization: Bearer $T" -X PATCH localhost:8080/queue/41 \
  -d '{"send_at": "2026-10-22T09:00", "send_before": "2h", "text": "See you at 10 instead."}'
```

- **Body.** Any of `send_at` (the two API forms), `send_before` (`null` or `""` removes it) and
  `text`. Nothing to change is a 400, and so is a new time that isn't before the item's existing
  `send_before` (change or clear it in the same request). A past `send_at` sends as soon as
  possible.
- **Who.** Your own items; with `admin`, anyone's. It answers 200 with the item.
- **Not sent early.** The worker only claims an item whose time has come, so a message moved to a
  later time while the worker was about to send it waits for the new time.
- **Replays still work.** The `send_at` as first requested stays stored, and an edited text keeps a
  hash of the original one, so running the same import again replays the item instead of
  answering 409. The item gets `edited_at`.
- **Audit.** `queue_edit`, listing the field names changed, never the text.

## CSV import

A spreadsheet export works too. `POST /queue` takes the same messages as a CSV file when sent with
`Content-Type: text/csv` (or `application/csv`), and `wame queue import` sends a file of any length from the command line.

```csv
to,text,send_at,reply_to
5511999999999,"Hi Maria, your order is ready.",,
5511988887777,"Hi John!
See you at 9.",2026-10-13T09:00:00-03:00,
552187654321,,,
```

- **The first line names the columns**, in any order and any case: `to` (required), `text`,
  `send_at` (the [two API forms](#when-it-goes-out)), `reply_to`, `name` (for [templates](#name-templates)) and `send_before`. Any other column
  is refused with its name, so a typo like `txet` can't silently drop the text. Leave out the
  columns you don't need.
- **One message per line.** Fields with commas, quotes or line breaks go in double quotes (`""` for
  a quote inside), as every spreadsheet writes them. A UTF-8 BOM, Windows line ends, spaces around
  fields, blank lines, rows shorter than the header and empty columns without a name (the trailing
  `,,` some spreadsheets add) are all fine. A value outside the named columns is refused: it's
  usually text with a comma that wasn't quoted.
- **Commas or semicolons.** Spreadsheets set to a language with a decimal comma (Portuguese, for
  one) export with `;` between fields. When the header line has `;` and no `,`, the file is read
  with `;`.
- A row without `text` (like the last row above) gets the default text; without `send_at`, the
  default time or as soon as the limits allow.
- **Errors name the line** of the file, like `line 7: invalid recipient "12"`, and nothing in the
  request is queued.
- A number a spreadsheet turned into scientific notation (`5.511999E+12`) has lost digits, and one
  written with decimals (`5511999999999.0`, as pandas and some exports do) isn't the number you
  meant: both are refused. Format the `to` column as text before exporting.

```sh
curl -H "Authorization: Bearer $T" -H "Content-Type: text/csv" -H "Idempotency-Key: promo-oct-1" \
  --data-binary @promo.csv "localhost:8080/queue?batch=promo-october"
```

The query takes `batch`, `send_at`, `send_before`, `text` and `file` (an uploaded [file](#files); the defaults, like the JSON body's top-level fields), `spread`
and `template`, `dedupe` and `skip_sent` (see [below](#tags-templates-and-skips));
the limits are the JSON form's: 1000 rows and 16 MB per request, the token's allowlist checked on
every row, and `Idempotency-Key` to make a retry safe. Use `--data-binary`, not `-d`, which drops
the line breaks. Put the message text in the file's `text` column rather than in `?text=`: query
strings end up in proxy logs, shell history and browser history. `?text=` still works, for short
defaults that aren't private.

**From the command line**, `wame queue import` reads the file, checks every row locally (columns,
numbers, times, text length) and sends it to the server in requests of 1000 rows, all under one
batch. What only the server knows (the token's allowlist, its limit of 10,000 waiting messages) is
checked per request, after the earlier requests were queued, so an import can stop halfway; see
[running it again](#running-an-import-again).

```sh
wame queue import -batch promo-october -text "Hi! Our store opens at 9." promo.csv
# 1/3: lines 2-1001: queued 1000
# 2/3: lines 1002-2001: queued 1000
# 3/3: lines 2002-2341: queued 340
# queued 2340 messages, batch promo-october (follow it with: wame queue status -batch promo-october)
wame queue status -batch promo-october
# batch promo-october: queued 2210, sending 0, sent 130 (delivered 120, read 41), failed 0, canceled 0, expired 0
```

It talks to the server over the API, like [`wame admin`](ADMIN.md) (same `-profile`, `-server`,
`-token`, `WAME_SERVER` / `WAME_TOKEN`, `./.env` and `admin.env`), so it works with a server in
Docker or on another machine; the token needs the `send` scope. `-dry-run` checks the file and
shows how it would be split, without sending anything.

Two more flags set times for the whole file. `-send-before WHEN` is the default `send_before` for
rows that have none. `-spread DURATION` (at most `24h`) spaces the rows evenly from their
`send_at`: the CLI works it out over the **whole file** (the server only sees chunks of 1000) and
sends each row with its own `send_at`, so a rerun gives the same times. It needs a `send_at` on every
row (from the column or `-send-at`) and refuses `tag:` rows; queue those with `POST /queue` and `spread`.

To send an image or a document to everyone in the file, add `-file menu.pdf` (see [Files](#files)):
the CLI checks it locally (a regular file, at most 64 MB), uploads it once before the first request
and passes its id on each one. The `text` column (or `-text`) becomes the caption and may be empty.
`-file` also takes an id you already have (`f_` and 32 hex digits), which skips the upload.

```sh
wame queue import -batch menu-october -file menu.pdf -text "Our new menu" promo.csv
# uploaded menu.pdf (document, 482.0 KiB)
# 1/1: lines 2-3: queued 2
```

### Running an import again

Each request carries an idempotency key: `-key` plus the request's number (`promo-oct.1`,
`promo-oct.2`, …) or, without `-key`, one taken from the file's full path (`csv-…`). Without
`-batch`, the batch name comes from that key too, so a rerun lands in the same batch. A request
whose key was used before returns what it queued instead of queuing it again; the CLI prints the
key in its error when an import stops.

- **It stopped halfway** (the network dropped, a row the token may not send to, the 10,000 waiting
  limit): fix what the error names, in that request's lines or later ones, and run the same
  command again (the error message spells it out, `-key` included). Requests whose rows didn't
  change are recognized and not queued twice; the rest are sent. Don't edit, add or remove lines
  before the request that failed: those were queued already.
- **Rows that were already queued can't be changed** by rerunning: a request whose rows (text,
  number, `reply_to`, `send_at`, batch) differ from what was queued under its key is refused with
  409, and nothing of it is queued. Cancel the batch and import again under a new `-key`
  instead. Rows only added at the end of the last request go through, so a file that grew can be
  sent again.
- **A new import from the same path** (next month's `promo.csv`) needs a new `-key`, or it's
  refused with 409; the same file sent again unchanged queues nothing new. Moving the
  file changes the derived key, so pass the `-key` from the error when resuming a moved file.
- `-text`, `-send-at` and `-spread` are filled in before sending, so changing them is a change of the rows.
  `-send-before` isn't part of a row's identity, so changing it replays.
  So is adding or dropping `-template` or `-dedupe` (`-dedupe` leaves rows out, which moves the
  rest between requests); `-skip-sent` isn't: it's checked again on every run.
- **`-file`** is part of the derived key (the file's id, or for a path the hash of its bytes), so the
  same CSV with another file is a new import. Running it again with the same file uploads nothing
  twice: the server answers 200 with the same id. An explicit `-key` with another file is a 409, as
  for any change of the rows. Rerun with `-file` given the same way: a path and the id it was
  uploaded as derive different keys, so switching between them starts a new import (pass the
  first run's `-key` to resume it). If the uploaded file was deleted in between (the admin
  panel's files view, or cleanup an hour after its last message), a rerun uploads it again under a
  new id and the requests already queued answer 409: they were queued, so don't rerun them under
  another key.
- The server remembers the keys as long as it keeps the items, until 7 days after they finish.

## Tags, templates and skips

Three request options that build on [your contacts](API.md#contacts). All of them work with JSON,
CSV and `wame queue import`; tags and templates also with the MCP tool `schedule_message`.

### `to=tag:NAME`

A `to` of `tag:clients` queues one item per person you tagged `clients`, at enqueue time (a
snapshot: people tagged later aren't added). It needs a token with `read` as well as `send`,
since the items show who has the tag. For a token limited to some chats, members outside its list
are dropped without a trace (not counted, not listed), so it can't learn how big the tag is. An
empty tag is a 400, `tag clients has no contacts` (`…no contacts this token can reach` for a
limited token). The expanded messages count toward the 1000 per request and the 10,000 waiting.
A person in two tagged rows gets two items, unless `dedupe` is on.

Each member's item has the idempotency key `KEY#i:CHAT` (`i` is the row's index), so retrying a
request after the tag changed replays the members already queued and adds only the new ones. A
member first queued under a hidden `…@lid` ID and known by number since is recognized too. A row
changed from a number to a tag (or back) under the same key is a different request: 409.

### `{name}` templates

With `"template": true` (`?template=1`, `wame queue import -template`, MCP `template`), these
placeholders are filled per recipient; without it, braces are plain text.

| Placeholder | Filled with |
|---|---|
| `{name}` | the message's own `name` (JSON field or CSV column), else your name for the person, the phone's address book, the business name, or the verified business name WhatsApp reported |
| `{first_name}` | the first word of `{name}` |
| `{push_name}` | the name the person set in WhatsApp themselves |
| `{name\|there}` | a fallback when there's no name (works on all three) |
| `{{`, `}}` | a literal `{` or `}` |

- **`{name}` never uses the push name.** Anyone can set theirs to anything, and it would go out
  under your account. Ask for `{push_name}` by name when you want it. Groups have no name here.
- A row without a name and without a fallback is refused (400, naming the first lines), so
  nothing goes out as "Hi ,". Any other `{…}` is a 400 too.
- Names are cleaned first: one line, no invisible or direction-changing characters, at most 60
  characters.
- The text is filled in when it's queued: `GET /queue/{id}` shows what goes out, and renaming
  someone later doesn't change it. A retry with the same key compares the template (and the
  row's own `name`), not the filled text, so it replays even after a contact was renamed or lost
  their name; a changed `name` column, or another tag, is a different request (409).
- Needs `read`, like tags: the filled text shows names.

### Skipping repeats: `dedupe` and `skip_sent`

| | `dedupe` | `skip_sent` |
|---|---|---|
| Skips | a row whose recipient, file and (filled) text already appeared earlier in the request | a recipient who already got a message: within a time (`48h`, `90m`, `3d`) or in a batch (`batch:promo-october`) |
| API | `"dedupe": true` / `?dedupe=1` | `"skip_sent": "48h"` / `?skip_sent=batch:promo-october` |
| CLI | `-dedupe` (across the whole file) | `-skip-sent 48h` |

What counts for `skip_sent`:

- **Queue items** that are queued or sending (whatever their age), or were sent within the time
  (with `batch:NAME`: every queued, sending or sent item of that batch still kept; finished items
  are deleted 7 days after they finish, so after that a rerun of the campaign sends again; use a
  time like `30d` with a `read` token for longer). Failed and canceled items
  don't count: the person didn't get the message. Without `admin`, only the token's own items
  count, so a token can't learn whether another one messaged someone.
- **With `read`, every message you sent** in that 1:1 chat within the time: from the phone, `/send`,
  the TUI or the queue (a reader can see those anyway). Up to 90 days. Without `read` only the
  token's queue counts, and finished items are kept 7 days, so the limit is 7 days.
- A person is matched under every form wame knows without asking WhatsApp: the number, its other
  form with or without Brazil's 9th digit, and its `…@lid` ID.

Order per row: a retry's replay first (an item already queued under the row's key is returned),
then `skip_sent`, then `dedupe`. Earlier rows of the same request count too. A retried request
skips the same rows again, unless the time has passed meanwhile: then they're queued, as a fresh
run would. Rows keep their own index in the key, so skipping one never changes another's key.
Two requests at once can't both pass the check: the database is locked while it runs.

```json
{ "batch": "promo-october", "queued": 2328, "replayed": 0, "items": [ … ],
  "skipped": { "duplicate": 4, "recently_sent": 8 },
  "skipped_rows": [ { "line": 7, "index": 5, "to": "5511999999999", "reason": "recently_sent" }, … ] }
```

`skipped_rows` lists the first 100. Skips aren't failures: they create no items, no events and no
alerts. A request where every row was skipped (a rerun of a finished campaign) is a 202 with
`queued: 0`. The audit entry reads `N messages, M skipped`. `wame queue import` prints
`queued 2328 messages, skipped 12 (8 sent in the last 48h, 4 duplicates)`. Its `-dedupe` runs over
the whole file before it's split into requests (rows with the same recipient, text and
`name`), and the server dedupes each request as well, which catches a person in two tags.

## States

```
          ┌──▶ canceled
          ├──▶ expired      (send_before passed)
queued ───┴──▶ sending ──▶ sent
  ▲                 │
  │                 ├──▶ failed   (bad number, not on WhatsApp, 5 attempts used, token revoked, opted out)
  └─────────────────┘ (retry later)
```

`expired` is a queued item whose `send_before` passed before it could go out. Finished items
(sent, failed, canceled, expired) are deleted after 7 days. The sent message itself stays
in the message history like any other.

**Delivered and read.** A sent item gains `delivered_at` when WhatsApp reports it reached the
phone, and `read_at` when the person opened it (only if they share read receipts; a read also
fills `delivered_at` when that receipt never came). They aren't states: the item stays `sent`.
`counts.delivered` and `counts.read` count them among the sent ones, the admin panel shows
✓ sent, ✓✓ delivered and a blue ✓✓ read, and each receipt sends one `queue` event (repeats don't).
Receipts are matched by the item's message id, so they work after a restart too. A receipt on an
item that isn't `sent` (one that failed although an attempt did reach the phone) is recorded but
sends no event, so a failure is reported once.

```json
{ "id": 41, "status": "sent", "message_id": "3EB0C4…", "delivered_at": "2026-10-12T21:41:05-03:00", "read_at": "2026-10-12T21:52:40-03:00", … }
```

## Files

Images and documents can be queued like text, and the usual case is one file to many people. Upload
it once, then give its id to as many messages and batches as you like; `POST /queue` stays small
JSON.

```sh
curl -H "Authorization: Bearer $T" -F file=@menu.pdf localhost:8080/queue/files
# 201 {"id": "f_0123456789abcdef0123456789abcdef", "sha256": "…", "size": 482113, "mime": "application/pdf",
#      "kind": "document", "name": "menu.pdf", "expires_at": "…", …}

curl -H "Authorization: Bearer $T" -X POST localhost:8080/queue -d '{
  "batch": "menu-october", "file": "f_0123456789abcdef0123456789abcdef", "text": "Our new menu",
  "messages": [{"to": "5511999999999"}, {"to": "5511988887777", "text": "Hi John, the menu"}]}'
```

- **Referencing it.** `file` at the top level is the default for every message; a message can set
  its own. With a file, `text` is the caption and may be empty. A CSV takes `?file=` as the
  default (`wame queue import -file PATH`
  uploads and passes it for you). The file must be your own (admin included), or the answer is 404; an id that isn't
  `f_` plus 32 hex digits is a 400. A retry with the same `Idempotency-Key` and a different file is a 409.
- **Uploading.** `POST /queue/files` takes `multipart/form-data` with one part named `file`, up to
  64 MB, like `/send/file`. The answer is 201, or 200 when you already have a live file with the
  same `sha256` (per token: another token's copy is a separate file). `GET /queue/files` lists your
  files with their usage (admin: everyone's, without other tokens' names). `DELETE /queue/files/{id}`
  deletes one; 409 while queued or sending items need it (cancel them first), 404 for an unknown id
  or someone else's file.
- **Types.** Read from the bytes, never from the client's `Content-Type`. Images: JPEG, PNG,
  WebP. Documents: PDF, plain text (`.txt`), CSV (`.csv`) and the Office formats `.docx` `.xlsx`
  `.pptx` `.odt` `.ods` `.odp` (at most 10,000 entries in the archive). Anything else is 415,
  including text that isn't UTF-8 or that starts with `<` (HTML, SVG, XML). The file name's
  extension must match the content (415 otherwise); a name without one
  gets one. An image over 16 MB, or over 16384 px a side or 100 MP, goes as a document. The name is
  display metadata only: base name, invisible and control characters dropped, 255 bytes at most.
- **Quotas.** 50 live files and 500 MB per token (its name), and a total over all tokens set by the
  `queue_files_quota` [setting](API.md#get-settings-patch-settings) (`2GB` by default; `500MB`, `64KB`,
  `1TB` or a byte count; `0` refuses new uploads). Over a quota is a 413 that says which one (your
  own usage, never other tokens'), checked before anything is written to disk; a server that can't
  write the file answers 507. At a quota, uploading bytes you already have still answers 200 with
  that file (the body is hashed, never written), so running an import again keeps working; if the
  server lost its copy of that file, the upload counts as new and gets the 413. At most two uploads per token and eight in all run at once; more is a
  429 with `Retry-After`. An upload must arrive within 5 minutes, so a stalled client can't keep
  its place.
- **Sending.** A file item is one send for the limits, like text: the global limiter, the per-chat
  gap and the token's rate, with no discount. The worker uploads the file to WhatsApp before it takes
  a slot (only when the slot is about to be free), one upload at a time, so a slow upload doesn't
  hold up interactive sends. That upload is **reused** for the later items of the same file while it
  is younger than `queue_media_ttl` (`24h` by default, up to `168h`; `off` uploads for every message).
  After a fresh upload the worker checks pause, quiet hours and the token again before it sends;
  the upload is kept in memory for that item (up to 4 items at once, 10 minutes each), so items
  waiting on busy chats don't upload twice, even with `off`.
  The caption is the item's text.
- **Retries.** An upload error counts as an attempt, backs off like any send error and counts toward
  the whole-queue cooldown (unless the connection dropped: then the attempt is given back, as for text). A send error drops the file's cached upload (fresh or reused), also
  for other messages waiting with it, so the retry uploads again; a shutdown, a rate limit or a
  dropped connection doesn't, since the upload wasn't what failed. A file that is missing or changed on disk fails the item at once with "file is gone".
  A recipient who asks for expired media to be sent again isn't answered (wame doesn't do that for
  `/send/file` either); the TTL keeps that window small.
- **Items.** Items that carry a file show `file` (its id), `kind` (`image` or `document`) and
  `file_size`, in `GET /queue`, `GET /queue/{id}`, the enqueue answer, `queue` events and webhook
  `queue` deliveries; text items have none of them. The file's name is never in any of these.
  The message kept in history has the type, mime, size and, for documents, the name, as `/send/file`
  does.
- **Cleanup.** The worker prunes hourly and once when it starts. A file with no
  open item is deleted 1 hour after its last item finished. A file no item ever used is deleted
  24 hours after upload (`expires_at`; time with the queue paused counts toward that, only for
  such files: a file that queued items need stays while those items are open, paused or not). Files
  of a token that was revoked or expired are deleted, and its queued items then fail with "file is
  gone". A deleted file's row keeps only its id, token, size, hash, mime and kind until its items are
  pruned (7 days), so the item list still says what was sent. Files in `wame-queue-files/` without a row that
  are older than 15 minutes (a crash during an upload) are swept.
- **Privacy.** Queued files sit **unencrypted on the server's disk** in `wame-queue-files/` next to the
  database (`/data/wame-queue-files/` in Docker; directory `0700`, files `0600`, named by random ids) until
  they are sent or pruned. No route returns their content. Names count as text: they are never in
  events, webhook deliveries, the audit log or the admin panel, which shows only the kind and size.
  `wame logout` (and `POST /logout`, or WhatsApp logging the device out) deletes every file and
  removes `wame-queue-files/` (only the files wame wrote there; the directory stays if anything
  else is in it); items still pointing at one then fail with "file is gone". `wame backup`
  copies the database only, not `wame-queue-files/`.
- **Audit.** Uploads are `queue_file` and deletes `queue_file_delete` (target: the file id; detail
  `kind, N bytes`, never the name), a logout that removed files `queue_files_wipe`, and each queued
  file send `send_file` with `kind, N bytes, queue #id`.

From an AI agent, `schedule_message` takes the `file` id of a file you uploaded with the same
token; there is no upload tool ([MCP.md](MCP.md#tools)).

## Quiet hours

`queue_quiet_hours` (`-queue-quiet-hours`, `WAME_QUEUE_QUIET_HOURS`, or the
[setting](API.md#get-settings-patch-settings)) is a daily window like `21:00-08:00`, read on the
clock of `queue_timezone` (`-queue-timezone`, `WAME_QUEUE_TIMEZONE`; an IANA name like
`America/Sao_Paulo`, default `Local`: the server's zone, which in Docker is UTC unless `TZ` is
set; settings and `GET /stats` show which one it is, like `Local (UTC)`). The window may cross
midnight; empty turns it off. Inside it the worker sends nothing and checks again every minute
(so a clock change or a suspended host can't oversleep it) until the window ends, then works
through what's due, at the usual pace. Changing the
setting wakes it at once. Items whose `send_at` falls inside the window go out when it ends, unless their `send_before` comes first: then they expire.
`GET /stats` shows `quiet_until` while it holds, and the admin panel's Queue tab says "quiet hours
until 08:00". Only the queue keeps quiet: `/send` without `send_at`, the TUI, the CLI and auto-replies don't.

**Switching them off and on.** `queue_quiet` (`true` by default) turns the window off without
forgetting it: `PATCH /settings {"queue_quiet": false}` lets the queue send at any hour, and `true`
(or `null`) brings the same window back. `Q` on the admin panel's Queue tab does the same (or
`Enter` on "Quiet hours on" in the Settings tab). While off, `GET /stats` has `quiet_off: true` and
the tab shows the window marked "off"; while on, the panel's header shows `☾ quiet 21:00-08:00`,
or `☾ quiet until 08:00` in amber while it's holding the queue.

## Opt-outs

When someone answers a 1:1 chat with nothing but `STOP`, `SAIR`, `PARAR`, `CANCELAR`,
`UNSUBSCRIBE` or `DESCADASTRAR` (any case, accents ignored), wame puts the chat on an opt-out list
(`wame_optouts`) without replying, and audits it as `optout_add` by `whatsapp`. The list is
checked when an item's turn comes, after its number is resolved to the chat WhatsApp uses: items
to a listed chat fail with `recipient opted out` and don't take a send slot. Queuing to them is
still accepted (the list can change before the item's turn, and one check at send time keeps
every path consistent), so watch for that error. Such items don't count toward the "queued messages
failed" alert. Direct sends aren't affected.

WhatsApp may show a person under a hidden ID (`…@lid`) before it says which number that is; an
opt-out received that way is stored under that ID, matched against both the number and its ID, and
moved onto the number once WhatsApp links them. A number added to the list by hand while logged out
is stored as typed and also matched as typed, in case WhatsApp resolves it to a different JID (a
Brazilian mobile with or without the extra 9). Removing a number removes every form it is stored
under. If the list can't be read, the item waits and is retried without using an attempt. Admins manage the
list with [`/optouts`](API.md#get-optouts-post-optouts-delete-optoutschat).

## API

All routes need the `send` scope. Without `admin`, a token sees and cancels only the items it
queued itself; with `admin`, every item (`?actor=` narrows it).

| Route | |
|---|---|
| `POST /queue` | queue up to 1000 messages. Body: `messages` (each `to`, `text`, optional `reply_to`, `send_at`, `send_before`, `name` and `file`), optional `batch` (letters, digits, `.`, `_`, `-`; generated when left out), top-level `text` / `send_at` / `send_before` / `file` used by messages that leave theirs out, `spread` ([above](#spread-space-a-batch-out)), and the options `template`, `dedupe` and `skip_sent` ([above](#tags-templates-and-skips)). Or a [CSV file](#csv-import) as `text/csv` or `application/csv`. Answers 202 with the items (without text), `queued` (how many items), `replayed` (how many of them were already queued under the idempotency key), `skipped` and `skipped_rows` |
| `PATCH /queue/{id}` | change a queued item: `send_at`, `send_before`, `text` ([above](#changing-a-queued-message)); 409 when it's no longer queued |
| `GET /queue` | items, newest first, plus `counts` by state (`queued`, `sending`, `sent`, `failed`, `canceled`, `expired`). Query: `status` (any of those), `batch`, `actor` (admin), `before` (id, to page), `limit` (≤ 500) |
| `GET /queue/{id}` | one item, with its text |
| `DELETE /queue/{id}` | cancel a queued item (409 when it's already sending or finished) |
| `DELETE /queue?batch=NAME` | cancel every queued item of a batch |
| `POST /queue/files`, `GET /queue/files`, `DELETE /queue/files/{id}` | upload, list and delete [files](#files) |

A request is safe to retry with an `Idempotency-Key` header (or `idempotency_key` in the body):
the same key from the same token returns the items it already queued instead of adding them again
(counted in `replayed`). The same key with different messages (any of `to`, `text`, `reply_to`,
`send_at`, `file`, `spread`, or a named `batch`) is refused with 409, like on `/send`, and nothing of that request is
queued. Replayed items don't count toward the 10,000 waiting limit. A body over 16 MB gets
413.

**Following progress.** `GET /queue?batch=promo-october` shows the counts. Or listen:
`/events?events=queue` streams a `queue` event each time an item changes state or a sent one is
delivered or read, and with `-webhook` a `queue` delivery (`X-Wame-Event: queue`) goes out when an
item is sent, fails, expires, or is delivered or read.
Neither includes the message text.

```json
{ "id": 41, "batch": "promo-october", "actor": "promo-bot", "to": "5511999999999", "status": "sent",
  "attempts": 1, "chat": "5511999999999@s.whatsapp.net", "message_id": "3EB0C4…", "due": "…", "created_at": "…", "updated_at": "…" }
```

An item with a `send_before` shows it as `send_before`, and one changed with `PATCH` has `edited_at`.

An item with a [file](#files) also has `file`, `kind` (`image` or `document`) and `file_size`.

**Settings.** `queue_media_ttl` and `queue_files_quota` (see [Files](#files)) are runtime settings without a flag, changed with `PATCH /settings` or the admin panel's Settings tab.

**Pausing.** The `queue_paused` [setting](API.md#get-settings-patch-settings) holds everything
(`PATCH /settings {"queue_paused": true}`, or `P` on the admin panel's Queue tab). Like other
settings it's kept over restarts.

**Timed `POST /send`.** `POST /send` with `send_at`, `send_before` or `batch` goes through this
queue as one item: 202 with the `POST /queue` answer, quiet hours and opt-outs apply, and its
`Idempotency-Key` is kept as `send:KEY` ([SENDING.md](SENDING.md#scheduled-and-bulk-text-the-queue)).

**Who sends.** The worker runs inside `wame serve` and `wame tui`. `wame mcp` and `wame send` don't
drain the queue. Every send is in the audit log as `send` under the token that queued it, with
`queue #id` in the detail; queuing, changing and canceling are audited as `queue`, `queue_edit` and `queue_cancel`.

**From an AI agent.** The MCP tools `schedule_message`, `list_scheduled`, `reschedule_scheduled` and `cancel_scheduled` use
the same queue with the same rules (send scope, allowlist, own items only); see [MCP.md](MCP.md#tools).

## Why not Redis

We measured it before deciding. On an M1 laptop, SQLite (WAL mode, the same file wame already
uses) stores about **10,000 queued messages per second** one at a time and about **32,000 per
second** in batches of 100. The worker's read, claim and finish of one item take about 0.3 ms
(`go test ./internal/store -run '^$' -bench .`).

The queue's speed is set by WhatsApp, not by storage: at the default 20 sends per minute the worker
needs one row every 3 seconds, about 30,000 times less than SQLite gives. Redis wouldn't send
anything sooner, and it would add:

- another service to run, secure and back up, next to a binary that's self-contained today;
- a second copy of the message text, in memory, which also needs persistence (AOF) configured
  or a Redis restart loses the queue;
- a network hop and a second source of truth to keep in step with the database.

What Redis is good at (many workers sharing one queue across machines) doesn't apply: one wame
process owns one WhatsApp session, and two processes sending for the same account would fight
over it. If you run several numbers, run one wame per number, each with its own database.
