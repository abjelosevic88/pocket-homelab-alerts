-- Open topics: a source may also take alerts at its plain topic name, without
-- the secret key (/<topic>, /notify/<topic>, /hook/<topic>) — what ntfy and
-- Apprise do on a private network. Box-wide names, so only admins set them.
-- feed_topic: the ntfy-style topic its alerts are read on (GET /<topics>/json
-- and /sse), for things that subscribed to ntfy before (a dashboard drawer,
-- a phone relay).
ALTER TABLE sources ADD COLUMN open INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sources ADD COLUMN feed_topic TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX sources_open_topic ON sources(topic) WHERE open = 1 AND topic != '';
CREATE INDEX sources_feed_topic ON sources(feed_topic) WHERE feed_topic != '';
