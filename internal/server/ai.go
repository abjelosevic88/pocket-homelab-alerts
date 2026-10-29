package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-alerts/internal/ai"
	"github.com/abjelosevic88/pocket-homelab-alerts/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// aiFor is the person's AI: Pocket AI through Core, their own
// OpenAI-compatible key, or nil with why.
func (s *Server) aiFor(ctx context.Context, uid int64, st store.Settings) (*ai.Client, string, string) {
	email := ""
	if u, err := s.store.GetUser(ctx, uid); err == nil {
		email = u.Email
	}
	pocket, status := s.core.SelectAI(ctx, email, st.AIProvider, st.OpenAIKey != "")
	if pocket != nil {
		if !status.Ready {
			return nil, "", "Pocket AI: " + status.Detail
		}
		c := ai.New("", "", "", 150*time.Second)
		c.Pocket = pocket.WithModel(st.AIPocketModel)
		return c, "pocket", ""
	}
	if st.OpenAIKey == "" {
		return nil, "", status.Detail
	}
	return ai.New(st.OpenAIBaseURL, st.OpenAIKey, st.OpenAIModel, 150*time.Second), "openai", ""
}

func (s *Server) handleAIStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, _ := s.store.GetSettings(ctx, actorID(ctx))
	cl, used, why := s.aiFor(ctx, actorID(ctx), st)
	writeJSON(w, http.StatusOK, map[string]any{"ready": cl != nil, "provider": used, "detail": why})
}

func (s *Server) handleAIModels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, status := s.core.SelectAI(ctx, userFrom(ctx).Email, "pocket", false)
	if c == nil || !status.Ready {
		writeErr(w, http.StatusServiceUnavailable, status.Detail)
		return
	}
	models, err := c.Models(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, models)
}

func validAIProvider(v string) bool { return core.ValidAIProvider(v) }

func validPocketModel(v string) bool { return core.ValidInferenceModel(v) }

// ---- explaining an alert ----

// handleExplain is POST /alerts/{id}/explain: what the alert means and what
// to do about it, in plain words. Nothing is saved.
func (s *Server) handleExplain(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	a, err := s.store.GetAlert(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "alert")
		return
	}
	st, _ := s.store.GetSettings(ctx, uid)
	cl, used, why := s.aiFor(ctx, uid, st)
	if cl == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": firstNonEmpty(why, "AI is not set up (Settings → AI)"), "not_configured": true})
		return
	}
	earlier, _ := s.store.ListAlerts(ctx, uid, store.AlertFilter{View: "all", SourceID: deref64(a.SourceID), Limit: 10})
	var b strings.Builder
	fmt.Fprintf(&b, "Source: %s\nImportance: %s\nReceived: %s (seen %d times)\nTitle: %s\nText:\n%s\n", a.SourceName, priorityName(a.Priority), localClock(a.ReceivedAt), a.Count, a.Title, clip(a.Body, 4000))
	if len(a.Tags) > 0 {
		fmt.Fprintf(&b, "Tags: %s\n", strings.Join(a.Tags, ", "))
	}
	if len(earlier) > 1 {
		b.WriteString("\nEarlier alerts from the same source (newest first):\n")
		for _, e := range earlier {
			if e.ID == a.ID {
				continue
			}
			fmt.Fprintf(&b, "- %s: %s ×%d\n", localClock(e.ReceivedAt), clipRunes(firstNonEmpty(e.Title, e.Body), 120), e.Count)
		}
	}
	system := "You help a person who runs a small home server understand an alert they got. In plain words and briefly: " +
		"1) what it means, 2) whether it is likely urgent, 3) what to check or do, as 2–4 short steps. " +
		"Do not invent facts that are not in the alert; say what you are unsure of. No headings, no preamble."
	text, err := cl.Complete(ctx, system, b.String(), false)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "AI: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "provider": used, "text": strings.TrimSpace(text)})
}

func deref64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// handleAsk answers a question about the person's alerts (the last 30 days).
func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	var in struct {
		Question string `json:"question"`
	}
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.Question) == "" {
		writeErr(w, http.StatusBadRequest, "question is required")
		return
	}
	st, _ := s.store.GetSettings(ctx, uid)
	cl, used, why := s.aiFor(ctx, uid, st)
	if cl == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": firstNonEmpty(why, "AI is not set up (Settings → AI)"), "not_configured": true})
		return
	}
	list, _ := s.store.ListAlerts(ctx, uid, store.AlertFilter{View: "all", From: time.Now().AddDate(0, 0, -30).UTC().Format(time.RFC3339Nano), Limit: 300})
	srcs, _ := s.store.ListSources(ctx, uid)
	var b strings.Builder
	fmt.Fprintf(&b, "Now: %s.\nSources: ", time.Now().Format("Mon 2006-01-02 15:04"))
	for i, x := range srcs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(x.Name)
		if x.Muted {
			b.WriteString(" (muted)")
		}
	}
	b.WriteString("\nAlerts, last 30 days, newest first (time, source, importance, ×repeats, state, done?, title — text):\n")
	for _, a := range list {
		done := ""
		if a.DoneAt != nil {
			done = " done"
		}
		fmt.Fprintf(&b, "%s | %s | %s | ×%d | %s%s | %s — %s\n", localClock(a.ReceivedAt), a.SourceName, priorityName(a.Priority), a.Count, a.State, done,
			clipRunes(a.Title, 120), clipRunes(strings.ReplaceAll(a.Body, "\n", " "), 160))
	}
	system := "You answer questions about a person's alerts (from their home server, scripts and apps) from the facts given. Be brief, plain and concrete; use the numbers and times. If the facts don't say, say so."
	text, err := cl.Complete(ctx, system, b.String()+"\nQuestion: "+clip(in.Question, 1000), false)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "AI: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "provider": used, "text": strings.TrimSpace(text)})
}
