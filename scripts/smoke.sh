#!/usr/bin/env bash
# smoke.sh — API smoke test for a running Hatchway deployment
#
# Prerequisites:
#   - Hatchway is running and reachable at an explicit API URL
#   - hatchway server init has been run (API token available)
#   - curl and jq are installed
#
# Usage:
#   HATCHWAY_TOKEN=sk_live_... HATCHWAY_SERVER=https://api.example.com \
#     ./scripts/smoke.sh
#   ./scripts/smoke.sh <api_token> <api_url>
#
# The API URL is intentionally required: the production Compose stack does not
# publish port 9000 directly.

set -euo pipefail

TOKEN="${HATCHWAY_TOKEN:-${1:-}}"
API_URL="${HATCHWAY_SERVER:-${2:-}}"
if [ -z "${TOKEN}" ] || [ -z "${API_URL}" ]; then
	echo "Usage: HATCHWAY_TOKEN=<token> HATCHWAY_SERVER=<api_url> $0" >&2
	echo "   or: $0 <api_token> <api_url>" >&2
	exit 2
fi
for dependency in curl jq; do
	if ! command -v "${dependency}" >/dev/null 2>&1; then
		echo "ERROR: ${dependency} is required" >&2
		exit 2
	fi
done

API_URL="${API_URL%/}"
CURL_COMMON=(
	--silent
	--show-error
	--fail
	--connect-timeout 5
	--max-time 20
)

TUNNEL_ID=""
cleanup() {
	if [ -n "${TUNNEL_ID}" ]; then
		curl --silent --show-error --connect-timeout 5 --max-time 20 \
			-X DELETE \
			-H "Authorization: Bearer ${TOKEN}" \
			"${API_URL}/v1/tunnels/${TUNNEL_ID}" \
			-o /dev/null || true
	fi
}
trap cleanup EXIT

echo "=== Hatchway Smoke Test ==="
echo "API: ${API_URL}"
echo

# 1. Health check
echo "1. Health check..."
curl "${CURL_COMMON[@]}" "${API_URL}/healthz" >/dev/null
echo "   healthz: OK"

# 2. Readiness check
echo "2. Readiness check..."
curl "${CURL_COMMON[@]}" "${API_URL}/readyz" >/dev/null
echo "   readyz: OK"

# 3. Auth check
echo "3. Auth check..."
WHOAMI=$(curl "${CURL_COMMON[@]}" \
	-H "Authorization: Bearer ${TOKEN}" \
	"${API_URL}/v1/me")
USER_ID=$(jq -er '.user_id | strings | select(length > 0)' <<<"${WHOAMI}")
echo "   authenticated user: ${USER_ID}"

# 4. Create a tunnel
echo "4. Creating tunnel..."
TUNNEL=$(curl "${CURL_COMMON[@]}" -X POST \
	-H "Authorization: Bearer ${TOKEN}" \
	-H "Content-Type: application/json" \
	-d '{"type":"http","local_port":8080,"ttl_seconds":300}' \
	"${API_URL}/v1/tunnels")

TUNNEL_ID=$(jq -er '.tunnel_id | strings | select(length > 0)' <<<"${TUNNEL}")
PUBLIC_URL=$(jq -er '.public_url | strings | select(length > 0)' <<<"${TUNNEL}")
echo "   tunnel_id: ${TUNNEL_ID}"
echo "   public_url: ${PUBLIC_URL}"

# 5. List tunnels
echo "5. Listing tunnels..."
LIST=$(curl "${CURL_COMMON[@]}" \
	-H "Authorization: Bearer ${TOKEN}" \
	"${API_URL}/v1/tunnels")
COUNT=$(jq -er '.tunnels | length' <<<"${LIST}")
if ! jq -e --arg id "${TUNNEL_ID}" \
	'any(.tunnels[]; .tunnel_id == $id)' <<<"${LIST}" >/dev/null; then
	echo "   FAIL: newly created tunnel missing from list" >&2
	exit 1
fi
echo "   found ${COUNT} tunnel(s)"

# 6. Get tunnel
echo "6. Getting tunnel ${TUNNEL_ID}..."
GET=$(curl "${CURL_COMMON[@]}" \
	-H "Authorization: Bearer ${TOKEN}" \
	"${API_URL}/v1/tunnels/${TUNNEL_ID}")
STATUS=$(jq -er '.status | strings | select(length > 0)' <<<"${GET}")
echo "   status: ${STATUS}"

# 7. Revoke tunnel
echo "7. Revoking tunnel ${TUNNEL_ID}..."
HTTP_CODE=$(curl --silent --show-error --connect-timeout 5 --max-time 20 \
	-X DELETE \
	-H "Authorization: Bearer ${TOKEN}" \
	"${API_URL}/v1/tunnels/${TUNNEL_ID}" \
	-o /dev/null -w "%{http_code}")
if [ "${HTTP_CODE}" != "204" ]; then
	echo "   FAIL: expected HTTP 204, got ${HTTP_CODE}" >&2
	exit 1
fi
echo "   HTTP 204"

# 8. Verify terminal state and idempotent revoke
echo "8. Verifying revoked state..."
GET=$(curl "${CURL_COMMON[@]}" \
	-H "Authorization: Bearer ${TOKEN}" \
	"${API_URL}/v1/tunnels/${TUNNEL_ID}")
STATUS=$(jq -er '.status' <<<"${GET}")
if [ "${STATUS}" != "revoked" ]; then
	echo "   FAIL: expected revoked status, got ${STATUS}" >&2
	exit 1
fi
HTTP_CODE=$(curl --silent --show-error --connect-timeout 5 --max-time 20 \
	-X DELETE \
	-H "Authorization: Bearer ${TOKEN}" \
	"${API_URL}/v1/tunnels/${TUNNEL_ID}" \
	-o /dev/null -w "%{http_code}")
if [ "${HTTP_CODE}" != "204" ]; then
	echo "   FAIL: repeated revoke returned HTTP ${HTTP_CODE}" >&2
	exit 1
fi
TUNNEL_ID=""
echo "   status: revoked; repeated revoke: HTTP 204"

echo
echo "=== Smoke test passed ==="
