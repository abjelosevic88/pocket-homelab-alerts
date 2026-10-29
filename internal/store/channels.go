package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

const channelCols = `id, user_id, name, kind, config, min_priority, enabled, position, last_status, last_error, last_sent_at, created_at, updated_at`

func scanChannel(row interface{ Scan(...any) error }) (*Channel, error) {
	var c Channel
	var cfg string
	var enabled int
	var last sql.NullString
	if err := row.Scan(&c.ID, &c.UserID, &c.Name, &c.Kind, &cfg, &c.MinPriority, &enabled, &c.Position, &c.LastStatus, &c.LastError, &last, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	c.Enabled = enabled == 1
	c.LastSentAt = nullStr(last)
	_ = json.Unmarshal([]byte(cfg), &c.Config)
	if c.Config == nil {
		c.Config = map[string]string{}
	}
	return &c, nil
}

func configJSON(m map[string]string) string {
	if m == nil {
		m = map[string]string{}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// CreateChannel adds a channel at the end of the person's list.
func (s *Store) CreateChannel(ctx context.Context, uid int64, c *Channel) error {
	now := Now()
	var pos int
	_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 1 FROM channels WHERE user_id = ?`, uid).Scan(&pos)
	res, err := s.DB.ExecContext(ctx, `INSERT INTO channels (user_id, name, kind, config, min_priority, enabled, position, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uid, c.Name, c.Kind, configJSON(c.Config), c.MinPriority, boolInt(c.Enabled), pos, now, now)
	if err != nil {
		return err
	}
	c.ID, _ = res.LastInsertId()
	c.UserID, c.Position, c.CreatedAt, c.UpdatedAt = uid, pos, now, now
	return nil
}

// UpdateChannel saves a person's channel.
func (s *Store) UpdateChannel(ctx context.Context, uid int64, c *Channel) error {
	c.UpdatedAt = Now()
	res, err := s.DB.ExecContext(ctx, `UPDATE channels SET name = ?, config = ?, min_priority = ?, enabled = ?, position = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		c.Name, configJSON(c.Config), c.MinPriority, boolInt(c.Enabled), c.Position, c.UpdatedAt, c.ID, uid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetChannel returns one of a person's channels.
func (s *Store) GetChannel(ctx context.Context, uid, id int64) (*Channel, error) {
	return scanChannel(s.DB.QueryRowContext(ctx, `SELECT `+channelCols+` FROM channels WHERE id = ? AND user_id = ?`, id, uid))
}

// ListChannels lists a person's channels in their order.
func (s *Store) ListChannels(ctx context.Context, uid int64) ([]Channel, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+channelCols+` FROM channels WHERE user_id = ? ORDER BY position, id`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// DeleteChannel removes a person's channel.
func (s *Store) DeleteChannel(ctx context.Context, uid, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM channels WHERE id = ? AND user_id = ?`, id, uid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetChannelResult records the last send.
func (s *Store) SetChannelResult(ctx context.Context, id int64, ok bool, errText string) error {
	status := "ok"
	if !ok {
		status = "error"
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE channels SET last_status = ?, last_error = ?, last_sent_at = ? WHERE id = ?`, status, errText, Now(), id)
	return err
}

// ReorderChannels puts a person's channels in this order.
func (s *Store) ReorderChannels(ctx context.Context, uid int64, ids []int64) error {
	return s.reorder(ctx, "channels", uid, ids)
}

func (s *Store) reorder(ctx context.Context, table string, uid int64, ids []int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET position = ? WHERE id = ? AND user_id = ?`, i+1, id, uid); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// ---- rules ----

const ruleCols = `id, user_id, name, enabled, position, match, action, priority, channel_ids, break_quiet, hits, last_hit_at, created_at, updated_at`

func scanRule(row interface{ Scan(...any) error }) (*Rule, error) {
	var r Rule
	var match, chans string
	var enabled, brk int
	var last sql.NullString
	if err := row.Scan(&r.ID, &r.UserID, &r.Name, &enabled, &r.Position, &match, &r.Action, &r.Priority, &chans, &brk, &r.Hits, &last, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	r.Enabled, r.BreakQuiet = enabled == 1, brk == 1
	r.LastHitAt = nullStr(last)
	_ = json.Unmarshal([]byte(match), &r.Match)
	_ = json.Unmarshal([]byte(chans), &r.ChannelIDs)
	if r.ChannelIDs == nil {
		r.ChannelIDs = []int64{}
	}
	if r.Match.SourceIDs == nil {
		r.Match.SourceIDs = []int64{}
	}
	return &r, nil
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// CreateRule adds a rule at the end of the person's list.
func (s *Store) CreateRule(ctx context.Context, uid int64, r *Rule) error {
	now := Now()
	var pos int
	_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 1 FROM rules WHERE user_id = ?`, uid).Scan(&pos)
	if r.ChannelIDs == nil {
		r.ChannelIDs = []int64{}
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO rules (user_id, name, enabled, position, match, action, priority, channel_ids, break_quiet, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, uid, r.Name, boolInt(r.Enabled), pos, jsonOf(r.Match), r.Action, r.Priority, jsonOf(r.ChannelIDs), boolInt(r.BreakQuiet), now, now)
	if err != nil {
		return err
	}
	r.ID, _ = res.LastInsertId()
	r.UserID, r.Position, r.CreatedAt, r.UpdatedAt = uid, pos, now, now
	return nil
}

// UpdateRule saves a person's rule.
func (s *Store) UpdateRule(ctx context.Context, uid int64, r *Rule) error {
	r.UpdatedAt = Now()
	if r.ChannelIDs == nil {
		r.ChannelIDs = []int64{}
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE rules SET name = ?, enabled = ?, match = ?, action = ?, priority = ?, channel_ids = ?, break_quiet = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		r.Name, boolInt(r.Enabled), jsonOf(r.Match), r.Action, r.Priority, jsonOf(r.ChannelIDs), boolInt(r.BreakQuiet), r.UpdatedAt, r.ID, uid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetRule returns one of a person's rules.
func (s *Store) GetRule(ctx context.Context, uid, id int64) (*Rule, error) {
	return scanRule(s.DB.QueryRowContext(ctx, `SELECT `+ruleCols+` FROM rules WHERE id = ? AND user_id = ?`, id, uid))
}

// ListRules lists a person's rules in order.
func (s *Store) ListRules(ctx context.Context, uid int64) ([]Rule, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+ruleCols+` FROM rules WHERE user_id = ? ORDER BY position, id`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// DeleteRule removes a person's rule.
func (s *Store) DeleteRule(ctx context.Context, uid, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM rules WHERE id = ? AND user_id = ?`, id, uid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RuleHit counts a match.
func (s *Store) RuleHit(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE rules SET hits = hits + 1, last_hit_at = ? WHERE id = ?`, Now(), id)
	return err
}

// ReorderRules puts a person's rules in this order.
func (s *Store) ReorderRules(ctx context.Context, uid int64, ids []int64) error {
	return s.reorder(ctx, "rules", uid, ids)
}
