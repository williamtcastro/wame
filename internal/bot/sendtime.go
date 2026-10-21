package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/williamtcastro/wame/internal/store"
)

const (
	// localLayout is a date and time without an offset: a time on the recipient's clock.
	localLayout = "2006-01-02T15:04:05"
	// pastSlack is how far in the past a typed time may be: a few seconds spent typing it.
	pastSlack = time.Minute
	// maxExpireAfter is how long after its time a message may still go out with send_before.
	maxExpireAfter = 7 * 24 * time.Hour
	// MaxSpread is the longest window a request's rows can be spread over.
	MaxSpread = 24 * time.Hour
)

// SendTime is a send time as written: an instant, a Local date and time on the recipient's
// clock, or a Clock time of day that Resolve places on their calendar. The zero value is no time.
type SendTime struct {
	time.Time
	Local bool
	Clock bool
	Days  int
}

// At is an instant as a SendTime.
func At(t time.Time) SendTime { return SendTime{Time: t} }

// Resolve is the instant t means on loc's clock at now. A wall-clock time that doesn't exist (a
// DST gap) moves forward, like time.Date.
func (t SendTime) Resolve(loc *time.Location, now time.Time) time.Time {
	switch {
	case !t.Local || t.IsZero():
		return t.Time
	case t.Clock:
		n := now.In(loc)
		at := time.Date(n.Year(), n.Month(), n.Day()+t.Days, t.Hour(), t.Minute(), 0, 0, loc)
		if t.Days == 0 && !at.After(n) {
			at = time.Date(n.Year(), n.Month(), n.Day()+1, t.Hour(), t.Minute(), 0, 0, loc)
		}
		return at
	}
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, loc)
}

// wallMilli is a recipient-clock time as if UTC, in Unix ms: what a retry is compared with.
// 0 for an instant or no time.
func (t SendTime) wallMilli() int64 {
	if !t.Local || t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// String is t in the form ParseSendTime reads back: RFC 3339, or a date and time without offset.
func (t SendTime) String() string {
	switch {
	case t.IsZero():
		return ""
	case t.Local:
		return t.Format(localLayout)
	}
	return t.Format(time.RFC3339Nano)
}

func (t SendTime) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte(`""`), nil
	}
	return json.Marshal(t.String())
}

// UnmarshalJSON takes what ParseSendTime takes without relative forms; "" and null are no time.
func (t *SendTime) UnmarshalJSON(b []byte) error {
	var s *string
	if err := json.Unmarshal(b, &s); err != nil {
		return errors.New("must be a string like 2026-10-13T09:00:00-03:00")
	}
	if s == nil {
		*t = SendTime{}
		return nil
	}
	v, err := ParseSendTime(*s, time.Time{}, false)
	if err != nil {
		return err
	}
	*t = v
	return nil
}

const sendTimeHelp = "isn't a time like 2026-10-13T09:00:00-03:00, or 2026-10-13T09:00 on the recipient's clock"

// SendTimeHelp describes the relative forms too, for callers that take them.
const SendTimeHelp = "isn't a time like 2026-10-13T09:00:00-03:00, 2026-10-13T09:00 (the recipient's clock), a delay like 90m, 2h or 3d, or 09:00 / tomorrow 09:00"

// ParseSendTime reads RFC 3339 (an instant) or a date and time without offset (Local). With
// relative it also takes "90m", "09:00" and "tomorrow 09:00", for people typing. "" is no time.
func ParseSendTime(s string, now time.Time, relative bool) (SendTime, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return SendTime{}, nil
	}
	t, ok := parseFixedTime(s)
	if !ok && relative {
		t, ok = parseRelativeTime(s, now)
	}
	if !ok {
		if relative {
			return SendTime{}, fmt.Errorf("%.40q %s", s, SendTimeHelp)
		}
		return SendTime{}, fmt.Errorf("%.40q %s", s, sendTimeHelp)
	}
	if relative && !t.Local && t.Before(now.Add(-pastSlack)) {
		return SendTime{}, fmt.Errorf("%.40q is in the past", s)
	}
	return t, nil
}

// ParseSendAt reads a send_at time: RFC 3339 or a date and time without offset (see ParseSendTime).
func ParseSendAt(s string) (SendTime, error) { return ParseSendTime(s, time.Time{}, false) }

// CutSendTime splits a typed "WHEN TEXT" into its time and the rest. WHEN is one word (2h, 09:00,
// 2026-10-21T09:00) or "tomorrow HH:MM", read as ParseSendTime reads it with relative forms.
func CutSendTime(s string, now time.Time) (at SendTime, rest string, err error) {
	word, rest, _ := strings.Cut(strings.TrimSpace(s), " ")
	if strings.EqualFold(word, "tomorrow") {
		var clock string
		clock, rest, _ = strings.Cut(strings.TrimSpace(rest), " ")
		word += " " + clock
	}
	if word == "" {
		return at, "", errors.New("no time given, like 2h or tomorrow 09:00")
	}
	at, err = ParseSendTime(word, now, true)
	return at, strings.TrimSpace(rest), err
}

func parseFixedTime(s string) (SendTime, bool) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return SendTime{Time: t}, true
	}
	for _, layout := range []string{localLayout, "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if t, err := time.Parse(layout, s); err == nil {
			return SendTime{Time: t, Local: true}, true
		}
	}
	return SendTime{}, false
}

func parseRelativeTime(s string, now time.Time) (SendTime, bool) {
	if d, ok := parseDays(s); ok && d > 0 {
		return SendTime{Time: now.Add(d)}, true
	}
	clock, tomorrow := strings.CutPrefix(strings.ToLower(s), "tomorrow")
	clock = strings.TrimSpace(clock)
	if tomorrow && clock == "" {
		return SendTime{}, false
	}
	hm, err := time.Parse("15:04", clock)
	if err != nil {
		return SendTime{}, false
	}
	day, days := 0, 0
	switch {
	case tomorrow:
		day, days = 1, 1
	case hm.Hour()*60+hm.Minute() <= now.Hour()*60+now.Minute():
		day = 1 // that time has passed today
	}
	at := time.Date(now.Year(), now.Month(), now.Day()+day, hm.Hour(), hm.Minute(), 0, 0, time.UTC)
	return SendTime{Time: at, Local: true, Clock: true, Days: days}, true
}

// parseDays reads a Go duration, or whole days like "3d".
func parseDays(s string) (time.Duration, bool) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 {
			return 0, false
		}
		// Capped only so it can't overflow; the queue refuses anything past its own limit.
		return time.Duration(min(n, 1000)) * 24 * time.Hour, true
	}
	d, err := time.ParseDuration(s)
	return d, err == nil
}

// Deadline is a send_before: a time (fixed or on the recipient's clock), or After the message's
// own time. The zero value is no deadline.
type Deadline struct {
	SendTime
	After time.Duration
}

func (d Deadline) IsZero() bool { return d.SendTime.IsZero() && d.After == 0 }

// String is d in the form ParseDeadline reads back.
func (d Deadline) String() string {
	if d.After > 0 {
		return d.After.String()
	}
	return d.SendTime.String()
}

func (d Deadline) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// UnmarshalJSON takes what ParseDeadline takes; "" and null are no deadline.
func (d *Deadline) UnmarshalJSON(b []byte) error {
	var s *string
	if err := json.Unmarshal(b, &s); err != nil {
		return errors.New("must be a string like 2h or 2026-10-13T11:00:00-03:00")
	}
	if s == nil {
		*d = Deadline{}
		return nil
	}
	v, err := ParseDeadline(*s)
	if err != nil {
		return err
	}
	*d = v
	return nil
}

// ParseDeadline reads send_before: a duration after the message's time ("2h", "1d"), or a time
// as ParseSendAt reads it. "" is none.
func ParseDeadline(s string) (Deadline, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Deadline{}, nil
	}
	if d, ok := parseDays(strings.TrimPrefix(s, "+")); ok {
		if d <= 0 {
			return Deadline{}, errors.New("send_before must be after the message's time")
		}
		return Deadline{After: d}, nil
	}
	t, err := ParseSendAt(s)
	if err != nil {
		return Deadline{}, fmt.Errorf("%.40q isn't a duration like 2h or a time like 2026-10-13T11:00:00-03:00", s)
	}
	return Deadline{SendTime: t}, nil
}

// expiry is when an item due at due stops being worth sending, or zero. It isn't checked against
// now: the queue refuses only new items past their deadline.
func (d Deadline) expiry(due time.Time, loc *time.Location, now time.Time) (time.Time, error) {
	var at time.Time
	switch {
	case d.IsZero():
		return time.Time{}, nil
	case d.After > 0:
		at = due.Add(d.After)
	default:
		at = d.Resolve(loc, now)
	}
	switch {
	case !at.After(due):
		return time.Time{}, errors.New("send_before must be after send_at")
	case at.Sub(due) > maxExpireAfter:
		return time.Time{}, fmt.Errorf("send_before is at most %d days after send_at", int(maxExpireAfter.Hours()/24))
	}
	return at, nil
}

// ParseSpread reads spread: a duration of at most MaxSpread; "" is none.
func ParseSpread(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	d, ok := parseDays(s)
	switch {
	case !ok || d <= 0:
		return 0, fmt.Errorf("spread %.40q must be a duration like 30m or 2h", s)
	case d > MaxSpread:
		return 0, fmt.Errorf("spread is at most %s", MaxSpread)
	}
	return d, nil
}

// SpreadOffset is row i of n's place in a window of d: evenly from 0 to d.
func SpreadOffset(i, n int, d time.Duration) time.Duration {
	if n < 2 || d <= 0 {
		return 0
	}
	return time.Duration(int64(d) / int64(n-1) * int64(i))
}

// QueueLocation is the zone of queue_timezone: where a typed "09:00" falls when no recipient's
// zone applies, and the day "tomorrow" means.
func (b *Bot) QueueLocation() *time.Location { return b.currentSettings().location() }

// zones finds each recipient's time zone for times on their clock: their own contact's
// time_zone, else the queue's zone. Never a lookup; each chat is read once per request.
type zones struct {
	b        *Bot
	fallback *time.Location
	seen     map[string]*time.Location
}

func (b *Bot) zones() *zones {
	return &zones{b: b, fallback: b.currentSettings().location(), seen: map[string]*time.Location{}}
}

func (z *zones) of(jid types.JID) *time.Location {
	key := jid.String()
	if loc, ok := z.seen[key]; ok {
		return loc
	}
	loc := z.fallback
	if isPersonChat(jid) {
		if c, err := z.b.Store.Contact(z.b.contactKeys(jid)...); err == nil && c.TimeZone != "" {
			if l, err := ParseTimezone(c.TimeZone); err == nil {
				loc = l
			}
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			z.b.Log.Warn("queue: contact time zone", "err", err)
		}
	}
	z.seen[key] = loc
	return loc
}
