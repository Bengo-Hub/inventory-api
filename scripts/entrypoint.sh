#!/bin/sh
# Entrypoint script for Inventory-API service
# Runs migrations and seed before starting the server

set -e

# Use direct PostgreSQL URL for migrate/seed to bypass PgBouncer transaction mode.
MIGRATE_URL="${POSTGRES_MIGRATE_URL:-$POSTGRES_URL}"

echo "=========================================="
echo "Inventory-API Service Startup"
echo "=========================================="

echo "Waiting for database and running migrations..."
MAX_RETRIES=60
RETRY_COUNT=0

# Captured (not swallowed) so a real migration failure is visible on every attempt -- the
# liveness probe usually kills this container long before MAX_RETRIES is ever reached.
until MIGRATE_OUTPUT=$(POSTGRES_URL="$MIGRATE_URL" /usr/local/bin/inventory-migrate 2>&1) || [ $RETRY_COUNT -eq $MAX_RETRIES ]; do
  RETRY_COUNT=$((RETRY_COUNT+1))
  echo "Migration attempt $RETRY_COUNT/$MAX_RETRIES failed:"
  echo "$MIGRATE_OUTPUT"
  sleep 5
done

if [ $RETRY_COUNT -eq $MAX_RETRIES ]; then
  echo "Migration failed after $MAX_RETRIES attempts. Last error:"
  echo "$MIGRATE_OUTPUT"
  exit 1
fi

echo "Migrations applied successfully"

echo ""
echo "=========================================="
echo "Running seed (idempotent)"
echo "=========================================="
POSTGRES_URL="$MIGRATE_URL" /usr/local/bin/inventory-seed || echo "Seed completed with warnings (non-fatal)"

echo "Syncing media assets to persistent volume..."
mkdir -p "${MEDIA_ROOT:-/data/media}/icons"
mkdir -p "${MEDIA_ROOT:-/data/media}/images"
cp -r ./media/icons/* "${MEDIA_ROOT:-/data/media}/icons/" 2>/dev/null || true
cp -rn ./media/images/* "${MEDIA_ROOT:-/data/media}/images/" 2>/dev/null || true

echo ""
echo "=========================================="
echo "Starting Inventory-API server"
echo "=========================================="
echo ""

exec /usr/local/bin/inventory
