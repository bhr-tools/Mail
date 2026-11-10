package api

import (
	"strconv"
	"strings"
	"time"

	"github.com/bhr-tools/mail/internal/config"
	"github.com/bhr-tools/mail/internal/db"
)

// Query is a parsed search.
type Query struct {
	db.Filter
	// Text is the words looked for, without the filters.
	Text string
	all  bool
}

// ParseQuery reads a search: words, "quoted phrases" and filters
// (from:, to:, in:, account:, is:unread, is:starred, has:attachment,
// newer:7d|4w|6m|1y, after:2026-01-31).
// Everything but the trash and spam is searched unless in: says otherwise; in: also
// takes the name of a folder made by the person (in:Receipts).
func ParseQuery(s string) Query {
	return parseQuery(s, time.Now())
}

func parseQuery(s string, now time.Time) Query {
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
					q.Roles, q.ExcludeRoles = nil, nil
					q.all = true
				case "done", "archived":
					q.Roles = []string{config.Archive}
				case "spam":
					q.Roles = []string{config.Junk}
				default:
					if isRole(v) {
						q.Roles = []string{v}
					} else {
						q.Roles = []string{config.CustomRole(value)}
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
			case "newer":
				if t, ok := since(value, now); ok {
					q.After = t
					continue
				}
			case "after":
				if t, err := time.ParseInLocation("2006-01-02", value, now.Location()); err == nil {
					q.After = t
					continue
				}
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
	if q.Roles == nil && !q.all {
		q.ExcludeRoles = []string{config.Trash, config.Junk}
	}
	q.Match = strings.Join(terms, " ")
	q.Text = strings.Join(words, " ")
	return q
}

// since reads a span such as 7d, 4w, 6m or 1y back from now.
func since(v string, now time.Time) (time.Time, bool) {
	if len(v) < 2 {
		return time.Time{}, false
	}
	n, err := strconv.Atoi(v[:len(v)-1])
	if err != nil || n <= 0 {
		return time.Time{}, false
	}
	switch strings.ToLower(v[len(v)-1:]) {
	case "d":
		return now.AddDate(0, 0, -n), true
	case "w":
		return now.AddDate(0, 0, -7*n), true
	case "m":
		return now.AddDate(0, -n, 0), true
	case "y":
		return now.AddDate(-n, 0, 0), true
	}
	return time.Time{}, false
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
