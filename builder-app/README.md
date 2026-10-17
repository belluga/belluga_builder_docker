# Builder Project Knowledge preview

The MVP serves the registered Company/Project catalog anonymously and read-only. It provides no IAM, MCP or editing. Only admitted, fingerprint-verified snapshot bytes are served. Loading a Project asks the private Delphi reader for the current published Foundation revision; registration binds identity and source, but does not publish content. Old path-only bindings are invalid and must be registered again.

From the Builder Docker root:

Set `DELPHI_PROJECT_READER_TOKEN` to a runtime-only 32+ character bearer secret shared by the Builder service and private reader, never by the browser. Set `DELPHI_GITHUB_READ_TOKEN` to a read-only credential scoped to the registered Foundation repositories in the private reader environment only. Do not commit either value or pass the Git credential to the Builder service.

```bash
docker build --target go-test -f builder-app/Dockerfile builder-app
docker build --target web-test -f builder-app/Dockerfile builder-app
docker build --target runtime -f builder-app/Dockerfile builder-app
DELPHI_BUILDER_STATE_ROOT=/home/elton/Dev/builder-local-state \
BUILDER_UID=$(id -u) BUILDER_GID=$(id -g) \
docker compose -p builder-mvp -f compose.builder.yaml up --build -d
```

The `go-test` stage installs Git for its Git-backed integration test fixtures; Git is not included in the final Builder runtime image. The private Delphi reader image contains Git for its published-source reads.

Run that Compose command from the dedicated `mvp` worktree after fetching and fast-forwarding it to the promoted remote `mvp` revision. A `git fetch` alone does not advance the checked-out worktree. The public UI is at `https://builder.belluga.online/` (or `http://127.0.0.1:8088/` on the host). The Builder runtime mounts shared derived state read-only; it does not mount a Foundation checkout or `.git`. On Project load, the private reader resolves the published Foundation head and refreshes the derived snapshot if needed, without another registration. A code change still requires `mvp` promotion and worktree fast-forward.

For the shared tunnel, use the existing external Docker network `builder-preview-ingress` and keep its connector attached while preserving its default network. In Cloudflare's tunnel hostname configuration, point **only** `builder.belluga.online` to `http://builder-preview:8080`. Do not point a public route at the host-loopback port or expose `8088` on a non-loopback address.

After deployment, verify the anonymous Company → Project → Overview/Roadmap journey, direct links, artifact isolation and negative paths through the hostname, then smoke the pre-existing tunnel hostnames. To stop only the MVP runtime, run `docker compose -p builder-mvp -f compose.builder.yaml down` from the `mvp` worktree.

## Local authoring preview

The development registered-Project viewer remains loopback-only at `http://127.0.0.1:8089/`. The separate MVP service publishes the same registered catalog at the approved domain. Both consumers mount the explicitly selected shared registration state directory read-only; neither mounts Project/Company/Foundation checkouts, `.git`, or environment files.

For this Builder workspace, the operator-selected shared state directory is `/home/elton/Dev/builder-local-state` (private, mode 0700, outside Project repositories). Set `DELPHI_BUILDER_STATE_ROOT` to that directory and `BUILDER_UID`/`BUILDER_GID` to the local user's IDs, then run `sh builder-app/service/preview-artifacts.sh` from this root with the private reader secrets configured. This path is local operator context, not a Project input or shared Delphi default. The public Delphi launcher resolves Project/Foundation bindings and private storage internally; a Project caller supplies its identity, not filesystem or state paths. See the shared `register-builder-project` skill and `delphi-ai/local-api/contract.md`. Initialization does not register Projects automatically. The Builder consumer verifies fingerprinted bytes; the private reader resolves current source freshness on Project load and visible-view checks. The saved Knowledge status is a document-review observation, not evidence that the Landing visual artifact matches the current source. The preview launcher rebuilds the consumer; it does not register Projects.

Only files listed as available in a verified immutable snapshot are served, from fingerprint-scoped routes so relative assets remain inside that same snapshot. HTML receives a response CSP sandbox without `allow-same-origin`; Prototype HTML alone receives `allow-scripts` within the response sandbox, and the iframe remains separately sandboxed. Integrity, saved freshness observations, and human review are presented as distinct facts. The viewer is local rendering only, not publication, review, visual-fidelity approval, or an AI capability claim.
