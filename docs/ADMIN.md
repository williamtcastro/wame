# Admin panel (`wame admin`)

`wame admin` is a terminal panel for a wame server that runs somewhere else: a VPS, a home server,
a container. It talks to that server only through the [HTTP API](API.md), so it doesn't need the
database file, doesn't open a WhatsApp connection and can run alongside `serve` or `tui`.

```sh
export WAME_TOKEN=…                          # the server's owner secret, or a token with the admin scope
wame admin -server https://wame.example.com
```

## Tabs

| Tab | Shows | Keys |
|---|---|---|
| **1 Status** | cards for the WhatsApp session, the server (URL, version and uptime, latency, live or polling), tokens (active, expiring within 7 days, unused for 30+ days, revoked), the last 24 h of activity, messages in the last hour (received, sent, busiest chats) and health (send rate, 429s, webhook). While the server isn't linked, the QR to scan instead, or the pairing code after `p` | `p` pair with a phone number, `L` log out (asks first) |
| **2 Chats** | the server's chats with unread counts, and the selected chat's latest messages; or results of a search across every chat | `j`/`k`, `g`/`G` pick a chat, `i` send a message (start it with `@2h`, `@09:00` or `@tomorrow 09:00` to schedule it instead), `/` search, `Esc` clear the search, `p` the person on the Contacts tab |
| **3 Tokens** | every token with colored scopes (`read`, `send`, `admin`), chats, rate, expiry (amber within 7 days), last use (amber ⚠ when unused for 30+ days), its requests and sends in the last 24 h (failed sends in red), open live connections, and creator; revoked and expired ones struck through. A card below shows the selected token in full, including when and from which IP it was last seen | `j`/`k` move, `n` new, `R` rotate, `x` revoke (both ask first) |
| **4 Audit** | the audit log, newest first, with ✓ / ✗ and actions colored by kind; a card below shows the selected entry with its full time and error | `j`/`k`, `Ctrl+D`/`Ctrl+U`, `g`/`G` move, `/` filter, `Esc` clear the filter |
| **5 Health** | the server's version and uptime; for sends, number lookups and auto-replies, a meter of the last minute's use against the limit, the per-chat gap and 429s (or dropped auto-replies) since startup; the webhook's queue, delivered / retried / replayed / given up / dropped counts and its latest failures; the [alerts](#alerts): backend, delay, sent (failed in red), the last one sent and the last error; a "Queue files" card with how many [queue files](QUEUE.md#files) wait on the server's disk, the bytes stored and a meter (in MB) against `queue_files_quota` | `j`/`k` pick a failure, `R` replay it, `t` send a test delivery, `a` send a test alert |
| **6 Settings** | the settings that change without a restart (send rate, chat gap, max wait, auto-replies, webhook filter, queue paused, queue jitter, queue quiet hours, quiet hours on/off, queue time zone): the value in effect, the server's flag, and who changed it and when | `j`/`k` move, `Enter` change (toggles on/off ones), `x` back to the flag |
| **7 Queue** | the [send queue](QUEUE.md): whether it's sending, paused, in [quiet hours](QUEUE.md#quiet-hours) (until when, in which zone), backing off or has no worker, what's waiting and what was sent or failed in the last 7 days, how many sent ones were delivered and read, the quiet hours, the jitter and the last error; below, the items (batch, recipient, type, state, attempts, who queued it, when it's due, when it stops being worth sending if it has a deadline, error; a `✎` after the number marks a message whose text was replaced), never their text; `expired` messages (past their deadline, never sent) count and filter like the other states; TYPE is `text`, or the file's kind and size like `image 1.2 MB`, never its name. A sent item's state shows its receipts like WhatsApp: `✓ sent`, `✓✓ delivered`, and a blue `✓✓ read`. The card also counts the opt-outs: chats that get no queued messages (`GET /optouts`, see [API.md](API.md#get-optouts-post-optouts-delete-optoutschat)); `o` swaps the items for that list (number, name when known, reason such as `keyword STOP` or `added by api`, when; one stored under a `…@lid` id shows that id, without a name); `F` swaps them for the uploaded [queue files](QUEUE.md#files) (`GET /queue/files`): short id, kind, size, when uploaded, when an unused one expires, how many items used it and how many are still open, who uploaded it, and the usage in files and bytes above the list; never a file's name, not even your own | `j`/`k` move, `f` filter by state, `t` reschedule the marked item (`2h`, `09:00`, `tomorrow 09:00`, `2026-10-21T09:00`; a bad time keeps the prompt open with the error), `e` replace its text (an empty prompt: the panel never shows the old one), both only while it's queued, `x` cancel the marked item, `X` cancel its whole batch (asks first), `P` pause / resume, `Q` quiet hours off / on (keeps the window), `o` opt-outs, `F` files. In the files: `j`/`k` move, `/` filter (id, kind, actor), `d` delete the marked one (asks first; refused with 409 while queued messages still use it, so cancel them first), `r` reload, `F` back to the items (`Esc` too, once the filter is cleared); a token that can't list them gets a read-only list. In the opt-outs: `j`/`k` move, `/` filter, `a` add a number or JID, `x` remove the marked one (asks first), `o` back to the items (`Esc` too, once the filter is cleared) |
| **9 Contacts** | the people in the phone's address book and the server's own contact list, merged (`GET /contacts`): unified name, where it comes from (`own`, `book`, `business`, `push`, `number`), number and tags; the profile card on the right shows the selected person (`GET /contacts/{number}`): number, LID, every name with its source, tags and, for an admin, your notes, language and time zone, what WhatsApp last said (about, verified name, photo, business profile), message counts, groups in common, the last 7 days of the queue, opt-out and the tokens limited to them ([details](#contacts)) | `j`/`k` move, `/` filter (name, number, tag), `t` show only some tags, `Esc` clear the filter, then the tags, `e` edit name, tags and notes, `x` delete your fields (asks first), `I` import a CSV (checks first), `E` export to a CSV file, `W` refresh the profile from WhatsApp (2-4 lookups), `Enter` open the chat on Chats |
| **8 Logs** | the server's recent app logs, newest first: time, level (debug faint, info blue, warn amber, error red), message and attributes (dim); a card below shows the selected entry with every attribute. While the tab is open it follows new entries every 3 s ([details](#logs)) | `j`/`k` move, `f` level (all, info, warn, error), `/` filter, `Esc` clear the filter, `F` pause / follow |

In every list, `j`/`k` or the arrows move, `Ctrl+D`/`Ctrl+U` or `PgDn`/`PgUp` page, and `g`/`G` or
`Home`/`End` jump to the top / bottom. Times show in your local time zone, whatever the server's is.

Everywhere: `1`–`9` or `Tab` / `Shift+Tab` switch tabs, `S` switches server (see
[Several servers](#several-servers)), `r` refreshes, `q` or `Ctrl+C` quits. The header shows unread
chats, active tokens, failed actions in the last 24 h (red), a `!` on Health after a 429,
webhook failure or alert error in the last hour, a `•` on Settings while any is changed from its flag and the number
of messages waiting on Queue (`⏸` while paused), the queue's quiet hours while they're switched on
(`☾ quiet 21:00-08:00`, or `☾ quiet until 08:00` in amber while they hold the queue; it never drops
off on a narrow terminal), plus the connection state, the server, live or polling, and the
latency. On a narrow terminal the other tabs shrink to their number and badge; the
connection state always stays. Confirmations, forms and the new token open as dialogs over the current tab.

## Live updates

The panel follows the server's [event stream](API.md#get-events) (`⚡live` in the header): a
status change shows at once, new messages update the Chats tab and the Status tab's
message counts as they arrive, and the Queue tab follows each queued message as it goes out. If the stream drops, it reconnects with backoff (2 s up to 30 s) and
the panel polls every 3 seconds meanwhile; while live, it polls every 15 s for the rest.

When a healthy server loses WhatsApp, gets logged out or stops answering, the terminal **bell**
rings once and the reason shows in red at the bottom.

The message counts keep only the time, chat and sender name of each message (never its text), in
memory, for an hour.

## Chats

The Chats tab reads the same data as `GET /chats` and `GET /messages`: what the server has stored,
newest 60 messages per chat. It's a quick look, not a second client. Viewing a chat **doesn't** mark
it as read on the phone. `i` sends a text to the open chat as the account, through the server's
rate limits, and is audited under the token's name. A message that starts with `@WHEN ` (one word such as `@2h`, `@90m`, `@09:00` or `@2026-10-21T09:00`, or `@tomorrow 09:00`) is queued for then instead (`POST /send` with `send_at`) and answers `scheduled #ID for Tue 21 Oct 09:00`; a time that doesn't parse shows the reason and keeps your text. `@WHEN ` is a schedule prefix only when WHEN looks like a time: it starts with a digit and has a `:` or ends in `s`, `m`, `h` or `d`, or it starts with `tomorrow`; `@maria hi` and `@5511999999999 hi` are sent as typed. A time-looking WHEN that doesn't parse (`@25:00`, `@tomorow`) shows the error and keeps your text. The day of `09:00` / `tomorrow 09:00` is picked on your own calendar and then read on the recipient's clock. A time without an offset is on the recipient's clock. Scheduled messages wait on the Queue tab, where `t` and `e` change them. `/` searches every chat with the server's
full-text search.

Text written by other people (names, messages, file names) is stripped of control characters and
bidi overrides before it's drawn, so it can't redraw or recolor the screen.

## Contacts

The Contacts tab is your own list of people, kept on the server (see
[the plan](proposed-features/CONTACTS.md)): a name, tags and notes per person, merged with the
phone's address book. Your name wins over every other one, in the panel, the TUI, the API and MCP.
Nothing is ever written to WhatsApp or to the phone.

- **Edit** (`e`): a form with the name, the tags (separated by spaces) and the notes. Only what you
  changed is sent (`PATCH`), and tags go as additions and removals, so another panel's edit in the
  meantime isn't undone. An empty field clears it. The notes field shows up once the card has
  loaded, and only when the notes fit on one line as stored: notes with line breaks (or tabs,
  runs of spaces, control characters) are left out of the form, so saving it can't flatten them;
  change those through the API or a CSV export and import. The card folds the notes onto one
  line and wraps them to at most 3 lines.
- **Profile card**: loads when you select someone and never asks WhatsApp, so moving through the
  list costs nothing. It shows the number and the LID (WhatsApp's hidden ID for the person, once
  known), every name with where it comes from, your own fields, then what WhatsApp said last time it
  was asked: on WhatsApp or not, about, verified business name, the photo's id (or that it's hidden
  from you; the photo's link is only in the API answer), the business profile, when that was
  (`stale` once it's older than a day) and the last error. Below that, from the server's own tables:
  messages sent, received and unread, first and last, groups in common, queue items of the last 7
  days, whether they opted out, and the tokens limited to their chat. A token without the admin
  scope gets only the names, tags and whether they're on WhatsApp.
- **Refresh** (`W`): asks WhatsApp about the selected person now (`GET /contacts/{number}?refresh=1`,
  admin only). It costs 2 to 4 lookups from the server's lookup budget and runs at most once per
  person every 5 minutes: a second `W` inside that says when the next one may run. Over the budget
  the server answers 429 and the panel says when to try again; while the server isn't logged in,
  it says so. The card shows the answer. A WhatsApp error is reported as new only when WhatsApp
  gave it to this `W`. A server from before profiles shows names, tags and notes (still editable)
  and asks for an update for the rest.
- **Delete** (`x`): removes your name, tags, notes, language and time zone for the person marked
  when you pressed it; the address book and WhatsApp names stay.
- **Import** (`I`): a local CSV file with a `number` column and any of `name`, `tags`, `notes`,
  `language`, `time_zone` (up to 5000 rows and 4 MB). The server checks it first (`dry_run`) and
  the panel shows how many people would be added, updated or left as they are; `y` sends the same
  file, as it was checked. A column present overwrites that field; an empty cell clears it. A bad
  line stores nothing and names the line.
- **Export** (`E`): saves your list as CSV in the import's columns to a new file (mode 0600; `~/`
  works). An existing file is never overwritten.
- **Tags** (`t`): show only people with every tag typed (`clients vip`); `Tab` completes the known tag
  being typed (each one, not only the first), and the prompt lists them with their counts. An
  empty prompt shows everyone again.
- **Filter** (`/`): narrows the loaded list as you type. The tab loads the first 5000 people; when
  there are more, the header says so and `Enter` on the filter asks the server
  (`GET /contacts?q=`), so people past the first 5000 are found too.
- `p` on the Chats tab jumps here with that chat's person selected, even when nothing is stored
  about them yet, so `e` names them. Groups aren't contacts.

The tab loads when you open it, after each of its own changes and on `r`; it doesn't poll. Names,
tags and notes are sanitized like messages. A token without the admin scope sees the list and the
names but no notes, and `e`, `x`, `I`, `E` and `W` say they need the admin scope; a token limited to some
chats sees only those and can't filter by tag. A server older than the contact list says so
instead of failing.

## Rotating a token

`R` on a token creates a replacement with the same scopes, chat allowlist and rate, and the same
lifetime it was created with (counted from now). It's named after the old one with a number
(`support-bot` → `support-bot-2` → `support-bot-3`; revoked names stay reserved). The new token is
shown once; the old one, and every token it created, is revoked right after. If creating fails,
nothing is revoked; if revoking fails, the new token still works and the panel says so.

Tokens nobody has used for 30 days (counting from creation if never used) are flagged in amber:
candidates to revoke.

## Changing settings

The Settings tab uses [`PATCH /settings`](API.md#get-settings-patch-settings): a change applies on
the server at once (the next send already uses the new rate), and is saved there, so it **also wins
over the server's flags after a restart** until you reset it with `x`. The FLAG column shows what
the server was started with. Bad values are refused with the reason and change nothing. Every
change is in the audit log as `settings`.

## Webhook test and replay

On the Health tab, `t` sends a signed test delivery (`X-Wame-Event: test`, no message) and shows
how the receiver answered, which is handy after changing its code or the secret. `R` sends the
marked failed delivery again, once, with the same delivery id; it's marked `replayed` when the
receiver accepts it. Only deliveries in the list can be replayed. See
[`POST /webhook/test`, `POST /webhook/replay`](API.md#post-webhooktest-post-webhookreplay).

## Alerts

The Health tab's Alerts card shows what the server's [alerts](API.md#alerts) (`-alert-gotify` /
`WAME_ALERT_GOTIFY`) have done since it started: the backend, how long a disconnection must last
before it alerts, how many were sent or failed, the last one's title and the last error. `a` sends a
low-priority test alert ([`POST /alerts/test`](API.md#alerts)) and says whether it went through; it's in the
audit log as `alert_test`. A server without alerts configured says so; an older one that can't send
test alerts asks to be updated.

## Logs

The Logs tab reads [`GET /logs`](API.md#get-logs): the newest 1000 app log records the server keeps
in memory since it started, at its log level (debug lines only when it runs with `-debug`). They're
the same lines `docker logs` or `-log-file` show, with secrets taken out (tokens, the webhook URL and
secret, anything under a key like `password` or `authorization`); message text is never logged in
the first place. whatsmeow's own protocol logs go to the server's stdout and aren't here.

`f` changes the lowest level shown and reloads; `/` filters what's loaded by message or attribute.
Following polls `GET /logs?since=` every 3 seconds while the tab is open (not over the event stream)
and adds new entries on top; if you've moved down the list, the selection stays on the same entry.
`F` pauses that, and `F` again catches up. Leaving the tab stops the polling; coming back catches up
the same way, keeping the selection. When the server restarts (its start time on the Health tab
changes), the list reloads from the newest entry, since the new server numbers its log from 1 again.

## Logging in a server remotely

When the server isn't linked yet, the Status tab draws the QR the server is offering, so you can
scan it from your phone without shell access to the box. The QR rotates every ~20 seconds and the
panel follows. Or press `p`, type the phone number with its country code, and enter the 8-character
code it shows under WhatsApp → Linked devices → Link a device → Link with phone number.

`L` logs out: it unlinks the device and deletes the session on the server, which then needs a new
login. Both are recorded in the audit log under the token's name (`api` for the owner secret).

## Creating tokens

`n` opens a form: name, scopes (`read`, `send`, `admin`), an optional chat allowlist (numbers or
JIDs), the per-token send rate (default 10/min, `0` = only the global limit) and an expiry (default
`720h`; empty = never). The token is shown **once**; press `c` to copy it (system clipboard, or the
terminal's OSC 52 clipboard over SSH). Any other key closes it for good.

The server's rules apply as in [Tokens and scopes](API.md#tokens-and-scopes): with an admin token
(not the owner secret), the new token can't have the `admin` scope, can't outlive yours, and is
revoked along with it. The same goes for rotating.

## Connecting

| Flag | Env | Default | |
|---|---|---|---|
| `-server URL` | `WAME_SERVER` | `http://127.0.0.1:8080` | the server's base URL; a path prefix behind a reverse proxy works (`https://example.com/wame`). Without a scheme, `https://` is assumed |
| `-token TOKEN` | `WAME_TOKEN` | | the owner secret or an admin token. Prefer the env variable: flags show up in `ps` |
| `-profile NAME` | | | use the saved server `NAME` (below) |
| `-insecure` | `WAME_INSECURE` (in a file) | `false` | allow plain `http://` to a non-loopback address |

### Saving the server and token

So you don't have to export them each time, `wame admin` also reads `WAME_SERVER`, `WAME_TOKEN` and
`WAME_INSECURE` from env files (`KEY=value` lines; other keys are ignored). It takes each value from
the first place that has it:

1. the `-server` / `-token` flags;
2. the environment;
3. `.env` in the current directory (the same file Docker Compose uses; see
   [`.env.example`](../.env.example));
4. `~/.config/wame/admin.env` (or `$XDG_CONFIG_HOME/wame/admin.env`), for use from any folder.

```sh
mkdir -p ~/.config/wame
printf 'WAME_SERVER=https://wame.example.com\nWAME_TOKEN=wame_…\n' > ~/.config/wame/admin.env
chmod 600 ~/.config/wame/admin.env
wame admin
```

A `WAME_SERVER` (and `WAME_INSECURE`) from a file is only used with a `WAME_TOKEN` from **that same
file**. If the token comes from somewhere else (say, `WAME_TOKEN` exported in your shell) and a
`.env` in the folder you're in names a server, wame refuses rather than send your token to it. Fix
it by putting both in one file, or by passing `-server`. Keep these files private (`chmod 600`):
they hold the token.

### Several servers

Save each server as its own file in `~/.config/wame/servers/`, named after it:

```sh
mkdir -p ~/.config/wame/servers && chmod 700 ~/.config/wame/servers
printf 'WAME_SERVER=https://wame.example.com\nWAME_TOKEN=wame_…\n' > ~/.config/wame/servers/vps.env
printf 'WAME_SERVER=http://10.0.0.5:8080\nWAME_TOKEN=wame_…\nWAME_INSECURE=true\n' > ~/.config/wame/servers/home.env
chmod 600 ~/.config/wame/servers/*.env
```

Each file needs both `WAME_SERVER` and `WAME_TOKEN`; files missing either are skipped.
`wame admin -profile vps` opens one directly. Without `-profile`, and with no token found in the
places above, the panel starts on a list of them. Inside the panel, `S` opens the list and switches
without restarting; everything loaded from the previous server is dropped, including answers still
on their way.

### Keeping the token safe

The token goes in every request, so the panel refuses plain `http://` to anything but
`localhost`/`127.0.0.1`/`::1`. Two good setups:

- **SSH tunnel** (nothing exposed): keep the server on `127.0.0.1:8080` and run
  `ssh -N -L 8080:127.0.0.1:8080 you@server`, then `wame admin` with the default `-server`.
- **HTTPS reverse proxy** (Caddy, nginx, Traefik) in front of `wame serve -addr 127.0.0.1:8080`,
  then `wame admin -server https://wame.example.com`.

Use `-insecure` (or `WAME_INSECURE=true` in a server's file) only on a network you trust (a VPN,
Tailscale): the token would otherwise be readable by anyone on the path.

A token without the admin scope can still open the panel and see the Status and Chats tabs (without
the QR), but the Tokens, Audit, Health and Logs tabs say it needs the admin scope, and so do the
opt-outs: the Queue card leaves out their count and the panel stops asking for them until `r`, `o`
or a tab switch. On the Contacts tab it can read names and tags but not notes, and can't change
anything. Prefer a dedicated
admin token with an expiry over the owner secret, so it can be revoked (or rotated) on its own:

```sh
wame token create ops-laptop -scopes admin -expires 720h   # on the server
```

The Health tab, the usage columns, webhook test and replay, the Settings, Queue and Logs tabs and the opt-outs
need a recent server (`GET /stats`, `/settings`, `/webhook/*`, `/queue`, `/queue/files`, `/logs`, `/optouts`), and so do the
Contacts tab and `p` (`/contacts/tags`, `/contacts/{number}`), and the full profile card and `W`
(`/contacts/{number}?refresh=1`); an older one says so instead of failing.

## What it doesn't do (yet)

No sending files, reactions or replies (use `wame tui` on the server, or the API), no changing the
webhook URL or secret, and no history of webhook deliveries or usage beyond what the server keeps
in memory since it started (the last 20 failures, 24 hours of usage). See the [web admin panel proposal](proposed-features/ADMIN-PANEL.md) for the
server-side pieces those need.
