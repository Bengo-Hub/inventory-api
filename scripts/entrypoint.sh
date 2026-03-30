#!/bin/sh
# Entrypoint script for Inventory-API service
# Runs migrations and seed before starting the server

set -e

echo "=========================================="
echo "Inventory-API Service Startup"
echo "=========================================="

# Wait for database to be ready (with timeout)
echo "Waiting for database connection..."
MAX_RETRIES=60
RETRY_COUNT=0

until /usr/local/bin/inventory-migrate > /dev/null 2>&1 || [ $RETRY_COUNT -eq $MAX_RETRIES ]; do
  RETRY_COUNT=$((RETRY_COUNT+1))
  echo "Database not ready yet or migrations failing... (attempt $RETRY_COUNT/$MAX_RETRIES)"
  sleep 5
done

if [ $RETRY_COUNT -eq $MAX_RETRIES ]; then
  echo "Database connection timeout after $MAX_RETRIES attempts"
  echo "Proceeding to start server anyway (will fail if DB is critical)"
else
  echo "Database connected and migrations completed (attempt $RETRY_COUNT)"
fi

echo ""
echo "=========================================="
echo "Running seed (idempotent)"
echo "=========================================="
/usr/local/bin/inventory-seed || echo "Seed completed with warnings (non-fatal)"

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
