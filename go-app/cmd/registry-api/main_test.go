package main

import (
	"os"
	"path/filepath"
	"testing"

	"belluga-builder-go/internal/registry"
)

func TestBoundRoadmapPathUsesRegisteredFoundation(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "project", "foundation", "roadmap", "catalog.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":"builder-roadmap-v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := boundRoadmapPath(&registry.Binding{WorkspaceRoot: workspace, ProjectRoot: "project", FoundationRoot: "foundation"})
	if err != nil || resolved != path {
		t.Fatalf("bound roadmap path = %q, %v; want %q", resolved, err, path)
	}
}

func TestBoundRoadmapPathRejectsWorkspaceEscape(t *testing.T) {
	workspace := t.TempDir()
	if _, err := boundRoadmapPath(&registry.Binding{WorkspaceRoot: workspace, ProjectRoot: "..", FoundationRoot: "foundation"}); err == nil {
		t.Fatal("accepted Project binding outside its registered workspace")
	}
}
