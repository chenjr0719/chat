#!/usr/bin/env bash
# Operator-owned setup: create OUTBOX_<site> JetStream streams.
# In production these are provisioned by ops/IaC (see CLAUDE.md
# §"Stream bootstrap ownership"). The chat-app services do not
# create them, and the integration tool deliberately does not
# either (ARCHITECTURE.md §0). This script is the ops-equivalent
# wired in via the scenario's pre_fire_scripts:.
set -euo pipefail

: "${ISM_SITE_A_NATS_URL:?missing}"
: "${ISM_SITE_B_NATS_URL:?missing}"
: "${ISM_NATS_CREDS_FILE:?missing}"

# Uses the prebuilt createoutbox helper. A different operator
# could substitute the `nats` CLI: `nats stream add --config=...`.
TOOL=/tmp/create-outbox/createoutbox
if [ ! -x "$TOOL" ]; then
  echo "prep-outbox.sh: $TOOL not found; build with 'go build -o $TOOL /tmp/create-outbox/'" >&2
  exit 1
fi

"$TOOL" -mode create \
  -url "$ISM_SITE_A_NATS_URL" \
  -creds "$ISM_NATS_CREDS_FILE" \
  -domain site-a \
  -stream OUTBOX_site-a \
  -subject 'outbox.site-a.>'

"$TOOL" -mode create \
  -url "$ISM_SITE_B_NATS_URL" \
  -creds "$ISM_NATS_CREDS_FILE" \
  -domain site-b \
  -stream OUTBOX_site-b \
  -subject 'outbox.site-b.>'
