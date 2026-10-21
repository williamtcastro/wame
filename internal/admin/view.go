package admin

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mdp/qrterminal/v3"

	"github.com/williamtcastro/wame/internal/store"
	"github.com/williamtcastro/wame/internal/termui"
)

var (
	colAccent = termui.ColAccent
	colText   = termui.ColText
	colMuted  = termui.ColMuted
	colFaint  = termui.ColFaint
	colErr    = termui.ColErr
	colWarn   = lipgloss.Color("214")
	colInfo   = lipgloss.Color("39")
	colPurple = lipgloss.Color("141")
	colSel    = termui.ColSel

	styleDim    = lipgloss.NewStyle().Foreground(colMuted)
	styleFaint  = lipgloss.NewStyle().Foreground(colFaint)
	styleErr    = lipgloss.NewStyle().Foreground(colErr)
	styleWarn   = lipgloss.NewStyle().Foreground(colWarn)
	styleBold   = lipgloss.NewStyle().Bold(true).Foreground(colText)
	styleAccent = lipgloss.NewStyle().Foreground(colAccent)
	styleInfo   = lipgloss.NewStyle().Foreground(colInfo)
	styleKey    = lipgloss.NewStyle().Foreground(colText).Background(colSel).Padding(0, 1)
	styleLogo   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(colAccent).Padding(0, 1)
	styleTab    = lipgloss.NewStyle().Padding(0, 1).Foreground(colMuted)
	styleTabOn  = lipgloss.NewStyle().Padding(0, 1).Bold(true).Foreground(colText).Background(colSel)
	styleCard   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colFaint).Padding(0, 1)
	styleModal  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colAccent).Padding(1, 3)
	styleDanger = styleModal.BorderForeground(colErr)
	styleHead   = lipgloss.NewStyle().Foreground(colMuted).Bold(true)

	scopeColors = map[string]lipgloss.Color{"read": colInfo, "send": colWarn, "admin": colErr}
)

// Widest a card of each kind gets on a wide terminal.
const (
	cardNarrow = 70
	cardMedium = 90
	cardWide   = 100
	cardDetail = 130
)

func (m *model) bodyHeight() int { return max(m.height-5, 3) }

func (m *model) cardWidth(w int) int { return min(m.width-2, w) }

// errCard is what a tab shows when its data didn't load. For a server too old to have the route
// (404), missing names it and the card asks for an update instead of showing the error.
func (m *model) errCard(title string, err error, missing string) string {
	body := errText(err)
	if missing != "" && isNotFound(err) {
		body = styleWarn.Render("This server doesn't have "+missing+" yet.") + "\n" +
			styleDim.Render("Update wame on the server to use it.")
	}
	return indent(card(title, m.cardWidth(cardMedium), body), " ")
}

// errText renders an error, sanitized: its text often comes from the server.
func errText(err error) string { return styleErr.Render(safe(err.Error())) }

func (m *model) View() string {
	if m.width == 0 {
		return ""
	}
	var body string
	switch {
	case m.mode == modeServers:
		body = m.modal(styleModal, m.viewServers())
	case m.mode == modeCreate:
		body = m.modal(styleModal, m.viewForm())
	case m.mode == modeShowToken:
		body = m.modal(styleModal, m.viewCreated())
	case m.mode == modePair:
		body = m.modal(styleModal, m.viewPair())
	case m.mode == modeConfirmLogout:
		body = m.modal(styleDanger, m.viewConfirmLogout())
	case m.mode == modeConfirmRevoke:
		body = m.modal(styleDanger, m.viewConfirmRevoke())
	case m.mode == modeConfirmRotate:
		body = m.modal(styleModal, m.viewConfirmRotate())
	case m.mode == modeConfirmCancelBatch:
		body = m.modal(styleDanger, m.viewConfirmCancelBatch())
	case m.mode == modeAddOptOut:
		body = m.modal(styleModal, m.viewAddOptOut())
	case m.mode == modeConfirmRemoveOptOut:
		body = m.modal(styleDanger, m.viewConfirmRemoveOptOut())
	case m.mode == modeConfirmDeleteQueueFile:
		body = m.modal(styleDanger, m.viewConfirmDeleteQueueFile())
	case m.mode == modeEditContact:
		body = m.modal(styleModal, m.viewContactForm())
	case m.mode == modeConfirmDeleteContact:
		body = m.modal(styleDanger, m.viewConfirmDeleteContact())
	case m.mode == modeConfirmImport:
		body = m.modal(styleModal, m.viewConfirmImport())
	case m.mode == modeImportContacts, m.mode == modeExportContacts:
		body = m.modal(styleModal, m.viewContactPath())
	case m.tab == tabStatus:
		body = m.viewStatus()
	case m.tab == tabChats:
		body = m.viewChats()
	case m.tab == tabTokens:
		body = m.viewTokens()
	case m.tab == tabAudit:
		body = m.viewAudit()
	case m.tab == tabHealth:
		body = m.viewHealth()
	case m.tab == tabQueue:
		body = m.viewQueue()
	case m.tab == tabLogs:
		body = m.viewLogs()
	case m.tab == tabContacts:
		body = m.viewContacts()
	default:
		body = m.viewSettings()
	}
	lines := strings.Split(body, "\n")
	if h := m.bodyHeight(); len(lines) > h {
		lines = lines[:h]
	} else {
		for len(lines) < h {
			lines = append(lines, "")
		}
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, m.width, "…")
	}
	return strings.Join([]string{m.viewHeader(), styleFaint.Render(strings.Repeat("─", m.width)), strings.Join(lines, "\n"), m.viewNotice(), m.viewKeys()}, "\n")
}

func (m *model) modal(style lipgloss.Style, content string) string {
	return lipgloss.Place(m.width, m.bodyHeight(), lipgloss.Center, lipgloss.Center, style.Render(content))
}

// connection describes the server's state as a colored dot and a short label.
func (m *model) connection() (string, string) {
	switch {
	case m.c == nil:
		return styleDim.Render("●"), "no server"
	case m.statusErr != nil:
		return styleErr.Render("●"), "unreachable"
	case m.checked.IsZero():
		return styleDim.Render("●"), "connecting…"
	case m.status.Connected && m.status.LoggedIn:
		return styleAccent.Render("●"), "connected"
	case m.status.QR != "":
		return styleWarn.Render("●"), "waiting for login"
	}
	return styleWarn.Render("●"), m.status.State
}

func (m *model) host() string {
	if m.c == nil {
		return ""
	}
	if m.profile != "" {
		return m.profile + " " + styleFaint.Render("("+m.c.base.Host+")")
	}
	return m.c.base.Host
}

// tabLabel is a tab's number, name and badge; compact drops the name unless it's the open tab.
func (m *model) tabLabel(t tab, compact bool) string {
	label := fmt.Sprint(int(t) + 1)
	if !compact || t == m.tab {
		label += " " + tabNames[t]
	}
	switch t {
	case tabChats:
		if n := m.unreadChats(); n > 0 {
			label += " " + styleAccent.Render(fmt.Sprint(n))
		}
	case tabTokens:
		if n := m.activeTokens(); n > 0 {
			label += " " + styleDim.Render(fmt.Sprint(n))
		}
	case tabAudit:
		if n := m.recentFailures(); n > 0 {
			label += " " + styleErr.Render(fmt.Sprintf("✗%d", n))
		}
	case tabHealth:
		if m.healthWarning() {
			label += " " + styleWarn.Render("!")
		}
	case tabQueue:
		switch q := m.stats.Queue; {
		case q.Paused && q.Queued > 0:
			label += " " + styleWarn.Render(fmt.Sprintf("⏸%d", q.Queued))
		case q.Queued+q.Sending > 0:
			label += " " + styleAccent.Render(fmt.Sprint(q.Queued+q.Sending))
		}
	case tabSettings:
		for _, s := range m.settings {
			if s.Overridden {
				label += " " + styleWarn.Render("•")
				break
			}
		}
	}
	if t == m.tab {
		return styleTabOn.Render(label)
	}
	return styleTab.Render(label)
}

// tabsLine is the logo and the tabs; compact shows only the open tab's name. Tabs are padded
// already: no gap between them leaves room for the state on 120 columns.
func (m *model) tabsLine(compact bool) string {
	var b strings.Builder
	b.WriteString(" " + styleLogo.Render("◆ wame admin") + " ")
	for i := range tabNames {
		b.WriteString(m.tabLabel(tab(i), compact))
	}
	return b.String()
}

func (m *model) viewHeader() string {
	dot, state := m.connection()
	live := ""
	if m.c != nil {
		live = styleDim.Render("polling")
		if m.live.on {
			live = styleAccent.Render("⚡live")
		}
	}
	latency := ""
	if m.statusErr == nil && m.latency > 0 {
		latency = styleDim.Render(fmt.Sprintf("%d ms", m.latency.Milliseconds()))
	}
	host := ""
	if m.c != nil {
		host = styleDim.Render(cmp.Or(m.profile, m.c.base.Host))
	}
	// Longest first; drop detail, then the inactive tabs' names, until it fits. The state and the
	// quiet-hours marker always stay.
	quiet := m.quietBadge()
	var right string
	for _, compact := range []bool{false, true} {
		left := m.tabsLine(compact)
		for _, items := range [][]string{
			{quiet, dot + " " + state, styleDim.Render(m.host()), live, latency},
			{quiet, dot + " " + state, host, live, latency},
			{quiet, dot + " " + state, host, live},
			{quiet, dot + " " + state, live},
			{quiet, dot + " " + state},
		} {
			right = strings.Join(nonEmpty(items...), styleFaint.Render("  ·  ")) + " "
			if gap := m.width - ansi.StringWidth(left) - ansi.StringWidth(right); gap >= 2 {
				return left + strings.Repeat(" ", gap) + right
			}
		}
	}
	right = ansi.Truncate(right, m.width, "…")
	return ansi.Truncate(m.tabsLine(true), max(m.width-ansi.StringWidth(right)-1, 0), "…") + " " + right
}

func (m *model) viewNotice() string {
	switch m.mode {
	case modeFilter, modeSearch:
		return " " + styleAccent.Render("/") + " " + m.prompt.View()
	case modeCompose:
		return m.promptErr() + " " + styleAccent.Render("✎ "+m.chatName(m.msgsChat)) + " " + m.prompt.View()
	case modeQueueReschedule, modeQueueEdit:
		return m.promptErr() + " " + styleAccent.Render("✎ #"+fmt.Sprint(m.queueEditID)) + " " + m.prompt.View()
	case modeSetting:
		label, _ := settingLabel(m.editKey)
		return " " + styleAccent.Render("✎ "+safe(label)) + " " + m.prompt.View()
	case modeContactTag:
		return m.viewContactTagPrompt()
	}
	if m.notice == "" {
		return ""
	}
	if m.noticeErr {
		return " " + styleErr.Render("✗ "+m.notice)
	}
	icon := styleAccent.Render("✓ ")
	if strings.HasSuffix(m.notice, "…") {
		icon = styleDim.Render("◌ ")
	}
	return " " + icon + styleDim.Render(m.notice)
}

// promptErr is the last error (a time that didn't parse) shown ahead of a prompt that kept its text.
func (m *model) promptErr() string {
	if m.noticeErr && m.notice != "" {
		return " " + styleErr.Render("✗ "+m.notice)
	}
	return ""
}

func (m *model) viewKeys() string {
	var keys [][2]string
	var optional []string // keys dropped, in order, when the line is too wide
	switch m.mode {
	case modeServers:
		keys = [][2]string{{"j/k", "move"}, {"enter", "connect"}, {"esc", "cancel"}}
	case modeCreate:
		keys = [][2]string{{"tab", "next field"}, {"enter", "next / create"}, {"esc", "cancel"}}
	case modeEditContact:
		keys = [][2]string{{"tab", "next field"}, {"enter", "next / save"}, {"esc", "cancel"}}
	case modeContactTag:
		keys = [][2]string{{"enter", "ok"}, {"tab", "complete"}, {"esc", "cancel"}}
	case modeShowToken:
		keys = [][2]string{{"c", "copy"}, {"any key", "close"}}
	case modePair, modeFilter, modeSearch, modeAddOptOut, modeImportContacts, modeExportContacts:
		keys = [][2]string{{"enter", "ok"}, {"esc", "cancel"}}
	case modeQueueReschedule, modeQueueEdit:
		keys = [][2]string{{"enter", "save"}, {"esc", "cancel"}}
	case modeCompose:
		keys = [][2]string{{"enter", "send"}, {"esc", "cancel"}}
	case modeSetting:
		keys = [][2]string{{"enter", "save"}, {"esc", "cancel"}}
	case modeConfirmLogout, modeConfirmRevoke, modeConfirmRotate, modeConfirmCancelBatch, modeConfirmRemoveOptOut,
		modeConfirmDeleteContact, modeConfirmImport, modeConfirmDeleteQueueFile:
		keys = [][2]string{{"y", "confirm"}, {"any key", "cancel"}}
	default:
		tabsKey := fmt.Sprintf("1-%d", len(tabNames))
		optional = []string{tabsKey}
		switch m.tab {
		case tabStatus:
			if m.status.LoggedIn {
				keys = append(keys, [2]string{"L", "log out"})
			} else {
				keys = append(keys, [2]string{"p", "pair with phone number"})
			}
		case tabChats:
			keys = [][2]string{{"j/k", "chat"}, {"i", "send"}, {"/", "search"}, {"p", "contact"}}
		case tabTokens:
			keys = [][2]string{{"j/k", "move"}, {"n", "new"}, {"R", "rotate"}, {"x", "revoke"}}
		case tabAudit:
			keys = [][2]string{{"j/k", "move"}, {"g/G", "top/bottom"}, {"/", "filter"}}
		case tabHealth:
			keys = [][2]string{{"j/k", "failure"}, {"t", "test webhook"}, {"R", "replay"}, {"a", "test alert"}}
		case tabSettings:
			keys = [][2]string{{"j/k", "move"}, {"enter", "change"}, {"x", "reset"}}
		case tabQueue:
			if m.queueOptOuts {
				keys = [][2]string{{"/", "filter"}, {"a", "add"}, {"x", "remove"}, {"o/esc", "queue"}}
				if m.optOutQuery != "" {
					keys = append(keys[:3], [2]string{"esc", "clear"}, [2]string{"o", "queue"})
				}
				break
			}
			if m.queueFiles {
				keys = [][2]string{{"/", "filter"}, {"d", "delete"}, {"F/esc", "queue"}}
				if m.qfileQuery != "" {
					keys = [][2]string{{"/", "filter"}, {"d", "delete"}, {"esc", "clear"}, {"F", "queue"}}
				}
				break
			}
			pause := "pause"
			if m.stats.Queue.Paused {
				pause = "resume"
			}
			keys = [][2]string{{"f", "filter"}, {"t/e", "when/text"}, {"x", "cancel"}, {"X", "batch"}, {"P", pause}, {"Q", "quiet"}, {"F", "files"}}
			optional = []string{tabsKey, "S"}
		case tabLogs:
			follow := "pause"
			if !m.logsFollow {
				follow = "follow"
			}
			keys = [][2]string{{"j/k", "move"}, {"f", "level"}, {"/", "filter"}, {"F", follow}}
		case tabContacts:
			keys = [][2]string{{"j/k", "move"}, {"/", "filter"}, {"t", "tag"}, {"e", "edit"}, {"x", "delete"}, {"I/E", "csv"}, {"W", "refresh (2-4 lookups)"}, {"enter", "chat"}}
			if m.contactsReadOnly() {
				keys = [][2]string{{"j/k", "move"}, {"/", "filter"}, {"t", "tag"}, {"enter", "chat"}}
			}
			optional = []string{"j/k", tabsKey, "enter", "S", "W"}
		}
		keys = append(keys, [2]string{tabsKey, "tabs"})
		if len(m.opts.Profiles) > 0 {
			keys = append(keys, [2]string{"S", "servers"})
		}
		keys = append(keys, [2]string{"r", "refresh"}, [2]string{"q", "quit"})
	}
	render := func() string {
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = styleKey.Render(k[0]) + " " + styleDim.Render(k[1])
		}
		return " " + strings.Join(parts, "   ")
	}
	line := render()
	for _, drop := range optional {
		if ansi.StringWidth(line) <= m.width {
			break
		}
		keys = slices.DeleteFunc(keys, func(k [2]string) bool { return k[0] == drop })
		line = render()
	}
	return ansi.Truncate(line, m.width, "…")
}

// card is a bordered box of total width w with a title.
func card(title string, w int, body string) string {
	return styleCard.Width(max(w-2, 10)).Render(styleBold.Render(title) + "\n" + body)
}

// cardSized is a card with a fixed total height, so cards side by side line up.
func cardSized(title string, w, h int, body string) string {
	return styleCard.Width(max(w-2, 10)).Height(max(h-2, 1)).Render(styleBold.Render(title) + "\n" + body)
}

func kv(rows [][2]string) string {
	w := 0
	for _, r := range rows {
		w = max(w, ansi.StringWidth(r[0]))
	}
	lines := make([]string, len(rows))
	for i, r := range rows {
		lines[i] = styleDim.Render(pad(r[0], w)) + "  " + r[1]
	}
	return strings.Join(lines, "\n")
}

func (m *model) viewStatus() string {
	if m.c == nil {
		return ""
	}
	if m.statusErr != nil && m.checked.IsZero() || m.statusErr != nil && m.status.State == "" {
		return indent(card("Can't reach the server", m.cardWidth(cardMedium),
			errText(m.statusErr)+"\n\n"+
				styleDim.Render("Check the URL and token. Retrying every few seconds.")), " ")
	}
	if m.checked.IsZero() {
		return "  " + styleDim.Render("loading…")
	}
	s := m.status
	yes := func(b bool, on, off string) string {
		if b {
			return styleAccent.Render("● " + on)
		}
		return styleWarn.Render("○ " + off)
	}
	account := styleDim.Render("not linked")
	if s.JID != "" {
		account = styleBold.Render("+" + userOf(s.JID))
		if s.PushName != "" {
			account += " " + styleDim.Render(safe(s.PushName))
		}
	}
	whatsapp := panel{"WhatsApp", kv([][2]string{
		{"Account", account},
		{"Session", yes(s.LoggedIn, "linked", "not linked")},
		{"Socket", yes(s.Connected, "connected", "disconnected")},
		{"State", styleDim.Render(s.State)},
	})}
	liveState := styleDim.Render("○ polling every " + m.pollEvery().String())
	if m.live.on {
		liveState = styleAccent.Render("● live") + styleDim.Render(" since "+stamp(m.live.since, "15:04"))
	} else if m.live.err != nil {
		liveState += styleDim.Render(" (" + fit(safe(m.live.err.Error()), 30) + ")")
	}
	version := styleDim.Render("?")
	if m.stats.Version != "" {
		version = safe(m.stats.Version) + styleDim.Render(", up "+strings.TrimSuffix(relTime(m.stats.Started), " ago"))
	}
	server := panel{"Server", kv([][2]string{
		{"URL", m.c.Server()},
		{"Version", version},
		{"Latency", fmt.Sprintf("%d ms", m.latency.Milliseconds())},
		{"Updates", liveState},
	})}

	switch {
	case m.pairCode != "":
		return m.besides([]panel{whatsapp, server}, panel{"Pairing code", "\n  " + styleLogo.Render(m.pairCode) + "\n\n" +
			styleDim.Render("On the phone: WhatsApp → Linked devices →\nLink a device → Link with phone number instead.")})
	case s.QR != "":
		var qr strings.Builder
		qrterminal.GenerateHalfBlock(s.QR, qrterminal.L, &qr)
		return m.besides([]panel{whatsapp, server}, panel{"Scan to log in", styleDim.Render("WhatsApp → Linked devices → Link a device.\nIt refreshes on its own; p pairs by phone number.") +
			"\n" + strings.TrimRight(qr.String(), "\n")})
	}
	ps := []panel{whatsapp, server, {"Tokens", m.tokenSummary()}, {"Activity", m.activitySummary()},
		{"Messages, last hour", m.liveSummary()}, {"Health", m.healthSummary()}}
	if !s.LoggedIn {
		ps = append([]panel{{"Not linked", styleWarn.Render("No QR yet.") + styleDim.Render(" The server shows one once it connects;\nor press p to pair with a phone number.")}}, ps...)
	}
	return m.grid(ps)
}

// besides stacks panels on the left and puts a tall one (a QR) on the right.
func (m *model) besides(left []panel, right panel) string {
	w := max((m.width-2)/2, 36)
	var cards []string
	for _, p := range left {
		cards = append(cards, card(p.title, w, p.body))
	}
	return indent(lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.JoinVertical(lipgloss.Left, cards...), " ", card(right.title, w, right.body)), " ")
}

func (m *model) liveSummary() string {
	if !m.live.on && len(m.live.recent) == 0 {
		return styleDim.Render("Counted from the live stream, which isn't connected.")
	}
	in, out, top := m.live.counts()
	lines := []string{kv([][2]string{{"Received", styleBold.Render(fmt.Sprint(in))}, {"Sent", styleBold.Render(fmt.Sprint(out))}})}
	for i, c := range top {
		if i == 3 {
			break
		}
		name := m.chatName(c.chat)
		if name == displayJID(c.chat) && c.name != "" {
			name = safe(c.name)
		}
		lines = append(lines, styleDim.Render(fmt.Sprintf("%3d  ", c.n))+fit(name, 30))
	}
	return strings.Join(lines, "\n")
}

func (m *model) healthWarning() bool {
	s := m.stats
	if m.statsAt.IsZero() {
		return false
	}
	recent := func(t time.Time) bool { return !t.IsZero() && time.Since(t) < time.Hour }
	return recent(s.Limits.Send.LastRejected) || (s.Webhook != nil && (recent(s.Webhook.LastFailure) || s.Webhook.Dropped > 0)) ||
		(s.Alerts.LastError != "" && recent(s.Alerts.LastErrorAt))
}

func (m *model) healthSummary() string {
	switch {
	case isNotFound(m.statsErr):
		return styleDim.Render("Update wame on the server for limiter\nand webhook health.")
	case m.statsAt.IsZero():
		return styleDim.Render("loading…")
	}
	s := m.stats
	webhook := styleDim.Render("not configured")
	if w := s.Webhook; w != nil {
		webhook = styleAccent.Render(fmt.Sprintf("%d delivered", w.Delivered))
		if w.Failed+w.Dropped > 0 {
			webhook += styleErr.Render(fmt.Sprintf(", %d lost", w.Failed+w.Dropped))
		}
	}
	rejected := styleDim.Render("none")
	if n := s.Limits.Send.Rejected; n > 0 {
		rejected = styleErr.Render(fmt.Sprint(n)) + styleDim.Render(", last "+relTime(s.Limits.Send.LastRejected))
	}
	return kv([][2]string{
		{"Sends/min", bar(s.Limits.Send.LastMinute, s.Limits.Send.PerMinute, 14)},
		{"429s", rejected},
		{"Webhook", webhook},
	})
}

func (m *model) unreadChats() int {
	n := 0
	for _, c := range m.chats {
		if c.Unread > 0 {
			n++
		}
	}
	return n
}

func (m *model) activeTokens() int {
	n := 0
	for _, t := range m.tokens {
		if tokenLive(t) {
			n++
		}
	}
	return n
}

func tokenLive(t store.Token) bool {
	return !t.Revoked && (t.Expires.IsZero() || t.Expires.After(time.Now()))
}

func (m *model) tokenSummary() string {
	if m.tokensErr != nil {
		return errText(m.tokensErr)
	}
	var expiring, revoked, idle int
	for _, t := range m.tokens {
		switch {
		case !tokenLive(t):
			revoked++
		case !t.Expires.IsZero() && time.Until(t.Expires) < 7*24*time.Hour:
			expiring++
		}
		if unused(t) {
			idle++
		}
	}
	warn := func(n int) string {
		if n > 0 {
			return styleWarn.Render(fmt.Sprint(n))
		}
		return styleDim.Render("0")
	}
	return kv([][2]string{
		{"Active", styleBold.Render(fmt.Sprint(m.activeTokens()))},
		{"Expiring in 7d", warn(expiring)},
		{"Unused 30d+", warn(idle)},
		{"Revoked/expired", styleDim.Render(fmt.Sprint(revoked))},
	})
}

func (m *model) recentFailures() int {
	n := 0
	since := time.Now().Add(-24 * time.Hour)
	for _, e := range m.audit {
		if e.At.After(since) && !e.OK {
			n++
		}
	}
	return n
}

func (m *model) activitySummary() string {
	if m.auditErr != nil && len(m.audit) == 0 {
		return errText(m.auditErr)
	}
	since := time.Now().Add(-24 * time.Hour)
	var total int
	for _, e := range m.audit {
		if e.At.After(since) {
			total++
		}
	}
	fails := styleDim.Render("0")
	if n := m.recentFailures(); n > 0 {
		fails = styleErr.Render(fmt.Sprint(n))
	}
	last := styleDim.Render("none")
	if len(m.audit) > 0 {
		e := m.audit[0]
		last = actionStyle(e.Action).Render(e.Action) + styleDim.Render(" by "+e.Actor+", "+relTime(e.At))
	}
	return kv([][2]string{
		{"Actions (24h)", styleBold.Render(fmt.Sprint(total))},
		{"Failed (24h)", fails},
		{"Latest", last},
	})
}

func scopeBadges(scopes []string) string {
	parts := make([]string, len(scopes))
	for i, s := range scopes {
		parts[i] = lipgloss.NewStyle().Foreground(scopeColors[s]).Render(safe(s))
	}
	return strings.Join(parts, " ")
}

// table formats cells into fixed-width columns; the last cell takes the rest of the line.
func table(cols []int, cells ...string) string {
	var b strings.Builder
	for i, c := range cells {
		if i < len(cols) {
			b.WriteString(pad(fit(c, cols[i]), cols[i]) + " ")
		} else {
			b.WriteString(c)
		}
	}
	return b.String()
}

func (m *model) selectedRow(line string) string { return rowIn(line, true, m.width) }

// rowIn is a table row cut to w columns, highlighted when it's the selected one.
func rowIn(line string, selected bool, w int) string {
	if !selected {
		return "   " + ansi.Truncate(line, max(w-3, 1), "…")
	}
	return " " + styleAccent.Render("▌") + " " + lipgloss.NewStyle().Background(colSel).Width(w-3).Render(ansi.Truncate(line, w-3, "…"))
}

var tokenCols = []int{18, 15, 8, 8, 10, 12, 7, 7, 4}

func (m *model) viewTokens() string {
	if m.tokensErr != nil {
		return m.errCard("Tokens", m.tokensErr, "")
	}
	if len(m.tokens) == 0 {
		return indent(card("No tokens yet", m.cardWidth(cardNarrow),
			styleDim.Render("Press n to create one for an agent, bot or script.\nEach gets its own scopes, chats, rate and expiry.")), " ")
	}
	detail := m.tokenDetail()
	lines := []string{"   " + styleHead.Render(table(tokenCols, "NAME", "SCOPES", "CHATS", "RATE", "EXPIRES", "LAST USED", "REQ/24H", "SENT", "LIVE", "CREATED BY"))}
	start, end := window(m.tokSel, len(m.tokens), m.bodyHeight()-2-lipgloss.Height(detail))
	for i := start; i < end; i++ {
		lines = append(lines, m.row(m.tokenRow(m.tokens[i]), i == m.tokSel))
	}
	lines = append(lines, "", indent(detail, " "))
	return strings.Join(lines, "\n")
}

func (m *model) tokenRow(t store.Token) string {
	chats := "all"
	if len(t.Chats) > 0 {
		chats = plural(len(t.Chats), "chat")
	}
	rate := "global"
	if t.Rate > 0 {
		rate = fmt.Sprintf("%d/min", t.Rate)
	}
	expires := "never"
	switch {
	case t.Revoked:
		expires = styleErr.Render("revoked")
	case !t.Expires.IsZero() && !tokenLive(t):
		expires = styleErr.Render("expired")
	case !t.Expires.IsZero() && time.Until(t.Expires) < 7*24*time.Hour:
		expires = styleWarn.Render(relTime(t.Expires))
	case !t.Expires.IsZero():
		expires = relTime(t.Expires)
	}
	lastUsed := styleDim.Render("never")
	if !t.LastUsed.IsZero() {
		lastUsed = relTime(t.LastUsed)
	}
	if unused(t) {
		lastUsed = styleWarn.Render(ansi.Strip(lastUsed) + " ⚠")
	}
	name, scopes := safe(t.Name), scopeBadges(t.Scopes)
	if !tokenLive(t) {
		name, scopes = styleDim.Strikethrough(true).Render(name), styleDim.Render(safe(strings.Join(t.Scopes, " ")))
	}
	reqs, sent, live := styleFaint.Render("–"), styleFaint.Render("–"), styleFaint.Render("–")
	if calls, sends, failed, streams, ok := m.callerUsage(t.Name); ok {
		reqs, sent = usageCell(calls, 1000), usageCell(sends, 0)
		if failed > 0 {
			sent += styleErr.Render(fmt.Sprintf("/%d✗", failed))
		}
		live = usageCell(int64(streams), 0)
	}
	return table(tokenCols, name, scopes, chats, rate, expires, lastUsed, reqs, sent, live, styleDim.Render(safe(cmp.Or(t.CreatedBy, "owner"))))
}

func (m *model) tokenDetail() string {
	if m.tokSel >= len(m.tokens) {
		return ""
	}
	t := m.tokens[m.tokSel]
	chats := "all chats"
	if len(t.Chats) > 0 {
		names := make([]string, len(t.Chats))
		for i, c := range t.Chats {
			names[i] = displayJID(safe(c))
		}
		chats = strings.Join(names, ", ")
	}
	expires := "never"
	if !t.Expires.IsZero() {
		expires = stamp(t.Expires, "2006-01-02 15:04") + styleDim.Render(" ("+relTime(t.Expires)+")")
	}
	created := stamp(t.Created, "2006-01-02 15:04") + styleDim.Render(" by "+safe(cmp.Or(t.CreatedBy, "the owner")))
	state := styleAccent.Render("● active")
	switch {
	case t.Revoked:
		state = styleErr.Render("● revoked")
	case !tokenLive(t):
		state = styleErr.Render("● expired")
	case unused(t):
		state = styleWarn.Render("● unused for 30+ days") + styleDim.Render(": consider revoking it")
	}
	activity := styleDim.Render("nothing since the server started")
	if calls, sends, failed, streams, ok := m.callerUsage(t.Name); ok {
		activity = fmt.Sprintf("%s, %s", plural(int(calls), "request"), plural(int(sends), "send"))
		if failed > 0 {
			activity += styleErr.Render(fmt.Sprintf(", %d failed", failed))
		}
		if streams > 0 {
			activity += styleAccent.Render(fmt.Sprintf(", %d open stream", streams))
		}
		activity += styleDim.Render(" (24 h)")
	}
	seen := styleDim.Render("not since the server started")
	for _, c := range m.stats.Callers {
		if c.Name == t.Name && !c.LastSeen.IsZero() {
			seen = relTime(c.LastSeen) + styleDim.Render(" from "+safe(cmp.Or(c.LastIP, "?")))
		}
	}
	left := kv([][2]string{{"ID", styleDim.Render(safe(t.ID))}, {"State", state}, {"Scopes", scopeBadges(t.Scopes)}, {"Chats", chats}, {"Activity", activity}})
	right := kv([][2]string{{"Created", created}, {"Expires", expires}, {"Rate", cmp.Or(rateText(t.Rate), "global limit only")}, {"Last seen", seen}})
	w := m.cardWidth(cardDetail)
	return card(safe(t.Name), w, lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width((w-4)/2).Render(left), right))
}

func rateText(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("%d sends per minute", n)
}

func actionStyle(action string) lipgloss.Style {
	switch {
	case strings.HasPrefix(action, "token"):
		return lipgloss.NewStyle().Foreground(colPurple)
	case action == "pair" || action == "logout" || action == "logged_out":
		return lipgloss.NewStyle().Foreground(colWarn)
	case action == "delete":
		return lipgloss.NewStyle().Foreground(colErr)
	}
	return styleInfo
}

func (m *model) viewAudit() string {
	if m.auditErr != nil && len(m.audit) == 0 {
		return m.errCard("Audit log", m.auditErr, "")
	}
	entries := m.visibleAudit()
	if len(entries) == 0 {
		msg := "The audit log is empty."
		if m.auditQuery != "" {
			msg = fmt.Sprintf("Nothing matches %q in the loaded entries. Esc clears the filter.", safe(m.auditQuery))
		}
		return indent(card("Audit log", m.cardWidth(cardMedium), styleDim.Render(msg)), " ")
	}
	cols := []int{2, 13, 16, 13, 22}
	detail := m.auditDetail(entries)
	lines := []string{"   " + styleHead.Render(table(cols, "", "WHEN", "ACTOR", "ACTION", "TARGET", "DETAIL"))}
	start, end := window(m.auditSel, len(entries), m.bodyHeight()-3-lipgloss.Height(detail))
	for i := start; i < end; i++ {
		e := entries[i]
		icon, detail := styleAccent.Render("✓"), styleDim.Render(e.Detail)
		if !e.OK {
			icon, detail = styleErr.Render("✗"), styleErr.Render(strings.Join(nonEmpty(e.Detail, e.Error), " · "))
		}
		line := table(cols, icon, styleDim.Render(when(e.At)), e.Actor, actionStyle(e.Action).Render(e.Action), displayJID(e.Target), detail)
		lines = append(lines, m.row(line, i == m.auditSel))
	}
	footer := fmt.Sprintf("%d of %d loaded", len(entries), len(m.audit))
	if m.auditQuery != "" {
		footer += fmt.Sprintf(" · filtered by %q (esc clears)", safe(m.auditQuery))
	}
	switch {
	case m.auditBusy:
		footer += " · loading…"
	case m.auditMore && m.auditQuery == "":
		footer += " · move past the last row for older entries"
	}
	lines = append(lines, "   "+styleFaint.Render(footer), indent(detail, " "))
	return strings.Join(lines, "\n")
}

func (m *model) auditDetail(entries []store.AuditEntry) string {
	if m.auditSel >= len(entries) {
		return ""
	}
	e := entries[m.auditSel]
	result := styleAccent.Render("✓ ok")
	if !e.OK {
		result = styleErr.Render("✗ " + e.Error)
	}
	rows := [][2]string{
		{"When", stamp(e.At, "2006-01-02 15:04:05") + styleDim.Render(" ("+relTime(e.At)+")")},
		{"Who", e.Actor},
		{"Target", cmp.Or(displayJID(e.Target), styleDim.Render("none"))},
		{"Detail", cmp.Or(e.Detail, styleDim.Render("none"))},
		{"Result", result},
	}
	return card(fmt.Sprintf("#%d %s", e.ID, e.Action), m.cardWidth(cardDetail), kv(rows))
}

func (m *model) viewServers() string {
	lines := []string{styleBold.Render("Servers"), styleDim.Render("Saved in ~/.config/wame/servers/NAME.env"), ""}
	if len(m.opts.Profiles) == 0 {
		lines = append(lines, styleDim.Render("None saved."))
	}
	for i, p := range m.opts.Profiles {
		mark := "  "
		if p.Name == m.profile {
			mark = styleAccent.Render("● ")
		}
		line := mark + pad(p.Name, 14) + " " + styleDim.Render(p.Server)
		if p.Insecure {
			line += " " + styleWarn.Render("insecure")
		}
		if i == m.serverSel {
			line = lipgloss.NewStyle().Background(colSel).Render(line)
		}
		lines = append(lines, line)
	}
	return lipgloss.NewStyle().Width(min(m.width-12, 70)).Render(strings.Join(lines, "\n"))
}

func (m *model) viewForm() string {
	lines := []string{styleBold.Render("New token"), styleDim.Render("For an agent, bot or script. Only its hash is stored; you'll see it once."), ""}
	for i, f := range formFields {
		label := styleDim.Render(pad(f.label, 8))
		marker := "  "
		if i == m.formFocus {
			label, marker = styleAccent.Render(pad(f.label, 8)), styleAccent.Render("› ")
		}
		lines = append(lines, marker+label+" "+m.form[i].View())
	}
	lines = append(lines, "",
		styleDim.Render("With an admin token rather than the owner secret, you can't grant"),
		styleDim.Render("the admin scope, and the new token expires no later than yours."))
	return lipgloss.NewStyle().Width(min(m.width-12, 84)).Render(strings.Join(lines, "\n"))
}

func (m *model) viewCreated() string {
	title := "✓ Token created"
	if m.replaced != "" {
		title = "✓ Token rotated"
	}
	out := styleAccent.Render(title) + "\n\n" +
		lipgloss.NewStyle().Background(colSel).Padding(0, 1).Render(m.created) + "\n\n" +
		styleWarn.Render("Store it now: it can't be shown again.") + "\n"
	if m.replaced != "" {
		out += styleDim.Render("It replaces "+m.replaced+", which no longer works.") + "\n"
	}
	return out + styleDim.Render("Send it as  Authorization: Bearer <token>")
}

func (m *model) viewPair() string {
	return styleBold.Render("Pair with a phone number") + "\n" +
		styleDim.Render("The server asks WhatsApp for an 8-character code to enter on that phone.") + "\n\n" +
		m.prompt.View()
}

func (m *model) viewConfirmLogout() string {
	return styleErr.Render("Log out of WhatsApp?") + "\n\n" +
		"This unlinks the device and deletes the session on\n" + styleBold.Render(m.c.base.Host) + ". It needs a new QR or pairing code after.\n" +
		"It also deletes your contacts (notes too), the WhatsApp profiles\nand the group cache; messages and opt-outs stay.\n\n" +
		styleKey.Render("y") + " log out   " + styleKey.Render("any key") + " cancel"
}

func (m *model) viewConfirmRevoke() string {
	t := m.confirmTok
	return styleErr.Render("Revoke "+safe(t.Name)+"?") + "\n\n" +
		"It stops working at once, with every token it created.\nOpen streams close within 10 s. This can't be undone.\n\n" +
		styleKey.Render("y") + " revoke   " + styleKey.Render("any key") + " cancel"
}

func (m *model) viewConfirmRotate() string {
	t := m.confirmTok
	r := rotationRequest(t, m.tokens)
	life := "never expires"
	if r.ExpiresIn != "" {
		life = "expires in " + r.ExpiresIn
	}
	return styleBold.Render("Rotate "+safe(t.Name)+"?") + "\n\n" +
		"Creates " + styleBold.Render(safe(r.Name)) + " with the same scopes, chats and rate\n(" + life + "), shows it once, then revokes " + safe(t.Name) + "\nand every token it created.\n\n" +
		styleKey.Render("y") + " rotate   " + styleKey.Render("any key") + " cancel"
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// listRows is how many list rows fit under lines, which may hold multi-line cards.
func (m *model) listRows(lines []string) int {
	return max(m.bodyHeight()-lipgloss.Height(strings.Join(lines, "\n")), 3)
}

// window returns the slice [start, end) of n rows to show in h lines, keeping sel visible.
func window(sel, n, h int) (int, int) {
	h = max(h, 1)
	if n <= h {
		return 0, n
	}
	start := min(max(sel-h/2, 0), n-h)
	return start, start + h
}

func fit(s string, w int) string {
	if ansi.StringWidth(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "…")
}

func pad(s string, w int) string {
	if n := ansi.StringWidth(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

func nonEmpty(parts ...string) []string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

const whenWidth = len("Mon 15:04")

// when is a compact timestamp padded to whenWidth, so it fills a column of its own: the time today,
// the weekday and time this week, the date this year, the month and year before.
func when(t time.Time) string {
	var s string
	now := time.Now()
	switch t = t.Local(); {
	case t.IsZero():
	case t.YearDay() == now.YearDay() && t.Year() == now.Year():
		s = t.Format("15:04")
	case now.Sub(t) >= 0 && now.Sub(t) < 6*24*time.Hour:
		s = t.Format("Mon 15:04")
	case t.Year() == now.Year():
		s = t.Format("Jan 02")
	default:
		s = t.Format("Jan 2006")
	}
	return pad(s, whenWidth)
}

// stamp formats a time from the server in the local zone; JSON keeps the server's (often UTC).
func stamp(t time.Time, layout string) string { return t.Local().Format(layout) }

// quietBadge advertises the queue's quiet hours in the header while they're switched on: amber while
// they hold the queue, dim otherwise. Empty when no window is set or the switch is off.
func (m *model) quietBadge() string {
	q := m.stats.Queue
	switch {
	case q.QuietHours == "" || q.QuietOff:
		return ""
	case !q.QuietUntil.IsZero():
		return styleWarn.Render("☾ quiet until " + stamp(q.QuietUntil, "15:04"))
	}
	return styleDim.Render("☾ quiet " + safe(q.QuietHours))
}

func displayJID(jid string) string {
	if strings.HasSuffix(jid, "@s.whatsapp.net") {
		return "+" + userOf(jid)
	}
	return jid
}

func relTime(t time.Time) string {
	d := time.Until(t)
	future := d > 0
	if !future {
		d = -d
	}
	var s string
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		s = fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		s = fmt.Sprintf("%dh", int(d.Hours()))
	default:
		s = fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	if future {
		return "in " + s
	}
	return s + " ago"
}

func userOf(jid string) string { return termui.UserOf(jid) }
