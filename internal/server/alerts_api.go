package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-alerts/internal/emoji"
	"github.com/abjelosevic88/pocket-homelab-alerts/internal/store"
)

// alertView is an alert as the API shows it: the emoji its tags stand for
// and the plain labels apart.
type alertView struct {
	store.Alert
	Emoji  string   `json:"emoji"`
	Labels []string `json:"labels"`
}

func viewOf(a store.Alert) alertView {
	e, l := emoji.Split(a.Tags)
	return alertView{Alert: a, Emoji: e, Labels: l}
}

func viewsOf(list []store.Alert) []alertView {
	out := make([]alertView, 0, len(list))
	for _, a := range list {
		out = append(out, viewOf(a))
	}
	return out
}

// dayStart is the start of today (local) in storage form.
func dayStart(t time.Time) string {
	y, m, d := t.In(time.Local).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local).UTC().Format(time.RFC3339Nano)
}

// handleDashboard is the numbers for the sidebar, the inbox header and Hub.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	c, err := s.store.CountInbox(ctx, uid, dayStart(time.Now()))
	if err != nil {
		s.fail(w, err, "dashboard")
		return
	}
	st, _ := s.store.GetSettings(ctx, uid)
	quiet, until := quietNow(st, time.Now())
	srcs, _ := s.store.ListSources(ctx, uid)
	chans, _ := s.store.ListChannels(ctx, uid)
	failing := 0
	on := 0
	for _, ch := range chans {
		if ch.Enabled {
			on++
			if ch.LastStatus == "error" {
				failing++
			}
		}
	}
	srcErr := 0
	for _, x := range srcs {
		if x.Enabled && x.Status == "error" {
			srcErr++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"counts": c, "quiet": quiet, "quiet_until": until, "quiet_enabled": st.QuietEnabled,
		"sources": len(srcs), "sources_failing": srcErr, "channels": on, "channels_failing": failing,
	})
}

// handleListAlerts is GET /alerts?view=open|unread|important|done|all&source=&q=&before=&limit=
func (s *Server) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	limit := queryInt(r, "limit")
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	f := store.AlertFilter{View: q.Get("view"), SourceID: int64(queryInt(r, "source")), Query: q.Get("q"), Before: q.Get("before"), Limit: limit + 1}
	list, err := s.store.ListAlerts(ctx, actorID(ctx), f)
	if err != nil {
		s.fail(w, err, "alerts")
		return
	}
	next := ""
	if len(list) > limit {
		list = list[:limit]
		next = list[len(list)-1].LastSeenAt
	}
	writeJSON(w, http.StatusOK, map[string]any{"alerts": viewsOf(list), "next": next})
}

func (s *Server) handleGetAlert(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	a, err := s.store.GetAlert(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "alert")
		return
	}
	ds, _ := s.store.AlertDeliveries(ctx, uid, a.ID)
	out := map[string]any{"alert": viewOf(*a), "deliveries": ds}
	if a.SourceID != nil {
		if src, err := s.store.GetSource(ctx, uid, *a.SourceID); err == nil {
			out["source"] = s.sourceView(ctx, src, nil)
		}
	}
	if a.RuleID != nil {
		if rule, err := s.store.GetRule(ctx, uid, *a.RuleID); err == nil {
			out["rule"] = rule
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreateAlert is POST /alerts: an alert from the signed-in person (a
// script with their token) or from another Pocket app through a Core link
// (then the app is the source).
func (s *Server) handleCreateAlert(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	var in struct {
		SourceID int64    `json:"source_id"`
		Title    string   `json:"title"`
		Body     string   `json:"body"`
		Message  string   `json:"message"`
		Priority any      `json:"priority"`
		Tags     []string `json:"tags"`
		Click    string   `json:"click"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	body := firstNonEmpty(in.Body, in.Message)
	if strings.TrimSpace(in.Title) == "" && strings.TrimSpace(body) == "" {
		writeErr(w, http.StatusBadRequest, "a title or text is required")
		return
	}
	var src *store.Source
	var err error
	switch {
	case linkApp(ctx) != "":
		src, err = s.linkSource(ctx, uid, linkApp(ctx))
	case in.SourceID > 0:
		src, err = s.store.GetSource(ctx, uid, in.SourceID)
	default:
		src, err = s.store.SourceByTopic(ctx, uid, "api")
		if errors.Is(err, store.ErrNotFound) {
			src = &store.Source{Name: "Sent with the API", Kind: "push", Topic: "api", Enabled: true}
			err = s.store.CreateSource(ctx, uid, src)
		}
	}
	if err != nil {
		s.fail(w, err, "source")
		return
	}
	p := 0
	switch v := in.Priority.(type) {
	case float64:
		p = int(v)
	case string:
		p = parsePriority(v)
	}
	a, _, err := s.receive(ctx, uid, src, incoming{Title: in.Title, Body: body, Priority: p, Tags: in.Tags, Click: in.Click})
	if err != nil {
		s.fail(w, err, "receive")
		return
	}
	if a == nil {
		writeJSON(w, http.StatusAccepted, map[string]any{"kept": false})
		return
	}
	writeJSON(w, http.StatusCreated, viewOf(*a))
}

// linkSource is the source for alerts another Pocket app sends through a link.
func (s *Server) linkSource(ctx context.Context, uid int64, slug string) (*store.Source, error) {
	if src, err := s.store.SourceByApp(ctx, uid, slug); err == nil {
		return src, nil
	}
	name := "Pocket " + strings.ToUpper(slug[:1]) + slug[1:]
	if a, ok := eventAppBySlug(slug); ok {
		name = a.name
	}
	src := &store.Source{Name: name, Kind: "app", AppSlug: slug, Enabled: true}
	if err := s.store.CreateSource(ctx, uid, src); err != nil {
		return nil, err
	}
	return src, nil
}

// handleMarkAlerts is POST /alerts/mark {ids, what: read|done, on}; no ids =
// every open alert (mark all read / all done).
func (s *Server) handleMarkAlerts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	var in struct {
		IDs  []int64 `json:"ids"`
		What string  `json:"what"`
		On   *bool   `json:"on"`
	}
	if err := decode(r, &in); err != nil || (in.What != "read" && in.What != "done") {
		writeErr(w, http.StatusBadRequest, "say what to mark: read or done")
		return
	}
	on := in.On == nil || *in.On
	n, err := s.store.MarkAlerts(ctx, uid, in.IDs, in.What, on)
	if err != nil {
		s.fail(w, err, "mark")
		return
	}
	if in.What == "done" && on && n > 0 {
		s.publish(ctx, uid, "alerts.done", "", map[string]any{"count": n, "ids": in.IDs})
	}
	s.live.send(uid, liveEvent{Type: "refresh"})
	writeJSON(w, http.StatusOK, map[string]any{"changed": n})
}

// handleDeleteAlerts is POST /alerts/delete {ids}.
func (s *Server) handleDeleteAlerts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if err := decode(r, &in); err != nil || len(in.IDs) == 0 {
		writeErr(w, http.StatusBadRequest, "say which alerts")
		return
	}
	n, err := s.store.DeleteAlerts(ctx, uid, in.IDs)
	if err != nil {
		s.fail(w, err, "delete")
		return
	}
	s.live.send(uid, liveEvent{Type: "refresh"})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n})
}

func (s *Server) handleDeleteAlert(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	n, err := s.store.DeleteAlerts(ctx, uid, []int64{idParam(r, "id")})
	if err != nil {
		s.fail(w, err, "delete")
		return
	}
	if n == 0 {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	s.live.send(uid, liveEvent{Type: "refresh"})
	w.WriteHeader(http.StatusNoContent)
}

// handleMuteSimilar is POST /alerts/{id}/quiet: a rule that keeps alerts like
// this one quiet from now on (same source, same words in the title).
func (s *Server) handleMuteSimilar(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	a, err := s.store.GetAlert(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "alert")
		return
	}
	title := firstNonEmpty(a.Title, strings.SplitN(a.Body, "\n", 2)[0])
	words := strings.TrimSpace(digits.ReplaceAllString(title, " "))
	words = strings.Join(strings.Fields(strings.ReplaceAll(words, ",", " ")), " ")
	rule := &store.Rule{Name: "Quiet: " + clipRunes(firstNonEmpty(words, a.SourceName), 60), Enabled: true, Action: "quiet", ChannelIDs: []int64{}}
	if a.SourceID != nil {
		rule.Match.SourceIDs = []int64{*a.SourceID}
	}
	if words != "" {
		rule.Match.Words = clip(words, 200)
	}
	if err := s.store.CreateRule(ctx, uid, rule); err != nil {
		s.fail(w, err, "rule")
		return
	}
	writeJSON(w, http.StatusCreated, rule)
}

// handleAlertTask is POST /alerts/{id}/task: the alert as a task in Pocket Tasks.
func (s *Server) handleAlertTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	a, err := s.store.GetAlert(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "alert")
		return
	}
	var in struct {
		Title string `json:"title"`
	}
	_ = decode(r, &in)
	conn, err := s.clients(ctx, uid).conn(ctx, appTasks, scopeTasksWrite)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "not_connected": isNotConnected(err)})
		return
	}
	title := firstNonEmpty(strings.TrimSpace(in.Title), firstNonEmpty(a.Title, clipRunes(strings.SplitN(a.Body, "\n", 2)[0], 120)))
	notes := strings.TrimSpace(a.Body)
	notes += fmt.Sprintf("\n\nFrom %s, %s", a.SourceName, localClock(a.ReceivedAt))
	if link := s.alertLink(a.ID); link != "" {
		notes += "\n" + link
	}
	t, err := conn.client.CreateTask(ctx, clip(title, 250), strings.TrimSpace(notes), 0)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Pocket Tasks: " + err.Error()})
		return
	}
	url := strings.TrimRight(conn.public, "/")
	if url != "" && t.ID > 0 {
		url = fmt.Sprintf("%s/tasks/%d", url, t.ID)
	}
	_ = s.store.SetTaskURL(ctx, uid, a.ID, url)
	s.live.send(uid, liveEvent{Type: "alert", ID: a.ID})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "url": url, "id": t.ID})
}

// handleDay is GET /day/{date}: one day's alerts for Pocket Day and recaps.
func (s *Server) handleDay(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	d, err := time.ParseInLocation("2006-01-02", chiParam(r, "date"), time.Local)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "the date is YYYY-MM-DD")
		return
	}
	from := d.UTC().Format(time.RFC3339Nano)
	to := d.AddDate(0, 0, 1).UTC().Format(time.RFC3339Nano)
	list, err := s.store.ListAlerts(ctx, uid, store.AlertFilter{View: "all", From: from, To: to, Limit: 500})
	if err != nil {
		s.fail(w, err, "day")
		return
	}
	bySource := map[string]int{}
	important := 0
	for _, a := range list {
		bySource[a.SourceName] += a.Count
		if a.Priority >= 4 {
			important++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"date": chiParam(r, "date"), "alerts": viewsOf(list), "total": len(list), "important": important, "by_source": bySource})
}

// handleHistory is GET /history?days=30: alerts per day, for the chart.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	days := queryInt(r, "days")
	if days <= 0 || days > 400 {
		days = 30
	}
	now := time.Now()
	start, _ := time.Parse(time.RFC3339Nano, dayStart(now.AddDate(0, 0, -(days-1))))
	_, off := now.Zone()
	sign := "+"
	if off < 0 {
		sign, off = "-", -off
	}
	offset := fmt.Sprintf("%s%02d:%02d", sign, off/3600, off%3600/60)
	list, err := s.store.AlertDays(ctx, uid, start.UTC().Format(time.RFC3339Nano), now.Add(time.Minute).UTC().Format(time.RFC3339Nano), offset)
	if err != nil {
		s.fail(w, err, "history")
		return
	}
	stats, _ := s.store.SourceStats(ctx, uid, start.UTC().Format(time.RFC3339Nano))
	srcs, _ := s.store.ListSources(ctx, uid)
	type top struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		Alerts  int    `json:"alerts"`
		Repeats int    `json:"repeats"`
	}
	tops := []top{}
	for _, x := range srcs {
		if st, ok := stats[x.ID]; ok {
			tops = append(tops, top{x.ID, x.Name, st.Alerts, st.Repeats})
		}
	}
	for i := 1; i < len(tops); i++ {
		for j := i; j > 0 && tops[j].Alerts+tops[j].Repeats > tops[j-1].Alerts+tops[j-1].Repeats; j-- {
			tops[j], tops[j-1] = tops[j-1], tops[j]
		}
	}
	recent, _ := s.store.RecentDeliveries(ctx, uid, 0, 50)
	writeJSON(w, http.StatusOK, map[string]any{"days": list, "sources": tops, "deliveries": recent, "from": start.Format("2006-01-02"), "to": now.Format("2006-01-02")})
}
