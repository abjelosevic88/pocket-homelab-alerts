package store

import (
	"context"
	"database/sql"
	"strings"
)

const sourceCols = `id, user_id, name, kind, topic, push_key, icon, default_priority, enabled, muted, snoozed_until,
	follow_url, follow_topics, follow_token, follow_cursor, app_slug, status, status_note, last_alert_at, created_at, updated_at`

func scanSource(row interface{ Scan(...any) error }) (*Source, error) {
	var x Source
	var snooze, last sql.NullString
	var enabled, muted int
	if err := row.Scan(&x.ID, &x.UserID, &x.Name, &x.Kind, &x.Topic, &x.PushKey, &x.Icon, &x.DefaultPriority, &enabled, &muted, &snooze,
		&x.FollowURL, &x.FollowTopics, &x.FollowToken, &x.FollowCursor, &x.AppSlug, &x.Status, &x.StatusNote, &last, &x.CreatedAt, &x.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	x.Enabled, x.Muted = enabled == 1, muted == 1
	x.SnoozedUntil, x.LastAlertAt = nullStr(snooze), nullStr(last)
	return &x, nil
}

func nullStr(n sql.NullString) *string {
	if !n.Valid || n.String == "" {
		return nil
	}
	v := n.String
	return &v
}

func strOrNil(p *string) any {
	if p == nil || *p == "" {
		return nil
	}
	return *p
}

// NewPushKey is the secret in a source's address.
func NewPushKey() string { return "k" + RandomToken(15) }

// CreateSource adds a source (a new push key when none is set).
func (s *Store) CreateSource(ctx context.Context, uid int64, x *Source) error {
	if x.PushKey == "" {
		x.PushKey = NewPushKey()
	}
	if x.Kind == "" {
		x.Kind = "push"
	}
	now := Now()
	res, err := s.DB.ExecContext(ctx, `INSERT INTO sources (user_id, name, kind, topic, push_key, icon, default_priority, enabled, muted, snoozed_until,
		follow_url, follow_topics, follow_token, follow_cursor, app_slug, status, status_note, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uid, x.Name, x.Kind, x.Topic, x.PushKey, x.Icon, x.DefaultPriority, boolInt(x.Enabled), boolInt(x.Muted), strOrNil(x.SnoozedUntil),
		x.FollowURL, x.FollowTopics, x.FollowToken, x.FollowCursor, x.AppSlug, x.Status, x.StatusNote, now, now)
	if err != nil {
		return conflict(err)
	}
	x.ID, _ = res.LastInsertId()
	x.UserID, x.CreatedAt, x.UpdatedAt = uid, now, now
	return nil
}

// UpdateSource saves a person's source.
func (s *Store) UpdateSource(ctx context.Context, uid int64, x *Source) error {
	x.UpdatedAt = Now()
	res, err := s.DB.ExecContext(ctx, `UPDATE sources SET name = ?, topic = ?, icon = ?, default_priority = ?, enabled = ?, muted = ?, snoozed_until = ?,
		follow_url = ?, follow_topics = ?, follow_token = ?, follow_cursor = ?, push_key = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		x.Name, x.Topic, x.Icon, x.DefaultPriority, boolInt(x.Enabled), boolInt(x.Muted), strOrNil(x.SnoozedUntil),
		x.FollowURL, x.FollowTopics, x.FollowToken, x.FollowCursor, x.PushKey, x.UpdatedAt, x.ID, uid)
	if err != nil {
		return conflict(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetSource returns one of a person's sources.
func (s *Store) GetSource(ctx context.Context, uid, id int64) (*Source, error) {
	return scanSource(s.DB.QueryRowContext(ctx, `SELECT `+sourceCols+` FROM sources WHERE id = ? AND user_id = ?`, id, uid))
}

// SourceByKey finds a source by its secret key (any person).
func (s *Store) SourceByKey(ctx context.Context, key string) (*Source, error) {
	if key == "" {
		return nil, ErrNotFound
	}
	return scanSource(s.DB.QueryRowContext(ctx, `SELECT `+sourceCols+` FROM sources WHERE push_key = ?`, key))
}

// SourceByTopic finds a person's push source for an ntfy-style topic.
func (s *Store) SourceByTopic(ctx context.Context, uid int64, topic string) (*Source, error) {
	return scanSource(s.DB.QueryRowContext(ctx, `SELECT `+sourceCols+` FROM sources WHERE user_id = ? AND kind = 'push' AND topic = ?`, uid, topic))
}

// SourceByApp finds a person's source for a Pocket app.
func (s *Store) SourceByApp(ctx context.Context, uid int64, slug string) (*Source, error) {
	return scanSource(s.DB.QueryRowContext(ctx, `SELECT `+sourceCols+` FROM sources WHERE user_id = ? AND app_slug = ?`, uid, slug))
}

// ListSources lists a person's sources.
func (s *Store) ListSources(ctx context.Context, uid int64) ([]Source, error) {
	return s.querySources(ctx, `SELECT `+sourceCols+` FROM sources WHERE user_id = ? ORDER BY kind = 'app', lower(name)`, uid)
}

// SourcesOfKind lists every person's sources of one kind (background work).
func (s *Store) SourcesOfKind(ctx context.Context, kind string) ([]Source, error) {
	return s.querySources(ctx, `SELECT `+sourceCols+` FROM sources WHERE kind = ? ORDER BY id`, kind)
}

func (s *Store) querySources(ctx context.Context, q string, args ...any) ([]Source, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Source{}
	for rows.Next() {
		x, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

// DeleteSource removes a source; its alerts stay (without a source).
func (s *Store) DeleteSource(ctx context.Context, uid, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM sources WHERE id = ? AND user_id = ?`, id, uid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetSourceStatus records how a follower or app source is doing.
func (s *Store) SetSourceStatus(ctx context.Context, id int64, status, note string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE sources SET status = ?, status_note = ? WHERE id = ?`, status, note, id)
	return err
}

// SetSourceCursor keeps the last ntfy message id seen.
func (s *Store) SetSourceCursor(ctx context.Context, id int64, cursor string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE sources SET follow_cursor = ? WHERE id = ?`, cursor, id)
	return err
}

// TouchSource records an alert's arrival.
func (s *Store) TouchSource(ctx context.Context, id int64, at string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE sources SET last_alert_at = ? WHERE id = ?`, at, id)
	return err
}

// SourceStat is a source's alerts over a stretch of time.
type SourceStat struct {
	SourceID int64 `json:"source_id"`
	Alerts   int   `json:"alerts"`
	Repeats  int   `json:"repeats"`
	Open     int   `json:"open"`
}

// SourceStats counts each source's alerts since a time.
func (s *Store) SourceStats(ctx context.Context, uid int64, since string) (map[int64]SourceStat, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT source_id, COUNT(1), COALESCE(SUM(count), 0) - COUNT(1), COALESCE(SUM(done_at IS NULL), 0)
		FROM alerts WHERE user_id = ? AND last_seen_at >= ? AND source_id IS NOT NULL GROUP BY source_id`, uid, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]SourceStat{}
	for rows.Next() {
		var x SourceStat
		if err := rows.Scan(&x.SourceID, &x.Alerts, &x.Repeats, &x.Open); err != nil {
			return nil, err
		}
		out[x.SourceID] = x
	}
	return out, rows.Err()
}

// CleanTopic makes an ntfy-style topic name: letters, digits, - and _.
func CleanTopic(t string) string {
	t = strings.TrimSpace(t)
	var b strings.Builder
	for _, r := range t {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}
