package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/williamtcastro/wame/internal/admin"
	"github.com/williamtcastro/wame/internal/bot"
	"github.com/williamtcastro/wame/internal/termui"
)

const (
	maxImportFile = 64 << 20
	maxChunkBytes = 15 << 20 // under the server's 16 MB body limit
)

// chunkMax is the rows per request: the server's limit (tests lower it).
var chunkMax = bot.MaxQueueBatch

// queueCommand is `wame queue import|status`: it talks to a running server over its HTTP API, never
// to the local database, since the server owns the WhatsApp session.
func queueCommand(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: wame queue import [flags] FILE.csv | wame queue status [-batch NAME]")
	}
	switch args[0] {
	case "import":
		return queueImport(ctx, args[1:], out)
	case "status":
		return queueStatus(ctx, args[1:], out)
	}
	return fmt.Errorf("unknown queue command %q (use import or status)", args[0])
}

// importChunk is one POST /queue: up to chunkMax rows and maxChunkBytes of CSV.
type importChunk struct {
	body        []byte
	rows        int
	first, last int
	lines       map[int]int // line in body → line in the file
}

func queueImport(ctx context.Context, args []string, out io.Writer) error {
	const use = "usage: wame queue import [-batch NAME] [-text DEFAULT] [-send-at TIME] [-send-before WHEN] [-spread DURATION] [-key KEY] [-template] [-dedupe] [-skip-sent DURATION|batch:NAME] [-file PATH|ID] [-dry-run] FILE.csv"
	file := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		file, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("queue import", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var sf serverFlags
	sf.register(fs)
	batch := fs.String("batch", "", "")
	text := fs.String("text", "", "")
	sendAt := fs.String("send-at", "", "")
	sendBefore := fs.String("send-before", "", "")
	spreadFlag := fs.String("spread", "", "")
	key := fs.String("key", "", "")
	dryRun := fs.Bool("dry-run", false, "")
	attach := fs.String("file", "", "")
	var opt admin.QueueCSVOptions
	fs.BoolVar(&opt.Template, "template", false, "")
	fs.BoolVar(&opt.Dedupe, "dedupe", false, "")
	fs.StringVar(&opt.SkipSent, "skip-sent", "", "")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\n%s", err, use)
	}
	if file == "" && fs.NArg() == 1 {
		file = fs.Arg(0)
	} else if fs.NArg() > 0 {
		return errors.New(use)
	}
	if file == "" {
		return errors.New(use)
	}
	at, err := bot.ParseSendAt(*sendAt)
	if err != nil {
		return fmt.Errorf("-send-at %w", err)
	}
	before, err := bot.ParseDeadline(*sendBefore)
	if err != nil {
		return fmt.Errorf("-send-before %w", err)
	}
	spread, err := bot.ParseSpread(*spreadFlag)
	if err != nil {
		return fmt.Errorf("-%w", err)
	}
	var skipRule bot.SkipSentRule
	if opt.SkipSent != "" {
		var err error
		if skipRule, err = bot.ParseSkipSent(opt.SkipSent); err != nil {
			return fmt.Errorf("-skip-sent: %w", err)
		}
	}

	// The attachment is checked before anything else touches the network: an id is used as given,
	// a path must be a regular file within the upload limit.
	att, err := checkAttachment(*attach)
	if err != nil {
		return err
	}
	data, err := readImportFile(file)
	if err != nil {
		return err
	}
	rows, err := bot.ReadQueueCSV(bytes.NewReader(data), 0)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	if len(rows) == 0 {
		return fmt.Errorf("%s: no messages, only the header", file)
	}
	for i := range rows {
		rows[i].Text, rows[i].SendAt = cmp.Or(rows[i].Text, *text), cmp.Or(rows[i].SendAt, at)
		rows[i].SendBefore = cmp.Or(rows[i].SendBefore, before)
		rows[i].File = att.checkID()
		if err := bot.CheckQueueRequest(rows[i]); err != nil {
			return fmt.Errorf("%s: %s: %w", file, rows[i].Where(i), err)
		}
		if opt.Template {
			if err := bot.CheckTemplate(rows[i].Text); err != nil {
				return fmt.Errorf("%s: %s: %w", file, rows[i].Where(i), err)
			}
		}
	}
	duplicates := 0
	if opt.Dedupe {
		rows, duplicates = dedupeRows(rows)
	}
	if spread > 0 {
		if err := spreadRows(rows, spread); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
	}
	base, err := importKey(*key, file, att.identity)
	if err != nil {
		return err
	}
	keySum := sha256.Sum256([]byte(base))
	*batch = cmp.Or(*batch, "csv-"+hex.EncodeToString(keySum[:6]))
	if err := bot.CheckBatch(*batch); err != nil {
		return err
	}
	chunks, err := chunkRows(rows)
	if err != nil {
		return err
	}

	if *dryRun {
		fmt.Fprintf(out, "%s: %d messages to %d recipients, batch %s, in %d request(s):\n", file, len(rows), recipients(rows), *batch, len(chunks))
		switch {
		case att.path != "":
			fmt.Fprintf(out, "  file %s (%s): would be uploaded once, then attached to every message\n", termui.SafeLine(att.name()), byteSize(att.size))
		case att.id != "":
			fmt.Fprintf(out, "  file %s: an existing upload, attached to every message\n", att.id)
		}
		for _, tag := range rowTags(rows) {
			fmt.Fprintf(out, "  tag:%s (expanded by the server)\n", tag)
		}
		if duplicates > 0 {
			fmt.Fprintf(out, "  %d duplicate rows left out\n", duplicates)
		}
		for i, c := range chunks {
			fmt.Fprintf(out, "  %d/%d: lines %d-%d, %d messages, %s, key %s\n", i+1, len(chunks), c.first, c.last, c.rows, byteSize(int64(len(c.body))), chunkKey(base, i))
		}
		fmt.Fprintln(out, "dry run: nothing sent")
		return nil
	}

	target, _, err := sf.resolve()
	if err != nil {
		return err
	}
	c, err := target.client()
	if err != nil {
		return err
	}
	opt.File = att.id
	if att.path != "" {
		if opt.File, err = uploadAttachment(ctx, c, att, out); err != nil {
			return err
		}
	}
	queued, replayed := 0, 0
	skipped := bot.SkipCounts{Duplicate: duplicates}
	for i, ch := range chunks {
		res, err := c.QueueCSV(ctx, ch.body, *batch, chunkKey(base, i), opt)
		if err != nil {
			fmt.Fprintf(out, "%d/%d: lines %d-%d: failed: %s\n", i+1, len(chunks), ch.first, ch.last, termui.SafeLine(ch.fileError(err).Error()))
			apiErr, ok := errors.AsType[*admin.APIError](err)
			switch {
			case ok && apiErr.Code == http.StatusConflict:
				return fmt.Errorf("stopped after %d of %d messages: lines %d-%d differ from what was queued under key %s before. "+
					"Rows that were already queued can't be changed (cancel them instead); for a new import, pass another -key", queued, len(rows), ch.first, ch.last, chunkKey(base, i))
			case ok && (apiErr.Code == http.StatusBadRequest || apiErr.Code == http.StatusForbidden):
				return fmt.Errorf("stopped after %d of %d messages. Fix the cause above (a row from line %d on: leave the lines before it as they are), then run: "+
					"wame queue import -key %s -batch %s%s %s (what was already queued isn't queued twice)", queued, len(rows), ch.first, base, *batch, optionFlags(opt), file)
			}
			return fmt.Errorf("stopped after %d of %d messages; run the same command again (with -key %s) to retry: what was already queued isn't queued twice", queued, len(rows), base)
		}
		queued += res.Queued - res.Replayed
		replayed += res.Replayed
		skipped.Duplicate += res.Skipped.Duplicate
		skipped.RecentlySent += res.Skipped.RecentlySent
		fmt.Fprintf(out, "%d/%d: lines %d-%d: queued %d%s%s\n", i+1, len(chunks), ch.first, ch.last, res.Queued-res.Replayed, alreadyQueued(res.Replayed), skippedCount(res.Skipped.Total()))
	}
	fmt.Fprintf(out, "queued %d messages%s%s, batch %s (follow it with: wame queue status -batch %s)\n", queued, alreadyQueued(replayed), skippedReport(skipped, skipRule), *batch, *batch)
	return nil
}

// attachment is -file: an id the server already has, or a local file to upload first.
type attachment struct {
	id       string
	path     string
	size     int64
	identity string // what the default key hashes: the id, or the sha256 of the file's bytes
}

func (a attachment) name() string { return filepath.Base(a.path) }

// checkID is the id the local row checks see; before the upload of a path there is none yet, and
// only its shape matters there.
func (a attachment) checkID() string {
	switch {
	case a.id != "":
		return a.id
	case a.path != "":
		return "f_" + strings.Repeat("0", 32)
	}
	return ""
}

// checkAttachment looks at -file locally: nothing here talks to the server.
func checkAttachment(arg string) (attachment, error) {
	if arg == "" {
		return attachment{}, nil
	}
	if bot.ValidQueueFileID(arg) {
		return attachment{id: arg, identity: arg}, nil
	}
	info, err := os.Stat(arg)
	switch {
	case err != nil:
		return attachment{}, fmt.Errorf("-file: %w", err)
	case !info.Mode().IsRegular():
		return attachment{}, fmt.Errorf("-file: %s is not a regular file", arg)
	case info.Size() == 0:
		return attachment{}, fmt.Errorf("-file: %s is empty", arg)
	case info.Size() > maxImportFile: // the server's own limit for an upload
		return attachment{}, fmt.Errorf("-file: %s is larger than %s", arg, byteSize(maxImportFile))
	}
	f, err := os.Open(arg)
	if err != nil {
		return attachment{}, fmt.Errorf("-file: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, maxImportFile+1)); err != nil {
		return attachment{}, fmt.Errorf("-file: %w", err)
	}
	return attachment{path: arg, size: info.Size(), identity: "sha256:" + hex.EncodeToString(h.Sum(nil))}, nil
}

// uploadAttachment sends the file once and returns its id. Running the import again is safe: the
// server answers 200 with the same id for the same bytes.
func uploadAttachment(ctx context.Context, c *admin.Client, a attachment, out io.Writer) (string, error) {
	f, err := os.Open(a.path)
	if err != nil {
		return "", fmt.Errorf("-file: %w", err)
	}
	defer f.Close()
	up, existed, err := c.UploadQueueFile(ctx, a.name(), io.LimitReader(f, maxImportFile+1))
	if err != nil {
		return "", fmt.Errorf("uploading %s: %s", termui.SafeLine(a.name()), termui.SafeLine(err.Error()))
	}
	switch {
	case !bot.ValidQueueFileID(up.ID):
		return "", fmt.Errorf("uploading %s: the server didn't answer with a file id", termui.SafeLine(a.name()))
	case "sha256:"+up.SHA256 != a.identity:
		return "", fmt.Errorf("-file: %s changed while it was being imported; run again once it's final", termui.SafeLine(a.name()))
	}
	if existed {
		fmt.Fprintf(out, "%s: already uploaded (%s)\n", termui.SafeLine(a.name()), up.ID)
	} else {
		fmt.Fprintf(out, "uploaded %s (%s, %s)\n", termui.SafeLine(a.name()), up.Kind, byteSize(up.Size))
	}
	return up.ID, nil
}

// dedupeRows drops rows whose recipient, text and name already appeared earlier in the file, as
// the server would (name matters for templates). It runs before the file is split into requests,
// so duplicates in different requests are caught and the split is the same on every run.
func dedupeRows(rows []bot.QueueRequest) ([]bot.QueueRequest, int) {
	seen := map[[3]string]bool{}
	out := rows[:0:0]
	for _, r := range rows {
		to := strings.TrimSpace(r.To)
		if tag, ok := bot.QueueTag(to); ok {
			to = "tag:" + strings.ToLower(tag)
		} else if jid, err := bot.ParseRecipient(to); err == nil {
			to = jid.String()
		}
		k := [3]string{to, r.Text, strings.TrimSpace(r.Name)}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, r)
	}
	return out, len(rows) - len(out)
}

// rowTags lists the tags rows send to, once each, in file order.
func rowTags(rows []bot.QueueRequest) []string {
	var tags []string
	for _, r := range rows {
		if tag, ok := bot.QueueTag(r.To); ok && !slices.Contains(tags, strings.ToLower(tag)) {
			tags = append(tags, strings.ToLower(tag))
		}
	}
	return tags
}

// optionFlags repeats the options that change what a request queues, for a rerun hint.
func optionFlags(opt admin.QueueCSVOptions) string {
	var s strings.Builder
	if opt.Template {
		s.WriteString(" -template")
	}
	if opt.Dedupe {
		s.WriteString(" -dedupe")
	}
	if opt.SkipSent != "" {
		s.WriteString(" -skip-sent " + opt.SkipSent)
	}
	if opt.File != "" {
		s.WriteString(" -file " + opt.File)
	}
	return s.String()
}

func skippedCount(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(", skipped %d", n)
}

// skippedReport is ", skipped 12 (8 sent in the last 48h, 4 duplicates)", or "" when nothing was.
func skippedReport(c bot.SkipCounts, rule bot.SkipSentRule) string {
	if c.Total() == 0 {
		return ""
	}
	var why []string
	if c.RecentlySent > 0 {
		if rule.Batch != "" {
			why = append(why, fmt.Sprintf("%d already in batch %s", c.RecentlySent, rule.Batch))
		} else {
			why = append(why, fmt.Sprintf("%d sent in the last %s", c.RecentlySent, shortDuration(rule.Within)))
		}
	}
	if c.Duplicate > 0 {
		why = append(why, fmt.Sprintf("%d duplicates", c.Duplicate))
	}
	return fmt.Sprintf(", skipped %d (%s)", c.Total(), strings.Join(why, ", "))
}

// shortDuration writes whole days as 3d and the rest like 48h or 90m.
func shortDuration(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0 && d >= 72*time.Hour:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	}
	return strings.TrimSuffix(strings.TrimSuffix(d.String(), "0s"), "0m")
}

func readImportFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxImportFile+1))
	if err == nil && len(data) > maxImportFile {
		err = fmt.Errorf("%s is larger than %s", path, byteSize(maxImportFile))
	}
	return data, err
}

// spreadRows spreads the rows' send_at over a window of d across the whole file, so a rerun gives
// the same times. Needs a send_at on every row; tag: rows are refused.
func spreadRows(rows []bot.QueueRequest, d time.Duration) error {
	for i, r := range rows {
		switch _, isTag := bot.QueueTag(r.To); {
		case isTag:
			return fmt.Errorf("%s: -spread can't spread a tag: row's members; queue the tag with POST /queue and spread, or schedule_message", r.Where(i))
		case r.SendAt.IsZero():
			return fmt.Errorf("%s: -spread needs a send_at on every row (or -send-at), so a rerun gives the same times", r.Where(i))
		}
	}
	for i := range rows {
		rows[i].SendAt.Time = rows[i].SendAt.Add(bot.SpreadOffset(i, len(rows), d))
		if err := bot.CheckQueueRequest(rows[i]); err != nil {
			return fmt.Errorf("%s: %w", rows[i].Where(i), err)
		}
	}
	return nil
}

func pick(record []string, keep []int) []string {
	out := make([]string, len(keep))
	for i, c := range keep {
		out[i] = record[c]
	}
	return out
}

// chunkRows splits rows into requests the server accepts, always at the same places for the same
// input, so each chunk keeps its idempotency key when the import is run again.
func chunkRows(rows []bot.QueueRequest) ([]importChunk, error) {
	var chunks []importChunk
	var buf, rec bytes.Buffer
	var cur importChunk
	lines := 0
	// The columns after reply_to (name, send_before) go out only when a row uses them, so older
	// servers still take the rest.
	keep := []int{0, 1, 2, 3}
	for c := len(keep); c < len(bot.QueueColumns); c++ {
		if slices.ContainsFunc(rows, func(r bot.QueueRequest) bool { return bot.QueueRecord(r)[c] != "" }) {
			keep = append(keep, c)
		}
	}
	columns := pick(bot.QueueColumns, keep)
	flush := func() {
		if cur.rows > 0 {
			cur.body = bytes.Clone(buf.Bytes())
			chunks = append(chunks, cur)
		}
		buf.Reset()
		cur = importChunk{}
	}
	for _, r := range rows {
		rec.Reset()
		if err := writeRecord(&rec, pick(bot.QueueRecord(r), keep)); err != nil {
			return nil, err
		}
		if cur.rows == chunkMax || (cur.rows > 0 && buf.Len()+rec.Len() > maxChunkBytes) {
			flush()
		}
		if cur.rows == 0 {
			_ = writeRecord(&buf, columns)
			cur.first, cur.lines, lines = r.Line, map[int]int{}, 1
		}
		cur.lines[lines+1] = r.Line
		lines += bytes.Count(rec.Bytes(), []byte("\n"))
		buf.Write(rec.Bytes())
		cur.rows++
		cur.last = r.Line
	}
	flush()
	return chunks, nil
}

func writeRecord(w io.Writer, rec []string) error {
	cw := csv.NewWriter(w)
	_ = cw.Write(rec)
	cw.Flush()
	return cw.Error()
}

var chunkLine = regexp.MustCompile(`^line (\d+): `)

// fileError rewrites a server error's "line N" (counted in the chunk it was sent) to the file's line.
func (c importChunk) fileError(err error) error {
	apiErr, ok := errors.AsType[*admin.APIError](err)
	if !ok {
		return err
	}
	m := chunkLine.FindStringSubmatch(apiErr.Msg)
	if m == nil {
		return err
	}
	n, _ := strconv.Atoi(m[1])
	if line, ok := c.lines[n]; ok {
		return &admin.APIError{Code: apiErr.Code, Msg: fmt.Sprintf("line %d: %s", line, apiErr.Msg[len(m[0]):])}
	}
	return err
}

func chunkKey(base string, i int) string { return fmt.Sprintf("%s.%d", base, i+1) }

// importKey is the base of the import's chunk keys: -key, or else one derived from the file's
// absolute path (and the file's identity under -file, so another file with the same CSV is a new
// import rather than a 409), so a rerun after fixing a row resumes the same import (see docs/QUEUE.md).
func importKey(key, file, attached string) (string, error) {
	if key != "" {
		if len(key) > bot.MaxKeyLength-8 {
			return "", fmt.Errorf("-key is longer than %d characters", bot.MaxKeyLength-8)
		}
		return key, nil
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		return "", err
	}
	if attached != "" {
		abs += "\x00file:" + attached
	}
	sum := sha256.Sum256([]byte(abs))
	return "csv-" + hex.EncodeToString(sum[:8]), nil
}

// recipients counts the distinct numbers rows name; tags are counted by the server.
func recipients(rows []bot.QueueRequest) int {
	seen := map[string]bool{}
	for _, r := range rows {
		if _, isTag := bot.QueueTag(r.To); isTag {
			continue
		}
		jid, _ := bot.ParseRecipient(r.To)
		seen[jid.String()] = true
	}
	return len(seen)
}

func queueStatus(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("queue status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var sf serverFlags
	sf.register(fs)
	batch := fs.String("batch", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return errors.New("usage: wame queue status [-batch NAME] [-server URL] [-profile NAME]")
	}
	target, _, err := sf.resolve()
	if err != nil {
		return err
	}
	c, err := target.client()
	if err != nil {
		return err
	}
	n, err := c.QueueCounts(ctx, *batch)
	if err != nil {
		return err
	}
	if *batch != "" {
		fmt.Fprintf(out, "batch %s: ", *batch)
	}
	fmt.Fprintf(out, "queued %d, sending %d, sent %d (delivered %d, read %d), failed %d, canceled %d, expired %d\n",
		n.Queued, n.Sending, n.Sent, n.Delivered, n.Read, n.Failed, n.Canceled, n.Expired)
	if !n.NextDue.IsZero() {
		fmt.Fprintf(out, "next due %s\n", n.NextDue.Local().Format("2006-01-02 15:04"))
	}
	return nil
}

func alreadyQueued(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" (already queued %d)", n)
}
