// Package admin is a terminal admin panel for a wame server, talking to it only over the HTTP API.
package admin

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/williamtcastro/wame/internal/api"
	"github.com/williamtcastro/wame/internal/bot"
	"github.com/williamtcastro/wame/internal/store"
)

type Client struct {
	base   *url.URL
	token  string
	http   *http.Client
	stream *http.Client
}

// NewClient checks the server URL. Plain http is only allowed to loopback addresses unless insecure
// is set, because the token travels in every request.
func NewClient(server, token string, insecure bool) (*Client, error) {
	if !strings.Contains(server, "://") {
		server = "https://" + server
	}
	u, err := url.Parse(server)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("server must be an http(s) URL, like https://wame.example.com")
	}
	if u.Scheme == "http" && !insecure && !isLoopback(u.Hostname()) {
		return nil, fmt.Errorf("refusing to send the token over plain http to %s: use https, an SSH tunnel to 127.0.0.1, or -insecure", u.Host)
	}
	if token == "" {
		return nil, errors.New("a token is required: pass -token or set WAME_TOKEN (in the environment, ./.env or ~/.config/wame/admin.env)")
	}
	u.RawQuery, u.Fragment = "", ""
	stream := &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: 30 * time.Second}}
	return &Client{base: u, token: token, http: &http.Client{Timeout: 30 * time.Second}, stream: stream}, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Server is the URL shown in the panel header.
func (c *Client) Server() string { return c.base.String() }

// APIError is a non-2xx answer from the server; RetryAfter is its Retry-After (a 429), or 0.
type APIError struct {
	Code       int
	Msg        string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	switch e.Code {
	case http.StatusUnauthorized:
		return "the server rejected the token (401)"
	case http.StatusForbidden:
		return "this token can't do that (403): " + cmp.Or(e.Msg, "it needs the admin scope")
	}
	if e.Msg == "" {
		return http.StatusText(e.Code)
	}
	return e.Msg
}

// isNotFound reports a 404, which is how an older server answers a route it doesn't have yet.
func isNotFound(err error) bool {
	apiErr, ok := errors.AsType[*APIError](err)
	return ok && apiErr.Code == http.StatusNotFound
}

// isForbidden reports a 403: the token lacks the scope the route needs.
func isForbidden(err error) bool {
	apiErr, ok := errors.AsType[*APIError](err)
	return ok && apiErr.Code == http.StatusForbidden
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	if body == nil {
		return c.send(ctx, c.http, method, path, query, nil, nil, out)
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return c.send(ctx, c.http, method, path, query, bytes.NewReader(data), http.Header{"Content-Type": {"application/json"}}, out)
}

// maxRawAnswer caps an answer read as bytes (a CSV export) rather than JSON.
const maxRawAnswer = 64 << 20

// send makes one request with a raw body on hc and decodes a 2xx JSON answer into out, or with
// out a *[]byte, keeps the answer as it came.
func (c *Client) send(ctx context.Context, hc *http.Client, method, path string, query url.Values, body io.Reader, header http.Header, out any) error {
	u := c.base.JoinPath(path)
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := hc.Do(req)
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			return ue.Err
		}
		return err
	}
	defer resp.Body.Close()
	limit := int64(16 << 20)
	raw, isRaw := out.(*[]byte)
	if isRaw {
		limit = maxRawAnswer
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		apiErr := &APIError{Code: resp.StatusCode, Msg: e.Error}
		if n, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && n > 0 {
			apiErr.RetryAfter = time.Duration(n) * time.Second
		}
		return apiErr
	}
	switch {
	case isRaw && int64(len(data)) > limit:
		return fmt.Errorf("the answer from %s is larger than %d MB", path, limit>>20)
	case isRaw:
		*raw = data
		return nil
	case out == nil:
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("unexpected answer from %s: %w", path, err)
	}
	return nil
}

func (c *Client) Status(ctx context.Context) (bot.Status, error) {
	var s bot.Status
	err := c.do(ctx, "GET", "status", nil, nil, &s)
	return s, err
}

func (c *Client) Pair(ctx context.Context, phone string) (string, error) {
	var out struct {
		Code string `json:"code"`
	}
	err := c.do(ctx, "POST", "pair", nil, map[string]string{"phone": phone}, &out)
	return out.Code, err
}

func (c *Client) Logout(ctx context.Context) error {
	return c.do(ctx, "POST", "logout", nil, struct{}{}, nil)
}

func (c *Client) Tokens(ctx context.Context) ([]store.Token, error) {
	var out []store.Token
	err := c.do(ctx, "GET", "tokens", nil, nil, &out)
	return out, err
}

// TokenRequest is the body of POST /tokens.
type TokenRequest struct {
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	Chats     []string `json:"chats,omitempty"`
	Rate      *int     `json:"rate,omitempty"`
	ExpiresIn string   `json:"expires_in,omitempty"`
}

// CreateToken returns the plain token, which the server shows only this once.
func (c *Client) CreateToken(ctx context.Context, r TokenRequest) (string, store.Token, error) {
	var out struct {
		Token   string      `json:"token"`
		Details store.Token `json:"details"`
	}
	err := c.do(ctx, "POST", "tokens", nil, r, &out)
	return out.Token, out.Details, err
}

func (c *Client) RevokeToken(ctx context.Context, name string) (int, error) {
	var out struct {
		Revoked int `json:"revoked"`
	}
	err := c.do(ctx, "DELETE", "tokens/"+url.PathEscape(name), nil, nil, &out)
	return out.Revoked, err
}

// Audit returns up to limit entries, newest first, older than before (0 = from the newest).
func (c *Client) Audit(ctx context.Context, limit int, before int64) ([]store.AuditEntry, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if before > 0 {
		q.Set("before", strconv.FormatInt(before, 10))
	}
	var out []store.AuditEntry
	err := c.do(ctx, "GET", "audit", q, nil, &out)
	return out, err
}

// Stats returns the server's limiter and webhook health (GET /stats).
func (c *Client) Stats(ctx context.Context) (api.Stats, error) {
	var s api.Stats
	err := c.do(ctx, "GET", "stats", nil, nil, &s)
	return s, err
}

func (c *Client) Chats(ctx context.Context, limit int) ([]store.Chat, error) {
	var out []store.Chat
	err := c.do(ctx, "GET", "chats", url.Values{"limit": {strconv.Itoa(limit)}}, nil, &out)
	return out, err
}

// Messages returns the newest messages first, in one chat or (with q) matching a text search.
func (c *Client) Messages(ctx context.Context, chat, q string, limit int) ([]store.Message, error) {
	v := url.Values{"limit": {strconv.Itoa(limit)}}
	if chat != "" {
		v.Set("chat", chat)
	}
	if q != "" {
		v.Set("q", q)
	}
	var out []store.Message
	err := c.do(ctx, "GET", "messages", v, nil, &out)
	return out, err
}

// Send sends a text message. The idempotency key makes a retried request safe.
func (c *Client) Send(ctx context.Context, to, text, key string) (store.Message, error) {
	var m store.Message
	err := c.do(ctx, "POST", "send", nil, map[string]string{"to": to, "text": text, "idempotency_key": key}, &m)
	return m, err
}

// Scheduled is the answer to a send with send_at: the queued items, not a sent message.
type Scheduled struct {
	Batch    string             `json:"batch"`
	Queued   int                `json:"queued"`
	Replayed int                `json:"replayed"`
	Items    []store.OutboxItem `json:"items"`
}

// SendAt queues a text message for sendAt (POST /send with send_at); sendBefore may be "".
func (c *Client) SendAt(ctx context.Context, to, text, key, sendAt, sendBefore string) (Scheduled, error) {
	var out Scheduled
	body := map[string]string{"to": to, "text": text, "idempotency_key": key, "send_at": sendAt}
	if sendBefore != "" {
		body["send_before"] = sendBefore
	}
	err := c.do(ctx, "POST", "send", nil, body, &out)
	return out, err
}

// EditQueued changes a queued message (PATCH /queue/{id}); 409 if it left the queue.
func (c *Client) EditQueued(ctx context.Context, id int64, fields map[string]any) (store.OutboxItem, error) {
	var it store.OutboxItem
	err := c.do(ctx, "PATCH", "queue/"+strconv.FormatInt(id, 10), nil, fields, &it)
	return it, err
}

// Event is one server-sent event from GET /events.
type Event struct {
	Type string
	Data json.RawMessage
}

// streamIdle is how long a stream may stay silent; the server pings every 25 s.
const streamIdle = 60 * time.Second

// Events streams the given event types (comma-separated) into out until ctx ends or the stream
// breaks; connected is called once the server accepts it.
func (c *Client) Events(ctx context.Context, events string, connected func(), out func(Event)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	u := c.base.JoinPath("events")
	u.RawQuery = url.Values{"events": {events}}.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.stream.Do(req)
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			return ue.Err
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e)
		return &APIError{Code: resp.StatusCode, Msg: e.Error}
	}
	connected()
	idle := time.AfterFunc(streamIdle, cancel)
	defer idle.Stop()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	var ev Event
	var data []byte
	for sc.Scan() {
		idle.Reset(streamIdle)
		line := sc.Text()
		switch {
		case line == "":
			if ev.Type != "" && data != nil {
				ev.Data = data
				out(ev)
			}
			ev, data = Event{}, nil
		case strings.HasPrefix(line, "event:"):
			ev.Type = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")...)
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	if ctx.Err() != nil && ctx.Err() != context.Canceled {
		return ctx.Err()
	}
	return errors.New("stream closed")
}

func (c *Client) Settings(ctx context.Context) ([]bot.SettingView, error) {
	var out []bot.SettingView
	err := c.do(ctx, "GET", "settings", nil, nil, &out)
	return out, err
}

// UpdateSettings changes settings; a nil value resets one to the server's flag.
func (c *Client) UpdateSettings(ctx context.Context, changes map[string]*string) ([]bot.SettingView, error) {
	var out []bot.SettingView
	err := c.do(ctx, "PATCH", "settings", nil, changes, &out)
	return out, err
}

func (c *Client) WebhookTest(ctx context.Context) (bot.WebhookResult, error) {
	var out bot.WebhookResult
	err := c.do(ctx, "POST", "webhook/test", nil, struct{}{}, &out)
	return out, err
}

func (c *Client) WebhookReplay(ctx context.Context, delivery string) (bot.WebhookResult, error) {
	var out bot.WebhookResult
	err := c.do(ctx, "POST", "webhook/replay", nil, map[string]string{"delivery": delivery}, &out)
	return out, err
}

// AlertTest sends a test alert through the server's alert backend.
func (c *Client) AlertTest(ctx context.Context) error {
	return c.do(ctx, "POST", "alerts/test", nil, struct{}{}, nil)
}

// QueuePage is the body of GET /queue.
type QueuePage struct {
	Items  []store.OutboxItem `json:"items"`
	Counts store.OutboxCounts `json:"counts"`
}

func (c *Client) Queue(ctx context.Context, status string, limit int) (QueuePage, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if status != "" {
		q.Set("status", status)
	}
	var out QueuePage
	err := c.do(ctx, "GET", "queue", q, nil, &out)
	return out, err
}

// QueueCounts returns the counts by state of the caller's queued items (every token's, for an admin),
// in one batch when batch is set.
func (c *Client) QueueCounts(ctx context.Context, batch string) (store.OutboxCounts, error) {
	q := url.Values{"limit": {"1"}}
	if batch != "" {
		q.Set("batch", batch)
	}
	var out QueuePage
	err := c.do(ctx, "GET", "queue", q, nil, &out)
	return out.Counts, err
}

// QueueResult is the answer of POST /queue; items come without their text.
type QueueResult struct {
	Batch       string             `json:"batch"`
	Queued      int                `json:"queued"`
	Replayed    int                `json:"replayed"` // of Queued, items an earlier request with the same key already queued
	Items       []store.OutboxItem `json:"items"`
	Skipped     bot.SkipCounts     `json:"skipped"`
	SkippedRows []bot.SkippedRow   `json:"skipped_rows"`
}

// QueueCSVOptions are POST /queue's options for a CSV upload (see docs/QUEUE.md).
type QueueCSVOptions struct {
	Template bool
	Dedupe   bool
	SkipSent string
	File     string // default file id for every row (see UploadQueueFile)
}

// QueueCSV queues the messages of a CSV file (at most 1000 rows, see docs/QUEUE.md) under batch;
// the idempotency key makes a retry return the items already queued instead of adding them again.
func (c *Client) QueueCSV(ctx context.Context, csv []byte, batch, key string, opt QueueCSVOptions) (QueueResult, error) {
	q := url.Values{}
	if batch != "" {
		q.Set("batch", batch)
	}
	if opt.Template {
		q.Set("template", "1")
	}
	if opt.Dedupe {
		q.Set("dedupe", "1")
	}
	if opt.SkipSent != "" {
		q.Set("skip_sent", opt.SkipSent)
	}
	if opt.File != "" {
		q.Set("file", opt.File)
	}
	h := http.Header{"Content-Type": {"text/csv; charset=utf-8"}}
	if key != "" {
		h.Set("Idempotency-Key", key)
	}
	var out QueueResult
	// c.stream has no overall timeout, so a large upload on a slow link isn't cut off; the answer
	// still has to start within 30 s of the upload ending.
	err := c.send(ctx, c.stream, "POST", "queue", q, bytes.NewReader(csv), h, &out)
	return out, err
}

// statusTap records the status code of the last answer that passed through it.
type statusTap struct {
	next http.RoundTripper
	code *int
}

func (s statusTap) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := s.next.RoundTrip(r)
	if resp != nil {
		*s.code = resp.StatusCode
	}
	return resp, err
}

// UploadQueueFile sends r as a queue file named name (POST /queue/files, streamed, never held in
// memory). existed is true when the server already had the same bytes from this token: it answers
// 200 with the same id instead of 201, so an upload is safe to repeat.
func (c *Client) UploadQueueFile(ctx context.Context, name string, r io.Reader) (f store.OutboxFile, existed bool, err error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		part, err := mw.CreateFormFile("file", name)
		if err == nil {
			_, err = io.Copy(part, r)
		}
		if err == nil {
			err = mw.Close()
		}
		pw.CloseWithError(err)
	}()
	defer pr.Close()
	code := 0
	hc := *c.stream
	hc.Transport = statusTap{next: cmp.Or(c.stream.Transport, http.DefaultTransport), code: &code}
	err = c.send(ctx, &hc, "POST", "queue/files", nil, pr, http.Header{"Content-Type": {mw.FormDataContentType()}}, &f)
	return f, code == http.StatusOK, err
}

// Logs returns the server's recent app logs at level or above ("" for all), newest first; with since
// set, only entries newer than that id.
func (c *Client) Logs(ctx context.Context, level string, since int64, limit int) ([]bot.LogEntry, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if level != "" {
		q.Set("level", level)
	}
	if since > 0 {
		q.Set("since", strconv.FormatInt(since, 10))
	}
	var out api.LogPage
	err := c.do(ctx, "GET", "logs", q, nil, &out)
	return out.Entries, err
}

// CancelQueued cancels one queued message, or with batch set, every queued one in that batch.
func (c *Client) CancelQueued(ctx context.Context, id int64, batch string) (int, error) {
	path, q := "queue/"+strconv.FormatInt(id, 10), url.Values(nil)
	if batch != "" {
		path, q = "queue", url.Values{"batch": {batch}}
	}
	var out struct {
		Canceled int `json:"canceled"`
	}
	err := c.do(ctx, "DELETE", path, q, nil, &out)
	return out.Canceled, err
}

// QueueFile is an uploaded queue file as GET /queue/files shows it. The panel never keeps the
// file's name: ListQueueFiles blanks it.
type QueueFile struct {
	store.OutboxFile
	Expires time.Time `json:"expires_at,omitzero"`
}

// QueueFiles is the answer of GET /queue/files: the live files and their usage (the caller's own,
// or everyone's for an admin).
type QueueFiles struct {
	Files []QueueFile           `json:"files"`
	Usage store.OutboxFileUsage `json:"usage"`
}

// ListQueueFiles lists the uploaded queue files with their usage, without their names.
func (c *Client) ListQueueFiles(ctx context.Context) (QueueFiles, error) {
	var out QueueFiles
	err := c.do(ctx, "GET", "queue/files", nil, nil, &out)
	for i := range out.Files {
		out.Files[i].Name = ""
	}
	return out, err
}

// DeleteQueueFile deletes an uploaded file; the server answers 409 while queued messages use it.
func (c *Client) DeleteQueueFile(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "queue/files/"+url.PathEscape(id), nil, nil, nil)
}

// OptOuts lists the chats that get no queued messages, newest first.
func (c *Client) OptOuts(ctx context.Context) ([]store.OptOut, error) {
	var out []store.OptOut
	err := c.do(ctx, "GET", "optouts", nil, nil, &out)
	return out, err
}

// AddOptOut lists a number or JID. The answer's Chat is the key the server stored it under; added
// is false when it was already listed.
func (c *Client) AddOptOut(ctx context.Context, chat string) (o store.OptOut, added bool, err error) {
	var out struct {
		Chat  string `json:"chat"`
		Added bool   `json:"added"`
	}
	err = c.do(ctx, "POST", "optouts", nil, map[string]string{"chat": chat}, &out)
	return store.OptOut{Chat: out.Chat}, out.Added, err
}

func (c *Client) RemoveOptOut(ctx context.Context, chat string) error {
	return c.do(ctx, "DELETE", "optouts/"+url.PathEscape(chat), nil, nil, nil)
}

// ContactsQuery filters GET /contacts: Q matches names, tags and the number, every tag in Tags must
// be set, Known drops people known only by push name or number.
type ContactsQuery struct {
	Q     string
	Tags  []string
	Known bool
	Limit int
}

// Contacts lists the people in the phone's address book and the server's own contact list, merged.
func (c *Client) Contacts(ctx context.Context, q ContactsQuery) ([]bot.Contact, error) {
	v := url.Values{"tag": q.Tags}
	if q.Q != "" {
		v.Set("q", q.Q)
	}
	if q.Known {
		v.Set("known", "1")
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	var out []bot.Contact
	err := c.do(ctx, "GET", "contacts", v, nil, &out)
	return out, err
}

// ContactTags counts the people per tag. A server older than the own contact list answers 404.
func (c *Client) ContactTags(ctx context.Context) ([]store.TagCount, error) {
	var out []store.TagCount
	err := c.do(ctx, "GET", "contacts/tags", nil, nil, &out)
	return out, err
}

// Contact is one person's profile by number or JID: all of it for an admin token; names, tags and
// whether they're on WhatsApp for others. It never asks WhatsApp.
func (c *Client) Contact(ctx context.Context, number string) (bot.Profile, error) {
	p, _, err := c.profile(ctx, number, false)
	return p, err
}

// RefreshContact asks WhatsApp about the person first (admin only; 2 to 4 lookups, once per person
// per 5 minutes) and answers the profile with Refreshed set. A server without refreshes ignores the
// parameter and answers Refreshed nil.
func (c *Client) RefreshContact(ctx context.Context, number string) (bot.Profile, error) {
	p, _, err := c.profile(ctx, number, true)
	return p, err
}

// profileShape says what a profile answer can tell: old is a server from before profiles (a
// bot.ContactView); notes, that the answer carries the notes (an admin's).
type profileShape struct{ old, notes bool }

func (c *Client) profile(ctx context.Context, number string, refresh bool) (bot.Profile, profileShape, error) {
	var q url.Values
	if refresh {
		q = url.Values{"refresh": {"1"}}
	}
	var raw json.RawMessage
	if err := c.do(ctx, "GET", "contacts/"+url.PathEscape(number), q, nil, &raw); err != nil {
		return bot.Profile{}, profileShape{}, err
	}
	var out struct {
		bot.Profile
		Names *bot.Names `json:"names"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return bot.Profile{}, profileShape{}, fmt.Errorf("unexpected answer from contacts: %w", err)
	}
	if out.Names != nil {
		out.Profile.Names = *out.Names
		return out.Profile, profileShape{notes: out.OptedOut != nil}, nil
	}
	var old bot.ContactView
	if err := json.Unmarshal(raw, &old); err != nil {
		return bot.Profile{}, profileShape{}, fmt.Errorf("unexpected answer from contacts: %w", err)
	}
	return oldProfile(old), profileShape{old: true, notes: old.Own != nil}, nil
}

// oldProfile reads a server's answer from before profiles (a bot.ContactView).
func oldProfile(v bot.ContactView) bot.Profile {
	p := bot.Profile{JID: v.JID, Phone: v.Phone, Names: v.Names, Own: bot.ProfileOwn{Tags: v.Tags}}
	if o := v.Own; o != nil {
		p.Own = bot.ProfileOwn{Tags: o.Tags, Notes: o.Notes, Language: o.Language, TimeZone: o.TimeZone,
			CreatedAt: o.CreatedAt, CreatedBy: o.CreatedBy, UpdatedAt: o.UpdatedAt, UpdatedBy: o.UpdatedBy}
	}
	return p
}

// PatchContact changes only the fields set in in; the answer is the person as the token may see them.
func (c *Client) PatchContact(ctx context.Context, number string, in bot.ContactInput) (bot.ContactView, error) {
	var out bot.ContactView
	err := c.do(ctx, "PATCH", "contacts/"+url.PathEscape(number), nil, in, &out)
	return out, err
}

// DeleteContact removes your own fields for a person; 404 when there were none.
func (c *Client) DeleteContact(ctx context.Context, number string) error {
	return c.do(ctx, "DELETE", "contacts/"+url.PathEscape(number), nil, nil, nil)
}

// ImportContacts uploads a contacts CSV (see bot.ContactImportColumns); with dryRun the server only
// counts what it would add and update.
func (c *Client) ImportContacts(ctx context.Context, csv []byte, dryRun bool) (store.ImportResult, error) {
	var q url.Values
	if dryRun {
		q = url.Values{"dry_run": {"1"}}
	}
	var out store.ImportResult
	err := c.send(ctx, c.stream, "POST", "contacts/import", q, bytes.NewReader(csv), http.Header{"Content-Type": {"text/csv; charset=utf-8"}}, &out)
	return out, err
}

// ExportContacts returns the server's own contact list as CSV, in the import's columns.
func (c *Client) ExportContacts(ctx context.Context) ([]byte, error) {
	var out []byte
	err := c.send(ctx, c.stream, "GET", "contacts/export", nil, nil, nil, &out)
	return out, err
}
