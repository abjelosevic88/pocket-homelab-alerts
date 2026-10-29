package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-alerts/internal/store"
)

// ---- channels: where alerts go ----

// channelView masks the secrets.
func channelView(c store.Channel) store.Channel {
	cfg := map[string]string{}
	for k, v := range c.Config {
		if secretKeys[k] {
			v = mask(v)
		}
		cfg[k] = v
	}
	c.Config = cfg
	return c
}

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := s.store.ListChannels(ctx, actorID(ctx))
	if err != nil {
		s.fail(w, err, "channels")
		return
	}
	out := make([]store.Channel, 0, len(list))
	for _, c := range list {
		out = append(out, channelView(c))
	}
	writeJSON(w, http.StatusOK, out)
}

type channelInput struct {
	Name        *string           `json:"name"`
	Kind        string            `json:"kind"`
	Config      map[string]string `json:"config"`
	MinPriority *int              `json:"min_priority"`
	Enabled     *bool             `json:"enabled"`
}

// channelConfigKeys are the settings each kind keeps.
var channelConfigKeys = map[string][]string{
	"pushover": {"user", "token", "device", "sound", "max_priority"},
	"ntfy":     {"url", "topic", "token"},
	"apprise":  {"url", "tag"},
	"webhook":  {"url", "secret"},
}

func (in channelInput) apply(c *store.Channel) string {
	if in.Name != nil {
		c.Name = clip(strings.TrimSpace(*in.Name), 80)
	}
	if in.MinPriority != nil {
		if *in.MinPriority < 1 || *in.MinPriority > 5 {
			return "the importance is 1 to 5"
		}
		c.MinPriority = *in.MinPriority
	}
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
	if in.Config != nil {
		if c.Config == nil {
			c.Config = map[string]string{}
		}
		for _, k := range channelConfigKeys[c.Kind] {
			v, ok := in.Config[k]
			if !ok || (secretKeys[k] && strings.Contains(v, "••")) {
				continue
			}
			v = strings.TrimSpace(v)
			if k == "url" {
				v = strings.TrimRight(v, "/")
			}
			c.Config[k] = v
		}
	}
	for _, k := range channelKinds[c.Kind] {
		if c.Config[k] == "" {
			switch k {
			case "user":
				return "Pushover needs your user key"
			case "token":
				return "Pushover needs an application token (pushover.net → Create an Application)"
			case "topic":
				return "say which ntfy topic to post to"
			default:
				return "the address is required"
			}
		}
	}
	if u := c.Config["url"]; u != "" {
		p, err := url.Parse(u)
		if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
			return "the address starts with http:// or https://"
		}
	}
	if c.Kind == "ntfy" && store.CleanTopic(c.Config["topic"]) != c.Config["topic"] {
		return "an ntfy topic has only letters, digits, - and _"
	}
	if c.Name == "" {
		c.Name = map[string]string{"pushover": "Pushover", "ntfy": "ntfy", "apprise": "Apprise", "webhook": "Webhook"}[c.Kind]
	}
	return ""
}

func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in channelInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if _, ok := channelKinds[in.Kind]; !ok {
		writeErr(w, http.StatusBadRequest, "a channel is Pushover, ntfy, Apprise or a webhook")
		return
	}
	c := &store.Channel{Kind: in.Kind, MinPriority: 1, Enabled: true, Config: map[string]string{}}
	if msg := in.apply(c); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if err := s.store.CreateChannel(ctx, actorID(ctx), c); err != nil {
		s.fail(w, err, "channel")
		return
	}
	writeJSON(w, http.StatusCreated, channelView(*c))
}

func (s *Server) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	c, err := s.store.GetChannel(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "channel")
		return
	}
	var in channelInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if msg := in.apply(c); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if err := s.store.UpdateChannel(ctx, uid, c); err != nil {
		s.fail(w, err, "channel")
		return
	}
	writeJSON(w, http.StatusOK, channelView(*c))
}

func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := s.store.DeleteChannel(ctx, actorID(ctx), idParam(r, "id")); err != nil {
		s.fail(w, err, "channel")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleReorderChannels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := s.store.ReorderChannels(ctx, actorID(ctx), in.IDs); err != nil {
		s.fail(w, err, "reorder")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTestChannel is POST /channels/{id}/test: one test message, right now
// (also with settings typed in but not saved yet).
func (s *Server) handleTestChannel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	var c *store.Channel
	var in channelInput
	_ = decode(r, &in)
	if id := idParam(r, "id"); id > 0 {
		saved, err := s.store.GetChannel(ctx, uid, id)
		if err != nil {
			s.fail(w, err, "channel")
			return
		}
		c = saved
	} else {
		if _, ok := channelKinds[in.Kind]; !ok {
			writeErr(w, http.StatusBadRequest, "unknown kind of channel")
			return
		}
		c = &store.Channel{Kind: in.Kind, MinPriority: 1, Enabled: true, Config: map[string]string{}}
	}
	if msg := in.apply(c); msg != "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": msg})
		return
	}
	tctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	err := s.send(tctx, uid, c, outMsg{Title: "Pocket Alerts", Body: "This channel works. Alerts will reach you here.", Priority: 3, Tags: []string{"white_check_mark"},
		Click: s.cfg.BaseURL, Source: "Pocket Alerts", Kind: "test", At: store.Now()})
	if c.ID > 0 {
		d := &store.Delivery{ChannelID: &c.ID, Channel: c.Name, Kind: "test", Status: "sent"}
		if err != nil {
			d.Status, d.Error = "failed", err.Error()
		}
		_ = s.store.AddDelivery(ctx, uid, d)
		_ = s.store.SetChannelResult(ctx, c.ID, err == nil, d.Error)
	}
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleChannelDeliveries(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	if _, err := s.store.GetChannel(ctx, uid, idParam(r, "id")); err != nil {
		s.fail(w, err, "channel")
		return
	}
	list, err := s.store.RecentDeliveries(ctx, uid, idParam(r, "id"), 50)
	if err != nil {
		s.fail(w, err, "deliveries")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// ---- rules ----

type ruleInput struct {
	Name       *string          `json:"name"`
	Enabled    *bool            `json:"enabled"`
	Match      *store.RuleMatch `json:"match"`
	Action     *string          `json:"action"`
	Priority   *int             `json:"priority"`
	ChannelIDs *[]int64         `json:"channel_ids"`
	BreakQuiet *bool            `json:"break_quiet"`
}

func (s *Server) applyRule(ctx context.Context, uid int64, in ruleInput, r *store.Rule) string {
	if in.Name != nil {
		r.Name = clip(strings.TrimSpace(*in.Name), 80)
	}
	if in.Enabled != nil {
		r.Enabled = *in.Enabled
	}
	if in.Action != nil {
		switch *in.Action {
		case "send", "quiet", "drop":
			r.Action = *in.Action
		default:
			return "a rule sends, keeps quiet or drops"
		}
	}
	if in.Priority != nil {
		if *in.Priority < 0 || *in.Priority > 5 {
			return "the importance is 1 to 5 (0 = keep)"
		}
		r.Priority = *in.Priority
	}
	if in.BreakQuiet != nil {
		r.BreakQuiet = *in.BreakQuiet
	}
	if in.Match != nil {
		m := *in.Match
		ids := []int64{}
		for _, id := range m.SourceIDs {
			if _, err := s.store.GetSource(ctx, uid, id); err == nil {
				ids = append(ids, id)
			}
		}
		m.SourceIDs = ids
		if m.MinPriority < 0 || m.MinPriority > 5 || m.MaxPriority < 0 || m.MaxPriority > 5 {
			return "the importance is 1 to 5"
		}
		m.Words, m.Tags = clip(strings.TrimSpace(m.Words), 500), clip(strings.TrimSpace(m.Tags), 300)
		r.Match = m
	}
	if in.ChannelIDs != nil {
		ids := []int64{}
		for _, id := range *in.ChannelIDs {
			if _, err := s.store.GetChannel(ctx, uid, id); err == nil {
				ids = append(ids, id)
			}
		}
		r.ChannelIDs = ids
	}
	if r.Name == "" {
		return "give the rule a name"
	}
	return ""
}

func (s *Server) handleListRules(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := s.store.ListRules(ctx, actorID(ctx))
	if err != nil {
		s.fail(w, err, "rules")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	var in ruleInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	rule := &store.Rule{Enabled: true, Action: "quiet", ChannelIDs: []int64{}}
	if msg := s.applyRule(ctx, uid, in, rule); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if err := s.store.CreateRule(ctx, uid, rule); err != nil {
		s.fail(w, err, "rule")
		return
	}
	writeJSON(w, http.StatusCreated, rule)
}

func (s *Server) handleUpdateRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	rule, err := s.store.GetRule(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "rule")
		return
	}
	var in ruleInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if msg := s.applyRule(ctx, uid, in, rule); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if err := s.store.UpdateRule(ctx, uid, rule); err != nil {
		s.fail(w, err, "rule")
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := s.store.DeleteRule(ctx, actorID(ctx), idParam(r, "id")); err != nil {
		s.fail(w, err, "rule")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleReorderRules(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := s.store.ReorderRules(ctx, actorID(ctx), in.IDs); err != nil {
		s.fail(w, err, "reorder")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePreviewRule is POST /rules/preview {match}: which of the last 30
// days' alerts the conditions would have caught.
func (s *Server) handlePreviewRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	var in struct {
		Match store.RuleMatch `json:"match"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	list, err := s.store.ListAlerts(ctx, uid, store.AlertFilter{View: "all", From: time.Now().AddDate(0, 0, -30).UTC().Format(time.RFC3339Nano), Limit: 2000})
	if err != nil {
		s.fail(w, err, "preview")
		return
	}
	hits := []alertView{}
	n := 0
	for i := range list {
		a := &list[i]
		var sid int64
		if a.SourceID != nil {
			sid = *a.SourceID
		}
		if ruleMatches(in.Match, sid, a) {
			n += a.Count
			if len(hits) < 8 {
				hits = append(hits, viewOf(*a))
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"matched": n, "of": len(list), "examples": hits})
}
