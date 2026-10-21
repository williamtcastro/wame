package bot

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/williamtcastro/wame/internal/store"
)

func TestParseSendTime(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		in       string
		relative bool
		want     time.Time
		local    bool
	}{
		{"", false, time.Time{}, false},
		{"2026-10-14T09:00:00-03:00", false, time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC), false},
		{"2026-10-14T09:00", false, time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC), true},
		{"2026-10-14T09:00:30", false, time.Date(2026, 10, 14, 9, 0, 30, 0, time.UTC), true},
		{"2026-10-14 09:00", true, time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC), true},
		{"2h", true, now.Add(2 * time.Hour), false},
		{"3d", true, now.Add(72 * time.Hour), false},
		{"tomorrow 09:00", true, time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC), true},
		{"Tomorrow  9:05", true, time.Date(2026, 10, 14, 9, 5, 0, 0, time.UTC), true},
		{"18:00", true, time.Date(2026, 10, 13, 18, 0, 0, 0, time.UTC), true},
		{"11:59", true, time.Date(2026, 10, 14, 11, 59, 0, 0, time.UTC), true},
	} {
		got, err := ParseSendTime(c.in, now, c.relative)
		if err != nil || !got.Equal(c.want) || got.Local != c.local {
			t.Errorf("ParseSendTime(%q, %v) = %v %v, %v; want %v %v", c.in, c.relative, got, got.Local, err, c.want, c.local)
		}
		// A time of day goes out as that day on the typing side's calendar (the API takes no time of day).
		if back, err := ParseSendTime(got.String(), now, false); err != nil || back.Time != got.Time || back.Local != got.Local {
			t.Errorf("%q doesn't round-trip through %q: %v %v", c.in, got.String(), back, err)
		}
	}
	for _, c := range []struct {
		in       string
		relative bool
	}{
		{"2h", false}, {"09:00", false}, {"tomorrow 09:00", false}, {"-1h", true}, {"0s", true}, {"tomorrow", true},
		{"25:00", true}, {"1.5d", true}, {"2026-10-13T08:00:00-03:00", true}, {"soon", true},
	} {
		if _, err := ParseSendTime(c.in, now, c.relative); err == nil {
			t.Errorf("ParseSendTime(%q, %v) accepted", c.in, c.relative)
		}
	}
	// A time on the recipient's clock may be "in the past" in some zone: it's checked once resolved.
	if _, err := ParseSendTime("2026-10-13T09:00", now, true); err != nil {
		t.Error(err)
	}
}

func TestSendTimeResolveAndJSON(t *testing.T) {
	sp, _ := time.LoadLocation("America/Sao_Paulo")
	local, _ := ParseSendAt("2026-10-14T09:00")
	if got := local.Resolve(sp, time.Now()); !got.Equal(time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)) {
		t.Fatal(got)
	}
	abs, _ := ParseSendAt("2026-10-14T09:00:00Z")
	if got := abs.Resolve(sp, time.Now()); !got.Equal(abs.Time) {
		t.Fatal("an instant doesn't depend on the zone", got)
	}
	var body struct {
		A SendTime `json:"a"`
		B SendTime `json:"b"`
		C SendTime `json:"c"`
		D Deadline `json:"d"`
		E Deadline `json:"e"`
	}
	if err := json.Unmarshal([]byte(`{"a":"2026-10-14T09:00","b":null,"c":"","d":"2h","e":"2026-10-14T11:00:00Z"}`), &body); err != nil {
		t.Fatal(err)
	}
	if !body.A.Local || !body.B.IsZero() || !body.C.IsZero() || body.D.After != 2*time.Hour || body.E.IsZero() {
		t.Fatalf("%+v", body)
	}
	for _, bad := range []string{`{"a":"2h"}`, `{"a":12}`, `{"d":"-2h"}`, `{"d":"later"}`} {
		if err := json.Unmarshal([]byte(bad), &body); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestDeadlineAndSpread(t *testing.T) {
	due := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	if at, err := (Deadline{After: 2 * time.Hour}).expiry(due, time.UTC, due); err != nil || !at.Equal(due.Add(2*time.Hour)) {
		t.Fatal(at, err)
	}
	for _, bad := range []Deadline{{SendTime: At(due)}, {SendTime: At(due.Add(-time.Hour))}, {After: 8 * 24 * time.Hour}} {
		if _, err := bad.expiry(due, time.UTC, due); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	if _, err := ParseSpread("25h"); err == nil {
		t.Error("spread over 24h accepted")
	}
	if d, err := ParseSpread("2h"); err != nil || d != 2*time.Hour {
		t.Fatal(d, err)
	}
	if SpreadOffset(0, 5, 4*time.Hour) != 0 || SpreadOffset(4, 5, 4*time.Hour) != 4*time.Hour || SpreadOffset(2, 5, 4*time.Hour) != 2*time.Hour || SpreadOffset(0, 1, time.Hour) != 0 {
		t.Fatal("offsets")
	}
}

func TestQueueOnRecipientClock(t *testing.T) {
	b := newTestBot(t)
	patch(t, b, "5511999999999", ContactInput{TimeZone: ptr("America/Sao_Paulo")})
	at, _ := ParseSendAt(time.Now().Add(48*time.Hour).UTC().Format("2006-01-02") + "T09:00")
	items, err := b.Enqueue("ops", "", "", "k", []QueueRequest{
		{To: "5511999999999", Text: "hi", SendAt: at}, // São Paulo: 12:00 UTC
		{To: "5511988887777", Text: "hi", SendAt: at}, // no zone: the queue's (UTC in tests)
	})
	if err != nil {
		t.Fatal(err)
	}
	if h := items[0].Due.UTC().Hour(); h != 12 {
		t.Errorf("São Paulo 09:00 is due at %v", items[0].Due.UTC())
	}
	if !items[1].Due.Equal(at.Resolve(b.QueueLocation(), time.Now())) {
		t.Errorf("the queue's zone: %v", items[1].Due)
	}
	again, err := b.Enqueue("ops", "", "", "k", []QueueRequest{{To: "5511999999999", Text: "hi", SendAt: at}, {To: "5511988887777", Text: "hi", SendAt: at}})
	if err != nil || again[0].ID != items[0].ID {
		t.Fatalf("a retry replays: %v", err)
	}
	far, _ := ParseSendAt(time.Now().Add(40 * 24 * time.Hour).UTC().Format("2006-01-02T15:04"))
	if _, err := b.Enqueue("ops", "", "", "", []QueueRequest{{To: "5511999999999", Text: "hi", SendAt: far}}); !isInputError(err) {
		t.Fatalf("40 days ahead on the recipient's clock = %v", err)
	}
}

func TestQueueSpreadAndExpiry(t *testing.T) {
	b := newTestBot(t)
	base := time.Now().Add(time.Hour).Truncate(time.Second)
	reqs := []QueueRequest{{To: "5511999999991", Text: "a"}, {To: "5511999999992", Text: "b"}, {To: "5511999999993", Text: "c"}}
	for i := range reqs {
		reqs[i].SendAt, reqs[i].SendBefore = At(base), Deadline{After: 30 * time.Minute}
	}
	opt := QueueOptions{Actor: "ops", Key: "s", Spread: 2 * time.Hour}
	res, err := b.Queue(t.Context(), opt, reqs)
	if err != nil {
		t.Fatal(err)
	}
	for i, it := range res.Items {
		want := base.Add(time.Duration(i) * time.Hour)
		if !it.Due.Equal(want) || !it.SendBefore.Equal(want.Add(30*time.Minute)) {
			t.Errorf("item %d due %v before %v, want %v", i, it.Due, it.SendBefore, want)
		}
	}
	if again, err := b.Queue(t.Context(), opt, reqs); err != nil || again.Replayed != 3 {
		t.Fatalf("retry: %v %+v", err, again)
	}
	opt.Spread = time.Hour
	if _, err := b.Queue(t.Context(), opt, reqs); !errors.Is(err, ErrKeyReused) {
		t.Fatalf("another spread under the same key = %v", err)
	}
	// Without send_at the window starts now, and a retry still matches.
	opt = QueueOptions{Actor: "ops", Key: "now", Spread: time.Hour}
	plain := []QueueRequest{{To: "5511999999991", Text: "a"}, {To: "5511999999992", Text: "b"}}
	if _, err := b.Queue(t.Context(), opt, plain); err != nil {
		t.Fatal(err)
	}
	if again, err := b.Queue(t.Context(), opt, plain); err != nil || again.Replayed != 2 {
		t.Fatalf("retry without send_at: %v", err)
	}

	// An item past its send_before ends expired and is never claimed.
	gone, err := b.Store.Enqueue([]store.OutboxItem{{Batch: "x", Actor: "ops", To: "5511999999999", Text: "late", SendBefore: time.Now().Add(-time.Second)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Store.ClaimOutbox(gone[0].ID, "m1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("claimed an expired item: %v", err)
	}
	ch, unsub := b.Subscribe()
	defer unsub()
	b.expireQueued()
	if it, _ := b.Store.OutboxItem(gone[0].ID); it.Status != store.OutboxExpired {
		t.Fatalf("status %s", it.Status)
	}
	select {
	case ev := <-ch:
		if qe, ok := ev.(QueueEvent); !ok || qe.Item.Status != store.OutboxExpired || qe.Item.Text != "" {
			t.Fatalf("%#v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no event")
	}
	if c, _ := b.Store.CountOutbox(store.OutboxFilter{}); c.Expired != 1 {
		t.Fatalf("%+v", c)
	}
}

func TestEditQueued(t *testing.T) {
	b := newTestBot(t)
	at := time.Now().Add(time.Hour).Truncate(time.Second)
	reqs := []QueueRequest{{To: "5511999999999", Text: "Hi Maria", SendAt: At(at)}}
	items, err := b.Enqueue("ops", "", "promo", "k", reqs)
	if err != nil {
		t.Fatal(err)
	}
	id := items[0].ID
	if _, err := b.EditQueued("ops", id, store.OutboxFilter{}, QueueEdit{}); !isInputError(err) {
		t.Fatalf("an empty edit = %v", err)
	}
	if _, err := b.EditQueued("bob", id, store.OutboxFilter{Actor: "bob"}, QueueEdit{SendAt: At(at)}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another actor's item = %v", err)
	}
	later, text := At(at.Add(2*time.Hour)), "Hi Maria, see you at 3"
	it, err := b.EditQueued("ops", id, store.OutboxFilter{Actor: "ops"}, QueueEdit{SendAt: later, Text: &text, SendBefore: Deadline{After: time.Hour}})
	if err != nil || !it.Due.Equal(later.Time) || it.Text != text || it.Edited.IsZero() || !it.SendBefore.Equal(later.Add(time.Hour)) {
		t.Fatalf("%v %+v", err, it)
	}
	// The import that queued it still replays it, and a different one still conflicts.
	if again, err := b.Enqueue("ops", "", "promo", "k", reqs); err != nil || again[0].ID != id || again[0].Text != text {
		t.Fatalf("re-import after an edit: %v %+v", err, again)
	}
	if _, err := b.Enqueue("ops", "", "promo", "k", []QueueRequest{{To: "5511999999999", Text: text, SendAt: At(at)}}); !errors.Is(err, ErrKeyReused) {
		t.Fatalf("the edited text isn't the request: %v", err)
	}
	// Moving the time past the deadline needs the deadline changed too.
	if _, err := b.EditQueued("ops", id, store.OutboxFilter{}, QueueEdit{SendAt: At(at.Add(5 * time.Hour))}); !isInputError(err) {
		t.Fatalf("past its send_before = %v", err)
	}
	if it, err = b.EditQueued("ops", id, store.OutboxFilter{}, QueueEdit{SendAt: At(at.Add(5 * time.Hour)), ClearBefore: true}); err != nil || !it.SendBefore.IsZero() {
		t.Fatalf("%v %+v", err, it)
	}
	if _, err := b.CancelQueued("ops", id, store.OutboxFilter{}); err != nil {
		t.Fatal(err)
	}
	_, err = b.EditQueued("ops", id, store.OutboxFilter{}, QueueEdit{SendAt: later})
	if nq, ok := errors.AsType[NotQueuedError](err); !ok || nq.Status != store.OutboxCanceled || !nq.Edit {
		t.Fatalf("editing a canceled item = %v", err)
	}
}

func TestClockOnRecipientCalendar(t *testing.T) {
	sp, _ := time.LoadLocation("America/Sao_Paulo")
	lisbon, _ := time.LoadLocation("Europe/Lisbon")
	now := time.Date(2026, 10, 13, 10, 0, 0, 0, sp) // 14:00 in Lisbon
	at, err := ParseSendTime("13:00", now, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := at.Resolve(lisbon, now); !got.Equal(time.Date(2026, 10, 14, 13, 0, 0, 0, lisbon)) {
		t.Fatalf("13:00 has passed in Lisbon: want tomorrow's, got %v", got)
	}
	if got := at.Resolve(sp, now); !got.Equal(time.Date(2026, 10, 13, 13, 0, 0, 0, sp)) {
		t.Fatalf("13:00 today in São Paulo, got %v", got)
	}
	late := time.Date(2026, 10, 13, 22, 0, 0, 0, sp) // already the 14th in Lisbon
	tomorrow, _ := ParseSendTime("tomorrow 09:00", late, true)
	if got := tomorrow.Resolve(lisbon, late); !got.Equal(time.Date(2026, 10, 15, 9, 0, 0, 0, lisbon)) {
		t.Fatalf("tomorrow on the recipient's calendar, got %v", got)
	}
}

func TestScheduleFixesFromReview(t *testing.T) {
	b := newTestBot(t)
	// A retry after a fixed send_before has passed replays instead of failing; a new one is refused.
	past := time.Now().Add(-time.Hour)
	reqs := []QueueRequest{{To: "5511999999999", Text: "hi", SendAt: At(past.Add(-time.Hour)), SendBefore: Deadline{SendTime: At(past)}}}
	if _, err := b.Enqueue("ops", "", "", "late", reqs); !isInputError(err) || !strings.Contains(err.Error(), "already passed") {
		t.Fatalf("a deadline already passed = %v", err)
	}
	fresh := []QueueRequest{{To: "5511999999999", Text: "hi", SendAt: At(time.Now().Add(-time.Minute)), SendBefore: Deadline{SendTime: At(time.Now().Add(time.Second))}}}
	first, err := b.Enqueue("ops", "", "", "k", fresh)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if again, err := b.Enqueue("ops", "", "", "k", fresh); err != nil || again[0].ID != first[0].ID {
		t.Fatalf("a retry after the deadline passed: %v", err)
	}

	// A local time keeps replaying after the contact's zone changes.
	patch(t, b, "5511988887777", ContactInput{TimeZone: ptr("America/Sao_Paulo")})
	local, _ := ParseSendAt(time.Now().Add(48*time.Hour).UTC().Format("2006-01-02") + "T09:00")
	lr := []QueueRequest{{To: "5511988887777", Text: "hi", SendAt: local}}
	first, err = b.Enqueue("ops", "", "", "zone", lr)
	if err != nil {
		t.Fatal(err)
	}
	patch(t, b, "5511988887777", ContactInput{TimeZone: ptr("Europe/Lisbon")})
	if again, err := b.Enqueue("ops", "", "", "zone", lr); err != nil || again[0].ID != first[0].ID {
		t.Fatalf("a retry after the zone changed: %v", err)
	}

	// An item rescheduled after the worker read it isn't claimed.
	if _, err := b.EditQueued("ops", first[0].ID, store.OutboxFilter{}, QueueEdit{SendAt: At(time.Now().Add(time.Hour))}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Store.ClaimOutbox(first[0].ID, "m"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("claimed an item due later: %v", err)
	}
}
