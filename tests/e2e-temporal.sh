#!/usr/bin/env bash
# TokenHub End-to-End Temporal Workflow Test
#
# Spins up the full stack (Temporal + mock provider + TokenHub) and verifies
# that a chat request flows through a Temporal workflow end-to-end.
#
# Usage: make test-e2e   (or: bash tests/e2e-temporal.sh)
#
# Requires: docker, docker-compose (or docker compose plugin)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
COMPOSE_FILE="$SCRIPT_DIR/docker-compose.e2e.yaml"
PROJECT_NAME="tokenhub-e2e"
PORT=18081
IMAGE="tokenhub:e2e"

export TOKENHUB_IMAGE="$IMAGE"
export E2E_PORT="$PORT"

# Detect docker-compose vs docker compose.
if command -v docker-compose >/dev/null 2>&1; then
    DC="docker-compose -p $PROJECT_NAME -f $COMPOSE_FILE"
elif docker compose version >/dev/null 2>&1; then
    DC="docker compose -p $PROJECT_NAME -f $COMPOSE_FILE"
else
    echo "ERROR: neither docker-compose nor docker compose found"
    exit 1
fi

PASS=0
FAIL=0

pass() {
    echo "  PASS  $1"
    PASS=$((PASS + 1))
}

fail() {
    echo "  FAIL  $1"
    FAIL=$((FAIL + 1))
}

cleanup() {
    echo ""
    echo "--- Cleaning up ---"
    $DC down -v --remove-orphans 2>/dev/null || true
}
trap cleanup EXIT

echo "=== TokenHub End-to-End Temporal Test ==="

# ──── Step 1: Build the tokenhub image ────
echo ""
echo "--- Building tokenhub image ---"
docker build -q -t "$IMAGE" "$PROJECT_DIR" >/dev/null
echo "Image: $IMAGE"

# ──── Step 2: Start the full stack ────
echo ""
echo "--- Starting stack (Temporal + mock provider + TokenHub) ---"
$DC up -d 2>&1

# ──── Step 3: Wait for all services ────
echo -n "Waiting for Temporal"
for i in $(seq 1 90); do
    # Check if the temporal container is healthy via docker inspect.
    HEALTH=$(docker inspect --format='{{.State.Health.Status}}' "${PROJECT_NAME}-temporal-1" 2>/dev/null || \
             docker inspect --format='{{.State.Health.Status}}' "${PROJECT_NAME}_temporal_1" 2>/dev/null || \
             echo "unknown")
    if [ "$HEALTH" = "healthy" ]; then
        echo " ready"
        break
    fi
    if [ "$i" -eq 90 ]; then
        echo " TIMEOUT"
        $DC logs temporal 2>&1 | tail -20
        exit 1
    fi
    echo -n "."
    sleep 2
done

echo -n "Waiting for TokenHub"
for i in $(seq 1 60); do
    if curl -sf "http://localhost:$PORT/metrics" >/dev/null 2>&1; then
        echo " ready"
        break
    fi
    if [ "$i" -eq 60 ]; then
        echo " TIMEOUT"
        $DC logs tokenhub 2>&1 | tail -20
        exit 1
    fi
    echo -n "."
    sleep 2
done

# Verify Temporal is connected by checking logs.
if $DC logs tokenhub 2>&1 | grep -q "temporal workflow engine"; then
    pass "Temporal workflow engine started"
else
    fail "Temporal workflow engine not started (check logs)"
fi

# ──── Step 4: Register a model for the vLLM mock provider ────
echo ""
echo "--- Registering mock model ---"
MODEL_RESP=$(curl -s -X POST "http://localhost:$PORT/admin/v1/models" \
    -H "Content-Type: application/json" \
    -d '{"id":"mock-model","provider_id":"vllm","weight":5,"max_context_tokens":4096,"enabled":true}')

if echo "$MODEL_RESP" | grep -q '"ok"'; then
    pass "Register mock model via admin API"
else
    fail "Register mock model: $MODEL_RESP"
fi

# ──── Step 5: Create an API key ────
echo ""
echo "--- Creating API key ---"
KEY_RESP=$(curl -s -X POST "http://localhost:$PORT/admin/v1/apikeys" \
    -H "Content-Type: application/json" \
    -d '{"name":"e2e-test","scopes":"[\"chat\",\"plan\"]"}')

API_KEY=$(echo "$KEY_RESP" | grep -o '"key":"[^"]*"' | head -1 | cut -d'"' -f4)
if [ -n "$API_KEY" ]; then
    pass "Create API key (prefix: ${API_KEY:0:16}...)"
else
    fail "Create API key: $KEY_RESP"
    echo "FATAL: Cannot continue without API key"
    exit 1
fi

# ──── Step 6: Send a chat request (should route through Temporal) ────
echo ""
echo "--- Sending chat request via Temporal workflow ---"
CHAT_RESP=$(curl -s -X POST "http://localhost:$PORT/v1/chat" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $API_KEY" \
    -d '{"request":{"messages":[{"role":"user","content":"Hello from e2e test"}]}}')

# Check response contains mock provider content.
if echo "$CHAT_RESP" | grep -q "mock provider"; then
    pass "Chat response contains mock provider content"
else
    fail "Chat response: $CHAT_RESP"
fi

# Check negotiated model is our mock model.
if echo "$CHAT_RESP" | grep -q '"negotiated_model":"mock-model"'; then
    pass "Negotiated model is mock-model"
else
    fail "Negotiated model mismatch: $CHAT_RESP"
fi

# ──── Step 7: Verify workflow executed in Temporal ────
echo ""
echo "--- Verifying Temporal workflow execution ---"

# Give Temporal a moment to index the workflow.
sleep 2

WORKFLOWS_RESP=$(curl -s "http://localhost:$PORT/admin/v1/workflows?limit=10")

# Check that at least one workflow was returned.
if echo "$WORKFLOWS_RESP" | grep -q '"workflow_id"'; then
    pass "Workflow visible in /admin/v1/workflows"
else
    # Temporal visibility may not be available — check if endpoint works at all.
    if echo "$WORKFLOWS_RESP" | grep -q "temporal"; then
        fail "Workflow query returned temporal error: $WORKFLOWS_RESP"
    else
        fail "No workflows found: $WORKFLOWS_RESP"
    fi
fi

# Check workflow type contains "Chat".
if echo "$WORKFLOWS_RESP" | grep -q '"type"'; then
    pass "Workflow has type field"
else
    fail "Workflow missing type field"
fi

# Check workflow status is COMPLETED.
if echo "$WORKFLOWS_RESP" | grep -qi "completed\|COMPLETED"; then
    pass "Workflow status is COMPLETED"
else
    fail "Workflow not completed: $WORKFLOWS_RESP"
fi

# ──── Step 8: Verify audit trail ────
echo ""
echo "--- Verifying observability ---"

AUDIT_RESP=$(curl -s "http://localhost:$PORT/admin/v1/audit?limit=10")
if echo "$AUDIT_RESP" | grep -q '"logs"'; then
    pass "Audit logs endpoint returns data"
else
    fail "Audit logs: $AUDIT_RESP"
fi

# Check request logs.
LOGS_RESP=$(curl -s "http://localhost:$PORT/admin/v1/logs?limit=10")
if echo "$LOGS_RESP" | grep -q '"logs"'; then
    pass "Request logs endpoint returns data"
else
    fail "Request logs: $LOGS_RESP"
fi

# ──── Results ────
echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="

if [ "$FAIL" -gt 0 ]; then
    echo ""
    echo "--- TokenHub logs ---"
    $DC logs tokenhub 2>&1 | tail -30
    exit 1
fi
