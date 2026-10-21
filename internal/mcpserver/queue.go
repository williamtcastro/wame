package mcpserver

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/williamtcastro/wame/internal/auth"
	"github.com/williamtcastro/wame/internal/bot"
	"github.com/williamtcastro/wame/internal/store"
)

func (s *server) addQueueTools(srv *mcp.Server) {
	open, closed := true, false
	mcp.AddTool(srv, &mcp.Tool{Name: "schedule_message", Description: "Queue a text message, or a file the user already uploaded (by its id), to send later (or as soon as the limits allow). Only when the user asked for it; confirm the time in the user's time zone first.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &open, OpenWorldHint: &open}}, s.scheduleMessage)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_scheduled", Description: "Queued messages, newest first, with counts by status: this connection's own (admins see all, but only their own text).",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, s.listScheduled)
	mcp.AddTool(srv, &mcp.Tool{Name: "reschedule_scheduled", Description: "Change a queued message that hasn't gone out yet: its time, its send_before deadline or its text. Only this connection's own messages. Confirm the new time in the user's time zone first.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &open, OpenWorldHint: &closed}}, s.rescheduleScheduled)
	mcp.AddTool(srv, &mcp.Tool{Name: "cancel_scheduled", Description: "Cancel queued messages that haven't gone out yet: one by id, or a whole batch.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &open, OpenWorldHint: &closed}}, s.cancelScheduled)
}

// queueTarget checks a restricted caller's recipient; the queue worker resolves the others at send time.
func (s *server) queueTarget(ctx context.Context, p auth.Principal, to string) (string, error) {
	switch {
	case strings.TrimSpace(to) == "":
		return "", errors.New("to is required")
	}
	if _, isTag := bot.QueueTag(to); isTag {
		return to, nil // Bot expands it, dropping members outside the allowlist
	}
	switch {
	case p.Restricted():
		return s.allowedChat(ctx, p, to)
	}
	return to, nil
}

// parseSendAt reads send_at as people write it, relative forms included.
func parseSendAt(v string, now time.Time) (bot.SendTime, error) {
	t, err := bot.ParseSendTime(v, now, true)
	if err != nil {
		return t, fmt.Errorf("send_at %w", err)
	}
	return t, nil
}

// ownFilter limits callers without the admin scope to the items they queued.
func ownFilter(p auth.Principal, f store.OutboxFilter) store.OutboxFilter {
	return f.For(p.Name, p.Has(auth.Admin))
}

type scheduleIn struct {
	To         string `json:"to" jsonschema:"chat JID, phone number with country code, or tag:NAME for everyone with that contact tag (needs the read scope)"`
	Text       string `json:"text,omitempty" jsonschema:"message text (WhatsApp formatting like *bold* works); with file, its caption; required without file"`
	Template   bool   `json:"template,omitempty" jsonschema:"fill {name}, {first_name} or {push_name} (with an optional |fallback, like {name|there}) per recipient; {{ and }} are literal braces. Needs the read scope"`
	SendAt     string `json:"send_at,omitempty" jsonschema:"when to send: RFC 3339 with offset (2026-10-13T09:00:00-03:00); a date and time without offset (2026-10-13T09:00) or 09:00 / tomorrow 09:00 for that time on the recipient's own clock (their contact time zone, else the queue's); or a delay like 90m, 2h, 3d. Omit to send as soon as the limits allow"`
	SendBefore string `json:"send_before,omitempty" jsonschema:"don't send after this: a duration after send_at (2h) or a time like send_at; if it can't go out by then it ends expired"`
	Spread     string `json:"spread,omitempty" jsonschema:"with tag:, spread the members' messages evenly over this window from send_at (30m, 2h; at most 24h)"`
	Batch      string `json:"batch,omitempty" jsonschema:"group name for list_scheduled and cancel_scheduled (letters, digits, . _ -); generated when omitted"`
	File       string `json:"file,omitempty" jsonschema:"id (f_…) of a file the user uploaded with POST /queue/files under this same token; text becomes its caption and may be empty"`
}

type scheduledOut struct {
	ID     int64     `json:"id,omitempty"`
	Batch  string    `json:"batch"`
	To     string    `json:"to"`
	Status string    `json:"status"`
	Due    time.Time `json:"due"`
	Queued int       `json:"queued"`
	Note   string    `json:"note,omitempty"`
}

func (s *server) scheduleMessage(ctx context.Context, req *mcp.CallToolRequest, in scheduleIn) (*mcp.CallToolResult, scheduledOut, error) {
	p, err := s.require(req, auth.Send)
	if err != nil {
		return nil, scheduledOut{}, err
	}
	to, err := s.queueTarget(ctx, p, in.To)
	if err != nil {
		return nil, scheduledOut{}, err
	}
	at, err := parseSendAt(in.SendAt, time.Now().In(s.b.QueueLocation()))
	if err != nil {
		return nil, scheduledOut{}, err
	}
	before, err := bot.ParseDeadline(in.SendBefore)
	if err != nil {
		return nil, scheduledOut{}, fmt.Errorf("send_before: %w", err)
	}
	spread, err := bot.ParseSpread(in.Spread)
	if err != nil {
		return nil, scheduledOut{}, err
	}
	res, err := s.b.Queue(ctx, bot.QueueOptions{Actor: p.Name, TokenID: p.TokenID, Batch: in.Batch, Template: in.Template,
		Read: p.Has(auth.Read), Allow: p.Chats, Spread: spread},
		[]bot.QueueRequest{{To: to, Text: in.Text, SendAt: at, SendBefore: before, File: strings.TrimSpace(in.File)}})
	if err != nil {
		return nil, scheduledOut{}, err
	}
	if len(res.Items) == 0 {
		return nil, scheduledOut{}, errors.New("nothing was queued")
	}
	it := res.Items[0]
	out := scheduledOut{ID: it.ID, Batch: it.Batch, To: cleanID(it.To), Status: it.Status, Due: it.Due, Queued: len(res.Items)}
	if _, isTag := bot.QueueTag(in.To); isTag {
		// One item per member: list_scheduled with the batch shows them.
		out.ID, out.To = 0, cleanID(strings.TrimSpace(in.To))
	}
	switch st := s.b.QueueStats(); {
	case st.Paused:
		out.Note = "the queue is paused; nothing goes out until an admin resumes it"
	case !st.Running:
		out.Note = "no queue worker runs in this process; it goes out once wame serve or wame tui runs on this database"
	}
	return nil, out, nil
}

type listScheduledIn struct {
	Status string `json:"status,omitempty" jsonschema:"queued, sending, sent, failed, canceled or expired"`
	Batch  string `json:"batch,omitempty" jsonschema:"only this batch"`
	Before int64  `json:"before,omitempty" jsonschema:"only ids below this one, to page"`
	Limit  int    `json:"limit,omitempty" jsonschema:"max items (default 50, max 200)"`
}

type queueItemOut struct {
	ID        int64     `json:"id"`
	Batch     string    `json:"batch"`
	Actor     string    `json:"actor"`
	To        string    `json:"to"`
	Chat      string    `json:"chat,omitempty"`
	Status    string    `json:"status"`
	Attempts  int       `json:"attempts"`
	Due       time.Time `json:"due"`
	Before    time.Time `json:"send_before,omitzero"`
	Edited    time.Time `json:"edited_at,omitzero"`
	Created   time.Time `json:"created_at"`
	Updated   time.Time `json:"updated_at"`
	MessageID string    `json:"message_id,omitempty"`
	Error     string    `json:"error,omitempty"`
	Delivered time.Time `json:"delivered_at,omitzero"`
	Read      time.Time `json:"read_at,omitzero"`
	Kind      string    `json:"kind"`
	FileSize  int64     `json:"file_size,omitempty"`
	Untrusted struct {
		Text string `json:"text,omitempty"`
	} `json:"untrusted"`
}

type listScheduledOut struct {
	Items  []queueItemOut     `json:"items"`
	Counts store.OutboxCounts `json:"counts"`
	Note   string             `json:"note"`
}

func (s *server) listScheduled(ctx context.Context, req *mcp.CallToolRequest, in listScheduledIn) (*mcp.CallToolResult, listScheduledOut, error) {
	p, err := s.require(req, auth.Send)
	if err != nil {
		return nil, listScheduledOut{}, err
	}
	if err := store.ValidOutboxStatus(in.Status); err != nil {
		return nil, listScheduledOut{}, err
	}
	f := ownFilter(p, store.OutboxFilter{Status: in.Status, Batch: in.Batch, Before: in.Before})
	items, err := s.b.Store.ListOutbox(f, clamp(in.Limit, 50, 200))
	if err != nil {
		return nil, listScheduledOut{}, err
	}
	counts, err := s.b.Store.CountOutbox(f)
	if err != nil {
		return nil, listScheduledOut{}, err
	}
	out := listScheduledOut{Items: []queueItemOut{}, Counts: counts, Note: untrustedNote}
	for _, it := range items {
		o := queueItemOut{ID: it.ID, Batch: it.Batch, Actor: it.Actor, To: cleanID(it.To), Chat: it.Chat, Status: it.Status,
			Attempts: it.Attempts, Due: it.Due, Before: it.SendBefore, Edited: it.Edited, Created: it.Created, Updated: it.Updated, MessageID: it.MessageID, Error: clean(it.Error),
			Delivered: it.Delivered, Read: it.Read, Kind: cmp.Or(it.FileKind, "text"), FileSize: it.FileSize}
		if it.Actor == p.Name {
			o.Untrusted.Text = clean(it.Text)
		}
		out.Items = append(out.Items, o)
	}
	return nil, out, nil
}

type rescheduleIn struct {
	ID         int64   `json:"id" jsonschema:"the queued item's id"`
	SendAt     string  `json:"send_at,omitempty" jsonschema:"the new time, in any form schedule_message takes"`
	SendBefore string  `json:"send_before,omitempty" jsonschema:"a new deadline (2h after send_at, or a time); \"none\" removes it"`
	Text       *string `json:"text,omitempty" jsonschema:"the new text (a file's caption, which may be empty)"`
}

func (s *server) rescheduleScheduled(ctx context.Context, req *mcp.CallToolRequest, in rescheduleIn) (*mcp.CallToolResult, scheduledOut, error) {
	p, err := s.require(req, auth.Send)
	if err != nil {
		return nil, scheduledOut{}, err
	}
	if in.ID <= 0 {
		return nil, scheduledOut{}, errors.New("id is required")
	}
	e := bot.QueueEdit{Text: in.Text}
	if e.SendAt, err = parseSendAt(in.SendAt, time.Now().In(s.b.QueueLocation())); err != nil {
		return nil, scheduledOut{}, err
	}
	if strings.EqualFold(strings.TrimSpace(in.SendBefore), "none") {
		e.ClearBefore = true
	} else if e.SendBefore, err = bot.ParseDeadline(in.SendBefore); err != nil {
		return nil, scheduledOut{}, fmt.Errorf("send_before: %w", err)
	}
	// Only the caller's own items, even for admins: the text is the caller's to change.
	it, err := s.b.EditQueued(p.Name, in.ID, store.OutboxFilter{Actor: p.Name}, e)
	if _, notQueued := errors.AsType[bot.NotQueuedError](err); !notQueued && errors.Is(err, store.ErrNotFound) {
		return nil, scheduledOut{}, fmt.Errorf("no queued message #%d from this connection", in.ID)
	}
	if err != nil {
		return nil, scheduledOut{}, err
	}
	return nil, scheduledOut{ID: it.ID, Batch: it.Batch, To: cleanID(it.To), Status: it.Status, Due: it.Due, Queued: 1}, nil
}

type cancelScheduledIn struct {
	ID    int64  `json:"id,omitempty" jsonschema:"one queued item's id"`
	Batch string `json:"batch,omitempty" jsonschema:"cancel every queued item of this batch instead"`
}

type cancelScheduledOut struct {
	Canceled []int64 `json:"canceled"`
}

func (s *server) cancelScheduled(ctx context.Context, req *mcp.CallToolRequest, in cancelScheduledIn) (*mcp.CallToolResult, cancelScheduledOut, error) {
	p, err := s.require(req, auth.Send)
	if err != nil {
		return nil, cancelScheduledOut{}, err
	}
	if (in.ID > 0) == (in.Batch != "") {
		return nil, cancelScheduledOut{}, errors.New("give either id or batch")
	}
	items, err := s.b.CancelQueued(p.Name, in.ID, ownFilter(p, store.OutboxFilter{Batch: in.Batch}))
	if _, notQueued := errors.AsType[bot.NotQueuedError](err); !notQueued && errors.Is(err, store.ErrNotFound) {
		return nil, cancelScheduledOut{}, fmt.Errorf("no queued message #%d from this connection", in.ID)
	}
	if err != nil {
		return nil, cancelScheduledOut{}, err
	}
	out := cancelScheduledOut{Canceled: []int64{}}
	for _, it := range items {
		out.Canceled = append(out.Canceled, it.ID)
	}
	return nil, out, nil
}
