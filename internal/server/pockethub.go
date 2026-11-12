package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-alerts/internal/store"
)

// ---- Pocket Hub: system notifications on the person's Macs, iPhones and iPads ----
//
// Alerts never talks to Apple. It hands the message to Pocket Core
// (POST /api/v1/push/notify with its app token); Core seals it for each of
// the person's Pocket Hub devices that has notifications on and passes it to
// the Pocket Push relay. Core decides who may be notified (only people who
// may use Pocket Alerts).

const pushNotifyPath = "/api/v1/push/notify"

// errHubNoCore is the refusal when this Pocket Alerts is not linked to a Core.
const errHubNoCore = "Pocket Hub notifications go through Pocket Core, and this Pocket Alerts is not connected to one"

// pushResult is Core's answer to /push/notify.
type pushResult struct {
	Devices int    `json:"devices"`
	Sent    int    `json:"sent"`
	Failed  int    `json:"failed"`
	Gone    int    `json:"gone"`
	Reason  string `json:"reason"`
}

func (s *Server) sendPocketHub(ctx context.Context, hc *http.Client, uid int64, m outMsg, emo string) error {
	if !s.core.Configured() {
		return &sendError{msg: errHubNoCore}
	}
	tok := s.core.Token()
	if tok == "" {
		return &sendError{msg: "Pocket Alerts has not registered with Pocket Core yet, so it cannot reach Pocket Hub; try again in a minute", temporary: true}
	}
	u, err := s.store.GetUser(ctx, uid)
	if err != nil || strings.TrimSpace(u.Email) == "" {
		return &sendError{msg: "this account has no email address for Pocket Core to find you by"}
	}
	title := m.Title
	if m.Kind == "test" {
		title = "Test from Pocket Alerts"
	}
	if emo != "" {
		title = emo + " " + title
	}
	source := firstNonEmpty(m.Source, "Pocket Alerts")
	in := map[string]any{
		"email":  u.Email,
		"title":  clipRunes(oneLine(title), 120),
		"body":   clipRunes(strings.TrimSpace(m.Body), 900),
		"url":    m.Click,
		"source": source,
		"sound":  m.Priority >= 3,
		"thread": source,
	}
	if m.AlertID > 0 {
		in["id"] = strconv.FormatInt(m.AlertID, 10)
	}
	b, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.core.BaseURL()+pushNotifyPath, bytes.NewReader(b))
	if err != nil {
		return &sendError{msg: "Pocket Core's address is not valid: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("User-Agent", s.cfg.UserAgent)
	res, err := hc.Do(req)
	if err != nil {
		return &sendError{msg: "could not reach Pocket Core: " + err.Error(), temporary: true}
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	coreSays := func() string {
		if e.Error != "" {
			return " (Pocket Core: " + clip(e.Error, 200) + ")"
		}
		return ""
	}
	switch {
	case res.StatusCode == http.StatusUnauthorized:
		return &sendError{msg: "Pocket Core did not accept Pocket Alerts' app token; check Pocket Alerts under Apps in Pocket Core" + coreSays()}
	case res.StatusCode == http.StatusForbidden:
		return &sendError{msg: "Pocket Core will not send you Pocket Hub notifications from Pocket Alerts; you may not be allowed to use Pocket Alerts there (ask the box's admin)" + coreSays()}
	case res.StatusCode == http.StatusNotFound:
		if e.Error != "" && !strings.EqualFold(strings.TrimSpace(e.Error), "not found") {
			return &sendError{msg: "Pocket Core: " + clip(e.Error, 200)}
		}
		return &sendError{msg: "Pocket Core is too old for Pocket Hub notifications"}
	case res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500:
		return &sendError{msg: fmt.Sprintf("Pocket Core answered HTTP %d", res.StatusCode) + coreSays(), temporary: true}
	case res.StatusCode >= 300:
		return &sendError{msg: fmt.Sprintf("Pocket Core refused it: HTTP %d", res.StatusCode) + coreSays()}
	}
	var out pushResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return &sendError{msg: "Pocket Core is too old for Pocket Hub notifications (it did not answer the way Pocket Hub needs)"}
	}
	return pushOutcome(out)
}

// pushOutcome turns Core's counts into a result: nothing to send to is a
// refusal (it won't fix itself), every device failing is worth a retry.
func pushOutcome(r pushResult) error {
	switch {
	case r.Devices == 0:
		return &sendError{msg: "No Pocket Hub device has notifications on — turn them on in Pocket Hub → Settings"}
	case r.Sent > 0:
		return nil
	case r.Failed > 0:
		return &sendError{msg: "Pocket Hub notifications did not go out: " + firstNonEmpty(r.Reason, "the push relay did not take them"), temporary: true}
	case r.Gone > 0:
		return &sendError{msg: "Your Pocket Hub devices stopped taking notifications — turn them on again in Pocket Hub → Settings"}
	}
	return nil
}

// hubReady says why a Pocket Hub channel cannot be made here ("" = it can).
func (s *Server) hubReady(c *store.Channel) string {
	if c.Kind == "pockethub" && !s.core.Configured() {
		return errHubNoCore
	}
	return ""
}
