#!/usr/bin/env bash
# smoke.sh — End-to-end smoke test for the Hatchway compose stack
#
# Prerequisites:
#   - docker compose up -d
#   - hatchway server init has been run (admin token available)
#
# Usage: ./scripts/smoke.sh <api_token> [api_url]
#   api_token: the admin API token from 'hatchway server init'
#   api_url:   defaults to http://localhost:9000

set -euo pipefail

TOKEN="${1:?Usage: smoke.sh <api_token> [api_url]}"
API_URL="${2:-http://localhost:9000}"

echo "=== Hatchway Smoke Test ==="
echo "API: ${API_URL}"
echo ""

# 1. Health check
echo "1. Health check..."
curl -sf "${API_URL}/healthz" >/dev/null && echo "   healthz: OK" || {
	echo "   FAIL: healthz"
	exit 1
}

# 2. Readiness check
echo "2. Readiness check..."
curl -sf "${API_URL}/readyz" >/dev/null && echo "   readyz: OK" || {
	echo "   FAIL: readyz (DB not ready?)"
	exit 1
}

# 3. Auth check
echo "3. Auth check..."
WHOAMI=$(curl -sf -H "Authorization: Bearer ${TOKEN}" "${API_URL}/v1/me")
echo "   whoami: ${WHOAMI}"

# 4. Create a tunnel
echo "4. Creating tunnel..."
TUNNEL=$(curl -sf -X POST \
	-H "Authorization: Bearer ${TOKEN}" \
	-H "Content-Type: application/json" \
	-d '{"type":"http","local_port":8080,"ttl_seconds":300}' \
	"${API_URL}/v1/tunnels")
echo "   response: ${TUNNEL}"

TUNNEL_ID=$(echo "${TUNNEL}" | grep -o '"tunnel_id":"[^"]*"' | cut -d'"' -f4)
PUBLIC_URL=$(echo "${TUNNEL}" | grep -o '"public_url":"[^"]*"' | cut -d'"' -f4)

if [ -z "${TUNNEL_ID}" ]; then
	echo "   FAIL: could not parse tunnel_id"
	exit 1
fi
echo "   tunnel_id: ${TUNNEL_ID}"
echo "   public_url: ${PUBLIC_URL}"

# 5. List tunnels
echo "5. Listing tunnels..."
LIST=$(curl -sf -H "Authorization: Bearer ${TOKEN}" "${API_URL}/v1/tunnels")
COUNT=$(echo "${LIST}" | grep -o '"tunnel_id"' | wc -l | tr -d ' ')
echo "   found ${COUNT} tunnel(s)"

# 6. Get tunnel
echo "6. Getting tunnel ${TUNNEL_ID}..."
GET=$(curl -sf -H "Authorization: Bearer ${TOKEN}" "${API_URL}/v1/tunnels/${TUNNEL_ID}")
echo "   status: $(echo "${GET}" | grep -o '"status":"[^"]*"' | cut -d'"' -f4)"

# 7. Delete tunnel
echo "7. Deleting tunnel ${TUNNEL_ID}..."
curl -sf -X DELETE -H "Authorization: Bearer ${TOKEN}" "${API_URL}/v1/tunnels/${TUNNEL_ID}" -o /dev/null -w "   HTTP %{http_code}\n"

# 8. Verify tunnel is gone
echo "8. Verifying tunnel is gone..."
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -H "Authorization: Bearer ${TOKEN}" "${API_URL}/v1/tunnels/${TUNNEL_ID}")
if [ "${HTTP_CODE}" = "404" ]; then
	echo "   OK: got 404 as expected"
else
	echo "   WARN: expected 404, got ${HTTP_CODE}"
fi

echo ""
echo "=== Smoke test passed ==="
