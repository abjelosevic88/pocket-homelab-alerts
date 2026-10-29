package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-alerts/internal/config"
	"github.com/abjelosevic88/pocket-homelab-alerts/internal/store"
)

type app struct {
	t     *testing.T
	url   string
	s     *Server
	st    *store.Store
	alice string
	bob   string
	ids   map[string]int64
}

func newApp(t *testing.T, tweak func(*config.Config)) *app {
	t.Helper()
	cfg := testConfig(t.TempDir())
	cfg.BaseURL = "http://alerts.test"
	cfg.MaxBodyBytes = 64 << 20
	cfg.LocalTargets = "all"
	if tweak != nil {
		tweak(cfg)
	}
	st := openTestStore(t, cfg)
	ctx := context.Background()
	a := &app{t: t, st: st, ids: map[string]int64{}}
	for _, u := range []*store.User{{Email: "alice@example.com", Name: "Alice", Role: "admin"}, {Email: "bob@example.com", Name: "Bob", Role: "member"}} {
		if err := st.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		tok, _, _ := st.CreateAPIToken(ctx, u.ID, "t")
		a.ids[u.Name] = u.ID
		if u.Name == "Alice" {
			a.alice = tok
		} else {
			a.bob = tok
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	a.s = New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err := a.s.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(a.s.Router())
	t.Cleanup(func() { cancel(); ts.Close(); a.s.Wait() })
	a.url = ts.URL
	return a
}

func (a *app) call(auth, method, path string, body any) (int, map[string]any, []any) {
	a.t.Helper()
	var rd io.Reader
	if body != nil {
		j, _ := json.Marshal(body)
		rd = bytes.NewReader(j)
	}
	req, _ := http.NewRequest(method, a.url+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var m map[string]any
	var l []any
	if json.Unmarshal(raw, &m) != nil {
		_ = json.Unmarshal(raw, &l)
	}
	return res.StatusCode, m, l
}

func (a *app) ok(auth, method, path string, body any) map[string]any {
	a.t.Helper()
	code, m, _ := a.call(auth, method, path, body)
	if code >= 300 {
		a.t.Fatalf("%s %s → %d %v", method, path, code, m)
	}
	return m
}

func (a *app) list(auth, path string) []any {
	a.t.Helper()
	code, m, l := a.call(auth, "GET", path, nil)
	if code >= 300 {
		a.t.Fatalf("GET %s → %d %v", path, code, m)
	}
	return l
}

func (a *app) expect(want int, auth, method, path string, body any) map[string]any {
	a.t.Helper()
	code, m, _ := a.call(auth, method, path, body)
	if code != want {
		a.t.Fatalf("%s %s → %d, want %d (%v)", method, path, code, want, m)
	}
	return m
}

// post sends raw text with headers (the ntfy / Apprise shapes).
func (a *app) post(path, body string, hdr map[string]string) (int, map[string]any) {
	a.t.Helper()
	req, _ := http.NewRequest("POST", a.url+path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer res.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(res.Body).Decode(&m)
	return res.StatusCode, m
}

func num(m map[string]any, k string) float64 { v, _ := m[k].(float64); return v }

func (a *app) alerts(auth, q string) []map[string]any {
	a.t.Helper()
	m := a.ok(auth, "GET", "/api/v1/alerts"+q, nil)
	out := []map[string]any{}
	for _, x := range m["alerts"].([]any) {
		out = append(out, x.(map[string]any))
	}
	return out
}

func (a *app) source(auth string, body map[string]any) (int64, string) {
	a.t.Helper()
	m := a.expect(201, auth, "POST", "/api/v1/sources", body)
	key := ""
	if ad, ok := m["addresses"].(map[string]any); ok {
		key, _ = ad["ntfy_topic"].(string)
	}
	return int64(num(m, "id")), key
}

// sink is a fake channel target that records what it got.
type sink struct {
	mu   sync.Mutex
	got  []map[string]string
	code int
	srv  *httptest.Server
}

func newSink(t *testing.T) *sink {
	k := &sink{code: 200}
	k.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		m := map[string]string{"path": r.URL.Path, "body": string(b), "title": r.Header.Get("Title"), "priority": r.Header.Get("Priority"), "auth": r.Header.Get("Authorization")}
		if r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" {
			v, _ := url.ParseQuery(string(b))
			for key := range v {
				m["form_"+key] = v.Get(key)
			}
		}
		k.mu.Lock()
		k.got = append(k.got, m)
		code := k.code
		k.mu.Unlock()
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"status":1}`))
	}))
	t.Cleanup(k.srv.Close)
	return k
}

func (k *sink) wait(t *testing.T, n int) []map[string]string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		k.mu.Lock()
		if len(k.got) >= n {
			out := append([]map[string]string(nil), k.got...)
			k.mu.Unlock()
			return out
		}
		k.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the channel got %d messages, want %d", len(k.got), n)
	return nil
}

func (k *sink) count() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.got)
}

func TestEverythingIsPerPerson(t *testing.T) {
	a := newApp(t, nil)
	sid, key := a.source(a.alice, map[string]any{"name": "Backups"})
	if key == "" {
		t.Fatal("a push source has no address")
	}
	if code, _ := a.post("/notify/"+key, `{"title":"Backup failed","body":"nas unreachable","type":"failure"}`, map[string]string{"Content-Type": "application/json"}); code != 200 {
		t.Fatalf("apprise notify → %d", code)
	}
	al := a.alerts(a.alice, "")
	if len(al) != 1 || al[0]["title"] != "Backup failed" || num(al[0], "priority") != 4 || al[0]["emoji"] != "🚨" {
		t.Fatalf("alice's inbox = %v", al)
	}
	aid := int64(num(al[0], "id"))
	ch := a.expect(201, a.alice, "POST", "/api/v1/channels", map[string]any{"kind": "webhook", "config": map[string]string{"url": "http://example.invalid/x", "secret": "s3cret-value"}})
	if ch["config"].(map[string]any)["secret"] != "s3cr••••alue" {
		t.Fatalf("the secret came back: %v", ch["config"])
	}
	rule := a.expect(201, a.alice, "POST", "/api/v1/rules", map[string]any{"name": "Quiet backups", "action": "quiet", "match": map[string]any{"source_ids": []int64{sid}}})

	// Bob sees and touches none of it
	if l := a.alerts(a.bob, "?view=all"); len(l) != 0 {
		t.Fatalf("bob sees %d alerts", len(l))
	}
	for _, l := range []string{"/api/v1/sources", "/api/v1/channels", "/api/v1/rules"} {
		if got := a.list(a.bob, l); len(got) != 0 {
			t.Fatalf("bob sees %s: %v", l, got)
		}
	}
	for _, p := range []struct{ m, path string }{
		{"GET", fmt.Sprintf("/api/v1/alerts/%d", aid)}, {"DELETE", fmt.Sprintf("/api/v1/alerts/%d", aid)},
		{"POST", fmt.Sprintf("/api/v1/alerts/%d/quiet", aid)}, {"POST", fmt.Sprintf("/api/v1/alerts/%d/task", aid)},
		{"GET", fmt.Sprintf("/api/v1/sources/%d", sid)}, {"PUT", fmt.Sprintf("/api/v1/sources/%d", sid)}, {"DELETE", fmt.Sprintf("/api/v1/sources/%d", sid)},
		{"POST", fmt.Sprintf("/api/v1/sources/%d/key", sid)}, {"POST", fmt.Sprintf("/api/v1/sources/%d/test", sid)}, {"POST", fmt.Sprintf("/api/v1/sources/%d/snooze", sid)},
		{"PUT", fmt.Sprintf("/api/v1/channels/%d", int64(num(ch, "id")))}, {"DELETE", fmt.Sprintf("/api/v1/channels/%d", int64(num(ch, "id")))},
		{"POST", fmt.Sprintf("/api/v1/channels/%d/test", int64(num(ch, "id")))}, {"GET", fmt.Sprintf("/api/v1/channels/%d/deliveries", int64(num(ch, "id")))},
		{"PUT", fmt.Sprintf("/api/v1/rules/%d", int64(num(rule, "id")))}, {"DELETE", fmt.Sprintf("/api/v1/rules/%d", int64(num(rule, "id")))},
	} {
		a.expect(404, a.bob, p.m, p.path, map[string]any{"name": "mine"})
	}
	// marking "everything" read or done only reaches Bob's own
	a.ok(a.bob, "POST", "/api/v1/alerts/mark", map[string]any{"what": "done"})
	a.ok(a.bob, "POST", "/api/v1/alerts/mark", map[string]any{"what": "done", "ids": []int64{aid}})
	a.ok(a.bob, "POST", "/api/v1/alerts/delete", map[string]any{"ids": []int64{aid}})
	if l := a.alerts(a.alice, ""); len(l) != 1 || l[0]["done_at"] != nil {
		t.Fatalf("bob reached alice's alert: %v", l)
	}
	// a rule cannot point at someone else's source or channel
	r2 := a.expect(201, a.bob, "POST", "/api/v1/rules", map[string]any{"name": "x", "match": map[string]any{"source_ids": []int64{sid}}, "channel_ids": []int64{int64(num(ch, "id"))}})
	if len(r2["match"].(map[string]any)["source_ids"].([]any)) != 0 || len(r2["channel_ids"].([]any)) != 0 {
		t.Fatalf("bob's rule kept alice's ids: %v", r2)
	}
	// the admin does not see Bob's alerts either
	bsid, bkey := a.source(a.bob, map[string]any{"name": "Bob's"})
	a.post("/hook/"+bkey, "hello from bob", nil)
	if l := a.alerts(a.alice, "?view=all"); len(l) != 1 {
		t.Fatalf("alice sees bob's alerts: %d", len(l))
	}
	a.expect(404, a.alice, "GET", fmt.Sprintf("/api/v1/sources/%d", bsid), nil)
}

func TestNtfyStylePublishing(t *testing.T) {
	a := newApp(t, nil)
	_, key := a.source(a.alice, map[string]any{"name": "Arr"})
	code, m := a.post("/"+key, "Indexers unavailable", map[string]string{"X-Title": "Health issue", "X-Priority": "high", "X-Tags": "warning,sonarr", "X-Click": "https://sonarr.example"})
	if code != 200 || m["event"] != "message" {
		t.Fatalf("ntfy publish → %d %v", code, m)
	}
	// ntfy's JSON at / (what Apprise's ntfy:// sends)
	if code, _ := a.post("/", `{"topic":"`+key+`","message":"json","title":"Via JSON","priority":2,"tags":["tada"]}`, map[string]string{"Content-Type": "application/json"}); code != 200 {
		t.Fatalf("ntfy json → %d", code)
	}
	// query parameters
	if code, _ := a.post("/"+key+"?title=From+query&p=5", "q", nil); code != 200 {
		t.Fatalf("ntfy query → %d", code)
	}
	l := a.alerts(a.alice, "")
	if len(l) != 3 || l[2]["title"] != "Health issue" || num(l[2], "priority") != 4 || l[2]["emoji"] != "⚠️" || l[2]["labels"].([]any)[0] != "sonarr" || l[2]["click_url"] != "https://sonarr.example" {
		t.Fatalf("alerts = %v", l)
	}
	if num(l[0], "priority") != 5 || l[0]["title"] != "From query" || num(l[1], "priority") != 2 {
		t.Fatalf("alerts = %v", l)
	}
	// a topic of one's own needs the token; then it becomes a source
	if code, _ := a.post("/backups", "x", nil); code != 401 {
		t.Fatalf("anonymous topic → %d", code)
	}
	if code, _ := a.post("/backups", "with token", map[string]string{"Authorization": "Bearer " + a.bob}); code != 200 {
		t.Fatalf("bearer topic → %d", code)
	}
	if code, _ := a.post("/backups", "with basic", map[string]string{"Authorization": "Basic " + b64std("x:"+a.bob)}); code != 200 {
		t.Fatalf("basic topic → %d", code)
	}
	srcs := a.list(a.bob, "/api/v1/sources")
	if len(srcs) != 1 || srcs[0].(map[string]any)["topic"] != "backups" {
		t.Fatalf("bob's sources = %v", srcs)
	}
	if l := a.alerts(a.bob, ""); len(l) != 2 {
		t.Fatalf("bob has %d alerts", len(l))
	}
	// an unknown key is not found, and not a way in
	if code, _ := a.post("/notify/nope", `{"body":"x"}`, map[string]string{"Content-Type": "application/json"}); code != 404 {
		t.Fatalf("unknown key → %d", code)
	}
	// the page itself still opens at a one-word path
	res, _ := http.Get(a.url + "/sources")
	res.Body.Close()
	if res.StatusCode == http.StatusMethodNotAllowed {
		t.Fatal("GET /sources answered 405")
	}
}

func b64std(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestRepeatsCountUpAndRulesDecide(t *testing.T) {
	a := newApp(t, nil)
	k := newSink(t)
	sid, key := a.source(a.alice, map[string]any{"name": "Disks"})
	a.expect(201, a.alice, "POST", "/api/v1/channels", map[string]any{"kind": "ntfy", "config": map[string]string{"url": k.srv.URL, "topic": "phone"}})
	for _, pct := range []int{91, 92, 93} {
		a.post("/"+key, fmt.Sprintf("pool tank is %d%% full", pct), map[string]string{"Title": fmt.Sprintf("Disk %d%% full", pct), "Priority": "4"})
	}
	l := a.alerts(a.alice, "")
	if len(l) != 1 || num(l[0], "count") != 3 || l[0]["body"] != "pool tank is 93% full" {
		t.Fatalf("repeats = %v", l)
	}
	got := k.wait(t, 1)
	time.Sleep(200 * time.Millisecond)
	if k.count() != 1 || got[0]["title"] != "Disk 91% full" || got[0]["priority"] != "4" || got[0]["path"] != "/phone" {
		t.Fatalf("the phone got %d: %v", k.count(), got)
	}
	// done: the next one is new again
	a.ok(a.alice, "POST", "/api/v1/alerts/mark", map[string]any{"what": "done"})
	a.post("/"+key, "again", map[string]string{"Title": "Disk 95% full"})
	if l := a.alerts(a.alice, ""); len(l) != 1 || num(l[0], "count") != 1 {
		t.Fatalf("after done = %v", l)
	}
	k.wait(t, 2)

	// rules: drop, quiet, and send with another importance
	a.ok(a.alice, "POST", "/api/v1/rules", map[string]any{"name": "No tests", "action": "drop", "match": map[string]any{"words": "test ping"}})
	a.ok(a.alice, "POST", "/api/v1/rules", map[string]any{"name": "Scrubs quiet", "action": "quiet", "match": map[string]any{"source_ids": []int64{sid}, "words": "scrub"}})
	a.ok(a.alice, "POST", "/api/v1/rules", map[string]any{"name": "Low but send", "action": "send", "priority": 2, "match": map[string]any{"tags": "inbox_tray"}})
	a.post("/"+key, "a test ping", nil)
	a.post("/"+key, "scrub finished", map[string]string{"Title": "Scrub"})
	a.post("/"+key, "job finished", map[string]string{"Title": "Download", "Tags": "inbox_tray"})
	a.post("/"+key, "low", map[string]string{"Title": "Lowly", "Priority": "1"})
	l = a.alerts(a.alice, "")
	byTitle := map[string]map[string]any{}
	for _, x := range l {
		byTitle[x["title"].(string)] = x
	}
	if _, ok := byTitle[""]; ok {
		t.Fatalf("the dropped test ping was kept: %v", l)
	}
	if s := byTitle["Scrub"]; s["state"] != "quiet" || !strings.Contains(s["reason"].(string), "Scrubs quiet") {
		t.Fatalf("scrub = %v", s)
	}
	if d := byTitle["Download"]; d["state"] != "sent" || num(d, "priority") != 2 {
		t.Fatalf("download = %v", d)
	}
	if lo := byTitle["Lowly"]; lo["state"] != "quiet" {
		t.Fatalf("a lowest alert was sent: %v", lo)
	}
	k.wait(t, 3)
	// rule hits are counted; the preview finds what a rule would catch
	rules := a.list(a.alice, "/api/v1/rules")
	if num(rules[0].(map[string]any), "hits") != 1 {
		t.Fatalf("rules = %v", rules)
	}
	p := a.ok(a.alice, "POST", "/api/v1/rules/preview", map[string]any{"match": map[string]any{"words": "disk"}})
	if num(p, "matched") != 4 {
		t.Fatalf("preview = %v", p)
	}
	// mute similar makes a quiet rule for this source and these words
	disk := byTitle["Disk 95% full"]
	r := a.ok(a.alice, "POST", fmt.Sprintf("/api/v1/alerts/%d/quiet", int64(num(disk, "id"))), nil)
	if r["action"] != "quiet" || !strings.Contains(r["match"].(map[string]any)["words"].(string), "Disk") {
		t.Fatalf("mute similar = %v", r)
	}
}

func TestMuteSnoozeAndQuietHours(t *testing.T) {
	a := newApp(t, nil)
	k := newSink(t)
	sid, key := a.source(a.alice, map[string]any{"name": "Kuma"})
	a.expect(201, a.alice, "POST", "/api/v1/channels", map[string]any{"kind": "ntfy", "config": map[string]string{"url": k.srv.URL, "topic": "p"}})
	a.ok(a.alice, "PUT", fmt.Sprintf("/api/v1/sources/%d", sid), map[string]any{"muted": true})
	a.post("/"+key, "down", map[string]string{"Title": "Muted"})
	a.ok(a.alice, "PUT", fmt.Sprintf("/api/v1/sources/%d", sid), map[string]any{"muted": false})
	sn := a.ok(a.alice, "POST", fmt.Sprintf("/api/v1/sources/%d/snooze", sid), map[string]any{"minutes": 60})
	if sn["snoozed_until"] == nil {
		t.Fatalf("snooze = %v", sn)
	}
	a.post("/"+key, "down", map[string]string{"Title": "Snoozed"})
	a.ok(a.alice, "POST", fmt.Sprintf("/api/v1/sources/%d/snooze", sid), map[string]any{"minutes": 0})

	// quiet hours around now: normal alerts wait, urgent ones come through
	now := time.Now()
	a.ok(a.alice, "PUT", "/api/v1/settings", map[string]any{"quiet_enabled": true,
		"quiet_start": now.Add(-time.Hour).Format("15:04"), "quiet_end": now.Add(time.Hour).Format("15:04"), "quiet_min_priority": 5})
	a.post("/"+key, "night", map[string]string{"Title": "Held one", "Priority": "4"})
	a.post("/"+key, "night", map[string]string{"Title": "Held two"})
	a.post("/"+key, "fire", map[string]string{"Title": "Urgent", "Priority": "5"})
	k.wait(t, 1)
	time.Sleep(200 * time.Millisecond)
	if k.count() != 1 {
		t.Fatalf("during quiet hours the phone got %d", k.count())
	}
	states := map[string]string{}
	for _, x := range a.alerts(a.alice, "") {
		states[x["title"].(string)] = x["state"].(string)
	}
	if states["Muted"] != "quiet" || states["Snoozed"] != "quiet" || states["Held one"] != "held" || states["Held two"] != "held" || states["Urgent"] != "sent" {
		t.Fatalf("states = %v", states)
	}
	if d := a.ok(a.alice, "GET", "/api/v1/dashboard", nil); d["quiet"] != true || num(d["counts"].(map[string]any), "held") != 2 {
		t.Fatalf("dashboard = %v", d)
	}
	// still quiet: no summary yet; quiet hours over: one summary
	a.s.sendSummaries(context.Background())
	if k.count() != 1 {
		t.Fatal("a summary went out during quiet hours")
	}
	a.ok(a.alice, "PUT", "/api/v1/settings", map[string]any{"quiet_enabled": false})
	a.s.sendSummaries(context.Background())
	got := k.wait(t, 2)
	if !strings.Contains(got[1]["title"], "2 alerts") || !strings.Contains(got[1]["body"], "Held one") {
		t.Fatalf("summary = %v", got[1])
	}
	for _, x := range a.alerts(a.alice, "") {
		if x["state"] == "held" {
			t.Fatalf("still held after the summary: %v", x)
		}
	}
}

func TestQuietHoursOverMidnight(t *testing.T) {
	st := store.Settings{QuietEnabled: true, QuietStart: "22:30", QuietEnd: "07:00"}
	at := func(h, m int) time.Time { return time.Date(2026, 10, 28, h, m, 0, 0, time.Local) } // a Wednesday
	for _, c := range []struct {
		t    time.Time
		want bool
	}{{at(22, 29), false}, {at(22, 30), true}, {at(23, 59), true}, {at(0, 30), true}, {at(6, 59), true}, {at(7, 0), false}, {at(12, 0), false}} {
		if on, _ := quietNow(st, c.t); on != c.want {
			t.Errorf("%s: quiet = %v", c.t.Format("15:04"), on)
		}
	}
	st.QuietDays = []int{5, 6} // Friday and Saturday nights
	if on, _ := quietNow(st, at(23, 0)); on {
		t.Error("Wednesday night is not a quiet night")
	}
	fri := time.Date(2026, 10, 30, 23, 0, 0, 0, time.Local)
	if on, _ := quietNow(st, fri); !on {
		t.Error("Friday night is quiet")
	}
	if on, _ := quietNow(st, fri.Add(7*time.Hour)); !on {
		t.Error("Saturday 06:00 still belongs to Friday night")
	}
}

func TestChannelsSendAndRetry(t *testing.T) {
	a := newApp(t, nil)
	push := newSink(t)
	hook := newSink(t)
	_, key := a.source(a.alice, map[string]any{"name": "Box"})
	a.expect(201, a.alice, "POST", "/api/v1/channels", map[string]any{"kind": "pushover", "config": map[string]string{"user": "u-key", "token": "app-token"}})
	// the API address is not something people set; point it at the sink
	chans, _ := a.st.ListChannels(context.Background(), a.ids["Alice"])
	chans[0].Config["api"] = push.srv.URL
	_ = a.st.UpdateChannel(context.Background(), a.ids["Alice"], &chans[0])
	wh := a.expect(201, a.alice, "POST", "/api/v1/channels", map[string]any{"kind": "webhook", "min_priority": 4, "config": map[string]string{"url": hook.srv.URL, "secret": "tok"}})
	long := strings.Repeat("word ", 300)
	a.post("/"+key, long, map[string]string{"Title": "Long one", "Tags": "warning,box", "Priority": "5"})
	got := push.wait(t, 2)
	if got[0]["form_user"] != "u-key" || got[0]["form_priority"] != "1" || !strings.HasPrefix(got[0]["form_title"], "⚠️ Long one") || !strings.HasSuffix(got[0]["form_title"], "(1/2)") {
		t.Fatalf("pushover = %v", got[0])
	}
	if !strings.Contains(got[1]["form_message"], "🏷 box") {
		t.Fatalf("labels missing: %v", got[1])
	}
	h := hook.wait(t, 1)
	var j map[string]any
	_ = json.Unmarshal([]byte(h[0]["body"]), &j)
	if h[0]["auth"] != "Bearer tok" || j["title"] != "Long one" || j["importance"] != "urgent" {
		t.Fatalf("webhook = %v", h[0])
	}
	// a normal alert skips the webhook (high and up only)
	a.post("/"+key, "normal", map[string]string{"Title": "Normal"})
	push.wait(t, 3)
	time.Sleep(150 * time.Millisecond)
	if hook.count() != 1 {
		t.Fatal("the webhook got a normal alert")
	}
	// a passing failure is tried again; a refusal is not
	hook.mu.Lock()
	hook.code = 503
	hook.mu.Unlock()
	a.post("/"+key, "x", map[string]string{"Title": "Retry me", "Priority": "4"})
	hook.wait(t, 2)
	var d *store.Delivery
	for i := 0; i < 50 && d == nil; i++ {
		ds, _ := a.st.RecentDeliveries(context.Background(), a.ids["Alice"], int64(num(wh, "id")), 1)
		if len(ds) == 1 && ds[0].Status == "retrying" {
			d = &ds[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	if d == nil {
		t.Fatal("no retry was planned")
	}
	hook.mu.Lock()
	hook.code = 200
	hook.mu.Unlock()
	past := time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	d.NextAt = &past
	_ = a.st.UpdateDelivery(context.Background(), d)
	a.s.retryDue(context.Background())
	ds, _ := a.st.RecentDeliveries(context.Background(), a.ids["Alice"], int64(num(wh, "id")), 1)
	if ds[0].Status != "sent" || ds[0].Attempts != 2 {
		t.Fatalf("after the retry = %+v", ds[0])
	}
	// the test button
	if r := a.ok(a.alice, "POST", fmt.Sprintf("/api/v1/channels/%d/test", int64(num(wh, "id"))), nil); r["ok"] != true {
		t.Fatalf("test = %v", r)
	}
	hook.mu.Lock()
	hook.code = 401
	hook.mu.Unlock()
	if r := a.ok(a.alice, "POST", fmt.Sprintf("/api/v1/channels/%d/test", int64(num(wh, "id"))), nil); r["ok"] != false || !strings.Contains(r["error"].(string), "401") {
		t.Fatalf("failing test = %v", r)
	}
}

func TestMembersCannotReachLocalAddresses(t *testing.T) {
	a := newApp(t, func(c *config.Config) { c.LocalTargets = "admins" })
	k := newSink(t) // on 127.0.0.1
	body := map[string]any{"kind": "webhook", "config": map[string]string{"url": k.srv.URL}}
	if r := a.ok(a.bob, "POST", "/api/v1/channels/test", body); r["ok"] != false || !strings.Contains(r["error"].(string), "private network") {
		t.Fatalf("bob reached a local address: %v", r)
	}
	if r := a.ok(a.alice, "POST", "/api/v1/channels/test", body); r["ok"] != true {
		t.Fatalf("the admin could not: %v", r)
	}
}

func TestFollowingAnNtfyServer(t *testing.T) {
	a := newApp(t, nil)
	var mu sync.Mutex
	var asked []string
	stop := make(chan struct{})
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.String())
		n := len(asked)
		mu.Unlock()
		fl := w.(http.Flusher)
		fmt.Fprintln(w, `{"id":"open1","time":1,"event":"open","topic":"alerts"}`)
		if n == 1 {
			fmt.Fprintln(w, `{"id":"m1","time":1761700000,"event":"message","topic":"alerts","title":"Backup failed","message":"nas down","priority":4,"tags":["x"]}`)
			fmt.Fprintln(w, `{"id":"m2","time":1761700060,"event":"message","topic":"alerts","message":"second"}`)
		}
		fl.Flush()
		if n == 1 {
			return // the connection drops; the follower comes back with since=m2
		}
		select {
		case <-r.Context().Done():
		case <-stop:
		}
	}))
	defer ntfy.Close()
	defer close(stop)
	sid, _ := a.source(a.alice, map[string]any{"kind": "ntfy", "name": "Home ntfy", "follow_url": ntfy.URL, "follow_topics": "alerts", "backfill": true})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(asked)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) < 2 || !strings.Contains(asked[0], "since=12h") || !strings.Contains(asked[1], "since=m2") {
		t.Fatalf("asked = %v", asked)
	}
	l := a.alerts(a.alice, "")
	if len(l) != 2 || l[1]["title"] != "Backup failed" || l[1]["ext_id"] != "ntfy:m1" {
		t.Fatalf("followed = %v", l)
	}
	src := a.ok(a.alice, "GET", fmt.Sprintf("/api/v1/sources/%d", sid), nil)
	if src["status"] != "ok" && src["status"] != "error" {
		t.Fatalf("source = %v", src)
	}
}

func TestExportImportIsIdempotent(t *testing.T) {
	a := newApp(t, func(c *config.Config) { c.DemoData = true })
	if err := a.s.seedDemo(context.Background(), a.ids["Alice"]); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", a.url+"/api/v1/export.json", nil)
	req.Header.Set("Authorization", "Bearer "+a.alice)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var exp exportDoc
	if err := json.Unmarshal(doc, &exp); err != nil || len(exp.Sources) != 7 || len(exp.Channels) != 3 || len(exp.Rules) != 5 || len(exp.Alerts) < 100 {
		t.Fatalf("export: %v, %d sources %d channels %d rules %d alerts", err, len(exp.Sources), len(exp.Channels), len(exp.Rules), len(exp.Alerts))
	}
	var raw map[string]any
	_ = json.Unmarshal(doc, &raw)
	first := a.ok(a.bob, "POST", "/api/v1/import", raw)
	if num(first, "sources") != 7 || num(first, "channels") != 3 || num(first, "rules") != 5 || int(num(first, "alerts")) != len(exp.Alerts) {
		t.Fatalf("first import = %v", first)
	}
	second := a.ok(a.bob, "POST", "/api/v1/import", raw)
	for _, k := range []string{"sources", "channels", "rules", "alerts"} {
		if num(second, k) != 0 {
			t.Fatalf("second import added %s: %v", k, second)
		}
	}
	// imported sources get addresses of their own
	bs := a.list(a.bob, "/api/v1/sources")
	as := a.list(a.alice, "/api/v1/sources")
	keys := map[string]bool{}
	for _, x := range as {
		if ad, ok := x.(map[string]any)["addresses"].(map[string]any); ok {
			keys[ad["ntfy_topic"].(string)] = true
		}
	}
	for _, x := range bs {
		if ad, ok := x.(map[string]any)["addresses"].(map[string]any); ok && keys[ad["ntfy_topic"].(string)] {
			t.Fatal("an imported source kept the old address")
		}
	}
}

func TestSettingsPerPerson(t *testing.T) {
	a := newApp(t, nil)
	a.ok(a.alice, "PUT", "/api/v1/settings", map[string]any{"quiet_enabled": true, "quiet_start": "23:00", "send_min_priority": 4, "tasks_token": "secret-token", "title": "Our Alerts"})
	al := a.ok(a.alice, "GET", "/api/v1/settings", nil)
	if al["quiet_enabled"] != true || al["quiet_start"] != "23:00" || num(al, "send_min_priority") != 4 || al["tasks_token"] != "secr••••oken" || al["title"] != "Our Alerts" {
		t.Fatalf("alice = %v", al)
	}
	a.ok(a.bob, "PUT", "/api/v1/settings", map[string]any{"title": "Bob's"})
	bo := a.ok(a.bob, "GET", "/api/v1/settings", nil)
	if bo["quiet_enabled"] != false || bo["quiet_start"] != "22:30" || num(bo, "send_min_priority") != 3 || bo["tasks_token"] != "" || bo["title"] != "Our Alerts" {
		t.Fatalf("bob = %v", bo)
	}
	a.expect(400, a.alice, "PUT", "/api/v1/settings", map[string]any{"quiet_start": "late"})
	a.expect(400, a.alice, "PUT", "/api/v1/settings", map[string]any{"send_min_priority": 9})
	a.expect(400, a.alice, "PUT", "/api/v1/settings", map[string]any{"quiet_days": []int{7}})
}

func TestLinkScopes(t *testing.T) {
	get := func(p string) *http.Request { r, _ := http.NewRequest("GET", p, nil); return r }
	post := func(p string) *http.Request { r, _ := http.NewRequest("POST", p, nil); return r }
	read := []string{"alerts:read"}
	for _, p := range []string{"/api/v1/day/2026-10-29", "/api/v1/dashboard", "/api/v1/alerts", "/api/v1/alerts/3", "/api/v1/sources"} {
		if !linkAllows(read, get(p)) {
			t.Errorf("read should allow %s", p)
		}
	}
	if linkAllows(read, post("/api/v1/alerts")) || linkAllows(read, get("/api/v1/settings")) || linkAllows(read, get("/api/v1/export.json")) || linkAllows(read, get("/api/v1/channels")) {
		t.Fatal("read must not write, nor see settings, channels or exports")
	}
	write := []string{"alerts:write"}
	if !linkAllows(write, post("/api/v1/alerts")) || linkAllows(write, get("/api/v1/alerts")) || linkAllows(write, post("/api/v1/alerts/mark")) || linkAllows(write, post("/api/v1/sources")) {
		t.Fatal("write may send alerts, nothing else")
	}
}

func TestSplitText(t *testing.T) {
	parts := splitText(strings.Repeat("ab\n", 700), 1024)
	if len(parts) != 3 {
		t.Fatalf("parts = %d", len(parts))
	}
	for _, p := range parts {
		if len(p) > 1024 {
			t.Fatalf("a part of %d", len(p))
		}
	}
	if fingerprint("Disk 91% full", "") != fingerprint("disk 92%  FULL", "") || fingerprint("Disk full", "") == fingerprint("Disk empty", "") {
		t.Fatal("fingerprints")
	}
}
