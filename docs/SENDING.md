# Sending messages

How to send every kind of message wame supports, through the HTTP API and through MCP, and what
each one looks like. The full field lists are in [API.md](API.md) and [MCP.md](MCP.md); this page
is the task-oriented view.

| What | HTTP API | MCP tool | CLI / TUI |
|---|---|---|---|
| Text | `POST /send` | `send_message` | `wame send`, TUI chat input or `/send` |
| Text at a set time | `POST /send` with `send_at` | `schedule_message` | `wame send -at`, TUI `/later` |
| Reply (quote) | `POST /send` with `reply_to` | `send_message` with `reply_to` | |
| Image or document | `POST /send/file` | not available | TUI `/file` |
| Reaction | `POST /react` | `react` | |
| Edit | `POST /edit` | not available | |
| Delete for everyone | `POST /delete` | not available | |
| Read receipts | `POST /read` | `mark_read` | TUI `/read` |
| Typing indicator | `POST /typing` | not available | |
| Scheduled or bulk text | `POST /queue` (JSON or CSV) | `schedule_message` | `wame queue import` |

Every route needs a token with the `send` scope (see [Tokens and scopes](API.md#tokens-and-scopes)).
All sends share the [rate limits](API.md#rate-limiting), and every one is in the audit log.

## Recipients

The recipient is `to` in `/send`, `/send/file` and `/queue`, and `chat` in `/react`, `/edit`,
`/delete`, `/read` and `/typing`. Either takes any of:

| Form | Example |
|---|---|
| Phone number with country code, any formatting | `5511999999999`, `+55 (11) 99999-9999` |
| Person JID | `5511999999999@s.whatsapp.net`, `…@lid` |
| Group JID | `120363012345678901@g.us` |

Numbers are checked with WhatsApp first, which also handles Brazil's optional 9th digit
(`5521987654321` and `552187654321` reach the same person). A number that isn't on WhatsApp is a 400;
`GET /contacts/{number}` shows `on_whatsapp` before you send.

**Groups** work everywhere a person does: put the group JID in `to`. Get it from `GET /groups`
(`[{"jid": "120363012345678901@g.us", "name": "Family", "members": 12}]`) or `/groups` in the TUI.

- You must be allowed to post: in a group where only admins send messages, your account must be an admin.
- A group message counts as one send against the rate limits, whatever the member count.
- A token limited to chats needs the group JID in its list (`-chats 120363012345678901@g.us`).
- [Opt-outs](API.md#get-optouts-post-optouts-delete-optoutschat) only come from private chats;
  someone typing `SAIR` in a group doesn't stop queued messages to that group.

## Formatting

WhatsApp formats text with marks inside the text itself. wame sends the text unchanged, so these
work in texts, captions, queued messages and MCP sends:

| Write | Shows as |
|---|---|
| `*bold*` | **bold** |
| `_italic_` | *italic* |
| `~strike~` | ~~strike~~ |
| `_~both~_` | italic and struck through |
| ```` ```monospace``` ```` | monospace |
| `` `inline code` `` | inline code |
| `> quote` at the start of a line | quote block |
| `- item` or `* item` at the start of a line | bulleted list |
| `1. item` at the start of a line | numbered list |

Line breaks, emoji and accents are kept as sent.

**Not supported:**

- **HTML.** WhatsApp has no HTML messages; `<b>hi</b>` arrives as those characters.
- **Standard Markdown.** It isn't converted: `**bold**`, `# Heading` and `[text](url)` arrive
  as typed. Use the marks above and bare URLs.
- **Link previews.** Texts go out without the preview card, so a link is tappable but shows no
  image or title. To show a picture, send it with `POST /send/file` and the text as its caption.

## Text

### HTTP

```sh
curl -H "Authorization: Bearer $T" -X POST localhost:8080/send \
  -d '{"to":"5511999999999","text":"*Hi Maria!* Your order shipped ✅"}'
```

```json
{ "to": "5511999999999", "text": "…", "reply_to": "3EB0C767D26A1D0F5E2A", "idempotency_key": "order-1234-shipped" }
```

Only `to` and `text` are required. Send `idempotency_key` (or the `Idempotency-Key` header) when
a retry must not send twice; see [Idempotency](API.md#post-send).

**Long or multi-line text.** Text with line breaks or double quotes is easy to break when typed
inside `-d '…'`. Keep the message in a file and let `jq` build the JSON:

```sh
jq -Rs '{to: "5511999999999", text: .}' promo.txt | \
  curl -H "Authorization: Bearer $T" -X POST localhost:8080/send --data-binary @-
```

`jq -Rs` reads the whole file as one JSON string. Use `--data-binary`; `-d` drops line breaks.

### MCP

```json
{ "name": "send_message", "arguments": { "to": "5511999999999", "text": "*Hi Maria!* Your order shipped ✅" } }
```

Returns `{"seq": 1042, "id": "3EB0C767D26A1D0F5E2A", "chat": "5511999999999@s.whatsapp.net"}`.
Over stdio it needs `wame mcp -allow-send`; over `/mcp`, a token with `send`.

## Reply (quote a message)

Add `reply_to` with the `id` of a stored message in the same chat. The reply shows the quoted
message above it.

```sh
curl -H "Authorization: Bearer $T" -X POST localhost:8080/send \
  -d '{"to":"5511999999999","text":"Yes, still available!","reply_to":"3EB0C767D26A1D0F5E2A"}'
```

MCP: `send_message` with `"reply_to": "3EB0C767D26A1D0F5E2A"`. Ids come from `GET /messages`,
the webhook, `/events` or the MCP `read_messages` tool. An unknown or deleted id is a 400.

## Image or document

`POST /send/file` is a multipart upload. wame sniffs the file's bytes (`http.DetectContentType`), not
its name: anything it detects as an image (JPEG, PNG, GIF, WebP, also BMP and ICO) goes as a photo,
everything else, including videos, audio and SVG, as a document. Up to 64 MB (413 above that).

```sh
curl -H "Authorization: Bearer $T" -X POST localhost:8080/send/file \
  -F to=5511999999999 \
  -F file=@churu.jpg \
  -F caption="*Churu 56g* por R\$ 19,12 😻"
```

| Field | Required | |
|---|---|---|
| `to` | yes | person or group |
| `file` | yes | the file |
| `caption` | no | text under the image or document; the [formatting](#formatting) marks work. A document without one is stored with its file name as `text` |
| `reply_to` | no | quote a message in the same chat |
| `idempotency_key` | no | or the `Idempotency-Key` header |

**Caption from a file.** `-F "caption=<promo.txt"` (with `<`, not `@`) makes curl read the
caption from a file, so line breaks, quotes and emoji arrive exactly as written, without JSON:

```sh
curl -H "Authorization: Bearer $T" -H "Idempotency-Key: churu-5511999999999" \
  -X POST localhost:8080/send/file \
  -F to=5511999999999 -F file=@churu.jpg -F "caption=<promo.txt"
```

Returns the sent Message with `type` `image` or `document` and a `media` object.

**MCP** has no file tool: models shouldn't move megabytes of base64 through their context. Send
files through the API, or the TUI's `/file`.

**Many recipients.** The send queue also takes files (upload once with `POST /queue/files`, then
queue with `file`; see [QUEUE.md](QUEUE.md#files)), but `/send/file` sends one file to one recipient
at once, so a file to many people through it is one call per recipient. Each call waits for its rate-limit slot (or answers 429), and opt-outs
are not checked outside the queue. A token also has its own rate, 10 a minute by default, so a long
loop gets 429s (`curl -f` makes them fail); raise it with `wame token create -rate`:

```sh
while read n; do
  curl -sf -H "Authorization: Bearer $T" -H "Idempotency-Key: churu-$n" \
    -X POST localhost:8080/send/file -F to="$n" -F file=@churu.jpg -F "caption=<promo.txt"
done < numbers.txt
```

Queued files (upload once, send to many) are planned in
[QUEUE-FILES.md](proposed-features/QUEUE-FILES.md).

## Reaction

```sh
curl -H "Authorization: Bearer $T" -X POST localhost:8080/react \
  -d '{"chat":"5511999999999","id":"3EB0C767D26A1D0F5E2A","emoji":"😻"}'
```

An empty `emoji` removes your reaction. MCP: `react` with `chat`, `message_id`, `emoji`.
Returns a Message with `type: "reaction"`.

## Edit and delete

```sh
curl -H "Authorization: Bearer $T" -X POST localhost:8080/edit \
  -d '{"chat":"5511999999999","id":"3EB0C767D26A1D0F5E2A","text":"*Por: R$ 19,12*"}'

curl -H "Authorization: Bearer $T" -X POST localhost:8080/delete \
  -d '{"chat":"5511999999999","id":"3EB0C767D26A1D0F5E2A"}'
```

- **Edit**: your own text messages only, within 20 minutes of sending. Captions can't be edited.
- **Delete** removes the message for everyone: your own messages, or other people's in a group
  where you're an admin.

Neither is available over MCP.

## Read receipts and typing

```sh
curl -H "Authorization: Bearer $T" -X POST localhost:8080/read -d '{"chat":"5511999999999"}'
curl -H "Authorization: Bearer $T" -X POST localhost:8080/typing -d '{"chat":"5511999999999","state":"typing"}'
```

`/read` takes optional `id` or `ids`; without them it marks the latest 50 incoming messages. MCP:
`mark_read` with `chat` and optional `message_ids`. `/typing` takes `typing`, `recording` or
`paused`.

## Scheduled and bulk text (the queue)

`POST /queue` stores messages and answers 202 at once; a worker sends them in the background
within the rate limits, retrying on errors. Items to a chat that opted out fail with `recipient
opted out`. See [QUEUE.md](QUEUE.md) for states, pacing and quiet hours.

```sh
jq -Rs '{batch: "churu-promo", text: ., messages: [
          {to: "5511999999999"},
          {to: "120363012345678901@g.us"},
          {to: "5511988887777", text: "Hi John!", send_at: "2026-10-20T09:00:00-03:00"}]}' promo.txt | \
  curl -H "Authorization: Bearer $T" -H "Idempotency-Key: churu-1" \
    -X POST localhost:8080/queue --data-binary @-
```

```json
{
  "batch": "churu-promo",
  "text": "default text for messages without their own",
  "send_at": "optional default: RFC 3339 with offset, or 2026-10-20T09:00 for the recipient's own clock",
  "send_before": "optional: 2h after send_at, or a time; later than that it expires instead of going out",
  "messages": [
    { "to": "5511999999999" },
    { "to": "5511988887777", "text": "own text", "send_at": "2026-10-20T09:00:00-03:00", "reply_to": "3EB0…" }
  ]
}
```

Up to 1000 messages and 16 MB per request; `send_at` at most 30 days ahead. Follow it with
`GET /queue?batch=churu-promo`; cancel what hasn't gone out with `DELETE /queue?batch=churu-promo`.
A date and time without offset (`2026-10-20T09:00`) is read on the recipient's clock (their contact
time zone, else `queue_timezone`), so one request reaches everyone at their own 09:00. `spread`
(like `"2h"`) spaces a batch evenly over a window. Change a queued message with `PATCH
/queue/{id}`. All of it is in [QUEUE.md](QUEUE.md#when-it-goes-out).

**One timed message**: `POST /send` takes the same `send_at` and `send_before`, and `batch`, for a
single message; no need for the `messages` list.

```sh
curl -H "Authorization: Bearer $T" -H "Idempotency-Key: hello-1" -X POST localhost:8080/send \
  -d '{"to": "5511999999999", "text": "Good morning!", "send_at": "2026-10-21T09:00", "send_before": "2h"}'
```

```json
{ "batch": "b-3f9a1c2d4e5b", "queued": 1, "replayed": 0, "items": [ { "id": 42, "status": "queued", … } ], "skipped": {"duplicate": 0, "recently_sent": 0}, "skipped_rows": [] }
```

It answers **202** like `POST /queue` instead of the sent message: the message goes through the
queue, so quiet hours and opt-outs apply, and it waits for its turn. The batch is generated, or
the `batch` you gave. `Idempotency-Key` is kept in the queue as `send:KEY`, so it survives a
restart (a plain `/send` key is remembered in memory only) and can't clash with a `POST /queue` key.
The token's chat allowlist applies as for an immediate send. `POST /send/file` has no `send_at` (a request with `send_at` or `send_before` is refused with 400, never sent at once): a
scheduled file goes through [queue files](QUEUE.md#files).

**From the command line**: `wame send -at WHEN [-before WHEN] NUMBER TEXT` queues the message on a
running server (`-server`, `-profile`, like `wame queue`) and exits: `queued #42 for Wed 21 Oct 09:00 (-03)`.
`WHEN` is a delay (`2h`), a time of day (`09:00`, `tomorrow 09:00`) or one of the two API forms. See
[CLI.md](CLI.md#wame-send-numberjid-text).

**From a spreadsheet**: `POST /queue` with `Content-Type: text/csv`, or `wame queue import
promo.csv` for files of any length. Columns `to` (required), `text`, `send_at`, `reply_to`. A
quoted CSV field can hold line breaks, so multi-line promos fit in the `text` column. See
[CSV import](QUEUE.md#csv-import).

**MCP**:

```json
{ "name": "schedule_message", "arguments": { "to": "5511999999999", "text": "…", "send_at": "2026-10-20T09:00:00-03:00", "batch": "churu-promo" } }
```

`send_at` also takes a delay like `90m` or `2h`, or `09:00` / `tomorrow 09:00` and a date and time
without offset (on the recipient's own clock); `send_before` and `spread` (with `tag:`) work as in
the API. One message per call; `list_scheduled` follows them, `reschedule_scheduled` changes a
queued one and `cancel_scheduled` cancels it. Scheduled messages sent over stdio wait until
`wame serve` or `wame tui` runs on the same database.

## What a sent message looks like

`/send`, `/send/file`, `/react`, `/edit` and `/delete` return the stored
[Message object](API.md#the-message-object):

```json
{
  "seq": 1042,
  "id": "3EB0C767D26A1D0F5E2A",
  "chat": "5511999999999@s.whatsapp.net",
  "sender": "5511988887777@s.whatsapp.net",
  "from_me": true,
  "is_group": false,
  "type": "image",
  "text": "*Churu 56g* por R$ 19,12 😻",
  "media": { "mimetype": "image/jpeg", "size": 183204, "path": "/media/1042" },
  "timestamp": "2026-10-15T10:00:00-03:00"
}
```

Keep `id` to reply to, react to, edit or delete the message later. Delivered and read receipts
are tracked for queued messages (`delivered_at`, `read_at` in `GET /queue`).

## Not supported

Sending these isn't built: videos and audio as playable media (they go as documents), voice
notes, stickers, locations, contacts cards, polls, buttons and lists, albums, link preview cards,
status updates, and files in the queue. wame does receive and store most of them.
