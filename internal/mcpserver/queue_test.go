package mcpserver

import (
	"strings"
	"testing"
	"time"

	"github.com/williamtcastro/wame/internal/auth"
	"github.com/williamtcastro/wame/internal/bot"
	"github.com/williamtcastro/wame/internal/store"
)

func restrictedPrincipal(t *testing.T, b *bot.Bot, name string, chats ...string) auth.Principal {
	t.Helper()
	plain, _, err := auth.Issue(b.Store, auth.Request{Name: name, Scopes: []auth.Scope{auth.Send}, Chats: chats})
	if err != nil {
		t.Fatal(err)
	}
	p, err := (&auth.Authenticator{Store: b.Store}).Authenticate(plain)
	if err != nil || !p.Restricted() {
		t.Fatalf("restricted token: %v %v", p, err)
	}
	return p
}

func TestScheduleNeedsSendScope(t *testing.T) {
	b := newBot(t)
	cs := connect(t, b, tokenPrincipal(t, b, "reader", []auth.Scope{auth.Read}, 0))
	for tool, args := range map[string]map[string]any{
		"schedule_message":     {"to": maria, "text": "oi", "send_at": "2h"},
		"list_scheduled":       nil,
		"cancel_scheduled":     {"batch": "x"},
		"reschedule_scheduled": {"id": 1, "send_at": "2h"},
	} {
		if _, text, isErr := call(t, cs, tool, args); !isErr || !strings.Contains(text, "send scope") {
			t.Errorf("read-only %s = %q, want refused", tool, text)
		}
	}
	if c, _ := b.Store.CountOutbox(store.OutboxFilter{}); c.Queued != 0 {
		t.Fatal("a read-only caller queued a message")
	}
}

func TestScheduleAllowlist(t *testing.T) {
	b := newBot(t)
	cs := connect(t, b, restrictedPrincipal(t, b, "maria-bot", maria))
	if _, text, isErr := call(t, cs, "schedule_message", map[string]any{"to": "5511988887777", "text": "oi"}); !isErr || !strings.Contains(text, "can't access") {
		t.Fatalf("restricted schedule to another chat = %q", text)
	}
	out, text, isErr := call(t, cs, "schedule_message", map[string]any{"to": "+55 11 99999-9999", "text": "oi Maria"})
	if isErr || out["to"] != maria {
		t.Fatalf("restricted schedule to an allowed number = %v %q", out, text)
	}
	if !strings.Contains(out["note"].(string), "no queue worker") {
		t.Fatalf("note = %q, want the missing worker explained", out["note"])
	}

	cs = connect(t, b, tokenPrincipal(t, b, "open-bot", []auth.Scope{auth.Send}, 0))
	if out, text, isErr := call(t, cs, "schedule_message", map[string]any{"to": "5521987654321", "text": "oi Ana"}); isErr || out["to"] != "5521987654321" {
		t.Fatalf("unrestricted schedule (resolved at send time, no lookup now) = %v %q", out, text)
	}
}

func TestScheduleOwnItemsOnly(t *testing.T) {
	b := newBot(t)
	mariaBot := connect(t, b, tokenPrincipal(t, b, "maria-bot", []auth.Scope{auth.Send}, 0))
	johnBot := connect(t, b, tokenPrincipal(t, b, "john-bot", []auth.Scope{auth.Send}, 0))
	admin := connect(t, b, auth.Owner())

	mine, _, _ := call(t, mariaBot, "schedule_message", map[string]any{"to": maria, "text": "segredo da Maria‮", "send_at": "1h", "batch": "promo"})
	theirs, _, _ := call(t, johnBot, "schedule_message", map[string]any{"to": john, "text": "segredo do John", "send_at": "1h", "batch": "promo"})
	mineID, theirsID := mine["id"].(float64), theirs["id"].(float64)

	out, _, _ := call(t, mariaBot, "list_scheduled", nil)
	items := out["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != mineID {
		t.Fatalf("maria-bot sees %v, want only its own item", items)
	}
	if text := items[0].(map[string]any)["untrusted"].(map[string]any)["text"]; text != "segredo da Maria" {
		t.Fatalf("own text = %q, want it cleaned", text)
	}
	if out["counts"].(map[string]any)["queued"] != float64(1) {
		t.Fatalf("counts = %v, want only its own", out["counts"])
	}

	if _, text, isErr := call(t, johnBot, "cancel_scheduled", map[string]any{"id": mineID}); !isErr || !strings.Contains(text, "no queued message") {
		t.Fatalf("john-bot canceling maria-bot's item = %q", text)
	}
	if out, _, _ := call(t, johnBot, "cancel_scheduled", map[string]any{"batch": "promo"}); len(out["canceled"].([]any)) != 1 || out["canceled"].([]any)[0] != theirsID {
		t.Fatalf("john-bot's batch cancel = %v, want only its own item", out)
	}
	if it, _ := b.Store.OutboxItem(int64(mineID)); it.Status != "queued" {
		t.Fatalf("maria-bot's item is %s after john-bot's cancel", it.Status)
	}

	out, _, _ = call(t, admin, "list_scheduled", nil)
	if items := out["items"].([]any); len(items) != 2 {
		t.Fatalf("admin sees %d items, want 2", len(items))
	}
	for _, it := range out["items"].([]any) {
		if text := it.(map[string]any)["untrusted"].(map[string]any)["text"]; text != nil {
			t.Fatalf("admin was shown another actor's text: %q", text)
		}
	}
	if _, text, isErr := call(t, admin, "cancel_scheduled", map[string]any{"id": theirsID}); !isErr || !strings.Contains(text, "is canceled") {
		t.Fatalf("canceling a finished item = %q", text)
	}
	if out, _, isErr := call(t, admin, "cancel_scheduled", map[string]any{"id": mineID}); isErr || len(out["canceled"].([]any)) != 1 {
		t.Fatalf("admin cancel = %v", out)
	}
	if _, text, isErr := call(t, admin, "cancel_scheduled", map[string]any{"id": mineID, "batch": "promo"}); !isErr || !strings.Contains(text, "either") {
		t.Fatalf("id and batch together = %q", text)
	}
}

func TestListScheduledReceipts(t *testing.T) {
	b := newBot(t)
	cs := connect(t, b, tokenPrincipal(t, b, "maria-bot", []auth.Scope{auth.Send}, 0))
	out, _, _ := call(t, cs, "schedule_message", map[string]any{"to": maria, "text": "oi Maria"})
	id := int64(out["id"].(float64))
	if _, err := b.Store.ClaimOutbox(id, "3EB0AAAA"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Store.FinishOutbox(id, store.OutboxResult{Status: store.OutboxSent}); err != nil {
		t.Fatal(err)
	}
	item := func() map[string]any {
		out, _, _ := call(t, cs, "list_scheduled", nil)
		return out["items"].([]any)[0].(map[string]any)
	}
	if it := item(); it["delivered_at"] != nil || it["read_at"] != nil {
		t.Fatalf("before any receipt: %v", it)
	}
	if _, err := b.Store.MarkOutboxReceipt([]string{"3EB0AAAA"}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if it := item(); it["delivered_at"] == nil || it["read_at"] == nil {
		t.Fatalf("after a read receipt: %v", it)
	}
}

func TestScheduleTimes(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"":                          {},
		"90m":                       now.Add(90 * time.Minute),
		" 2h ":                      now.Add(2 * time.Hour),
		"3d":                        now.Add(72 * time.Hour),
		"2026-10-14T09:00:00-03:00": time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC),
		"2026-10-14 09:00":          time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC), // on the recipient's clock
		"13:00":                     time.Date(2026, 10, 13, 13, 0, 0, 0, time.UTC),
		"12:00":                     time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC), // passed today: tomorrow's
		"tomorrow 08:30":            time.Date(2026, 10, 14, 8, 30, 0, 0, time.UTC),
	} {
		if got, err := parseSendAt(in, now); err != nil || !got.Equal(want) {
			t.Errorf("parseSendAt(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"-1h", "0s", "d", "1.5d", "-3d", "tomorrow", "25:00", "2026-10-13T09:00:00Z"} {
		if _, err := parseSendAt(in, now); err == nil {
			t.Errorf("parseSendAt(%q) accepted", in)
		}
	}

	b := newBot(t)
	cs := connect(t, b, tokenPrincipal(t, b, "agent", []auth.Scope{auth.Send}, 0))
	out, text, isErr := call(t, cs, "schedule_message", map[string]any{"to": maria, "text": "oi", "send_at": "2h"})
	if isErr {
		t.Fatal(text)
	}
	due, _ := time.Parse(time.RFC3339Nano, out["due"].(string))
	if d := time.Until(due); d < 119*time.Minute || d > 2*time.Hour {
		t.Fatalf("due in %v, want 2h", d)
	}
	for _, far := range []string{"40d", "9999999d", "800000h"} {
		if _, text, isErr := call(t, cs, "schedule_message", map[string]any{"to": maria, "text": "oi", "send_at": far}); !isErr || !strings.Contains(text, "days ahead") {
			t.Fatalf("%s = %q, want the queue's limit", far, text)
		}
	}
}

func TestInstructionsCoverScheduling(t *testing.T) {
	cs := connect(t, newBot(t), auth.Owner())
	ins := cs.InitializeResult().Instructions
	for _, want := range []string{"schedule", "when the user asked", "time zone"} {
		if !strings.Contains(ins, want) {
			t.Errorf("server instructions lack %q", want)
		}
	}
}

func TestRescheduleScheduled(t *testing.T) {
	b := newBot(t)
	agent := connect(t, b, tokenPrincipal(t, b, "agent", []auth.Scope{auth.Send}, 0))
	other := connect(t, b, tokenPrincipal(t, b, "other", []auth.Scope{auth.Send, auth.Admin}, 0))
	out, text, isErr := call(t, agent, "schedule_message", map[string]any{"to": maria, "text": "oi", "send_at": "2h", "send_before": "1h"})
	if isErr {
		t.Fatal(text)
	}
	id := out["id"]
	if _, text, isErr := call(t, other, "reschedule_scheduled", map[string]any{"id": id, "send_at": "3h"}); !isErr || !strings.Contains(text, "no queued message") {
		t.Fatalf("someone else's message, even as admin: %q", text)
	}
	if _, text, isErr := call(t, agent, "reschedule_scheduled", map[string]any{"id": id, "send_at": "5h"}); !isErr || !strings.Contains(text, "send_before") {
		t.Fatalf("past its deadline: %q", text)
	}
	out, text, isErr = call(t, agent, "reschedule_scheduled", map[string]any{"id": id, "send_at": "5h", "send_before": "none", "text": "oi de novo"})
	if isErr {
		t.Fatal(text)
	}
	due, _ := time.Parse(time.RFC3339Nano, out["due"].(string))
	if d := time.Until(due); d < 299*time.Minute || d > 5*time.Hour {
		t.Fatalf("due in %v, want 5h", d)
	}
	it, _ := b.Store.OutboxItem(int64(id.(float64)))
	if it.Text != "oi de novo" || !it.SendBefore.IsZero() {
		t.Fatalf("%+v", it)
	}
}
