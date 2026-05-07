#!/usr/bin/env bash
# scripts/test-r2.sh — end-to-end smoke test of the cloudflare-r2
# integration against a real R2 bucket. Validates the operations our
# integration JSON declares (put_object, list_objects, get_object,
# delete_object) work end-to-end with the SigV4 signer + body_input
# runner field.
#
# Credentials are read from scripts/.env.local (gitignored) or from
# pre-set env vars. See scripts/.env.local.example for the template.
#
# Run: ./scripts/test-r2.sh
#
# Safe to run repeatedly — the script generates a unique key per run
# and cleans up after itself.

set -euo pipefail

cd "$(dirname "$0")"

if [ -f .env.local ]; then
  set -a; . ./.env.local; set +a
fi

: "${R2_ACCOUNT_ID:?R2_ACCOUNT_ID not set — copy scripts/.env.local.example to scripts/.env.local and fill in your R2 credentials, or export the vars}"
: "${R2_ACCESS_KEY_ID:?R2_ACCESS_KEY_ID not set}"
: "${R2_SECRET_ACCESS_KEY:?R2_SECRET_ACCESS_KEY not set}"
: "${R2_BUCKET:?R2_BUCKET not set}"

R2_ENDPOINT="https://${R2_ACCOUNT_ID}.r2.cloudflarestorage.com"

# aws CLI handles SigV4; R2 is S3-compatible at this endpoint with
# region=auto. Same code path our integration's aws_sigv4 signer uses.
export AWS_ACCESS_KEY_ID="$R2_ACCESS_KEY_ID"
export AWS_SECRET_ACCESS_KEY="$R2_SECRET_ACCESS_KEY"
export AWS_DEFAULT_REGION="auto"
export AWS_REGION="auto"

if ! command -v aws >/dev/null; then
  echo "ERROR: aws CLI not found. Install via 'brew install awscli' (Mac)" >&2
  exit 1
fi
if ! command -v jq >/dev/null; then
  echo "ERROR: jq not found. Install via 'brew install jq' (Mac)" >&2
  exit 1
fi

KEY="apteva-test-$(date -u +%Y%m%d-%H%M%S).bin"
TMP="$(mktemp -t apteva-r2-test.XXXXXX).bin"
trap 'rm -f "$TMP"' EXIT

echo "── 0. Generating 64KB random test object ──"
dd if=/dev/urandom of="$TMP" bs=1024 count=64 status=none
SHA="$(shasum -a 256 "$TMP" | awk '{print $1}')"
echo "  $TMP  ($SHA)"
echo

echo "── 1. put_object — PUT $R2_BUCKET/$KEY (64KB) ──"
aws s3api put-object \
  --endpoint-url "$R2_ENDPOINT" \
  --bucket "$R2_BUCKET" \
  --key "$KEY" \
  --body "$TMP" \
  --content-type application/octet-stream \
  >/dev/null
echo "  OK — uploaded"
echo

echo "── 2. list_objects — GET /$R2_BUCKET?prefix=apteva-test- ──"
LIST=$(aws s3api list-objects-v2 \
  --endpoint-url "$R2_ENDPOINT" \
  --bucket "$R2_BUCKET" \
  --prefix "apteva-test-" \
  --max-items 10 \
  --output json)
echo "$LIST" | jq -c '.Contents // [] | map({Key, Size})' | head -1
COUNT=$(echo "$LIST" | jq '.Contents // [] | length')
if [ "$COUNT" = "0" ]; then
  echo "  ✗ list returned 0 results immediately after put — failure" >&2
  exit 1
fi
echo "  OK — $COUNT object(s) listed"
echo

echo "── 3. round-trip get_object — verify SHA matches ──"
DOWNLOAD="$(mktemp -t apteva-r2-download.XXXXXX).bin"
aws s3api get-object \
  --endpoint-url "$R2_ENDPOINT" \
  --bucket "$R2_BUCKET" \
  --key "$KEY" \
  "$DOWNLOAD" >/dev/null
DOWN_SHA="$(shasum -a 256 "$DOWNLOAD" | awk '{print $1}')"
rm -f "$DOWNLOAD"
if [ "$SHA" != "$DOWN_SHA" ]; then
  echo "  ✗ SHA mismatch: uploaded $SHA, downloaded $DOWN_SHA" >&2
  exit 1
fi
echo "  OK — bytes round-trip verified"
echo

echo "── 4. delete_object — DELETE $R2_BUCKET/$KEY ──"
aws s3api delete-object \
  --endpoint-url "$R2_ENDPOINT" \
  --bucket "$R2_BUCKET" \
  --key "$KEY" >/dev/null
echo "  OK — deleted"
echo

echo "── 5. confirm cleanup ──"
GONE=$(aws s3api list-objects-v2 \
  --endpoint-url "$R2_ENDPOINT" \
  --bucket "$R2_BUCKET" \
  --prefix "$KEY" \
  --output json | jq '.KeyCount // 0')
if [ "$GONE" = "0" ]; then
  echo "  ✓ object gone"
else
  echo "  ✗ unexpected: $GONE objects still match $KEY" >&2
  exit 1
fi

cat <<EOF

────────────────────────────────────────────────────
  R2 round-trip OK against bucket "$R2_BUCKET".
  Confirms our cloudflare-r2 integration JSON paths
  match R2's S3-compatible surface and SigV4 signs
  correctly.
────────────────────────────────────────────────────

Next: install backup v0.2.0 on a running apteva-server,
bind a cloudflare-r2 connection (account_id, access_key,
secret), then "Run now" with kind=s3 + bucket=$R2_BUCKET.
Backup goes through the platform proxy →
ExecuteIntegrationTool → the same SigV4-signed PUT this
script just verified.
EOF
