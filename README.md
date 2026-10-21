# wame

**WhatsApp manager for your terminal: a TUI, an HTTP API, a bot, an MCP server and a remote admin panel in one binary.**

[![CI](https://github.com/williamtcastro/wame/actions/workflows/ci.yml/badge.svg)](https://github.com/williamtcastro/wame/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/go-1.26+-00ADD8?logo=go&logoColor=white)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

wame links to your WhatsApp account as a linked device, like WhatsApp Web does. From there you
can read, search and answer your chats in the terminal, script them over HTTP, or let an AI agent
handle them. Scoped tokens, rate limits and an audit log keep each of those in check.

It's one Go binary and one SQLite file, with no cgo and no external services. Built on
[whatsmeow](https://github.com/tulir/whatsmeow).

![wame TUI: chat list on the left, a conversation with a quoted reply, a reaction and a PDF on the right](docs/wame-tui.png)

<sub>Mocked data; regenerate with <code>_demo/screenshot.sh</code>.</sub>

## What you can do

| | |
|---|---|
| 💬 **Chat from the terminal** | Chat list with unread badges, replies, reactions, edits, deletes, saving media, search across every chat, vim-style keys and command autocomplete |
| 🔌 **Script it over HTTP** | Send texts and files, reply, react, edit, delete and mark as read. Query history with filters and full-text search, long-poll or stream new messages, receive signed webhooks. Interactive docs at `/docs` |
| 🤖 **Hand it to an AI agent** | A built-in MCP server (HTTP at `/mcp` or `wame mcp` on stdio) for Claude Code, Claude Desktop and other MCP clients. Message text comes back marked as untrusted |
| 🔐 **Stay in control** | One token per agent or bot, with `read` / `send` / `admin` scopes, a chat allowlist, its own send rate, expiry and instant revocation. Every action is audited |
| 🛰️ **Run it on a server** | A Docker image, and `wame admin`: a live terminal panel for a remote server with QR login, chats, token rotation and usage, the audit log, limiter and webhook health, the send queue, the server's logs, and settings you can change without a restart. Gotify alerts, Prometheus metrics at `/metrics`, and `wame backup` for a safe copy of the database while it runs |
| 👥 **Your own contacts** | Names, tags and notes per person on top of WhatsApp's address book, one name rule everywhere (API, TUI, admin panel, MCP), CSV import and export, a Contacts tab in the admin panel and `/name` / `/tag` in the TUI. A profile per person (`GET /contacts/{number}`, MCP `contact_profile`): about, photo, business info, groups in common and your stats, from a cache an admin can refresh |
| 📬 **Queue bulk sends** | `POST /queue` takes up to 1000 messages at once and a worker sends them at a safe pace, with random gaps, quiet hours, scheduled times (on each recipient's own clock, with an expiry), retries and back-off, and tracks when each one is delivered and read. The queue survives restarts and can be paused or canceled by batch |
| 🐢 **Avoid bans** | Sends are spaced out by default: 20 per minute and 1 s between messages to the same chat. Number lookups and auto-replies have their own caps |

> [!WARNING]
> whatsmeow is unofficial. Automated or bulk messaging can get a number banned. Use a number you
> can afford to lose, keep the rate limits on, and don't spam.

## Quick start

```sh
go install github.com/williamtcastro/wame@latest   # Go 1.26+; installs to $(go env GOPATH)/bin

wame login    # scan the QR: WhatsApp → Linked devices → Link a device
wame tui      # start chatting
```

`wame login -phone +5511999999999` gives you an 8-character pairing code instead of a QR. The
session lives in `~/.config/wame/wame.db` (or `$XDG_CONFIG_HOME/wame/wame.db`; change it with `-db`
or `WAME_DB`). **Anyone with that file can act as your WhatsApp account, so keep it private and out
of git.**

<details>
<summary>Other ways to install</summary>

**From source:**

```sh
git clone https://github.com/williamtcastro/wame && cd wame
go build -o wame .
go test ./...    # optional: the test suite runs offline
```

**Prebuilt binaries** for Linux and macOS (amd64 and arm64) are published on the
[Releases page](https://github.com/williamtcastro/wame/releases) for each tagged version, with
`checksums.txt`:

```sh
tar xzf wame_*_darwin_arm64.tar.gz
shasum -a 256 -c checksums.txt --ignore-missing
mv wame /usr/local/bin/
```

`wame version` prints the release version, or `dev` for a local build.

**Docker:** `docker compose up -d`, with the database in a Docker volume or a host folder. See
[docs/DOCKER.md](docs/DOCKER.md).

</details>

## The terminal UI

```sh
wame tui                          # just the terminal UI
wame tui -addr 127.0.0.1:8080     # and the HTTP API, from the same process
```

| Keys | |
|---|---|
| `Tab` | move between the input, the chat list (type to filter) and the messages |
| `Ctrl+J` / `Ctrl+K` | next / previous chat |
| `Ctrl+D` / `Ctrl+U` | scroll half a page |
| `j` / `k`, `g` / `G` | with a message selected: move, first / last |
| `r` `e` `d d` `1`–`6` `s` | reply, edit, delete, react, save media |
| `Ctrl+Y` | copy the open chat's number |

Commands start with `/` and autocomplete with `Tab`: `/to maria`, `/send 5511999999999 hi`,
`/file ~/photo.jpg`, `/search boleto`, `/groups`, `/help`. Full guide: [docs/TUI.md](docs/TUI.md).

## The HTTP API

```sh
export WAME_TOKEN=$(openssl rand -hex 24)   # owner secret, 16+ characters
wame serve                                  # 127.0.0.1:8080
```

```sh
H="Authorization: Bearer $WAME_TOKEN"

curl -H "$H" localhost:8080/status
curl -H "$H" -X POST localhost:8080/send -d '{"to":"+55 11 99999-9999","text":"Hello from wame 👋"}'
curl -H "$H" -X POST localhost:8080/send/file -F to=5511999999999 -F caption="look" -F file=@photo.jpg
curl -H "$H" "localhost:8080/messages?q=boleto&limit=10"            # full-text search
curl -H "$H" "localhost:8080/messages/wait?from_me=false&timeout=60" # wait for the next message
curl -N -H "$H" localhost:8080/events                                # live stream (SSE)
curl -H "$H" -X POST localhost:8080/queue -d '{"batch":"promo","text":"Hi!","messages":[{"to":"5511999999999"},{"to":"5511988887777"}]}'
```

While it runs, open **http://localhost:8080/docs** to try every endpoint from the browser. Full
reference: [docs/API.md](docs/API.md), or the OpenAPI spec in [docs/openapi.yaml](docs/openapi.yaml).

**Safe defaults.** `serve` listens on localhost only. It refuses any other address until a token
is set. Without any token, it accepts only same-origin requests from the machine itself. Pass
secrets as environment variables, not flags, since flags show up in `ps`.

### Tokens for agents and bots

Don't share the owner secret. Give each client its own token, limited to what it needs:

```sh
wame token create support-bot -scopes read,send -chats 5511999999999 -rate 5 -expires 720h
wame token list
wame token revoke support-bot     # also revokes tokens it created; open streams close within 10 s
```

The token is shown once (`wame_…`), and only its hash is stored. Once any token exists, every
request needs one, from localhost too. See [Tokens and scopes](docs/API.md#tokens-and-scopes) and
the [security review](docs/security/SCOPED-TOKENS.md).

## AI agents (MCP)

```sh
wame token create claude -scopes read -chats 5511999999999 -expires 720h
claude mcp add --transport http wame http://127.0.0.1:8080/mcp --header "Authorization: Bearer wame_…"
```

Without a running server, `claude mcp add wame -- wame mcp` uses stdio. It's read-only unless you
pass `-allow-send`. The tools are `status`, `list_chats`, `read_messages`, `search_messages`,
`find_contact`, `contact_profile`, `send_message`, `react`, `mark_read`, `schedule_message`,
`list_scheduled`, `reschedule_scheduled` and `cancel_scheduled`. Each one goes through the same scopes,
allowlists, rate limits and audit log as the API. Setup for Claude Desktop and other clients:
[docs/MCP.md](docs/MCP.md).

## Managing a remote server

Running `wame serve` on a VPS or home server? Manage it from your laptop:

```sh
export WAME_TOKEN=…   # owner secret or an admin token
wame admin -server https://wame.example.com
```

A terminal panel, updated live: connection status, a QR to link the server's WhatsApp from afar
(or a pairing code), chats with search and sending, tokens (create, rotate, revoke), the audit log,
limiter and webhook health, runtime settings, the send queue and the server's recent logs, across several saved servers. It uses only the HTTP API and
refuses plain `http://` beyond localhost, so use HTTPS or an SSH tunnel. See [docs/ADMIN.md](docs/ADMIN.md).

## Bots and webhooks

wame calls your code when a message arrives:

```sh
export WAME_WEBHOOK_SECRET=$(openssl rand -hex 24)
wame serve -webhook https://example.com/hook -webhook-filter "type=text&from_me=false"
```

Each message is POSTed as JSON, signed with HMAC-SHA256 (`X-Wame-Signature`) and retried with
backoff. Your code answers through `POST /send`, in any language. If you can't expose a port,
long-poll `GET /messages/wait` or stream `GET /events` instead. [docs/BOT.md](docs/BOT.md) has
complete examples in Python and Node, including signature verification and running as a service.

Out of the box, `serve` and `tui` answer `!ping`, `!time` and `!help` in any chat. Turn that off
with `-autoreply=false`.

## Scripts and cron

```sh
wame send 5511999999999 "Backup finished ✅"
wame send 120363012345678901@g.us "hello group"
```

Only one process can use a session at a time. While `serve` or `tui` is running, send through the
HTTP API instead. To send later, `wame send -at 09:00 5511999999999 "Good morning"` queues the
message on a running server.

```sh
wame backup -keep 7 ~/wame-backups/      # a consistent copy, even while the server runs
wame queue import promo.csv              # queue a CSV (to,text,send_at,reply_to,name,send_before) on a running server
wame queue status -batch promo           # its progress
```

**Recipients** can be a phone number in any format with the country code (`5511999999999`,
`+55 (11) 99999-9999`) or a JID (`…@s.whatsapp.net`, a group `…@g.us`, a `…@lid` user). Numbers
are checked with WhatsApp first, which also handles Brazil's optional 9th digit.

## Configuration

The most used global flags:

| Flag | Env | Default | |
|---|---|---|---|
| `-db` | `WAME_DB` | `~/.config/wame/wame.db` | session and message database |
| `-addr` | `WAME_ADDR` | `127.0.0.1:8080` for `serve` | HTTP listen address; a non-loopback address needs a token |
| `-token` | `WAME_TOKEN` | | owner secret, 16+ characters (for `admin`: the token to connect with) |
| `-server` | `WAME_SERVER` | `http://127.0.0.1:8080` | `admin` only: the server to manage |
| `-webhook` / `-webhook-secret` / `-webhook-filter` | `WAME_WEBHOOK*` | | where and what to deliver, and how to sign it |
| `-rate` / `-chat-gap` / `-max-wait` | `WAME_RATE` | `20` / `1s` / `30s` | send limits; a send that would wait longer than `-max-wait` gets a 429 |
| `-online` | | `false` | appear online (off keeps notifications on your phone) |
| `-autoreply` | | `true` | answer `!ping` / `!time` / `!help` |
| `-queue-jitter` | `WAME_QUEUE_JITTER` | `2s` | random extra wait between [queued](docs/QUEUE.md) sends |
| `-queue-quiet-hours` / `-queue-timezone` | `WAME_QUEUE_QUIET_HOURS` / `WAME_QUEUE_TIMEZONE` | off / `Local` | daily window when the queue sends nothing, e.g. `21:00-08:00`, and its time zone |
| `-alert-gotify` / `-alert-gotify-token` / `-alert-delay` | `WAME_ALERT_GOTIFY*` / `WAME_ALERT_DELAY` | / / `2m` | push alerts to Gotify when WhatsApp disconnects or logs out, or the queue or webhook is in trouble |
| `-log-format` / `-log-file` / `-debug` | `WAME_LOG_*` | `text` / stderr | logging |

`-rate`, `-chat-gap`, `-max-wait`, `-autoreply`, `-webhook-filter` and `-queue-jitter` (plus pausing
the send queue) can also be changed while it runs (`PATCH /settings` or the admin panel); such changes are saved and win over the flags until
reset. All flags and commands: [docs/CLI.md](docs/CLI.md).

## Documentation

| | |
|---|---|
| [CLI](docs/CLI.md) | every command and flag, logs, the database file |
| [HTTP API](docs/API.md) | every endpoint with curl examples, filters, events, tokens, rate limiting, webhooks |
| [Sending messages](docs/SENDING.md) | every message type through the API and MCP: text, files, replies, reactions, queue, groups, formatting |
| [OpenAPI spec](docs/openapi.yaml) | for Postman, Insomnia or Bruno; also served live at `/docs` |
| [TUI](docs/TUI.md) | layout, keys and commands |
| [Send queue](docs/QUEUE.md) | bulk sends: pacing, retries, scheduling, canceling, and why it uses SQLite rather than Redis |
| [Bots](docs/BOT.md) | your own bot logic via webhook, polling or Go, verifying signatures, running as a service |
| [Docker](docs/DOCKER.md) | the image, keeping the database in a Docker volume or a host folder, login, backups |
| [Admin panel](docs/ADMIN.md) | `wame admin`: manage a remote server from the terminal, connecting safely |
| [MCP](docs/MCP.md) | the MCP server: HTTP and stdio, tools, safety, client setup |
| [Security review](docs/security/SCOPED-TOKENS.md) | the token system's threat model, findings and fixes, operational advice |
| [Proposed features](docs/proposed-features/README.md) | ideas not built yet: admin panel, image previews, LLM context, drafts approval, rules and state |
| [AGENTS.md](AGENTS.md) | code layout and conventions, for contributors and coding agents |

## License

[MIT](LICENSE): free to use, modify and redistribute, including commercially. The
[whatsmeow](https://github.com/tulir/whatsmeow) library it builds on is MPL-2.0.
