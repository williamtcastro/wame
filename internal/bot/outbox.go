package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"go.mau.fi/whatsmeow/types"

	"github.com/williamtcastro/wame/internal/store"
)

const (
	MaxQueueBatch    = 1000
	maxQueueText     = 65536
	maxQueueAhead    = 30 * 24 * time.Hour
	MaxKeyLength     = 200
	maxQueueTo       = 64
	maxQueueReplyTo  = 128
	outboxAttempts   = 5
	outboxKeep       = 7 * 24 * time.Hour
	outboxIdle       = time.Minute
	outboxOffline    = 5 * time.Second
	outboxMinStep    = 10 * time.Millisecond
	outboxSlotWithin = 250 * time.Millisecond
	maxOutboxBackoff = 15 * time.Minute
	maxCooldown      = 10 * time.Minute
)

var (
	maxQueueOpen = 10000 // per actor; tests lower it

	batchName    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	errBatchName = BadInput("batch must be 1-64 letters, digits, '.', '_' or '-'")

	// ErrKeyReused rejects a repeated idempotency key whose request differs: silently returning the
	// first result would hide a bug in the caller.
	ErrKeyReused = errors.New("idempotency key was already used for a different request")
)

// QueueRequest is one message to queue. A zero SendAt means as soon as the limits allow.
// Line is the CSV line it came from, for errors.
type QueueRequest struct {
	To         string
	Text       string
	ReplyTo    string
	SendAt     SendTime
	SendBefore Deadline
	Line       int
	// Name is the {name} for this message's template; it wins over every name wame knows.
	Name string
	File string // a queue file's id; Text is then its caption and may be empty
}

// Where names the i-th message of a request in errors: its CSV line, or its index.
func (r QueueRequest) Where(i int) string {
	if r.Line > 0 {
		return fmt.Sprintf("line %d", r.Line)
	}
	return fmt.Sprintf("messages[%d]", i)
}

// QueueEvent is published (and sent to the webhook as event "queue") when an item changes state.
// It never carries the message text.
type QueueEvent struct{ Item store.OutboxItem }

// QueueStats is the outbox's state for GET /stats and the admin panel.
type QueueStats struct {
	store.OutboxCounts
	Paused        bool      `json:"paused"`
	Running       bool      `json:"running"`
	JitterMS      int64     `json:"jitter_ms"`
	QuietHours    string    `json:"quiet_hours,omitempty"`
	Timezone      string    `json:"timezone"`
	QuietUntil    time.Time `json:"quiet_until,omitzero"`
	QuietOff      bool      `json:"quiet_off,omitempty"`
	CooldownUntil time.Time `json:"cooldown_until,omitzero"`
	LastError     string    `json:"last_error,omitempty"`
	LastErrorAt   time.Time `json:"last_error_at,omitzero"`
	Files         int       `json:"files"`
	FileBytes     int64     `json:"file_bytes"`
	FilesQuota    int64     `json:"files_quota"`
}

// QuietHours is a daily window, in minutes since midnight, when the queue sends nothing. It may
// wrap midnight (21:00-08:00). The zero value is off.
type QuietHours struct{ start, end int }

// ParseQuietHours reads "HH:MM-HH:MM"; "" turns quiet hours off.
func ParseQuietHours(s string) (QuietHours, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return QuietHours{}, nil
	}
	from, to, ok := strings.Cut(s, "-")
	start, err1 := time.Parse("15:04", strings.TrimSpace(from))
	end, err2 := time.Parse("15:04", strings.TrimSpace(to))
	switch {
	case !ok || err1 != nil || err2 != nil:
		return QuietHours{}, fmt.Errorf("%q isn't a window like 21:00-08:00 (empty turns it off)", s)
	case start.Equal(end):
		return QuietHours{}, errors.New("start and end must differ")
	}
	return QuietHours{start.Hour()*60 + start.Minute(), end.Hour()*60 + end.Minute()}, nil
}

func (q QuietHours) On() bool { return q.start != q.end }

func (q QuietHours) String() string {
	if !q.On() {
		return ""
	}
	return fmt.Sprintf("%02d:%02d-%02d:%02d", q.start/60, q.start%60, q.end/60, q.end%60)
}

// Until is when the window that now falls in ends, read on loc's wall clock, or zero outside it.
func (q QuietHours) Until(now time.Time, loc *time.Location) time.Time {
	if !q.On() {
		return time.Time{}
	}
	t := now.In(loc)
	m := t.Hour()*60 + t.Minute()
	endOn := func(days int) time.Time {
		return time.Date(t.Year(), t.Month(), t.Day()+days, q.end/60, q.end%60, 0, 0, loc)
	}
	switch {
	case q.start < q.end && m >= q.start && m < q.end:
		return endOn(0)
	case q.start > q.end && m >= q.start:
		return endOn(1)
	case q.start > q.end && m < q.end:
		return endOn(0)
	}
	return time.Time{}
}

// ParseTimezone loads an IANA zone name; "", "Local" or zoneName's "Local (…)" is the server's own zone.
func ParseTimezone(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "Local" || strings.HasPrefix(name, "Local (") {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown time zone %q (use an IANA name like America/Sao_Paulo)", name)
	}
	return loc, nil
}

// zoneName shows which zone "Local" really is (in a container without TZ that's UTC), so it's
// clear what quiet hours are measured in.
func zoneName(loc *time.Location) string {
	if loc != time.Local {
		return loc.String()
	}
	abbr, _ := time.Now().In(loc).Zone()
	return "Local (" + abbr + ")"
}

type outbox struct {
	wake      chan struct{}
	failed    atomic.Int64 // items that failed for good, for the alerter
	mu        sync.Mutex
	running   bool
	fails     int
	cooldown  time.Time
	lastErr   string
	lastErrAt time.Time
	ready     []readyMedia   // uploads kept for items, oldest first, at most maxReadyMedia
	uploading map[string]int // POST /queue/files in progress, by actor
}

func (b *Bot) wakeOutbox() {
	select {
	case b.outbox.wake <- struct{}{}:
	default:
	}
}

// Enqueued is what QueueMessages stored: Items in the request's order, Replayed of them already
// queued under the same idempotency key before. Rows skipped by dedupe or skip_sent have no item;
// Skipped counts them and SkippedRows lists the first maxSkippedRows.
type Enqueued struct {
	Batch       string
	Items       []store.OutboxItem
	Replayed    int
	Skipped     SkipCounts
	SkippedRows []SkippedRow
}

// Enqueue is QueueMessages without the replay count.
func (b *Bot) Enqueue(actor, tokenID, batch, key string, reqs []QueueRequest) ([]store.OutboxItem, error) {
	res, err := b.QueueMessages(actor, tokenID, batch, key, reqs)
	return res.Items, err
}

// QueueMessages validates and stores messages for the outbox worker. key makes the request safe to
// retry: the same key from the same actor returns the items already queued, and the same key for a
// different request is ErrKeyReused, with nothing stored.
func (b *Bot) QueueMessages(actor, tokenID, batch, key string, reqs []QueueRequest) (Enqueued, error) {
	return b.Queue(context.Background(), QueueOptions{Actor: actor, TokenID: tokenID, Batch: batch, Key: key}, reqs)
}

// sameRequest reports whether stored was queued by the request that would queue want. Items stored
// before send_at was kept (SendAt -1) are compared without it. A template's items compare the
// unrendered text (SourceHash), so a name changed since doesn't make a retry differ; so does an
// item whose text was edited (editHash). An edited due time or send_before isn't compared.
func sameRequest(stored, want store.OutboxItem, named bool) bool {
	sameText := stored.Text == want.Text
	switch {
	case want.SourceHash != "":
		sameText = stored.SourceHash == want.SourceHash
	case stored.SourceHash != "" && !stored.Edited.IsZero():
		sameText = stored.SourceHash == editHash(want.Text)
	case stored.SourceHash != "":
		sameText = false
	}
	return (!named || stored.Batch == want.Batch) && stored.To == want.To && sameText &&
		strings.TrimSpace(stored.ReplyTo) == want.ReplyTo && (stored.SendAt == -1 || stored.SendAt == want.RequestedSendAt()) &&
		stored.SpreadMS == want.SpreadMS && stored.FileID == want.FileID
}

// CheckBatch accepts 1-64 letters, digits, '.', '_' or '-'.
func CheckBatch(name string) error {
	if !batchName.MatchString(name) {
		return errBatchName
	}
	return nil
}

// CheckQueueRequest is Enqueue's check of one message, for callers that report errors their own way.
// A to of tag:NAME passes when the tag is well formed: the server expands it.
func CheckQueueRequest(r QueueRequest) error {
	switch {
	case len(strings.TrimSpace(r.To)) > maxQueueTo:
		return fmt.Errorf("to is longer than %d characters", maxQueueTo)
	case len(strings.TrimSpace(r.ReplyTo)) > maxQueueReplyTo:
		return fmt.Errorf("reply_to is longer than %d characters", maxQueueReplyTo)
	case r.File != "" && !ValidQueueFileID(r.File):
		return errors.New("file must be an id from POST /queue/files (f_ and 32 hex digits)")
	case strings.TrimSpace(r.Text) == "" && r.File == "":
		return errors.New("text is empty")
	case utf8.RuneCountInString(r.Text) > maxQueueText:
		return fmt.Errorf("text is longer than %d characters", maxQueueText)
	case !r.SendAt.Local && r.SendAt.After(time.Now().Add(maxQueueAhead)):
		return fmt.Errorf("send_at is more than %d days ahead", int(maxQueueAhead.Hours()/24))
	}
	if _, ok := QueueTag(r.To); ok {
		return checkQueueTag(r)
	}
	_, err := ParseRecipient(r.To)
	return err
}

// NotQueuedError is the answer for a cancel or edit of an item that is no longer queued. It
// unwraps to store.ErrNotFound.
type NotQueuedError struct {
	ID     int64
	Status string
	Edit   bool
}

func (e NotQueuedError) Error() string {
	if e.Edit {
		return fmt.Sprintf("only queued messages can be changed; #%d is %s", e.ID, e.Status)
	}
	return fmt.Sprintf("only queued messages can be canceled; #%d is %s", e.ID, e.Status)
}

func (e NotQueuedError) Unwrap() error { return store.ErrNotFound }

// CancelQueued cancels queued items: one by id (within f), or every queued one matching f.
func (b *Bot) CancelQueued(actor string, id int64, f store.OutboxFilter) (items []store.OutboxItem, err error) {
	target := f.Batch
	if id > 0 {
		target = fmt.Sprintf("#%d", id)
	}
	items, err = b.Store.CancelOutbox(id, f)
	if err == nil && len(items) == 0 && id > 0 {
		err = store.ErrNotFound
		if it, gerr := b.Store.OutboxItem(id); gerr == nil && (f.Actor == "" || it.Actor == f.Actor) && (f.Batch == "" || it.Batch == f.Batch) {
			err = NotQueuedError{ID: id, Status: it.Status}
		}
	}
	b.Audit(actor, "queue_cancel", target, fmt.Sprintf("%d canceled", len(items)), err)
	for _, it := range items {
		b.queueEvent(it)
	}
	return items, err
}

// QueueEdit changes a queued item. A zero field stays as it is; ClearBefore drops send_before.
type QueueEdit struct {
	SendAt      SendTime
	SendBefore  Deadline
	ClearBefore bool
	Text        *string
}

// Fields names what e changes, for the audit log (never the text itself).
func (e QueueEdit) Fields() []string {
	var out []string
	if !e.SendAt.IsZero() {
		out = append(out, "send_at")
	}
	if e.ClearBefore || !e.SendBefore.IsZero() {
		out = append(out, "send_before")
	}
	if e.Text != nil {
		out = append(out, "text")
	}
	return out
}

// EditQueued reschedules a queued item (within f), changes its send_before or replaces its text.
// A retry of the request that queued it still replays it (see sameRequest).
func (b *Bot) EditQueued(actor string, id int64, f store.OutboxFilter, e QueueEdit) (it store.OutboxItem, err error) {
	fields := e.Fields()
	defer func() { b.Audit(actor, "queue_edit", fmt.Sprintf("#%d", id), strings.Join(fields, ","), err) }()
	if len(fields) == 0 {
		return it, BadInput("nothing to change: give send_at, send_before or text")
	}
	cur, err := b.Store.OutboxItem(id)
	switch {
	case err != nil:
		return it, err
	case (f.Actor != "" && cur.Actor != f.Actor) || (f.Batch != "" && cur.Batch != f.Batch):
		return it, store.ErrNotFound
	case cur.Status != store.OutboxQueued:
		return it, NotQueuedError{ID: id, Status: cur.Status, Edit: true}
	}
	loc := b.currentSettings().location()
	if jid, err := b.localJID(cur.To); err == nil {
		loc = b.zones().of(jid)
	}
	var edit store.OutboxEdit
	now := time.Now()
	due := maxTime(cur.Due, now)
	if !e.SendAt.IsZero() {
		t := e.SendAt.Resolve(loc, now)
		if t.After(now.Add(maxQueueAhead)) {
			return it, BadInput("send_at is more than %d days ahead", int(maxQueueAhead.Hours()/24))
		}
		due = maxTime(t, now)
		edit.Due = &due
	}
	switch {
	case e.ClearBefore:
		edit.SendBefore = &time.Time{}
	case !e.SendBefore.IsZero():
		exp, err := e.SendBefore.expiry(due, loc, now)
		if err != nil {
			return it, BadInput("%v", err)
		}
		edit.SendBefore = &exp
	case edit.Due != nil && !cur.SendBefore.IsZero() && !cur.SendBefore.After(due):
		return it, BadInput("its send_before (%s) isn't after the new time: change or clear it too", cur.SendBefore.Format(time.RFC3339))
	}
	if e.Text != nil {
		switch {
		case strings.TrimSpace(*e.Text) == "" && cur.FileID == "":
			return it, BadInput("text is empty")
		case utf8.RuneCountInString(*e.Text) > maxQueueText:
			return it, BadInput("text is longer than %d characters", maxQueueText)
		}
		edit.Text, edit.OldText, edit.OldHash = e.Text, cur.Text, editHash(cur.Text)
	}
	it, err = b.Store.EditOutbox(id, f, edit)
	if errors.Is(err, store.ErrNotFound) {
		// Claimed, canceled or edited since it was read.
		status := "changed meanwhile"
		if now, gerr := b.Store.OutboxItem(id); gerr == nil && now.Status != store.OutboxQueued {
			status = now.Status
		}
		return it, NotQueuedError{ID: id, Status: status, Edit: true}
	}
	if err != nil {
		return it, err
	}
	b.queueEvent(it)
	b.wakeOutbox()
	return it, nil
}

func (b *Bot) QueueStats() QueueStats {
	c, err := b.Store.CountOutbox(store.OutboxFilter{})
	if err != nil {
		b.Log.Error("outbox: count", "err", err)
	}
	s := b.currentSettings()
	cooldown := b.queueCooldown()
	files := b.queueFileUsage()
	b.outbox.mu.Lock()
	defer b.outbox.mu.Unlock()
	return QueueStats{OutboxCounts: c, Paused: s.QueuePaused, Running: b.outbox.running, JitterMS: s.QueueJitter.Milliseconds(),
		QuietHours: s.QuietHours.String(), Timezone: zoneName(s.location()), QuietUntil: s.quietUntil(time.Now()), QuietOff: s.QuietOff,
		CooldownUntil: cooldown, LastError: b.outbox.lastErr, LastErrorAt: b.outbox.lastErrAt,
		Files: files.Files, FileBytes: files.Bytes, FilesQuota: s.QueueFilesQuota}
}

// queueCooldown is when the queue's back-off after repeated errors ends, or zero.
func (b *Bot) queueCooldown() time.Time {
	b.outbox.mu.Lock()
	defer b.outbox.mu.Unlock()
	if time.Now().Before(b.outbox.cooldown) {
		return b.outbox.cooldown
	}
	return time.Time{}
}

func (b *Bot) currentSettings() Settings {
	b.settingsMu.Lock()
	defer b.settingsMu.Unlock()
	return b.settings
}

func (b *Bot) queueEvent(it store.OutboxItem) {
	it.Text = ""
	b.publish(QueueEvent{Item: it})
	if b.Webhook != nil && (it.Status == store.OutboxSent || it.Status == store.OutboxFailed || it.Status == store.OutboxExpired) {
		if body, err := json.Marshal(it); err == nil {
			b.Webhook.enqueue(webhookJob{event: "queue", body: body, id: fmt.Sprintf("q%d-%s-%s", it.ID, it.Progress(), randomHex(4))})
		}
	}
}

// outboxReceipt records a delivery or read receipt for queued messages. It runs inline in the
// event handler: one indexed UPDATE is quick, and doing it in order keeps a read from landing
// before its delivery.
func (b *Bot) outboxReceipt(ids []types.MessageID, read bool, at time.Time) {
	if at.IsZero() {
		at = time.Now()
	}
	strs := make([]string, len(ids))
	for i, id := range ids {
		strs[i] = string(id)
	}
	items, err := b.Store.MarkOutboxReceipt(strs, read, at)
	if err != nil {
		b.Log.Error("outbox: receipt", "err", err)
		return
	}
	for _, it := range items {
		// Only sent items report receipts: a failed or retried one already had its final event.
		if it.Status == store.OutboxSent {
			b.queueEvent(it)
		}
	}
}

// RunOutbox sends queued messages until ctx is done. Only one should run per database.
func (b *Bot) RunOutbox(ctx context.Context) {
	if n, err := b.Store.RequeueSending(); err != nil {
		b.Log.Error("outbox: requeue", "err", err)
	} else if n > 0 {
		b.Log.Info("outbox: requeued items left sending", "count", n)
	}
	b.setOutboxRunning(true)
	defer b.setOutboxRunning(false)
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()
	b.pruneOutbox()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-prune.C:
			b.pruneOutbox()
			continue
		case <-b.outbox.wake:
		case <-timer.C:
		}
		timer.Reset(b.outboxStep(ctx))
	}
}

func (b *Bot) setOutboxRunning(on bool) {
	b.outbox.mu.Lock()
	b.outbox.running = on
	b.outbox.mu.Unlock()
}

func (b *Bot) pruneOutbox() {
	if n, err := b.Store.PruneOutbox(time.Now().Add(-outboxKeep)); err != nil {
		b.Log.Error("outbox: prune", "err", err)
	} else if n > 0 {
		b.Log.Info("outbox: pruned finished items", "count", n)
	}
	if n, err := b.Store.PruneContactCache(time.Now().Add(-ProfileCacheKeep)); err != nil {
		b.Log.Error("profile cache: prune", "err", err)
	} else if n > 0 {
		b.Log.Info("profile cache: pruned old profiles", "count", n)
	}
	b.pruneQueueFiles()
}

// outboxStep sends at most one message and returns how long to wait before the next step.
func (b *Bot) outboxStep(ctx context.Context) time.Duration {
	b.expireQueued()
	s := b.currentSettings()
	quietUntil := s.quietUntil(time.Now())
	switch {
	case s.QueuePaused:
		return outboxIdle
	case !quietUntil.IsZero():
		return min(max(time.Until(quietUntil), outboxMinStep), outboxIdle)
	case !b.Client.IsConnected() || !b.Client.IsLoggedIn():
		return outboxOffline
	}
	b.outbox.mu.Lock()
	cooldown := time.Until(b.outbox.cooldown)
	b.outbox.mu.Unlock()
	if cooldown > 0 {
		return cooldown
	}
	items, err := b.Store.DueOutbox(time.Now(), 50)
	if err != nil {
		b.Log.Error("outbox: read", "err", err)
		return outboxOffline
	}
	if len(items) == 0 {
		next, err := b.Store.NextOutboxDue()
		if err != nil || next.IsZero() {
			return outboxIdle
		}
		return min(max(time.Until(next), outboxMinStep), outboxIdle)
	}
	wait := outboxIdle
	tokens := map[string]*store.Token{}
	for _, it := range items {
		tok, err := b.queueToken(it.TokenID, tokens)
		if errors.Is(err, errTokenLookup) {
			b.Log.Error("outbox: token lookup", "err", err)
			return outboxOffline
		}
		if err != nil {
			b.finishQueued(it, store.OutboxResult{Status: store.OutboxFailed, Error: "the token that queued it: " + err.Error()})
			continue
		}
		jid, err := b.resolveOnline(ctx, it.To)
		limited, isLimited := errors.AsType[RateLimitError](err)
		switch {
		case isLimited:
			wait = min(wait, limited.RetryAfter)
			continue
		case err != nil && !b.Client.IsLoggedIn():
			return outboxOffline
		case err != nil:
			// Record the attempt without sending: a bad number fails, anything else is retried later.
			// Sending here would skip the slot and, for a file, upload while the item is claimed.
			b.failAttempt(ctx, it, err)
			return jitter(s.QueueJitter)
		}
		chat := b.PhoneJID(jid).String()
		if err := b.optedOut(jid, it.To); err != nil {
			if _, isInput := errors.AsType[InputError](err); !isInput {
				b.Log.Error("outbox: opt-out check", "err", err)
				return outboxOffline
			}
			b.finishQueued(it, store.OutboxResult{Status: store.OutboxFailed, Chat: chat, Error: err.Error()})
			continue
		}
		media, d, err := b.queuedFile(ctx, it, tok, chat)
		switch {
		case err != nil:
			b.queuedFileFailed(ctx, it, err)
			return jitter(s.QueueJitter)
		case d == redoStep:
			return outboxMinStep
		case d > 0:
			wait = min(wait, d)
			continue
		}
		d, ok := b.outboxSlot(tok, chat)
		if !ok {
			wait = min(wait, d)
			continue
		}
		if SleepUntil(ctx, time.Now().Add(d)) != nil {
			return outboxIdle
		}
		b.sendQueuedWith(ctx, it, media)
		return jitter(s.QueueJitter)
	}
	return max(wait, outboxMinStep)
}

// expireQueued ends the queued items whose send_before has passed, on every step.
func (b *Bot) expireQueued() {
	items, err := b.Store.ExpireOutbox(time.Now())
	if err != nil {
		b.Log.Error("outbox: expire", "err", err)
		return
	}
	if len(items) > 0 {
		b.Log.Info("outbox: expired items past their send_before", "count", len(items))
	}
	for _, it := range items {
		b.queueEvent(it)
	}
}

// queueToken looks up the token an item was queued with (nil for the owner or the CLI), once per
// step, and says why it can't be used any more.
func (b *Bot) queueToken(id string, seen map[string]*store.Token) (*store.Token, error) {
	if id == "" {
		return nil, nil
	}
	t, ok := seen[id]
	if !ok {
		found, err := b.Store.TokenByID(id)
		if errors.Is(err, store.ErrNotFound) {
			return nil, errors.New("no longer exists")
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errTokenLookup, err)
		}
		t, seen[id] = &found, &found
	}
	return t, t.Usable(time.Now())
}

// outboxSlot takes a send slot for chat if one is free now under both the token's own rate and the
// global limiter; checking both first keeps one from being taken while the other is busy.
func (b *Bot) outboxSlot(tok *store.Token, chat string) (time.Duration, bool) {
	own, wait := b.outboxPeek(tok, chat)
	if wait > outboxSlotWithin {
		return wait, false
	}
	if own != nil {
		if d, ok := own.TryReserve(tok.ID, outboxSlotWithin); !ok {
			return d, false
		}
	}
	return b.Limiter.TryReserve(chat, outboxSlotWithin)
}

// outboxPeek is how long until both the token's own rate and the global limiter have a slot for
// chat, without taking one, and the token's limiter (nil without a rate).
func (b *Bot) outboxPeek(tok *store.Token, chat string) (*Limiter, time.Duration) {
	var own *Limiter
	if tok != nil && tok.Rate > 0 {
		own = b.tokenLimiter(tok.ID, tok.Rate)
	}
	wait := b.Limiter.Peek(chat)
	if own != nil {
		wait = max(wait, own.Peek(tok.ID))
	}
	return own, wait
}

func (b *Bot) sendQueued(ctx context.Context, it store.OutboxItem) { b.sendQueuedWith(ctx, it, nil) }

// sendQueuedWith claims and sends an item; media is a file item's upload when it was made before
// the slot was taken (nil uploads it here).
func (b *Bot) sendQueuedWith(ctx context.Context, it store.OutboxItem, media *queueMedia) {
	it, err := b.Store.ClaimOutbox(it.ID, string(b.Client.GenerateMessageID()))
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			b.Log.Error("outbox: claim", "id", it.ID, "err", err)
		}
		return
	}
	b.queueEvent(it)
	m, err := b.sendQueuedItem(ctx, it, media)
	if err == nil {
		b.outboxResult(true, "")
		b.finishQueued(it, store.OutboxResult{Status: store.OutboxSent, Chat: m.Chat})
		return
	}
	if ctx.Err() != nil {
		return // shutting down: it stays in sending and is requeued on the next start
	}
	b.finishQueued(it, b.attemptResult(it, err))
}

// errTokenLookup is queueToken's answer when the database couldn't say: try again later, never
// treat the token as gone.
var errTokenLookup = errors.New("token lookup")

// failAttempt claims an item and records err as a failed attempt, without sending.
func (b *Bot) failAttempt(ctx context.Context, it store.OutboxItem, err error) {
	if ctx.Err() != nil {
		return
	}
	claimed, cerr := b.Store.ClaimOutbox(it.ID, string(b.Client.GenerateMessageID()))
	if cerr != nil {
		if !errors.Is(cerr, store.ErrNotFound) {
			b.Log.Error("outbox: claim", "id", it.ID, "err", cerr)
		}
		return
	}
	b.queueEvent(claimed)
	b.finishQueued(claimed, b.attemptResult(claimed, err))
}

// attemptResult turns a failed attempt's error into what happens to the item next.
func (b *Bot) attemptResult(it store.OutboxItem, err error) store.OutboxResult {
	r := store.OutboxResult{Status: store.OutboxQueued, Error: err.Error()}
	_, isInput := errors.AsType[InputError](err)
	limited, isLimited := errors.AsType[RateLimitError](err)
	switch {
	case isInput:
		r.Status = store.OutboxFailed
	case isLimited:
		r.Due, r.Refund = time.Now().Add(limited.RetryAfter), true
	case errors.Is(err, errOptOutCheck):
		b.Log.Error("outbox: opt-out check", "id", it.ID, "err", err)
		r.Due, r.Refund = time.Now().Add(outboxOffline), true
	case !b.Client.IsLoggedIn() || !b.Client.IsConnected():
		r.Refund = true
	default:
		b.outboxResult(false, err.Error())
		if it.Attempts >= outboxAttempts {
			r.Status = store.OutboxFailed
		} else {
			r.Due = time.Now().Add(outboxBackoff(it.Attempts))
		}
	}
	return r
}

func (b *Bot) finishQueued(it store.OutboxItem, r store.OutboxResult) {
	done, err := b.Store.FinishOutbox(it.ID, r)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return // canceled meanwhile
	case err != nil:
		b.Log.Error("outbox: update", "id", it.ID, "err", err)
		return
	case r.Status == store.OutboxFailed:
		b.Log.Warn("outbox: gave up", "id", it.ID, "batch", it.Batch, "err", r.Error)
		if r.Error != errOptedOut {
			b.outbox.failed.Add(1)
		}
	}
	b.queueEvent(done)
}

// outboxResult tracks consecutive WhatsApp errors. From the second in a row the whole queue backs off
// (30 s, 1 min, 2 min … up to 10 min), since a run of failures usually means WhatsApp is pushing back.
func (b *Bot) outboxResult(ok bool, errText string) {
	b.outbox.mu.Lock()
	defer b.outbox.mu.Unlock()
	if ok {
		b.outbox.fails, b.outbox.cooldown = 0, time.Time{}
		return
	}
	b.outbox.fails++
	b.outbox.lastErr, b.outbox.lastErrAt = errText, time.Now()
	if b.outbox.fails >= 2 {
		d := min(15*time.Second<<min(b.outbox.fails-1, 10), maxCooldown)
		b.outbox.cooldown = time.Now().Add(d)
		b.Log.Warn("outbox: backing off after repeated errors", "errors", b.outbox.fails, "for", d)
	}
}

func outboxBackoff(attempts int) time.Duration {
	return min(30*time.Second<<min(max(attempts-1, 0), 10), maxOutboxBackoff)
}

func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return mrand.N(d)
}
