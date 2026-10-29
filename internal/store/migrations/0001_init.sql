CREATE TABLE users (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  email         TEXT NOT NULL UNIQUE,
  name          TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL DEFAULT '',
  role          TEXT NOT NULL DEFAULT 'member',
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at    TEXT
);

CREATE TABLE sessions (
  id         TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE api_tokens (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL DEFAULT '',
  token_hash   TEXT NOT NULL UNIQUE,
  prefix       TEXT NOT NULL DEFAULT '',
  last_used_at TEXT,
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- Box-wide settings (title, setup flag); everything else is personal.
CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT ''
);

CREATE TABLE user_settings (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  key     TEXT NOT NULL,
  value   TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (user_id, key)
);

CREATE TABLE cache (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL DEFAULT '',
  fetched_at TEXT NOT NULL
);


-- Where alerts come from. kind:
--   push  — anything that posts to /notify/<key> (Apprise style), /hook/<key>
--           or ntfy style to /<topic> with the person's token; topic is the
--           ntfy topic name ("" for key-only sources)
--   ntfy  — Pocket Alerts follows topics on an ntfy server (follow_*)
--   app   — events from a Pocket app through Pocket Core (app_slug)
-- push_key is the secret in the address; muted keeps alerts quiet,
-- snoozed_until keeps them quiet until then.
CREATE TABLE sources (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id          INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name             TEXT NOT NULL,
  kind             TEXT NOT NULL DEFAULT 'push',
  topic            TEXT NOT NULL DEFAULT '',
  push_key         TEXT NOT NULL UNIQUE,
  icon             TEXT NOT NULL DEFAULT '',
  default_priority INTEGER NOT NULL DEFAULT 0,      -- 0 = as sent
  enabled          INTEGER NOT NULL DEFAULT 1,      -- off = nothing comes in
  muted            INTEGER NOT NULL DEFAULT 0,
  snoozed_until    TEXT,
  follow_url       TEXT NOT NULL DEFAULT '',
  follow_topics    TEXT NOT NULL DEFAULT '',
  follow_token     TEXT NOT NULL DEFAULT '',
  follow_cursor    TEXT NOT NULL DEFAULT '',        -- the last ntfy message id
  app_slug         TEXT NOT NULL DEFAULT '',
  status           TEXT NOT NULL DEFAULT '',        -- ok | error | "" (followers and apps)
  status_note      TEXT NOT NULL DEFAULT '',
  last_alert_at    TEXT,
  created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX sources_user ON sources(user_id, name);
CREATE UNIQUE INDEX sources_user_topic ON sources(user_id, kind, topic) WHERE topic != '';
CREATE UNIQUE INDEX sources_user_app ON sources(user_id, app_slug) WHERE app_slug != '';

-- One alert. Repeats of the same alert (same source and fingerprint) while
-- it is still open within the grouping window count up instead of adding a
-- row. state says what happened to it:
--   sent   — went to the person's phone (one or more channels)
--   quiet  — kept in the inbox only (low importance, muted, snoozed or a rule)
--   held   — waiting for the end of quiet hours (then one summary)
--   none   — no channel would take it
CREATE TABLE alerts (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  source_id    INTEGER REFERENCES sources(id) ON DELETE SET NULL,
  source_name  TEXT NOT NULL DEFAULT '',
  ext_id       TEXT NOT NULL DEFAULT '',          -- the sender's message id (ntfy), for de-duplication
  fingerprint  TEXT NOT NULL DEFAULT '',
  title        TEXT NOT NULL DEFAULT '',
  body         TEXT NOT NULL DEFAULT '',
  priority     INTEGER NOT NULL DEFAULT 3,        -- 1 lowest … 5 urgent (ntfy's scale)
  tags         TEXT NOT NULL DEFAULT '[]',        -- JSON
  click_url    TEXT NOT NULL DEFAULT '',
  state        TEXT NOT NULL DEFAULT 'sent',
  reason       TEXT NOT NULL DEFAULT '',          -- why it was kept quiet / held, in words
  rule_id      INTEGER REFERENCES rules(id) ON DELETE SET NULL,
  count        INTEGER NOT NULL DEFAULT 1,
  received_at  TEXT NOT NULL,
  last_seen_at TEXT NOT NULL,
  read_at      TEXT,
  done_at      TEXT,
  task_url     TEXT NOT NULL DEFAULT ''           -- the Pocket Tasks task made from it
);
CREATE INDEX alerts_user_seen ON alerts(user_id, last_seen_at);
CREATE INDEX alerts_user_open ON alerts(user_id, done_at, last_seen_at);
CREATE INDEX alerts_user_source ON alerts(user_id, source_id, last_seen_at);
CREATE INDEX alerts_user_group ON alerts(user_id, source_id, fingerprint, last_seen_at);
CREATE INDEX alerts_user_ext ON alerts(user_id, ext_id) WHERE ext_id != '';
CREATE INDEX alerts_user_held ON alerts(user_id, state) WHERE state = 'held';

-- Where alerts go: a phone app (Pushover, ntfy), an Apprise gateway or any
-- webhook. min_priority = only alerts at least this important.
CREATE TABLE channels (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  kind         TEXT NOT NULL,                     -- pushover | ntfy | apprise | webhook
  config       TEXT NOT NULL DEFAULT '{}',        -- JSON, secrets included (never sent back unmasked)
  min_priority INTEGER NOT NULL DEFAULT 1,
  enabled      INTEGER NOT NULL DEFAULT 1,
  position     INTEGER NOT NULL DEFAULT 0,
  last_status  TEXT NOT NULL DEFAULT '',          -- ok | error
  last_error   TEXT NOT NULL DEFAULT '',
  last_sent_at TEXT,
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX channels_user ON channels(user_id, position);

-- What to do with alerts that match, in order; the first match wins.
CREATE TABLE rules (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,
  enabled     INTEGER NOT NULL DEFAULT 1,
  position    INTEGER NOT NULL DEFAULT 0,
  match       TEXT NOT NULL DEFAULT '{}',         -- JSON, see store.RuleMatch
  action      TEXT NOT NULL DEFAULT 'send',       -- send | quiet | drop
  priority    INTEGER NOT NULL DEFAULT 0,         -- 0 = keep; else set to this
  channel_ids TEXT NOT NULL DEFAULT '[]',         -- JSON; empty = the usual channels
  break_quiet INTEGER NOT NULL DEFAULT 0,         -- send even during quiet hours
  hits        INTEGER NOT NULL DEFAULT 0,
  last_hit_at TEXT,
  created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX rules_user ON rules(user_id, position);

-- Each try to send an alert (or a quiet-hours summary) to a channel.
CREATE TABLE deliveries (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  alert_id   INTEGER REFERENCES alerts(id) ON DELETE CASCADE,
  channel_id INTEGER REFERENCES channels(id) ON DELETE SET NULL,
  channel    TEXT NOT NULL DEFAULT '',            -- the channel name at the time
  kind       TEXT NOT NULL DEFAULT 'alert',       -- alert | summary | test
  status     TEXT NOT NULL,                       -- sent | failed | retrying
  error      TEXT NOT NULL DEFAULT '',
  attempts   INTEGER NOT NULL DEFAULT 1,
  next_at    TEXT,                                -- the next try (retrying)
  at         TEXT NOT NULL
);
CREATE INDEX deliveries_user_at ON deliveries(user_id, at);
CREATE INDEX deliveries_alert ON deliveries(alert_id);
CREATE INDEX deliveries_retry ON deliveries(status, next_at) WHERE status = 'retrying';
