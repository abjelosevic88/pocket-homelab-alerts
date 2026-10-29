package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-alerts/internal/store"
)

// ---- following topics on an ntfy server ----
//
// A "ntfy" source keeps a connection open to <server>/<topics>/json and
// takes every message that arrives, so whatever already posts to ntfy
// shows up here without changing anything. The id of the last message is
// kept: after a restart the follower asks for everything since then, so
// nothing is lost (ntfy keeps messages for 12 hours by default). A quiet
// connection (no keepalive for two minutes) is dropped and opened again.

type follower struct {
	cancel context.CancelFunc
	sig    string
}

func followSig(x *store.Source) string {
	return fmt.Sprintf("%s|%s|%s|%t", x.FollowURL, x.FollowTopics, x.FollowToken, x.Enabled)
}

// syncFollowers starts, restarts and stops followers to match the sources.
func (s *Server) syncFollowers(ctx context.Context) {
	if s.cfg.DemoData {
		return // demo followers point nowhere
	}
	list, err := s.store.SourcesOfKind(ctx, "ntfy")
	if err != nil {
		return
	}
	s.followMu.Lock()
	defer s.followMu.Unlock()
	want := map[int64]bool{}
	for i := range list {
		x := list[i]
		if !x.Enabled || x.FollowURL == "" || x.FollowTopics == "" {
			continue
		}
		want[x.ID] = true
		sig := followSig(&x)
		if f, ok := s.followers[x.ID]; ok {
			if f.sig == sig {
				continue
			}
			f.cancel()
		}
		fctx, cancel := context.WithCancel(s.bg)
		s.followers[x.ID] = &follower{cancel: cancel, sig: sig}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.follow(fctx, x.ID)
		}()
	}
	for id, f := range s.followers {
		if !want[id] {
			f.cancel()
			delete(s.followers, id)
		}
	}
}

func (s *Server) follow(ctx context.Context, id int64) {
	wait := 2 * time.Second
	for ctx.Err() == nil {
		src, err := s.findSource(ctx, id)
		if err != nil || !src.Enabled {
			return
		}
		got, err := s.followOnce(ctx, src)
		if ctx.Err() != nil {
			return
		}
		note := "the connection ended"
		if err != nil {
			note = err.Error()
		}
		_ = s.store.SetSourceStatus(ctx, id, "error", note)
		s.live.send(src.UserID, liveEvent{Type: "source", ID: id})
		if got {
			wait = 2 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if wait < 5*time.Minute {
			wait *= 2
		}
	}
}

// findSource reads a source without knowing its person (background work).
func (s *Server) findSource(ctx context.Context, id int64) (*store.Source, error) {
	list, err := s.store.SourcesOfKind(ctx, "ntfy")
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i], nil
		}
	}
	return nil, store.ErrNotFound
}

// ntfyMessage is one line of ntfy's JSON stream.
type ntfyMessage struct {
	ID       string   `json:"id"`
	Time     int64    `json:"time"`
	Event    string   `json:"event"`
	Topic    string   `json:"topic"`
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	Priority int      `json:"priority"`
	Tags     []string `json:"tags"`
	Click    string   `json:"click"`
}

// followOnce holds one connection; got = it connected and heard something.
func (s *Server) followOnce(ctx context.Context, src *store.Source) (bool, error) {
	topics := []string{}
	for _, t := range strings.Split(src.FollowTopics, ",") {
		if t = store.CleanTopic(t); t != "" {
			topics = append(topics, t)
		}
	}
	if len(topics) == 0 {
		return false, fmt.Errorf("no topics to follow")
	}
	q := url.Values{}
	if src.FollowCursor != "" {
		q.Set("since", src.FollowCursor)
	}
	target := strings.TrimRight(src.FollowURL, "/") + "/" + strings.Join(topics, ",") + "/json"
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, target, nil)
	if err != nil {
		return false, fmt.Errorf("the server address is not valid")
	}
	req.Header.Set("User-Agent", s.cfg.UserAgent)
	if tok := src.FollowToken; tok != "" {
		if user, pass, ok := strings.Cut(tok, ":"); ok {
			req.SetBasicAuth(user, pass)
		} else {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	res, err := s.outClient(0, s.localOK(ctx, src.UserID)).Do(req)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return false, fmt.Errorf("the ntfy server answered %d %s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	// no line (not even a keepalive, every 45 s by default) for two minutes:
	// the connection is dead even if TCP has not noticed
	idle := time.AfterFunc(2*time.Minute, cancel)
	defer idle.Stop()
	_ = s.store.SetSourceStatus(ctx, src.ID, "ok", "")
	s.live.send(src.UserID, liveEvent{Type: "source", ID: src.ID})
	got := false
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		idle.Reset(2 * time.Minute)
		got = true
		var m ntfyMessage
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.Event != "message" {
			continue
		}
		at := time.Unix(m.Time, 0)
		if m.Time == 0 {
			at = time.Time{}
		}
		in := incoming{Title: m.Title, Body: m.Message, Priority: m.Priority, Tags: m.Tags, Click: m.Click, ExtID: "ntfy:" + m.ID, At: at}
		if len(topics) > 1 && m.Topic != "" {
			in.Tags = append(in.Tags, m.Topic)
		}
		if s.limits.allow(src.ID) {
			if _, _, err := s.receive(ctx, src.UserID, src, in); err != nil {
				s.log.Warn("ntfy follower: receive failed", "source", src.ID, "err", err)
			}
		}
		if m.ID != "" {
			src.FollowCursor = m.ID
			_ = s.store.SetSourceCursor(ctx, src.ID, m.ID)
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		if cctx.Err() != nil {
			return got, fmt.Errorf("the server went quiet (no keepalive for two minutes)")
		}
		return got, err
	}
	return got, nil
}
