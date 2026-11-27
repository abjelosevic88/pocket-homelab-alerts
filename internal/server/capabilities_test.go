package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-alerts/internal/config"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// fakeCapCore is a Pocket Core that vouches for link tokens
// pcx_<alice|bob>_<search|day|widget|capture|read|write> from Pocket Hub
// and remembers the manifest Alerts registers with.
type fakeCapCore struct {
	*httptest.Server
	mu       sync.Mutex
	manifest map[string]any
}

func newCapApp(t *testing.T) (*app, *fakeCapCore) {
	t.Helper()
	fc := &fakeCapCore{}
	scopes := map[string]string{"search": core.ScopeSearch, "day": core.ScopeDay, "widget": core.ScopeWidget, "capture": core.ScopeCapture,
		"read": "alerts:read", "write": "alerts:write"}
	fc.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/apps/register":
			var m map[string]any
			_ = json.NewDecoder(r.Body).Decode(&m)
			fc.mu.Lock()
			fc.manifest = m
			fc.mu.Unlock()
			io.WriteString(w, `{}`)
		case "/api/v1/auth/introspect":
			var in struct{ Token string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			parts := strings.Split(in.Token, "_")
			if len(parts) != 3 || parts[0] != "pcx" || scopes[parts[2]] == "" {
				io.WriteString(w, `{"active":false}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "kind": "delegated", "audience": "alerts", "scope": scopes[parts[2]],
				"user": map[string]any{"id": 1, "sub": parts[1] + "@example.com", "email": parts[1] + "@example.com", "name": parts[1], "role": "member"},
				"app":  map[string]any{"id": 9, "slug": "hub", "name": "Pocket Hub"}})
		default:
			io.WriteString(w, `{}`)
		}
	}))
	t.Cleanup(fc.Close)
	return newApp(t, func(c *config.Config) { c.CoreURL, c.CoreRegistrationKey = fc.URL, "key" }), fc
}

// items are the rows of a capability answer.
func items(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	l, ok := m["items"].([]any)
	if !ok {
		t.Fatalf("no items list in %v", m)
	}
	out := []map[string]any{}
	for _, v := range l {
		out = append(out, v.(map[string]any))
	}
	return out
}

func titles(t *testing.T, m map[string]any) string {
	t.Helper()
	var out []string
	for _, it := range items(t, m) {
		out = append(out, it["title"].(string))
	}
	return strings.Join(out, " | ")
}

// raise sends the person an alert through the API and returns its id.
func (a *app) raise(auth string, body map[string]any) int64 {
	a.t.Helper()
	return int64(num(a.expect(201, auth, "POST", "/api/v1/alerts", body), "id"))
}

// at moves an alert to other moments (nothing in the API can).
func (a *app) at(id int64, received, lastSeen string) {
	a.t.Helper()
	if _, err := a.st.DB.Exec(`UPDATE alerts SET received_at = ?, last_seen_at = ? WHERE id = ?`, received, lastSeen, id); err != nil {
		a.t.Fatal(err)
	}
}

func agoTime(d time.Duration) string { return time.Now().Add(-d).UTC().Format(time.RFC3339Nano) }

// untouched fails when the alerts are no longer as they were: the routes only read.
func (a *app) untouched(auth string, want []map[string]any) {
	a.t.Helper()
	got := a.alerts(auth, "?view=all")
	if fmt.Sprint(got) != fmt.Sprint(want) {
		a.t.Fatalf("reading changed the alerts:\n%v\n%v", want, got)
	}
}

func TestPocketSearch(t *testing.T) {
	a := newApp(t, nil)
	src, _ := a.source(a.alice, map[string]any{"name": "Backups"})
	failed := a.raise(a.alice, map[string]any{"source_id": src, "title": "Backup failed", "body": "nas unreachable", "priority": 5})
	disk := a.raise(a.alice, map[string]any{"title": "Disk almost full", "body": "the backup volume is at 91%", "priority": 4})
	nightly := a.raise(a.alice, map[string]any{"source_id": src, "title": "Nightly backup done", "priority": 2})
	untitled := a.raise(a.alice, map[string]any{"body": "\n  Printer out of paper  \nTray 2", "priority": 3})
	a.raise(a.bob, map[string]any{"title": "Bob's backup failed", "priority": 5})
	a.at(failed, agoTime(3*time.Hour), agoTime(2*time.Hour+time.Minute))
	a.at(disk, agoTime(time.Hour), agoTime(time.Hour))
	a.at(nightly, agoTime(10*time.Minute), agoTime(10*time.Minute))
	a.ok(a.alice, "POST", "/api/v1/alerts/mark", map[string]any{"ids": []int64{disk}, "what": "read"})
	a.ok(a.alice, "POST", "/api/v1/alerts/mark", map[string]any{"ids": []int64{nightly}, "what": "done"})
	before := a.alerts(a.alice, "?view=all")

	// a title with the word comes before an alert that only says it in its text; else the newest first
	res := a.ok(a.alice, "GET", "/api/v1/pocket/search?q=backup", nil)
	if got := titles(t, res); got != "Nightly backup done | Backup failed | Disk almost full" {
		t.Fatalf("backup finds %q", got)
	}
	got := items(t, res)
	f := got[1]
	if f["id"] != fmt.Sprint(failed) || f["kind"] != "alert" || f["url"] != fmt.Sprintf("/alerts/%d", failed) || f["snippet"] != "Backups · urgent · unread · 2 hours ago" {
		t.Fatalf("an urgent alert: %v", f)
	}
	if _, err := time.Parse(time.RFC3339, f["date"].(string)); err != nil {
		t.Fatalf("date %v", f["date"])
	}
	if got[0]["snippet"] != "Backups · low priority · done · 10 minutes ago" || got[2]["snippet"] != "Sent with the API · high priority · read · 1 hour ago" {
		t.Fatalf("snippets: %v | %v", got[0]["snippet"], got[2]["snippet"])
	}
	// by source and text; every word must be there; an alert without a title is shown by its first line
	for q, want := range map[string]string{
		"BACKUPS":         "Nightly backup done | Backup failed",
		"nas+failed":      "Backup failed",
		"volume+91":       "Disk almost full",
		"tray":            "Printer out of paper",
		"backup+printer":  "",
		"nothing+like+it": "",
		"??":              "",
		"*":               "",
		"%25":             "",
		"_":               "",
		"bob":             "",
	} {
		if got := titles(t, a.ok(a.alice, "GET", "/api/v1/pocket/search?q="+q, nil)); got != want {
			t.Errorf("q=%s finds %q, want %q", q, got, want)
		}
	}
	if p := items(t, a.ok(a.alice, "GET", "/api/v1/pocket/search?q=paper", nil)); len(p) != 1 || p[0]["id"] != fmt.Sprint(untitled) || !strings.HasPrefix(p[0]["snippet"].(string), "Sent with the API · normal · unread · ") {
		t.Fatalf("untitled: %v", p)
	}
	if got := titles(t, a.ok(a.alice, "GET", "/api/v1/pocket/search?q=backup&limit=1", nil)); got != "Nightly backup done" {
		t.Fatalf("limit 1: %q", got)
	}
	if got := items(t, a.ok(a.alice, "GET", "/api/v1/pocket/search?q=", nil)); len(got) != 0 {
		t.Fatalf("no words: %v", got)
	}
	// another person's alerts never appear, the admin's included
	if got := titles(t, a.ok(a.bob, "GET", "/api/v1/pocket/search?q=backup", nil)); got != "Bob's backup failed" {
		t.Fatalf("bob finds %q", got)
	}
	a.untouched(a.alice, before)
	a.expect(401, "", "GET", "/api/v1/pocket/search?q=backup", nil)
}

func TestPocketDay(t *testing.T) {
	a := newApp(t, nil)
	src, _ := a.source(a.alice, map[string]any{"name": "Backups"})
	normal := a.raise(a.alice, map[string]any{"title": "Printer out of paper", "priority": 3})
	urgent := a.raise(a.alice, map[string]any{"source_id": src, "title": "Backup failed", "priority": 5})
	high := a.raise(a.alice, map[string]any{"title": "Disk almost full", "priority": 4})
	late := a.raise(a.alice, map[string]any{"title": "Late one", "priority": 2})
	older := a.raise(a.alice, map[string]any{"title": "Came the day before", "priority": 5})
	theirs := a.raise(a.bob, map[string]any{"title": "Bob's alert", "priority": 3})
	a.at(normal, "2026-03-10T08:05:00Z", "2026-03-10T08:05:00Z")
	a.at(urgent, "2026-03-10T14:30:00.25Z", "2026-03-10T15:00:00Z")
	a.at(high, "2026-03-10T00:00:00.5Z", "2026-03-10T00:00:00.5Z")
	a.at(late, "2026-03-10T23:30:00Z", "2026-03-10T23:30:00Z")  // already the 11th in Sarajevo
	a.at(older, "2026-03-09T22:00:00Z", "2026-03-10T09:00:00Z") // repeated on the 10th, raised on the 9th
	a.at(theirs, "2026-03-10T09:00:00Z", "2026-03-10T09:00:00Z")
	if _, err := a.st.DB.Exec(`UPDATE alerts SET count = 3 WHERE id = ?`, urgent); err != nil {
		t.Fatal(err)
	}
	a.ok(a.alice, "POST", "/api/v1/alerts/mark", map[string]any{"ids": []int64{high}, "what": "done"})
	before := a.alerts(a.alice, "?view=all")

	day := a.ok(a.alice, "GET", "/api/v1/pocket/day/2026-03-10?tz=UTC", nil)
	got := items(t, day)
	// the most important first
	if day["date"] != "2026-03-10" || day["summary"] != "4 alerts, 1 urgent, 1 high" || titles(t, day) != "Backup failed | Disk almost full | Printer out of paper | Late one" {
		t.Fatalf("the day: %v", day)
	}
	u := got[0]
	if u["id"] != fmt.Sprint(urgent) || u["time"] != "14:30" || u["sub"] != "Backups · urgent · seen 3 times" || u["kind"] != "alert" || u["done"] != nil ||
		u["url"] != fmt.Sprintf("/alerts/%d", urgent) {
		t.Fatalf("the urgent alert: %v", u)
	}
	if h := got[1]; h["done"] != true || h["time"] != "00:00" || h["sub"] != "Sent with the API · high priority" {
		t.Fatalf("the alert already done: %v", h)
	}
	if n := got[2]; n["sub"] != "Sent with the API" || n["time"] != "08:05" || got[3]["time"] != "23:30" {
		t.Fatalf("times and lines: %v", got)
	}
	// the person's time zone moves the day's edges and the clock
	sar := a.ok(a.alice, "GET", "/api/v1/pocket/day/2026-03-10?tz=Europe/Sarajevo", nil)
	if sar["summary"] != "3 alerts, 1 urgent, 1 high" || titles(t, sar) != "Backup failed | Disk almost full | Printer out of paper" || items(t, sar)[0]["time"] != "15:30" {
		t.Fatalf("the 10th in Sarajevo: %v", sar)
	}
	next := a.ok(a.alice, "GET", "/api/v1/pocket/day/2026-03-11?tz=Europe/Sarajevo", nil)
	if got := items(t, next); len(got) != 1 || got[0]["time"] != "00:30" || next["summary"] != "1 alert" {
		t.Fatalf("the 11th in Sarajevo: %v", next)
	}
	// an alert counts on the day it came in, not the day it repeated
	if prev := a.ok(a.alice, "GET", "/api/v1/pocket/day/2026-03-09?tz=UTC", nil); titles(t, prev) != "Came the day before" || prev["summary"] != "1 alert, 1 urgent" {
		t.Fatalf("the 9th: %v", prev)
	}
	// no zone said: the box's own
	when, _ := time.Parse(time.RFC3339, "2026-03-10T08:05:00Z")
	found := false
	for _, it := range items(t, a.ok(a.alice, "GET", "/api/v1/pocket/day/"+when.Local().Format("2006-01-02"), nil)) {
		found = found || it["id"] == fmt.Sprint(normal) && it["time"] == when.Local().Format("15:04")
	}
	if !found {
		t.Fatal("the box's day misses the alert")
	}
	// a day with nothing says nothing
	empty := a.ok(a.alice, "GET", "/api/v1/pocket/day/2026-03-01?tz=UTC", nil)
	if len(items(t, empty)) != 0 || empty["summary"] != nil || empty["date"] != "2026-03-01" {
		t.Fatalf("an empty day: %v", empty)
	}
	a.expect(400, a.alice, "GET", "/api/v1/pocket/day/yesterday", nil)
	// per person
	if bob := a.ok(a.bob, "GET", "/api/v1/pocket/day/2026-03-10?tz=UTC", nil); titles(t, bob) != "Bob's alert" || bob["summary"] != "1 alert" {
		t.Fatalf("bob's day: %v", bob)
	}
	a.untouched(a.alice, before)
	a.expect(401, "", "GET", "/api/v1/pocket/day/2026-03-10", nil)
}

func TestPocketWidget(t *testing.T) {
	a := newApp(t, nil)
	empty := func(auth, why string) {
		t.Helper()
		if w := a.ok(auth, "GET", "/api/v1/pocket/widget", nil); len(items(t, w)) != 0 || w["line"] != nil || w["metrics"] != nil {
			t.Fatalf("%s: %v", why, w)
		}
	}
	metrics := func(w map[string]any) string {
		var out []string
		for _, m := range w["metrics"].([]any) {
			out = append(out, fmt.Sprint(m.(map[string]any)["label"], "=", m.(map[string]any)["value"]))
		}
		return strings.Join(out, " ")
	}
	empty(a.alice, "no alerts")

	src, _ := a.source(a.alice, map[string]any{"name": "Backups"})
	normal := a.raise(a.alice, map[string]any{"title": "Printer out of paper", "priority": 3})
	high := a.raise(a.alice, map[string]any{"title": "Disk almost full", "priority": 4})
	old := a.raise(a.alice, map[string]any{"title": "Old urgent", "priority": 5})
	urgent := a.raise(a.alice, map[string]any{"source_id": src, "title": "Backup failed", "priority": 5})
	done := a.raise(a.alice, map[string]any{"title": "Fixed already", "priority": 5})
	a.at(normal, agoTime(time.Minute*5), agoTime(time.Second*10))
	a.at(old, agoTime(72*time.Hour), agoTime(72*time.Hour))
	a.at(urgent, agoTime(3*time.Hour), agoTime(2*time.Hour+time.Minute))
	a.ok(a.alice, "POST", "/api/v1/alerts/mark", map[string]any{"ids": []int64{done}, "what": "done"})
	a.ok(a.alice, "POST", "/api/v1/alerts/mark", map[string]any{"ids": []int64{high}, "what": "read"})
	before := a.alerts(a.alice, "?view=all")

	w := a.ok(a.alice, "GET", "/api/v1/pocket/widget", nil)
	got := items(t, w)
	if w["line"] != "2 urgent, 4 open" || metrics(w) != "Open=4 Urgent=2" || w["url"] != "/" ||
		titles(t, w) != "Backup failed | Old urgent | Disk almost full | Printer out of paper" {
		t.Fatalf("the widget: %v", w)
	}
	if u := got[0]; u["id"] != fmt.Sprint(urgent) || u["detail"] != "Backups · 2 hours ago" || u["url"] != fmt.Sprintf("/alerts/%d", urgent) || u["done"] != nil {
		t.Fatalf("the urgent row: %v", u)
	}
	if got[1]["detail"] != "Sent with the API · 3 days ago" || got[3]["detail"] != "Sent with the API · just now" {
		t.Fatalf("details: %v", got)
	}
	a.untouched(a.alice, before)

	// only urgent ones open, then none urgent
	a.ok(a.alice, "POST", "/api/v1/alerts/mark", map[string]any{"ids": []int64{normal, high}, "what": "done"})
	if w := a.ok(a.alice, "GET", "/api/v1/pocket/widget", nil); w["line"] != "2 urgent" {
		t.Fatalf("all urgent: %v", w)
	}
	a.ok(a.alice, "POST", "/api/v1/alerts/mark", map[string]any{"ids": []int64{old, urgent}, "what": "done"})
	a.ok(a.alice, "POST", "/api/v1/alerts/mark", map[string]any{"ids": []int64{normal, high}, "what": "done", "on": false})
	if w := a.ok(a.alice, "GET", "/api/v1/pocket/widget", nil); w["line"] != "2 open" || metrics(w) != "Open=2 Urgent=0" {
		t.Fatalf("none urgent: %v", w)
	}
	// nothing open: nothing to show
	a.ok(a.alice, "POST", "/api/v1/alerts/mark", map[string]any{"what": "done"})
	empty(a.alice, "everything done")
	// per person
	a.raise(a.alice, map[string]any{"title": "Still alice's", "priority": 5})
	empty(a.bob, "bob's widget")
	a.expect(401, "", "GET", "/api/v1/pocket/widget", nil)
}

func TestAgo(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		-time.Minute: "just now", 30 * time.Second: "just now", time.Minute: "1 minute ago", 59 * time.Minute: "59 minutes ago",
		time.Hour: "1 hour ago", 23 * time.Hour: "23 hours ago", 30 * time.Hour: "a day ago", 50 * time.Hour: "2 days ago", 29 * 24 * time.Hour: "29 days ago",
	} {
		if got := ago(now.Add(-d).Format(time.RFC3339Nano), now); got != want {
			t.Errorf("%v ago = %q, want %q", d, got, want)
		}
	}
	if got := ago(now.AddDate(0, 0, -40).Format(time.RFC3339Nano), now); got != now.AddDate(0, 0, -40).Local().Format("2 Jan") {
		t.Errorf("40 days ago = %q", got)
	}
	if got := ago(now.AddDate(-1, 0, 0).Format(time.RFC3339Nano), now); got != now.AddDate(-1, 0, 0).Local().Format("2 Jan 2006") {
		t.Errorf("a year ago = %q", got)
	}
	if ago("not a time", now) != "" {
		t.Error("a bad time says something")
	}
}

// A link token with one pocket:* scope reaches its one route and nothing else.
func TestPocketLinkTokens(t *testing.T) {
	a, _ := newCapApp(t)
	id := a.raise(a.alice, map[string]any{"title": "Backup failed", "priority": 5})
	a.at(id, "2026-03-10T08:05:00Z", "2026-03-10T08:05:00Z")
	a.raise(a.alice, map[string]any{"title": "Backup slow", "priority": 3})
	before := a.alerts(a.alice, "?view=all")
	const day = "2026-03-10?tz=UTC"
	line := map[string]any{"request_id": "req-0000000000000001", "text": "a line"}
	for _, c := range []struct {
		tok, method, path string
		body              any
		want              int
	}{
		{"pcx_alice_search", "GET", "/api/v1/pocket/search?q=backup", nil, 200},
		{"pcx_alice_day", "GET", "/api/v1/pocket/day/" + day, nil, 200},
		{"pcx_alice_widget", "GET", "/api/v1/pocket/widget", nil, 200},
		{"pcx_alice_widget", "GET", "/api/v1/auth/me", nil, 200},
		// another capability's route
		{"pcx_alice_search", "GET", "/api/v1/pocket/day/" + day, nil, 403},
		{"pcx_alice_search", "GET", "/api/v1/pocket/widget", nil, 403},
		{"pcx_alice_day", "GET", "/api/v1/pocket/search?q=backup", nil, 403},
		{"pcx_alice_day", "GET", "/api/v1/pocket/widget", nil, 403},
		{"pcx_alice_widget", "GET", "/api/v1/pocket/search?q=backup", nil, 403},
		{"pcx_alice_widget", "GET", "/api/v1/pocket/day/" + day, nil, 403},
		// nothing can be kept here, whatever the scope: there is no such route
		{"pcx_alice_capture", "POST", "/api/v1/pocket/capture", line, 404},
		{"pcx_alice_widget", "POST", "/api/v1/pocket/capture", line, 404},
		{"pcx_alice_capture", "GET", "/api/v1/pocket/widget", nil, 403},
		// the rest of the app: nothing is read elsewhere, marked, muted or sent
		{"pcx_alice_widget", "GET", "/api/v1/dashboard", nil, 403},
		{"pcx_alice_search", "GET", "/api/v1/alerts?q=backup", nil, 403},
		{"pcx_alice_search", "GET", fmt.Sprintf("/api/v1/alerts/%d", id), nil, 403},
		{"pcx_alice_day", "GET", "/api/v1/day/2026-03-10", nil, 403},
		{"pcx_alice_widget", "POST", "/api/v1/alerts/mark", map[string]any{"what": "done"}, 403},
		{"pcx_alice_widget", "POST", "/api/v1/alerts/mark", map[string]any{"what": "read"}, 403},
		{"pcx_alice_search", "POST", fmt.Sprintf("/api/v1/alerts/%d/quiet", id), nil, 403},
		{"pcx_alice_day", "POST", "/api/v1/alerts", map[string]any{"title": "x"}, 403},
		{"pcx_alice_widget", "POST", "/api/v1/alerts/delete", map[string]any{"ids": []int64{id}}, 403},
		{"pcx_alice_widget", "POST", "/api/v1/channels/test", nil, 403},
		{"pcx_alice_search", "GET", "/api/v1/settings", nil, 403},
		// the app's own scopes do not open the new routes
		{"pcx_alice_read", "GET", "/api/v1/pocket/search?q=backup", nil, 403},
		{"pcx_alice_read", "GET", "/api/v1/pocket/day/" + day, nil, 403},
		{"pcx_alice_read", "GET", "/api/v1/pocket/widget", nil, 403},
		{"pcx_alice_write", "GET", "/api/v1/pocket/widget", nil, 403},
		{"pcx_alice_write", "POST", "/api/v1/pocket/capture", line, 404},
		// and still open what they always did
		{"pcx_alice_read", "GET", "/api/v1/alerts?q=backup", nil, 200},
		{"pcx_alice_read", "GET", "/api/v1/day/2026-03-10", nil, 200},
		{"pcx_nobody", "GET", "/api/v1/pocket/widget", nil, 401},
	} {
		if code, m, _ := a.call(c.tok, c.method, c.path, c.body); code != c.want {
			t.Errorf("%s %s %s → %d, want %d (%v)", c.tok, c.method, c.path, code, c.want, m)
		}
	}
	a.untouched(a.alice, before)
	// the token acts for its person only
	if got := titles(t, a.ok("pcx_alice_search", "GET", "/api/v1/pocket/search?q=backup", nil)); got != "Backup failed | Backup slow" && got != "Backup slow | Backup failed" {
		t.Fatalf("alice through the link finds %q", got)
	}
	if got := items(t, a.ok("pcx_bob_search", "GET", "/api/v1/pocket/search?q=backup", nil)); len(got) != 0 {
		t.Fatalf("bob through the link finds alice's alert: %v", got)
	}
	if w := a.ok("pcx_alice_widget", "GET", "/api/v1/pocket/widget", nil); w["line"] != "1 urgent, 2 open" {
		t.Fatalf("alice's widget through the link: %v", w)
	}
	if w := a.ok("pcx_bob_widget", "GET", "/api/v1/pocket/widget", nil); len(items(t, w)) != 0 || w["line"] != nil {
		t.Fatalf("bob's widget through the link: %v", w)
	}
	if d := a.ok("pcx_alice_day", "GET", "/api/v1/pocket/day/"+day, nil); titles(t, d) != "Backup failed" {
		t.Fatalf("alice's day through the link: %v", d)
	}
	if d := a.ok("pcx_bob_day", "GET", "/api/v1/pocket/day/"+day, nil); len(items(t, d)) != 0 {
		t.Fatalf("bob's day through the link: %v", d)
	}
}

func TestPocketLinkScopes(t *testing.T) {
	req := func(m, p string) *http.Request { return httptest.NewRequest(m, p, nil) }
	for _, c := range []struct {
		scope string
		r     *http.Request
		want  bool
	}{
		{core.ScopeSearch, req("GET", "/api/v1/pocket/search?q=x"), true},
		{core.ScopeDay, req("GET", "/api/v1/pocket/day/2026-01-02"), true},
		{core.ScopeWidget, req("GET", "/api/v1/pocket/widget"), true},
		{core.ScopeWidget, req("GET", "/api/v1/auth/me"), true},
		{core.ScopeCapture, req("POST", "/api/v1/pocket/capture"), false},
		{core.ScopeSearch, req("POST", "/api/v1/pocket/search"), false},
		{core.ScopeSearch, req("GET", "/api/v1/alerts"), false},
		{core.ScopeDay, req("GET", "/api/v1/pocket/day/today"), false},
		{core.ScopeDay, req("GET", "/api/v1/day/2026-01-02"), false},
		{core.ScopeWidget, req("GET", "/api/v1/dashboard"), false},
		{core.ScopeWidget, req("POST", "/api/v1/alerts/mark"), false},
		{"alerts:read", req("GET", "/api/v1/pocket/search"), false},
		{"alerts:read", req("GET", "/api/v1/pocket/day/2026-01-02"), false},
		{"alerts:read", req("GET", "/api/v1/pocket/widget"), false},
		{"alerts:write", req("POST", "/api/v1/pocket/capture"), false},
	} {
		if got := linkAllows([]string{c.scope}, c.r); got != c.want {
			t.Errorf("%s %s %s = %v, want %v", c.scope, c.r.Method, c.r.URL.Path, got, c.want)
		}
	}
}

// Alerts tells Core what it can do, so Search, Day and Hub find it on their own.
func TestManifestCapabilities(t *testing.T) {
	a, fc := newCapApp(t)
	if err := a.s.registerWithCore(context.Background()); err != nil {
		t.Fatal(err)
	}
	fc.mu.Lock()
	m := fc.manifest
	fc.mu.Unlock()
	if m["slug"] != "alerts" || m["category"] != "System" {
		t.Fatalf("manifest: %v %v", m["slug"], m["category"])
	}
	search, _ := m["search"].(map[string]any)
	day, _ := m["day"].(map[string]any)
	widget, _ := m["widget"].(map[string]any)
	if search == nil || fmt.Sprint(search["bangs"]) != "[alerts alert]" || fmt.Sprint(search["kinds"]) != "[alert]" ||
		day == nil || day["label"] != "Alerts" || day["one"] != "alert" || day["many"] != "alerts" || widget == nil || widget["title"] != "Alerts" || m["capture"] != nil {
		t.Fatalf("capabilities: %v %v %v %v", search, day, widget, m["capture"])
	}
	offered := map[string]bool{}
	for _, o := range m["offers"].([]any) {
		offered[o.(map[string]any)["scope"].(string)] = true
	}
	for _, sc := range []string{"alerts:read", "alerts:write", core.ScopeMoveExport, core.ScopeMoveImport, core.ScopeSearch, core.ScopeDay, core.ScopeWidget} {
		if !offered[sc] {
			t.Errorf("%s is not offered", sc)
		}
	}
	if offered[core.ScopeCapture] {
		t.Error("pocket:capture is offered, but nothing can be kept here")
	}
}
