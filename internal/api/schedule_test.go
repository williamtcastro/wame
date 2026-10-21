package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/williamtcastro/wame/internal/auth"
	"github.com/williamtcastro/wame/internal/store"
)

func TestTimedSendAndEdit(t *testing.T) {
	b := newTestBot(t)
	h := newHandler(b, "s3cret-owner-secret")
	sender := bearer(issue(t, b, "promo-bot", []auth.Scope{auth.Send}, nil, 0))
	other := bearer(issue(t, b, "other-bot", []auth.Scope{auth.Send}, nil, 0))
	scoped := bearer(issue(t, b, "maria-only", []auth.Scope{auth.Send}, []string{maria}, 0))
	at := time.Now().Add(time.Hour).UTC().Truncate(time.Second)

	keyed := map[string]string{"Authorization": sender["Authorization"], "Idempotency-Key": "s1"}
	body := fmt.Sprintf(`{"to":"5511999999999","text":"see you","send_at":%q,"send_before":"2h"}`, at.Format(time.RFC3339))
	w := doBody(h, "POST", "/send", body, keyed)
	var res struct {
		Queued int                `json:"queued"`
		Items  []store.OutboxItem `json:"items"`
	}
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &res) != nil || res.Queued != 1 || res.Items[0].Text != "" ||
		!res.Items[0].Due.Equal(at) || !res.Items[0].SendBefore.Equal(at.Add(2*time.Hour)) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	id := res.Items[0].ID
	if w := doBody(h, "POST", "/send", body, keyed); w.Code != 202 || !strings.Contains(w.Body.String(), `"replayed":1`) {
		t.Fatalf("a retry replays: %d %s", w.Code, w.Body)
	}
	long := map[string]string{"Authorization": sender["Authorization"], "Idempotency-Key": strings.Repeat("k", 200)}
	if w := doBody(h, "POST", "/send", body, long); w.Code != 202 {
		t.Fatalf("a 200-character key: %d %s", w.Code, w.Body)
	}
	if w := doBody(h, "POST", "/send", `{"to":"5511999999999","text":"x","send_at":"2h"}`, sender); w.Code != 400 {
		t.Fatalf("a relative time over the API: %d %s", w.Code, w.Body)
	}
	if w := doBody(h, "POST", "/send", `{"to":"5511988887777","text":"x","send_at":"2026-10-14T09:00"}`, scoped); w.Code != 403 {
		t.Fatalf("allowlist: %d %s", w.Code, w.Body)
	}

	patch := func(hdr map[string]string, id int64, body string) *strings.Builder {
		t.Helper()
		w := doBody(h, "PATCH", fmt.Sprintf("/queue/%d", id), body, hdr)
		sb := &strings.Builder{}
		fmt.Fprintf(sb, "%d %s", w.Code, w.Body)
		return sb
	}
	later := at.Add(3 * time.Hour)
	if got := patch(sender, id, fmt.Sprintf(`{"send_at":%q}`, later.Format(time.RFC3339))).String(); !strings.HasPrefix(got, "400") {
		t.Fatalf("moving past send_before: %s", got)
	}
	if got := patch(sender, id, fmt.Sprintf(`{"send_at":%q,"send_before":null,"text":"new text"}`, later.Format(time.RFC3339))).String(); !strings.HasPrefix(got, "200") {
		t.Fatal(got)
	}
	if it, _ := b.Store.OutboxItem(id); !it.Due.Equal(later) || !it.SendBefore.IsZero() || it.Text != "new text" || it.Edited.IsZero() {
		t.Fatalf("%+v", it)
	}
	if got := patch(other, id, `{"text":"mine now"}`).String(); !strings.HasPrefix(got, "404") {
		t.Fatalf("another token's item: %s", got)
	}
	if got := patch(sender, id, `{}`).String(); !strings.HasPrefix(got, "400") {
		t.Fatalf("nothing to change: %s", got)
	}
	if w := do(h, "DELETE", fmt.Sprintf("/queue/%d", id), sender); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if got := patch(sender, id, `{"text":"too late"}`).String(); !strings.HasPrefix(got, "409") {
		t.Fatalf("a canceled item: %s", got)
	}

	csv := "to,text,send_at,send_before\n5511999999999,hi,2099-01-01T09:00,\n"
	if w := doCSV(h, "/queue?spread=1h", csv, sender); w.Code != 400 || !strings.Contains(w.Body.String(), "days ahead") {
		t.Fatalf("a local time 70 years ahead: %d %s", w.Code, w.Body)
	}
	if w := doBody(h, "POST", "/queue", `{"spread":"48h","messages":[{"to":"5511999999999","text":"x"}]}`, sender); w.Code != 400 {
		t.Fatalf("spread over a day: %d %s", w.Code, w.Body)
	}
	if w := doMultipart(t, h, map[string]string{"to": "5511999999999", "send_at": "2026-10-21T09:00"}, 10, sender); w.Code != 400 || !strings.Contains(w.Body.String(), "POST /queue/files") {
		t.Fatalf("a timed /send/file: %d %s", w.Code, w.Body)
	}
	if w := doBody(h, "GET", "/queue?status=expired", "", sender); w.Code != 200 {
		t.Fatalf("expired is a status: %d %s", w.Code, w.Body)
	}
}
