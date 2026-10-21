package tui

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/williamtcastro/wame/internal/bot"
	"github.com/williamtcastro/wame/internal/store"
)

const (
	maria = "5511999999999@s.whatsapp.net"
	john  = "5511988887777@s.whatsapp.net"
	ana   = "552187654321@s.whatsapp.net"
)

func newTestModel(t *testing.T, msgs ...store.Message) *model {
	t.Helper()
	b, err := bot.New(context.Background(), filepath.Join(t.TempDir(), "wame.db"), waLog.Noop, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range msgs {
		if _, ok, err := b.Store.Save(x); !ok || err != nil {
			t.Fatalf("save %s: %v", x.ID, err)
		}
	}
	m := newModel(b, make(chan any))
	m.Update(tea.WindowSizeMsg{Width: 110, Height: 24})
	run(m, m.loadChats())
	run(m, m.loadFeed())
	return m
}

// run executes cmd synchronously and feeds its message back, like the Bubble Tea loop would.
func run(m *model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if msg := cmd(); msg != nil {
		if _, isBatch := msg.(tea.BatchMsg); !isBatch {
			m.Update(msg)
		}
	}
}

// settle is run followed to the end of the chain: each answer's command runs too, timers included.
func settle(m *model, cmd tea.Cmd) {
	for cmd != nil {
		msg := cmd()
		if _, isBatch := msg.(tea.BatchMsg); isBatch || msg == nil {
			return
		}
		_, cmd = m.Update(msg)
	}
}

func press(m *model, keys ...string) tea.Cmd {
	var cmd tea.Cmd
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "ctrl+j":
			msg = tea.KeyMsg{Type: tea.KeyCtrlJ}
		case "ctrl+k":
			msg = tea.KeyMsg{Type: tea.KeyCtrlK}
		case "ctrl+d":
			msg = tea.KeyMsg{Type: tea.KeyCtrlD}
		case "ctrl+u":
			msg = tea.KeyMsg{Type: tea.KeyCtrlU}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		if msg.String() != k {
			panic("bad key " + k + " → " + msg.String())
		}
		_, cmd = m.Update(msg)
	}
	return cmd
}

func typeText(m *model, s string) {
	for _, r := range s {
		press(m, string(r))
	}
}

func chatMessages() []store.Message {
	now := time.Now().Add(-time.Hour)
	var out []store.Message
	add := func(chat, sender, id, text string, fromMe bool) {
		out = append(out, store.Message{ID: id, Chat: chat, Sender: sender, FromMe: fromMe, PushName: map[string]string{maria: "Maria", john: "John", ana: "Ana"}[sender],
			Type: "text", Text: text, Timestamp: now.Add(time.Duration(len(out)) * time.Minute)})
	}
	add(ana, ana, "A1", "oi, tudo bem?", false)
	add(john, john, "J1", "hello", false)
	for i := range 30 {
		add(maria, maria, fmt.Sprint("M", i), fmt.Sprintf("message %d from Maria", i), false)
	}
	add(maria, john, "M-me", "see you", true)
	return out
}

func checkScreen(t *testing.T, m *model, label string) {
	t.Helper()
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 24 {
		t.Fatalf("%s: %d lines, want 24", label, len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > 110 {
			t.Fatalf("%s: line %d is %d wide: %q", label, i, w, l)
		}
	}
}

func TestRenderHeight(t *testing.T) {
	m := newTestModel(t, chatMessages()...)
	checkScreen(t, m, "feed")
	typeText(m, "/")
	if m.contextBar() == "" {
		t.Fatal("typing / should show the hint bar")
	}
	checkScreen(t, m, "hint bar")
	press(m, "esc")
	m.input.SetValue("")
	run(m, m.suggest())
	run(m, m.open(maria))
	checkScreen(t, m, "chat")
	press(m, "up", "r")
	if m.replyTo == nil {
		t.Fatal("r should start a reply")
	}
	checkScreen(t, m, "reply bar")
	m.Update(panelMsg{"Help", tuiHelp})
	checkScreen(t, m, "help panel")
}

func TestAutocomplete(t *testing.T) {
	m := newTestModel(t, chatMessages()...)
	m.Update(contactNamesMsg{"Ana", "John", "Maria"})
	typeText(m, "/t")
	press(m, "tab")
	if v := m.input.Value(); v != "/to " {
		t.Fatalf("/t + tab = %q, want %q", v, "/to ")
	}
	typeText(m, "M")
	if got := m.input.MatchedSuggestions(); len(got) != 1 || got[0] != "/to Maria" {
		t.Fatalf("/to M suggestions = %q", got)
	}
	press(m, "tab")
	if v := m.input.Value(); v != "/to Maria" {
		t.Fatalf("accepted = %q", v)
	}

	m.input.SetValue("")
	run(m, m.suggest())
	typeText(m, "/")
	all := m.input.MatchedSuggestions()
	if len(all) != len(tuiCommands) || all[0] != "/to " {
		t.Fatalf("/ suggestions = %q", all)
	}
	seq := m.openSeq
	press(m, "ctrl+j")
	press(m, "ctrl+j")
	if i := m.input.CurrentSuggestionIndex(); i != 2 {
		t.Fatalf("after two ctrl+j index = %d, want 2", i)
	}
	press(m, "ctrl+k")
	if m.input.CurrentSuggestion() != all[1] {
		t.Fatalf("ctrl+k = %q, want %q", m.input.CurrentSuggestion(), all[1])
	}
	press(m, "down")
	if m.input.CurrentSuggestionIndex() != 2 {
		t.Fatal("↓ should cycle too")
	}
	if m.openSeq != seq || m.input.Value() != "/" {
		t.Fatal("cycling suggestions must not switch chats or edit the input")
	}
}

func TestReactionDividerHiddenWhenRemoved(t *testing.T) {
	now := time.Now()
	text := store.Message{ID: "M1", Chat: maria, Sender: maria, Type: "text", Text: "oi", Timestamp: now}
	like := store.Message{ID: "R1", Chat: maria, Sender: john, FromMe: true, Type: "reaction", Text: "👍", ReplyTo: "M1", Timestamp: now}
	unlike := store.Message{ID: "R2", Chat: maria, Sender: john, FromMe: true, Type: "reaction", Text: "", ReplyTo: "M1", Timestamp: now}

	m := newTestModel(t)
	m.active, m.msgs = maria, []store.Message{text, like}
	if out := m.renderChat(80); !strings.Contains(out, "│ 👍") {
		t.Fatalf("reaction not shown:\n%s", out)
	}
	m.msgs = []store.Message{text, like, unlike}
	if out := m.renderChat(80); strings.Contains(out, "│") || strings.Contains(out, "👍") {
		t.Fatalf("removed reaction still shown:\n%s", out)
	}
}

func TestCtrlJKSwitchChats(t *testing.T) {
	m := newTestModel(t, chatMessages()...)
	var order []string
	for _, c := range m.chats {
		order = append(order, c.JID)
	}
	if len(order) != 3 || order[0] != maria {
		t.Fatalf("chats = %v", order)
	}
	run(m, press(m, "ctrl+j"))
	if m.active != order[0] {
		t.Fatalf("ctrl+j from All activity opened %q, want %q", m.active, order[0])
	}
	run(m, press(m, "ctrl+j"))
	if m.active != order[1] {
		t.Fatalf("second ctrl+j opened %q, want %q", m.active, order[1])
	}
	run(m, press(m, "ctrl+k"))
	if m.active != order[0] {
		t.Fatalf("ctrl+k opened %q, want %q", m.active, order[0])
	}
	run(m, press(m, "ctrl+k"))
	if m.active != "" {
		t.Fatalf("ctrl+k at the top should open All activity, got %q", m.active)
	}

	press(m, "tab")
	if m.focus != focusChats {
		t.Fatal("tab should focus the chat list")
	}
	press(m, "down", "down", "down")
	run(m, press(m, "ctrl+k"))
	if m.active != order[1] || m.cursor != 2 {
		t.Fatalf("ctrl+k in the chat list steps from the cursor: active %q cursor %d", m.active, m.cursor)
	}
	press(m, "up")
	if m.cursor != 1 {
		t.Fatalf("↑ in the chat list: cursor %d", m.cursor)
	}
	typeText(m, "jo")
	if m.filter != "jo" || len(m.filteredChats()) != 1 {
		t.Fatalf("letters should filter, filter %q", m.filter)
	}
}

func TestMessageSelectVimKeys(t *testing.T) {
	m := newTestModel(t, chatMessages()...)
	run(m, m.open(maria))
	last := len(m.visible) - 1
	press(m, "up")
	if m.focus != focusMessages || m.sel != last {
		t.Fatalf("↑ on empty input: focus %d sel %d", m.focus, m.sel)
	}
	press(m, "g")
	if m.sel != 0 {
		t.Fatalf("g: sel %d", m.sel)
	}
	press(m, "k")
	if m.sel != 0 {
		t.Fatalf("k at the top: sel %d", m.sel)
	}
	press(m, "j", "j")
	if m.sel != 2 {
		t.Fatalf("j j: sel %d", m.sel)
	}
	press(m, "G")
	if m.sel != last {
		t.Fatalf("G: sel %d, want %d", m.sel, last)
	}
	press(m, "j")
	if m.focus != focusInput {
		t.Fatal("j past the last message goes back to typing")
	}
	press(m, "up", "x")
	if m.focus != focusInput || m.input.Value() != "x" {
		t.Fatalf("other letters jump back to typing: focus %d value %q", m.focus, m.input.Value())
	}
}

func TestHalfPageScroll(t *testing.T) {
	m := newTestModel(t, chatMessages()...)
	run(m, m.open(maria))
	typeText(m, "draft")
	bottom := m.vp.YOffset
	if bottom == 0 {
		t.Fatal("expected a scrollable chat")
	}
	press(m, "ctrl+u")
	if m.vp.YOffset != bottom-m.vp.Height/2 {
		t.Fatalf("ctrl+u: offset %d, want %d", m.vp.YOffset, bottom-m.vp.Height/2)
	}
	press(m, "ctrl+d")
	if m.vp.YOffset != bottom {
		t.Fatalf("ctrl+d: offset %d, want %d", m.vp.YOffset, bottom)
	}
	press(m, "ctrl+k")
	if m.input.Value() != "draft" {
		t.Fatalf("textinput's own ctrl+u/ctrl+k must not edit the draft: %q", m.input.Value())
	}
}

func TestSafe(t *testing.T) {
	for in, want := range map[string]string{
		"\x1b]0;pwned\x07hello":       "hello",
		"\x1b[31mred\x1b[0m":          "red",
		"Maria‮gnp.exe":               "Mariagnp.exe",
		"line1\nline2\tx\x00\x7f\x9b": "line1\nline2\tx",
	} {
		if got := safe(in); got != want {
			t.Errorf("safe(%q) = %q, want %q", in, got, want)
		}
	}
	if got := safeLine(" Ana\n\tde  Souza "); got != "Ana de Souza" {
		t.Errorf("safeLine = %q", got)
	}
	x := safeMessage(store.Message{Text: "\x1b[2Jhi", PushName: "Jo\nhn", Media: &store.Media{FileName: "a\x1b]8;;x\x07.pdf"}})
	if x.Text != "hi" || x.PushName != "Jo hn" || x.Media.FileName != "a.pdf" {
		t.Errorf("safeMessage = %+v %+v", x, x.Media)
	}
}

func TestFeedShowsNamesOnceChatsLoad(t *testing.T) {
	m := newTestModel(t, chatMessages()...)
	m.chats = nil
	m.render(true)
	if !strings.Contains(m.View(), "+"+userOf(maria)) {
		t.Fatal("expected a bare number before chats load")
	}
	run(m, m.loadChats())
	if v := m.View(); !strings.Contains(v, "~Maria") || strings.Contains(v, "+"+userOf(maria)+"  ") {
		t.Fatalf("feed not redrawn with names after chats loaded:\n%s", v)
	}
}

func TestCopyTarget(t *testing.T) {
	for jid, want := range map[string]string{
		maria:                     "+" + userOf(maria),
		"120363000000000000@g.us": "120363000000000000@g.us",
		"12345@lid":               "",
	} {
		got, err := copyTarget(jid)
		if got != want || (want == "") != (err != nil) {
			t.Errorf("copyTarget(%s) = %q, %v; want %q", jid, got, err, want)
		}
	}
	m := newTestModel(t, chatMessages()...)
	run(m, m.open(maria))
	if !strings.Contains(m.View(), "ctrl+y copy") {
		t.Fatal("chat header should show the copy hint")
	}
}

func TestOwnMessagesLabelledMe(t *testing.T) {
	m := newTestModel(t, chatMessages()...)
	if v := m.View(); !strings.Contains(v, "me → ") || strings.Contains(v, "you → ") {
		t.Fatalf("feed should label own messages \"me\":\n%s", v)
	}
}

func TestNewMessagesDivider(t *testing.T) {
	incoming := slices.DeleteFunc(chatMessages(), func(x store.Message) bool { return x.FromMe })
	m := newTestModel(t, incoming...)
	var cutoff int64
	msgs, _ := m.bot.Store.Messages(maria, 100)
	for _, x := range msgs {
		if x.ID == "M27" {
			cutoff = x.Seq
		}
	}
	m.bot.Store.MarkReadUpTo(maria, cutoff)
	run(m, m.loadChats())
	if !strings.Contains(m.View(), styleBadge.Render("2")) {
		t.Fatal("sidebar should show 2 unread for Maria from the database")
	}
	run(m, m.open(maria))
	if v := m.View(); !strings.Contains(v, "2 new messages") {
		t.Fatalf("expected the new-messages divider:\n%s", v)
	}
}

func TestSearchJumpsToMessage(t *testing.T) {
	m := newTestModel(t, chatMessages()...)
	run(m, m.command("/search message 3"))
	if !m.searching || len(m.results) == 0 {
		t.Fatalf("search returned nothing (searching=%v)", m.searching)
	}
	if !strings.Contains(m.View(), "Search: message 3") {
		t.Fatal("results header missing")
	}
	var target store.Message
	for i, r := range m.results {
		if r.Text == "message 3 from Maria" {
			m.resultSel, target = i, r
		}
	}
	if target.Seq == 0 {
		t.Fatalf("expected 'message 3 from Maria' among %d results", len(m.results))
	}
	run(m, press(m, "enter"))
	if m.searching || m.active != maria || m.focus != focusMessages {
		t.Fatalf("enter should open Maria with a message selected (searching=%v active=%s focus=%v)", m.searching, m.active, m.focus)
	}
	if got := m.visible[m.sel]; got.Seq != target.Seq {
		t.Fatalf("selected %q, want %q", got.Text, target.Text)
	}
}

func TestOpeningChatClearsBadgeOffline(t *testing.T) {
	incoming := slices.DeleteFunc(chatMessages(), func(x store.Message) bool { return x.FromMe })
	m := newTestModel(t, incoming...)
	if i := chatIndex(m.chats, maria); i < 0 || m.chats[i].Unread == 0 {
		t.Fatal("Maria should start unread")
	}
	settle(m, m.open(maria))
	if i := chatIndex(m.chats, maria); i < 0 || m.chats[i].Unread != 0 {
		t.Fatal("badge should clear after opening the chat")
	}
	last, _ := m.bot.Store.Messages(maria, 1)
	for _, c := range last {
		if m.bot.Store.ReadSeq(maria) != c.Seq {
			t.Fatal("opening the chat should mark it read locally even when not logged in")
		}
	}
}

func TestSearchWithNoResultsIgnoresKeys(t *testing.T) {
	m := newTestModel(t, chatMessages()...)
	run(m, m.command("/search zzzznothing"))
	if !m.searching || len(m.results) != 0 {
		t.Fatalf("expected an empty result list, got %d", len(m.results))
	}
	run(m, press(m, "G", "down", "enter"))
	if !m.searching {
		t.Fatal("keys on an empty result list should do nothing, not open a chat")
	}
	run(m, press(m, "esc"))
	if m.searching {
		t.Fatal("esc should close the results")
	}
}

func TestChatReloadsAreDebouncedAndOrdered(t *testing.T) {
	m := newTestModel(t, chatMessages()...)
	run(m, m.open(ana))
	var scheduled int
	for i := range 5 {
		if m.onEvent(store.Message{Seq: int64(100 + i), Chat: john, Sender: john, Type: "text", Text: "hi"}) != nil {
			scheduled++
		}
	}
	if scheduled != 1 || !m.chatsReload.Pending() {
		t.Fatalf("5 messages scheduled %d reloads, want 1", scheduled)
	}
	settle(m, func() tea.Msg { return chatsReloadMsg{} })
	if m.chatsReload.Pending() || len(m.chats) != 3 {
		t.Fatalf("reload: pending %v, %d chats", m.chatsReload.Pending(), len(m.chats))
	}

	older, newer := m.loadChats(), m.loadChats()
	m.Update(newer())
	stale := older().(chatsMsg)
	stale.chats = stale.chats[:1]
	m.Update(stale)
	if len(m.chats) != 3 {
		t.Fatalf("an older chat list replaced a newer one: %d chats", len(m.chats))
	}
}

func TestNameAndTagCommands(t *testing.T) {
	group := "120363000000000000@g.us"
	msgs := append(chatMessages(), store.Message{ID: "G1", Chat: group, Sender: ana, IsGroup: true, Type: "text", Text: "oi", Timestamp: time.Now()})
	m := newTestModel(t, msgs...)
	command := func(line string) {
		t.Helper()
		m.input.SetValue(line)
		m.notice = ""
		if cmd := press(m, "enter"); m.notice == "" {
			run(m, cmd)
		}
	}
	title := func() string { return ansi.Strip(m.paneTitle()) }

	command("/name Maria")
	if !m.noticeErr || !strings.Contains(m.notice, "no chat open") {
		t.Fatalf("/name without a chat: %q", m.notice)
	}
	run(m, m.open(maria))
	command("/name Maria Silva")
	if !strings.HasPrefix(title(), "Maria Silva") || m.noticeErr {
		t.Fatalf("/name: title %q, notice %q", title(), m.notice)
	}
	if got := m.bot.ChatName(t.Context(), maria, ""); got != "Maria Silva" {
		t.Fatalf("the unified name is %q", got)
	}
	command("/tag clients vip")
	if !strings.Contains(title(), "Maria Silva  clients vip") {
		t.Fatalf("/tag: %q", title())
	}
	command("/tag +late -vip")
	if !strings.Contains(title(), "clients late") || strings.Contains(title(), "vip") {
		t.Fatalf("/tag +late -vip: %q", title())
	}
	command("/tag clients +vip")
	if !m.noticeErr || !strings.Contains(m.notice, "not both") {
		t.Fatalf("mixed /tag: %q", m.notice)
	}
	command("/tag Bad\x1b[2J")
	if !m.noticeErr || strings.Contains(m.notice, "\x1b") {
		t.Fatalf("a bad tag: %q", m.notice)
	}

	if !m.chatsReload.Pending() {
		t.Fatal("/name must reload the chat list")
	}
	settle(m, func() tea.Msg { return chatsReloadMsg{} })
	if i := chatIndex(m.chats, maria); i < 0 || m.chats[i].Name != "Maria Silva" {
		t.Fatalf("the chat list keeps the old name: %+v", m.chats)
	}
	run(m, m.open(john))
	run(m, m.open(maria))
	if !strings.Contains(title(), "Maria Silva  clients late") {
		t.Fatalf("reopening must load the tags: %q", title())
	}
	m.input.SetValue("")
	typeText(m, "/name ")
	if got := m.input.MatchedSuggestions(); len(got) != 1 || got[0] != "/name Maria Silva" {
		t.Fatalf("/name completion: %q", got)
	}

	command("/name")
	command("/tag")
	if got := title(); strings.Contains(got, "Maria") || strings.Contains(got, "clients") || !strings.HasPrefix(got, "+5511999999999") {
		t.Fatalf("/name and /tag alone must clear: %q", got)
	}

	run(m, m.open(group))
	command("/name Family")
	if !m.noticeErr || !strings.Contains(m.notice, "not groups") {
		t.Fatalf("/name on a group: %q", m.notice)
	}
}

func TestNameCompletionOnlyForNamedPeople(t *testing.T) {
	group, lid := "120363000000000000@g.us", "123456789012345@lid"
	msgs := append(chatMessages(),
		store.Message{ID: "G1", Chat: group, Sender: ana, IsGroup: true, Type: "text", Text: "oi", Timestamp: time.Now()},
		store.Message{ID: "L1", Chat: lid, Sender: lid, Type: "text", Text: "oi", Timestamp: time.Now()})
	m := newTestModel(t, msgs...)
	name := "Maria"
	if _, err := m.bot.PatchContact(t.Context(), "api", maria, bot.ContactInput{Name: &name}); err != nil {
		t.Fatal(err)
	}
	run(m, m.loadChats())
	for chat, want := range map[string]int{group: 0, lid: 0, maria: 1} {
		run(m, m.open(chat))
		m.input.SetValue("")
		typeText(m, "/name ")
		if got := m.input.MatchedSuggestions(); len(got) != want {
			t.Errorf("%s (%q): /name suggestions %q", chat, m.activeName, got)
		}
	}
}

func TestOpenSurfacesContactErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wame.db")
	b, err := bot.New(context.Background(), path, waLog.Noop, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range chatMessages() {
		if _, _, err := b.Store.Save(x); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE wame_contacts`); err != nil {
		t.Fatal(err)
	}
	m := newModel(b, make(chan any))
	m.Update(tea.WindowSizeMsg{Width: 110, Height: 24})
	run(m, m.open(maria))
	if m.active != maria || !m.noticeErr || !strings.HasPrefix(m.notice, "contact: ") {
		t.Fatalf("a failed contact lookup must show: active %q, notice %q", m.active, m.notice)
	}
}

func TestOpeningTheSelfChatShowsMe(t *testing.T) {
	m := newTestModel(t, chatMessages()...)
	self, _ := types.ParseJID("5511999999999:1@s.whatsapp.net")
	m.bot.Client.Store.ID = &self
	t.Cleanup(func() { m.bot.Client.Store.ID = nil })
	m.chats = nil // the title must come from the contact lookup, not the chat list
	run(m, m.open(maria))
	if m.activeName != "Me" {
		t.Fatalf("own chat title = %q, want Me", m.activeName)
	}
}

func TestScheduledCommands(t *testing.T) {
	m := newTestModel(t, chatMessages()...)
	command := func(line string) {
		t.Helper()
		m.input.SetValue(line)
		m.notice = ""
		if cmd := press(m, "enter"); m.notice == "" {
			run(m, cmd)
		}
	}
	command("/later 2h hello")
	if !m.noticeErr || !strings.Contains(m.notice, "no chat open") {
		t.Fatalf("/later without a chat: %q", m.notice)
	}
	run(m, m.open(maria))
	command("/later 2h hello later")
	if m.noticeErr || !strings.HasPrefix(m.notice, "scheduled #") {
		t.Fatalf("/later: %q (err %v)", m.notice, m.noticeErr)
	}
	items, err := m.bot.Store.ListOutbox(store.OutboxFilter{Actor: actor, Status: store.OutboxQueued}, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("queued %v, %v", items, err)
	}
	it := items[0]
	if d := time.Until(it.Due); d < 119*time.Minute || d > 121*time.Minute || it.Text != "hello later" {
		t.Fatalf("due in %v, text %q", time.Until(it.Due), it.Text)
	}
	if !strings.Contains(m.notice, dueText(it.Due)) {
		t.Fatalf("notice %q lacks %q", m.notice, dueText(it.Due))
	}

	command("/later tomorrow 09:00 morning")
	if m.noticeErr || !strings.HasPrefix(m.notice, "scheduled #") {
		t.Fatalf("/later tomorrow: %q", m.notice)
	}
	command("/later whenever hi")
	if !m.noticeErr {
		t.Fatalf("bad WHEN gave notice %q", m.notice)
	}

	run(m, m.listScheduled())
	if !strings.Contains(m.panelBody, "hello later") || !strings.Contains(m.panelBody, fmt.Sprintf("#%d", it.ID)) {
		t.Fatalf("/scheduled panel: %q", m.panelBody)
	}

	command(fmt.Sprintf("/reschedule %d 5h", it.ID))
	if m.noticeErr || !strings.HasPrefix(m.notice, "rescheduled #") {
		t.Fatalf("/reschedule: %q", m.notice)
	}
	items, _ = m.bot.Store.ListOutbox(store.OutboxFilter{Actor: actor, Status: store.OutboxQueued}, 10)
	for _, x := range items {
		if x.ID == it.ID && time.Until(x.Due) < 299*time.Minute {
			t.Fatalf("due still in %v", time.Until(x.Due))
		}
	}
	command(fmt.Sprintf("/unschedule %d", it.ID))
	if m.noticeErr {
		t.Fatalf("/unschedule: %q", m.notice)
	}
	items, _ = m.bot.Store.ListOutbox(store.OutboxFilter{Actor: actor, Status: store.OutboxQueued}, 10)
	for _, x := range items {
		if x.ID == it.ID {
			t.Fatalf("#%d still queued", it.ID)
		}
	}
	command("/reschedule 999999 2h")
	if !m.noticeErr {
		t.Fatalf("unknown id: %q", m.notice)
	}
}
