package server

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/abjelosevic88/pocket-homelab-alerts/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// The three routes behind `capabilities` (links.go): /api/v1/pocket/search,
// /day/{date} and /widget. Each runs as the request's person and sees only
// their own alerts, exactly as the inbox does (an admin sees nobody else's
// either). They only read: nothing here marks an alert read or done, mutes
// it or sends it anywhere.

func alertPath(id int64) string { return "/alerts/" + strconv.FormatInt(id, 10) }

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// alertTitle is the line an alert is shown by: its title, else the first
// line of its text, else where it came from.
func alertTitle(a store.Alert) string {
	if t := strings.TrimSpace(a.Title); t != "" {
		return t
	}
	for _, l := range strings.Split(a.Body, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return a.SourceName
}

// priorityWords is an importance as a person reads it in a line.
func priorityWords(p int) string {
	switch n := priorityName(p); n {
	case "urgent", "normal":
		return n
	default:
		return n + " priority"
	}
}

// statusWord is where an alert stands: unread, read or done.
func statusWord(a store.Alert) string {
	switch {
	case a.DoneAt != nil:
		return "done"
	case a.ReadAt != nil:
		return "read"
	}
	return "unread"
}

// ago says how long ago a stored time was: "5 minutes ago", "3 days ago",
// then the date.
func ago(at string, now time.Time) string {
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute ago", "minutes ago")
	case d < 24*time.Hour:
		return plural(int(d/time.Hour), "hour ago", "hours ago")
	case d < 48*time.Hour:
		return "a day ago"
	case d < 30*24*time.Hour:
		return plural(int(d/(24*time.Hour)), "day ago", "days ago")
	}
	t = t.In(time.Local)
	if t.Year() == now.In(time.Local).Year() {
		return t.Format("2 Jan")
	}
	return t.Format("2 Jan 2006")
}

func joinParts(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " · ")
}

// ---------- search ----------

// searchWords are the words of a query that can find something: lowercase,
// and only those with a letter or a digit in them ("??" finds nothing).
func searchWords(q string) []string {
	var out []string
	for _, w := range strings.Fields(strings.ToLower(q)) {
		if strings.IndexFunc(w, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0 {
			out = append(out, w)
		}
	}
	return out
}

// pocketSearch finds the person's alerts by title, text and source (and
// tags, as the inbox search does): every word must be there. An alert with
// every word in its title comes first; otherwise the newest first.
func (s *Server) pocketSearch(r *http.Request, q string, limit int) ([]core.SearchItem, error) {
	words := searchWords(q)
	if len(words) == 0 {
		return nil, nil
	}
	list, err := s.store.SearchAlerts(r.Context(), actorID(r.Context()), words, min(limit*5, 250))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]core.SearchItem, 0, len(list))
	for _, a := range list {
		title := alertTitle(a)
		it := core.SearchItem{ID: strconv.FormatInt(a.ID, 10), Title: title, Kind: "alert", Date: a.LastSeenAt, URL: alertPath(a.ID), Score: 1,
			Snippet: joinParts(a.SourceName, priorityWords(a.Priority), statusWord(a), ago(a.LastSeenAt, now))}
		lower := strings.ToLower(title)
		inTitle := true
		for _, w := range words {
			inTitle = inTitle && strings.Contains(lower, w)
		}
		if inTitle {
			it.Score = 2
		}
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ---------- day ----------

// pocketDay is the person's day in Alerts: the alerts that came in that day
// (in the caller's time zone when it said one, else the box's), the most
// important first; the ones already dealt with are marked done.
func (s *Server) pocketDay(r *http.Request, date string, tz *time.Location) (*core.Day, error) {
	ctx := r.Context()
	loc := time.Local
	if tz != nil {
		loc = tz
	}
	d, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return nil, nil
	}
	// bounds without the zone letter, so "…:00Z" and "…:00.123Z" both sort after them
	const bound = "2006-01-02T15:04:05"
	list, err := s.store.AlertsReceived(ctx, actorID(ctx), d.UTC().Format(bound), d.AddDate(0, 0, 1).UTC().Format(bound), 500)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	day := &core.Day{Date: date}
	urgent, high := 0, 0
	for _, a := range list {
		switch {
		case a.Priority >= 5:
			urgent++
		case a.Priority == 4:
			high++
		}
		at := ""
		if t, err := time.Parse(time.RFC3339Nano, a.ReceivedAt); err == nil {
			at = t.In(loc).Format("15:04")
		}
		times := ""
		if a.Count > 1 {
			times = "seen " + strconv.Itoa(a.Count) + " times"
		}
		pr := ""
		if a.Priority != 3 {
			pr = priorityWords(a.Priority)
		}
		day.Items = append(day.Items, core.DayItem{ID: strconv.FormatInt(a.ID, 10), Time: at, Title: alertTitle(a), Sub: joinParts(a.SourceName, pr, times),
			Kind: "alert", Done: a.DoneAt != nil, URL: alertPath(a.ID)})
	}
	parts := []string{plural(len(list), "alert", "alerts")}
	if urgent > 0 {
		parts = append(parts, strconv.Itoa(urgent)+" urgent")
	}
	if high > 0 {
		parts = append(parts, strconv.Itoa(high)+" high")
	}
	day.Summary = strings.Join(parts, ", ")
	return day, nil
}

// ---------- widget ----------

// pocketWidget is the open alerts in a few lines: how many, how many are
// urgent, and the most urgent ones (the newest first within an importance).
// Nothing open, nothing to show.
func (s *Server) pocketWidget(r *http.Request) (*core.WidgetData, error) {
	ctx := r.Context()
	uid := actorID(ctx)
	open, urgent, err := s.store.CountOpen(ctx, uid)
	if err != nil || open == 0 {
		return nil, err
	}
	list, err := s.store.OpenAlerts(ctx, uid, 8)
	if err != nil {
		return nil, err
	}
	out := &core.WidgetData{URL: "/", Metrics: []core.Metric{{Label: "Open", Value: strconv.Itoa(open)}, {Label: "Urgent", Value: strconv.Itoa(urgent)}}}
	switch {
	case urgent == 0:
		out.Line = strconv.Itoa(open) + " open"
	case urgent == open:
		out.Line = strconv.Itoa(urgent) + " urgent"
	default:
		out.Line = strconv.Itoa(urgent) + " urgent, " + strconv.Itoa(open) + " open"
	}
	now := time.Now()
	for _, a := range list {
		out.Items = append(out.Items, core.WidgetItem{ID: strconv.FormatInt(a.ID, 10), Title: alertTitle(a), Detail: joinParts(a.SourceName, ago(a.LastSeenAt, now)), URL: alertPath(a.ID)})
	}
	return out, nil
}
