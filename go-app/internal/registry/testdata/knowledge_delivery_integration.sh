#!/usr/bin/env bash
set -euo pipefail

repo_root="${BUILDER_PROJECT_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)}"
if [[ ! -f "$repo_root/go-app/go.mod" || ! -f "$repo_root/builder-app/service/go.mod" || ! -f "$repo_root/delphi-ai/local-api/go.mod" ]]; then
  echo 'Knowledge integration runner must resolve the Builder and PACED Go modules' >&2
  exit 2
fi

suffix="$(date +%s)-$$"
network="builder-knowledge-it-$suffix"
mongo_container="builder-knowledge-mongo-$suffix"
cleanup() {
  docker container rm --force "$mongo_container" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker network create "$network" >/dev/null
docker run --detach --name "$mongo_container" --network "$network" --network-alias mongo mongo:7 --replSet rs0 --bind_ip_all >/dev/null

ready=false
for attempt in {1..30}; do
  if docker exec "$mongo_container" mongosh --quiet --eval 'db.adminCommand({ping:1}).ok' 2>/dev/null | rg -q '^1$'; then
    ready=true
    break
  fi
  sleep 1
done
if [[ "$ready" != true ]]; then
  echo 'Isolated Mongo did not become reachable' >&2
  exit 2
fi
docker exec "$mongo_container" mongosh --quiet --eval 'rs.initiate({_id:"rs0",members:[{_id:0,host:"mongo:27017"}]})' >/dev/null

docker run --rm --network "$network" \
  --env REGISTRY_TEST_MONGODB_URI='mongodb://mongo:27017/?replicaSet=rs0' \
  --env REGISTRY_REQUIRE_MONGO_INTEGRATION=1 \
  --volume "$repo_root/go-app:/src/go-app:ro" \
  --volume "$repo_root/builder-app/service:/src/builder-app/service:ro" \
  --volume "$(realpath "$repo_root/delphi-ai/local-api"):/src/delphi-ai/local-api:ro" \
  --workdir /src/go-app \
  golang:1.23-bookworm \
  go test ./internal/registry -run '^TestMongoKnowledgeDeliveryVerifiesExactImmutableMutation$' -count=1
