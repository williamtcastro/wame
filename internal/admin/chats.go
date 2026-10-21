package admin

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/williamtcastro/wame/internal/bot"
	"github.com/williamtcastro/wame/internal/store"
	"github.com/williamtcastro/wame/internal/termui"
)

const (
	chatListWidth = 37
	chatsDebounce = 200 * time.Millisecond
)

type chatsReloadMsg struct{}

// chatsChanged reloads the chat list once per chatsDebounce, however many messages stream in.
func (m *model) chatsChanged() tea.Cmd {
	return m.chatsReload.Schedule(chatsDebounce, chatsReloadMsg{})
}

func (m *model) loadChats() tea.Cmd {
	return m.call(func(ctx context.Context, c *Client) tea.Msg {
		chats, err := c.Chats(ctx, chatsLimit)
		return chatsMsg{chats, err}
	})
}

func (m *model) loadMessages(chat string) tea.Cmd {
	if chat == "" {
		return nil
	}
	return m.call(func(ctx context.Context, c *Client) tea.Msg {
		msgs, err := c.Messages(ctx, chat, "", messagesLimit)
		return messagesMsg{chat: chat, msgs: msgs, err: err}
	})
}

// applyChats keeps the same chat selected when the list reorders, and opens it if it changed.
func (m *model) applyChats(msg chatsMsg) tea.Cmd {
	m.chatsErr = msg.err
	if msg.err != nil {
		return nil
	}
	selected := m.selectedChat()
	m.chats = msg.chats
	if i := slices.IndexFunc(m.chats, func(c store.Chat) bool { return c.JID == selected }); i >= 0 {
		m.chatSel = i
	}
	m.chatSel = clamp(m.chatSel, len(m.chats))
	if chat := m.selectedChat(); chat != m.msgsChat && m.tab == tabChats {
		return m.loadMessages(chat)
	}
	return nil
}

func (m *model) applyMessages(msg messagesMsg) {
	if msg.query != "" {
		if msg.query != m.search {
			return
		}
		m.notice = ""
		m.searchHits, m.msgsErr = msg.msgs, msg.err
		return
	}
	if msg.chat != m.selectedChat() {
		return
	}
	m.msgsChat, m.msgs, m.msgsErr = msg.chat, msg.msgs, msg.err
	slices.Reverse(m.msgs)
}

func (m *model) selectedChat() string {
	if m.chatSel < len(m.chats) {
		return m.chats[m.chatSel].JID
	}
	return ""
}

// onMessage folds a streamed message into the Chats tab.
func (m *model) onMessage(msg store.Message) tea.Cmd {
	if msg.Chat == m.msgsChat && !slices.ContainsFunc(m.msgs, func(x store.Message) bool { return x.Seq == msg.Seq }) {
		m.msgs = append(m.msgs, msg)
		if len(m.msgs) > messagesLimit {
			m.msgs = m.msgs[len(m.msgs)-messagesLimit:]
		}
	}
	if m.tab == tabChats {
		return m.chatsChanged()
	}
	return nil
}

func (m *model) chatsKey(k string) tea.Cmd {
	if sel, ok := termui.ListMove(k, m.chatSel, len(m.chats), m.bodyHeight()/2); ok {
		m.chatSel = sel
		if chat := m.selectedChat(); chat != m.msgsChat {
			return m.loadMessages(chat)
		}
		return nil
	}
	switch k {
	case "i", "enter":
		if m.msgsChat == "" {
			return nil
		}
		m.startPrompt(modeCompose, "message to "+m.chatName(m.msgsChat)+" (sends as the account; @2h or @tomorrow 09:00 first schedules it)")
	case "/":
		m.startPrompt(modeSearch, "search every chat")
		m.prompt.SetValue(m.search)
		m.prompt.CursorEnd()
	case "esc":
		m.search, m.searchHits = "", nil
	}
	return nil
}

func (m *model) chatName(jid string) string {
	for _, c := range m.chats {
		if c.JID == jid && c.Name != "" {
			return safe(c.Name)
		}
	}
	return displayJID(safe(jid))
}

func (m *model) viewChats() string {
	if m.chatsErr != nil {
		return m.errCard("Chats", m.chatsErr, "")
	}
	if len(m.chats) == 0 {
		return indent(card("Chats", m.cardWidth(cardNarrow), styleDim.Render("No chats stored on the server yet.")), " ")
	}
	h := m.bodyHeight()
	listW := chatListWidth
	var list []string
	start, end := window(m.chatSel, len(m.chats), h)
	for i := start; i < end; i++ {
		c := m.chats[i]
		badge := "   "
		if c.Unread > 0 {
			badge = styleAccent.Render(fmt.Sprintf("%2d ", min(c.Unread, 99)))
		}
		nameW := listW - 5 - whenWidth
		line := badge + pad(fit(safe(cmp.Or(c.Name, displayJID(c.JID))), nameW), nameW) + " " + styleDim.Render(when(c.LastAt))
		if i == m.chatSel {
			line = lipgloss.NewStyle().Background(colSel).Width(listW).Render(styleAccent.Render("▌") + line)
		} else {
			line = " " + line
		}
		list = append(list, line)
	}
	left := lipgloss.NewStyle().Width(listW).Height(h).Render(strings.Join(list, "\n"))
	sep := styleFaint.Render(strings.Repeat("│\n", h-1) + "│")
	paneW := max(m.width-listW-4, 20)
	var right string
	if m.search != "" {
		right = m.viewSearch(paneW, h)
	} else {
		right = m.viewMessages(paneW, h)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, left, " ", sep, " ", right)
}

func (m *model) viewMessages(w, h int) string {
	chat := m.selectedChat()
	title := fit(styleBold.Render(m.chatName(chat))+"  "+styleDim.Render(safe(chat)), w)
	if m.msgsErr != nil {
		return title + "\n\n" + errText(m.msgsErr)
	}
	if m.msgsChat != chat {
		return title + "\n\n" + styleDim.Render("loading…")
	}
	var lines []string
	for _, msg := range m.msgs {
		lines = append(lines, m.messageLines(msg, w)...)
	}
	room := h - 3
	if len(lines) > room {
		lines = lines[len(lines)-room:]
	}
	if len(lines) == 0 {
		lines = []string{styleDim.Render("No messages stored for this chat.")}
	}
	foot := styleFaint.Render("Reading here doesn't mark anything as read on the phone.")
	return title + "\n" + styleFaint.Render(strings.Repeat("─", w)) + "\n" + strings.Join(lines, "\n") + strings.Repeat("\n", max(room-len(lines), 0)) + "\n" + foot
}

func (m *model) messageLines(msg store.Message, w int) []string {
	who := styleAccent.Render("me")
	if !msg.FromMe {
		who = styleInfo.Render(fit(safe(cmp.Or(msg.PushName, displayJID(msg.Sender))), 18))
	}
	prefix := styleDim.Render(stamp(msg.Timestamp, "15:04")) + " " + who + " "
	body := messageText(msg)
	indentW := ansi.StringWidth(prefix)
	wrapped := strings.Split(lipgloss.NewStyle().Width(max(w-indentW, 10)).Render(body), "\n")
	out := []string{prefix + wrapped[0]}
	for _, l := range wrapped[1:] {
		out = append(out, strings.Repeat(" ", indentW)+l)
	}
	return out
}

func messageText(msg store.Message) string {
	text := safe(msg.Text)
	if msg.Type != "text" {
		label := "[" + msg.Type
		if msg.Media != nil && msg.Media.FileName != "" {
			label += " " + safe(msg.Media.FileName)
		}
		label += "]"
		text = strings.TrimSpace(styleDim.Render(label) + " " + text)
	}
	switch {
	case msg.Deleted:
		return styleFaint.Render("message deleted")
	case msg.Edited:
		text += styleFaint.Render(" (edited)")
	}
	return text
}

func (m *model) viewSearch(w, h int) string {
	title := fit(styleBold.Render("Search: "+safe(m.search))+"  "+styleDim.Render(plural(len(m.searchHits), "match")+" · esc clears"), w)
	if m.msgsErr != nil {
		return title + "\n\n" + errText(m.msgsErr)
	}
	lines := []string{title, styleFaint.Render(strings.Repeat("─", w))}
	for _, msg := range m.searchHits {
		if len(lines) >= h {
			break
		}
		head := styleDim.Render(when(msg.Timestamp)) + " " + styleInfo.Render(fit(m.chatName(msg.Chat), 20)) + " "
		lines = append(lines, ansi.Truncate(head+messageText(msg), w, "…"))
	}
	return strings.Join(lines, "\n")
}

// safe sanitizes text that other people control (names, messages, file names, token names, server
// and webhook errors) to one clean line before it's rendered; see termui.SafeLine.
func safe(s string) string { return termui.SafeLine(s) }

// splitSendAt reads a leading "@WHEN " off a composed message (see ADMIN.md).
func splitSendAt(v string, now time.Time) (when bot.SendTime, text string, scheduled bool, err error) {
	rest, ok := strings.CutPrefix(v, "@")
	if !ok || !looksLikeTime(rest) {
		return bot.SendTime{}, v, false, nil
	}
	when, text, err = bot.CutSendTime(rest, now)
	if err == nil && text == "" {
		err = errors.New("nothing to send after the time")
	}
	return when, text, true, err
}

// looksLikeTime tells a time-looking WHEN from a mention like "@maria".
func looksLikeTime(s string) bool {
	word, _, _ := strings.Cut(s, " ")
	if strings.HasPrefix(strings.ToLower(word), "tomor") {
		return true
	}
	return word != "" && word[0] >= '0' && word[0] <= '9' && (strings.Contains(word, ":") || strings.ContainsAny(word[len(word)-1:], "smhd"))
}

// sendCompose sends the compose text, or schedules it when it starts with @WHEN.
func (m *model) sendCompose(chat, v string) tea.Cmd {
	when, text, scheduled, err := splitSendAt(v, time.Now())
	if err != nil {
		m.mode = modeCompose
		m.prompt.Focus()
		m.setNotice(err.Error(), true)
		return nil
	}
	m.setNotice("sending…", false)
	key := "admin-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if !scheduled {
		return m.call(func(ctx context.Context, c *Client) tea.Msg {
			_, err := c.Send(ctx, chat, text, key)
			return sentMsg{chat: chat, err: err}
		})
	}
	return m.call(func(ctx context.Context, c *Client) tea.Msg {
		out, err := c.SendAt(ctx, chat, text, key, when.String(), "")
		if err != nil || len(out.Items) == 0 {
			return sentMsg{chat: chat, err: err}
		}
		return sentMsg{chat: chat, scheduled: fmt.Sprintf("scheduled #%d for %s", out.Items[0].ID, whenLabel(when))}
	})
}

// whenLabel shows a send time like "Tue 21 Oct 09:00", on the recipient's clock for a local one.
func whenLabel(t bot.SendTime) string {
	s := t.Format("Mon 2 Jan 15:04")
	if t.Local {
		return s + " (their time)"
	}
	return t.Time.Local().Format("Mon 2 Jan 15:04")
}
