package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-alerts/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// ---- alerts from the other Pocket apps, through Pocket Core ----
//
// Some Pocket apps say on Core's event bus when something needs a person:
// a server alert, a failed backup, a document that could not be read. A
// person switches an app on under Sources (it becomes an "app" source) and
// allows the Alerts → <app> link (events:read) in Core; Core then streams
// those events to Alerts. A "resolved" event closes the alert it resolves.

// alertTopic is an event that becomes an alert.
type alertTopic struct {
	topic    string
	title    string
	priority int
	tags     []string
	resolves string // the topic whose alert this one closes
}

// eventApp is a Pocket app whose events can become alerts.
type eventApp struct {
	slug, name, icon, about string
	topics                  []alertTopic
}

var eventApps = []eventApp{
	{"monitoring", "Pocket Monitoring", "activity", "Server alerts: a disk filling up, a container that crashed, a server offline — and when they clear.", []alertTopic{
		{topic: "monitoring.alert", title: "Server alert", priority: 4, tags: []string{"rotating_light"}},
		{topic: "monitoring.resolved", title: "Server alert cleared", priority: 2, tags: []string{"white_check_mark"}, resolves: "monitoring.alert"},
	}},
	{"backups", "Pocket Backups", "archive", "A backup that failed.", []alertTopic{
		{topic: "backups.run.failed", title: "A backup failed", priority: 4, tags: []string{"x"}},
	}},
	{"documents", "Pocket Documents", "file", "A document that could not be read.", []alertTopic{
		{topic: "documents.failed", title: "A document could not be read", priority: 3, tags: []string{"page_facing_up"}},
	}},
	{"subtitles", "Pocket Subtitles", "text", "Subtitles that could not be synced.", []alertTopic{
		{topic: "subtitles.failed", title: "Subtitles could not be synced", priority: 3, tags: []string{"speech_balloon"}},
	}},
	{"ai", "Pocket AI", "sparkles", "Pocket AI waiting for your answer, and scheduled runs that need you.", []alertTopic{
		{topic: "ai.needs_input", title: "Pocket AI needs your answer", priority: 4, tags: []string{"speech_balloon"}},
		{topic: "ai.schedule_attention", title: "A scheduled AI run needs you", priority: 4, tags: []string{"robot"}},
	}},
}

func eventAppBySlug(slug string) (eventApp, bool) {
	for _, a := range eventApps {
		if a.slug == slug {
			return a, true
		}
	}
	return eventApp{}, false
}

func topicOf(topic string) (eventApp, alertTopic, bool) {
	slug, _, _ := strings.Cut(topic, ".")
	a, ok := eventAppBySlug(slug)
	if !ok {
		return a, alertTopic{}, false
	}
	for _, t := range a.topics {
		if t.topic == topic {
			return a, t, true
		}
	}
	return a, alertTopic{}, false
}

const cursorKey = "events:cursor"

func (s *Server) setStream(ok bool, err string) {
	s.streamMu.Lock()
	s.streamOK, s.streamErr = ok, err
	s.streamMu.Unlock()
}

func (s *Server) streamStatus() (bool, string) {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	return s.streamOK, s.streamErr
}

// listen follows Core's bus until ctx ends, reconnecting with backoff.
func (s *Server) listen(ctx context.Context) {
	wait := 2 * time.Second
	for ctx.Err() == nil {
		if s.core.Token() == "" { // not registered yet
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		err := s.streamOnce(ctx, s.cursor(ctx))
		if ctx.Err() != nil {
			return
		}
		msg := "the stream ended"
		if err != nil {
			msg = err.Error()
		}
		s.setStream(false, msg)
		s.log.Debug("pocket core: event stream closed, reconnecting", "err", msg, "in", wait.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if wait < time.Minute {
			wait *= 2
		}
		if err == nil {
			wait = 2 * time.Second
		}
	}
}

// cursor is the last event id seen; on the very first start the newest
// event on the bus, so old history does not flood the inbox.
func (s *Server) cursor(ctx context.Context) int64 {
	if v, _, ok := s.store.CacheGet(ctx, cursorKey); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	var last int64
	if evs, err := s.core.Events(ctx, "", 0, 1); err == nil && len(evs) > 0 {
		last = evs[0].ID
	}
	_ = s.store.CachePut(ctx, cursorKey, strconv.FormatInt(last, 10))
	return last
}

func (s *Server) streamOnce(ctx context.Context, after int64) error {
	topics := []string{}
	for _, a := range eventApps {
		for _, t := range a.topics {
			topics = append(topics, t.topic)
		}
	}
	q := url.Values{"topics": {strings.Join(topics, ",")}}
	if after > 0 {
		q.Set("after", strconv.FormatInt(after, 10))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.CoreURL+"/api/v1/events/stream?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.core.Token())
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", s.cfg.UserAgent)
	if after > 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatInt(after, 10))
	}
	res, err := (&http.Client{}).Do(req) // long-lived: no timeout
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return fmt.Errorf("Pocket Core answered %d %s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	s.setStream(true, "")
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var data strings.Builder
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if data.Len() > 0 {
				var e core.Event
				if json.Unmarshal([]byte(data.String()), &e) == nil && e.ID > 0 {
					s.handleEvent(ctx, e)
					_ = s.store.CachePut(ctx, cursorKey, strconv.FormatInt(e.ID, 10))
				}
				data.Reset()
			}
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	return sc.Err()
}

func eventOwner(p map[string]any) string {
	for _, k := range []string{"user_email", "owner_email", "user"} {
		if v, ok := p[k].(string); ok && strings.Contains(v, "@") {
			return strings.ToLower(strings.TrimSpace(v))
		}
	}
	return ""
}

func payloadStr(p map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := p[k].(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		}
	}
	return ""
}

// handleEvent turns an app's event into an alert for the person it is about,
// when they switched that app on under Sources.
func (s *Server) handleEvent(ctx context.Context, e core.Event) {
	app, t, ok := topicOf(e.Topic)
	if !ok {
		return
	}
	var p map[string]any
	if json.Unmarshal(e.Payload, &p) != nil {
		return
	}
	email := eventOwner(p)
	if email == "" {
		return
	}
	u, err := s.store.GetUserByEmail(ctx, email)
	if err != nil {
		return
	}
	src, err := s.store.SourceByApp(ctx, u.ID, app.slug)
	if err != nil || !src.Enabled {
		return
	}
	_ = s.store.SetSourceStatus(ctx, src.ID, "ok", "")
	at, _ := time.Parse(time.RFC3339Nano, e.CreatedAt)
	if !at.IsZero() && time.Since(at) > 6*time.Hour {
		return // an old event replayed after a long break
	}
	title := firstNonEmpty(payloadStr(p, "title"), t.title)
	if subj := payloadStr(p, "server", "name", "target"); subj != "" && !strings.Contains(title, subj) {
		title += ": " + subj
	}
	body := payloadStr(p, "message", "error", "reason", "detail", "body", "summary")
	if body == "" {
		body = e.Subject
	}
	pr := t.priority
	switch strings.ToLower(payloadStr(p, "severity", "level")) {
	case "critical", "error":
		pr = 5
	case "warning":
		pr = 4
	}
	ref := payloadStr(p, "alert_id", "run_id", "id")
	ext := fmt.Sprintf("core:%d", e.ID)
	in := incoming{Title: title, Body: body, Priority: pr, Tags: t.tags, Click: payloadStr(p, "url", "link"), ExtID: ext, At: at}
	if in.Click == "" {
		if r, err := s.core.Resolve(ctx, app.slug); err == nil {
			in.Click = r.PublicURL
		}
	}
	a, _, err := s.receive(ctx, u.ID, src, in)
	if err != nil {
		s.log.Warn("pocket core: event to alert failed", "topic", e.Topic, "err", err)
		return
	}
	if ref == "" {
		return
	}
	key := "ref:" + app.slug + ":" + ref
	if t.resolves != "" {
		// close the alert this one resolves
		if v, _, ok := s.store.CacheGet(ctx, key); ok {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil {
				_, _ = s.store.MarkAlerts(ctx, u.ID, []int64{id}, "done", true)
				s.live.send(u.ID, liveEvent{Type: "alert", ID: id})
			}
			_ = s.store.CacheDelete(ctx, key)
		}
		return
	}
	if a != nil {
		_ = s.store.CachePut(ctx, key, strconv.FormatInt(a.ID, 10))
	}
}

// appSourceStatus says, for the Sources page, whether Core lets the events
// of an app through for this person.
func (s *Server) appSourceStatus(ctx context.Context, email, slug string) (bool, string) {
	if !s.core.Configured() {
		return false, "Needs Pocket Core"
	}
	on, reason := s.core.LinkOn(ctx, email, slug, scopeEventsRead)
	if on {
		return true, ""
	}
	if t := linkReason("Pocket "+strings.ToUpper(slug[:1])+slug[1:], reason); t != "" {
		return false, t
	}
	return false, reason
}

// ensureAppSource switches a Pocket app on or off as a source.
func (s *Server) ensureAppSource(ctx context.Context, uid int64, slug string, on bool) (*store.Source, error) {
	app, ok := eventAppBySlug(slug)
	if !ok {
		return nil, store.ErrNotFound
	}
	src, err := s.store.SourceByApp(ctx, uid, slug)
	if err == store.ErrNotFound {
		if !on {
			return nil, nil
		}
		src = &store.Source{Name: app.name, Kind: "app", AppSlug: slug, Enabled: true}
		return src, s.store.CreateSource(ctx, uid, src)
	}
	if err != nil {
		return nil, err
	}
	src.Enabled = on
	return src, s.store.UpdateSource(ctx, uid, src)
}
