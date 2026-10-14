#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -P "$(dirname "$0")/../.." && pwd)
fixture_root=$(mktemp -d "$repo_root/local-api/.fixtures-artifact-status.XXXXXX")
case "$fixture_root" in "$repo_root"/local-api/.fixtures-artifact-status.*) ;; *) printf '%s\n' 'unsafe fixture cleanup path' >&2; exit 1 ;; esac
fixture_name=${fixture_root##*/}
project_root="$fixture_root/project"
foundation_root="$fixture_root/foundation"
container_project_root="/workspace/local-api/$fixture_name/project"
container_foundation_root="/workspace/local-api/$fixture_name/foundation"
fixture_workspace="$fixture_root"
container_fixture_workspace="/workspace"
binding_file="$fixture_root/local-api/source-bindings.json"
container_binding_file="/workspace/local-api/$fixture_name/local-api/source-bindings.json"
company_ds_root="$fixture_root/company"
default_ds_root="$fixture_root/default"
runner="$repo_root/local-api/artifact-status"
artifact_evidence_root="$repo_root/belluga_builder_foundation_documentation/artifacts/tmp/design-system-fixtures"
process_sequence=0
cleanup() { case "$fixture_root" in "$repo_root"/local-api/.fixtures-artifact-status.*) rm -rf "$fixture_root" ;; esac; }
trap cleanup EXIT HUP INT TERM
mkdir -p "$artifact_evidence_root"
artifact_evidence=$(mktemp -d "$artifact_evidence_root/prototype-convergence.XXXXXX")
if [ "${ARTIFACT_STATUS_BOOTSTRAP_ONLY:-0}" != 1 ] && [ "${ARTIFACT_STATUS_TAIL_ONLY:-0}" != 1 ]; then
	: >"$artifact_evidence/concurrency-counts.txt"
	: >"$artifact_evidence/concurrency-process-status.txt"
fi
: >"$artifact_evidence/bootstrap-collision-results.txt"
mkdir -p "$project_root" "$foundation_root/prototypes/sample" "$foundation_root/prototypes/checkout" "$foundation_root/artifacts"
mkdir -p "$fixture_root/local-api"
mkdir -p "$fixture_root/evidence"

cat >"$foundation_root/prototypes/catalog.json" <<'JSON'
{"schema_version":"1","project_id":"builder","prototypes":[{"id":"sample","name":"Sample","description":null,"root":"prototypes/sample"},{"id":"checkout","name":"Checkout","description":"Two-screen flow","root":"prototypes/checkout"}]}
JSON
cat >"$foundation_root/prototypes/sample/prototype.json" <<'JSON'
{"schema_version":"1","id":"sample","entry_point":"index.html","screens":[{"id":"home","name":"Home","path":"index.html","scope":null}],"sources":["index.html"],"assets":[],"links":[],"related":[],"design_system_ref":null}
JSON
printf '%s\n' '<main>sample</main>' >"$foundation_root/prototypes/sample/index.html"
cat >"$foundation_root/prototypes/checkout/prototype.json" <<'JSON'
{"schema_version":"1","id":"checkout","entry_point":"start.html","screens":[{"id":"start","name":"Start","path":"start.html","scope":null},{"id":"confirm","name":"Confirm","path":"confirm.html","scope":null}],"sources":["start.html","confirm.html"],"assets":[],"links":[{"from_screen_id":"start","to_screen_id":"confirm"}],"related":[],"design_system_ref":null}
JSON
printf '%s\n' '<main>start</main>' >"$foundation_root/prototypes/checkout/start.html"
printf '%s\n' '<main>confirm</main>' >"$foundation_root/prototypes/checkout/confirm.html"
printf '%s\n' 'evidence' >"$foundation_root/artifacts/evidence.md"
git -C "$foundation_root" init -q
git -C "$foundation_root" config user.email fixture@example.invalid
git -C "$foundation_root" config user.name 'Artifact Status Fixture'
git -C "$foundation_root" add prototypes artifacts
git -C "$foundation_root" commit -qm 'seed artifact status fixture'
commit=$(git -C "$foundation_root" rev-parse HEAD)
run_expect() {
	expected=$1
	shift
	set +e
	"$runner" prototypes --project-root "$container_project_root" --foundation-root "$container_foundation_root" "$@" >"$fixture_root/stdout" 2>"$fixture_root/stderr"
	actual=$?
	set -e
	process_sequence=$((process_sequence + 1))
	printf 'prototype\t%s\texpected=%s\tactual=%s\n' "$process_sequence" "$expected" "$actual" >>"$artifact_evidence/process-status.tsv"
	cp "$fixture_root/stdout" "$artifact_evidence/prototype-$process_sequence.stdout"
	cp "$fixture_root/stderr" "$artifact_evidence/prototype-$process_sequence.stderr"
	if [ "$actual" -ne "$expected" ]; then
		printf 'expected exit %s, got %s: ' "$expected" "$actual" >&2
		cat "$fixture_root/stderr" >&2
		cat "$fixture_root/stdout" >&2
		return 1
	fi
}

run_design_system_expect() {
	expected=$1
	shift
	set +e
	"$runner" design-system --project-root "$container_project_root" --foundation-root "$container_foundation_root" --source-bindings "$container_binding_file" "$@" >"$fixture_root/ds.stdout" 2>"$fixture_root/ds.stderr"
	actual=$?
	set -e
	process_sequence=$((process_sequence + 1))
	printf 'design-system\t%s\texpected=%s\tactual=%s\n' "$process_sequence" "$expected" "$actual" >>"$artifact_evidence/process-status.tsv"
	cp "$fixture_root/ds.stdout" "$artifact_evidence/design-system-$process_sequence.stdout"
	cp "$fixture_root/ds.stderr" "$artifact_evidence/design-system-$process_sequence.stderr"
	if [ "$actual" -ne "$expected" ]; then
		printf 'Design System expected exit %s, got %s: ' "$expected" "$actual" >&2
		cat "$fixture_root/ds.stderr" >&2
		cat "$fixture_root/ds.stdout" >&2
		return 1
	fi
}

run_output_guard_expect() {
	label=$1
	expected=$2
	target=$3
	shift 3
	set +e
	"$runner" "$target" "$@" >"$fixture_root/$label.stdout" 2>"$fixture_root/$label.stderr"
	actual=$?
	set -e
	printf '%s\texpected=%s\tactual=%s\n' "$label" "$expected" "$actual" >>"$artifact_evidence/output-boundary-status.tsv"
	cp "$fixture_root/$label.stdout" "$artifact_evidence/$label.stdout"
	cp "$fixture_root/$label.stderr" "$artifact_evidence/$label.stderr"
	if [ "$actual" -ne "$expected" ]; then
		printf '%s expected exit %s, got %s\n' "$label" "$expected" "$actual" >&2
		cat "$fixture_root/$label.stderr" "$fixture_root/$label.stdout" >&2
		return 1
	fi
	if [ "$expected" -eq 70 ] && [ -s "$fixture_root/$label.stdout" ]; then
		printf '%s emitted JSON before refusing protected output\n' "$label" >&2
		cat "$fixture_root/$label.stdout" >&2
		return 1
	fi
}

verify_bootstrap_collision() {
	for bootstrap_status in 2 64; do
		fake_container_bin="$fixture_root/fake-container-bin-$bootstrap_status"
		mkdir -p "$fake_container_bin"
		printf '#!/bin/sh\nexit %s\n' "$bootstrap_status" >"$fake_container_bin/apk"
		cat >"$fake_container_bin/docker" <<'SH'
#!/bin/sh
while [ "$#" -gt 0 ]; do
	if [ "$1" = sh ]; then
		shift
		[ "$1" = -c ] || exit 125
		shift
		container_script=$1
		shift
		PATH="$ARTIFACT_STATUS_FAKE_APK_DIR:$PATH" sh -c "$container_script" "$@"
		exit $?
	fi
	shift
done
exit 125
SH
		chmod +x "$fake_container_bin/apk" "$fake_container_bin/docker"
		before=$(sha256sum "$project_root/local-api/v1/projects/builder/artifact-status.json" | cut -d ' ' -f 1)
		set +e
		PATH="$fake_container_bin:$PATH" ARTIFACT_STATUS_FAKE_APK_DIR="$fake_container_bin" "$runner" design-system --project-root "$container_project_root" --foundation-root "$container_foundation_root" --mode working-tree >"$fixture_root/bootstrap-$bootstrap_status.stdout" 2>"$fixture_root/bootstrap-$bootstrap_status.stderr"
		bootstrap_exit=$?
		set -e
		[ "$bootstrap_exit" -eq 70 ]
		[ ! -s "$fixture_root/bootstrap-$bootstrap_status.stdout" ]
		grep -F 'container bootstrap dependency install failed' "$fixture_root/bootstrap-$bootstrap_status.stderr" >/dev/null
		after=$(sha256sum "$project_root/local-api/v1/projects/builder/artifact-status.json" | cut -d ' ' -f 1)
		[ "$before" = "$after" ]
		printf 'apk_exit=%s runner_exit=%s stdout_bytes=%s snapshot_before=%s snapshot_after=%s\n' "$bootstrap_status" "$bootstrap_exit" "$(wc -c <"$fixture_root/bootstrap-$bootstrap_status.stdout" | tr -d ' ')" "$before" "$after" >>"$artifact_evidence/bootstrap-collision-results.txt"
		cp "$fixture_root/bootstrap-$bootstrap_status.stdout" "$artifact_evidence/bootstrap-apk-$bootstrap_status.stdout"
		cp "$fixture_root/bootstrap-$bootstrap_status.stderr" "$artifact_evidence/bootstrap-apk-$bootstrap_status.stderr"
	done
}

preserve_ds_stdout() {
	cp "$fixture_root/ds.stdout" "$artifact_evidence/$1.json"
}

seed_design_system() {
	ds_root=$1
	ds_owner_level=$2
	ds_owner_id=$3
	seed_design_system_files "$ds_root" "$ds_owner_level" "$ds_owner_id"
	git -C "$ds_root" init -q
	git -C "$ds_root" config user.email fixture@example.invalid
	git -C "$ds_root" config user.name 'Artifact Status Fixture'
	git -C "$ds_root" add design/system
	git -C "$ds_root" commit -qm 'seed Design System fixture'
	git -C "$ds_root" rev-parse HEAD
}

seed_design_system_files() {
	ds_root=$1
	ds_owner_level=$2
	ds_owner_id=$3
	mkdir -p "$ds_root/design/system/components" "$ds_root/design/system/examples" "$ds_root/design/system/assets"
	cat >"$ds_root/design/system/design-system.json" <<JSON
{"schema_version":"1","id":"builder-system","name":"Builder System","owner":{"level":"$ds_owner_level","id":"$ds_owner_id"},"tokens":[{"id":"ink","type":"color","value":"#000000"}],"components":[{"id":"button","name":"Button","documentation":"components/button.md","token_ids":["ink"],"states":["default"],"variants":[],"examples":["examples/button.md"]}],"assets":["assets/logo.svg"],"contrast_pairs":[]}
JSON
	printf '%s\n' 'Button documentation.' >"$ds_root/design/system/components/button.md"
	printf '%s\n' 'Button example.' >"$ds_root/design/system/examples/button.md"
	printf '%s\n' '<svg/>' >"$ds_root/design/system/assets/logo.svg"
}

write_source_bindings() {
	cat >"$binding_file" <<JSON
{"schema_version":"1","project_id":"builder","company_id":"belluga-solutions","sources":{"default":$1,"company":$2,"project":$3}}
JSON
}

if [ "${ARTIFACT_STATUS_BOOTSTRAP_ONLY:-0}" = 1 ]; then
	run_expect 0 --mode working-tree
	verify_bootstrap_collision
	printf '%s\n' 'artifact-status bootstrap collision: PASS (injected apk exits 2/64 normalized to 70; stdout empty; snapshot preserved)'
	exit 0
fi

if [ "${ARTIFACT_STATUS_TAIL_ONLY:-0}" != 1 ]; then
seed_design_system_files "$foundation_root" project builder
company_revision=$(seed_design_system "$company_ds_root" company belluga-solutions)
default_revision=$(seed_design_system "$default_ds_root" default builder-platform)
project_binding="{\"repository_id\":\"project-foundation\",\"checkout_root\":\"local-api/$fixture_name/foundation\",\"revision\":null,\"definition_path\":\"design/system/design-system.json\"}"
company_binding="{\"repository_id\":\"company-source\",\"checkout_root\":\"local-api/$fixture_name/company\",\"revision\":null,\"definition_path\":\"design/system/design-system.json\"}"
default_binding="{\"repository_id\":\"default-source\",\"checkout_root\":\"local-api/$fixture_name/default\",\"revision\":null,\"definition_path\":\"design/system/design-system.json\"}"

# Project wins over Company and default; capture fresh output for the consumer handoff.
write_source_bindings "$default_binding" "$company_binding" "$project_binding"
run_design_system_expect 0 --mode working-tree
grep -F '"selected_level": "project"' "$fixture_root/ds.stdout" >/dev/null
grep -F '"repository_id": "project-foundation"' "$fixture_root/ds.stdout" >/dev/null
preserve_ds_stdout project-working
cp "$fixture_root/ds.stdout" "$fixture_root/evidence/project-working.json"
project_inventory=$(sed -n 's/.*"inventory_digest": "\([^"]*\)".*/\1/p' "$fixture_root/ds.stdout")
project_definition=$(sed -n 's/.*"definition_digest": "\([^"]*\)".*/\1/p' "$fixture_root/ds.stdout")
[ -n "$project_inventory" ] && [ -n "$project_definition" ]

# Company wins when Project is absent, and default is selected only when both are absent.
write_source_bindings "$default_binding" "$company_binding" null
run_design_system_expect 0 --mode working-tree
grep -F '"selected_level": "company"' "$fixture_root/ds.stdout" >/dev/null
preserve_ds_stdout company-working
cp "$fixture_root/ds.stdout" "$fixture_root/evidence/company-working.json"
company_inventory=$(sed -n 's/.*"inventory_digest": "\([^"]*\)".*/\1/p' "$fixture_root/ds.stdout")
company_definition=$(sed -n 's/.*"definition_digest": "\([^"]*\)".*/\1/p' "$fixture_root/ds.stdout")
write_source_bindings "$default_binding" null null
run_design_system_expect 0 --mode working-tree
grep -F '"selected_level": "default"' "$fixture_root/ds.stdout" >/dev/null
preserve_ds_stdout default-working
cp "$fixture_root/ds.stdout" "$fixture_root/evidence/default-working.json"

# Committed mode uses the selected source's explicit full SHA; it never infers HEAD.
company_binding_committed="{\"repository_id\":\"company-source\",\"checkout_root\":\"local-api/$fixture_name/company\",\"revision\":\"$company_revision\",\"definition_path\":\"design/system/design-system.json\"}"
write_source_bindings null "$company_binding_committed" null
run_design_system_expect 0 --mode committed
grep -F '"revision": "'$company_revision'"' "$fixture_root/ds.stdout" >/dev/null
preserve_ds_stdout company-committed
cp "$fixture_root/ds.stdout" "$fixture_root/evidence/company-committed.json"

# A changed support file changes full inventory_digest without changing raw definition_digest.
printf '%s\n' 'revised documentation' >"$company_ds_root/design/system/components/button.md"
write_source_bindings null "$company_binding" null
run_design_system_expect 0 --mode working-tree
changed_inventory=$(sed -n 's/.*"inventory_digest": "\([^"]*\)".*/\1/p' "$fixture_root/ds.stdout")
changed_definition=$(sed -n 's/.*"definition_digest": "\([^"]*\)".*/\1/p' "$fixture_root/ds.stdout")
[ "$company_inventory" != "$changed_inventory" ]
[ "$company_definition" = "$changed_definition" ]
preserve_ds_stdout company-support-change
cp "$fixture_root/ds.stdout" "$fixture_root/evidence/company-support-change.json"

# Project path is a canonical repository binding, not a self-asserted owner label.
forged_root="$fixture_root/forged-project"
forged_revision=$(seed_design_system "$forged_root" project builder)
forged_binding="{\"repository_id\":\"forged\",\"checkout_root\":\"local-api/$fixture_name/forged-project\",\"revision\":null,\"definition_path\":\"design/system/design-system.json\"}"
write_source_bindings "$default_binding" "$company_binding" "$forged_binding"
run_design_system_expect 2 --mode working-tree
grep -F '"code": "owner_mismatch"' "$fixture_root/ds.stdout" >/dev/null
preserve_ds_stdout forged-project-owner-mismatch

# A broken selected source does not silently fall back to Company or default.
wrong_owner_root="$fixture_root/wrong-owner"
wrong_revision=$(seed_design_system "$wrong_owner_root" company not-belluga)
wrong_binding="{\"repository_id\":\"wrong-owner\",\"checkout_root\":\"local-api/$fixture_name/wrong-owner\",\"revision\":null,\"definition_path\":\"design/system/design-system.json\"}"
write_source_bindings "$default_binding" "$wrong_binding" null
run_design_system_expect 2 --mode working-tree
grep -F '"code": "owner_mismatch"' "$fixture_root/ds.stdout" >/dev/null
preserve_ds_stdout company-definition-owner-mismatch

# Missing and all-null source maps return real no-go JSON; preserve exact outputs for review.
write_source_bindings null null null
run_design_system_expect 2 --mode working-tree
grep -F '"code": "source_binding_missing"' "$fixture_root/ds.stdout" >/dev/null
preserve_ds_stdout all-null-source-binding
cp "$fixture_root/ds.stdout" "$fixture_root/evidence/source-binding-missing.json"
rm -f "$binding_file"
run_design_system_expect 2 --mode working-tree
grep -F '"code": "source_binding_missing"' "$fixture_root/ds.stdout" >/dev/null
preserve_ds_stdout source-map-absent
cp "$fixture_root/ds.stdout" "$fixture_root/evidence/source-map-absent.json"

# Prototype consumer handshake: references bind item identity to the full provider inventory digest.
write_source_bindings "$default_binding" "$company_binding" "$project_binding"
run_design_system_expect 0 --mode working-tree
preserve_ds_stdout project-working-reference-provider
ds_id=$(sed -n 's/.*"id": "\([^"]*\)".*/\1/p' "$fixture_root/ds.stdout" | head -1)
ds_digest=$(sed -n 's/.*"inventory_digest": "\([^"]*\)".*/\1/p' "$fixture_root/ds.stdout")
run_expect 0 --mode working-tree
cp "$fixture_root/stdout" "$artifact_evidence/project-prototype-null-reference-structural-go.json"
cp "$foundation_root/prototypes/sample/prototype.json" "$artifact_evidence/project-prototype-manifest-null-reference.json"
python3 -c 'import json,sys; p=json.load(open(sys.argv[1])); d=json.load(open(sys.argv[2])); m=json.load(open(sys.argv[3])); assert p["outcome"]==d["outcome"]=="go" and m["design_system_ref"] is None and p["design_system_validation"]=="not_evaluated"' "$artifact_evidence/project-prototype-null-reference-structural-go.json" "$fixture_root/ds.stdout" "$artifact_evidence/project-prototype-manifest-null-reference.json"
cp "$foundation_root/prototypes/sample/prototype.json" "$fixture_root/prototype-original.json"
sed 's#"design_system_ref":null#"design_system_ref":{"id":"'"$ds_id"'","content_digest":"'"$ds_digest"'"}#' "$foundation_root/prototypes/sample/prototype.json" >"$fixture_root/manifest.tmp"
mv "$fixture_root/manifest.tmp" "$foundation_root/prototypes/sample/prototype.json"
run_expect 0 --mode working-tree
grep -F '"design_system_validation": "not_evaluated"' "$fixture_root/stdout" >/dev/null
python3 -c 'import json,sys; p=json.load(open(sys.argv[1])); m=json.load(open(sys.argv[2])); d=json.load(open(sys.argv[3])); ref=m["design_system_ref"]; assert p["design_system_validation"]=="not_evaluated" and ref["id"]==d["items"][0]["id"] and ref["content_digest"]==d["inventory_digest"] and ref["content_digest"]!=d["definition_digest"]' "$fixture_root/stdout" "$foundation_root/prototypes/sample/prototype.json" "$fixture_root/ds.stdout"
python3 -c 'import json,sys; p=json.load(open(sys.argv[1])); assert p["outcome"]=="go" and len(p["items"])==2 and p["items"][0]["id"]=="checkout" and [s["id"] for s in p["items"][0]["screens"]]==["start","confirm"]' "$fixture_root/stdout"
cp "$fixture_root/stdout" "$artifact_evidence/prototype-reference-handshake.json"
sed 's#"id":"'"$ds_id"'","content_digest":"'"$ds_digest"'"#"id":"wrong-system","content_digest":"'"$ds_digest"'"#' \
	"$foundation_root/prototypes/sample/prototype.json" >"$fixture_root/manifest.tmp"
mv "$fixture_root/manifest.tmp" "$foundation_root/prototypes/sample/prototype.json"
run_expect 0 --mode working-tree
cp "$foundation_root/prototypes/sample/prototype.json" "$artifact_evidence/project-prototype-manifest-id-mismatch.json"
cp "$fixture_root/stdout" "$artifact_evidence/project-prototype-id-mismatch-structural-go.json"
python3 -c 'import json,sys; p=json.load(open(sys.argv[1])); m=json.load(open(sys.argv[2])); d=json.load(open(sys.argv[3])); ref=m["design_system_ref"]; assert p["outcome"]=="go" and ref["id"]!=d["items"][0]["id"] and ref["content_digest"]==d["inventory_digest"] and p["design_system_validation"]=="not_evaluated"' "$artifact_evidence/project-prototype-id-mismatch-structural-go.json" "$artifact_evidence/project-prototype-manifest-id-mismatch.json" "$fixture_root/ds.stdout"
cp "$fixture_root/prototype-original.json" "$foundation_root/prototypes/sample/prototype.json"

# Bind actual committed Project provider stdout to the exact Prototype Foundation revision.
git -C "$foundation_root" add design/system prototypes
git -C "$foundation_root" commit -qm 'add Project Design System fixture'
project_revision=$(git -C "$foundation_root" rev-parse HEAD)
project_binding_committed="{\"repository_id\":\"project-foundation\",\"checkout_root\":\"local-api/$fixture_name/foundation\",\"revision\":\"$project_revision\",\"definition_path\":\"design/system/design-system.json\"}"
company_binding_committed="{\"repository_id\":\"company-source\",\"checkout_root\":\"local-api/$fixture_name/company\",\"revision\":\"$company_revision\",\"definition_path\":\"design/system/design-system.json\"}"
default_binding_committed="{\"repository_id\":\"default-source\",\"checkout_root\":\"local-api/$fixture_name/default\",\"revision\":\"$default_revision\",\"definition_path\":\"design/system/design-system.json\"}"
write_source_bindings "$default_binding_committed" "$company_binding_committed" "$project_binding_committed"
run_design_system_expect 0 --mode committed
project_ds_id=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["items"][0]["id"])' "$fixture_root/ds.stdout")
project_ds_digest=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["inventory_digest"])' "$fixture_root/ds.stdout")
python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); assert d["schema_version"]=="1" and d["project_id"]=="builder" and d["target"]=="design-system" and d["mode"]=="committed" and d["revision"]==sys.argv[2] and d["outcome"]=="go" and d["selected_level"]=="project" and d["source"]["revision"]==sys.argv[2] and len(d["items"])==1 and d["inventory_digest"] and d["diagnostics"]==[]' "$fixture_root/ds.stdout" "$project_revision"
cp "$fixture_root/ds.stdout" "$fixture_root/evidence/project-committed-before-reference.json"
sed 's#"design_system_ref":null#"design_system_ref":{"id":"'"$project_ds_id"'","content_digest":"'"$project_ds_digest"'"}#' \
	"$foundation_root/prototypes/sample/prototype.json" >"$fixture_root/manifest.tmp"
mv "$fixture_root/manifest.tmp" "$foundation_root/prototypes/sample/prototype.json"
git -C "$foundation_root" add prototypes/sample/prototype.json
git -C "$foundation_root" commit -qm 'bind Prototype fixture to full Design System inventory'
project_revision=$(git -C "$foundation_root" rev-parse HEAD)
project_binding_committed="{\"repository_id\":\"project-foundation\",\"checkout_root\":\"local-api/$fixture_name/foundation\",\"revision\":\"$project_revision\",\"definition_path\":\"design/system/design-system.json\"}"
write_source_bindings "$default_binding_committed" "$company_binding_committed" "$project_binding_committed"
run_design_system_expect 0 --mode committed
cp "$fixture_root/ds.stdout" "$artifact_evidence/project-design-system-valid-committed.json"
cp "$foundation_root/prototypes/sample/prototype.json" "$artifact_evidence/project-prototype-manifest-valid-committed.json"
run_expect 0 --mode committed --revision "$project_revision"
cp "$fixture_root/stdout" "$artifact_evidence/project-prototype-valid-committed.json"
python3 -c 'import json,sys; p=json.load(open(sys.argv[1])); d=json.load(open(sys.argv[2])); m=json.load(open(sys.argv[3])); ref=m["design_system_ref"]; assert p["schema_version"]==d["schema_version"]=="1" and p["project_id"]==d["project_id"]=="builder" and p["target"]=="prototypes" and d["target"]=="design-system" and p["mode"]==d["mode"]=="committed" and p["revision"]==d["revision"]==sys.argv[4] and d["selected_level"]=="project" and d["source"]["revision"]==sys.argv[4] and p["outcome"]==d["outcome"]=="go" and len(d["items"])==1 and ref["id"]==d["items"][0]["id"] and ref["content_digest"]==d["inventory_digest"] and ref["content_digest"]!=d["definition_digest"] and p["design_system_validation"]=="not_evaluated" and p["diagnostics"]==[] and d["diagnostics"]==[]' \
	"$artifact_evidence/project-prototype-valid-committed.json" "$artifact_evidence/project-design-system-valid-committed.json" "$artifact_evidence/project-prototype-manifest-valid-committed.json" "$project_revision"
printf '%s\n' "$project_revision" >"$artifact_evidence/project-valid-foundation-revision.txt"

# A support-only change changes the full inventory digest, leaving the old reference stale.
printf '%s\n' 'changed Project support documentation' >"$foundation_root/design/system/components/button.md"
git -C "$foundation_root" add design/system/components/button.md
git -C "$foundation_root" commit -qm 'change Project Design System support file'
changed_project_revision=$(git -C "$foundation_root" rev-parse HEAD)
project_binding_committed="{\"repository_id\":\"project-foundation\",\"checkout_root\":\"local-api/$fixture_name/foundation\",\"revision\":\"$changed_project_revision\",\"definition_path\":\"design/system/design-system.json\"}"
write_source_bindings "$default_binding_committed" "$company_binding_committed" "$project_binding_committed"
run_design_system_expect 0 --mode committed
cp "$fixture_root/ds.stdout" "$artifact_evidence/project-design-system-support-changed.json"
cp "$foundation_root/prototypes/sample/prototype.json" "$artifact_evidence/project-prototype-manifest-stale-support.json"
run_expect 0 --mode committed --revision "$changed_project_revision"
cp "$fixture_root/stdout" "$artifact_evidence/project-prototype-stale-support-structural-go.json"
python3 -c 'import json,sys; p=json.load(open(sys.argv[1])); d=json.load(open(sys.argv[2])); m=json.load(open(sys.argv[3])); old=json.load(open(sys.argv[4])); ref=m["design_system_ref"]; assert p["outcome"]==d["outcome"]=="go" and p["revision"]==d["revision"]==sys.argv[5] and d["source"]["revision"]==sys.argv[5] and ref["id"]==d["items"][0]["id"] and ref["content_digest"]!=d["inventory_digest"] and d["definition_digest"]==old["definition_digest"] and d["inventory_digest"]!=old["inventory_digest"] and p["design_system_validation"]=="not_evaluated"' \
	"$artifact_evidence/project-prototype-stale-support-structural-go.json" "$artifact_evidence/project-design-system-support-changed.json" "$artifact_evidence/project-prototype-manifest-stale-support.json" "$artifact_evidence/project-design-system-valid-committed.json" "$changed_project_revision"
printf '%s\n' "$changed_project_revision" >"$artifact_evidence/project-stale-foundation-revision.txt"

# A missing provider is no-go evidence while the Prototype remains structurally valid.
rm -f "$binding_file"
run_design_system_expect 2 --mode working-tree
cp "$fixture_root/ds.stdout" "$artifact_evidence/project-design-system-missing-provider.json"
run_expect 0 --mode working-tree
cp "$fixture_root/stdout" "$artifact_evidence/project-prototype-valid-with-missing-provider.json"
python3 -c 'import json,sys; p=json.load(open(sys.argv[1])); d=json.load(open(sys.argv[2])); assert p["outcome"]=="go" and d["outcome"]=="no_go" and d["items"]==[] and p["design_system_validation"]=="not_evaluated"' "$artifact_evidence/project-prototype-valid-with-missing-provider.json" "$artifact_evidence/project-design-system-missing-provider.json"

# Restore a null-reference mutable snapshot for the remaining isolated runner checks.
cp "$fixture_root/prototype-original.json" "$foundation_root/prototypes/sample/prototype.json"
write_source_bindings "$default_binding" "$company_binding" "$project_binding"

# Ten overlapping cross-target runs (five per batch) each retain their own stdout identity;
# every shared snapshot must remain complete JSON regardless of last-writer ordering.
batch=1
while [ "$batch" -le 2 ]; do
	index=1
	: >"$fixture_root/batch-$batch-processes.txt"
	while [ "$index" -le 3 ]; do
		("$runner" design-system --project-root "$container_project_root" --foundation-root "$container_foundation_root" --source-bindings "$container_binding_file" --mode working-tree >"$fixture_root/batch-$batch-ds-$index.json" 2>"$fixture_root/batch-$batch-ds-$index.err") &
		printf '%s batch-%s-ds-%s\n' "$!" "$batch" "$index" >>"$fixture_root/batch-$batch-processes.txt"
		index=$((index + 1))
	done
	index=1
	while [ "$index" -le 2 ]; do
		("$runner" prototypes --project-root "$container_project_root" --foundation-root "$container_foundation_root" --mode working-tree >"$fixture_root/batch-$batch-proto-$index.json" 2>"$fixture_root/batch-$batch-proto-$index.err") &
		printf '%s batch-%s-proto-%s\n' "$!" "$batch" "$index" >>"$fixture_root/batch-$batch-processes.txt"
		index=$((index + 1))
	done
	child_failure=0
	while IFS=' ' read -r pid name; do
		if wait "$pid"; then child_status=0; else child_status=$?; child_failure=1; fi
		printf '%s exit=%s\n' "$name" "$child_status" >>"$artifact_evidence/concurrency-process-status.txt"
		cp "$fixture_root/$name.json" "$artifact_evidence/$name.json"
		cp "$fixture_root/$name.err" "$artifact_evidence/$name.err"
		if [ "$child_status" -ne 0 ]; then
			printf 'batch %s child %s exited %s\n' "$batch" "$name" "$child_status" >&2
			cat "$fixture_root/$name.err" >&2
		fi
	done <"$fixture_root/batch-$batch-processes.txt"
	if [ "$child_failure" -ne 0 ]; then exit 1; fi
	index=1
	while [ "$index" -le 3 ]; do
		python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); assert d["schema_version"]=="1" and d["project_id"]=="builder" and d["company_id"]=="belluga-solutions" and d["target"]=="design-system" and d["mode"]=="working-tree" and d["revision"] is None and d["outcome"]=="go" and len(d["items"])==1 and d["inventory_digest"] and d["definition_digest"] and d["selected_level"]=="project" and d["source"]["repository_id"]=="project-foundation" and d["source"]["owner_level"]=="project" and d["source"]["owner_id"]=="builder" and d["design_system_validation"]=="valid"' "$fixture_root/batch-$batch-ds-$index.json"
		python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); assert d["schema_version"]=="1" and d["project_id"]=="builder" and d["company_id"]=="belluga-solutions" and d["target"]=="design-system" and d["mode"]=="working-tree" and d["revision"] is None and d["outcome"]=="go" and len(d["items"])==1 and d["inventory_digest"] and d["definition_digest"] and d["selected_level"]=="project" and d["source"]["repository_id"]=="project-foundation" and d["source"]["owner_level"]=="project" and d["source"]["owner_id"]=="builder" and d["design_system_validation"]=="valid"' "$fixture_root/batch-$batch-ds-$index.json"
		index=$((index + 1))
	done
	index=1
	while [ "$index" -le 2 ]; do
		python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); assert d["schema_version"]=="1" and d["project_id"]=="builder" and d["target"]=="prototypes" and d["mode"]=="working-tree" and d["revision"] is None and d["outcome"]=="go" and len(d["items"])==2 and d["items"][0]["id"]=="checkout" and [s["id"] for s in d["items"][0]["screens"]]==["start","confirm"] and d["inventory_digest"] and d["design_system_validation"]=="not_evaluated" and d["diagnostics"]==[]' "$fixture_root/batch-$batch-proto-$index.json"
		index=$((index + 1))
	done
		python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); assert d["target"] in ("design-system","prototypes") and d["outcome"]=="go" and isinstance(d["items"],list) and isinstance(d["diagnostics"],list) and not d["diagnostics"]' "$project_root/local-api/v1/projects/builder/artifact-status.json"
	printf 'batch %s: 3 Design System + 2 Prototype = 5 overlapping successful, identity-checked stdout responses\n' "$batch" >>"$artifact_evidence/concurrency-counts.txt"
	batch=$((batch + 1))
done
fi

run_expect 0 --mode working-tree
cmp "$fixture_root/stdout" "$project_root/local-api/v1/projects/builder/artifact-status.json"
working_digest=$(sed -n 's/.*"inventory_digest": "\([^"]*\)".*/\1/p' "$fixture_root/stdout")
[ -n "$working_digest" ]
snapshot_uid=$(stat -c %u "$project_root/local-api/v1/projects/builder/artifact-status.json")
[ "$snapshot_uid" = "$(id -u)" ]

run_expect 0 --mode committed --revision "$commit"
cmp "$fixture_root/stdout" "$project_root/local-api/v1/projects/builder/artifact-status.json"
committed_digest=$(sed -n 's/.*"inventory_digest": "\([^"]*\)".*/\1/p' "$fixture_root/stdout")
[ "$working_digest" = "$committed_digest" ]
grep -F '"revision": "'"$commit"'"' "$fixture_root/stdout" >/dev/null

# Immutable Git-tree evaluation rejects symlink and submodule artifact entries.
ln -s index.html "$foundation_root/prototypes/sample/linked.html"
git -C "$foundation_root" add prototypes/sample/linked.html
git -C "$foundation_root" commit -qm 'add committed Prototype symlink fixture'
committed_symlink_revision=$(git -C "$foundation_root" rev-parse HEAD)
run_expect 2 --mode committed --revision "$committed_symlink_revision"
grep -F '"code": "unsupported_file"' "$fixture_root/stdout" >/dev/null
grep -F '"path": "prototypes/sample/linked.html"' "$fixture_root/stdout" >/dev/null
cp "$fixture_root/stdout" "$artifact_evidence/prototype-committed-symlink-no-go.json"
rm "$foundation_root/prototypes/sample/linked.html"
git -C "$foundation_root" add -u prototypes/sample/linked.html
git -C "$foundation_root" commit -qm 'remove committed Prototype symlink fixture'

submodule_root="$fixture_root/nested-submodule"
mkdir -p "$submodule_root"
git -C "$submodule_root" init -q
git -C "$submodule_root" config user.email fixture@example.invalid
git -C "$submodule_root" config user.name 'Artifact Status Fixture'
printf '%s\n' 'nested source' >"$submodule_root/README.md"
git -C "$submodule_root" add README.md
git -C "$submodule_root" commit -qm 'seed nested repository fixture'
submodule_revision=$(git -C "$submodule_root" rev-parse HEAD)
git -C "$foundation_root" update-index --add --cacheinfo "160000,$submodule_revision,prototypes/checkout/nested-submodule"
git -C "$foundation_root" commit -qm 'add committed Prototype submodule fixture'
committed_submodule_revision=$(git -C "$foundation_root" rev-parse HEAD)
run_expect 2 --mode committed --revision "$committed_submodule_revision"
grep -F '"code": "unsupported_file"' "$fixture_root/stdout" >/dev/null
grep -F '"path": "prototypes/checkout/nested-submodule"' "$fixture_root/stdout" >/dev/null
cp "$fixture_root/stdout" "$artifact_evidence/prototype-committed-submodule-no-go.json"
git -C "$foundation_root" update-index --force-remove prototypes/checkout/nested-submodule
git -C "$foundation_root" commit -qm 'remove committed Prototype submodule fixture'

# Committed enumeration must be independent of a large unrelated Foundation subtree.
large_baseline_revision=$(git -C "$foundation_root" rev-parse HEAD)
run_expect 0 --mode committed --revision "$large_baseline_revision"
large_baseline_digest=$(sed -n 's/.*"inventory_digest": "\([^"]*\)".*/\1/p' "$fixture_root/stdout")
[ -n "$large_baseline_digest" ]
mkdir -p "$foundation_root/unrelated"
i=0
while [ "$i" -lt 2100 ]; do printf x >"$foundation_root/unrelated/$i"; i=$((i + 1)); done
git -C "$foundation_root" add unrelated
git -C "$foundation_root" commit -qm 'add unrelated fixture tree'
large_commit=$(git -C "$foundation_root" rev-parse HEAD)
run_expect 0 --mode committed --revision "$large_commit"
[ "$large_baseline_digest" = "$(sed -n 's/.*"inventory_digest": "\([^"]*\)".*/\1/p' "$fixture_root/stdout")" ]

# Ignored/untracked files remain visible in working-tree mode.
printf '%s\n' 'prototypes/sample/untracked.txt' >"$foundation_root/.gitignore"
printf x >"$foundation_root/prototypes/sample/untracked.txt"
before=$(sha256sum "$project_root/local-api/v1/projects/builder/artifact-status.json" | cut -d ' ' -f 1)
run_expect 2 --mode working-tree
grep -F '"code": "undeclared_file"' "$fixture_root/stdout" >/dev/null
after=$(sha256sum "$project_root/local-api/v1/projects/builder/artifact-status.json" | cut -d ' ' -f 1)
[ "$before" != "$after" ]

# Related evidence reads reject a symlink in an ancestor outside prototypes/.
rm -f "$foundation_root/prototypes/sample/untracked.txt" "$foundation_root/.gitignore"
mkdir -p "$foundation_root/outside"
printf evidence >"$foundation_root/outside/evidence.md"
ln -s ../outside "$foundation_root/artifacts/linked"
sed 's#"related":\[\]#"related":[{"kind":"todo","path":"artifacts/linked/evidence.md"}]#' \
	"$foundation_root/prototypes/sample/prototype.json" >"$fixture_root/manifest.tmp"
mv "$fixture_root/manifest.tmp" "$foundation_root/prototypes/sample/prototype.json"
run_expect 2 --mode working-tree
grep -F '"code": "invalid_reference"' "$fixture_root/stdout" >/dev/null
grep -F '"path": "artifacts/linked/evidence.md"' "$fixture_root/stdout" >/dev/null

# Usage and infrastructure exits retain the previous snapshot unchanged.
before=$(sha256sum "$project_root/local-api/v1/projects/builder/artifact-status.json" | cut -d ' ' -f 1)
run_expect 64 --mode committed --revision main
after=$(sha256sum "$project_root/local-api/v1/projects/builder/artifact-status.json" | cut -d ' ' -f 1)
[ "$before" = "$after" ]

verify_bootstrap_collision
run_expect 64 --mode working-tree --workspace-root /
run_expect 70 --mode working-tree --foundation-root /workspace/builder-app/service
after=$(sha256sum "$project_root/local-api/v1/projects/builder/artifact-status.json" | cut -d ' ' -f 1)
[ "$before" = "$after" ]
mkdir -p "$fixture_root/fake-bin"
printf '#!/bin/sh\nexit 125\n' >"$fixture_root/fake-bin/docker"
chmod +x "$fixture_root/fake-bin/docker"
set +e
PATH="$fixture_root/fake-bin:$PATH" "$runner" prototypes --project-root "$container_project_root" --foundation-root "$container_foundation_root" --mode working-tree >"$fixture_root/stdout" 2>"$fixture_root/stderr"
fake_status=$?
set -e
[ "$fake_status" -eq 70 ]
grep -F 'Docker or runner bootstrap failed' "$fixture_root/stderr" >/dev/null
after=$(sha256sum "$project_root/local-api/v1/projects/builder/artifact-status.json" | cut -d ' ' -f 1)
[ "$before" = "$after" ]

# Reject every output root whose fixed snapshot would mutate a read-only source.
# These disposable roots are inside the mounted workspace so trustedRoot is not the rejection cause.
foundation_status_before=$(git -C "$foundation_root" status --porcelain --untracked-files=all)
run_output_guard_expect prototypes-equal-foundation 70 prototypes \
	--project-root "$container_foundation_root" --foundation-root "$container_foundation_root" --mode working-tree
[ ! -e "$foundation_root/local-api/v1/projects/builder/artifact-status.json" ]
[ "$foundation_status_before" = "$(git -C "$foundation_root" status --porcelain --untracked-files=all)" ]

descendant_project_root="$foundation_root/output-project"
mkdir -p "$descendant_project_root"
container_descendant_project_root="$container_foundation_root/output-project"
foundation_status_before=$(git -C "$foundation_root" status --porcelain --untracked-files=all)
run_output_guard_expect prototypes-unavailable-revision-descendant 70 prototypes \
	--project-root "$container_descendant_project_root" --foundation-root "$container_foundation_root" \
	--mode committed --revision ffffffffffffffffffffffffffffffffffffffff
[ ! -e "$descendant_project_root/local-api/v1/projects/builder/artifact-status.json" ]
[ "$foundation_status_before" = "$(git -C "$foundation_root" status --porcelain --untracked-files=all)" ]

missing_bindings="$container_fixture_workspace/$fixture_name/local-api/missing-source-bindings.json"
foundation_status_before=$(git -C "$foundation_root" status --porcelain --untracked-files=all)
run_output_guard_expect design-system-missing-map-foundation 70 design-system \
	--project-root "$container_foundation_root" --foundation-root "$container_foundation_root" \
	--source-bindings "$missing_bindings" --mode working-tree
[ ! -e "$foundation_root/local-api/v1/projects/builder/artifact-status.json" ]
[ "$foundation_status_before" = "$(git -C "$foundation_root" status --porcelain --untracked-files=all)" ]

# Selected Company go and no-go responses must not place the snapshot in that source either.
company_output_project="$company_ds_root/output-project"
container_company_output_project="/workspace/local-api/$fixture_name/company/output-project"
mkdir -p "$company_output_project"
write_source_bindings null "$company_binding" null
company_status_before=$(git -C "$company_ds_root" status --porcelain --untracked-files=all)
run_output_guard_expect design-system-company-go-descendant 70 design-system \
	--project-root "$container_company_output_project" --foundation-root "$container_foundation_root" \
	--source-bindings "$container_binding_file" --mode working-tree
[ ! -e "$company_output_project/local-api/v1/projects/builder/artifact-status.json" ]
[ "$company_status_before" = "$(git -C "$company_ds_root" status --porcelain --untracked-files=all)" ]

printf '{\n' >"$company_ds_root/design/system/design-system.json"
write_source_bindings null "$company_binding" null
run_design_system_expect 2 --mode working-tree
cp "$fixture_root/ds.stdout" "$artifact_evidence/design-system-company-no-go-control.json"
python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); assert d["selected_level"]=="company" and d["outcome"]=="no_go"' "$artifact_evidence/design-system-company-no-go-control.json"
company_status_before=$(git -C "$company_ds_root" status --porcelain --untracked-files=all)
run_output_guard_expect design-system-company-no-go-descendant 70 design-system \
	--project-root "$container_company_output_project" --foundation-root "$container_foundation_root" \
	--source-bindings "$container_binding_file" --mode working-tree
[ ! -e "$company_output_project/local-api/v1/projects/builder/artifact-status.json" ]
[ "$company_status_before" = "$(git -C "$company_ds_root" status --porcelain --untracked-files=all)" ]

# A strictly decodable but semantically invalid map still cannot direct output
# into any safely declared fixed-slot source, selected or unselected.
write_source_bindings "$default_binding" "$company_binding" null
sed 's/"project_id":"builder"/"project_id":"other"/' "$binding_file" >"$fixture_root/invalid-identity.json"
cp "$fixture_root/invalid-identity.json" "$binding_file"
run_design_system_expect 2 --mode working-tree
cp "$fixture_root/ds.stdout" "$artifact_evidence/design-system-invalid-identity-control.json"
python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); assert d["outcome"]=="no_go" and any(x["code"]=="invalid_schema" for x in d["diagnostics"])' "$artifact_evidence/design-system-invalid-identity-control.json"

company_status_before=$(git -C "$company_ds_root" status --porcelain --untracked-files=all)
default_status_before=$(git -C "$default_ds_root" status --porcelain --untracked-files=all)
foundation_status_before=$(git -C "$foundation_root" status --porcelain --untracked-files=all)
run_output_guard_expect design-system-invalid-identity-foundation 70 design-system \
	--project-root "$container_foundation_root" --foundation-root "$container_foundation_root" \
	--source-bindings "$container_binding_file" --mode working-tree
run_output_guard_expect design-system-invalid-identity-selected-company 70 design-system \
	--project-root "$container_company_output_project" --foundation-root "$container_foundation_root" \
	--source-bindings "$container_binding_file" --mode working-tree
default_output_project="$default_ds_root/output-project"
mkdir -p "$default_output_project"
container_default_output_project="/workspace/local-api/$fixture_name/default/output-project"
run_output_guard_expect design-system-invalid-identity-unselected-default 70 design-system \
	--project-root "$container_default_output_project" --foundation-root "$container_foundation_root" \
	--source-bindings "$container_binding_file" --mode working-tree
[ ! -e "$foundation_root/local-api/v1/projects/builder/artifact-status.json" ]
[ ! -e "$company_output_project/local-api/v1/projects/builder/artifact-status.json" ]
[ ! -e "$default_output_project/local-api/v1/projects/builder/artifact-status.json" ]
[ "$foundation_status_before" = "$(git -C "$foundation_root" status --porcelain --untracked-files=all)" ]
[ "$company_status_before" = "$(git -C "$company_ds_root" status --porcelain --untracked-files=all)" ]
[ "$default_status_before" = "$(git -C "$default_ds_root" status --porcelain --untracked-files=all)" ]

# A Project root above Foundation is valid when the fixed Project snapshot is outside it.
# Restore the related-reference fixture after its deliberate symlink rejection case.
rm "$foundation_root/artifacts/linked"
mkdir -p "$foundation_root/artifacts/linked"
cp "$foundation_root/outside/evidence.md" "$foundation_root/artifacts/linked/evidence.md"
foundation_status_before=$(git -C "$foundation_root" status --porcelain --untracked-files=all)
run_output_guard_expect ancestor-project-root-allowed 0 prototypes \
	--project-root "$container_fixture_workspace" --foundation-root "$container_foundation_root" --mode working-tree
python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); assert d["outcome"]=="go" and d["target"]=="prototypes"' "$artifact_evidence/ancestor-project-root-allowed.stdout"
[ -f "$repo_root/local-api/v1/projects/builder/artifact-status.json" ]
[ "$foundation_status_before" = "$(git -C "$foundation_root" status --porcelain --untracked-files=all)" ]

# The actual Project must stay absent and produce a real no-go, never a fabricated catalog.
set +e
"$runner" prototypes --mode working-tree >"$fixture_root/real.stdout" 2>"$fixture_root/real.stderr"
real_status=$?
set -e
printf 'prototypes\texpected=2\tactual=%s\n' "$real_status" >"$artifact_evidence/real-project-status.tsv"
cp "$fixture_root/real.stdout" "$artifact_evidence/real-project-prototypes.stdout"
cp "$fixture_root/real.stderr" "$artifact_evidence/real-project-prototypes.stderr"
if [ "$real_status" -ne 2 ]; then
	printf 'real-project missing-catalog invocation expected exit 2, got %s\n' "$real_status" >&2
	cat "$fixture_root/real.stderr" "$fixture_root/real.stdout" >&2
	exit 1
fi
grep -F '"code": "missing_catalog"' "$fixture_root/real.stdout" >/dev/null
[ ! -e "$repo_root/belluga_builder_foundation_documentation/prototypes/catalog.json" ]
[ "$(stat -c %u "$repo_root/local-api/v1/projects/builder/artifact-status.json")" = "$(id -u)" ]

# The real Project must also remain without a private binding map and fail closed.
set +e
"$runner" design-system --mode working-tree >"$fixture_root/real-ds.stdout" 2>"$fixture_root/real-ds.stderr"
real_ds_status=$?
set -e
printf 'design-system\texpected=2\tactual=%s\n' "$real_ds_status" >>"$artifact_evidence/real-project-status.tsv"
cp "$fixture_root/real-ds.stdout" "$artifact_evidence/real-project-design-system.stdout"
cp "$fixture_root/real-ds.stderr" "$artifact_evidence/real-project-design-system.stderr"
[ "$real_ds_status" -eq 2 ]
grep -F '"code": "source_binding_missing"' "$fixture_root/real-ds.stdout" >/dev/null
[ ! -e "$repo_root/local-api/source-bindings.json" ]
mkdir -p "$repo_root/belluga_builder_foundation_documentation/artifacts/tmp/design-system-fixtures"
if [ "${ARTIFACT_STATUS_TAIL_ONLY:-0}" != 1 ]; then
	cp "$fixture_root/evidence/"*.json "$artifact_evidence/"
fi
cp "$fixture_root/real-ds.stdout" "$artifact_evidence/source-binding-missing-real.stdout"
cp "$fixture_root/real-ds.stderr" "$artifact_evidence/source-binding-missing-real.stderr"

if [ "${ARTIFACT_STATUS_TAIL_ONLY:-0}" = 1 ]; then
	printf '%s\n' 'artifact-status Prototype tail diagnostic: PASS (not the full Design System integration matrix)'
else
	printf '%s\n' 'artifact-status runner integration: PASS (working-tree, committed, bounded subtree, ignored file, no-follow related path, exits 0/2/64/70, stale snapshots, caller UID, real missing catalog)'
fi
