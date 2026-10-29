package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// ---- live updates for open pages ----
//
// The inbox listens on /api/live (server-sent events): "alert" when one of
// the person's alerts arrives or changes, "refresh" when many changed.
// Only ever the person's own events.

type liveEvent struct {
	Type string `json:"type"` // alert | refresh | source
	ID   int64  `json:"id,omitempty"`
	New  bool   `json:"new,omitempty"`
}

type liveHub struct {
	mu   sync.Mutex
	subs map[int64]map[chan liveEvent]bool
}

func newLiveHub() *liveHub { return &liveHub{subs: map[int64]map[chan liveEvent]bool{}} }

func (h *liveHub) subscribe(uid int64) chan liveEvent {
	ch := make(chan liveEvent, 32)
	h.mu.Lock()
	if h.subs[uid] == nil {
		h.subs[uid] = map[chan liveEvent]bool{}
	}
	h.subs[uid][ch] = true
	h.mu.Unlock()
	return ch
}

func (h *liveHub) unsubscribe(uid int64, ch chan liveEvent) {
	h.mu.Lock()
	delete(h.subs[uid], ch)
	if len(h.subs[uid]) == 0 {
		delete(h.subs, uid)
	}
	h.mu.Unlock()
}

// send never blocks: a page that is not keeping up gets "refresh" later.
func (h *liveHub) send(uid int64, e liveEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[uid] {
		select {
		case ch <- e:
		default:
		}
	}
}

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}
	uid := actorID(r.Context())
	ch := s.live.subscribe(uid)
	defer s.live.unsubscribe(uid, ch)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, "retry: 5000\n: hello\n\n")
	fl.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.bg.Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case e := <-ch:
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		}
	}
}
