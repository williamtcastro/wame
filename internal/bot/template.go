package bot

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"go.mau.fi/whatsmeow/types"
)

// maxTemplateName is the most runes a name put into a template may have.
const maxTemplateName = 60

// Template placeholders: {name}, {first_name} and {push_name}, each with an optional |fallback.
const (
	fieldName      = "name"
	fieldFirstName = "first_name"
	fieldPushName  = "push_name"
)

const errTemplateHelp = "use {name}, {first_name}, {push_name}, {name|fallback}, and {{ or }} for a brace"

var placeholderLike = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,23}$`)

// templatePart is literal text, or (with field set) a placeholder.
type templatePart struct {
	text, field, fallback string
	hasFallback           bool
}

// parseTemplate splits a {name} template into parts: {{ and }} are literal braces, anything else
// in braces must be a known placeholder.
func parseTemplate(text string) ([]templatePart, error) {
	var parts []templatePart
	var lit strings.Builder
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c == '{' && strings.HasPrefix(text[i:], "{{"), c == '}' && strings.HasPrefix(text[i:], "}}"):
			lit.WriteByte(c)
			i++
		case c == '}':
			return nil, fmt.Errorf("a lone } in the template (%s)", errTemplateHelp)
		case c == '{':
			end := strings.IndexAny(text[i+1:], "{}")
			if end < 0 || text[i+1+end] != '}' {
				return nil, fmt.Errorf("a { without its } in the template (%s)", errTemplateHelp)
			}
			body := text[i+1 : i+1+end]
			field, fallback, hasFallback := strings.Cut(body, "|")
			field = strings.TrimSpace(field)
			if field != fieldName && field != fieldFirstName && field != fieldPushName {
				// Errors are audited and logged: only quote what looks like a placeholder's name,
				// never other text of the message.
				if !placeholderLike.MatchString(field) {
					return nil, fmt.Errorf("unknown placeholder in the template (%s)", errTemplateHelp)
				}
				return nil, fmt.Errorf("unknown placeholder {%s} (%s)", field, errTemplateHelp)
			}
			if lit.Len() > 0 {
				parts = append(parts, templatePart{text: lit.String()})
				lit.Reset()
			}
			parts = append(parts, templatePart{field: field, fallback: fallback, hasFallback: hasFallback})
			i += end + 1
		default:
			lit.WriteByte(c)
		}
	}
	if lit.Len() > 0 {
		parts = append(parts, templatePart{text: lit.String()})
	}
	return parts, nil
}

// CheckTemplate reports a template's syntax errors, for callers that check before queuing.
func CheckTemplate(text string) error {
	_, err := parseTemplate(text)
	return err
}

// hasPlaceholders reports whether rendering needs names.
func hasPlaceholders(parts []templatePart) bool {
	for _, p := range parts {
		if p.field != "" {
			return true
		}
	}
	return false
}

// templateNames are the names a template may use for one recipient, already cleaned.
type templateNames struct{ name, push string }

// errNoName is renderTemplate's answer when a placeholder has neither a name nor a fallback.
var errNoName = errors.New("no name")

// renderTemplate fills the placeholders of parts with names.
func renderTemplate(parts []templatePart, names templateNames) (string, error) {
	var out strings.Builder
	for _, p := range parts {
		if p.field == "" {
			out.WriteString(p.text)
			continue
		}
		v := names.name
		switch p.field {
		case fieldFirstName:
			if f := strings.Fields(v); len(f) > 0 {
				v = f[0]
			}
		case fieldPushName:
			v = names.push
		}
		switch {
		case v != "":
			out.WriteString(v)
		case p.hasFallback:
			out.WriteString(p.fallback)
		default:
			return "", errNoName
		}
	}
	return out.String(), nil
}

// templateNameOf cleans a name for a template: one line, no invisible runes, at most
// maxTemplateName runes.
func templateNameOf(s string) string {
	s = cleanName(s)
	if r := []rune(s); len(r) > maxTemplateName {
		s = strings.TrimSpace(string(r[:maxTemplateName]))
	}
	return s
}

// namesForTemplate looks up a recipient's names without asking WhatsApp. {name} is given (a name
// column), else your own name, the address book's, the business name or the cached verified name;
// never the push name, which a stranger chose: that's {push_name}, asked for by name. Groups have
// no name here (their subject is set by any member).
func (b *Bot) namesForTemplate(ctx context.Context, jid types.JID, given string) templateNames {
	n := templateNames{name: templateNameOf(given)}
	if !isPersonChat(jid) {
		return n
	}
	pn := b.PhoneJID(jid)
	book := b.bookEntry(ctx, pn)
	n.push = templateNameOf(book.PushName)
	if n.name == "" {
		n.name = cmp.Or(templateNameOf(b.ownName(jid.String(), pn)), templateNameOf(book.FullName),
			templateNameOf(book.FirstName), templateNameOf(book.BusinessName))
	}
	if n.name == "" {
		if c, err := b.Store.CachedProfileOf(b.contactKeys(pn)...); err == nil {
			n.name = templateNameOf(c.VerifiedName)
		}
	}
	return n
}

// sourceHash identifies what a row asked for before wame filled anything in: its tag (for a
// tag's member), its unrendered text and its own name. A replay compares it instead of the text,
// so it still matches after a contact was renamed, while a different tag or name column is a
// different request.
// editHash is what an edited item keeps of its original text (see sameRequest).
func editHash(text string) string { return sourceHash("\x00edited", text, "") }

func sourceHash(tag, text, name string) string {
	sum := sha256.Sum256([]byte(tag + "\x00" + text + "\x00" + strings.TrimSpace(name)))
	return hex.EncodeToString(sum[:])
}
