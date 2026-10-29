package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-alerts/internal/emoji"
	"github.com/abjelosevic88/pocket-homelab-alerts/internal/notify"
	"github.com/abjelosevic88/pocket-homelab-alerts/internal/store"
)

// ---- sending to channels ----
//
// Pushover and ntfy are phone apps; Apprise reaches about a hundred other
// services (Telegram, email, Signal, Slack…); a webhook gets the alert as
// JSON. A failed send is tried again after 1, 5 and 15 minutes when the
// failure looks passing (no answer, 429, 5xx); a refusal (wrong key) is not.

// channelKinds are the kinds of channel and the settings each needs.
var channelKinds = map[string][]string{
	"pushover": {"user", "token"},
	"ntfy":     {"url", "topic"},
	"apprise":  {"url"},
	"webhook":  {"url"},
}

// secretKeys are channel settings never shown back in full.
var secretKeys = map[string]bool{"token": true, "user": true, "password": true, "secret": true}

// outMsg is what a channel sends.
type outMsg struct {
	Title    string
	Body     string
	Priority int
	Tags     []string
	Click    string
	Source   string
	AlertID  int64
	Count    int
	At       string
	Kind     string // alert | summary | test
}

func msgFor(a *store.Alert, link string) outMsg {
	title := a.Title
	if title == "" {
		title = a.SourceName
	}
	if a.Count > 1 {
		title = fmt.Sprintf("%s (×%d)", title, a.Count)
	}
	click := a.ClickURL
	if click == "" {
		click = link
	}
	return outMsg{Title: title, Body: a.Body, Priority: a.Priority, Tags: a.Tags, Click: click, Source: a.SourceName, AlertID: a.ID, Count: a.Count, At: a.ReceivedAt, Kind: "alert"}
}

// sendError says whether trying again later could help.
type sendError struct {
	msg       string
	temporary bool
}

func (e *sendError) Error() string { return e.msg }

func retryable(err error) bool {
	var se *sendError
	if errors.As(err, &se) {
		return se.temporary
	}
	return true // network errors
}

// send delivers one message to one channel.
func (s *Server) send(ctx context.Context, uid int64, c *store.Channel, m outMsg) error {
	if s.cfg.DemoData {
		return nil // demo: nothing leaves the box
	}
	hc := s.outClient(20*time.Second, s.localOK(ctx, uid))
	emo, labels := emoji.Split(m.Tags)
	switch c.Kind {
	case "pushover":
		return s.sendPushover(ctx, hc, c, m, emo, labels)
	case "ntfy":
		return s.sendNtfy(ctx, hc, c, m)
	case "apprise":
		title := m.Title
		if emo != "" {
			title = emo + " " + title
		}
		body := m.Body
		if m.Click != "" {
			body = strings.TrimSpace(body + "\n" + m.Click)
		}
		in := map[string]any{"title": title, "body": firstNonEmpty(body, title), "type": appriseType(m)}
		if t := c.Config["tag"]; t != "" {
			in["tag"] = t
		}
		return s.postJSON(ctx, hc, c.Config["url"], in, nil)
	case "webhook":
		hdr := map[string]string{}
		if sec := c.Config["secret"]; sec != "" {
			hdr["Authorization"] = "Bearer " + sec
		}
		return s.postJSON(ctx, hc, c.Config["url"], map[string]any{
			"app": "pocket-alerts", "kind": m.Kind, "id": m.AlertID, "title": m.Title, "body": m.Body, "priority": m.Priority,
			"importance": priorityName(m.Priority), "tags": m.Tags, "emoji": emo, "click": m.Click, "source": m.Source, "count": m.Count, "received_at": m.At,
		}, hdr)
	}
	return &sendError{msg: "unknown kind of channel " + c.Kind}
}

// appriseType maps importance (and a "done" tag) to Apprise's four types.
func appriseType(m outMsg) string {
	for _, t := range m.Tags {
		switch strings.ToLower(t) {
		case "white_check_mark", "heavy_check_mark", "ballot_box_with_check", "green_circle", "success", "resolved", "ok":
			return "success"
		}
	}
	switch {
	case m.Priority >= 5:
		return "failure"
	case m.Priority == 4:
		return "warning"
	}
	return "info"
}

// pushoverPriority: ntfy 1…5 → Pushover -2…1. Emergency (2) needs an
// acknowledgement and is never used.
var pushoverPriority = map[int]int{1: -2, 2: -1, 3: 0, 4: 0, 5: 1}

const pushoverAPI = "https://api.pushover.net/1/messages.json"

func (s *Server) sendPushover(ctx context.Context, hc *http.Client, c *store.Channel, m outMsg, emo string, labels []string) error {
	title := m.Title
	if emo != "" {
		title = emo + " " + title
	}
	body := strings.TrimSpace(m.Body)
	if body == "" {
		body = title
	}
	if len(labels) > 0 {
		body += "\n🏷 " + strings.Join(labels, ", ")
	}
	prio := pushoverPriority[clampPriority(m.Priority)]
	if p, err := strconv.Atoi(c.Config["max_priority"]); err == nil && c.Config["max_priority"] != "" && prio > p {
		prio = p
	}
	api := firstNonEmpty(c.Config["api"], pushoverAPI) // tests point this elsewhere
	parts := splitText(body, 1024)
	for i, part := range parts {
		t := title
		if len(parts) > 1 {
			t = clipRunes(title, 240) + fmt.Sprintf(" (%d/%d)", i+1, len(parts))
		}
		form := url.Values{"token": {c.Config["token"]}, "user": {c.Config["user"]}, "title": {clipRunes(t, 250)}, "message": {part}, "priority": {strconv.Itoa(prio)}}
		if m.Click != "" {
			form.Set("url", clip(m.Click, 512))
		}
		if d := c.Config["device"]; d != "" {
			form.Set("device", d)
		}
		if snd := c.Config["sound"]; snd != "" {
			form.Set("sound", snd)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, api, strings.NewReader(form.Encode()))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if err := s.do(hc, req); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) sendNtfy(ctx context.Context, hc *http.Client, c *store.Channel, m outMsg) error {
	base := strings.TrimRight(c.Config["url"], "/")
	topic := strings.Trim(c.Config["topic"], "/")
	body := strings.TrimSpace(m.Body)
	if body == "" {
		body = m.Title
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/"+url.PathEscape(topic), strings.NewReader(clip(body, 4000)))
	if err != nil {
		return err
	}
	req.Header.Set("Title", headerSafe(m.Title))
	req.Header.Set("Priority", strconv.Itoa(clampPriority(m.Priority)))
	if len(m.Tags) > 0 {
		req.Header.Set("Tags", headerSafe(strings.Join(m.Tags, ",")))
	}
	if m.Click != "" {
		req.Header.Set("Click", m.Click)
	}
	if tok := c.Config["token"]; tok != "" {
		if user, pass, ok := strings.Cut(tok, ":"); ok {
			req.SetBasicAuth(user, pass)
		} else {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	return s.do(hc, req)
}

// headerSafe keeps a header value on one line; ntfy reads UTF-8 headers.
func headerSafe(v string) string { return clip(oneLine(v), 250) }

func (s *Server) postJSON(ctx context.Context, hc *http.Client, target string, body any, hdr map[string]string) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(b))
	if err != nil {
		return &sendError{msg: "the address is not valid: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return s.do(hc, req)
}

func (s *Server) do(hc *http.Client, req *http.Request) error {
	req.Header.Set("User-Agent", s.cfg.UserAgent)
	res, err := hc.Do(req)
	if err != nil {
		if errors.Is(err, errLocalAddress) {
			return &sendError{msg: errLocalAddress.Error()}
		}
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 400))
		text := strings.TrimSpace(string(b))
		var j struct {
			Errors []string `json:"errors"`
			Error  string   `json:"error"`
		}
		if json.Unmarshal(b, &j) == nil {
			if len(j.Errors) > 0 {
				text = strings.Join(j.Errors, "; ")
			} else if j.Error != "" {
				text = j.Error
			}
		}
		return &sendError{msg: fmt.Sprintf("HTTP %d: %s", res.StatusCode, clip(text, 200)), temporary: res.StatusCode == 429 || res.StatusCode >= 500}
	}
	return nil
}

// splitText cuts long text into parts, at lines or words where it can.
func splitText(body string, limit int) []string {
	var parts []string
	rest := body
	for len(rest) > limit {
		cut := strings.LastIndex(rest[:limit+1], "\n")
		if cut < limit/2 {
			cut = strings.LastIndex(rest[:limit+1], " ")
		}
		if cut < limit/2 {
			cut = limit
			for cut > 0 && !utf8Start(rest[cut]) {
				cut--
			}
		}
		parts = append(parts, strings.TrimRight(rest[:cut], " \n"))
		rest = strings.TrimLeft(rest[cut:], " \n")
	}
	return append(parts, rest)
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// ---- delivery with retries ----

var retryDelays = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute}

// deliverAsync sends an alert to its channels in the background.
func (s *Server) deliverAsync(uid int64, a *store.Alert, chans []store.Channel) {
	if s.cfg.DemoData {
		s.demoDeliver(s.bg, uid, a, chans)
		return
	}
	alert := *a
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(s.bg, 2*time.Minute)
		defer cancel()
		s.deliver(ctx, uid, &alert, chans)
	}()
}

func (s *Server) deliver(ctx context.Context, uid int64, a *store.Alert, chans []store.Channel) {
	m := msgFor(a, s.alertLink(a.ID))
	failed := 0
	for i := range chans {
		c := &chans[i]
		err := s.send(ctx, uid, c, m)
		d := &store.Delivery{AlertID: &a.ID, ChannelID: &c.ID, Channel: c.Name, Kind: "alert", Status: "sent"}
		if err != nil {
			d.Status, d.Error = "failed", err.Error()
			if retryable(err) {
				d.Status = "retrying"
				next := time.Now().Add(retryDelays[0]).UTC().Format(time.RFC3339Nano)
				d.NextAt = &next
			} else {
				failed++
			}
		}
		_ = s.store.AddDelivery(ctx, uid, d)
		s.channelResult(ctx, uid, c, err)
		s.sent.Add(1)
	}
	if failed == len(chans) && failed > 0 {
		_ = s.store.SetAlertState(ctx, uid, a.ID, "failed", "Every channel refused it")
	}
	s.live.send(uid, liveEvent{Type: "alert", ID: a.ID})
}

// channelResult records a send on the channel and, when a channel keeps
// failing, tells the person once a day through Pocket Alerts' own
// notification address.
func (s *Server) channelResult(ctx context.Context, uid int64, c *store.Channel, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	_ = s.store.SetChannelResult(ctx, c.ID, err == nil, msg)
	if err == nil || retryable(err) {
		return
	}
	if s.store.ChannelFailures(ctx, c.ID) < 3 {
		return
	}
	key := fmt.Sprintf("failing:%d:%s", c.ID, time.Now().Format("2006-01-02"))
	if _, _, done := s.store.CacheGet(ctx, key); done {
		return
	}
	_ = s.store.CachePut(ctx, key, "1")
	st, _ := s.store.GetSettings(ctx, uid)
	if !st.NotifyFailing || st.NotifyURL == "" {
		return
	}
	n := notify.New(st.NotifyURL, st.NotifyFormat)
	_ = n.Send(ctx, notify.Message{Title: "Alerts are not reaching " + c.Name, Body: "The last three tries failed: " + msg + "\nCheck it under Send to in Pocket Alerts.",
		Priority: "failure", Tags: []string{"warning"}, Link: s.cfg.BaseURL + "/send-to"})
}

// retryDue tries again the sends that are waiting.
func (s *Server) retryDue(ctx context.Context) {
	due, err := s.store.DueRetries(ctx, store.Now(), 50)
	if err != nil {
		return
	}
	for i := range due {
		d := &due[i]
		if d.AlertID == nil || d.ChannelID == nil {
			d.Status, d.NextAt = "failed", nil
			_ = s.store.UpdateDelivery(ctx, d)
			continue
		}
		a, err1 := s.store.GetAlert(ctx, d.UserID, *d.AlertID)
		c, err2 := s.store.GetChannel(ctx, d.UserID, *d.ChannelID)
		if err1 != nil || err2 != nil || !c.Enabled {
			d.Status, d.NextAt, d.Error = "failed", nil, firstNonEmpty(d.Error, "the alert or channel is gone")
			_ = s.store.UpdateDelivery(ctx, d)
			continue
		}
		err := s.send(ctx, d.UserID, c, msgFor(a, s.alertLink(a.ID)))
		d.Attempts++
		d.At = store.Now()
		switch {
		case err == nil:
			d.Status, d.Error, d.NextAt = "sent", "", nil
		case retryable(err) && d.Attempts <= len(retryDelays):
			next := time.Now().Add(retryDelays[d.Attempts-1]).UTC().Format(time.RFC3339Nano)
			d.Status, d.Error, d.NextAt = "retrying", err.Error(), &next
		default:
			d.Status, d.Error, d.NextAt = "failed", err.Error(), nil
		}
		_ = s.store.UpdateDelivery(ctx, d)
		if d.Status != "retrying" {
			s.channelResult(ctx, d.UserID, c, errOrNil(err, d.Status))
		}
		s.live.send(d.UserID, liveEvent{Type: "alert", ID: a.ID})
	}
}

// errOrNil turns a final retryable failure into a refusal, so the
// failing-channel notice counts it.
func errOrNil(err error, status string) error {
	if status != "failed" || err == nil {
		return err
	}
	return &sendError{msg: err.Error()}
}

// ---- the summary after quiet hours ----

// sendSummaries sends each person whose quiet hours ended one message with
// what came in while it was quiet.
func (s *Server) sendSummaries(ctx context.Context) {
	ids, err := s.store.UsersWithHeld(ctx)
	if err != nil {
		return
	}
	for _, uid := range ids {
		st, err := s.store.GetSettings(ctx, uid)
		if err != nil {
			continue
		}
		if on, _ := quietNow(st, time.Now()); on {
			continue
		}
		s.summarise(ctx, uid)
	}
}

func (s *Server) summarise(ctx context.Context, uid int64) {
	held, err := s.store.HeldAlerts(ctx, uid)
	if err != nil || len(held) == 0 {
		return
	}
	top := 1
	for _, a := range held {
		if a.Priority > top {
			top = a.Priority
		}
	}
	chans, _, _ := s.targets(ctx, uid, top, nil)
	at := time.Now().Format("15:04")
	if len(chans) == 0 {
		for _, a := range held {
			_ = s.store.SetAlertState(ctx, uid, a.ID, "quiet", "Quiet hours (no channel for the summary)")
		}
		s.live.send(uid, liveEvent{Type: "refresh"})
		return
	}
	var b strings.Builder
	for i, a := range held {
		if i == 15 {
			fmt.Fprintf(&b, "…and %d more\n", len(held)-15)
			break
		}
		emo, _ := emoji.Split(a.Tags)
		title := firstNonEmpty(a.Title, strings.SplitN(a.Body, "\n", 2)[0])
		line := "• " + strings.TrimSpace(emo+" "+clipRunes(title, 90))
		if a.Count > 1 {
			line += fmt.Sprintf(" ×%d", a.Count)
		}
		fmt.Fprintf(&b, "%s — %s\n", line, a.SourceName)
	}
	n := len(held)
	title := fmt.Sprintf("While it was quiet: %d alert", n)
	if n != 1 {
		title += "s"
	}
	m := outMsg{Title: title, Body: strings.TrimSpace(b.String()), Priority: 3, Tags: []string{"crescent_moon"}, Click: s.cfg.BaseURL + "/", Kind: "summary", At: store.Now()}
	for i := range chans {
		c := &chans[i]
		err := s.send(ctx, uid, c, m)
		d := &store.Delivery{ChannelID: &c.ID, Channel: c.Name, Kind: "summary", Status: "sent"}
		if err != nil {
			d.Status, d.Error = "failed", err.Error()
		}
		_ = s.store.AddDelivery(ctx, uid, d)
		_ = s.store.SetChannelResult(ctx, c.ID, err == nil, d.Error)
	}
	for _, a := range held {
		_ = s.store.SetAlertState(ctx, uid, a.ID, "sent", "In the summary at "+at)
	}
	s.live.send(uid, liveEvent{Type: "refresh"})
}
