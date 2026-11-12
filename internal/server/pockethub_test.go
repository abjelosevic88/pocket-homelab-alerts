package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-alerts/internal/config"
	"github.com/abjelosevic88/pocket-homelab-alerts/internal/store"
)

// fakeCore answers /api/v1/push/notify the way Pocket Core does and records
// what Alerts sent; every other Core route is a 404, as on an old Core.
type fakeCore struct {
	mu     sync.Mutex
	got    []map[string]any
	auth   []string
	code   int
	answer string
	srv    *httptest.Server
}

func newFakeCore(t *testing.T) *fakeCore {
	f := &fakeCore{code: 200, answer: `{"devices":2,"sent":2,"failed":0,"gone":0}`}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost || r.URL.Path != pushNotifyPath {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
			return
		}
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		f.mu.Lock()
		f.got = append(f.got, m)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		code, answer := f.code, f.answer
		f.mu.Unlock()
		w.WriteHeader(code)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCore) set(code int, answer string) {
	f.mu.Lock()
	f.code, f.answer = code, answer
	f.mu.Unlock()
}

func (f *fakeCore) wait(t *testing.T, n int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		if len(f.got) >= n {
			out := append([]map[string]any(nil), f.got...)
			f.mu.Unlock()
			return out
		}
		f.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Core got %d notify calls, want %d", len(f.got), n)
	return nil
}

func newHubApp(t *testing.T, f *fakeCore) *app {
	return newApp(t, func(c *config.Config) {
		c.CoreURL = f.srv.URL
		if err := os.WriteFile(c.DataDir+"/core.token", []byte("pca_alerts_test\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
}

func TestPocketHubChannel(t *testing.T) {
	f := newFakeCore(t)
	a := newHubApp(t, f)
	ch := a.expect(201, a.alice, "POST", "/api/v1/channels", map[string]any{"kind": "pockethub", "config": map[string]string{"url": "ignored"}})
	if ch["name"] != "Pocket Hub" || len(ch["config"].(map[string]any)) != 0 {
		t.Fatalf("channel = %v", ch)
	}
	cid := int64(num(ch, "id"))
	_, key := a.source(a.alice, map[string]any{"name": "Backups"})
	a.post("/"+key, "Nightly backup took 4 min", map[string]string{"Title": "Backup finished", "Priority": "4", "Tags": "white_check_mark", "Click": "https://example.com/b"})
	got := f.wait(t, 1)
	m := got[0]
	if f.auth[0] != "Bearer pca_alerts_test" {
		t.Fatalf("auth = %q", f.auth[0])
	}
	if m["email"] != "alice@example.com" || m["title"] != "✅ Backup finished" || m["body"] != "Nightly backup took 4 min" ||
		m["url"] != "https://example.com/b" || m["source"] != "Backups" || m["thread"] != "Backups" || m["sound"] != true {
		t.Fatalf("notify body = %v", m)
	}
	if id, _ := m["id"].(string); id == "" || id == "0" {
		t.Fatalf("id = %v", m["id"])
	}
	for _, k := range []string{"user_id", "at"} {
		if _, ok := m[k]; ok {
			t.Fatalf("unexpected field %s in %v", k, m)
		}
	}
	waitStatus(t, a, cid, "sent")

	// a low alert: no sound; no click link of its own → the alert's page
	if err := a.s.send(context.Background(), a.ids["Alice"], &store.Channel{Kind: "pockethub"}, msgFor(&store.Alert{ID: 77, Title: "Low", Priority: 2, SourceName: "Disks"}, a.s.alertLink(77))); err != nil {
		t.Fatal(err)
	}
	if got := f.wait(t, 2); got[1]["sound"] != false || got[1]["url"] != "http://alerts.test/alerts/77" || got[1]["id"] != "77" || got[1]["thread"] != "Disks" {
		t.Fatalf("low alert = %v", got[1])
	}

	// the test button
	if r := a.ok(a.alice, "POST", fmt.Sprintf("/api/v1/channels/%d/test", cid), nil); r["ok"] != true {
		t.Fatalf("test = %v", r)
	}
	if got := f.wait(t, 3); !strings.HasSuffix(got[2]["title"].(string), "Test from Pocket Alerts") || got[2]["source"] != "Pocket Alerts" || got[2]["id"] != nil {
		t.Fatalf("test body = %v", got[2])
	}

	// nobody has notifications on: a refusal, not a retry
	f.set(200, `{"devices":0,"sent":0,"failed":0,"gone":0}`)
	r := a.ok(a.alice, "POST", fmt.Sprintf("/api/v1/channels/%d/test", cid), nil)
	if r["ok"] != false || !strings.Contains(r["error"].(string), "No Pocket Hub device has notifications on") {
		t.Fatalf("devices 0 = %v", r)
	}
	a.post("/"+key, "x", map[string]string{"Title": "No devices", "Priority": "4"})
	f.wait(t, 5)
	waitStatus(t, a, cid, "failed")

	// every device failed: tried again later, with Core's reason
	f.set(200, `{"devices":1,"sent":0,"failed":1,"gone":0,"reason":"no Apple push key on the relay yet"}`)
	a.post("/"+key, "x", map[string]string{"Title": "Relay down", "Priority": "4"})
	f.wait(t, 6)
	d := waitStatus(t, a, cid, "retrying")
	if !strings.Contains(d.Error, "no Apple push key on the relay yet") {
		t.Fatalf("retry error = %q", d.Error)
	}

	// Core 5xx: tried again
	f.set(502, `{"error":"bad gateway"}`)
	a.post("/"+key, "x", map[string]string{"Title": "Core down", "Priority": "4"})
	f.wait(t, 7)
	waitStatus(t, a, cid, "retrying")

	// an old Core: no such route
	f.set(404, `{"error":"not found"}`)
	r = a.ok(a.alice, "POST", fmt.Sprintf("/api/v1/channels/%d/test", cid), nil)
	if r["ok"] != false || r["error"] != "Pocket Core is too old for Pocket Hub notifications" {
		t.Fatalf("404 = %v", r)
	}
	a.post("/"+key, "x", map[string]string{"Title": "Old core", "Priority": "4"})
	f.wait(t, 9)
	waitStatus(t, a, cid, "failed")

	// not allowed to use Alerts in Core
	f.set(403, `{"error":"not in this app's audience"}`)
	r = a.ok(a.alice, "POST", fmt.Sprintf("/api/v1/channels/%d/test", cid), nil)
	if r["ok"] != false || !strings.Contains(r["error"].(string), "may not be allowed to use Pocket Alerts") || !strings.Contains(r["error"].(string), "audience") {
		t.Fatalf("403 = %v", r)
	}
	f.set(401, `{"error":"invalid token"}`)
	if err := a.s.send(context.Background(), a.ids["Alice"], &store.Channel{Kind: "pockethub"}, outMsg{Title: "x"}); err == nil || retryable(err) || !strings.Contains(err.Error(), "app token") {
		t.Fatalf("401 = %v", err)
	}
}

func waitStatus(t *testing.T, a *app, cid int64, want string) store.Delivery {
	t.Helper()
	var last []store.Delivery
	for i := 0; i < 100; i++ {
		last, _ = a.st.RecentDeliveries(context.Background(), a.ids["Alice"], cid, 1)
		if len(last) == 1 && last[0].Status == want {
			return last[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("last delivery = %+v, want %s", last, want)
	return store.Delivery{}
}

func TestPocketHubNeedsCore(t *testing.T) {
	a := newApp(t, nil)
	if m := a.expect(400, a.alice, "POST", "/api/v1/channels", map[string]any{"kind": "pockethub"}); !strings.Contains(fmt.Sprint(m["error"]), "Pocket Core") {
		t.Fatalf("without Core = %v", m)
	}
	if r := a.ok(a.alice, "POST", "/api/v1/channels/test", map[string]any{"kind": "pockethub"}); r["ok"] != false {
		t.Fatalf("test without Core = %v", r)
	}
}

func TestPushOutcome(t *testing.T) {
	cases := []struct {
		r    pushResult
		ok   bool
		temp bool
	}{
		{pushResult{Devices: 2, Sent: 1, Failed: 1}, true, false},
		{pushResult{Devices: 0}, false, false},
		{pushResult{Devices: 1, Failed: 1, Reason: "x"}, false, true},
		{pushResult{Devices: 1, Gone: 1}, false, false},
	}
	for _, c := range cases {
		err := pushOutcome(c.r)
		if (err == nil) != c.ok || (err != nil && retryable(err) != c.temp) {
			t.Errorf("%+v → %v", c.r, err)
		}
	}
}
