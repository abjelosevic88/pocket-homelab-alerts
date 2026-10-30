package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

const alertCols = `id, user_id, source_id, source_name, ext_id, fingerprint, title, body, priority, tags, click_url, state, reason, rule_id,
	count, received_at, last_seen_at, read_at, done_at, task_url`

func scanAlert(row interface{ Scan(...any) error }) (*Alert, error) {
	var x Alert
	var src, rule sql.NullInt64
	var tags string
	var read, done sql.NullString
	if err := row.Scan(&x.ID, &x.UserID, &src, &x.SourceName, &x.ExtID, &x.Fingerprint, &x.Title, &x.Body, &x.Priority, &tags, &x.ClickURL,
		&x.State, &x.Reason, &rule, &x.Count, &x.ReceivedAt, &x.LastSeenAt, &read, &done, &x.TaskURL); err != nil {
		return nil, notFound(err)
	}
	if src.Valid {
		x.SourceID = &src.Int64
	}
	if rule.Valid {
		x.RuleID = &rule.Int64
	}
	x.ReadAt, x.DoneAt = nullStr(read), nullStr(done)
	_ = json.Unmarshal([]byte(tags), &x.Tags)
	if x.Tags == nil {
		x.Tags = []string{}
	}
	return &x, nil
}

func tagsJSON(t []string) string {
	if t == nil {
		t = []string{}
	}
	b, _ := json.Marshal(t)
	return string(b)
}

// AddAlert stores a new alert.
func (s *Store) AddAlert(ctx context.Context, uid int64, x *Alert) error {
	if x.ReceivedAt == "" {
		x.ReceivedAt = Now()
	}
	if x.LastSeenAt == "" {
		x.LastSeenAt = x.ReceivedAt
	}
	if x.Count < 1 {
		x.Count = 1
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO alerts (user_id, source_id, source_name, ext_id, fingerprint, title, body, priority, tags, click_url,
		state, reason, rule_id, count, received_at, last_seen_at, read_at, done_at, task_url) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uid, nullI(x.SourceID), x.SourceName, x.ExtID, x.Fingerprint, x.Title, x.Body, x.Priority, tagsJSON(x.Tags), x.ClickURL,
		x.State, x.Reason, nullI(x.RuleID), x.Count, x.ReceivedAt, x.LastSeenAt, strOrNil(x.ReadAt), strOrNil(x.DoneAt), x.TaskURL)
	if err != nil {
		return err
	}
	x.ID, _ = res.LastInsertId()
	x.UserID = uid
	return nil
}

// OpenRepeat finds the open alert a new one repeats: same source and
// fingerprint, not done, seen since the given time.
func (s *Store) OpenRepeat(ctx context.Context, uid int64, sourceID *int64, fingerprint, since string) (*Alert, error) {
	if sourceID == nil || fingerprint == "" {
		return nil, ErrNotFound
	}
	return scanAlert(s.DB.QueryRowContext(ctx, `SELECT `+alertCols+` FROM alerts WHERE user_id = ? AND source_id = ? AND fingerprint = ?
		AND done_at IS NULL AND last_seen_at >= ? ORDER BY last_seen_at DESC LIMIT 1`, uid, *sourceID, fingerprint, since))
}

// HasExtID reports whether a person already has an alert with the sender's id.
func (s *Store) HasExtID(ctx context.Context, uid int64, ext string) bool {
	if ext == "" {
		return false
	}
	var n int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM alerts WHERE user_id = ? AND ext_id = ?`, uid, ext).Scan(&n)
	return n > 0
}

// Repeat counts a repeat on an open alert: newer text, unread again.
func (s *Store) Repeat(ctx context.Context, uid int64, x *Alert, body string, priority int, at string) error {
	x.Count++
	x.LastSeenAt = at
	x.Body = body
	if priority > x.Priority {
		x.Priority = priority
	}
	x.ReadAt = nil
	_, err := s.DB.ExecContext(ctx, `UPDATE alerts SET count = ?, last_seen_at = ?, body = ?, priority = ?, read_at = NULL WHERE id = ? AND user_id = ?`,
		x.Count, x.LastSeenAt, x.Body, x.Priority, x.ID, uid)
	return err
}

// GetAlert returns one of a person's alerts.
func (s *Store) GetAlert(ctx context.Context, uid, id int64) (*Alert, error) {
	return scanAlert(s.DB.QueryRowContext(ctx, `SELECT `+alertCols+` FROM alerts WHERE id = ? AND user_id = ?`, id, uid))
}

// AlertFilter narrows the inbox.
type AlertFilter struct {
	View     string // open (default) | unread | important | done | all
	SourceID int64
	Query    string
	From, To string // last_seen_at range (RFC 3339)
	Before   string // last_seen_at < this (paging)
	Limit    int
}

// ListAlerts lists a person's alerts, newest first.
func (s *Store) ListAlerts(ctx context.Context, uid int64, f AlertFilter) ([]Alert, error) {
	q := `SELECT ` + alertCols + ` FROM alerts WHERE user_id = ?`
	args := []any{uid}
	switch f.View {
	case "unread":
		q += ` AND read_at IS NULL AND done_at IS NULL`
	case "important":
		q += ` AND priority >= 4 AND done_at IS NULL`
	case "done":
		q += ` AND done_at IS NOT NULL`
	case "all":
	default:
		q += ` AND done_at IS NULL`
	}
	if f.SourceID > 0 {
		q += ` AND source_id = ?`
		args = append(args, f.SourceID)
	}
	if t := strings.TrimSpace(f.Query); t != "" {
		q += ` AND (title LIKE ? ESCAPE '\' OR body LIKE ? ESCAPE '\' OR source_name LIKE ? ESCAPE '\' OR tags LIKE ? ESCAPE '\')`
		like := "%" + escapeLike(t) + "%"
		args = append(args, like, like, like, like)
	}
	if f.From != "" {
		q += ` AND last_seen_at >= ?`
		args = append(args, f.From)
	}
	if f.To != "" {
		q += ` AND last_seen_at < ?`
		args = append(args, f.To)
	}
	if f.Before != "" {
		q += ` AND last_seen_at < ?`
		args = append(args, f.Before)
	}
	q += ` ORDER BY last_seen_at DESC, id DESC`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	return s.queryAlerts(ctx, q, args...)
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (s *Store) queryAlerts(ctx context.Context, q string, args ...any) ([]Alert, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Alert{}
	for rows.Next() {
		x, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

// HeldAlerts are a person's alerts waiting for the end of quiet hours.
func (s *Store) HeldAlerts(ctx context.Context, uid int64) ([]Alert, error) {
	return s.queryAlerts(ctx, `SELECT `+alertCols+` FROM alerts WHERE user_id = ? AND state = 'held' ORDER BY priority DESC, last_seen_at`, uid)
}

// UsersWithHeld lists the people with alerts waiting.
func (s *Store) UsersWithHeld(ctx context.Context) ([]int64, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT user_id FROM alerts WHERE state = 'held'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetAlertState records what happened to an alert.
func (s *Store) SetAlertState(ctx context.Context, uid, id int64, state, reason string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE alerts SET state = ?, reason = ? WHERE id = ? AND user_id = ?`, state, reason, id, uid)
	return err
}

// MarkAlerts sets read or done on some of a person's alerts (ids empty =
// every alert matching the filter view). on=false clears it.
func (s *Store) MarkAlerts(ctx context.Context, uid int64, ids []int64, what string, on bool) (int, error) {
	col := "read_at"
	if what == "done" {
		col = "done_at"
	}
	val := any(nil)
	if on {
		val = Now()
	}
	q := `UPDATE alerts SET ` + col + ` = ?`
	if what == "done" && on {
		q += `, read_at = COALESCE(read_at, ?)`
	}
	q += ` WHERE user_id = ?`
	args := []any{val}
	if what == "done" && on {
		args = append(args, val)
	}
	args = append(args, uid)
	if on {
		q += ` AND ` + col + ` IS NULL`
	}
	if len(ids) > 0 {
		q += ` AND id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `)`
		for _, id := range ids {
			args = append(args, id)
		}
	} else if what == "read" {
		q += ` AND done_at IS NULL`
	}
	res, err := s.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// SetTaskURL records the task made from an alert.
func (s *Store) SetTaskURL(ctx context.Context, uid, id int64, url string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE alerts SET task_url = ? WHERE id = ? AND user_id = ?`, url, id, uid)
	return err
}

// DeleteAlerts deletes some of a person's alerts.
func (s *Store) DeleteAlerts(ctx context.Context, uid int64, ids []int64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	args := []any{uid}
	for _, id := range ids {
		args = append(args, id)
	}
	res, err := s.DB.ExecContext(ctx, `DELETE FROM alerts WHERE user_id = ? AND id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// PruneAlerts deletes a person's done alerts older than doneBefore and every
// alert older than allBefore ("" = keep).
func (s *Store) PruneAlerts(ctx context.Context, uid int64, doneBefore, allBefore string) error {
	if doneBefore != "" {
		if _, err := s.DB.ExecContext(ctx, `DELETE FROM alerts WHERE user_id = ? AND done_at IS NOT NULL AND last_seen_at < ?`, uid, doneBefore); err != nil {
			return err
		}
	}
	if allBefore != "" {
		if _, err := s.DB.ExecContext(ctx, `DELETE FROM alerts WHERE user_id = ? AND last_seen_at < ? AND state != 'held'`, uid, allBefore); err != nil {
			return err
		}
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM deliveries WHERE user_id = ? AND at < ?`, uid, firstNonEmpty(allBefore, doneBefore))
	return err
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// InboxCounts are the numbers for the sidebar.
type InboxCounts struct {
	Open      int `json:"open"`
	Unread    int `json:"unread"`
	Important int `json:"important"`
	Held      int `json:"held"`
	Today     int `json:"today"`
}

// CountInbox counts a person's inbox; today = alerts seen since dayStart.
func (s *Store) CountInbox(ctx context.Context, uid int64, dayStart string) (InboxCounts, error) {
	var c InboxCounts
	err := s.DB.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(done_at IS NULL), 0),
		COALESCE(SUM(done_at IS NULL AND read_at IS NULL), 0),
		COALESCE(SUM(done_at IS NULL AND priority >= 4), 0),
		COALESCE(SUM(state = 'held'), 0),
		COALESCE(SUM(last_seen_at >= ?), 0)
		FROM alerts WHERE user_id = ?`, dayStart, uid).Scan(&c.Open, &c.Unread, &c.Important, &c.Held, &c.Today)
	return c, err
}

// DayCount is one day's alerts.
type DayCount struct {
	Day     string `json:"day"`
	Alerts  int    `json:"alerts"`
	Sent    int    `json:"sent"`
	Quiet   int    `json:"quiet"`
	Repeats int    `json:"repeats"`
}

// AlertDays counts alerts per local day (received_at) in a range; offset is
// the box's UTC offset as a SQLite modifier ("+02:00").
func (s *Store) AlertDays(ctx context.Context, uid int64, from, to, offset string) ([]DayCount, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT date(received_at, ?) AS d, COUNT(1), COALESCE(SUM(state = 'sent'), 0), COALESCE(SUM(state != 'sent'), 0), COALESCE(SUM(count), 0) - COUNT(1)
		FROM alerts WHERE user_id = ? AND received_at >= ? AND received_at < ? GROUP BY d ORDER BY d`, offset, uid, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DayCount{}
	for rows.Next() {
		var d DayCount
		if err := rows.Scan(&d.Day, &d.Alerts, &d.Sent, &d.Quiet, &d.Repeats); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ---- deliveries ----

// AddDelivery records a try.
func (s *Store) AddDelivery(ctx context.Context, uid int64, d *Delivery) error {
	if d.At == "" {
		d.At = Now()
	}
	if d.Attempts < 1 {
		d.Attempts = 1
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO deliveries (user_id, alert_id, channel_id, channel, kind, status, error, attempts, next_at, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, uid, nullI(d.AlertID), nullI(d.ChannelID), d.Channel, d.Kind, d.Status, d.Error, d.Attempts, strOrNil(d.NextAt), d.At)
	if err != nil {
		return err
	}
	d.ID, _ = res.LastInsertId()
	d.UserID = uid
	return nil
}

// UpdateDelivery saves a retried delivery.
func (s *Store) UpdateDelivery(ctx context.Context, d *Delivery) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE deliveries SET status = ?, error = ?, attempts = ?, next_at = ?, at = ? WHERE id = ?`,
		d.Status, d.Error, d.Attempts, strOrNil(d.NextAt), d.At, d.ID)
	return err
}

const deliveryCols = `id, user_id, alert_id, channel_id, channel, kind, status, error, attempts, next_at, at`

func scanDelivery(row interface{ Scan(...any) error }) (*Delivery, error) {
	var d Delivery
	var alert, ch sql.NullInt64
	var next sql.NullString
	if err := row.Scan(&d.ID, &d.UserID, &alert, &ch, &d.Channel, &d.Kind, &d.Status, &d.Error, &d.Attempts, &next, &d.At); err != nil {
		return nil, notFound(err)
	}
	if alert.Valid {
		d.AlertID = &alert.Int64
	}
	if ch.Valid {
		d.ChannelID = &ch.Int64
	}
	d.NextAt = nullStr(next)
	return &d, nil
}

func (s *Store) queryDeliveries(ctx context.Context, q string, args ...any) ([]Delivery, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// AlertDeliveries lists the tries for one of a person's alerts.
func (s *Store) AlertDeliveries(ctx context.Context, uid, alertID int64) ([]Delivery, error) {
	return s.queryDeliveries(ctx, `SELECT `+deliveryCols+` FROM deliveries WHERE user_id = ? AND alert_id = ? ORDER BY at`, uid, alertID)
}

// RecentDeliveries lists a person's latest tries (optionally one channel's).
func (s *Store) RecentDeliveries(ctx context.Context, uid, channelID int64, limit int) ([]Delivery, error) {
	if channelID > 0 {
		return s.queryDeliveries(ctx, `SELECT `+deliveryCols+` FROM deliveries WHERE user_id = ? AND channel_id = ? ORDER BY at DESC LIMIT ?`, uid, channelID, limit)
	}
	return s.queryDeliveries(ctx, `SELECT `+deliveryCols+` FROM deliveries WHERE user_id = ? ORDER BY at DESC LIMIT ?`, uid, limit)
}

// DueRetries are tries waiting for another go (every person).
func (s *Store) DueRetries(ctx context.Context, now string, limit int) ([]Delivery, error) {
	return s.queryDeliveries(ctx, `SELECT `+deliveryCols+` FROM deliveries WHERE status = 'retrying' AND next_at <= ? ORDER BY next_at LIMIT ?`, now, limit)
}

// ChannelFailures counts a channel's failed tries in a row (newest first).
func (s *Store) ChannelFailures(ctx context.Context, channelID int64) int {
	rows, err := s.DB.QueryContext(ctx, `SELECT status FROM deliveries WHERE channel_id = ? AND status != 'retrying' ORDER BY at DESC LIMIT 20`, channelID)
	if err != nil {
		return 0
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var st string
		if rows.Scan(&st) != nil || st != "failed" {
			break
		}
		n++
	}
	return n
}

// FeedItem is an alert as an ntfy-style subscriber reads it.
type FeedItem struct {
	Alert
	FeedTopic string
}

// FeedAlerts lists alerts of sources that publish on these topics, oldest
// first: after an alert id (afterID > 0) or since a time.
func (s *Store) FeedAlerts(ctx context.Context, topics []string, afterID int64, since string, limit int) ([]FeedItem, error) {
	if len(topics) == 0 {
		return nil, nil
	}
	cols := strings.Fields(strings.ReplaceAll(alertCols, ",", " "))
	for i, c := range cols {
		cols[i] = "a." + c
	}
	q := `SELECT ` + strings.Join(cols, ", ") + `, src.feed_topic FROM alerts a JOIN sources src ON src.id = a.source_id
		WHERE src.feed_topic IN (` + strings.TrimSuffix(strings.Repeat("?,", len(topics)), ",") + `)`
	args := []any{}
	for _, t := range topics {
		args = append(args, t)
	}
	if afterID > 0 {
		q += ` AND a.id > ?`
		args = append(args, afterID)
	} else {
		q += ` AND a.received_at >= ?`
		args = append(args, since)
	}
	q += ` ORDER BY a.id LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FeedItem{}
	for rows.Next() {
		var it FeedItem
		var topic string
		x, err := scanAlert(scanTail{rows, &topic})
		if err != nil {
			return nil, err
		}
		it.Alert, it.FeedTopic = *x, topic
		out = append(out, it)
	}
	return out, rows.Err()
}

// scanTail scans the alert columns plus extra trailing columns.
type scanTail struct {
	r     interface{ Scan(...any) error }
	extra *string
}

func (t scanTail) Scan(dest ...any) error { return t.r.Scan(append(dest, t.extra)...) }
