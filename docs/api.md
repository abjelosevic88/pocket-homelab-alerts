# API

## Sending alerts in

Every source has a secret key; the address is the secret (make a new one under Sources → the source → New address).
No token is needed for these. A source takes at most 300 alerts a minute.

| Shape | Call | Body |
| :--- | :--- | :--- |
| Apprise API | `POST /notify/<key>` | JSON or form: `title`, `body`, `type` (`info`, `success`, `warning`, `failure` → normal, normal, high, high), `tag`. Pocket apps' `NOTIFY_URL` with `NOTIFY_FORMAT=apprise` work as they are |
| ntfy | `POST` or `PUT /<key>` | The text as the body; `Title`, `Priority` (1–5 or `min low default high max/urgent`), `Tags` (comma separated; emoji short codes show as emoji), `Click` headers — also `X-…` headers and `?title=&p=&tags=` |
| ntfy JSON | `POST /` | `{"topic":"<key>","title":…,"message":…,"priority":4,"tags":[…],"click":…}` — what Apprise's `ntfys://host/<key>` sends |
| Webhook | `POST /hook/<key>` | JSON (`title`, `message`/`body`/`text`, `priority`/`severity`, `tags`, `click`/`url`, `id`) or plain text |

ntfy-style topics of your own: `POST /<topic>` with your API token (`Authorization: Bearer pal_…`, or as the
password with any user name, or ntfy's `?auth=`). A new topic becomes a source. Answers are ntfy's
(`{"id","event":"message","topic",…}`).

### Open topics (replacing ntfy and Apprise without touching the senders)

An admin can open a push source at its plain topic name (source page → "Old ntfy and Apprise addresses"):
then `/<topic>`, `/notify/<topic>`, `/hook/<topic>` and ntfy's JSON `{"topic":"<topic>"}` work without the key,
the way an ntfy server or an Apprise gateway does on a private network. Open names are box-wide and unique.

### Reading alerts the ntfy way

A source with a feed topic shows its alerts at `GET /<topics>/json` and `GET /<topics>/sse` (comma-separated
topics, like ntfy): `poll=1` answers and closes; `since=all|12h|<message id>|<unix time>`; without `poll` the
connection stays open with `open` / `keepalive` events like ntfy. No token (like ntfy's open topics), CORS open.
Messages are ntfy's (`id`, `time`, `event`, `topic`, `title`, `message`, `priority`, `tags`, `click`); an id this
server did not give out counts as "from now". `GET /v1/health` answers like ntfy.

## The JSON API

Under `/api/v1`. Authenticate with the session cookie (browser), `Authorization: Bearer <token>`
(`pal_…`, made under Settings → API & shortcuts), or a Pocket Core token. Types: `web/src/lib/types.ts`.
Everything is per person: another person's ids answer 404. Times are RFC 3339 UTC; quiet hours and days are
counted in the server's `TZ`. Importance is 1 (lowest) to 5 (urgent).

### Inbox

| Call | What |
| :--- | :--- |
| `GET /dashboard` | `counts {open, unread, important, held, today}`, whether quiet hours are on and until when, sources and channels (and how many fail) |
| `GET /alerts?view=open\|unread\|important\|done\|all&source=&q=&before=&limit=` | Newest first; `next` = the `before` for the next page |
| `POST /alerts` | `{title, body, priority, tags, click, source_id?}` — send yourself an alert (it goes through rules and quiet hours like any other) |
| `GET /alerts/{id}` | The alert, its sends (`deliveries`), its source and the rule that decided |
| `POST /alerts/mark` | `{what: "read"\|"done", on?: true, ids?: […]}` — no ids = every open alert |
| `POST /alerts/delete` `{ids}`, `DELETE /alerts/{id}` | Delete |
| `POST /alerts/{id}/quiet` | A rule that keeps alerts like this one quiet (same source, same words) |
| `POST /alerts/{id}/task` | The alert as a task in Pocket Tasks → `{ok, url}` |
| `POST /alerts/{id}/explain` | What it means and what to do, from Pocket AI or your OpenAI key → `{ok, text}` |
| `GET /history?days=30` | Alerts per day (sent / kept quiet / repeats), the busiest sources, the latest sends |
| `GET /day/{date}` | One day — what Pocket Day reads |
| `GET /api/live` | Server-sent events for the open pages: `{"type":"alert","id":…,"new":true}`, `refresh`, `source` |

### Sources

| Call | What |
| :--- | :--- |
| `GET /sources` | Yours, with their addresses and the last week's numbers |
| `POST /sources` | `{kind:"push", name, topic?, open?, feed_topic?}` (open/feed_topic: admins) or `{kind:"ntfy", name, follow_url, follow_topics, follow_token?, backfill?}` |
| `GET/PUT/DELETE /sources/{id}` | `name icon default_priority enabled muted` (+ the follow fields) |
| `POST /sources/{id}/key` | A new secret address; the old one stops working |
| `POST /sources/{id}/snooze` | `{minutes}` or `{until:"tomorrow"}`; `{minutes:0}` wakes it up |
| `POST /sources/{id}/test` | A test alert through rules, quiet hours and channels |
| `GET /apps`, `PUT /apps/{slug}` `{on}` | The Pocket apps whose alerts come in through Pocket Core |

### Send to (channels) and rules

| Call | What |
| :--- | :--- |
| `GET/POST /channels`, `PUT/DELETE /channels/{id}` | `{name, kind: pushover\|ntfy\|apprise\|webhook, config, min_priority, enabled}`. Config: Pushover `user token device? sound? max_priority?`; ntfy `url topic token?`; Apprise `url tag?`; webhook `url secret?` (sent as Bearer). Secrets come back masked |
| `POST /channels/{id}/test`, `POST /channels/test` | Send a test now (the second with unsaved settings) |
| `POST /channels/reorder` `{ids}`, `GET /channels/{id}/deliveries` | |
| `GET/POST /rules`, `PUT/DELETE /rules/{id}` | `{name, enabled, match {source_ids, min_priority, max_priority, words, tags}, action: send\|quiet\|drop, priority, channel_ids, break_quiet}` — checked in order, the first match decides |
| `POST /rules/reorder` `{ids}`, `POST /rules/preview` `{match}` | What a rule would have caught in the last 30 days |

### Settings and data

`GET/PUT /settings` (quiet hours `quiet_*`, `group_minutes`, `send_min_priority`, `keep_*_days`, Pocket Tasks, AI),
`GET /export.json`, `GET /export.csv`, `POST /import`, `GET /backup` (admin), `GET/POST/DELETE /tokens`, `GET /ai/status`, `POST /ai/ask`.

## Pocket Core

Offers `alerts:read` (`/dashboard /alerts /alerts/{id} /sources /day/{date}`) and `alerts:write` (`POST /alerts` — the
acting app becomes the source). Publishes `alerts.received` (every new alert: `alert_id title body priority source
state tags url`) and `alerts.done`. Uses `tasks:write` on Pocket Tasks and `events:read` on Pocket Monitoring, Backups,
Documents, Subtitles and AI (their alert events become alerts; a "resolved" event closes the alert it resolves).
