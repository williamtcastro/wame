package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/williamtcastro/wame/internal/bot"
	"github.com/williamtcastro/wame/internal/store"
)

func TestSplitSendAt(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		in, text string
		sched    bool
		bad      bool
	}{
		{"hello", "hello", false, false},
		{"@2h hi there", "hi there", true, false},
		{"@tomorrow 09:00 good morning", "good morning", true, false},
		{"@2026-10-21T09:00 hi", "hi", true, false},
		{"@maria see you at 5", "@maria see you at 5", false, false},
		{"@5511999999999 hi", "@5511999999999 hi", false, false},
		{"@25:00 hi", "", true, true},
		{"@tomorow 09:00 hi", "", true, true},
		{"@2h", "", true, true},
		{"@ hi", "@ hi", false, false},
	} {
		when, text, sched, err := splitSendAt(c.in, now)
		if sched != c.sched || (err != nil) != c.bad || (!c.bad && text != c.text) {
			t.Errorf("%q: text %q scheduled %v err %v", c.in, text, sched, err)
		}
		if c.sched && !c.bad && when.IsZero() {
			t.Errorf("%q: no time", c.in)
		}
	}
}

func TestComposeSchedules(t *testing.T) {
	b, c := newServer(t)
	m := newTestModel(t, Options{Client: c})
	press(m, "2")
	m.msgsChat = "5511999999999@s.whatsapp.net"

	press(m, "i", "@25:00 hi", "enter")
	if m.mode != modeCompose || !m.noticeErr || m.prompt.Value() != "@25:00 hi" {
		t.Fatalf("a bad WHEN must keep the text: mode %v notice %q value %q", m.mode, m.notice, m.prompt.Value())
	}
	if !strings.Contains(ansi.Strip(m.View()), "✗") {
		t.Fatal("the parser's error isn't shown")
	}
	press(m, "esc")
	if items, _ := b.Store.ListOutbox(store.OutboxFilter{}, 10); len(items) != 0 {
		t.Fatalf("a bad WHEN queued %+v", items)
	}

	before := time.Now()
	press(m, "i", "@2h hi", "enter")
	items, _ := b.Store.ListOutbox(store.OutboxFilter{}, 10)
	if len(items) != 1 || items[0].Text != "hi" || items[0].Status != store.OutboxQueued {
		t.Fatalf("items = %+v, notice %q", items, m.notice)
	}
	if d := items[0].Due.Sub(before); d < 119*time.Minute || d > 121*time.Minute {
		t.Fatalf("due in %v, want 2h", d)
	}
	if !strings.HasPrefix(m.notice, "scheduled #") || m.noticeErr {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestQueueRescheduleAndEdit(t *testing.T) {
	b, c := newServer(t)
	items, err := b.Enqueue("api", "", "promo", "", []bot.QueueRequest{{To: "5511999999999", Text: "Hi Maria"}, {To: "5511988887777", Text: "Hi John"}})
	if err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t, Options{Client: c})
	press(m, "7")
	id := m.queue.Items[m.queueSel].ID

	press(m, "t")
	if m.mode != modeQueueReschedule || !strings.Contains(m.prompt.Placeholder, "reschedule #") {
		t.Fatalf("t opened mode %v %q", m.mode, m.prompt.Placeholder)
	}
	press(m, "esc", "t", "nonsense", "enter")
	if !m.noticeErr || m.mode != modeQueueReschedule || m.prompt.Value() != "nonsense" {
		t.Fatalf("a bad time must say so and keep the prompt: %q %v %q", m.notice, m.mode, m.prompt.Value())
	}
	m.prompt.SetValue("")
	before := time.Now()
	press(m, "3h", "enter")
	it, _ := b.Store.OutboxItem(id)
	if d := it.Due.Sub(before); d < 179*time.Minute || d > 181*time.Minute {
		t.Fatalf("after t 3h: due in %v, notice %q", d, m.notice)
	}
	if !strings.HasPrefix(m.notice, "rescheduled #") {
		t.Fatalf("notice = %q", m.notice)
	}

	press(m, "e")
	if m.mode != modeQueueEdit || m.prompt.Value() != "" || !strings.Contains(m.prompt.Placeholder, "replace the text of #") {
		t.Fatalf("e opened mode %v value %q", m.mode, m.prompt.Value())
	}
	if strings.Contains(ansi.Strip(m.View()), "Hi Maria") || strings.Contains(ansi.Strip(m.View()), "Hi John") {
		t.Fatal("the edit prompt must not show the text")
	}
	press(m, "brand new text", "enter")
	it, _ = b.Store.OutboxItem(id)
	if it.Text != "brand new text" || it.Edited.IsZero() {
		t.Fatalf("after e: %+v, notice %q", it, m.notice)
	}
	if view := ansi.Strip(m.View()); strings.Contains(view, "brand new text") || !strings.Contains(view, "✎") {
		t.Fatalf("an edited item is marked, its text never shown:\n%s", view)
	}

	press(m, "x", "t")
	if m.mode != modeNormal || !m.noticeErr {
		t.Fatalf("t on a canceled item: mode %v notice %q", m.mode, m.notice)
	}
	press(m, "e")
	if m.mode != modeNormal || !m.noticeErr {
		t.Fatalf("e on a canceled item: mode %v notice %q", m.mode, m.notice)
	}
	_ = items
}

func TestQueueExpired(t *testing.T) {
	b, c := newServer(t)
	if _, err := b.Enqueue("api", "", "promo", "", []bot.QueueRequest{{To: "5511999999999", Text: "Hi"}}); err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t, Options{Client: c})
	press(m, "7")
	seen := map[string]bool{}
	for range store.OutboxStates {
		press(m, "f")
		seen[m.queueStatus] = true
	}
	if !seen[store.OutboxExpired] {
		t.Fatalf("f never reached expired: %v", seen)
	}
	press(m, "f")
	for m.queueStatus != store.OutboxExpired {
		press(m, "f")
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "showing expired") || !strings.Contains(view, "expired, kept a week") {
		t.Fatalf("expired not shown:\n%s", view)
	}
}

func TestSendAtRefusesOldServer(t *testing.T) {
	sent := false
	old := http.NewServeMux()
	old.HandleFunc("GET /queue/{id}", func(w http.ResponseWriter, r *http.Request) {})
	old.HandleFunc("POST /send", func(w http.ResponseWriter, r *http.Request) { sent = true })
	srv := httptest.NewServer(old)
	defer srv.Close()
	c, err := NewClient(srv.URL, "tok", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SendAt(t.Context(), "5511999999999", "hi", "k", "2026-10-21T09:00", ""); !errors.Is(err, ErrNoScheduling) || sent {
		t.Fatalf("an old server: %v, sent %v", err, sent)
	}
}
