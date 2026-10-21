package bot

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/williamtcastro/wame/internal/store"
)

const (
	// MaxSkipSent is the longest skip_sent window. Without the read scope only the caller's queue
	// counts, and finished items are kept outboxKeep, so the window is that at most.
	MaxSkipSent = 90 * 24 * time.Hour
	// maxSkippedRows caps Enqueued.SkippedRows.
	maxSkippedRows = 100
	// maxNoNameLines is how many rows a "no name" error lists.
	maxNoNameLines = 5
)

// QueueOptions describe a request to QueueMessages beyond its messages.
type QueueOptions struct {
	Actor, TokenID, Batch, Key string
	// Template fills {name}, {first_name} and {push_name} in each text (see template.go).
	Template bool
	// Dedupe skips a row whose recipient and (rendered) text appeared earlier in the request.
	Dedupe bool
	// SkipSent ("48h", "3d", "batch:NAME") skips recipients who already got a message: see ParseSkipSent.
	SkipSent string
	// Admin counts every actor's queue items for SkipSent, not only Actor's.
	Admin bool
	// Read is the caller's read scope: needed to expand tag: rows and fill templates (both reveal
	// contact data), and it lets SkipSent count every message sent, not only Actor's queue.
	Read bool
	// Allow is a restricted caller's chat allowlist: tag members outside it are dropped unseen.
	Allow []string
	// Spread sets the rows' times evenly across this window from their send_at (or now): see ParseSpread.
	Spread time.Duration
}

// SkipCounts counts the rows a request skipped, by reason.
type SkipCounts struct {
	Duplicate    int `json:"duplicate"`
	RecentlySent int `json:"recently_sent"`
}

// Total is every skipped row.
func (c SkipCounts) Total() int { return c.Duplicate + c.RecentlySent }

// SkippedRow is one row a request skipped: its CSV line (0 for JSON), its index in the request,
// its recipient (a tag's member for an expanded row) and why.
type SkippedRow struct {
	Line   int    `json:"line,omitempty"`
	Index  int    `json:"index"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// SkipSentRule is a parsed skip_sent: a window back from now, or a batch.
type SkipSentRule struct {
	Within time.Duration
	Batch  string
}

// ParseSkipSent reads skip_sent: a duration like "48h", "90m" or "3d" (at most MaxSkipSent), or
// "batch:NAME".
func ParseSkipSent(s string) (SkipSentRule, error) {
	s = strings.TrimSpace(s)
	if name, ok := strings.CutPrefix(s, "batch:"); ok {
		if err := CheckBatch(name); err != nil {
			return SkipSentRule{}, fmt.Errorf("skip_sent: %w", err)
		}
		return SkipSentRule{Batch: name}, nil
	}
	d, ok := parseDays(s)
	switch {
	case !ok || d <= 0:
		return SkipSentRule{}, fmt.Errorf("skip_sent %.40q must be a duration like 48h or 3d, or batch:NAME", s)
	case d > MaxSkipSent:
		return SkipSentRule{}, fmt.Errorf("skip_sent is at most %d days", int(MaxSkipSent.Hours()/24))
	}
	return SkipSentRule{Within: d}, nil
}

// QueueTag returns NAME when to is tag:NAME.
func QueueTag(to string) (string, bool) {
	return strings.CutPrefix(strings.TrimSpace(to), "tag:")
}

func checkQueueTag(r QueueRequest) error {
	tag, _ := QueueTag(r.To)
	switch {
	case !tagPattern.MatchString(strings.ToLower(tag)):
		return fmt.Errorf("tag %.40q must be 1 to %d of a-z, 0-9, _ and -", tag, MaxContactTag)
	case strings.TrimSpace(r.Name) != "":
		return errors.New("a name can't be given for a tag: row (each member gets their own)")
	}
	return nil
}

// queueRow is one message after tags were expanded: the request's row, its index there, and for a
// tag's member the tag, the member's chat and its place among the members (from 1).
type queueRow struct {
	QueueRequest
	index   int
	tag     string
	member  string
	ordinal int
}

// where names the row in errors. A member is named by its place, not its number: errors are
// audited, and the audit log shouldn't list who has a tag.
func (r queueRow) where() string {
	if r.member != "" {
		return fmt.Sprintf("%s (member %d)", r.Where(r.index), r.ordinal)
	}
	return r.Where(r.index)
}

// Queue validates and stores messages for the outbox worker; QueueMessages is Queue without
// options. opt.Key makes the request safe to retry: the same key from the same actor returns the
// items already queued, and the same key for a different request is ErrKeyReused, with nothing
// stored. A row's key is KEY#i (i its index in reqs, even when other rows are skipped), or
// KEY#i:CHAT for a member of a tag: row, so a retry after the tag changed replays the members
// already queued and adds only the new ones.
func (b *Bot) Queue(ctx context.Context, opt QueueOptions, reqs []QueueRequest) (res Enqueued, err error) {
	batch := opt.Batch
	named := batch != ""
	if !named {
		batch = "b-" + randomHex(6)
	}
	n := len(reqs)
	defer func() { b.Audit(opt.Actor, "queue", batch, queueDetail(n, res.Skipped), err) }()
	switch {
	case len(reqs) == 0:
		return res, BadInput("no messages given")
	case len(reqs) > MaxQueueBatch:
		return res, BadInput("at most %d messages per request", MaxQueueBatch)
	case !batchName.MatchString(batch):
		return res, errBatchName
	case len(opt.Key) > MaxKeyLength:
		return res, BadInput("idempotency key is longer than %d characters", MaxKeyLength)
	case opt.Template && !opt.Read:
		return res, BadInput("templates need a token with the read scope")
	case opt.Spread < 0 || opt.Spread > MaxSpread:
		return res, BadInput("spread is at most %s", MaxSpread)
	}
	skip, err := opt.skipSent()
	if err != nil {
		return res, err
	}
	templates := make([][]templatePart, len(reqs))
	for i, r := range reqs {
		if err := CheckQueueRequest(r); err != nil {
			return res, BadInput("%s: %v", r.Where(i), err)
		}
		if _, isTag := QueueTag(r.To); isTag && !opt.Read {
			return res, BadInput("%s: tag: recipients need a token with the read scope", r.Where(i))
		}
		if opt.Template {
			if templates[i], err = parseTemplate(r.Text); err != nil {
				return res, BadInput("%s: %v", r.Where(i), err)
			}
		}
	}
	rows, err := b.expandTags(ctx, opt.Allow, reqs)
	if err != nil {
		return res, err
	}
	n = len(rows)
	if len(rows) > MaxQueueBatch {
		return res, BadInput("%d messages once tags are expanded; at most %d per request", len(rows), MaxQueueBatch)
	}
	items := make([]store.OutboxItem, len(rows))
	extras := make([]store.EnqueueRow, len(rows))
	// A row without a name is refused only when it would be queued now: a retry replays one queued
	// before the name was removed, and a skipped one sends nothing.
	noName := make([]bool, len(rows))
	zones, now := b.zones(), time.Now()
	for i, r := range rows {
		jid, err := b.localJID(r.To)
		if err != nil {
			return res, BadInput("%s: %v", r.where(), err)
		}
		items[i] = store.OutboxItem{Batch: batch, Actor: opt.Actor, TokenID: opt.TokenID, To: strings.TrimSpace(r.To), Text: r.Text,
			ReplyTo: strings.TrimSpace(r.ReplyTo), RecipientKey: jid.String()}
		if err := scheduleRow(&items[i], r.QueueRequest, zones.of(jid), now, opt.Spread, i, len(rows)); err != nil {
			return res, BadInput("%s: %v", r.where(), err)
		}
		extras[i].Recipients = b.contactKeys(jid)
		if opt.Template || r.tag != "" {
			items[i].SourceHash = sourceHash(r.tag, r.Text, r.Name)
		}
		if parts := templates[r.index]; opt.Template {
			var names templateNames
			if hasPlaceholders(parts) {
				names = b.namesForTemplate(ctx, jid, r.Name)
			}
			text, err := renderTemplate(parts, names)
			if errors.Is(err, errNoName) {
				noName[i] = true
			} else if err := CheckQueueRequest(QueueRequest{To: r.To, Text: text, ReplyTo: r.ReplyTo, File: r.File}); err != nil {
				return res, BadInput("%s: %v", r.where(), err)
			} else {
				items[i].Text = text
			}
		}
		if opt.Key != "" {
			items[i].Key, extras[i].AltKeys, extras[i].Taken = rowKeys(opt.Key, r, extras[i].Recipients)
		}
	}
	expanded := make([]QueueRequest, len(rows))
	for i, r := range rows {
		expanded[i] = r.QueueRequest
	}
	if err := b.attachQueueFiles(opt.Actor, expanded, func(i int) string { return rows[i].where() }, items); err != nil {
		return res, err
	}
	replays := 0
	stored, skipped, err := b.Store.EnqueueWith(items, store.EnqueueOptions{Rows: extras, SkipSent: skip, Dedupe: opt.Dedupe,
		Check: func(out []store.OutboxItem, replayed []bool, open int) error {
			added := 0
			var unnamed []string
			for i, it := range out {
				switch {
				case noName[i] && it.ID != 0 && !replayed[i]:
					unnamed = append(unnamed, rows[i].where())
				case replayed[i]:
					replays++
					want := items[i]
					if it.Key != want.Key {
						want.To = it.To // the same tag member, stored under another form of their number
					}
					if !sameRequest(it, want, named) {
						return ErrKeyReused
					}
				case it.ID != 0:
					added++
					if !it.SendBefore.IsZero() && !it.SendBefore.After(now) {
						return BadInput("%s: send_before has already passed", rows[i].where())
					}
				}
			}
			if len(unnamed) > 0 {
				return noNameError(unnamed)
			}
			if added > 0 && open > maxQueueOpen {
				return BadInput("%s already has %d messages waiting; at most %d may be", opt.Actor, open-added, maxQueueOpen)
			}
			return nil
		}})
	if errors.Is(err, store.ErrKeyTaken) {
		err = ErrKeyReused
	}
	if err != nil {
		return Enqueued{}, queueFileErr(err)
	}
	res = Enqueued{Batch: batch, Items: make([]store.OutboxItem, 0, len(stored)), Replayed: replays}
	for i, it := range stored {
		switch skipped[i] {
		case "":
			res.Items = append(res.Items, it)
			continue
		case store.SkipDuplicate:
			res.Skipped.Duplicate++
		case store.SkipRecentlySent:
			res.Skipped.RecentlySent++
		}
		if len(res.SkippedRows) < maxSkippedRows {
			res.SkippedRows = append(res.SkippedRows, SkippedRow{Line: rows[i].Line, Index: rows[i].index, To: items[i].To, Reason: skipped[i]})
		}
	}
	if !named && len(res.Items) > 0 {
		// A retry without a batch name made up a new one; its items are in the first run's.
		res.Batch, batch = res.Items[0].Batch, res.Items[0].Batch
	}
	n = len(res.Items)
	b.wakeOutbox()
	return res, nil
}

// scheduleRow sets a row's due time, spread offset and send_before; SendAt keeps what a retry
// is compared with (see store.OutboxItem.RequestedSendAt).
func scheduleRow(it *store.OutboxItem, r QueueRequest, loc *time.Location, now time.Time, spread time.Duration, i, n int) error {
	base := r.SendAt.Resolve(loc, now)
	if r.SendAt.Local && base.After(now.Add(maxQueueAhead)) {
		return fmt.Errorf("send_at is more than %d days ahead", int(maxQueueAhead.Hours()/24))
	}
	it.Due, it.SendAt = base, r.SendAt.wallMilli()
	if spread > 0 {
		it.SendAt = cmp.Or(it.SendAt, store.MilliOrZero(base))
		it.SpreadMS = spread.Milliseconds()
		it.Due = cmp.Or(base, now).Add(SpreadOffset(i, n, spread))
	}
	exp, err := r.SendBefore.expiry(cmp.Or(it.Due, now), loc, now)
	it.SendBefore = exp
	return err
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// noNameError lists the first rows a template had no name for.
func noNameError(where []string) error {
	more := ""
	if len(where) > maxNoNameLines {
		more = fmt.Sprintf(" and %d more", len(where)-maxNoNameLines)
		where = where[:maxNoNameLines]
	}
	return BadInput("no name for %s%s: add a name column, a fallback like {name|there}, or name them in contacts", strings.Join(where, ", "), more)
}

// rowKeys derives a row's idempotency key from the request's: KEY#i, or KEY#i:CHAT for a tag
// member. alts are the member's other forms (an @lid, the 9th-digit variant) an earlier run may have
// used. taken are the keys of the other kind of row at the same index: finding one means the row
// was changed from a number to a tag or back, which is a different request (ErrKeyReused), not new
// rows to add.
func rowKeys(key string, r queueRow, recipients []string) (own string, alts, taken []string) {
	base := fmt.Sprintf("%s#%d", key, r.index)
	if r.member == "" {
		return base, nil, []string{base + ":"}
	}
	for _, k := range recipients {
		if k != r.member {
			alts = append(alts, base+":"+k)
		}
	}
	return base + ":" + r.member, alts, []string{base}
}

// skipSent turns opt.SkipSent into the store's rule: nil when it's off.
func (opt QueueOptions) skipSent() (*store.SkipSent, error) {
	if strings.TrimSpace(opt.SkipSent) == "" {
		return nil, nil
	}
	rule, err := ParseSkipSent(opt.SkipSent)
	switch {
	case err != nil:
		return nil, BadInput("%v", err)
	case !opt.Read && rule.Within > outboxKeep:
		return nil, BadInput("skip_sent is at most %d days for a token without the read scope (only its own queue counts, and finished items are kept %d days)",
			int(outboxKeep.Hours()/24), int(outboxKeep.Hours()/24))
	}
	s := &store.SkipSent{Batch: rule.Batch, Actor: store.OutboxFilter{}.For(opt.Actor, opt.Admin).Actor, Messages: opt.Read}
	if rule.Batch == "" {
		s.Since = time.Now().Add(-rule.Within)
	}
	return s, nil
}

// expandTags replaces each tag:NAME row by one row per person with that tag (a snapshot: people
// tagged later aren't added). With an allowlist, people outside it are dropped without a trace, so
// a restricted caller can't learn how big the tag is.
func (b *Bot) expandTags(ctx context.Context, allow []string, reqs []QueueRequest) ([]queueRow, error) {
	rows := make([]queueRow, 0, len(reqs))
	expanded := map[string][]string{}
	for i, r := range reqs {
		tag, ok := QueueTag(r.To)
		if !ok {
			rows = append(rows, queueRow{QueueRequest: r, index: i})
			continue
		}
		tag = strings.ToLower(tag)
		members, ok := expanded[tag]
		if !ok {
			var err error
			if members, err = b.tagMembers(ctx, tag, allow); err != nil {
				return nil, err
			}
			expanded[tag] = members
		}
		if len(members) == 0 {
			if len(allow) > 0 {
				return nil, BadInput("%s: tag %s has no contacts this token can reach", r.Where(i), tag)
			}
			return nil, BadInput("%s: tag %s has no contacts", r.Where(i), tag)
		}
		for n, m := range members {
			row := queueRow{QueueRequest: r, index: i, tag: tag, member: m, ordinal: n + 1}
			row.To = m
			rows = append(rows, row)
		}
		if len(rows) > MaxQueueBatch {
			return rows, nil // refused by the caller; no need to read further
		}
	}
	return rows, nil
}

// tagMembers lists the chats of the people with tag, once per person (a row under an @lid or the
// other 9th-digit form is the same person), never asking WhatsApp.
func (b *Bot) tagMembers(ctx context.Context, tag string, allow []string) ([]string, error) {
	list, err := b.Store.Contacts(store.ContactFilter{Tags: []string{tag}})
	if err != nil {
		return nil, err
	}
	var allowed []string
	if len(allow) > 0 {
		allowed = b.ExpandAllowlist(ctx, allow)
	}
	seen := map[string]bool{}
	var out []string
	for _, c := range list {
		jid, err := b.localJID(c.JID)
		if err != nil || !isPersonChat(jid) || seen[jid.String()] {
			continue
		}
		for _, k := range b.contactKeys(jid) {
			seen[k] = true
		}
		if chat := jid.String(); len(allow) == 0 || slices.Contains(allowed, chat) {
			out = append(out, chat)
		}
	}
	return out, nil
}

// queueDetail is the audit detail of a queue request: counts only, never names, tags or text.
func queueDetail(n int, skipped SkipCounts) string {
	if s := skipped.Total(); s > 0 {
		return fmt.Sprintf("%d messages, %d skipped", n, s)
	}
	return fmt.Sprintf("%d messages", n)
}
