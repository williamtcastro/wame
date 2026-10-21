package admin

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/williamtcastro/wame/internal/bot"
	"github.com/williamtcastro/wame/internal/store"
	"github.com/williamtcastro/wame/internal/termui"
)

const (
	queueLimit    = 200
	queueDebounce = time.Second
)

type (
	queueMsg struct {
		page QueuePage
		err  error
	}
	queueReloadMsg struct{}
)

// queueChanged reloads the queue and stats once per queueDebounce however many queue events arrive:
// a large batch sends two per message.
func (m *model) queueChanged() tea.Cmd {
	return m.queueReload.Schedule(queueDebounce, queueReloadMsg{})
}

func (m *model) reloadQueue() tea.Cmd {
	m.queueReload.Done()
	if m.tab != tabQueue {
		return m.loadStats()
	}
	return tea.Batch(m.loadQueue(), m.loadStats())
}

// queueFilters is the order f cycles through on the Queue tab; "" shows every state.
var queueFilters = append([]string{""}, store.OutboxStates...)

func (m *model) loadQueue() tea.Cmd {
	status := m.queueStatus
	return m.call(func(ctx context.Context, c *Client) tea.Msg {
		p, err := c.Queue(ctx, status, queueLimit)
		return queueMsg{p, err}
	})
}

func (m *model) applyQueue(msg queueMsg) {
	m.queueErr = msg.err
	if msg.err == nil {
		m.queue = msg.page
		m.queueSel = clamp(m.queueSel, len(m.queue.Items))
	}
}

func (m *model) selectedQueued() (store.OutboxItem, bool) {
	if m.queueSel < len(m.queue.Items) {
		return m.queue.Items[m.queueSel], true
	}
	return store.OutboxItem{}, false
}

func (m *model) cancelQueued(id int64, batch, what string) tea.Cmd {
	return m.call(func(ctx context.Context, c *Client) tea.Msg {
		n, err := c.CancelQueued(ctx, id, batch)
		return doneMsg{"canceled " + plural(n, "queued message") + " (" + what + ")", err}
	})
}

func (m *model) rescheduleQueued(v string) tea.Cmd {
	id := m.queueEditID
	when, err := bot.ParseSendTime(v, time.Now(), true)
	if err != nil {
		m.mode = modeQueueReschedule
		m.prompt.Focus()
		m.setNotice(err.Error(), true)
		return nil
	}
	return m.call(func(ctx context.Context, c *Client) tea.Msg {
		_, err := c.EditQueued(ctx, id, map[string]any{"send_at": when.String()})
		return doneMsg{fmt.Sprintf("rescheduled #%d for %s", id, whenLabel(when)), err}
	})
}

func (m *model) replaceQueuedText(v string) tea.Cmd {
	id := m.queueEditID
	return m.call(func(ctx context.Context, c *Client) tea.Msg {
		_, err := c.EditQueued(ctx, id, map[string]any{"text": v})
		return doneMsg{fmt.Sprintf("replaced the text of #%d", id), err}
	})
}

func (m *model) queueKey(k string) tea.Cmd {
	if m.queueOptOuts {
		return m.optOutsKey(k)
	}
	if m.queueFiles {
		return m.queueFilesKey(k)
	}
	if sel, ok := termui.ListMove(k, m.queueSel, len(m.queue.Items), m.bodyHeight()/2); ok {
		m.queueSel = sel
		return nil
	}
	it, ok := m.selectedQueued()
	switch k {
	case "f":
		for i, s := range queueFilters {
			if s == m.queueStatus {
				m.queueStatus = queueFilters[(i+1)%len(queueFilters)]
				break
			}
		}
		m.queueSel = 0
		return m.loadQueue()
	case "Q":
		if m.stats.Queue.QuietHours == "" {
			m.setNotice("no quiet hours set: add a window on the Settings tab first", true)
			return nil
		}
		next := strconv.FormatBool(m.stats.Queue.QuietOff)
		return m.changeSetting("queue_quiet", &next, map[string]string{"true": "quiet hours on", "false": "quiet hours off"}[next])
	case "P":
		next := strconv.FormatBool(!m.stats.Queue.Paused)
		return m.changeSetting("queue_paused", &next, map[string]string{"true": "queue paused", "false": "queue resumed"}[next])
	case "x":
		if !ok || it.Status != store.OutboxQueued {
			m.setNotice("only queued messages can be canceled", true)
			return nil
		}
		return m.cancelQueued(it.ID, "", fmt.Sprintf("#%d", it.ID))
	case "t", "e":
		if !ok || it.Status != store.OutboxQueued {
			m.setNotice("only queued messages can be rescheduled or edited", true)
			return nil
		}
		m.queueEditID = it.ID
		if k == "t" {
			m.startPrompt(modeQueueReschedule, fmt.Sprintf("reschedule #%d (2h, 09:00, tomorrow 09:00, 2026-10-21T09:00)", it.ID))
		} else {
			m.startPrompt(modeQueueEdit, fmt.Sprintf("replace the text of #%d (the panel never shows it)", it.ID))
		}
	case "o":
		m.queueOptOuts = true
		return m.loadOptOuts()
	case "F":
		m.queueFiles = true
		return m.loadQueueFiles()
	case "X":
		if !ok {
			return nil
		}
		m.confirmBatch = it.Batch
		m.mode = modeConfirmCancelBatch
	}
	return nil
}

var queueStyles = map[string]func(...string) string{
	store.OutboxQueued:   styleAccent.Render,
	store.OutboxSending:  styleWarn.Render,
	store.OutboxSent:     styleDim.Render,
	store.OutboxFailed:   styleErr.Render,
	store.OutboxCanceled: styleFaint.Render,
	store.OutboxExpired:  styleWarn.Render,
}

// queueState shows a sent item's receipts the way WhatsApp does: ✓ sent, ✓✓ delivered, blue ✓✓ read.
func queueState(it store.OutboxItem) string {
	switch it.Progress() {
	case store.OutboxRead:
		return styleInfo.Render("✓✓ read")
	case store.OutboxDelivered:
		return "✓✓ delivered"
	case store.OutboxSent:
		return styleDim.Render("✓ sent")
	}
	if style, ok := queueStyles[it.Status]; ok {
		return style(it.Status)
	}
	return styleDim.Render(safe(it.Status))
}

// queueBody renders the Send queue card; optOuts < 0 leaves out the opt-out count, optOutsKey is
// what o does from here.
func queueBody(q bot.QueueStats, optOuts int, optOutsKey string) string {
	state := styleAccent.Render("sending")
	switch {
	case !q.Running:
		state = styleWarn.Render("no worker") + styleDim.Render(" (serve or tui must be running)")
	case q.Paused:
		state = styleWarn.Render("paused")
	case !q.QuietUntil.IsZero():
		state = styleWarn.Render("quiet hours") + styleDim.Render(" until "+q.QuietUntil.Format("15:04")+" "+safe(q.Timezone))
	case !q.CooldownUntil.IsZero():
		state = styleWarn.Render("backing off") + styleDim.Render(" until "+stamp(q.CooldownUntil, "15:04:05"))
	case q.Queued == 0:
		state = styleDim.Render("idle")
	}
	rows := [][2]string{
		{"State", state},
		{"Waiting", styleBold.Render(fmt.Sprint(q.Queued+q.Sending)) + styleDim.Render(fmt.Sprintf("  (%d sent, %d failed, %d canceled, %d expired, kept a week)", q.Sent, q.Failed, q.Canceled, q.Expired))},
	}
	if q.Sent > 0 {
		rows = append(rows, [2]string{"Receipts", fmt.Sprintf("%d delivered", q.Delivered) + styleDim.Render(" · ") +
			styleInfo.Render(fmt.Sprintf("%d read", q.Read)) + styleDim.Render(fmt.Sprintf(" of %d sent", q.Sent))})
	}
	if q.QuietHours != "" {
		quiet := safe(q.QuietHours) + styleDim.Render(" "+safe(q.Timezone))
		if q.QuietOff {
			quiet = styleDim.Render(safe(q.QuietHours)+" "+safe(q.Timezone)+" · ") + styleWarn.Render("off")
		}
		rows = append(rows, [2]string{"Quiet hours", quiet})
	}
	if !q.OldestQueued.IsZero() && q.Queued > 0 {
		rows = append(rows, [2]string{"Oldest", relTime(q.OldestQueued)})
	}
	if !q.NextDue.IsZero() && q.Queued > 0 && time.Until(q.NextDue) > time.Second {
		rows = append(rows, [2]string{"Next due", relTime(q.NextDue)})
	}
	rows = append(rows, [2]string{"Jitter", "0–" + (time.Duration(q.JitterMS) * time.Millisecond).String()})
	if optOuts >= 0 {
		rows = append(rows, [2]string{"Opt-outs", fmt.Sprint(optOuts) + styleDim.Render(" · o "+optOutsKey)})
	}
	if q.LastError != "" {
		rows = append(rows, [2]string{"Last error", styleErr.Render(fit(safe(q.LastError), 50)) + styleDim.Render(" "+relTime(q.LastErrorAt))})
	}
	return kv(rows)
}

func (m *model) viewQueue() string {
	if m.queueErr != nil {
		return m.errCard("Queue", m.queueErr, "the send queue (GET /queue)")
	}
	optOutsKey := "lists them"
	if m.queueOptOuts {
		optOutsKey = "back"
	}
	lines := []string{indent(card("Send queue", m.cardWidth(cardWide), queueBody(m.stats.Queue, m.optOutCount(), optOutsKey)), " "), ""}
	if m.queueOptOuts {
		return m.viewOptOuts(lines)
	}
	if m.queueFiles {
		return m.viewQueueFiles(lines)
	}
	filter := "all"
	if m.queueStatus != "" {
		filter = m.queueStatus
	}
	lines = append(lines, "   "+styleDim.Render(fmt.Sprintf("showing %s · %d of %d", filter, len(m.queue.Items), m.queue.Counts.Of(m.queueStatus))))
	cols := []int{6, 14, 18, 15, 13, 4, 12, 12}
	head := []string{"ID", "BATCH", "TO", "TYPE", "STATE", "TRY", "BY", "DUE"}
	before := slices.ContainsFunc(m.queue.Items, func(it store.OutboxItem) bool { return !it.SendBefore.IsZero() })
	if before {
		cols, head = append(cols, 12), append(head, "BEFORE")
	}
	lines = append(lines, "   "+styleHead.Render(table(cols, append(head, "ERROR")...)))
	if len(m.queue.Items) == 0 {
		return strings.Join(append(lines, "   "+styleDim.Render("nothing here")), "\n")
	}
	start, end := window(m.queueSel, len(m.queue.Items), m.listRows(lines))
	for i := start; i < end; i++ {
		it := m.queue.Items[i]
		due := relTime(it.Due)
		if it.Status != store.OutboxQueued {
			due = styleDim.Render(relTime(it.Updated))
		}
		id := fmt.Sprint(it.ID)
		if !it.Edited.IsZero() {
			id += styleDim.Render("✎")
		}
		cells := []string{id, fit(safe(it.Batch), 13), displayJID(safe(cmp.Or(it.Chat, it.To))),
			queueType(it), queueState(it), fmt.Sprint(it.Attempts), fit(safe(it.Actor), 11), due}
		if before {
			cell := ""
			if !it.SendBefore.IsZero() {
				cell = relTime(it.SendBefore)
			}
			cells = append(cells, styleDim.Render(cell))
		}
		line := table(cols, append(cells, styleErr.Render(safe(it.Error)))...)
		lines = append(lines, m.row(line, i == m.queueSel))
	}
	return strings.Join(lines, "\n")
}

// queueType is an item's type and, for a file, its size; never the file's name.
func queueType(it store.OutboxItem) string {
	if it.FileKind == "" {
		return styleDim.Render("text")
	}
	return safe(it.FileKind) + styleDim.Render(" "+byteSize(it.FileSize))
}

// byteSize shows a size in B, KB, MB or GB (powers of 1024), one decimal from MB up.
func byteSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d B", n)
}

func (m *model) viewConfirmCancelBatch() string {
	return styleBold.Render("Cancel batch "+safe(m.confirmBatch)+"?") + "\n\n" +
		styleDim.Render("Every message of it still queued is canceled. Sent ones stay sent.") + "\n\n" +
		styleKey.Render("y") + styleDim.Render(" cancel the batch   any other key: keep it")
}
