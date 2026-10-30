package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-alerts/internal/store"
)

// sourceView is a source as the API shows it: its addresses, last-week
// numbers, and the follow token masked.
func (s *Server) sourceView(ctx context.Context, x *store.Source, stats map[int64]store.SourceStat) map[string]any {
	out := map[string]any{
		"id": x.ID, "name": x.Name, "kind": x.Kind, "topic": x.Topic, "icon": x.Icon, "default_priority": x.DefaultPriority,
		"enabled": x.Enabled, "muted": x.Muted, "snoozed_until": nil, "follow_url": x.FollowURL, "follow_topics": x.FollowTopics,
		"follow_token": mask(x.FollowToken), "app_slug": x.AppSlug, "status": x.Status, "status_note": x.StatusNote,
		"open": x.Open, "feed_topic": x.FeedTopic,
		"last_alert_at": x.LastAlertAt, "created_at": x.CreatedAt,
	}
	if x.Snoozed(store.Now()) {
		out["snoozed_until"] = x.SnoozedUntil
	}
	if x.Kind == "push" {
		out["addresses"] = s.sourceAddresses(x)
	}
	if st, ok := stats[x.ID]; ok {
		out["week"] = st
	} else {
		out["week"] = store.SourceStat{SourceID: x.ID}
	}
	return out
}

func (s *Server) handleListSources(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	list, err := s.store.ListSources(ctx, uid)
	if err != nil {
		s.fail(w, err, "sources")
		return
	}
	stats, _ := s.store.SourceStats(ctx, uid, time.Now().AddDate(0, 0, -7).UTC().Format(time.RFC3339Nano))
	out := make([]map[string]any, 0, len(list))
	for i := range list {
		out = append(out, s.sourceView(ctx, &list[i], stats))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetSource(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	x, err := s.store.GetSource(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "source")
		return
	}
	stats, _ := s.store.SourceStats(ctx, uid, time.Now().AddDate(0, 0, -7).UTC().Format(time.RFC3339Nano))
	writeJSON(w, http.StatusOK, s.sourceView(ctx, x, stats))
}

// sourceInput is what a person can set on a source.
type sourceInput struct {
	Name            *string `json:"name"`
	Kind            string  `json:"kind"`
	Topic           *string `json:"topic"`
	Icon            *string `json:"icon"`
	DefaultPriority *int    `json:"default_priority"`
	Enabled         *bool   `json:"enabled"`
	Muted           *bool   `json:"muted"`
	FollowURL       *string `json:"follow_url"`
	FollowTopics    *string `json:"follow_topics"`
	FollowToken     *string `json:"follow_token"`
	Backfill        bool    `json:"backfill"`   // a new follower brings in the last 12 hours
	Open            *bool   `json:"open"`       // admins only
	FeedTopic       *string `json:"feed_topic"` // admins only
}

func (in sourceInput) apply(x *store.Source, admin bool) string {
	if in.Name != nil {
		x.Name = clip(strings.TrimSpace(*in.Name), 80)
	}
	if in.Icon != nil {
		x.Icon = clip(strings.TrimSpace(*in.Icon), 40)
	}
	if in.DefaultPriority != nil {
		if *in.DefaultPriority < 0 || *in.DefaultPriority > 5 {
			return "the importance is 1 to 5 (0 = as sent)"
		}
		x.DefaultPriority = *in.DefaultPriority
	}
	if in.Enabled != nil {
		x.Enabled = *in.Enabled
	}
	if in.Muted != nil {
		x.Muted = *in.Muted
	}
	if x.Kind == "push" && in.Topic != nil {
		t := store.CleanTopic(*in.Topic)
		if t != strings.TrimSpace(*in.Topic) {
			return "a topic has only letters, digits, - and _"
		}
		x.Topic = t
	}
	if x.Kind == "ntfy" {
		if in.FollowURL != nil {
			u := strings.TrimRight(strings.TrimSpace(*in.FollowURL), "/")
			if p, err := url.Parse(u); err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
				return "the ntfy server is an address like https://ntfy.sh"
			}
			x.FollowURL = u
		}
		if in.FollowTopics != nil {
			var ts []string
			for _, t := range strings.Split(*in.FollowTopics, ",") {
				if c := store.CleanTopic(t); c != "" {
					ts = append(ts, c)
				}
			}
			if len(ts) == 0 {
				return "say which topics to follow"
			}
			x.FollowTopics = strings.Join(ts, ",")
		}
		if in.FollowToken != nil && !strings.Contains(*in.FollowToken, "••") {
			x.FollowToken = strings.TrimSpace(*in.FollowToken)
		}
	}
	if (in.Open != nil || in.FeedTopic != nil) && !admin {
		return "only the box's admins can open a topic or put a source on a feed"
	}
	if in.Open != nil {
		if *in.Open && (x.Kind != "push" || x.Topic == "") {
			return "give the source a topic first; that name is its open address"
		}
		x.Open = *in.Open
	}
	if in.FeedTopic != nil {
		t := strings.TrimSpace(*in.FeedTopic)
		if t != "" && store.CleanTopic(t) != t {
			return "a feed topic has only letters, digits, - and _"
		}
		x.FeedTopic = t
	}
	if x.Name == "" {
		x.Name = firstNonEmpty(x.Topic, firstNonEmpty(x.FollowTopics, "Untitled"))
	}
	return ""
}

func isAdmin(ctx context.Context) bool {
	u := userFrom(ctx)
	return u != nil && u.Role == "admin"
}

func (s *Server) handleCreateSource(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	var in sourceInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Kind == "" {
		in.Kind = "push"
	}
	if in.Kind != "push" && in.Kind != "ntfy" {
		writeErr(w, http.StatusBadRequest, "a source receives (push) or follows an ntfy server (ntfy); Pocket apps are switched on in the list")
		return
	}
	x := &store.Source{Kind: in.Kind, Enabled: true}
	if msg := in.apply(x, isAdmin(ctx)); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if x.Kind == "ntfy" {
		if x.FollowURL == "" || x.FollowTopics == "" {
			writeErr(w, http.StatusBadRequest, "say which ntfy server and topics to follow")
			return
		}
		if in.Backfill {
			x.FollowCursor = "12h"
		}
	}
	if err := s.store.CreateSource(ctx, uid, x); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeErr(w, http.StatusConflict, "this topic is taken: another source already has it (or takes it openly)")
			return
		}
		s.fail(w, err, "source")
		return
	}
	if x.Kind == "ntfy" {
		s.syncFollowers(ctx)
	}
	writeJSON(w, http.StatusCreated, s.sourceView(ctx, x, nil))
}

func (s *Server) handleUpdateSource(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	x, err := s.store.GetSource(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "source")
		return
	}
	var in sourceInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if msg := in.apply(x, isAdmin(ctx)); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if err := s.store.UpdateSource(ctx, uid, x); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeErr(w, http.StatusConflict, "this topic is taken: another source already has it (or takes it openly)")
			return
		}
		s.fail(w, err, "source")
		return
	}
	if x.Kind == "ntfy" {
		s.syncFollowers(ctx)
	}
	writeJSON(w, http.StatusOK, s.sourceView(ctx, x, nil))
}

func (s *Server) handleDeleteSource(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := s.store.DeleteSource(ctx, actorID(ctx), idParam(r, "id")); err != nil {
		s.fail(w, err, "source")
		return
	}
	s.syncFollowers(ctx)
	w.WriteHeader(http.StatusNoContent)
}

// handleSourceKey is POST /sources/{id}/key: a new secret address (the old
// one stops working at once).
func (s *Server) handleSourceKey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	x, err := s.store.GetSource(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "source")
		return
	}
	x.PushKey = store.NewPushKey()
	if err := s.store.UpdateSource(ctx, uid, x); err != nil {
		s.fail(w, err, "source")
		return
	}
	writeJSON(w, http.StatusOK, s.sourceView(ctx, x, nil))
}

// handleSnoozeSource is POST /sources/{id}/snooze {minutes} (0 = wake up now).
func (s *Server) handleSnoozeSource(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	x, err := s.store.GetSource(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "source")
		return
	}
	var in struct {
		Minutes int    `json:"minutes"`
		Until   string `json:"until"` // "tomorrow" = tomorrow at the end of quiet hours (or 08:00)
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	var until *string
	switch {
	case in.Until == "tomorrow":
		st, _ := s.store.GetSettings(ctx, uid)
		hm := 8 * 60
		if m, ok := parseClock(st.QuietEnd); ok && st.QuietEnabled {
			hm = m
		}
		y, mo, d := time.Now().AddDate(0, 0, 1).Date()
		t := time.Date(y, mo, d, hm/60, hm%60, 0, 0, time.Local).UTC().Format(time.RFC3339Nano)
		until = &t
	case in.Minutes > 0:
		if in.Minutes > 60*24*31 {
			writeErr(w, http.StatusBadRequest, "snooze for a month at most")
			return
		}
		t := time.Now().Add(time.Duration(in.Minutes) * time.Minute).UTC().Format(time.RFC3339Nano)
		until = &t
	}
	x.SnoozedUntil = until
	if err := s.store.UpdateSource(ctx, uid, x); err != nil {
		s.fail(w, err, "source")
		return
	}
	writeJSON(w, http.StatusOK, s.sourceView(ctx, x, nil))
}

// handleTestSource is POST /sources/{id}/test: a test alert through the
// whole way (rules, quiet hours, channels) as if the source sent it.
func (s *Server) handleTestSource(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	x, err := s.store.GetSource(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "source")
		return
	}
	in := testIncoming()
	in.Title += " (" + x.Name + ")"
	in.Body += " Sent " + time.Now().Format("15:04:05") + "."
	a, _, err := s.receive(ctx, uid, x, in)
	if err != nil {
		s.fail(w, err, "test")
		return
	}
	if a == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "it was not kept: the source is switched off or a rule drops it"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "alert": viewOf(*a)})
}

// handleListApps is GET /apps: the Pocket apps whose alerts can come in,
// whether each is on for the person and whether Core lets it through.
func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	out := []map[string]any{}
	for _, a := range eventApps {
		item := map[string]any{"slug": a.slug, "name": a.name, "icon": a.icon, "about": a.about, "on": false, "source_id": nil}
		if src, err := s.store.SourceByApp(ctx, u.ID, a.slug); err == nil {
			item["on"], item["source_id"] = src.Enabled, src.ID
		}
		ok, why := s.appSourceStatus(ctx, u.Email, a.slug)
		item["link_on"], item["link_detail"] = ok, why
		out = append(out, item)
	}
	connected, why := s.streamStatus()
	writeJSON(w, http.StatusOK, map[string]any{"apps": out, "core": s.core.Configured(), "stream": connected, "stream_error": why})
}

// handleSetApp is PUT /apps/{slug} {on}.
func (s *Server) handleSetApp(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		On bool `json:"on"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.On && !s.core.Configured() {
		writeErr(w, http.StatusConflict, "alerts from the other Pocket apps need Pocket Core")
		return
	}
	src, err := s.ensureAppSource(ctx, actorID(ctx), chiParam(r, "slug"), in.On)
	if err != nil {
		s.fail(w, err, "app")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"on": in.On, "source_id": idOr(src)})
}

func idOr(x *store.Source) any {
	if x == nil {
		return nil
	}
	return x.ID
}
