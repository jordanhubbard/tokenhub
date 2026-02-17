#!/usr/bin/env bash
# TokenHub SQLite Backup Script
#
# Usage:
#   ./scripts/backup.sh [db_path] [backup_dir]
#
# Defaults:
#   db_path:    /data/tokenhub.sqlite
#   backup_dir: /backups
#
# Safe to run while TokenHub is running (uses SQLite .backup command).
# Recommended: run via cron every 6 hours.
#
# Example crontab entry:
#   0 */6 * * * /app/scripts/backup.sh /data/tokenhub.sqlite /backups

set -euo pipefail

DB_PATH="${1:-/data/tokenhub.sqlite}"
BACKUP_DIR="${2:-/backups}"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
BACKUP_FILE="${BACKUP_DIR}/tokenhub-${TIMESTAMP}.sqlite"

# Ensure backup directory exists.
mkdir -p "${BACKUP_DIR}"

# Verify source database exists.
if [ ! -f "${DB_PATH}" ]; then
  echo "ERROR: Database not found at ${DB_PATH}" >&2
  exit 1
fi

# Use SQLite .backup for a consistent snapshot (safe with WAL mode).
sqlite3 "${DB_PATH}" ".backup '${BACKUP_FILE}'"

# Compress the backup.
gzip "${BACKUP_FILE}"

# Prune backups older than 30 days.
find "${BACKUP_DIR}" -name "tokenhub-*.sqlite.gz" -mtime +30 -delete

BACKUP_SIZE=$(du -h "${BACKUP_FILE}.gz" | cut -f1)
echo "Backup complete: ${BACKUP_FILE}.gz (${BACKUP_SIZE})"
