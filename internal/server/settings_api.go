package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-alerts/internal/ai"
	"github.com/abjelosevic88/pocket-homelab-alerts/internal/notify"
	"github.com/abjelosevic88/pocket-homelab-alerts/internal/pocket"
	"github.com/abjelosevic88/pocket-homelab-alerts/internal/store"
)

// ---- settings ----

func mask(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "••••"
	}
	return s[:4] + "••••" + s[len(s)-4:]
}

// publicSettings is what one person sees: their settings with the secrets
// masked and, per Pocket app, the mode in effect (<app>_mode), whether it
// works and how (<app>_via: core | direct | "") and why not (<app>_detail).
func (s *Server) publicSettings(ctx context.Context, u *store.User, st store.Settings) map[string]any {
	out := map[string]any{
		"core_configured": s.core.Configured(), "core_registered": s.coreStatus()["registered"],
		"title": st.Title, "date_format": st.DateFormat, "time_format": st.TimeFormat, "week_start": st.WeekStart,
		"quiet_enabled": st.QuietEnabled, "quiet_start": st.QuietStart, "quiet_end": st.QuietEnd, "quiet_days": st.QuietDays,
		"quiet_min_priority": st.QuietMinPriority, "quiet_summary": st.QuietSummary,
		"group_minutes": st.GroupMinutes, "send_min_priority": st.SendMinPriority, "keep_done_days": st.KeepDoneDays, "keep_days": st.KeepDays,
		"notify_failing": st.NotifyFailing, "notify_url": st.NotifyURL, "notify_format": st.NotifyFormat, "notify_configured": st.NotifyURL != "",
		"tasks_url": st.TasksURL, "tasks_token": mask(st.TasksToken), "tasks_public_url": st.TasksPublicURL,
		"ai_provider": st.AIProvider, "ai_pocket_model": st.AIPocketModel, "openai_api_key": mask(st.OpenAIKey), "openai_model": st.OpenAIModel, "openai_base_url": st.OpenAIBaseURL,
		"setup_complete": st.SetupComplete, "base_url": s.cfg.BaseURL, "timezone": time.Now().Location().String(),
		"local_targets": s.localOK(ctx, u.ID),
	}
	if out["quiet_days"] == nil {
		out["quiet_days"] = []int{}
	}
	for _, a := range pocketApps {
		mode, via, detail := s.connStatus(ctx, u, st, a)
		out[a.slug+"_mode"], out[a.slug+"_via"], out[a.slug+"_detail"] = mode, via, detail
		out[a.slug+"_configured"] = via != ""
		_, _, _, public := connSettings(st, a)
		if public == "" && via != "" {
			public = s.resolveSibling(ctx, a.slug)
		}
		out[a.slug+"_open_url"] = public
	}
	return out
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.GetSettings(r.Context(), actorID(r.Context()))
	if err != nil {
		s.fail(w, err, "settings")
		return
	}
	writeJSON(w, http.StatusOK, s.publicSettings(r.Context(), userFrom(r.Context()), st))
}

// handlePutSettings saves the caller's personal settings. The box-wide title
// is only taken from an admin.
func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	me := userFrom(r.Context())
	isAdmin := me.Role == "admin"
	cur, err := s.store.GetSettings(r.Context(), me.ID)
	if err != nil {
		s.fail(w, err, "settings")
		return
	}
	var in store.Settings
	keys, err := decodeKeys(r, &in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	bad := func(msg string) { writeErr(w, http.StatusBadRequest, msg) }
	if keys["title"] && isAdmin {
		cur.Title = strings.TrimSpace(in.Title)
		if cur.Title == "" {
			cur.Title = store.DefaultTitle
		}
	}
	if keys["date_format"] {
		cur.DateFormat = in.DateFormat
	}
	if keys["time_format"] {
		cur.TimeFormat = in.TimeFormat
	}
	if keys["week_start"] && (in.WeekStart == 1 || in.WeekStart == 7 || in.WeekStart == 6) {
		cur.WeekStart = in.WeekStart
	}
	secret := func(k string, dst *string, v string) {
		if keys[k] && !strings.Contains(v, "••") {
			*dst = strings.TrimSpace(v)
		}
	}
	url := func(k string, dst *string, v string) {
		if keys[k] {
			*dst = strings.TrimRight(strings.TrimSpace(v), "/")
		}
	}
	text := func(k string, dst *string, v string) {
		if keys[k] {
			*dst = strings.TrimSpace(v)
		}
	}
	boolIn := func(k string, dst *bool, v bool) {
		if keys[k] {
			*dst = v
		}
	}
	if keys["quiet_start"] || keys["quiet_end"] {
		for k, v := range map[string]string{"quiet_start": in.QuietStart, "quiet_end": in.QuietEnd} {
			if keys[k] {
				if _, ok := parseClock(v); !ok {
					bad("quiet hours are times like 22:30")
					return
				}
			}
		}
		if keys["quiet_start"] {
			cur.QuietStart = strings.TrimSpace(in.QuietStart)
		}
		if keys["quiet_end"] {
			cur.QuietEnd = strings.TrimSpace(in.QuietEnd)
		}
	}
	if keys["quiet_days"] {
		days := []int{}
		seen := map[int]bool{}
		for _, d := range in.QuietDays {
			if d < 0 || d > 6 {
				bad("days are 0 (Sunday) to 6 (Saturday)")
				return
			}
			if !seen[d] {
				seen[d] = true
				days = append(days, d)
			}
		}
		if len(days) == 7 {
			days = []int{}
		}
		cur.QuietDays = days
	}
	prio := func(k string, dst *int, v int) bool {
		if !keys[k] {
			return true
		}
		if v < 1 || v > 5 {
			bad("the importance is 1 to 5")
			return false
		}
		*dst = v
		return true
	}
	if !prio("quiet_min_priority", &cur.QuietMinPriority, in.QuietMinPriority) || !prio("send_min_priority", &cur.SendMinPriority, in.SendMinPriority) {
		return
	}
	boolIn("quiet_enabled", &cur.QuietEnabled, in.QuietEnabled)
	boolIn("quiet_summary", &cur.QuietSummary, in.QuietSummary)
	days := func(k string, dst *int, v, max int, msg string) bool {
		if !keys[k] {
			return true
		}
		if v < 0 || v > max {
			bad(msg)
			return false
		}
		*dst = v
		return true
	}
	if !days("group_minutes", &cur.GroupMinutes, in.GroupMinutes, 1440, "group repeats for 0 to 1440 minutes") ||
		!days("keep_done_days", &cur.KeepDoneDays, in.KeepDoneDays, 3650, "keep done alerts for 0 (always) to 3650 days") ||
		!days("keep_days", &cur.KeepDays, in.KeepDays, 3650, "keep alerts for 0 (always) to 3650 days") {
		return
	}
	boolIn("notify_failing", &cur.NotifyFailing, in.NotifyFailing)
	text("notify_url", &cur.NotifyURL, in.NotifyURL)
	if keys["notify_format"] && in.NotifyFormat != "" {
		cur.NotifyFormat = in.NotifyFormat
	}
	url("tasks_url", &cur.TasksURL, in.TasksURL)
	secret("tasks_token", &cur.TasksToken, in.TasksToken)
	url("tasks_public_url", &cur.TasksPublicURL, in.TasksPublicURL)
	if keys["ai_provider"] {
		if !validAIProvider(in.AIProvider) {
			bad("AI is automatic, Pocket AI or OpenAI")
			return
		}
		cur.AIProvider = in.AIProvider
	}
	if keys["ai_pocket_model"] {
		if !validPocketModel(in.AIPocketModel) {
			bad("unknown model")
			return
		}
		cur.AIPocketModel = in.AIPocketModel
	}
	secret("openai_api_key", &cur.OpenAIKey, in.OpenAIKey)
	text("openai_model", &cur.OpenAIModel, in.OpenAIModel)
	url("openai_base_url", &cur.OpenAIBaseURL, in.OpenAIBaseURL)
	mode := func(k string, dst *string, v string) {
		if keys[k] && (v == modeCore || v == modeDirect || v == modeOff) {
			*dst = v
		}
	}
	mode("tasks_mode", &cur.TasksMode, in.TasksMode)
	if err := s.store.SaveUserSettings(r.Context(), me.ID, cur); err != nil {
		s.fail(w, err, "save settings")
		return
	}
	if keys["quiet_enabled"] || keys["quiet_start"] || keys["quiet_end"] || keys["quiet_days"] {
		s.live.send(me.ID, liveEvent{Type: "refresh"})
	}
	if isAdmin && keys["title"] {
		if err := s.store.SaveBoxSettings(r.Context(), cur); err != nil {
			s.fail(w, err, "save settings")
			return
		}
	}
	for _, k := range modeKeys {
		if keys[k] {
			s.reportModesSoon()
			break
		}
	}
	writeJSON(w, http.StatusOK, s.publicSettings(r.Context(), me, cur))
}

// handleIntegrationTest checks a connection with the caller's own saved
// settings (or the URL/key typed into the form).
func (s *Server) handleIntegrationTest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Which  string `json:"which"`
		URL    string `json:"url"`
		Key    string `json:"key"`
		User   string `json:"user"`
		Format string `json:"format"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	uid := actorID(r.Context())
	st, _ := s.store.GetSettings(r.Context(), uid)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	keyOr := func(saved string) string {
		if strings.Contains(in.Key, "••") || in.Key == "" {
			return saved
		}
		return in.Key
	}
	urlOr := func(saved string) string {
		if strings.TrimSpace(in.URL) == "" {
			return saved
		}
		return strings.TrimRight(strings.TrimSpace(in.URL), "/")
	}
	var msg string
	var err error
	if a, ok := appBySlug(in.Which); ok {
		mode, savedURL, savedTok, _ := connSettings(st, a)
		typed := in.Key != "" && !strings.Contains(in.Key, "••")
		var conn *siblingConn
		if tok := keyOr(savedTok); tok != "" && (typed || s.connMode(mode, savedTok) == modeDirect) {
			base := urlOr(savedURL)
			if base == "" {
				base = s.resolveSibling(ctx, a.slug)
			}
			conn = &siblingConn{client: pocket.New(base, tok, 15*time.Second), via: modeDirect}
		} else {
			conn, err = s.clients(ctx, uid).conn(ctx, a, a.scopes...)
		}
		if err == nil {
			how := "direct connection"
			if conn.via == modeCore {
				how = "through Pocket Core"
			}
			var v string
			if v, err = conn.client.Health(ctx); err == nil {
				msg = fmt.Sprintf("Pocket Tasks %s, %s", v, how)
			}
		}
	} else if in.Which == "openai" {
		c := ai.New(urlOr(st.OpenAIBaseURL), keyOr(st.OpenAIKey), st.OpenAIModel, 20*time.Second)
		var ids []string
		if ids, err = c.Models(ctx); err == nil {
			msg = fmt.Sprintf("The key works (%d models)", len(ids))
		}
	} else if in.Which == "notify" {
		f := in.Format
		if f == "" {
			f = st.NotifyFormat
		}
		n := notify.New(urlOr(st.NotifyURL), f)
		err = n.Send(ctx, notify.Message{Title: "Pocket Alerts", Body: "Notifications work. If one of your channels keeps failing, you will hear it here.", Priority: "info", Tags: []string{"bell"}})
		msg = "Test message sent via " + n.Format
	} else {
		writeErr(w, http.StatusBadRequest, "unknown integration")
		return
	}
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg})
}

func (s *Server) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, _ := s.store.GetSettings(ctx, actorID(ctx))
	n := notify.New(st.NotifyURL, st.NotifyFormat)
	if err := n.Send(ctx, notify.Message{Title: "Pocket Alerts", Body: "Notifications work.", Priority: "info", Tags: []string{"bell"}}); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- system / backup / export ----

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	var size int64
	if fi, err := os.Stat(s.cfg.DBPath); err == nil {
		size = fi.Size()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": Version, "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH,
		"core":      s.coreStatus(),
		"auth_mode": s.cfg.AuthMode, "data_dir": s.cfg.DataDir, "db_path": s.cfg.DBPath, "db_bytes": size,
		"uptime": time.Since(s.started).Round(time.Second).String(), "timezone": time.Now().Location().String(),
		"counts": s.store.CountsFor(r.Context(), actorID(r.Context())), "saved": s.received.Load(),
	})
}

// handleBackup downloads the whole database, an admin-only box operation.
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	dir := filepath.Join(s.cfg.DataDir, "backups")
	name := fmt.Sprintf("pocket-alerts-%s.db", time.Now().Format("20060102-150405"))
	dst := filepath.Join(dir, name)
	if err := s.store.Backup(r.Context(), dst); err != nil {
		s.fail(w, err, "backup")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, dst)
}

// exportDoc is the JSON export of one person's data. Channel secrets are
// included: the file is the person's own, to move to another box.
type exportDoc struct {
	App      string          `json:"app"`
	Version  string          `json:"version"`
	Exported string          `json:"exported_at"`
	Sources  []store.Source  `json:"sources"`
	Channels []store.Channel `json:"channels"`
	Rules    []store.Rule    `json:"rules"`
	Alerts   []store.Alert   `json:"alerts"`
}

func (s *Server) handleExportJSON(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	doc := exportDoc{App: "pocket-alerts", Version: Version, Exported: store.Now()}
	var err error
	if doc.Sources, err = s.store.ListSources(ctx, uid); err != nil {
		s.fail(w, err, "export")
		return
	}
	doc.Channels, _ = s.store.ListChannels(ctx, uid)
	doc.Rules, _ = s.store.ListRules(ctx, uid)
	doc.Alerts, _ = s.store.ListAlerts(ctx, uid, store.AlertFilter{View: "all"})
	w.Header().Set("Content-Disposition", `attachment; filename="pocket-alerts-export.json"`)
	writeJSON(w, http.StatusOK, doc)
}

// handleExportCSV writes the alerts as CSV.
func (s *Server) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := s.store.ListAlerts(ctx, actorID(ctx), store.AlertFilter{View: "all"})
	if err != nil {
		s.fail(w, err, "export")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="pocket-alerts.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"received", "last_seen", "count", "source", "importance", "title", "text", "tags", "what_happened", "read", "done"})
	for _, a := range list {
		_ = cw.Write([]string{a.ReceivedAt, a.LastSeenAt, strconv.Itoa(a.Count), a.SourceName, priorityName(a.Priority), a.Title, a.Body,
			strings.Join(a.Tags, ","), firstNonEmpty(a.Reason, a.State), deref(a.ReadAt), deref(a.DoneAt)})
	}
	cw.Flush()
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// handleImportJSON merges an export into the caller's own data. Sources are
// matched by name and kind, channels by name, rules by name, alerts by
// source, time and title, so importing the same file twice adds nothing.
// Imported sources get new secret addresses (the old ones belong to the old
// box).
func (s *Server) handleImportJSON(w http.ResponseWriter, r *http.Request) {
	var doc exportDoc
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, s.cfg.MaxBodyBytes))
	if err := dec.Decode(&doc); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if doc.App != "" && doc.App != "pocket-alerts" {
		writeErr(w, http.StatusBadRequest, "this is not a Pocket Alerts export")
		return
	}
	ctx := r.Context()
	uid := actorID(ctx)
	counts := map[string]int{"sources": 0, "channels": 0, "rules": 0, "alerts": 0, "skipped": 0}
	srcIDs := map[int64]int64{}
	have, _ := s.store.ListSources(ctx, uid)
	byKey := map[string]int64{}
	for _, x := range have {
		byKey[x.Kind+"|"+strings.ToLower(x.Name)] = x.ID
	}
	for _, x := range doc.Sources {
		k := x.Kind + "|" + strings.ToLower(x.Name)
		if id, ok := byKey[k]; ok {
			srcIDs[x.ID] = id
			continue
		}
		old := x.ID
		n := x
		n.ID, n.PushKey, n.Status, n.StatusNote, n.FollowCursor = 0, "", "", "", ""
		if n.Kind != "push" && n.Kind != "ntfy" && n.Kind != "app" {
			counts["skipped"]++
			continue
		}
		if err := s.store.CreateSource(ctx, uid, &n); err != nil {
			counts["skipped"]++
			continue
		}
		srcIDs[old], byKey[k] = n.ID, n.ID
		counts["sources"]++
	}
	chIDs := map[int64]int64{}
	haveCh, _ := s.store.ListChannels(ctx, uid)
	chByName := map[string]int64{}
	for _, c := range haveCh {
		chByName[strings.ToLower(c.Name)] = c.ID
	}
	for _, c := range doc.Channels {
		if id, ok := chByName[strings.ToLower(c.Name)]; ok {
			chIDs[c.ID] = id
			continue
		}
		if _, ok := channelKinds[c.Kind]; !ok {
			counts["skipped"]++
			continue
		}
		old := c.ID
		n := c
		n.ID = 0
		if err := s.store.CreateChannel(ctx, uid, &n); err != nil {
			counts["skipped"]++
			continue
		}
		chIDs[old], chByName[strings.ToLower(c.Name)] = n.ID, n.ID
		counts["channels"]++
	}
	haveR, _ := s.store.ListRules(ctx, uid)
	rByName := map[string]bool{}
	for _, x := range haveR {
		rByName[strings.ToLower(x.Name)] = true
	}
	for _, x := range doc.Rules {
		if rByName[strings.ToLower(x.Name)] {
			continue
		}
		n := x
		n.ID = 0
		ids := []int64{}
		for _, id := range x.Match.SourceIDs {
			if m, ok := srcIDs[id]; ok {
				ids = append(ids, m)
			}
		}
		n.Match.SourceIDs = ids
		chs := []int64{}
		for _, id := range x.ChannelIDs {
			if m, ok := chIDs[id]; ok {
				chs = append(chs, m)
			}
		}
		n.ChannelIDs = chs
		if err := s.store.CreateRule(ctx, uid, &n); err != nil {
			counts["skipped"]++
			continue
		}
		rByName[strings.ToLower(x.Name)] = true
		counts["rules"]++
	}
	known, _ := s.store.ListAlerts(ctx, uid, store.AlertFilter{View: "all"})
	seen := map[string]bool{}
	for _, a := range known {
		seen[a.SourceName+"|"+a.ReceivedAt+"|"+a.Title] = true
	}
	for _, a := range doc.Alerts {
		k := a.SourceName + "|" + a.ReceivedAt + "|" + a.Title
		if seen[k] || a.ReceivedAt == "" {
			continue
		}
		n := a
		n.ID, n.RuleID = 0, nil
		if a.SourceID != nil {
			if m, ok := srcIDs[*a.SourceID]; ok {
				n.SourceID = &m
			} else {
				n.SourceID = nil
			}
		}
		if n.State == "held" {
			n.State = "quiet"
		}
		if n.Fingerprint == "" {
			n.Fingerprint = fingerprint(n.Title, n.Body)
		}
		if err := s.store.AddAlert(ctx, uid, &n); err != nil {
			counts["skipped"]++
			continue
		}
		seen[k] = true
		counts["alerts"]++
	}
	s.syncFollowers(ctx)
	s.live.send(uid, liveEvent{Type: "refresh"})
	writeJSON(w, http.StatusOK, counts)
}
