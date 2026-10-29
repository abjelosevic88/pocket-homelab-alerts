package server

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-alerts/internal/store"
)

// ---- what happens to an alert ----
//
// Every alert, however it came in, goes through receive:
//
//  1. a source that is switched off takes nothing; a message id seen before
//     (an ntfy follower reconnecting) is skipped;
//  2. a repeat of an open alert from the same source within the grouping
//     window counts up on that alert and pings nobody;
//  3. the person's rules, in order — the first that matches decides: keep
//     it quiet, don't keep it at all, or send it (to some channels, with
//     another importance, even during quiet hours);
//  4. then: a muted or snoozed source, an importance below what the person
//     gets pinged for, or quiet hours keep it quiet (quiet hours hold it for
//     one summary when they end);
//  5. otherwise it goes to every channel that takes alerts this important.

// incoming is an alert as a sender gave it.
type incoming struct {
	Title    string
	Body     string
	Priority int // 1–5; 0 = not given
	Tags     []string
	Click    string
	ExtID    string
	At       time.Time // when it happened; zero = now
}

var digits = regexp.MustCompile(`[0-9]+`)

// fingerprint is what makes two alerts "the same": the title (or the first
// line of the text) without numbers, so "Disk 91% full" and "Disk 92% full"
// count as one.
func fingerprint(title, body string) string {
	key := strings.TrimSpace(title)
	if key == "" {
		key, _, _ = strings.Cut(strings.TrimSpace(body), "\n")
	}
	key = strings.Join(strings.Fields(strings.ToLower(digits.ReplaceAllString(key, "#"))), " ")
	if key == "" {
		return ""
	}
	h := sha1.Sum([]byte(key))
	return hex.EncodeToString(h[:8])
}

func clampPriority(p int) int {
	switch {
	case p < 1:
		return 3
	case p > 5:
		return 5
	}
	return p
}

// priorityName is the word people see for an importance.
func priorityName(p int) string {
	switch p {
	case 5:
		return "urgent"
	case 4:
		return "high"
	case 2:
		return "low"
	case 1:
		return "lowest"
	}
	return "normal"
}

// verdict is what receive decided.
type verdict struct {
	state    string // sent | quiet | held | none
	reason   string
	rule     *store.Rule
	channels []store.Channel
}

// receive takes one alert for a person from one of their sources. It
// returns the alert (nil when it was not kept) and whether it is new (false
// for a repeat or a duplicate).
func (s *Server) receive(ctx context.Context, uid int64, src *store.Source, in incoming) (*store.Alert, bool, error) {
	if src == nil || !src.Enabled {
		return nil, false, nil
	}
	if in.ExtID != "" && s.store.HasExtID(ctx, uid, in.ExtID) {
		return nil, false, nil
	}
	at := in.At
	if at.IsZero() || at.After(time.Now().Add(time.Minute)) {
		at = time.Now()
	}
	now := at.UTC().Format(time.RFC3339Nano)
	a := &store.Alert{
		SourceID: &src.ID, SourceName: src.Name, ExtID: clip(in.ExtID, 120),
		Title: clip(oneLine(in.Title), 250), Body: clip(strings.TrimSpace(in.Body), 8000),
		Priority: in.Priority, Tags: cleanTags(in.Tags), ClickURL: clip(strings.TrimSpace(in.Click), 2000),
		ReceivedAt: now, LastSeenAt: now,
	}
	if a.Priority < 1 || a.Priority > 5 {
		a.Priority = 0
	}
	if a.Priority == 0 {
		a.Priority = src.DefaultPriority
	}
	a.Priority = clampPriority(a.Priority)
	if a.Title == "" && a.Body == "" {
		a.Body = "(no text)"
	}
	a.Fingerprint = fingerprint(a.Title, a.Body)
	st, err := s.store.GetSettings(ctx, uid)
	if err != nil {
		return nil, false, err
	}
	_ = s.store.TouchSource(ctx, src.ID, now)

	// a repeat counts up and pings nobody
	if st.GroupMinutes > 0 {
		since := at.Add(-time.Duration(st.GroupMinutes) * time.Minute).UTC().Format(time.RFC3339Nano)
		if open, err := s.store.OpenRepeat(ctx, uid, a.SourceID, a.Fingerprint, since); err == nil {
			body := a.Body
			if body == "" {
				body = open.Body
			}
			if err := s.store.Repeat(ctx, uid, open, body, a.Priority, now); err != nil {
				return nil, false, err
			}
			s.live.send(uid, liveEvent{Type: "alert", ID: open.ID})
			return open, false, nil
		}
	}

	v, err := s.decide(ctx, uid, st, src, a, at)
	if err != nil {
		return nil, false, err
	}
	if v.state == "drop" {
		return nil, false, nil
	}
	a.State, a.Reason = v.state, v.reason
	if v.rule != nil {
		a.RuleID = &v.rule.ID
	}
	if err := s.store.AddAlert(ctx, uid, a); err != nil {
		return nil, false, err
	}
	s.received.Add(1)
	s.live.send(uid, liveEvent{Type: "alert", ID: a.ID, New: true})
	s.publish(ctx, uid, "alerts.received", fmt.Sprint(a.ID), map[string]any{
		"alert_id": a.ID, "title": a.Title, "body": clip(a.Body, 500), "priority": a.Priority, "source": a.SourceName, "state": a.State, "tags": a.Tags,
		"url": s.alertLink(a.ID),
	})
	if v.state == "sent" {
		s.deliverAsync(uid, a, v.channels)
	}
	return a, true, nil
}

// decide applies the rules, then mute, snooze, importance and quiet hours.
func (s *Server) decide(ctx context.Context, uid int64, st store.Settings, src *store.Source, a *store.Alert, at time.Time) (verdict, error) {
	var v verdict
	rules, err := s.store.ListRules(ctx, uid)
	if err != nil {
		return v, err
	}
	for i := range rules {
		r := &rules[i]
		if !r.Enabled || !ruleMatches(r.Match, src.ID, a) {
			continue
		}
		v.rule = r
		_ = s.store.RuleHit(ctx, r.ID)
		if r.Priority >= 1 && r.Priority <= 5 {
			a.Priority = r.Priority
		}
		break
	}
	ruleName := func() string { return "the rule “" + v.rule.Name + "”" }
	if v.rule != nil {
		switch v.rule.Action {
		case "drop":
			return verdict{state: "drop"}, nil
		case "quiet":
			return verdict{state: "quiet", reason: "Kept quiet by " + ruleName(), rule: v.rule}, nil
		}
	}
	nowStr := store.Now()
	switch {
	case src.Muted:
		return verdict{state: "quiet", reason: "The source is muted", rule: v.rule}, nil
	case src.Snoozed(nowStr):
		return verdict{state: "quiet", reason: "The source is snoozed until " + localClock(*src.SnoozedUntil), rule: v.rule}, nil
	}
	forced := v.rule != nil && v.rule.Action == "send"
	if !forced && a.Priority < st.SendMinPriority {
		return verdict{state: "quiet", reason: "Less important than what you get pinged for (" + priorityName(st.SendMinPriority) + " and up)", rule: v.rule}, nil
	}
	breaks := (v.rule != nil && v.rule.BreakQuiet) || a.Priority >= st.QuietMinPriority
	if on, until := quietNow(st, at); on && !breaks {
		if st.QuietSummary {
			return verdict{state: "held", reason: "Quiet hours: in the summary at " + until, rule: v.rule}, nil
		}
		return verdict{state: "quiet", reason: "Quiet hours until " + until, rule: v.rule}, nil
	}
	chans, why, err := s.targets(ctx, uid, a.Priority, v.rule)
	if err != nil {
		return v, err
	}
	if len(chans) == 0 {
		return verdict{state: "none", reason: why, rule: v.rule}, nil
	}
	reason := ""
	if forced {
		reason = "Sent by " + ruleName()
	}
	return verdict{state: "sent", reason: reason, rule: v.rule, channels: chans}, nil
}

// targets are the channels an alert this important goes to.
func (s *Server) targets(ctx context.Context, uid int64, priority int, r *store.Rule) ([]store.Channel, string, error) {
	all, err := s.store.ListChannels(ctx, uid)
	if err != nil {
		return nil, "", err
	}
	if r != nil && len(r.ChannelIDs) > 0 {
		want := map[int64]bool{}
		for _, id := range r.ChannelIDs {
			want[id] = true
		}
		out := []store.Channel{}
		for _, c := range all {
			if want[c.ID] && c.Enabled {
				out = append(out, c)
			}
		}
		if len(out) == 0 {
			return nil, "The channels of the rule “" + r.Name + "” are switched off", nil
		}
		return out, "", nil
	}
	out := []store.Channel{}
	on := 0
	for _, c := range all {
		if !c.Enabled {
			continue
		}
		on++
		if priority >= c.MinPriority {
			out = append(out, c)
		}
	}
	switch {
	case len(all) == 0:
		return nil, "No place to send alerts to yet (Send to)", nil
	case on == 0:
		return nil, "Every channel is switched off", nil
	case len(out) == 0:
		return nil, "No channel takes " + priorityName(priority) + " alerts", nil
	}
	return out, "", nil
}

// ruleMatches checks an alert against a rule's conditions.
func ruleMatches(m store.RuleMatch, sourceID int64, a *store.Alert) bool {
	if len(m.SourceIDs) > 0 {
		ok := false
		for _, id := range m.SourceIDs {
			if id == sourceID {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if m.MinPriority > 0 && a.Priority < m.MinPriority {
		return false
	}
	if m.MaxPriority > 0 && a.Priority > m.MaxPriority {
		return false
	}
	if words := store.SplitTags(m.Words); len(words) > 0 {
		text := strings.ToLower(a.Title + "\n" + a.Body)
		ok := false
		for _, w := range words {
			if strings.Contains(text, w) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if want := store.SplitTags(m.Tags); len(want) > 0 {
		have := map[string]bool{}
		for _, t := range a.Tags {
			have[strings.ToLower(t)] = true
		}
		ok := false
		for _, t := range want {
			if have[t] {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// ---- quiet hours ----

func parseClock(s string) (int, bool) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

func quietDay(days []int, wd time.Weekday) bool {
	if len(days) == 0 {
		return true
	}
	for _, d := range days {
		if d == int(wd) {
			return true
		}
	}
	return false
}

// quietNow reports whether quiet hours are on at t, and when they end (HH:MM).
func quietNow(st store.Settings, t time.Time) (bool, string) {
	if !st.QuietEnabled {
		return false, ""
	}
	start, ok1 := parseClock(st.QuietStart)
	end, ok2 := parseClock(st.QuietEnd)
	if !ok1 || !ok2 || start == end {
		return false, ""
	}
	t = t.In(time.Local)
	m := t.Hour()*60 + t.Minute()
	on := false
	if start < end {
		on = m >= start && m < end && quietDay(st.QuietDays, t.Weekday())
	} else { // over midnight: the night belongs to the day it started
		on = (m >= start && quietDay(st.QuietDays, t.Weekday())) || (m < end && quietDay(st.QuietDays, t.AddDate(0, 0, -1).Weekday()))
	}
	return on, st.QuietEnd
}

// ---- small helpers ----

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func cleanTags(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range in {
		for _, p := range strings.Split(t, ",") {
			p = strings.TrimSpace(p)
			if p == "" || len(p) > 60 || seen[strings.ToLower(p)] {
				continue
			}
			seen[strings.ToLower(p)] = true
			out = append(out, p)
			if len(out) >= 12 {
				return out
			}
		}
	}
	return out
}

// localClock shows a stored time as HH:MM (or "Mon 15:04" when not today).
func localClock(at string) string {
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return at
	}
	t = t.In(time.Local)
	if t.Format("2006-01-02") == time.Now().Format("2006-01-02") {
		return t.Format("15:04")
	}
	return t.Format("Mon 15:04")
}

func (s *Server) alertLink(id int64) string {
	if s.cfg.BaseURL == "" {
		return ""
	}
	return fmt.Sprintf("%s/alerts/%d", s.cfg.BaseURL, id)
}
