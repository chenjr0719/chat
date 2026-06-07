#!/usr/bin/env bash
#
# setup-jwt-supercluster.sh — test-only extension to docker-local/setup.sh
#
# DESIGN CONTRACT EXCEPTION
# =========================
# The multi-site integration suite's ARCHITECTURE.md §0 documents one
# exception to "the tool does not fill ops/infra gaps": when (1) the
# fix is unambiguously infra/SRE work, (2) the gap is pre-boot, (3) it
# blocks a developer from verifying code they are writing, and (4) no
# alternative home exists within the developer's PR cycle, the tool
# ships a setup-time mutator the operator runs once per machine.
#
# This script is that mutator. Finding F-001
# (docs/integration-suite-multisite-findings.md) is the case it
# resolves.
#
# DELETE THIS SCRIPT WHEN:
#   docker-local/setup.sh grows a --multi-site flag (or equivalent)
#   that produces an operator JWT with $JS.<site>.API.> exports/imports
#   between sites. The chat-app project owns that change. Until then,
#   this script lives in the test tool so multi-site federation
#   developers aren't stuck waiting on an out-of-cycle PR.
#
# WHAT IT DOES
#   - Reads the current docker-local trust chain.
#   - Checks whether the chatapp account JWT already declares the
#     exports/imports needed for cross-domain JetStream Sources. If
#     yes, exits 0 — idempotent.
#   - If not: backs up nats.conf + backend.creds, mutates the chatapp
#     account JWT via nsc, regenerates the operator JWT, rewrites
#     resolver_preload, and re-issues backend.creds with the same
#     user scope.
#   - Verifies the new JWT contains the required exports and prints
#     a summary of what changed.
#
# WHAT IT DOES NOT DO
#   - Run automatically as part of any test invocation.
#   - Modify files outside docker-local/.
#   - Add any state the F-001 finding would not have required anyway.
#
# RUN
#   make -C tools/integration-suite-multisite setup-jwt
#
# PREREQUISITES
#   docker-local/setup.sh has been run once (this script extends its
#   output; it does not replace it).
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
DOCKER_LOCAL="$REPO_ROOT/docker-local"
NATS_CONF="$DOCKER_LOCAL/nats.conf"
BACKEND_CREDS="$DOCKER_LOCAL/backend.creds"
NATS_BOX_IMAGE="natsio/nats-box:latest"

REQUIRED_EXPORT_SUBJECT='$JS.>'

echo "=== setup-jwt-supercluster — multi-site JS Sources prep ==="
echo ""

# --- Prerequisites --------------------------------------------------

if [ ! -f "$NATS_CONF" ] || [ ! -f "$BACKEND_CREDS" ]; then
  cat >&2 <<EOF
ERROR: docker-local trust chain is missing.

This script extends the JWT produced by docker-local/setup.sh. Run
that script first:

  cd $DOCKER_LOCAL && ./setup.sh

Then re-run this one.
EOF
  exit 1
fi

if ! command -v docker &>/dev/null; then
  echo "ERROR: docker not found. This script uses nats-box via Docker." >&2
  exit 1
fi

# --- Idempotency check ----------------------------------------------
#
# Decode the chatapp account JWT and look for the JS.> export. If
# already present, exit 0 — re-running the script is a no-op.

check_jwt_supports_supercluster() {
  # Pull the chatapp account JWT out of resolver_preload. The block
  # looks like:
  #   resolver_preload {
  #     ACCOUNT_PUB: ACCOUNT_JWT
  #     SYS_PUB: SYS_JWT
  #   }
  # We want the line whose pub key matches the chatapp account.
  local account_jwt
  account_jwt=$(awk '
    /resolver_preload/ { in_block = 1; next }
    in_block && /}/    { in_block = 0 }
    in_block && /:/    { print $2 }
  ' "$NATS_CONF" | head -1)

  if [ -z "$account_jwt" ]; then
    return 1
  fi

  # JWTs are header.payload.signature in base64url. Decode the payload
  # and look for the required export subject.
  local payload
  payload=$(echo "$account_jwt" \
    | cut -d. -f2 \
    | tr '_-' '/+' \
    | base64 -d 2>/dev/null || true)

  echo "$payload" | grep -q -F "$REQUIRED_EXPORT_SUBJECT"
}

if check_jwt_supports_supercluster; then
  echo "Chatapp account JWT already declares the cross-domain JS API"
  echo "exports needed for multi-site federation. Nothing to do."
  echo ""
  exit 0
fi

echo "Chatapp account JWT does NOT declare the required exports."
echo "Mutating the trust chain in-place."
echo ""

# --- Backup ---------------------------------------------------------

TS=$(date -u +%Y%m%dT%H%M%SZ)
cp "$NATS_CONF" "$NATS_CONF.bak-$TS"
cp "$BACKEND_CREDS" "$BACKEND_CREDS.bak-$TS"
echo "Backed up:"
echo "  $NATS_CONF  ->  $NATS_CONF.bak-$TS"
echo "  $BACKEND_CREDS  ->  $BACKEND_CREDS.bak-$TS"
echo ""

# --- nsc mutation ---------------------------------------------------
#
# TODO(suite-multisite): finalise the exact nsc invocations.
#
# Based on F-001's probe evidence, the chatapp account JWT needs
# service exports/imports for the $JS.<site>.API.> subject pattern
# between sites. Empirical work needed to pin down:
#   - whether single-account (current design) requires explicit
#     exports for cross-cluster JS API, or whether account-level
#     scope changes (--allow-pub on $JS.>) are sufficient
#   - the exact nsc add export / nsc add import commands
#   - whether resolver_preload needs any additional account JWT
#     entries
#
# VERIFICATION TARGET (from Run 54, recorded in F-001):
#   after this body lands + setup-jwt is run + scenario re-runs,
#   probe of INBOX_site-b's Source must show:
#     cfg.sources[0].domain   = "site-a"   (currently "")
#     state.sources[0].active = > 0         (currently -1ns)
#     state.sources[0].lag    ≥ 0
#     msgs                    > 0
#   if cfg.sources[0].domain still nulls, export/import shape is
#   wrong. see docs/integration-suite-multisite-findings.md F-001
#   §"Verification target" for the full reproducer.
#
# Until the nsc body is filled in, this script fails loudly so the
# operator knows the fix isn't applied yet. The framework around the
# nsc bit (idempotency check, backup, re-verify, restoration on
# failure) is the harness's contribution; the nsc invocations are
# the trust-chain expert's contribution.
#
# When the body is complete, replace this block with the nsc commands
# and the JWT regeneration sequence (see docker-local/setup.sh for
# the regeneration pattern).

cat >&2 <<EOF
ERROR: nsc mutation body is not yet implemented.

The framework is in place (idempotency check, backups, verification)
but the exact nsc commands to add the \$JS.<site>.API.> exports/
imports on the chatapp account are not yet finalised — they need
hands-on nsc verification, not best-effort guessing.

See:
  docs/integration-suite-multisite-findings.md F-001
  tools/integration-suite-multisite/setup-jwt-supercluster.sh
    (this file, search for "TODO(suite-multisite)")

Until the nsc body lands, Surface 5 of the federation scenario stays
red, which is the documented finding. The two infra-sanity scenarios
and the cross-site-seed-visibility scenario continue to pass.

This is intentional — shipping a half-correct nsc body would create a
worse failure mode (subtly broken trust chain) than the current
documented one (clearly broken at Surface 5).
EOF
exit 2

# When the nsc body is added, restore the backups on the validation
# failure path so a botched run doesn't leave the operator with a
# broken trust chain.

# --- Post-mutation verification -------------------------------------

# if ! check_jwt_supports_supercluster; then
#   echo "ERROR: nsc mutation completed but JWT still does not declare" >&2
#   echo "       the required exports. Restoring backups." >&2
#   mv "$NATS_CONF.bak-$TS" "$NATS_CONF"
#   mv "$BACKEND_CREDS.bak-$TS" "$BACKEND_CREDS"
#   exit 3
# fi

# echo ""
# echo "=== Done ==="
# echo ""
# echo "Chatapp account JWT now declares the cross-domain JS API"
# echo "exports needed for multi-site federation. Restart the stack"
# echo "to pick up the new trust chain:"
# echo ""
# echo "  USE_INFRA=true make -C tools/integration-suite-multisite local"
# echo ""
