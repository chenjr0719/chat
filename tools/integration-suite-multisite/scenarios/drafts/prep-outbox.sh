#!/usr/bin/env bash
# Operator-owned setup: create OUTBOX_<site> JetStream streams.
# In production these are provisioned by ops/IaC (see CLAUDE.md
# §"Stream bootstrap ownership" and finding F-001). The chat-app
# services do not create them, and the integration tool deliberately
# does not either (ARCHITECTURE.md §0). This script is the
# ops-equivalent wired in via the scenario's `pre_fire_scripts:`.
#
# Idempotent — re-running on an existing stream is a no-op
# (createoutbox uses jetstream.CreateOrUpdateStream).
set -euo pipefail

: "${ISM_SITE_A_NATS_URL:?missing}"
: "${ISM_SITE_B_NATS_URL:?missing}"
: "${ISM_NATS_CREDS_FILE:?missing}"

# The harness runs pre_fire_scripts with cwd = the scenario YAML's
# directory (scenarios/drafts/). The createoutbox cmd lives two
# directories up, then under cmd/. Using `go run` against the
# Go module-relative path keeps this stable even if scenarios get
# nested into subdirectories later (plan-ahead §2.8) — `go run`
# resolves the path against the enclosing module, not cwd-depth.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CREATEOUTBOX_DIR="$SCRIPT_DIR/../../cmd/createoutbox"

go run "$CREATEOUTBOX_DIR" \
  -url "$ISM_SITE_A_NATS_URL" \
  -creds "$ISM_NATS_CREDS_FILE" \
  -domain site-a \
  -stream OUTBOX_site-a \
  -subject 'outbox.site-a.>'

go run "$CREATEOUTBOX_DIR" \
  -url "$ISM_SITE_B_NATS_URL" \
  -creds "$ISM_NATS_CREDS_FILE" \
  -domain site-b \
  -stream OUTBOX_site-b \
  -subject 'outbox.site-b.>'
