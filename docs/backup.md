# Backup

One SQLite file: `DATA_DIR/pocket-alerts.db` (WAL mode). Take a consistent copy with `pocket-alerts backup` (writes `DATA_DIR/backups/pocket-alerts-<time>.db` with `VACUUM INTO`) or Settings → Backup & data → Download database (admin). Back up that copy, not the live file. `DATA_DIR/core.token*` is the Pocket Core registration.

Restore: stop the app, replace `pocket-alerts.db` (remove `-wal`/`-shm` files), start.

Every person can also export their own data as JSON (`/api/v1/export.json`: sources, places to send to — with their keys —, rules and alerts; re-importable, importing twice adds nothing, imported sources get new addresses) and their alerts as CSV (`/api/v1/export.csv`).
