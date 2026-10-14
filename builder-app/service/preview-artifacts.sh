#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -P "$(dirname "$0")/../.." && pwd)
cd "$repo_root"
for command_name in docker curl; do
	command -v "$command_name" >/dev/null 2>&1 || { printf 'preview requires %s\n' "$command_name" >&2; exit 70; }
done
docker compose version >/dev/null
: "${DELPHI_BUILDER_STATE_ROOT:?set DELPHI_BUILDER_STATE_ROOT to the explicit shared local state root}"
case "$DELPHI_BUILDER_STATE_ROOT" in /*) ;; *) printf '%s\n' 'state root must be absolute' >&2; exit 64 ;; esac
[ "$DELPHI_BUILDER_STATE_ROOT" != / ] || { printf '%s\n' 'state root must not be /' >&2; exit 64; }
[ -d "$DELPHI_BUILDER_STATE_ROOT" ] || { printf '%s\n' 'state root must already exist' >&2; exit 64; }
state_real=$(CDPATH= cd -P "$DELPHI_BUILDER_STATE_ROOT" && pwd)
[ "$state_real" = "$DELPHI_BUILDER_STATE_ROOT" ] || { printf '%s\n' 'state root must not contain symlinks' >&2; exit 64; }
export BUILDER_UID=${BUILDER_UID:-$(id -u)}
export BUILDER_GID=${BUILDER_GID:-$(id -g)}
docker compose -p builder-local-artifacts -f compose.builder.artifacts.yaml up --build --force-recreate -d artifact-preview
served=''
attempt=0
while [ "$attempt" -lt 30 ]; do
	if served=$(curl --silent --show-error --fail http://127.0.0.1:8089/api/local-preview 2>/dev/null); then break; fi
	attempt=$((attempt + 1))
	sleep 1
done
[ -n "$served" ] || { printf '%s\n' 'local Builder consumer did not become ready' >&2; exit 70; }
printf '%s\n' "$served"
printf 'Local registered Project viewer: http://127.0.0.1:8089/local\nRead-only state root: %s\n' "$DELPHI_BUILDER_STATE_ROOT"
