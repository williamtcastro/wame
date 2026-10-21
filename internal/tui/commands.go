package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/williamtcastro/wame/internal/bot"
	"github.com/williamtcastro/wame/internal/store"
	"github.com/williamtcastro/wame/internal/termui"
)

const tuiHelp = `Moving around
  Tab / Shift+Tab     cycle focus: input → chats → messages
  Ctrl+J / Ctrl+K     next / previous chat (from anywhere)
  ↑ (empty input)     select messages
  Ctrl+D / Ctrl+U     scroll half a page down / up
  Ctrl+Y              copy the open chat's number (group ID for groups)
  PgUp / PgDn         scroll
  Esc                 cancel / close / back to the input

Chat list (focused)
  type                filter chats by name
  ↑ ↓  Enter          pick and open

Selected message
  j k  ↑ ↓           move               g  G   first / last
  r  reply           e  edit (yours, 20 min)
  d d  delete        s  save media to ~/Downloads
  1-6  react 👍 ❤️ 😂 😮 😢 🙏     0  remove reaction
  other letters      back to typing

Commands
  /to <name|number|jid>     open a chat
  /search <words>           search every chat (↑↓ pick, Enter jumps to it, Esc closes)
  /copy                     copy this chat's number (same as Ctrl+Y)
  /all                      live feed of every chat
  /contacts [search]        your address book
  /name [text]              your name for the open chat (alone: clear it)
  /tag clients vip          set its tags; /tag +vip -old edits them, alone clears
  /groups                   your groups
  /send <number|jid> <msg>  one-off message
  /file <path> [caption]    send a file to the open chat
  /later <when> <msg>       queue a message to the open chat (2h, 09:00, tomorrow 09:00, 2026-10-21T09:00)
  /sendat <number|jid> <when> <msg>   same, for a number or group
  /scheduled                queued messages of the open chat (all when none is open)
  /reschedule <id> <when>   move a queued message
  /unschedule <id>          cancel a queued message
  /read                     mark the open chat as read
  /pair <phone>             log in with a pairing code
  /help  /quit

Typing a command, Tab completes it (and names after /to, numbers after /send,
paths after /file, the current name after /name), ↑ ↓ or Ctrl+J / Ctrl+K cycle
the matches.`

var tuiCommands = []struct{ name, args, desc string }{
	{"/to", "<name|number>", "open a chat"},
	{"/all", "", "every chat, live"},
	{"/contacts", "[search]", "address book"},
	{"/name", "[text]", "your name for this chat"},
	{"/tag", "<tags|+add -remove>", "tags for this chat"},
	{"/groups", "", "your groups"},
	{"/send", "<number> <text>", "one-off message"},
	{"/file", "<path> [caption]", "send a file here"},
	{"/later", "<when> <text>", "queue a message here"},
	{"/sendat", "<number> <when> <text>", "queue a message"},
	{"/scheduled", "", "queued messages"},
	{"/reschedule", "<id> <when>", "move a queued message"},
	{"/unschedule", "<id>", "cancel a queued message"},
	{"/search", "<words>", "search all messages"},
	{"/copy", "", "copy this chat's number"},
	{"/read", "", "mark this chat read"},
	{"/pair", "<phone>", "pairing-code login"},
	{"/help", "", "keys and commands"},
	{"/quit", "", "exit"},
}

func (m *model) submit(line string) tea.Cmd {
	if line == "" {
		return nil
	}
	if strings.HasPrefix(line, "/") {
		return m.command(line)
	}
	if m.active == "" {
		return m.setNotice("no chat open: press Tab to pick one, or /to <name>", true)
	}
	chat, reply, editing := m.active, m.replyTo, m.editing
	m.replyTo, m.editing = nil, nil
	m.layout()
	m.render(true)
	if editing != nil {
		return m.do(func(ctx context.Context) (string, error) {
			_, err := m.bot.Edit(ctx, actor, chat, editing.ID, line)
			return "", err
		})
	}
	opt := bot.SendOptions{}
	if reply != nil {
		opt.ReplyTo = reply.ID
	}
	return m.do(func(ctx context.Context) (string, error) {
		_, err := m.bot.SendText(ctx, actor, chat, line, opt)
		return "", err
	})
}

func (m *model) command(line string) tea.Cmd {
	cmd, rest, _ := strings.Cut(line, " ")
	rest = strings.TrimSpace(rest)
	switch cmd {
	case "/quit", "/q":
		return tea.Quit
	case "/help":
		return func() tea.Msg { return panelMsg{"Help", tuiHelp} }
	case "/all":
		return m.open("")
	case "/to":
		return m.openByName(rest)
	case "/contacts":
		return func() tea.Msg {
			contacts, err := m.bot.Contacts(context.Background(), bot.ContactQuery{Q: rest})
			if err != nil {
				return noticeMsg{err.Error(), true}
			}
			if len(contacts) == 0 {
				return noticeMsg{"no contacts found (they sync from your phone after connecting)", true}
			}
			return panelMsg{fmt.Sprintf("Contacts (%d) · /to <name> to open", len(contacts)), formatContacts(contacts, 0)}
		}
	case "/name", "/tag":
		if m.active == "" {
			return m.setNotice("no chat open: /to <name> first", true)
		}
		in, err := contactInput(cmd, rest)
		if err != nil {
			return m.setNotice(err.Error(), true)
		}
		chat := m.active
		return func() tea.Msg {
			v, err := m.bot.PatchContact(context.Background(), actor, chat, in)
			if err != nil {
				return noticeMsg{err.Error(), true}
			}
			return contactMsg{chat, v}
		}
	case "/groups":
		return func() tea.Msg {
			groups, err := m.bot.Client.GetJoinedGroups(context.Background())
			if err != nil {
				return noticeMsg{err.Error(), true}
			}
			var sb strings.Builder
			for _, g := range groups {
				fmt.Fprintf(&sb, "%s  %s\n", styleDim.Render(g.JID.String()), safeLine(g.Name))
			}
			return panelMsg{fmt.Sprintf("Groups (%d) · /to <jid> to open", len(groups)), sb.String()}
		}
	case "/send":
		to, text, _ := strings.Cut(rest, " ")
		return m.do(func(ctx context.Context) (string, error) {
			_, err := m.bot.SendText(ctx, actor, to, text, bot.SendOptions{})
			return "sent to " + to, err
		})
	case "/file":
		path, caption, _ := strings.Cut(rest, " ")
		if m.active == "" {
			return m.setNotice("no chat open", true)
		}
		chat, path := m.active, termui.ExpandHome(path)
		return m.do(func(ctx context.Context) (string, error) {
			data, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			_, err = m.bot.SendFile(ctx, actor, chat, data, filepath.Base(path), caption, bot.SendOptions{})
			return "", err
		})
	case "/later":
		if m.active == "" {
			return m.setNotice("no chat open", true)
		}
		return m.schedule(m.active, rest)
	case "/sendat":
		to, spec, _ := strings.Cut(rest, " ")
		return m.schedule(to, strings.TrimSpace(spec))
	case "/scheduled":
		return m.listScheduled()
	case "/reschedule":
		idText, when, _ := strings.Cut(rest, " ")
		id, err := strconv.ParseInt(idText, 10, 64)
		if err != nil || strings.TrimSpace(when) == "" {
			return m.setNotice("usage: /reschedule <id> <when>", true)
		}
		return m.do(func(ctx context.Context) (string, error) {
			at, err := bot.ParseSendTime(strings.TrimSpace(when), time.Now().In(m.bot.QueueLocation()), true)
			if err != nil {
				return "", err
			}
			it, err := m.bot.EditQueued(actor, id, store.OutboxFilter{Actor: actor}, bot.QueueEdit{SendAt: at})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("rescheduled #%d for %s", it.ID, dueText(it.Due)), nil
		})
	case "/unschedule":
		id, err := strconv.ParseInt(rest, 10, 64)
		if err != nil {
			return m.setNotice("usage: /unschedule <id>", true)
		}
		return m.do(func(ctx context.Context) (string, error) {
			if _, err := m.bot.CancelQueued(actor, id, store.OutboxFilter{Actor: actor}); err != nil {
				return "", err
			}
			return fmt.Sprintf("unscheduled #%d", id), nil
		})
	case "/search":
		if rest == "" {
			return m.setNotice("type what to search for, e.g. /search bom dia", true)
		}
		return func() tea.Msg {
			results, err := m.bot.Store.Query(store.Filter{Text: rest}, searchLimit, false)
			if err != nil {
				return noticeMsg{"search: " + err.Error(), true}
			}
			return searchMsg{rest, results}
		}
	case "/copy":
		return m.copyNumber()
	case "/read":
		if m.active == "" {
			return m.setNotice("no chat open", true)
		}
		chat := m.active
		return m.do(func(ctx context.Context) (string, error) {
			n, err := m.bot.MarkRead(ctx, actor, chat, nil)
			return fmt.Sprintf("marked %d messages as read", n), err
		})
	case "/pair":
		return m.do(func(ctx context.Context) (string, error) {
			code, err := m.bot.PairPhone(ctx, actor, rest)
			return "pairing code " + code + " (WhatsApp → Linked devices → Link with phone number)", err
		})
	}
	return m.setNotice("unknown command "+cmd+" (try /help)", true)
}

// contactMsg is a person's contact after /name or /tag changed it.
type contactMsg struct {
	chat string
	v    bot.ContactView
}

// contactInput turns /name or /tag into a PATCH: "/tag a b" sets the tags, "/tag +a -b" edits them,
// and either command alone clears.
func contactInput(cmd, rest string) (bot.ContactInput, error) {
	if cmd == "/name" {
		return bot.ContactInput{Name: &rest}, nil
	}
	var in bot.ContactInput
	set := []string{}
	for _, f := range strings.Fields(rest) {
		switch {
		case strings.HasPrefix(f, "+"):
			in.AddTags = append(in.AddTags, f[1:])
		case strings.HasPrefix(f, "-"):
			in.RemoveTags = append(in.RemoveTags, f[1:])
		default:
			set = append(set, f)
		}
	}
	switch {
	case len(in.AddTags)+len(in.RemoveTags) == 0:
		in.Tags = &set
	case len(set) > 0:
		return in, errors.New("either list every tag (/tag clients vip) or change some (/tag +vip -old), not both")
	}
	return in, nil
}

// applyContact shows the new name and tags of the open chat; the chat list follows on reload.
func (m *model) applyContact(msg contactMsg) tea.Cmd {
	if msg.chat == m.active {
		m.activeName, m.activeTags = safeLine(msg.v.Display), termui.SafeLines(msg.v.Tags)
	}
	notice := "no tags"
	if len(msg.v.Tags) > 0 {
		notice = "tags: " + strings.Join(msg.v.Tags, " ")
	}
	return tea.Batch(m.setNotice(msg.v.Display+" · "+notice, false), m.chatsChanged())
}

func (m *model) openByName(q string) tea.Cmd {
	m.openSeq++
	seq := m.openSeq
	return func() tea.Msg {
		ctx := context.Background()
		if _, err := bot.ParseRecipient(q); err == nil {
			jid, err := m.bot.Resolve(ctx, q)
			if err != nil {
				return noticeMsg{err.Error(), true}
			}
			return m.load(seq, m.bot.PhoneJID(jid).String(), 0)()
		}
		contacts, err := m.bot.Contacts(ctx, bot.ContactQuery{Q: q})
		switch {
		case err != nil:
			return noticeMsg{err.Error(), true}
		case len(contacts) == 0:
			return noticeMsg{"no contact matches " + q, true}
		case len(contacts) == 1:
			return m.load(seq, contacts[0].JID, 0)()
		}
		return panelMsg{fmt.Sprintf("%d contacts match %q · be more specific", len(contacts), q), formatContacts(contacts, 30)}
	}
}

// markRead marks what's on screen as read: locally right away (so badges clear even offline) and,
// when connected, with read receipts to the senders.
func (m *model) markRead(msgs []store.Message) tea.Cmd {
	var ids []string
	var last int64
	for _, x := range msgs {
		if x.Chat != m.active {
			continue
		}
		last = max(last, x.Seq)
		if !x.FromMe && x.Type != "reaction" {
			ids = append(ids, x.ID)
		}
	}
	if len(ids) > receiptsMax {
		ids = ids[len(ids)-receiptsMax:]
	}
	if last == 0 {
		return nil
	}
	chat, online := m.active, m.status.LoggedIn
	return func() tea.Msg {
		moved := m.bot.Store.MarkReadUpTo(chat, last)
		if online && len(ids) > 0 {
			_, _ = m.bot.MarkRead(context.Background(), actor, chat, ids)
		}
		if moved {
			return chatsStaleMsg{}
		}
		return nil
	}
}

func (m *model) save(ctx context.Context, target store.Message) (string, error) {
	data, full, err := m.bot.DownloadMedia(ctx, target.Seq)
	if err != nil {
		return "", err
	}
	dir, err := os.UserHomeDir()
	if err == nil {
		dir = filepath.Join(dir, "Downloads")
	}
	if _, err := os.Stat(dir); err != nil {
		dir = "."
	}
	name := filepath.Base(safe(full.MediaFileName()))
	ext := filepath.Ext(name)
	for i := range 100 {
		path := filepath.Join(dir, name)
		if i > 0 {
			path = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), i, ext))
		}
		err := termui.WriteNew(path, data)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		return "saved " + path, nil
	}
	return "", fmt.Errorf("%s: too many files with that name in %s", name, dir)
}

// dueText is a send time the way notices show it.
func dueText(t time.Time) string { return t.Local().Format("Mon 02 Jan 15:04 MST") }

// schedule queues text for to at the time spec starts with, through the send queue.
func (m *model) schedule(to, spec string) tea.Cmd {
	if to == "" || strings.TrimSpace(spec) == "" {
		return m.setNotice("usage: /later <when> <text>  or  /sendat <number> <when> <text>", true)
	}
	at, text, err := bot.CutSendTime(spec, time.Now().In(m.bot.QueueLocation()))
	switch {
	case err != nil:
		return m.setNotice(err.Error(), true)
	case text == "":
		return m.setNotice("usage: /later <when> <text>  or  /sendat <number> <when> <text>", true)
	}
	return m.do(func(ctx context.Context) (string, error) {
		res, err := m.bot.Queue(ctx, bot.QueueOptions{Actor: actor}, []bot.QueueRequest{{To: to, Text: text, SendAt: at}})
		if err != nil {
			return "", err
		}
		if len(res.Items) == 0 {
			return "", errors.New("nothing was queued")
		}
		it := res.Items[0]
		return fmt.Sprintf("scheduled #%d for %s", it.ID, dueText(it.Due)), nil
	})
}

// listScheduled shows the TUI's queued messages: the open chat's, or all of them with no chat open.
func (m *model) listScheduled() tea.Cmd {
	chat := m.active
	return func() tea.Msg {
		items, err := m.bot.Store.ListOutbox(store.OutboxFilter{Actor: actor, Status: store.OutboxQueued}, 500)
		if err != nil {
			return noticeMsg{err.Error(), true}
		}
		var sb strings.Builder
		n := 0
		for _, it := range items {
			if chat != "" && it.RecipientKey != chat && it.Chat != chat && it.To != chat {
				continue
			}
			n++
			text := []rune(safeLine(it.Text))
			if len(text) > 40 {
				text = append(text[:39], '…')
			}
			fmt.Fprintf(&sb, "#%d  %s  %s", it.ID, dueText(it.Due), string(text))
			if chat == "" {
				fmt.Fprintf(&sb, "  %s", styleDim.Render("→ "+safeLine(it.To)))
			}
			sb.WriteByte('\n')
		}
		if n == 0 {
			return noticeMsg{"nothing scheduled", false}
		}
		return panelMsg{fmt.Sprintf("Scheduled (%d) · /reschedule <id> <when>, /unschedule <id>", n), sb.String()}
	}
}
