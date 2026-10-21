# MCP server

wame speaks the [Model Context Protocol](https://modelcontextprotocol.io), so AI agents (Claude Code,
Claude Desktop, other MCP clients) can read and send WhatsApp messages through it. It's built on the
official Go SDK, `github.com/modelcontextprotocol/go-sdk` v1.8.0.

The MCP tools call the same `Bot` methods as the HTTP API. The global rate limits, the audit log, scopes
and chat allowlists all apply. The audit actor is the token's name, or `mcp` for a stdio server that
isn't using a token.

## Two ways to connect

### 1. HTTP at `/mcp` on a running server (recommended)

`wame serve` (and `wame tui -addr …`) also serves MCP over streamable HTTP at `/mcp`:

```sh
WAME_TOKEN=$(openssl rand -hex 24) ./wame serve      # or use tokens only, see below
./wame token create claude -scopes read -chats 5511999999999 -expires 720h
```

- Each request carries `Authorization: Bearer <token>`. That token's scopes and chat allowlist are
  checked on **every tool call**, so give each agent its own token (see
  [API.md → Tokens and scopes](API.md#tokens-and-scopes)).
- An MCP session is pinned to the token that opened it; another token can't take it over.
- A revoked or expired token fails on its next request.
- Before any token exists and without `-token`, `/mcp` works like the rest of the API in token-less
  mode: same-origin requests over a loopback connection to a loopback `Host` only, acting as the owner.
- It shares the process with the API and TUI, so there's only one WhatsApp connection.

Failed logins on `/mcp` count toward the same per-IP block as the rest of the API, and the send tools
charge the token's own per-token rate (one budget shared with its HTTP calls). The global limits apply
too.

### 2. stdio: `wame mcp`

```sh
wame mcp                     # read-only: status, list_chats, read_messages, search_messages, find_contact, contact_profile
wame mcp -allow-send         # also send_message, react, mark_read and the queue tools
wame mcp -mcp-token wame_…  # act as that API token (its scopes and chat allowlist)
```

- `-mcp-token` (or `WAME_MCP_TOKEN`) must be a token created with `wame token create` or `POST /tokens`.
  The owner secret doesn't work there. Every tool call re-checks the token, so revoking it or letting it
  expire takes effect at the next call, even in a `wame mcp` that's already running.
- It holds **its own WhatsApp connection**, so it can't run alongside `wame serve` or `wame tui` on the
  same database: they'd kick each other off. If you already run the server, use `/mcp` instead.
- It needs an existing login (`wame login`).
- stdout carries the protocol. App logs go to stderr (or `-log-file`), and whatsmeow's logs are off.
- All global flags apply (`-db`, `-rate`, …). Auto-replies stay on unless you pass `-autoreply=false`.
- It doesn't run the [send queue](QUEUE.md): messages scheduled through it wait in the database until
  `wame serve` or `wame tui` runs on it (`schedule_message` says so in its `note`).

## Tools

To send every kind of message (files, edits and deletes are API-only), see [SENDING.md](SENDING.md).

| Tool | Scope | Input | Output |
|---|---|---|---|
| `status` | read | none | `connected`, `logged_in`, `account` (your JID; empty for tokens limited to chats), `state` |
| `list_chats` | read | `limit` (default 30, max 200), `unread_only` | `chats[]`: `chat`, `is_group`, `unread`, `last_at`, `last_from` (`me`/`them`), `untrusted.name`, `untrusted.last_text` |
| `read_messages` | read | `chat` (JID or number), `limit` (default 50, max 200), `since_seq` | `messages[]` oldest first, `next_seq`, `note`. Without `since_seq`, the latest messages; pass `next_seq` as `since_seq` to page forward |
| `search_messages` | read | `query`, `chat` (optional), `limit` (default 30, max 200) | `messages[]` newest first; same matching as `q=` in the API |
| `find_contact` | read | `query` (part of a name, a tag or part of a number) | `contacts[]` (max 50, sorted by name): `chat`, `phone`, `name_source` (`own`, `book`, `business`, `push`, `number`), `untrusted.name` (the one name wame shows, as in [`/contacts`](API.md#contacts)), `untrusted.push_name`, `untrusted.tags`; `note`. A token limited to chats finds only those chats |
| `contact_profile` | read | `chat` (number or JID) | `chat` (phone JID), `phone`, `lid`, `name_source`, `on_whatsapp`, `verified_level`, `first_at`, `last_at`, `sent`, `received`, `unread`, `note`; `untrusted`: `name`, `own_name`, `book_name`, `business_name`, `push_name`, `tags`, `about`, `verified_name`, `groups_in_common` (`[{chat, name}]`). Read-only: never asks WhatsApp and never refreshes. A token limited to chats gets only its allowed chats (otherwise `this connection can't access that chat`) and only the groups on its list. Never returned, even to admin: notes, language, time zone, the picture URL, the business profile, queue stats, opt-out status, which tokens can see the chat, WhatsApp error text |
| `send_message` | send | `to`, `text`, `reply_to` (optional) | `seq`, `id`, `chat` |
| `react` | send | `chat`, `message_id`, `emoji` (empty removes) | `seq`, `id`, `chat` |
| `mark_read` | send | `chat`, `message_ids` (optional; omit for the whole chat) | `marked` |
| `schedule_message` | send | `to` (or `tag:NAME`: everyone with that contact tag, needs read too), `text` (optional with `file`: then it's the caption), `file` (optional: id of a file uploaded with `POST /queue/files` under the same token), `send_at` (optional: RFC 3339 with offset; a date and time without offset like `2026-10-21T09:00`, or `09:00` / `tomorrow 09:00`, for that time on the recipient's own clock; or a delay like `90m`, `2h`, `3d`), `send_before` (optional: a duration after `send_at` like `2h`, or a time; past it the message ends `expired`), `spread` (optional, with `tag:`: space the members evenly over this window, at most `24h`), `batch` (optional), `template` (optional: fill `{name}`, `{first_name}`, `{push_name}`, with `{name\|fallback}`; needs read too) | `id` (one recipient only), `batch`, `to`, `status`, `due`, `queued` (how many items), `note` (when the queue is paused or no worker runs) |
| `list_scheduled` | send | `status` (`queued`, `sending`, `sent`, `failed`, `canceled` or `expired`), `batch`, `before` (id, to page), `limit` (default 50, max 200) | `items[]` newest first: `id`, `batch`, `actor`, `to`, `chat`, `status`, `attempts`, `due`, `send_before` and `edited_at` (when set), `created_at`, `updated_at`, `message_id`, `error`, `delivered_at` and `read_at` (once WhatsApp reports them), `kind` (`text`, `image` or `document`) and `file_size`, `untrusted.text` (own items only); `counts` by status (plus `delivered` and `read` among the sent); `note` |
| `reschedule_scheduled` | send | `id`, and any of `send_at` (the forms above), `send_before` (a deadline, or `none` to remove it), `text` (a file's caption may be empty) | `id`, `batch`, `to`, `status`, `due`, `queued` (1) |
| `cancel_scheduled` | send | `id` **or** `batch` | `canceled` (ids) |

Each message in `messages[]` has `seq`, `id`, `chat`, `from` (`me`/`them`), `sender`, `type`,
`reply_to`, `edited`, `deleted`, `has_media`, `timestamp`, and `untrusted.text`,
`untrusted.push_name`, `untrusted.file_name`.

The queue tools use the [send queue](QUEUE.md), like `POST /queue`: `schedule_message` stores the
message and returns at once; the worker sends it when it's due, within the global limits and the
token's own rate (charged at send time, not when scheduling). Without `send_at` it goes out as soon as
the limits allow; `send_at` may be at most 30 days ahead, and an instant (a delay, a time of day or an RFC 3339 time) more than a minute in the past is
refused. A time without offset is read on the recipient's clock: their contact `time_zone`, else the queue's `queue_timezone`
(whose date `tomorrow` means); see [QUEUE.md](QUEUE.md#when-it-goes-out).
`reschedule_scheduled` changes a message that is still queued (its time, its `send_before` or its text); it works on the
connection's own items only, even with the `admin` scope, since it can change the text. A message that already went out can't be changed. Without the `admin` scope, `list_scheduled` and `cancel_scheduled` only see the items the
same token (or, over stdio without a token, `mcp`, a name no token can take) queued; with it, every item, but the text of other
callers' items is never returned. A finished item can't be canceled ("only queued messages can be
canceled"). A `tag:` recipient queues one item per tagged person, the same
[rules](QUEUE.md#tags-templates-and-skips) as the API: a token limited to chats reaches only the
members on its list, without learning about the rest. With `template`, `{name}` is never a push
name; the filled text shows in `list_scheduled` under `untrusted.text`.

**Files.** There is no upload tool: a model shouldn't push megabytes through its context. The user uploads
the file through [`POST /queue/files`](API.md#post-queuefiles-get-queuefiles-delete-queuefilesid)
(or `curl -F file=@menu.pdf`) with the same token and gives the model the id; `schedule_message` then
queues it with the same scopes, allowlist and own-files rule as the API (an id that isn't the
token's own is refused as not found). No tool returns a file's name or content.
See [QUEUE.md](QUEUE.md#files).

Read tools and `list_scheduled` are annotated read-only. `send_message` and `schedule_message` are
marked destructive and open-world, `react` / `mark_read` open-world, and `cancel_scheduled` and `reschedule_scheduled`
destructive. A restricted token gets "this connection can't access that chat" for
chats outside its allowlist. A rate-limited send comes back as "rate limited: wait N seconds before
trying again".

## Safety

- **Untrusted text.** Message text, names, captions and file names were written by other people
  (so were imported contact names and tags).
  They're returned only under `untrusted`, each list carries a `note` saying so, and the server
  instructions tell the model to treat them as data, never as instructions.
- **Cleaning.** Untrusted fields have every invisible character removed: control characters, format
  characters (zero-width spaces and joiners, bidi marks and overrides, soft hyphens, the invisible
  "tag" characters used to smuggle hidden text) and variation selectors. They're capped at 4000
  characters. Message ids, which the sender's app chooses, are cleaned too and capped at 128 bytes.
- **Server instructions** also say: only send, react, mark read, schedule or cancel when the user
  asked; before scheduling, state the send time in the user's own time zone and confirm it (and pass
  `send_at` with that offset rather than assuming UTC); wait instead of looping when rate limited.
- **Scheduling.** The queue tools need the `send` scope, so a `read`-only connection can't schedule,
  list or cancel. A token limited to chats can only schedule to those chats, checked without asking
  WhatsApp (the same check as `send_message`); other recipients are resolved by the worker at send
  time. Scheduled text comes back only to the caller that queued it, under `untrusted` and cleaned.
  Revoking the token fails its items that haven't gone out yet.
- **Contacts.** `find_contact` and `contact_profile` never return your notes, language or time
  zone, even to an admin connection, and never look anything up on WhatsApp. `contact_profile` puts
  the text other people wrote (names, tags, about, verified name, group names) under `untrusted`.
- **Limits and audit.** Global send and lookup limits apply. Every send, reaction and read is in
  `GET /audit` under the token's name, or `mcp` for stdio without a token.
- **Approvals.** Configure your client to ask before each `send_message`, `schedule_message`, `react`
  and `mark_read` call. Prompt injection through a message can't send anything if a human approves every send.
- Prefer a `read`-only token with a chat allowlist, and add `send` only for agents that need it.

## Client setup

### Claude Code

HTTP (server already running):

```sh
claude mcp add --transport http wame http://127.0.0.1:8080/mcp \
  --header "Authorization: Bearer $WAME_TOKEN"
```

Use a token made for the agent (`wame token create claude …`) rather than the owner secret.

stdio:

```sh
claude mcp add wame -- wame mcp                  # read-only
claude mcp add wame -- wame mcp -allow-send
```

### Claude Desktop

stdio, in `claude_desktop_config.json`. Use the absolute path to the binary, since Desktop doesn't
use your shell's `PATH`:

```json
{
  "mcpServers": {
    "wame": {
      "command": "/usr/local/bin/wame",
      "args": ["mcp"],
      "env": { "WAME_DB": "/Users/maria/.config/wame/wame.db" }
    }
  }
}
```

To reach `/mcp` on a running server from Desktop, bridge it with
[`mcp-remote`](https://www.npmjs.com/package/mcp-remote):

```json
{
  "mcpServers": {
    "wame": {
      "command": "npx",
      "args": ["-y", "mcp-remote", "http://127.0.0.1:8080/mcp", "--header", "Authorization: Bearer ${WAME_AGENT_TOKEN}"],
      "env": { "WAME_AGENT_TOKEN": "wame_…" }
    }
  }
}
```
