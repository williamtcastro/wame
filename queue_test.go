package main

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/williamtcastro/wame/internal/api"
	"github.com/williamtcastro/wame/internal/auth"
	"github.com/williamtcastro/wame/internal/bot"
	"github.com/williamtcastro/wame/internal/store"
)

const testOwner = "a-long-enough-owner-secret"

// queueServer starts a real API on a scratch database and points the CLI's config at it through
// $XDG_CONFIG_HOME/wame/admin.env, from an empty working directory with no WAME_* variables set.
func queueServer(t *testing.T) (*bot.Bot, string) {
	t.Helper()
	b, err := bot.New(t.Context(), filepath.Join(t.TempDir(), "wame.db"), waLog.Noop, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	srv := httptest.NewServer(api.NewHandler(b, &auth.Authenticator{Store: b.Store, Owner: testOwner}, "test"))
	t.Cleanup(srv.Close)
	for _, k := range []string{"WAME_SERVER", "WAME_TOKEN"} {
		t.Setenv(k, "")
	}
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	writeFile(t, filepath.Join(cfg, "wame", "admin.env"), fmt.Sprintf("WAME_SERVER=%s\nWAME_TOKEN=%s\n", srv.URL, testOwner))
	t.Chdir(t.TempDir())
	return b, srv.URL
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func csvFile(t *testing.T, rows int) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("to,text\n")
	for i := range rows {
		fmt.Fprintf(&b, "55119%08d,\"Hi, number %d\"\n", i, i)
	}
	path := filepath.Join(t.TempDir(), "promo.csv")
	writeFile(t, path, b.String())
	return path
}

func runQueue(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := queueCommand(t.Context(), args, &out)
	return out.String(), err
}

func counts(t *testing.T, b *bot.Bot, batch string) store.OutboxCounts {
	t.Helper()
	n, err := b.Store.CountOutbox(store.OutboxFilter{Batch: batch})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func smallChunks(t *testing.T) {
	chunkMax = 10
	t.Cleanup(func() { chunkMax = bot.MaxQueueBatch })
}

func TestQueueImportChunksAndRerun(t *testing.T) {
	b, _ := queueServer(t)
	smallChunks(t)
	file := csvFile(t, 23)
	out, err := runQueue(t, "import", "-batch", "promo", file)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"1/3: lines 2-11: queued 10", "2/3: lines 12-21: queued 10", "3/3: lines 22-24: queued 3", "queued 23 messages, batch promo"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if n := counts(t, b, "promo"); n.Queued != 23 {
		t.Fatalf("queued %+v", n)
	}
	if out, err := runQueue(t, "import", file, "-batch", "promo"); err != nil || !strings.Contains(out, "queued 0 messages (already queued 23)") {
		t.Fatalf("rerun: %v\n%s", err, out)
	}
	if n := counts(t, b, ""); n.Queued != 23 {
		t.Fatalf("a rerun with the same file must not queue again: %+v", n)
	}
	out, err = runQueue(t, "status", "-batch", "promo")
	if err != nil || !strings.Contains(out, "batch promo: queued 23, sending 0, sent 0") {
		t.Fatalf("status: %v\n%s", err, out)
	}
}

func TestQueueImportResumesWithKey(t *testing.T) {
	b, _ := queueServer(t)
	smallChunks(t)
	first := csvFile(t, 10)
	if _, err := runQueue(t, "import", "-key", "promo-oct", first); err != nil {
		t.Fatal(err)
	}
	all := csvFile(t, 12)
	out, err := runQueue(t, "import", "-key", "promo-oct", all)
	if err != nil || counts(t, b, "").Queued != 12 {
		t.Fatalf("the chunk already sent must be replayed, not queued again: %v %+v\n%s", err, counts(t, b, ""), out)
	}
	if _, err := runQueue(t, "import", "-key", "promo-oct", "-text", "x", csvFileTo(t, "to,text\n5511999999999,changed\n")); err == nil || !strings.Contains(err.Error(), "pass another -key") {
		t.Fatalf("a different file under the same key must be refused: %v", err)
	}
}

func TestChunkRowsByCount(t *testing.T) {
	rows := make([]bot.QueueRequest, 2003)
	for i := range rows {
		rows[i] = bot.QueueRequest{To: "5511999999999", Text: "hi", Line: i + 2}
	}
	chunks, err := chunkRows(rows)
	if err != nil || len(chunks) != 3 || chunks[0].rows != 1000 || chunks[1].rows != 1000 || chunks[2].rows != 3 ||
		chunks[1].first != 1002 || chunks[2].last != 2004 {
		t.Fatalf("%d chunks, %v", len(chunks), err)
	}
}

func TestQueueImportWithoutBatchOrKey(t *testing.T) {
	b, _ := queueServer(t)
	file := csvFile(t, 3)
	out, err := runQueue(t, "import", file)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := runQueue(t, "import", file); err != nil {
		t.Fatal(err)
	}
	n := counts(t, b, "")
	items, _ := b.Store.ListOutbox(store.OutboxFilter{}, 10)
	if n.Queued != 3 || !strings.HasPrefix(items[0].Batch, "csv-") || !strings.Contains(out, "batch "+items[0].Batch) {
		t.Fatalf("the batch and keys must come from the file, so a rerun is a no-op: %+v %+v\n%s", n, items[0], out)
	}
	writeFile(t, file, "to,text\n5511999999999,changed\n")
	if _, err := runQueue(t, "import", file); err == nil || !strings.Contains(err.Error(), "pass another -key") || counts(t, b, "").Queued != 3 {
		t.Fatalf("rows already queued from this file can't change silently: %v", err)
	}
	if _, err := runQueue(t, "import", "-key", "promo-nov", file); err != nil || counts(t, b, "").Queued != 4 {
		t.Fatalf("a new import of the same path takes a new -key: %v", err)
	}
}

func TestQueueImportDryRun(t *testing.T) {
	b, _ := queueServer(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	file := csvFile(t, 1500)
	out, err := runQueue(t, "import", "-dry-run", "-batch", "promo", "-key", "k1", file)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1500 messages to 1500 recipients, batch promo, in 2 request(s)", "1/2: lines 2-1001, 1000 messages", "key k1.1", "2/2: lines 1002-1501, 500 messages", "key k1.2", "nothing sent"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run lacks %q:\n%s", want, out)
		}
	}
	if n := counts(t, b, ""); n.Queued != 0 {
		t.Fatalf("a dry run queued %+v", n)
	}

	path := filepath.Join(t.TempDir(), "bad.csv")
	for _, c := range []struct{ body, args, want string }{
		{"to,text\n5511999999999,hi\n123,hi\n", "", "line 3: invalid recipient"},
		{"to,text\n5511999999999,\"a\nb\"\n5511988887777,\n", "", "line 4: text is empty"},
		{"to\n5511999999999\n", "", "line 2: text is empty"},
		{"to,nome\n5511999999999,Ana\n", "", `unknown columns "nome"`},
		{"text\nhi\n", "", `"to" column is required`},
		{"to,text\n", "", "no messages"},
		{"to,text,send_at\n5511999999999,hi,2099-01-01T00:00:00Z\n", "", "line 2: send_at is more than 30 days ahead"},
		{"to,text\n5511999999999,hi\n", "-send-at tomorrow", `-send-at "tomorrow" isn't a time like`},
		{"to,text\n5511999999999.0,hi\n", "", "line 2: \"5511999999999.0\" is a number with decimals"},
		{"to,text\n5511999999999,hi\n", "-batch no/slash", "batch must be"},
		{"to,text\n5511999999999,hi\n", "-key " + strings.Repeat("k", 200), "-key is longer"},
	} {
		writeFile(t, path, c.body)
		args := append([]string{"import", "-dry-run"}, strings.Fields(c.args)...)
		if _, err := runQueue(t, append(args, path)...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q %s: %v, want %q", c.body, c.args, err, c.want)
		}
	}
	if _, err := runQueue(t, "import", "-dry-run", filepath.Join(t.TempDir(), "missing.csv")); err == nil {
		t.Error("a missing file must fail")
	}
	if _, err := runQueue(t, "import", "-dry-run"); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("no file: %v", err)
	}
	if out, err := runQueue(t, "import", "-dry-run", "-text", "Hello!", "-send-at", "2026-10-13T09:00:00-03:00", csvFileTo(t, "to\n5511999999999\n")); err != nil || !strings.Contains(out, "1 messages") {
		t.Errorf("defaults fill rows without text: %v\n%s", err, out)
	}
}

func csvFileTo(t *testing.T, body string) string {
	path := filepath.Join(t.TempDir(), "file.csv")
	writeFile(t, path, body)
	return path
}

func TestQueueImportReportsFileLines(t *testing.T) {
	b, url := queueServer(t)
	smallChunks(t)
	scoped, _, err := auth.Issue(b.Store, auth.Request{Name: "maria-only", Scopes: []auth.Scope{auth.Send}, Chats: []string{"5511999999999@s.whatsapp.net"}})
	if err != nil {
		t.Fatal(err)
	}
	var body strings.Builder
	body.WriteString("to,text\n")
	for range 10 {
		body.WriteString("5511999999999,\"hi\nMaria\"\n")
	}
	body.WriteString("5511999999999,hi\n5511988887777,hi John\n")
	file := csvFileTo(t, body.String())
	out, err := runQueue(t, "import", "-server", url, "-token", scoped, file)
	if err == nil || !strings.Contains(out, "2/2: lines 22-23: failed: this token can't do that (403): line 23: this token can't access that chat") ||
		!strings.Contains(err.Error(), "stopped after 10 of 12 messages") || !strings.Contains(err.Error(), "from line 22 on") {
		t.Fatalf("%v\n%s", err, out)
	}
	items, _ := b.Store.ListOutbox(store.OutboxFilter{}, 1)
	batch := items[0].Batch

	writeFile(t, file, strings.Replace(body.String(), "5511988887777,hi John", "5511999999999,hi again", 1))
	out, err = runQueue(t, "import", "-server", url, "-token", scoped, file)
	if err != nil || !strings.Contains(out, "batch "+batch) {
		t.Fatalf("a rerun after fixing the row must resume the same import: %v\n%s", err, out)
	}
	if n := counts(t, b, batch); n.Queued != 12 {
		t.Fatalf("the first chunk must be replayed, not queued again: %+v", n)
	}
}

func TestQueueCommandConfig(t *testing.T) {
	b, url := queueServer(t)
	file := csvFile(t, 1)

	if out, err := runQueue(t, "import", "-batch", "home", file); err != nil || counts(t, b, "home").Queued != 1 {
		t.Fatalf("admin.env: %v\n%s", err, out)
	}

	cfg := os.Getenv("XDG_CONFIG_HOME")
	writeFile(t, filepath.Join(cfg, "wame", "admin.env"), "")
	if _, err := runQueue(t, "status"); err == nil || !strings.Contains(err.Error(), "token is required") {
		t.Fatalf("no token anywhere: %v", err)
	}
	writeFile(t, filepath.Join(cfg, "wame", "servers", "vps.env"), fmt.Sprintf("WAME_SERVER=%s\nWAME_TOKEN=%s\n", url, testOwner))
	if out, err := runQueue(t, "status", "-profile", "vps", "-batch", "home"); err != nil || !strings.Contains(out, "batch home: queued 1") {
		t.Fatalf("-profile: %v\n%s", err, out)
	}
	if _, err := runQueue(t, "status", "-profile", "nope"); err == nil || !strings.Contains(err.Error(), `no saved server "nope"`) {
		t.Fatalf("unknown profile: %v", err)
	}

	writeFile(t, ".env", "WAME_SERVER=https://wame.example.com\n")
	t.Setenv("WAME_TOKEN", testOwner)
	if _, err := runQueue(t, "status"); err == nil || !strings.Contains(err.Error(), "refusing to send it there") {
		t.Fatalf("a .env server must not get a token from elsewhere: %v", err)
	}
	t.Setenv("WAME_SERVER", url)
	if out, err := runQueue(t, "status"); err != nil || !strings.Contains(out, "queued 1") {
		t.Fatalf("environment: %v\n%s", err, out)
	}
	if _, err := runQueue(t, "status", "-server", "http://10.0.0.5:8080"); err == nil || !strings.Contains(err.Error(), "plain http") {
		t.Fatalf("plain http beyond loopback: %v", err)
	}
	if _, err := runQueue(t, "nope"); err == nil {
		t.Fatal("unknown subcommand")
	}
}

func TestChunkRowsBySize(t *testing.T) {
	text := strings.Repeat("x", 60000)
	rows := make([]bot.QueueRequest, 300)
	for i := range rows {
		rows[i] = bot.QueueRequest{To: "5511999999999", Text: text, Line: i + 2}
	}
	chunks, err := chunkRows(rows)
	if err != nil || len(chunks) != 2 || chunks[0].rows+chunks[1].rows != 300 {
		t.Fatalf("%d chunks, %v", len(chunks), err)
	}
	for _, c := range chunks {
		if len(c.body) > maxChunkBytes {
			t.Errorf("chunk of %d bytes", len(c.body))
		}
		got, err := bot.ReadQueueCSV(bytes.NewReader(c.body), bot.MaxQueueBatch)
		if err != nil || len(got) != c.rows || got[0].Text != text {
			t.Errorf("chunk doesn't read back: %v", err)
		}
	}
	if chunks[1].first != chunks[0].last+1 {
		t.Errorf("chunks must be contiguous: %+v %+v", chunks[0].last, chunks[1].first)
	}
}

func TestQueueImportSanitizesServerErrors(t *testing.T) {
	queueServer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"line 2: \u001b[2J\u001b]0;pwned\u0007bad\nrow"}`)
	}))
	defer srv.Close()
	out, err := runQueue(t, "import", "-server", srv.URL, "-token", testOwner, csvFile(t, 1))
	if err == nil || strings.ContainsAny(out, "\x1b\x07") || !strings.Contains(out, "failed: line 2:") || strings.Count(out, "\n") != 1 {
		t.Fatalf("%v\n%q", err, out)
	}
}

func TestQueueImportSpread(t *testing.T) {
	rows := []bot.QueueRequest{{To: "5511999999991", SendAt: bot.At(time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC))},
		{To: "5511999999992"}, {To: "5511999999993"}}
	for i := range rows {
		rows[i].Text, rows[i].Line = "hi", i+2
	}
	if err := spreadRows(slices.Clone(rows), time.Hour); err == nil || !strings.Contains(err.Error(), "line 3: -spread needs a send_at") {
		t.Fatalf("a row without send_at: %v", err)
	}
	local, _ := bot.ParseSendAt("2026-10-21T09:00")
	for i := range rows {
		rows[i].SendAt = local
	}
	if err := spreadRows(rows, 2*time.Hour); err != nil {
		t.Fatal(err)
	}
	if got := rows[2].SendAt.String(); got != "2026-10-21T11:00:00" || !rows[1].SendAt.Local {
		t.Fatalf("the last row at %s", got)
	}
	tagged := []bot.QueueRequest{{To: "tag:clients", Text: "hi", SendAt: local, Line: 2}}
	if err := spreadRows(tagged, time.Hour); err == nil || !strings.Contains(err.Error(), "tag:") {
		t.Fatalf("a tag row: %v", err)
	}
}

func TestSendAtCommand(t *testing.T) {
	b, _ := queueServer(t)
	if !scheduledSend([]string{"-at", "2h", "5511999999999", "hi"}) || !scheduledSend([]string{"--before=1h"}) || scheduledSend([]string{"5511999999999", "-at"[1:]}) {
		t.Fatal("scheduledSend")
	}
	var out bytes.Buffer
	if err := sendAtCommand(t.Context(), []string{"-at", "2h", "-before", "30m", "5511999999999", "see", "you"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "queued #1 for ") {
		t.Fatalf("%q", out.String())
	}
	it, err := b.Store.OutboxItem(1)
	if err != nil || it.Text != "see you" || it.Actor == "" || it.SendBefore.Sub(it.Due) != 30*time.Minute {
		t.Fatalf("%v %+v", err, it)
	}
	if d := time.Until(it.Due); d < 119*time.Minute || d > 2*time.Hour {
		t.Fatalf("due in %v", d)
	}
	for _, args := range [][]string{{"-at", "soon", "5511999999999", "hi"}, {"-at", "2h", "5511999999999"}, {"-before", "1h", "5511999999999", "hi"}} {
		if err := sendAtCommand(t.Context(), args, io.Discard); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}
