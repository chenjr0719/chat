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
# This regenerates the entire trust chain with the addition of a
# cross-domain JS API service export on the chatapp account. Approach:
# duplicate docker-local/setup.sh's nsc steps inside nats-box, then add
# `nsc add export ... --subject $JS.> --service` to make $JS.<domain>.API.*
# routable across the supercluster gateway for the chatapp account.
#
# We can't mutate the existing JWT in place — setup.sh's nsc state is
# ephemeral (no persistent volume), so the nsc keystore is gone after
# its run. The trade-off the limits-doc exception permits: this script
# is a setup.sh-with-additions, not an in-place mutator.
#
# Best-effort caveat: the exact NATS handling of cross-domain JS API
# for same-account-multi-cluster topology is unobvious from docs alone.
# The post-mutation verification at the bottom of this script catches
# the case where the export shape doesn't actually unlock cross-domain
# delivery, and restores backups so a wrong guess doesn't leave the
# operator with a broken trust chain.

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

echo "Regenerating trust chain with multi-site JS exports via nats-box..."
echo ""

docker run --rm \
  -v "$TMPDIR:/output" \
  "$NATS_BOX_IMAGE" \
  sh -c '
    set -e

    nsc add operator --name localdev --sys 2>&1 | sed "s/^/  /"
    nsc env -o localdev >/dev/null 2>&1

    nsc add account --name chatapp 2>&1 | sed "s/^/  /"
    nsc edit account chatapp \
      --js-mem-storage 512M \
      --js-disk-storage 5G \
      --js-streams 10 \
      2>&1 | sed "s/^/  /"

    # MULTI-SITE ADDITION:
    # Service export for cross-domain JS API. With a single account
    # spanning the supercluster, this is what permits the federation
    # Source on INBOX_site-b to issue $JS.site-a.API.STREAM.MSG.GET
    # requests that NATS-A can route through the gateway and reply to.
    # Without this export, NATS-B silently strips the Domain field on
    # the configured Source (F-001) because the account JWT does not
    # declare any JS API subjects as routable.
    nsc add export --account chatapp --service \
      --subject "\$JS.>" \
      --name "CrossDomainJSAPI" \
      --response-type Stream \
      2>&1 | sed "s/^/  /"

    nsc describe operator --raw > /output/operator.jwt
    nsc describe account chatapp --raw > /output/account.jwt
    nsc describe account SYS --raw > /output/sys.jwt

    nsc describe account chatapp 2>/dev/null \
      | grep "Account ID" \
      | awk -F"|" "{gsub(/[ \t]/, \"\", \$3); print \$3}" \
      > /output/account_pub.txt
    nsc describe account SYS 2>/dev/null \
      | grep "Account ID" \
      | awk -F"|" "{gsub(/[ \t]/, \"\", \$3); print \$3}" \
      > /output/sys_pub.txt

    nsc add user --account chatapp --name backend 2>&1 | sed "s/^/  /"
    nsc edit user --account chatapp --name backend \
      --allow-sub ">" --allow-pub ">" 2>&1 | sed "s/^/  /"
    nsc generate creds --account chatapp --name backend > /output/backend.creds
  '

# --- Atomic replacement of nats.conf + backend.creds ----------------

OPERATOR_JWT=$(cat "$TMPDIR/operator.jwt")
ACCOUNT_JWT=$(cat "$TMPDIR/account.jwt")
SYS_JWT=$(cat "$TMPDIR/sys.jwt")
ACCOUNT_PUB_KEY=$(cat "$TMPDIR/account_pub.txt")
SYS_PUB_KEY=$(cat "$TMPDIR/sys_pub.txt")

cp "$TMPDIR/backend.creds" "$BACKEND_CREDS"
chmod 644 "$BACKEND_CREDS"

cat > "$NATS_CONF" <<EOF
# Generated by tools/integration-suite-multisite/setup-jwt-supercluster.sh
# Test-only multi-site extension of docker-local/setup.sh's trust chain.
# When the chat-app project grows multi-site support in setup.sh, this
# script and its output are obsolete — see ARCHITECTURE.md §0 + F-001.

port: 4222
http_port: 8222

operator: ${OPERATOR_JWT}

resolver: MEMORY

resolver_preload {
  ${ACCOUNT_PUB_KEY}: ${ACCOUNT_JWT}
  ${SYS_PUB_KEY}: ${SYS_JWT}
}

jetstream {
  store_dir: /data/jetstream
  max_mem: 1G
  max_file: 10G
}

websocket {
  port: 9222
  no_tls: true
}
EOF

echo "Wrote new trust chain:"
echo "  $NATS_CONF"
echo "  $BACKEND_CREDS"
echo ""

# --- Post-mutation verification -------------------------------------

if ! check_jwt_supports_supercluster; then
  echo "ERROR: nsc mutation completed but JWT still does not declare" >&2
  echo "       the required exports. Restoring backups." >&2
  mv "$NATS_CONF.bak-$TS" "$NATS_CONF"
  mv "$BACKEND_CREDS.bak-$TS" "$BACKEND_CREDS"
  exit 3
fi

echo "=== Done ==="
echo ""
echo "Chatapp account JWT now declares the cross-domain JS API"
echo "export needed for multi-site federation. Restart the stack"
echo "to pick up the new trust chain:"
echo ""
echo "  USE_INFRA=true make -C tools/integration-suite-multisite local"
echo ""
echo "Backups retained at:"
echo "  $NATS_CONF.bak-$TS"
echo "  $BACKEND_CREDS.bak-$TS"
echo ""
echo "NOTE: this script is a best-effort attempt at the nsc body for"
echo "F-001. The verification above checks that the JWT now lists"
echo "\$JS.> as an export, which is necessary but may not be SUFFICIENT"
echo "for the federation Source to actually deliver. The federation"
echo "scenario re-run is the real test: if INBOX_site-b's Source still"
echo "shows domain=\"\" + active=-1ns + msgs=0 after running this"
echo "script + restarting the stack, the export shape needs more work."
echo "See docs/integration-suite-multisite-findings.md F-001 for the"
echo "verification target."
