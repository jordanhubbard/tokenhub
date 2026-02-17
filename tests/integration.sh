#!/usr/bin/env bash
# TokenHub Integration Tests
#
# Runs the production Docker image and verifies core endpoints.
# Usage: make test-integration   (or: bash tests/integration.sh)
#
# Requires: docker

set -euo pipefail

IMAGE="tokenhub:$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
CONTAINER="tokenhub-integration-test"
PORT=18080

cleanup() {
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "=== TokenHub Integration Tests ==="
echo "Image: $IMAGE"

# Start container with a temp SQLite DB (writable by nonroot user).
docker run -d --name "$CONTAINER" \
    -p "$PORT:8080" \
    -e TOKENHUB_LISTEN_ADDR=:8080 \
    -e TOKENHUB_DB_DSN="file:/tmp/tokenhub-test.sqlite" \
    "$IMAGE"

# Wait for server to accept connections (use /metrics — always 200).
echo -n "Waiting for server"
for i in $(seq 1 30); do
    if curl -sf "http://localhost:$PORT/metrics" >/dev/null 2>&1; then
        echo " ready"
        break
    fi
    if [ "$i" -eq 30 ]; then
        echo " TIMEOUT"
        docker logs "$CONTAINER"
        exit 1
    fi
    echo -n "."
    sleep 1
done

PASS=0
FAIL=0

check() {
    local name="$1" method="$2" url="$3" expected_status="$4"
    local status
    status=$(curl -s -o /dev/null -w "%{http_code}" -X "$method" "http://localhost:$PORT$url" 2>/dev/null || echo "000")
    if [ "$status" = "$expected_status" ]; then
        echo "  PASS  $name (HTTP $status)"
        PASS=$((PASS + 1))
    else
        echo "  FAIL  $name (expected $expected_status, got $status)"
        FAIL=$((FAIL + 1))
    fi
}

# ──── Health ────
# 503 is expected: no provider API keys configured in test.
check "GET /healthz (no providers → 503)" GET  /healthz      503

# ──── Admin UI ────
check "GET /admin serves dashboard"       GET  /admin        200

# ──── Metrics ────
check "GET /metrics returns Prometheus"   GET  /metrics      200

# ──── Docs ────
check "GET /docs/ serves documentation"   GET  /docs/        200

# ──── Chat without auth ────
check "POST /v1/chat without key → 401"  POST /v1/chat      401

# ──── Admin API without token ────
check "GET /admin/v1/models no token"     GET  /admin/v1/models  200

# ──── SSE endpoint (uses timeout — SSE connections stay open) ────
SSE_STATUS=$(curl -s -o /dev/null -w "%{http_code}" --max-time 2 "http://localhost:$PORT/admin/v1/events" 2>/dev/null; true)
if [ "$SSE_STATUS" = "200" ]; then
    echo "  PASS  GET /admin/v1/events reachable (HTTP $SSE_STATUS)"
    PASS=$((PASS + 1))
else
    echo "  FAIL  GET /admin/v1/events (expected 200, got $SSE_STATUS)"
    FAIL=$((FAIL + 1))
fi

echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
