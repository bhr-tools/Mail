package api

import (
	"strings"

	"github.com/bhr-tools/mail/internal/config"
	"github.com/bhr-tools/mail/internal/db"
)

// Query is a parsed search.
type Query struct {
	db.Filter
	// Text is the words looked for, without the filters.
	Text string
}

// ParseQuery reads a search: words, "quoted phrases" and filters
// (from:, to:, in:, account:, is:unread, is:starred, has:attachment).
// Everything but the trash is searched unless in: says otherwise.
func ParseQuery(s string) Query {
	var q Query
	var words, terms []string
	for _, tok := range tokenize(s) {
		key, value, found := strings.Cut(tok, ":")
		if found && value != "" {
			switch strings.ToLower(key) {
			case "from":
				q.From = value
				continue
			case "to":
				q.To = value
				continue
			case "account":
				q.Account = value
				continue
			case "in":
				switch v := strings.ToLower(value); v {
				case "all", "anywhere":
					q.Roles = append([]string{}, config.Roles...)
				case "done", "archived":
					q.Roles = []string{config.Archive}
				default:
					if isRole(v) {
						q.Roles = []string{v}
					}
				}
				continue
			case "is":
				switch strings.ToLower(value) {
				case "unread":
					q.Unread = true
				case "starred", "flagged":
					q.Flagged = true
				}
				continue
			case "has":
				if strings.HasPrefix(strings.ToLower(value), "attachment") {
					q.Attachments = true
				}
				continue
			}
		}
		words = append(words, tok)
		// Each word matches as a prefix ("rapp" finds "rapport").
		term := `"` + strings.ReplaceAll(tok, `"`, `""`) + `"`
		if !strings.Contains(tok, " ") {
			term += "*"
		}
		terms = append(terms, term)
	}
	if q.Roles == nil {
		q.Roles = []string{config.Inbox, config.Archive, config.Sent, config.Drafts}
	}
	q.Match = strings.Join(terms, " ")
	q.Text = strings.Join(words, " ")
	return q
}

// tokenize splits on spaces, keeping "quoted phrases" whole.
func tokenize(s string) []string {
	var out []string
	var cur strings.Builder
	quoted := false
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			out = append(out, t)
		}
		cur.Reset()
	}
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
		case (r == ' ' || r == '\t') && !quoted:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}
