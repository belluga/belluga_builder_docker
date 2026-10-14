# Builder local checks

`local-api/knowledge-status` and `local-api/artifact-status` are thin forwarding entrypoints to the shared Delphi evaluators. They preserve Builder-root convenience, but do not contain evaluator logic or select Builder identity implicitly for other callers. The local preview runner supplies explicit workspace, Project, Foundation, Company and binding-map context.

From the Builder root:

```sh
./local-api/knowledge-status status --workspace-root "$PWD" --project-root . --foundation-root belluga_builder_foundation_documentation --project-id builder
./local-api/artifact-status design-system --workspace-root "$PWD" --project-root . --foundation-root belluga_builder_foundation_documentation --project-id builder --company-id belluga-solutions --source-bindings local-api/source-bindings.json --mode working-tree
./local-api/artifact-status prototypes --workspace-root "$PWD" --project-root . --foundation-root belluga_builder_foundation_documentation --project-id builder --mode working-tree
```

These commands emit fresh JSON to stdout and do not write snapshots or review markers. The private `local-api/source-bindings.json` is read only by the Design System command; never print or commit it. Prototype status is structural evidence only and does not execute or render a candidate.

Registering or refreshing what the local Builder panel serves is a separate, explicit `builder-project register` operation through the shared Delphi API. A successful structural check does not refresh a previously saved panel snapshot.
