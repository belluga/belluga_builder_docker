# Builder Project Knowledge preview

The MVP serves the registered Company/Project catalog anonymously and read-only. It provides no IAM, MCP, editing, or remote freshness attestation. Only admitted, fingerprint-verified snapshot bytes are served; explicit registration refreshes Project content.

From the Builder Docker root:

```bash
docker build --target go-test -f builder-app/Dockerfile builder-app
docker build --target web-test -f builder-app/Dockerfile builder-app
docker build --target runtime -f builder-app/Dockerfile builder-app
DELPHI_BUILDER_STATE_ROOT=/home/elton/Dev/builder-local-state \
BUILDER_UID=$(id -u) BUILDER_GID=$(id -g) \
docker compose -p builder-mvp -f compose.builder.yaml up --build -d
```

The `go-test` stage installs Git for its Git-backed integration test fixtures; Git is not included in the final runtime image.

Run that Compose command from the dedicated `mvp` worktree after fetching and fast-forwarding it to the promoted remote `mvp` revision. A `git fetch` alone does not advance the checked-out worktree. The public UI is at `https://builder.belluga.online/local` (or `http://127.0.0.1:8088/local` on the host). The runtime mounts the shared registration state read-only; it does not mount a Foundation checkout or `.git`. It verifies the selected snapshot before serving admitted artifacts. A Foundation change requires explicit matching registration to refresh that Project's saved snapshot; a code change requires another `mvp` promotion and worktree fast-forward.

For the shared tunnel, attach its connector to the external Docker network `builder-preview-ingress` while preserving its default network. In Cloudflare's tunnel hostname configuration, point **only** `builder.belluga.online` to `http://builder-preview:8080`. The tunnel connector token is not a route-management credential; adding the hostname requires Cloudflare dashboard/API access. Do not point a public route at the host-loopback port or expose `8088` on a non-loopback address.

After deployment, verify the anonymous Company → Project → Overview/Roadmap journey, direct links, artifact isolation and negative paths through the hostname, then smoke the pre-existing tunnel hostnames. To stop only the MVP runtime, run `docker compose -p builder-mvp -f compose.builder.yaml down` from the `mvp` worktree.

## Local authoring preview

The development registered-Project viewer remains loopback-only at `http://127.0.0.1:8089/local`. The separate MVP service publishes the same registered catalog at the approved domain. Both consumers mount the explicitly selected shared registration state directory read-only; neither mounts Project/Company/Foundation checkouts, `.git`, or environment files.

For this Builder workspace, the explicitly selected shared state directory is `/home/elton/Dev/builder-local-state` (private, mode 0700, outside Project repositories). Set `DELPHI_BUILDER_STATE_ROOT` to that directory and `BUILDER_UID`/`BUILDER_GID` to the local user's IDs, then run `./builder-app/service/preview-artifacts.sh` from this root. This path is local machine handoff context, not a shared Delphi default. Registration and status require explicit workspace, Project, Foundation, and identity flags; see the shared `register-builder-project` skill and `delphi-ai/local-api/contract.md`. Initialization does not register Projects automatically. The UI verifies stored snapshot bytes by fingerprint; it does not check source freshness live. The saved Knowledge status is a document-review observation made during preparation, not evidence that the Landing visual artifact matches the current source. Re-run `builder-project register` for the explicitly selected Project to refresh its snapshot.

Only files listed as available in a verified immutable snapshot are served, from fingerprint-scoped routes so relative assets remain inside that same snapshot. HTML receives a response CSP sandbox without `allow-same-origin`; Prototype HTML alone receives `allow-scripts` within the response sandbox, and the iframe remains separately sandboxed. Integrity, saved freshness observations, and human review are presented as distinct facts. The viewer is local rendering only, not publication, review, visual-fidelity approval, or an AI capability claim.
