// Package api serves the HTTP API on top of a bot.Bot.
package api

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"rsc.io/qr"

	"github.com/williamtcastro/wame/docs"
	"github.com/williamtcastro/wame/internal/auth"
	"github.com/williamtcastro/wame/internal/bot"
	"github.com/williamtcastro/wame/internal/store"
)

const (
	maxPollWait = 120 * time.Second
	maxPageSize = 1000
)

// maxUpload is the largest file POST /send/file accepts (a variable so tests can lower it).
var maxUpload int64 = 64 << 20

const docsPage = `<!doctype html>
<html><head><meta charset="utf-8"><title>wame API</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui.css"
  integrity="sha384-wxLW6kwyHktdDGr6Pv1zgm/VGJh99lfUbzSn6HNHBENZlCN7W602k9VkGdxuFvPn" crossorigin="anonymous">
</head><body><div id="ui"></div>
<script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui-bundle.js"
  integrity="sha384-wmyclcVGX/WhUkdkATwhaK1X1JtiNrr2EoYJ+diV3vj4v6OC5yCeSu+yW13SYJep" crossorigin="anonymous"></script>
<script>SwaggerUIBundle({url: "openapi.yaml", dom_id: "#ui"});</script>
</body></html>`

type server struct {
	b       *bot.Bot
	version string
	idem    idempotency
	streams streams
	closing context.Context
}

// Handler is the API. StopStreams ends open /events and /messages/wait connections, which
// http.Server.Shutdown doesn't; register it with RegisterOnShutdown.
type Handler struct {
	http.Handler
	StopStreams func()
}

// NewHandler serves the API. Every route requires a scope; see docs/API.md for the table.
func NewHandler(b *bot.Bot, authn *auth.Authenticator, version string) *Handler {
	closing, stop := context.WithCancel(context.Background())
	a := &server{b: b, version: version, idem: idempotency{entries: map[string]*idemEntry{}}, closing: closing}
	mux := http.NewServeMux()
	read, send, admin := auth.Read, auth.Send, auth.Admin

	mux.HandleFunc("GET /status", need(read, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, visibleStatus(principal(r), b.Status()))
	}))
	mux.HandleFunc("GET /qr", need(admin, a.qr))
	mux.HandleFunc("POST /pair", need(admin, a.pair))
	mux.HandleFunc("POST /logout", need(admin, a.logout))

	mux.HandleFunc("POST /send", need(send, a.sendText))
	mux.HandleFunc("POST /send/file", need(send, a.sendFile))
	mux.HandleFunc("POST /react", need(send, a.react))
	mux.HandleFunc("POST /edit", need(send, a.edit))
	mux.HandleFunc("POST /delete", need(send, a.delete))
	mux.HandleFunc("POST /read", need(send, a.read))
	mux.HandleFunc("POST /typing", need(send, a.typing))

	mux.HandleFunc("POST /queue", need(send, a.enqueue))
	mux.HandleFunc("GET /queue", need(send, a.listQueue))
	mux.HandleFunc("DELETE /queue", need(send, a.cancelBatch))
	mux.HandleFunc("GET /queue/{id}", need(send, a.getQueued))
	mux.HandleFunc("PATCH /queue/{id}", need(send, a.editQueued))
	mux.HandleFunc("DELETE /queue/{id}", need(send, a.cancelQueued))
	mux.HandleFunc("POST /queue/files", need(send, a.uploadQueueFile))
	mux.HandleFunc("GET /queue/files", need(send, a.listQueueFiles))
	mux.HandleFunc("DELETE /queue/files/{id}", need(send, a.deleteQueueFile))
	mux.HandleFunc("GET /optouts", need(admin, a.listOptOuts))
	mux.HandleFunc("POST /optouts", need(admin, a.addOptOut))
	mux.HandleFunc("DELETE /optouts/{chat}", need(admin, a.removeOptOut))

	mux.HandleFunc("GET /messages", need(read, a.messages))
	mux.HandleFunc("GET /messages/wait", need(read, a.wait))
	mux.HandleFunc("GET /events", need(read, a.events))
	mux.HandleFunc("GET /media/{seq}", need(read, a.media))
	mux.HandleFunc("GET /chats", need(read, a.chats))

	mux.HandleFunc("GET /contacts", need(read, a.contacts))
	mux.HandleFunc("GET /contacts/tags", need(read, unrestricted(a.contactTags)))
	mux.HandleFunc("GET /contacts/export", need(admin, a.exportContacts))
	mux.HandleFunc("GET /contacts/{number}", need(read, a.contact))
	mux.HandleFunc("PUT /contacts/{number}", need(admin, a.putContact))
	mux.HandleFunc("PATCH /contacts/{number}", need(admin, a.putContact))
	mux.HandleFunc("DELETE /contacts/{number}", need(admin, a.deleteContact))
	mux.HandleFunc("DELETE /contacts", need(admin, a.deleteAllContacts))
	mux.HandleFunc("POST /contacts/import", need(admin, a.importContacts))
	mux.HandleFunc("GET /groups", need(read, unrestricted(a.groups)))
	mux.HandleFunc("POST /check", need(send, unrestricted(a.check)))

	mux.HandleFunc("GET /audit", need(admin, a.audit))
	mux.HandleFunc("GET /stats", need(admin, a.stats))
	mux.HandleFunc("GET /logs", need(admin, a.logs))
	mux.HandleFunc("GET /settings", need(admin, a.settings))
	mux.HandleFunc("PATCH /settings", need(admin, a.updateSettings))
	mux.HandleFunc("POST /webhook/test", need(admin, a.webhookTest))
	mux.HandleFunc("POST /webhook/replay", need(admin, a.webhookReplay))
	mux.HandleFunc("POST /alerts/test", need(admin, a.alertTest))
	mux.HandleFunc("GET /metrics", need(admin, a.metrics))
	mux.HandleFunc("GET /tokens", need(admin, a.listTokens))
	mux.HandleFunc("POST /tokens", need(admin, a.createToken))
	mux.HandleFunc("DELETE /tokens/{name}", need(admin, a.revokeToken))

	root := http.NewServeMux()
	fails := &failureLimiter{}
	root.Handle("/", withAuth(authn, fails, b.RecordRequest, mux))
	root.Handle("/mcp", mcpHandler(b, authn, fails, version, b.Log))
	root.HandleFunc("GET /docs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, docsPage)
	})
	root.HandleFunc("GET /openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(docs.OpenAPI)
	})
	return &Handler{Handler: withRequestLog(b.Log, root), StopStreams: stop}
}

func (a *server) qr(w http.ResponseWriter, r *http.Request) {
	s := a.b.Status()
	if s.LoggedIn || s.QR == "" {
		writeError(w, fmt.Errorf("%w: no QR available (state: %s)", store.ErrNotFound, s.State))
		return
	}
	code, err := qr.Encode(s.QR, qr.M)
	a.b.Audit(principal(r).Name, "qr", "", "", err)
	if err != nil {
		writeError(w, err)
		return
	}
	code.Scale = 8
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(code.PNG())
}

func (a *server) pair(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phone string `json:"phone"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	code, err := a.b.PairPhone(r.Context(), principal(r).Name, req.Phone)
	respond(w, map[string]string{"code": code}, err)
}

func (a *server) logout(w http.ResponseWriter, r *http.Request) {
	err := a.b.Logout(r.Context(), principal(r).Name)
	respond(w, map[string]bool{"ok": true}, err)
}

func (a *server) sendText(w http.ResponseWriter, r *http.Request) {
	var req struct {
		To             string       `json:"to"`
		Text           string       `json:"text"`
		ReplyTo        string       `json:"reply_to"`
		IdempotencyKey string       `json:"idempotency_key"`
		SendAt         bot.SendTime `json:"send_at"`
		SendBefore     bot.Deadline `json:"send_before"`
		Batch          string       `json:"batch"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.To == "" || req.Text == "" {
		writeError(w, bot.BadInput("'to' and 'text' are required"))
		return
	}
	to, ok := a.authorizeChat(w, r, req.To)
	if !ok {
		return
	}
	key := cmp.Or(r.Header.Get("Idempotency-Key"), req.IdempotencyKey)
	if !req.SendAt.IsZero() || !req.SendBefore.IsZero() || req.Batch != "" {
		if key != "" {
			sum := sha256.Sum256([]byte(key))
			key = "send:" + hex.EncodeToString(sum[:])
		}
		p := principal(r)
		res, err := a.b.Queue(r.Context(), bot.QueueOptions{Actor: p.Name, TokenID: p.TokenID, Batch: req.Batch, Key: key,
			Admin: p.Has(auth.Admin), Read: p.Has(auth.Read), Allow: p.Chats},
			[]bot.QueueRequest{{To: to, Text: req.Text, ReplyTo: req.ReplyTo, SendAt: req.SendAt, SendBefore: req.SendBefore}})
		writeQueued(w, res, err)
		return
	}
	m, err := a.idem.do(r.Context(), principal(r).Name+":send", key, fingerprint(to, req.Text, req.ReplyTo), func() (store.Message, error) {
		if err := a.throttle(r); err != nil {
			return store.Message{}, err
		}
		return a.b.SendText(r.Context(), principal(r).Name, to, req.Text, bot.SendOptions{ReplyTo: req.ReplyTo})
	})
	respond(w, m, err)
}

func (a *server) sendFile(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload+1<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			uploadTooLarge(w)
			return
		}
		writeError(w, bot.BadInput("invalid multipart form: %v", err))
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeError(w, bot.BadInput("multipart field 'file' is required"))
		return
	}
	defer f.Close()
	to := r.FormValue("to")
	if to == "" {
		writeError(w, bot.BadInput("'to' is required"))
		return
	}
	if r.FormValue("send_at") != "" || r.FormValue("send_before") != "" {
		// Refused rather than sent now: a file meant for later going out at once can't be undone.
		writeError(w, bot.BadInput("/send/file sends at once; to send a file later, upload it with POST /queue/files and queue it with POST /queue"))
		return
	}
	to, ok := a.authorizeChat(w, r, to)
	if !ok {
		return
	}
	if hdr.Size > maxUpload {
		uploadTooLarge(w)
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, maxUpload+1))
	if err != nil {
		writeError(w, bot.BadInput("read upload: %v", err))
		return
	}
	if int64(len(data)) > maxUpload {
		uploadTooLarge(w)
		return
	}
	key := cmp.Or(r.Header.Get("Idempotency-Key"), r.FormValue("idempotency_key"))
	sum := sha256.Sum256(data)
	m, err := a.idem.do(r.Context(), principal(r).Name+":file", key, fingerprint(to, hdr.Filename, r.FormValue("caption"), r.FormValue("reply_to"), string(sum[:])), func() (store.Message, error) {
		if err := a.throttle(r); err != nil {
			return store.Message{}, err
		}
		return a.b.SendFile(r.Context(), principal(r).Name, to, data, hdr.Filename, r.FormValue("caption"), bot.SendOptions{ReplyTo: r.FormValue("reply_to")})
	})
	respond(w, m, err)
}

func uploadTooLarge(w http.ResponseWriter) {
	writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("file is larger than %d MB", maxUpload>>20))
}

type targetReq struct {
	Chat  string   `json:"chat"`
	ID    string   `json:"id"`
	IDs   []string `json:"ids"`
	Emoji string   `json:"emoji"`
	Text  string   `json:"text"`
	State string   `json:"state"`
}

func (a *server) target(w http.ResponseWriter, r *http.Request) (targetReq, bool) {
	var req targetReq
	if !readJSON(w, r, &req) {
		return req, false
	}
	if req.Chat == "" {
		writeError(w, bot.BadInput("'chat' is required"))
		return req, false
	}
	chat, ok := a.authorizeChat(w, r, req.Chat)
	if !ok {
		return req, false
	}
	if err := a.throttle(r); err != nil {
		writeError(w, err)
		return req, false
	}
	req.Chat = chat
	return req, true
}

func (a *server) react(w http.ResponseWriter, r *http.Request) {
	if req, ok := a.target(w, r); ok {
		m, err := a.b.React(r.Context(), principal(r).Name, req.Chat, req.ID, req.Emoji)
		respond(w, m, err)
	}
}

func (a *server) edit(w http.ResponseWriter, r *http.Request) {
	if req, ok := a.target(w, r); ok {
		m, err := a.b.Edit(r.Context(), principal(r).Name, req.Chat, req.ID, req.Text)
		respond(w, m, err)
	}
}

func (a *server) delete(w http.ResponseWriter, r *http.Request) {
	if req, ok := a.target(w, r); ok {
		m, err := a.b.Delete(r.Context(), principal(r).Name, req.Chat, req.ID)
		respond(w, m, err)
	}
}

func (a *server) read(w http.ResponseWriter, r *http.Request) {
	if req, ok := a.target(w, r); ok {
		if req.ID != "" {
			req.IDs = append(req.IDs, req.ID)
		}
		n, err := a.b.MarkRead(r.Context(), principal(r).Name, req.Chat, req.IDs)
		respond(w, map[string]int{"marked": n}, err)
	}
}

func (a *server) typing(w http.ResponseWriter, r *http.Request) {
	if req, ok := a.target(w, r); ok {
		respond(w, map[string]bool{"ok": true}, a.b.SetTyping(r.Context(), req.Chat, req.State))
	}
}

func (a *server) filter(w http.ResponseWriter, r *http.Request) (store.Filter, bool) {
	f, err := store.ParseFilter(r.URL.Query(), func(s string) (string, error) {
		if principal(r).Restricted() {
			return a.b.LocalChat(s)
		}
		jid, err := a.b.Resolve(r.Context(), s)
		if err != nil {
			if jid, err = bot.ParseRecipient(s); err != nil {
				return "", err
			}
		}
		return a.b.PhoneJID(jid).String(), nil
	})
	if err != nil {
		writeError(w, bot.BadInput("%v", err))
		return f, false
	}
	return f, a.restrictFilter(w, r, &f)
}

func intParam(r *http.Request, name string, def, maxVal int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || n <= 0 {
		return def
	}
	return min(n, maxVal)
}

func (a *server) messages(w http.ResponseWriter, r *http.Request) {
	f, ok := a.filter(w, r)
	if !ok {
		return
	}
	msgs, err := a.b.Store.Query(f, intParam(r, "limit", 50, maxPageSize), r.URL.Query().Has("since"))
	respond(w, msgs, err)
}

// startAtLatest points f at the newest stored message, so only later ones match.
func (a *server) startAtLatest(w http.ResponseWriter, f *store.Filter) bool {
	seq, err := a.b.Store.LastSeq()
	if err != nil {
		writeError(w, err)
		return false
	}
	f.Since = seq
	return true
}

func (a *server) wait(w http.ResponseWriter, r *http.Request) {
	f, ok := a.filter(w, r)
	if !ok {
		return
	}
	if !r.URL.Query().Has("since") && !a.startAtLatest(w, &f) {
		return
	}
	limit := intParam(r, "limit", 100, maxPageSize)
	timeout := time.Duration(intParam(r, "timeout", 30, int(maxPollWait/time.Second))) * time.Second
	p := principal(r)
	release, ok := a.streams.acquire(p)
	if !ok {
		writeErr(w, http.StatusTooManyRequests, errTooManyStreams.Error())
		return
	}
	defer release()

	ch, unsub := a.b.Subscribe()
	defer unsub()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		if !a.stillValid(p) {
			writeErr(w, http.StatusUnauthorized, "token revoked or expired")
			return
		}
		msgs, err := a.b.Store.Query(f, limit, true)
		if err != nil {
			writeError(w, err)
			return
		}
		if len(msgs) > 0 {
			writeJSON(w, 200, map[string]any{"messages": msgs, "next_since": msgs[len(msgs)-1].Seq})
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-a.closing.Done():
			// Shutting down: answer like a timeout, so the client polls again.
			writeJSON(w, 200, map[string]any{"messages": []store.Message{}, "next_since": f.Since})
			return
		case <-timer.C:
			writeJSON(w, 200, map[string]any{"messages": []store.Message{}, "next_since": f.Since})
			return
		case <-ch:
		}
	}
}

func (a *server) events(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	f, ok := a.filter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	// An EventSource reconnects to the same URL with Last-Event-ID, which must win over ?since=.
	if last, err := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64); err == nil && last >= 0 {
		f.Since = last
	} else if !q.Has("since") && !a.startAtLatest(w, &f) {
		return
	}
	want := map[string]bool{"message": true, "update": true, "status": true}
	if v := q.Get("events"); v != "" {
		want = map[string]bool{}
		for e := range strings.SplitSeq(v, ",") {
			want[strings.TrimSpace(e)] = true
		}
	}

	p := principal(r)
	release, ok := a.streams.acquire(p)
	if !ok {
		writeErr(w, http.StatusTooManyRequests, errTooManyStreams.Error())
		return
	}
	defer release()
	ch, unsub := a.b.Subscribe()
	defer unsub()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, ": connected\n\n")

	send := func(event, id string, v any) {
		data, _ := json.Marshal(v)
		if id != "" {
			fmt.Fprintf(w, "id: %s\n", id)
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	}
	// catchUp sends every stored match after f.Since; false ends the stream (the database failed).
	catchUp := func() bool {
		if !want["message"] {
			return true
		}
		for r.Context().Err() == nil {
			msgs, err := a.b.Store.Query(f, 500, true)
			if err != nil {
				a.b.Log.Error("events: read messages", "err", err)
				return false
			}
			for _, m := range msgs {
				send("message", strconv.FormatInt(m.Seq, 10), m)
				f.Since = m.Seq
			}
			_ = rc.Flush()
			if len(msgs) < 500 {
				return true
			}
		}
		return false
	}
	if !catchUp() {
		return
	}
	_ = rc.Flush()

	unfiltered := f
	unfiltered.Since = 0
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	recheck := time.NewTicker(streamRecheck)
	defer recheck.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-a.closing.Done():
			return
		case <-recheck.C:
			if !a.stillValid(p) {
				return
			}
			continue
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
		case v := <-ch:
			if !a.stillValid(p) {
				return
			}
			switch v := v.(type) {
			case store.Message, store.ChatsChanged:
				if !catchUp() {
					return
				}
			case store.MessageUpdate:
				if want["update"] && unfiltered.Match(v.Message) {
					send("update", "", v.Message)
				}
			case bot.Status:
				if want["status"] {
					send("status", "", visibleStatus(p, v))
				}
			case bot.QueueEvent:
				if want["queue"] && (p.Has(auth.Admin) || v.Item.Actor == p.Name) {
					send("queue", "", v.Item)
				}
			}
		}
		_ = rc.Flush()
	}
}

func (a *server) media(w http.ResponseWriter, r *http.Request) {
	seq, err := strconv.ParseInt(r.PathValue("seq"), 10, 64)
	if err != nil {
		writeError(w, bot.BadInput("seq must be a number"))
		return
	}
	if p := principal(r); p.Restricted() {
		if m, err := a.b.Store.GetBySeq(seq); err != nil || !a.b.ChatAllowed(r.Context(), p.Chats, m.Chat) {
			writeError(w, store.ErrNotFound)
			return
		}
	}
	data, m, err := a.b.DownloadMedia(r.Context(), seq)
	if dl, ok := errors.AsType[whatsmeow.DownloadHTTPError](err); ok && dl.Response != nil && slices.Contains([]int{403, 404, 410}, dl.Response.StatusCode) {
		writeErr(w, http.StatusGone, "media expired on WhatsApp's servers")
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	ctype, disposition := m.Media.Mimetype, "inline"
	if !inlineSafe(ctype) {
		ctype, disposition = "application/octet-stream", "attachment"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": m.MediaFileName()}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write(data)
}

func inlineSafe(mimetype string) bool {
	base, _, _ := strings.Cut(strings.ToLower(mimetype), ";")
	base = strings.TrimSpace(base)
	switch {
	case base == "image/svg+xml":
		return false
	case strings.HasPrefix(base, "image/"), strings.HasPrefix(base, "audio/"), strings.HasPrefix(base, "video/"), base == "application/pdf":
		return true
	}
	return false
}

func (a *server) chats(w http.ResponseWriter, r *http.Request) {
	limit := intParam(r, "limit", 50, maxPageSize)
	p := principal(r)
	if !p.Restricted() {
		chats, err := a.b.Chats(r.Context(), limit)
		respond(w, chats, err)
		return
	}
	chats, err := a.b.Chats(r.Context(), maxPageSize)
	if err != nil {
		writeError(w, err)
		return
	}
	allowed := a.b.ExpandAllowlist(r.Context(), p.Chats)
	chats = slices.DeleteFunc(chats, func(c store.Chat) bool { return !slices.Contains(allowed, c.JID) })
	writeJSON(w, 200, chats[:min(len(chats), limit)])
}

func (a *server) groups(w http.ResponseWriter, r *http.Request) {
	groups, err := a.b.Client.GetJoinedGroups(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	type item struct {
		JID     string `json:"jid"`
		Name    string `json:"name"`
		Members int    `json:"members"`
	}
	out := make([]item, len(groups))
	for i, g := range groups {
		out[i] = item{g.JID.String(), g.Name, len(g.Participants)}
	}
	writeJSON(w, 200, out)
}

func (a *server) check(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phones []string `json:"phones"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Phones) == 0 || len(req.Phones) > 50 {
		writeError(w, bot.BadInput("send between 1 and 50 phones"))
		return
	}
	out, err := a.b.Check(r.Context(), req.Phones)
	a.b.Audit(principal(r).Name, "check", "", fmt.Sprintf("%d numbers", len(req.Phones)), err)
	respond(w, out, err)
}

func (a *server) audit(w http.ResponseWriter, r *http.Request) {
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	entries, err := a.b.Store.AuditLog(intParam(r, "limit", 100, maxPageSize), before)
	respond(w, entries, err)
}

// Stats is the body of GET /stats: what the admin panel's Health tab shows.
type Stats struct {
	Version   string            `json:"version"`
	Started   time.Time         `json:"started"`
	AutoReply bool              `json:"auto_reply"`
	Limits    StatsLimits       `json:"limits"`
	Webhook   *bot.WebhookStats `json:"webhook"`
	Queue     bot.QueueStats    `json:"queue"`
	Alerts    bot.AlertStats    `json:"alerts"`
	Callers   []CallerStats     `json:"callers"`
}

// CallerStats is a token's (or the owner's, as "api") activity in the last 24 hours.
type CallerStats struct {
	bot.TokenUsage
	Streams int `json:"open_streams"`
}

type StatsLimits struct {
	Send      bot.LimiterStats `json:"send"`
	Lookups   bot.LimiterStats `json:"lookups"`
	AutoReply bot.LimiterStats `json:"auto_reply"`
}

func (a *server) stats(w http.ResponseWriter, r *http.Request) {
	out := Stats{
		Version: a.version, Started: a.b.Started, AutoReply: a.b.AutoReplyOn(),
		Limits: StatsLimits{Send: a.b.Limiter.Stats(), Lookups: a.b.Lookups.Stats(), AutoReply: a.b.AutoReplyStats()},
		Queue:  a.b.QueueStats(),
		Alerts: a.b.AlertStats(),
	}
	if a.b.Webhook != nil {
		s := a.b.Webhook.Stats()
		out.Webhook = &s
	}
	out.Callers = []CallerStats{}
	for _, u := range a.b.Usage() {
		out.Callers = append(out.Callers, CallerStats{u, a.streams.count(u.Name)})
	}
	writeJSON(w, 200, out)
}

func (a *server) settings(w http.ResponseWriter, r *http.Request) {
	v, err := a.b.SettingsView()
	respond(w, v, err)
}

// updateSettings takes {"key": value} where value is a string, a number or a boolean, or null to go
// back to the flag's value.
func (a *server) updateSettings(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if !readJSON(w, r, &raw) {
		return
	}
	changes := map[string]*string{}
	for k, v := range raw {
		if string(v) == "null" {
			changes[k] = nil
			continue
		}
		var s string
		if json.Unmarshal(v, &s) != nil {
			s = string(v)
		}
		changes[k] = &s
	}
	if err := a.b.UpdateSettings(principal(r).Name, changes); err != nil {
		writeError(w, err)
		return
	}
	a.settings(w, r)
}

func (a *server) webhookTest(w http.ResponseWriter, r *http.Request) {
	res, err := a.b.TestWebhook(r.Context(), principal(r).Name)
	webhookResult(w, res, err)
}

func (a *server) webhookReplay(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Delivery string `json:"delivery"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	res, err := a.b.ReplayWebhook(r.Context(), principal(r).Name, req.Delivery)
	webhookResult(w, res, err)
}

func (a *server) alertTest(w http.ResponseWriter, r *http.Request) {
	respond(w, map[string]bool{"ok": true}, a.b.TestAlert(r.Context(), principal(r).Name))
}

// webhookResult answers 200 with the attempt's outcome, even when the receiver failed: the request
// itself worked. Errors before any attempt (no webhook, unknown delivery) map as usual.
func webhookResult(w http.ResponseWriter, res bot.WebhookResult, err error) {
	if res.Delivery == "" && err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, res)
}
