package bot

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
)

// sciNumber is how spreadsheets show long numbers they treat as numbers: 5.511999E+12; decNumber
// is how exports (pandas, a column of floats) write them: 5511999999999.0.
var (
	sciNumber = regexp.MustCompile(`^\+?\d+(?:[.,]\d+)?[eE]\+?\d+$`)
	decNumber = regexp.MustCompile(`^\+?\d+[.,]\d+$`)
)

// QueueColumns are the columns a queue CSV may have; the ones after reply_to are optional.
var QueueColumns = []string{"to", "text", "send_at", "reply_to", "name", "send_before"}

// csvTable describes a CSV with a header row: the columns it may name (the first two show up as the
// example in errors), other names accepted for some of them, the one it must name, the most
// records it may have (0 for no limit) and what a record is called in that error.
type csvTable struct {
	columns  []string
	aliases  map[string]string
	required string
	limit    int
	noun     string
}

// csvRow is one record: its line and the trimmed value of every column the header named.
type csvRow struct {
	line  int
	cells map[string]string
}

// readCSVTable reads a header row naming the columns (any order, any case) and the records under it.
// A leading BOM, CRLF line ends, quoted fields, a ';' separator (spreadsheets in locales with a
// decimal comma), short records and unnamed columns (a spreadsheet's empty trailing ones) are
// handled; fields are trimmed and blank records skipped. Errors name the line.
func readCSVTable(r io.Reader, t csvTable) ([]csvRow, error) {
	br := bufio.NewReader(r)
	if bom, err := br.Peek(3); err == nil && string(bom) == "\xef\xbb\xbf" {
		_, _ = br.Discard(3)
	}
	cr := csv.NewReader(br)
	cr.TrimLeadingSpace = true
	cr.ReuseRecord = true
	cr.FieldsPerRecord = -1
	head, _ := br.Peek(br.Size())
	if i := bytes.IndexByte(head, '\n'); i >= 0 {
		head = head[:i]
	}
	if !bytes.ContainsRune(head, ',') && bytes.ContainsRune(head, ';') {
		cr.Comma = ';'
	}
	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return nil, BadInput("the CSV is empty: the first line must name the columns, like %s", strings.Join(t.columns[:min(2, len(t.columns))], ","))
	}
	if err != nil {
		return nil, csvError(err)
	}
	hline, _ := cr.FieldPos(0)
	col := map[string]int{}
	named := map[int]string{}
	var unknown []string
	for i, h := range header {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if a, ok := t.aliases[h]; ok {
			h = a
		}
		if _, dup := col[h]; dup {
			return nil, BadInput("line %d: column %q appears twice", hline, h)
		}
		if !slices.Contains(t.columns, h) {
			if len(unknown) <= 5 {
				unknown = append(unknown, fmt.Sprintf("%.40q", h))
			}
			continue
		}
		col[h], named[i] = i, h
	}
	if len(unknown) > 0 {
		if len(unknown) > 5 {
			unknown[5] = "…"
		}
		hint := ""
		if slices.ContainsFunc(unknown, func(u string) bool { return strings.Contains(u, ";") }) {
			hint = "; export the file as comma-separated CSV"
		}
		return nil, BadInput("line %d: unknown columns %s (use %s)%s", hline, strings.Join(unknown, ", "), strings.Join(t.columns, ", "), hint)
	}
	if _, ok := col[t.required]; !ok {
		return nil, BadInput(`line %d: the %q column is required`, hline, t.required)
	}
	var rows []csvRow
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return rows, nil
		}
		if err != nil {
			return nil, csvError(err)
		}
		line, _ := cr.FieldPos(0)
		if !slices.ContainsFunc(rec, func(f string) bool { return strings.TrimSpace(f) != "" }) {
			continue
		}
		for i, f := range rec {
			if named[i] == "" && strings.TrimSpace(f) != "" {
				return nil, BadInput("line %d: field %d is outside the named columns (quote text that has a %q in it)", line, i+1, cr.Comma)
			}
		}
		if t.limit > 0 && len(rows) == t.limit {
			return nil, BadInput("line %d: at most %d %s per request", line, t.limit, t.noun)
		}
		row := csvRow{line: line, cells: make(map[string]string, len(col))}
		for name, i := range col {
			if i < len(rec) {
				row.cells[name] = strings.TrimSpace(rec[i])
			} else {
				row.cells[name] = ""
			}
		}
		rows = append(rows, row)
	}
}

// numberCell refuses what a spreadsheet does to a phone number it took for a number.
func numberCell(line int, s string) error {
	switch {
	case sciNumber.MatchString(s):
		return BadInput("line %d: %.40q is a number in scientific notation, which has lost digits; format the column as text in the spreadsheet", line, s)
	case decNumber.MatchString(s):
		return BadInput("line %d: %.40q is a number with decimals, not a phone number; format the column as text in the spreadsheet", line, s)
	}
	return nil
}

// ReadQueueCSV reads a queue CSV (see readCSVTable): to required; text, send_at, reply_to, name
// and send_before optional. With limit > 0, more records than that is an error.
func ReadQueueCSV(r io.Reader, limit int) ([]QueueRequest, error) {
	table, err := readCSVTable(r, csvTable{columns: QueueColumns, required: "to", limit: limit, noun: "messages"})
	if err != nil {
		return nil, err
	}
	rows := make([]QueueRequest, 0, len(table))
	for _, c := range table {
		row := QueueRequest{Line: c.line, To: c.cells["to"], Text: c.cells["text"], ReplyTo: c.cells["reply_to"], Name: c.cells["name"]}
		if err := numberCell(c.line, row.To); err != nil {
			return nil, err
		}
		if s := c.cells["send_at"]; s != "" {
			if row.SendAt, err = ParseSendAt(s); err != nil {
				return nil, BadInput("line %d: send_at %v", c.line, err)
			}
		}
		if s := c.cells["send_before"]; s != "" {
			if row.SendBefore, err = ParseDeadline(s); err != nil {
				return nil, BadInput("line %d: send_before %v", c.line, err)
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// csvError turns a parse error into bad input naming the line; read errors (like a body over its
// limit) pass through.
func csvError(err error) error {
	if pe, ok := errors.AsType[*csv.ParseError](err); ok {
		return BadInput("line %d: %v", pe.Line, pe.Err)
	}
	return err
}

// QueueRecord is r as a record under the QueueColumns header, in the form ReadQueueCSV reads.
func QueueRecord(r QueueRequest) []string {
	before := ""
	if !r.SendBefore.IsZero() {
		before = r.SendBefore.String()
	}
	return []string{r.To, r.Text, r.SendAt.String(), r.ReplyTo, r.Name, before}
}
