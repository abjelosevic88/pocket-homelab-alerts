package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-alerts/internal/store"
)

// ---- how alerts come in ----
//
// Three shapes, so most things can send here without changes:
//
//   - Apprise API: POST /notify/<key> with {title, body, type} (JSON or a
//     form) — what Pocket apps, scripts and anything that speaks to an
//     Apprise gateway send. <key> is a source's secret.
//   - ntfy: POST or PUT /<topic> with the text as the body and Title,
//     Priority, Tags, Click headers (or ?title=…), and POST / with ntfy's
//     JSON — what Sonarr/Radarr, Uptime Kuma, Apprise's ntfy:// and the
//     ntfy CLI send. When <topic> is a source's key nothing else is needed;
//     any other topic needs the person's API token (Bearer, or as the
//     password) and becomes a source of its own on first use.
//   - Webhook: POST /hook/<key> with JSON {title, message, priority, tags,
//     click} or plain text.

const ingestMax = 1 << 20

// ingestLimit allows a source this many alerts a minute (a loop in a
// script must not flood the phone or the database).
const ingestLimit = 300

type rateWindow struct {
	mu     sync.Mutex
	minute int64
	counts map[int64]int
}

func (rw *rateWindow) allow(id int64) bool {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	m := time.Now().Unix() / 60
	if m != rw.minute {
		rw.minute, rw.counts = m, map[int64]int{}
	}
	rw.counts[id]++
	return rw.counts[id] <= ingestLimit
}

// parsePriority understands 1–5 and ntfy's and Apprise's words.
func parsePriority(v string) int {
	v = strings.ToLower(strings.TrimSpace(v))
	if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 5 {
		return n
	}
	switch v {
	case "min", "lowest":
		return 1
	case "low":
		return 2
	case "default", "normal", "info", "success", "moderate":
		return 3
	case "high", "warning", "warn":
		return 4
	case "max", "urgent", "emergency", "critical", "failure", "error":
		return 5
	}
	return 0
}

// appriseTypes: Apprise's message types → importance. A failure is high,
// not urgent, so it does not break quiet hours unless the person says so.
var appriseTypes = map[string]int{"info": 3, "success": 3, "warning": 4, "failure": 4}

func (s *Server) ingestSource(w http.ResponseWriter, r *http.Request, key string) (*store.Source, bool) {
	src, err := s.store.SourceByKey(r.Context(), key)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no source has this address")
		return nil, false
	}
	return src, true
}

func (s *Server) take(w http.ResponseWriter, r *http.Request, src *store.Source, in incoming) (*store.Alert, bool) {
	if !src.Enabled {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "ignored": "the source is switched off"})
		return nil, false
	}
	if !s.limits.allow(src.ID) {
		writeErr(w, http.StatusTooManyRequests, fmt.Sprintf("more than %d alerts a minute from this source", ingestLimit))
		return nil, false
	}
	a, _, err := s.receive(r.Context(), src.UserID, src, in)
	if err != nil {
		s.fail(w, err, "receive")
		return nil, false
	}
	return a, true
}

// handleAppriseNotify is POST /notify/{key}: Apprise API's stateful notify.
func (s *Server) handleAppriseNotify(w http.ResponseWriter, r *http.Request) {
	src, ok := s.ingestSource(w, r, chi.URLParam(r, "key"))
	if !ok {
		return
	}
	var in struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		Type  string `json:"type"`
		Tag   any    `json:"tag"`
	}
	if isJSON(r) {
		if err := json.NewDecoder(io.LimitReader(r.Body, ingestMax)).Decode(&in); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid json")
			return
		}
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, ingestMax)
		if err := r.ParseForm(); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid form")
			return
		}
		in.Title, in.Body, in.Type = r.PostForm.Get("title"), r.PostForm.Get("body"), r.PostForm.Get("type")
		if t := r.PostForm.Get("tag"); t != "" {
			in.Tag = t
		}
	}
	if strings.TrimSpace(in.Body) == "" && strings.TrimSpace(in.Title) == "" {
		writeErr(w, http.StatusBadRequest, "body is required")
		return
	}
	p := appriseTypes[strings.ToLower(strings.TrimSpace(in.Type))]
	tags := anyTags(in.Tag)
	switch strings.ToLower(in.Type) {
	case "success":
		tags = append([]string{"white_check_mark"}, tags...)
	case "warning":
		tags = append([]string{"warning"}, tags...)
	case "failure":
		tags = append([]string{"rotating_light"}, tags...)
	}
	if _, ok := s.take(w, r, src, incoming{Title: in.Title, Body: in.Body, Priority: p, Tags: tags}); ok {
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	}
}

// handleHook is POST /hook/{key}: any JSON or plain text.
func (s *Server) handleHook(w http.ResponseWriter, r *http.Request) {
	src, ok := s.ingestSource(w, r, chi.URLParam(r, "key"))
	if !ok {
		return
	}
	in, err := readMessage(r, true)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if a, ok := s.take(w, r, src, in); ok {
		writeJSON(w, http.StatusOK, hookAnswer(a))
	}
}

func hookAnswer(a *store.Alert) map[string]any {
	if a == nil {
		return map[string]any{"ok": true, "kept": false}
	}
	return map[string]any{"ok": true, "kept": true, "id": a.ID, "state": a.State, "count": a.Count}
}

// handleNtfyPublish is POST|PUT /{topic} and POST / (JSON with "topic").
func (s *Server) handleNtfyPublish(w http.ResponseWriter, r *http.Request) {
	topic := chi.URLParam(r, "topic")
	var in incoming
	var err error
	if topic == "" {
		var j ntfyJSON
		if err := json.NewDecoder(io.LimitReader(r.Body, ingestMax)).Decode(&j); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		topic = j.Topic
		in = incoming{Title: j.Title, Body: j.Message, Priority: j.Priority, Tags: j.Tags, Click: j.Click}
	} else if in, err = readMessage(r, false); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if topic == "" {
		writeErr(w, http.StatusBadRequest, "topic is required")
		return
	}
	src, err := s.ntfySource(r, topic)
	if err != nil {
		code := http.StatusUnauthorized
		if errors.Is(err, store.ErrNotFound) {
			code = http.StatusNotFound
		}
		writeErr(w, code, err.Error())
		return
	}
	a, ok := s.take(w, r, src, in)
	if !ok {
		return
	}
	out := map[string]any{"event": "message", "topic": topic, "time": time.Now().Unix(), "message": in.Body}
	if a != nil {
		out["id"] = strconv.FormatInt(a.ID, 10)
		if in.Title != "" {
			out["title"] = in.Title
		}
		out["priority"] = a.Priority
	}
	writeJSON(w, http.StatusOK, out)
}

type ntfyJSON struct {
	Topic    string   `json:"topic"`
	Message  string   `json:"message"`
	Title    string   `json:"title"`
	Priority int      `json:"priority"`
	Tags     []string `json:"tags"`
	Click    string   `json:"click"`
}

var errNeedToken = errors.New("this topic needs your Pocket Alerts API token (Settings → API & shortcuts) as a Bearer token or the password; or use a source's own address")

// ntfySource finds the source an ntfy-style publish goes to: a source's key
// as the topic, or a topic of the person whose token came with it.
func (s *Server) ntfySource(r *http.Request, topic string) (*store.Source, error) {
	ctx := r.Context()
	if src, err := s.store.SourceByKey(ctx, topic); err == nil {
		return src, nil
	}
	clean := store.CleanTopic(topic)
	if clean == "" || clean != topic {
		return nil, store.ErrNotFound
	}
	u, err := s.publisher(r)
	if err != nil {
		return nil, err
	}
	if src, err := s.store.SourceByTopic(ctx, u.ID, clean); err == nil {
		return src, nil
	}
	src := &store.Source{Name: clean, Kind: "push", Topic: clean, Enabled: true}
	if err := s.store.CreateSource(ctx, u.ID, src); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return s.store.SourceByTopic(ctx, u.ID, clean)
		}
		return nil, err
	}
	s.live.send(u.ID, liveEvent{Type: "source", ID: src.ID})
	return src, nil
}

// publisher is the person behind an ntfy-style publish: an API token as
// Bearer, as the Basic password (or user), or in ntfy's ?auth= parameter.
func (s *Server) publisher(r *http.Request) (*store.User, error) {
	ctx := r.Context()
	h := r.Header.Get("Authorization")
	if a := r.URL.Query().Get("auth"); h == "" && a != "" {
		if b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(a, "=")); err == nil {
			h = string(b)
		}
	}
	tok := ""
	switch {
	case strings.HasPrefix(strings.ToLower(h), "bearer "):
		tok = strings.TrimSpace(h[7:])
	case strings.HasPrefix(strings.ToLower(h), "basic "):
		if b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(h[6:])); err == nil {
			user, pass, _ := strings.Cut(string(b), ":")
			tok = firstNonEmpty(pass, user)
		}
	}
	if tok == "" {
		if s.cfg.AuthMode == "none" {
			return s.firstOrSystemUser(ctx, "admin@localhost", "Admin")
		}
		return nil, errNeedToken
	}
	u, err := s.store.GetUserByAPIToken(ctx, tok)
	if err != nil {
		return nil, errNeedToken
	}
	return u, nil
}

// readMessage reads ntfy-style headers / query parameters and the body as
// the text; with asJSON a JSON body is read as {title, message, …}.
func readMessage(r *http.Request, asJSON bool) (incoming, error) {
	q := r.URL.Query()
	pick := func(names ...string) string {
		for _, n := range names {
			if v := r.Header.Get(n); v != "" {
				return v
			}
			if v := q.Get(strings.ToLower(n)); v != "" {
				return v
			}
		}
		return ""
	}
	in := incoming{
		Title:    pick("X-Title", "Title", "ti", "t"),
		Priority: parsePriority(pick("X-Priority", "Priority", "prio", "p")),
		Click:    pick("X-Click", "Click"),
	}
	if t := pick("X-Tags", "Tags", "Tag", "ta"); t != "" {
		in.Tags = strings.Split(t, ",")
	}
	in.Title = decodeHeader(in.Title)
	body, err := io.ReadAll(io.LimitReader(r.Body, ingestMax))
	if err != nil {
		return in, errors.New("could not read the message")
	}
	if asJSON && isJSON(r) && len(body) > 0 {
		var j map[string]any
		if err := json.Unmarshal(body, &j); err != nil {
			return in, errors.New("invalid json")
		}
		str := func(keys ...string) string {
			for _, k := range keys {
				if v, ok := j[k]; ok {
					switch t := v.(type) {
					case string:
						if t != "" {
							return t
						}
					case float64:
						return strconv.FormatFloat(t, 'f', -1, 64)
					}
				}
			}
			return ""
		}
		in.Title = firstNonEmpty(str("title", "subject", "name"), in.Title)
		in.Body = str("message", "body", "text", "content", "description")
		if p := parsePriority(str("priority", "severity", "level", "type")); p > 0 {
			in.Priority = p
		}
		if t := anyTags(j["tags"]); len(t) > 0 {
			in.Tags = t
		}
		in.Click = firstNonEmpty(str("click", "url", "link"), in.Click)
		in.ExtID = str("id")
		if in.Title == "" && in.Body == "" {
			b, _ := json.MarshalIndent(j, "", "  ")
			in.Body = string(b)
		}
		return in, nil
	}
	in.Body = strings.TrimSpace(string(body))
	if m := pick("X-Message", "Message", "m"); in.Body == "" && m != "" {
		in.Body = m
	}
	if in.Body == "" && in.Title == "" {
		return in, errors.New("the message is empty")
	}
	return in, nil
}

// decodeHeader reads RFC 2047 encoded words (=?UTF-8?B?…?=), which some
// senders use for non-ASCII titles.
func decodeHeader(v string) string {
	if !strings.Contains(v, "=?") {
		return v
	}
	if d, err := new(mime.WordDecoder).DecodeHeader(v); err == nil {
		return d
	}
	return v
}

func anyTags(v any) []string {
	switch t := v.(type) {
	case string:
		return strings.Split(t, ",")
	case []any:
		out := []string{}
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func isJSON(r *http.Request) bool {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return ct == "application/json" || strings.HasSuffix(ct, "+json")
}

// ---- addresses people copy ----

// sourceAddresses are the ways to send to a source, ready to paste.
func (s *Server) sourceAddresses(src *store.Source) map[string]string {
	base := s.cfg.BaseURL
	if base == "" {
		base = "http://localhost:" + strconv.Itoa(s.cfg.Port)
	}
	u, _ := url.Parse(base)
	host := base
	scheme := "ntfy"
	if u != nil {
		host = u.Host
		if u.Scheme == "https" {
			scheme = "ntfys"
		}
	}
	return map[string]string{
		"apprise":     base + "/notify/" + src.PushKey,
		"hook":        base + "/hook/" + src.PushKey,
		"ntfy_server": base,
		"ntfy_topic":  src.PushKey,
		"ntfy_url":    base + "/" + src.PushKey,
		"apprise_url": scheme + "://" + host + "/" + src.PushKey,
		"curl":        "curl -H 'Title: Backup failed' -H 'Priority: high' -d 'The nightly backup could not reach the NAS.' " + base + "/" + src.PushKey,
	}
}

// testIncoming is what "Send a test" sends into a source.
func testIncoming() incoming {
	return incoming{Title: "A test from Pocket Alerts", Body: "If you see this on your phone, alerts from this source reach you.", Priority: 3, Tags: []string{"wave"}}
}

func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
