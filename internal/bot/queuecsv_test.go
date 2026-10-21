package bot

import (
	"bytes"
	"encoding/csv"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestReadQueueCSV(t *testing.T) {
	in := "\xef\xbb\xbf Send_At ,TO,text,reply_to\r\n" +
		"2026-10-13T09:00:00-03:00,5511999999999,\"Hi Maria, see you\",\r\n" +
		"\r\n" +
		",,,\r\n" +
		", 5511988887777 ,\"Hi John\nline two\",3EB0C4\r\n" +
		",552187654321,,\r\n"
	rows, err := ReadQueueCSV(strings.NewReader(in), 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []QueueRequest{
		{To: "5511999999999", Text: "Hi Maria, see you", SendAt: At(time.Date(2026, 10, 13, 9, 0, 0, 0, time.FixedZone("", -3*3600))), Line: 2},
		{To: "5511988887777", Text: "Hi John\nline two", ReplyTo: "3EB0C4", Line: 5},
		{To: "552187654321", Line: 7},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %+v", rows)
	}
	for i, w := range want {
		g := rows[i]
		if g.To != w.To || g.Text != w.Text || g.ReplyTo != w.ReplyTo || !g.SendAt.Equal(w.SendAt.Time) || g.Line != w.Line {
			t.Errorf("row %d = %+v, want %+v", i, g, w)
		}
	}

	loose := "to,text,,\n5511999999999,Hi Maria,,\n  \t \n5511988887777\n+55 21 98765-4321,Hi Ana\n"
	rows, err = ReadQueueCSV(strings.NewReader(loose), 0)
	if err != nil || len(rows) != 3 || rows[1].To != "5511988887777" || rows[1].Text != "" || rows[2].To != "+55 21 98765-4321" {
		t.Fatalf("short rows, blank lines, empty trailing columns: %+v %v", rows, err)
	}
	semi := "to;text;send_at\r\n5511999999999;\"Olá Maria, tudo bem?\";2026-10-13T09:00:00-03:00\r\n"
	if rows, err := ReadQueueCSV(strings.NewReader(semi), 0); err != nil || len(rows) != 1 || rows[0].Text != "Olá Maria, tudo bem?" || rows[0].SendAt.IsZero() {
		t.Fatalf("';' separated: %+v %v", rows, err)
	}

	only := "\xef\xbb\xbf\"to\"\n5511999999999\n"
	if rows, err := ReadQueueCSV(strings.NewReader(only), 0); err != nil || len(rows) != 1 || rows[0].Text != "" {
		t.Fatalf("to only: %+v %v", rows, err)
	}
}

func TestReadQueueCSVErrors(t *testing.T) {
	for _, c := range []struct {
		name, in, want string
		limit          int
	}{
		{"empty", "", "empty", 0},
		{"unknown columns", "to,text,nome,city\n", `line 1: unknown columns "nome", "city"`, 0},
		{"missing to", "text\nhello\n", `"to" column is required`, 0},
		{"duplicate column", "to,TO\n", `column "to" appears twice`, 0},
		{"bad time", "to,send_at\n5511999999999,tomorrow\n", "line 2: send_at", 0},
		{"value outside the columns", "to,text\n5511999999999,a\n5511988887777,Hi, John\n", "line 3: field 3 is outside the named columns", 0},
		{"value under an unnamed column", "to,,text\n5511999999999,x,a\n", "line 2: field 2 is outside", 0},
		{"decimal number", "to,text\n5511999999999.0,hi\n", "line 2: \"5511999999999.0\" is a number with decimals", 0},
		{"decimal comma", "to;text\n5511999999999,0;hi\n", "number with decimals", 0},
		{"mixed separators", "to;text,x\n", "export the file as comma-separated", 0},
		{"bare quote", "to,text\n5511999999999,a\"b\n", "line 2:", 0},
		{"scientific notation", "to,text\n5.511999E+12,hi\n", "line 2: \"5.511999E+12\" is a number in scientific notation", 0},
		{"over the limit", "to,text\n5511999999999,a\n5511988887777,b\n552187654321,c\n", "line 4: at most 2 messages", 2},
	} {
		_, err := ReadQueueCSV(strings.NewReader(c.in), c.limit)
		if _, input := errors.AsType[InputError](err); !input || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want bad input with %q", c.name, err, c.want)
		}
	}
	many := "to," + strings.Repeat("x,", 100) + "y\n"
	if _, err := ReadQueueCSV(strings.NewReader(many), 0); err == nil || strings.Count(err.Error(), `"x"`) > 5 {
		t.Errorf("unknown columns must be listed briefly: %v", err)
	}
}

func TestQueueRecordRoundTrip(t *testing.T) {
	in := []QueueRequest{
		{To: "5511999999999", Text: "Hi Maria, \"quoted\"\nnext line", SendAt: At(time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC))},
		{To: "5511988887777", Text: "Hi John", ReplyTo: "3EB0C4", Name: "John Smith"},
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write(QueueColumns)
	for _, r := range in {
		_ = w.Write(QueueRecord(r))
	}
	w.Flush()
	out, err := ReadQueueCSV(&buf, 0)
	if err != nil || len(out) != 2 || out[0].Text != in[0].Text || !out[0].SendAt.Equal(in[0].SendAt.Time) || out[1].ReplyTo != "3EB0C4" || out[1].Name != "John Smith" || out[1].Line != 4 {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestQueueRequestWhere(t *testing.T) {
	if got := (QueueRequest{}).Where(3); got != "messages[3]" {
		t.Error(got)
	}
	if got := (QueueRequest{Line: 7}).Where(3); got != "line 7" {
		t.Error(got)
	}
}
