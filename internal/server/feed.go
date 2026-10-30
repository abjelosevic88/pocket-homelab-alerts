package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-alerts/internal/store"
)

// ---- reading alerts the ntfy way ----
//
// Things that used to subscribe to an ntfy server — a dashboard's
// notification drawer, a phone relay — keep working against Pocket Alerts:
// GET /<topics>/json and /<topics>/sse answer like ntfy (poll=1, since=
// all | 12h | <message id> | <unix time>), with every alert of the sources
// whose feed topic is asked for. Like ntfy's open topics there is no token:
// only admins put sources on a feed, and the box is meant to be reached on
// a private network.

type feedHub struct {
	mu   sync.Mutex
	subs map[chan store.FeedItem]map[string]bool
}

func newFeedHub() *feedHub { return &feedHub{subs: map[chan store.FeedItem]map[string]bool{}} }

func (h *feedHub) subscribe(topics []string) chan store.FeedItem {
	want := map[string]bool{}
	for _, t := range topics {
		want[t] = true
	}
	ch := make(chan store.FeedItem, 64)
	h.mu.Lock()
	h.subs[ch] = want
	h.mu.Unlock()
	return ch
}

func (h *feedHub) unsubscribe(ch chan store.FeedItem) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

func (h *feedHub) send(it store.FeedItem) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch, want := range h.subs {
		if !want[it.FeedTopic] {
			continue
		}
		select {
		case ch <- it:
		default:
		}
	}
}

// ntfyEvent is one line of ntfy's JSON stream.
type ntfyEvent struct {
	ID       string   `json:"id"`
	Time     int64    `json:"time"`
	Event    string   `json:"event"`
	Topic    string   `json:"topic"`
	Title    string   `json:"title,omitempty"`
	Message  string   `json:"message,omitempty"`
	Priority int      `json:"priority,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Click    string   `json:"click,omitempty"`
}

func feedEvent(it store.FeedItem) ntfyEvent {
	t, _ := time.Parse(time.RFC3339Nano, it.ReceivedAt)
	return ntfyEvent{ID: "a" + strconv.FormatInt(it.ID, 10), Time: t.Unix(), Event: "message", Topic: it.FeedTopic,
		Title: it.Title, Message: it.Body, Priority: it.Priority, Tags: it.Tags, Click: it.ClickURL}
}

// parseSince turns ntfy's since= into an alert id or a time. An id this
// server never gave out (an old ntfy id) means "from now": a subscriber
// switching over is not flooded with history it already has.
func parseSince(v string) (int64, string) {
	now := time.Now()
	switch {
	case v == "" || v == "all":
		if v == "" {
			return 0, now.UTC().Format(time.RFC3339Nano)
		}
		return 0, "0"
	case strings.HasPrefix(v, "a"):
		if n, err := strconv.ParseInt(v[1:], 10, 64); err == nil && n > 0 {
			return n, ""
		}
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return 0, now.Add(-d).UTC().Format(time.RFC3339Nano)
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 1e9 {
		return 0, time.Unix(n, 0).UTC().Format(time.RFC3339Nano)
	}
	return 0, now.UTC().Format(time.RFC3339Nano)
}

func feedTopics(raw string) []string {
	out := []string{}
	for _, t := range strings.Split(raw, ",") {
		if c := store.CleanTopic(t); c != "" && c == strings.TrimSpace(t) {
			out = append(out, c)
		}
	}
	return out
}

func (s *Server) handleFeedJSON(w http.ResponseWriter, r *http.Request) { s.feed(w, r, false) }
func (s *Server) handleFeedSSE(w http.ResponseWriter, r *http.Request)  { s.feed(w, r, true) }

func (s *Server) feed(w http.ResponseWriter, r *http.Request, sse bool) {
	topics := feedTopics(chi.URLParam(r, "topics"))
	if len(topics) == 0 {
		writeErr(w, http.StatusBadRequest, "say which topics")
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if sse {
		w.Header().Set("Content-Type", "text/event-stream")
	} else {
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fl, _ := w.(http.Flusher)
	write := func(e ntfyEvent) {
		b, _ := json.Marshal(e)
		switch {
		case !sse:
			fmt.Fprintf(w, "%s\n", b)
		case e.Event == "message":
			fmt.Fprintf(w, "data: %s\n\n", b)
		default:
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Event, b)
		}
		if fl != nil {
			fl.Flush()
		}
	}
	poll := queryBool(r, "poll")
	var ch chan store.FeedItem
	if !poll {
		// subscribe before reading the backlog, so nothing falls in between
		ch = s.feeds.subscribe(topics)
		defer s.feeds.unsubscribe(ch)
		write(ntfyEvent{ID: "open", Time: time.Now().Unix(), Event: "open", Topic: strings.Join(topics, ",")})
	}
	afterID, since := parseSince(r.URL.Query().Get("since"))
	var last int64
	if since != "" || afterID > 0 {
		backlog, err := s.store.FeedAlerts(r.Context(), topics, afterID, since, 1000)
		if err == nil {
			for _, it := range backlog {
				write(feedEvent(it))
				last = it.ID
			}
		}
	}
	if poll {
		return
	}
	keep := time.NewTicker(45 * time.Second)
	defer keep.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.bg.Done():
			return
		case <-keep.C:
			write(ntfyEvent{ID: "keepalive", Time: time.Now().Unix(), Event: "keepalive", Topic: strings.Join(topics, ",")})
		case it := <-ch:
			if it.ID <= last {
				continue
			}
			write(feedEvent(it))
			last = it.ID
		}
	}
}
