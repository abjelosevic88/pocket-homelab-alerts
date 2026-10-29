# Configuration

Environment variables; every one has a default. Everything personal — sources, places to send to, rules, quiet hours, Pocket Tasks, AI — lives in the UI per person; the `OPENAI_*`/`NOTIFY_*` values below are copied once into the first admin's settings.

| Variable | Default | What |
|---|---|---|
| `PORT`, `HOST` | `8080`, `0.0.0.0` | Listen address |
| `BASE_URL` | – | Public URL: the addresses sources show, links in pushes, the OIDC callback |
| `DATA_DIR` | `./data` | Database, Core token, backups |
| `DB_PATH` | `$DATA_DIR/pocket-alerts.db` | SQLite file |
| `MAX_BODY_BYTES` | `67108864` | Largest JSON request, e.g. an export being imported (64 MB). Incoming alerts are capped at 1 MB |
| `TZ` | – | The zone quiet hours and days are counted in (mount `/usr/share/zoneinfo` on Alpine) |
| `ALLOW_LOCAL_TARGETS` | `admins` | The server sends to addresses people type in (channels) and follows ntfy servers. By default only admins may use private, loopback, link-local and CGNAT (Tailscale) addresses, so members cannot use it to reach the box's own network. `all` allows everyone, `none` nobody |
| `AUTH_MODE` | `local` | `local` (accounts), `proxy` (trust `AUTH_PROXY_HEADER`/`AUTH_PROXY_EMAIL_HEADER`), `none` |
| `ADMIN_EMAIL`, `ADMIN_PASSWORD`, `ADMIN_NAME` | – | Seed the first admin while the database is empty |
| `SESSION_TTL`, `SECURE_COOKIES` | `720h`, `false` | Set `SECURE_COOKIES=true` behind HTTPS |
| `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_NAME`, `OIDC_AUTO_CREATE` | – | Sign in with an OpenID Connect provider (not needed with Pocket Core) |
| `NOTIFY_URL`, `NOTIFY_FORMAT` | – | Pocket Alerts' own notices (a channel that keeps failing): Apprise (`…/notify/key`), ntfy topic URL or JSON webhook; format `apprise`/`ntfy`/`json` |
| `OPENAI_API_KEY`, `OPENAI_MODEL`, `OPENAI_BASE_URL` | – | "Explain" and questions without Pocket AI |
| `CORE_URL`, `CORE_REGISTRATION_KEY`, `CORE_APP_URL` | – | Pocket Core |
| `TRUST_PROXY`, `METRICS_ENABLED`, `LOG_LEVEL`, `LOG_FORMAT` | `true`, `true`, `info`, `text` | |
| `DEMO_DATA` | `false` | Seed seven sources, three channels, five rules and ten weeks of alerts into the owner account (screenshots, trying it out); nothing is really sent and followers do not connect |

Commands: `pocket-alerts backup [-o FILE]`, `pocket-alerts reset-password [-email] [-password]`, `pocket-alerts version`.

Data in `DATA_DIR`: `pocket-alerts.db`, `backups/`, `core.token*`.

A source accepts at most 300 alerts a minute (a loop in a script must not flood your phone); more get HTTP 429.
