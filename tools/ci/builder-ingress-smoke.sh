#!/usr/bin/env bash
set -euo pipefail

if [[ $# != 5 || ! $5 =~ ^[0-9a-f]{40}$ ]]; then
  echo "Usage: $0 VIEWER_IMAGE REGISTRY_IMAGE READER_IMAGE MCP_IMAGE BUILDER_SHA" >&2
  exit 64
fi

cd "$(dirname "${BASH_SOURCE[0]}")/../.."
scratch_root="$PWD/artifacts/tmp/todo-execution/ci-ingress"
mkdir -p "$scratch_root"
scratch=$(mktemp -d "$scratch_root/run.XXXXXXXX")
project="builder-ingress-smoke-${scratch##*.}"
project=${project,,}

export BUILDER_SMOKE_VIEWER_IMAGE=$1 BUILDER_SMOKE_REGISTRY_IMAGE=$2
export BUILDER_SMOKE_READER_IMAGE=$3 BUILDER_SMOKE_MCP_IMAGE=$4
# Base config is also validated for local test images. Only the smoke
# overlay uses local tags and the base references never run in that case.
image_digest() {
  if [[ $1 =~ @sha256:([0-9a-f]{64})$ ]]; then
    printf '%s' "${BASH_REMATCH[1]}"
  else
    printf '%064d' 0
  fi
}
export BUILDER_VIEWER_DIGEST BUILDER_REGISTRY_DIGEST BUILDER_READER_DIGEST BUILDER_MCP_DIGEST
BUILDER_VIEWER_DIGEST=$(image_digest "$1")
BUILDER_REGISTRY_DIGEST=$(image_digest "$2")
BUILDER_READER_DIGEST=$(image_digest "$3")
BUILDER_MCP_DIGEST=$(image_digest "$4")

export BUILDER_REMOTE_HOST=builder-ci.test BUILDER_TUNNEL_NETWORK="$project-tunnel"
export REGISTRY_MONGODB_URI='mongodb://mongo:27017/?replicaSet=rs0'
export REGISTRY_CONTROL_DATABASE=builder_ingress_fixture
export BUILDER_MCP_COMPANY_IDS=ingress-fixture
export BUILDER_MCP_BEARER_TOKEN="$(openssl rand -hex 32)"
export DELPHI_PROJECT_READER_TOKEN="$(openssl rand -hex 32)"
export DELPHI_PROJECT_READER_SCOPED_PROOF_TOKEN="$(openssl rand -hex 32)"
export BUILDER_GITHUB_APP_ID=42 BUILDER_GITHUB_APP_SLUG=builder-ingress-fixture
export BUILDER_GITHUB_APP_CLIENT_ID=fixture-client
export BUILDER_GITHUB_APP_CLIENT_SECRET="$(openssl rand -hex 32)"
export BUILDER_GITHUB_ACCESS_ISSUER=https://builder-ingress-fixture.cloudflareaccess.com
export BUILDER_GITHUB_ACCESS_AUDIENCE=fixture-audience
export BUILDER_GITHUB_ACCESS_OPERATORS='{"fixture-operator":["ingress-fixture"]}'
export BUILDER_ADMIN_COMPANY_CREATORS='[]'
export BUILDER_ADMIN_SHARED_AUTHENTICATED_ACCESS=false
export BUILDER_GITHUB_APP_KEY_PATH="$scratch/github-app.pem"
export BUILDER_READER_STATE_PATH="$scratch/reader-state"
export BUILDER_SMOKE_CADDYFILE="$scratch/Caddyfile"

install -d -m 700 "$BUILDER_READER_STATE_PATH"
openssl genrsa -out "$BUILDER_GITHUB_APP_KEY_PATH" 2048 2>/dev/null
# The key is synthetic and its containing directory is 0700.
chmod 644 "$BUILDER_GITHUB_APP_KEY_PATH"
sed -e '/admin off/a\	skip_install_trust' -e '/^{\$BUILDER_REMOTE_HOST} {$/a\	tls internal' docker/caddy/Caddyfile >"$BUILDER_SMOKE_CADDYFILE"
grep -q 'tls internal' "$BUILDER_SMOKE_CADDYFILE"
chmod 644 "$BUILDER_SMOKE_CADDYFILE"

base=(docker compose --env-file /dev/null -p "$project" -f compose.builder.remote.yaml)
smoke=("${base[@]}" -f tools/ci/builder-ingress-smoke.compose.yaml)
cleanup() {
  local result=$?
  trap - EXIT
  if (( result != 0 )); then
    "${smoke[@]}" logs --no-color --tail 30 >&2 || true
  fi
  docker rm --force "$project-stream" >/dev/null 2>&1 || true
  docker image rm "$project-registry-test" >/dev/null 2>&1 || true
  "${smoke[@]}" down --volumes --remove-orphans >/dev/null || result=1
  rmdir -- "$BUILDER_READER_STATE_PATH" 2>/dev/null || true
  rm -rf -- "$scratch" || result=1
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

"${base[@]}" config --format json | python3 tools/ci/builder-ingress-smoke.py topology
"${base[@]}" -f deploy/compose.builder.tunnel.yaml config --format json |
  python3 tools/ci/builder-ingress-smoke.py topology tunnel
"${smoke[@]}" config --quiet
docker run --rm --network none --user 0:0 --entrypoint chown \
  --mount "type=bind,source=$BUILDER_READER_STATE_PATH,target=/fixture-state" \
  "$BUILDER_SMOKE_READER_IMAGE" 65532:65532 /fixture-state
"${smoke[@]}" run --rm --no-deps --volume "$PWD/docker/caddy/Caddyfile:/production.Caddyfile:ro" \
  builder-ingress caddy validate --config /production.Caddyfile --adapter caddyfile

"${smoke[@]}" up -d mongo
for attempt in {1..45}; do
  if "${smoke[@]}" exec -T mongo mongosh --quiet --eval 'db.adminCommand({ping:1}).ok' 2>/dev/null | grep -qx 1; then
    break
  fi
  test "$attempt" -lt 45
  sleep 1
done
"${smoke[@]}" exec -T mongo mongosh --quiet --eval 'rs.initiate({_id:"rs0",members:[{_id:0,host:"mongo:27017"}]})' >/dev/null
for attempt in {1..45}; do
  if "${smoke[@]}" exec -T mongo mongosh --quiet --eval 'db.hello().isWritablePrimary' 2>/dev/null | grep -qx true; then
    break
  fi
  test "$attempt" -lt 45
  sleep 1
done

"${smoke[@]}" up --no-build -d
for attempt in {1..45}; do
  if "${smoke[@]}" cp builder-ingress:/data/caddy/pki/authorities/local/root.crt "$scratch/root.crt" 2>/dev/null; then
    break
  fi
  test "$attempt" -lt 45
  sleep 1
done
http_port=$("${smoke[@]}" port builder-ingress 80)
https_port=$("${smoke[@]}" port builder-ingress 443)
python3 tools/ci/builder-ingress-smoke.py live "$project" "${http_port##*:}" "${https_port##*:}" "$scratch/root.crt" "$5"

# Remove the container so a successful second handshake needs the persisted
# CA rather than a certificate that survived in the old container filesystem.
"${smoke[@]}" up --no-build -d --no-deps --force-recreate builder-ingress
https_port=$("${smoke[@]}" port builder-ingress 443)
python3 tools/ci/builder-ingress-smoke.py restart "${https_port##*:}" "$scratch/root.crt" "$5"

# Production MCP is stateless and returns JSON. Swap only the disposable
# upstream for a controlled SSE emitter to verify the same Caddy route flushes.
"${smoke[@]}" stop builder-mcp
docker run --detach --rm --name "$project-stream" --network "${project}_ingress-mcp" \
  --network-alias builder-mcp --mount "type=bind,source=$PWD/tools/ci/builder-ingress-smoke.py,target=/probe.py,readonly" \
  python:3.12-alpine@sha256:b658b05ce9c4eb97ec6fc19424818a1f31993659c2b505a532f470e83807815d \
  python /probe.py emitter >/dev/null
python3 tools/ci/builder-ingress-smoke.py stream "${https_port##*:}" "$scratch/root.crt" "$5"
"${smoke[@]}" stop builder-viewer
ingress_id=$("${smoke[@]}" ps --quiet builder-ingress)
python3 tools/ci/builder-ingress-smoke.py redaction "${https_port##*:}" "$scratch/root.crt" "$ingress_id"

# Existing controlled GitHub provider fixtures exercise the App token and
# bound repository read separately from the real Registry/Viewer/MCP path.
docker build --target test -f go-app/Dockerfile -t "$project-registry-test" .
app_fixtures=(
  TestGitHubAppRepositoryReadTokenIsScopedAndVerifiesExactIdentity
  TestGitHubVisualWarmPrototypeAssetAndConditionalReadRequireLiveAccess
  TestGitHubScopedReaderProofKeepsAliasAndFencesTwoRepositoryConnections
)
app_filter=$(IFS='|'; printf '%s' "${app_fixtures[*]}")
docker run --rm --network none "$project-registry-test" go test -mod=vendor -v -count=1 \
  -run "^($app_filter)$" ./internal/registry | tee "$scratch/app-tests.log"
for fixture in "${app_fixtures[@]}"; do
  grep -q "^--- PASS: $fixture (" "$scratch/app-tests.log"
done
