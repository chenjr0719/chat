#!/usr/bin/env bash
#
# End-to-end check for the Teams "create online meeting" RPC against a RUNNING
# stack (local docker-compose, or any reachable NATS by setting NATS_URL).
# Hits the REAL Microsoft Graph API — room-service must have valid Teams creds.
#
# It drives the NATS request/reply RPC directly (the service exposes no HTTP
# endpoint; the REST label POST /api/v1/meetings belongs to the edge gateway).
#
# Asserts:
#   1. the meeting RPC returns {id, joinUrl} with a real teams.microsoft.com URL
#   2. idempotency: a repeat call returns the SAME id + joinUrl
#   3. exactly one teams_meet_started system message lands on the canonical
#      stream for this meeting (matched by joinUrl)
#
# Required env:
#   SITE_ID   room's origin site id           (e.g. 00302000)
#   ACCOUNT   requester account; MUST be a member of ROOM_ID
#   ROOM_ID   an existing room with ACCOUNT as a member
# Optional env:
#   NATS_URL  default nats://localhost:4222
#   NATS_BIN  the nats CLI binary/command       (default: nats)
#   TIMEOUT   per-request reply timeout          (default: 30s)
#
# Prereqs: the `nats` CLI and `jq`. If `nats` isn't installed locally, point
# NATS_BIN at a containerised one, e.g. against the compose network:
#   NATS_BIN="docker run --rm --network <stack_net> synadia/nats-box nats"
#
set -euo pipefail

NATS_URL="${NATS_URL:-nats://localhost:4222}"
NATS_BIN="${NATS_BIN:-nats}"
TIMEOUT="${TIMEOUT:-30s}"
SITE_ID="${SITE_ID:?set SITE_ID (room origin site id)}"
ACCOUNT="${ACCOUNT:?set ACCOUNT (requester; must be a member of ROOM_ID)}"
ROOM_ID="${ROOM_ID:?set ROOM_ID (existing room with ACCOUNT as member)}"

command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }
# shellcheck disable=SC2086  # NATS_BIN may be a multi-word command
$NATS_BIN --version >/dev/null 2>&1 || {
  echo "need the 'nats' CLI (set NATS_BIN). https://github.com/nats-io/natscli" >&2
  exit 2
}

SUBJ="chat.user.${ACCOUNT}.request.room.${ROOM_ID}.${SITE_ID}.teams.meeting"
EVT_SUBJ="chat.msg.canonical.${SITE_ID}.created"

fail() { echo "FAIL: $*" >&2; exit 1; }
req()  { $NATS_BIN --server="$NATS_URL" req --timeout="$TIMEOUT" --raw "$SUBJ" '{}'; }

# Capture canonical events live (start before firing so we don't miss the publish).
EVT_LOG="$(mktemp)"
$NATS_BIN --server="$NATS_URL" sub --raw "$EVT_SUBJ" >"$EVT_LOG" 2>/dev/null &
SUB_PID=$!
trap 'kill "$SUB_PID" 2>/dev/null || true; rm -f "$EVT_LOG"' EXIT
sleep 1

# --- create ---
R1="$(req)" || fail "NATS request failed — is room-service reachable at $NATS_URL?"
if printf '%s' "$R1" | jq -e 'has("code")' >/dev/null 2>&1; then
  ERR="$(printf '%s' "$R1" | jq -r '.error // .code')"
  case "$ERR" in
    *"teams meetings are not configured"*)
      fail "room-service has no Teams creds (TEAMS_TENANT_ID/CLIENT_ID/CLIENT_SECRET). Set them + restart." ;;
    *) fail "RPC error: $ERR" ;;
  esac
fi
ID1="$(printf '%s' "$R1" | jq -r '.id')"
URL1="$(printf '%s' "$R1" | jq -r '.joinUrl')"
[ -n "$ID1" ] && [ "$ID1" != null ] || fail "empty meeting id in reply: $R1"
case "$URL1" in
  https://teams.microsoft.com/l/meetup-join/*) ;;
  *) fail "joinUrl is not a real Teams meeting URL: $URL1" ;;
esac
echo "PASS create   : id=$ID1"
echo "              joinUrl=$URL1"
sleep 2  # let the canonical publish propagate

# --- idempotency ---
R2="$(req)" || fail "second request failed"
ID2="$(printf '%s' "$R2" | jq -r '.id')"
URL2="$(printf '%s' "$R2" | jq -r '.joinUrl')"
[ "$ID2" = "$ID1" ]  || fail "idempotency: id changed ($ID1 -> $ID2)"
[ "$URL2" = "$URL1" ] || fail "idempotency: joinUrl changed"
echo "PASS idempotent: repeat call returned the same id + joinUrl"
sleep 1

# --- event: exactly one teams_meet_started for this meeting (matched by joinUrl) ---
NR="$(grep '"type":"teams_meet_started"' "$EVT_LOG" 2>/dev/null | grep -c -F "$URL1" || true)"
[ "$NR" -eq 1 ] || fail "expected exactly 1 teams_meet_started for this meeting, saw $NR"
echo "PASS event    : exactly one teams_meet_started on $EVT_SUBJ with matching joinUrl"

echo "ALL PASS"
