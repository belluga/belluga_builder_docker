# Builder Project Knowledge preview

This is an anonymous, read-only preview of one reviewed Builder Foundation revision. It is **not** general Project access and provides no IAM, MCP, editing, or remote freshness attestation.

From the Builder Docker root:

```bash
docker build --target go-test -f builder-app/Dockerfile builder-app
docker build --target web-test -f builder-app/Dockerfile builder-app
docker build --target runtime -f builder-app/Dockerfile builder-app
docker compose -f compose.builder.yaml up --build -d
```

The `go-test` stage installs Git for its Git-backed integration test fixtures; Git is not included in the final runtime image.

The local preview is at `http://127.0.0.1:8088`. The Go API exposes only `/api/preview` and `/api/preview/documents/{approved-id}`. A trusted one-shot exporter reads the pinned Git commit from the dedicated Foundation checkout and writes only the approved eight-file bundle to a named volume. The public runtime mounts that volume read-only; it does not mount the Foundation checkout or `.git`. It rejects incomplete, extra, oversized, or digest-mismatched bundle content at startup. A changed checkout does not update the served revision.

For the shared tunnel, attach its connector to the external Docker network `builder-preview-ingress` while preserving its default network. In Cloudflare's tunnel hostname configuration, point **only** `builder.belluga.online` to `http://builder-preview:8080`. The tunnel connector token is not a route-management credential; adding the hostname requires Cloudflare dashboard/API access. Do not point a public route at the host-loopback port or expose `8088` on a non-loopback address.

After route creation, verify the anonymous Landing → Foundation → Genesis journey and negative paths through the hostname, then smoke the pre-existing tunnel hostnames. To stop only the Builder runtime, run `docker compose -f compose.builder.yaml down` from this root. This preserves the named bundle volume. A new Foundation revision requires a separate content review, approved catalog/digest update, and a deliberate replacement of the publication bundle; it is never automatic.

## Local authoring preview

The separate registered-Project viewer is loopback-only at `http://127.0.0.1:8089/local`. The public `compose.builder.yaml`, port `8088`, immutable pin and tunnel route remain unchanged. The local consumer mounts only the explicitly selected shared registration state directory read-only; it never mounts Project/Company/Foundation checkouts, `.git`, or environment files.

For this Builder workspace, the explicitly selected shared state directory is `/home/elton/Dev/builder-local-state` (private, mode 0700, outside Project repositories). Set `DELPHI_BUILDER_STATE_ROOT` to that directory and `BUILDER_UID`/`BUILDER_GID` to the local user's IDs, then run `./builder-app/service/preview-artifacts.sh` from this root. This path is local machine handoff context, not a shared Delphi default. Registration and status require explicit workspace, Project, Foundation, and identity flags; see the shared `register-builder-project` skill and `delphi-ai/local-api/contract.md`. Initialization does not register Projects automatically. The UI verifies stored snapshot bytes by fingerprint; it does not check source freshness live. The saved Knowledge status is a document-review observation made during preparation, not evidence that the Landing visual artifact matches the current source. Re-run `builder-project register` for the explicitly selected Project to refresh its snapshot.

Only files listed as available in a verified immutable snapshot are served, from fingerprint-scoped routes so relative assets remain inside that same snapshot. HTML receives a response CSP sandbox without `allow-same-origin`; Prototype HTML alone receives `allow-scripts` within the response sandbox, and the iframe remains separately sandboxed. Integrity, saved freshness observations, and human review are presented as distinct facts. The viewer is local rendering only, not publication, review, visual-fidelity approval, or an AI capability claim.
