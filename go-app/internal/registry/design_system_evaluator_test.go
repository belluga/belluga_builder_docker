package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ac "belluga-builder-viewer/artifact_catalog"
)

func TestRegistryUsesSharedDesignSystemEvaluatorOnExactGitRevision(t *testing.T) {
	root := t.TempDir()
	runFixtureGit(t, root, "init", "--quiet", "--initial-branch=main")
	runFixtureGit(t, root, "config", "user.name", "Registry Fixture")
	runFixtureGit(t, root, "config", "user.email", "registry-fixture@example.invalid")
	definitionPath := filepath.Join(root, filepath.FromSlash(designSystemDefinitionPath))
	if err := os.MkdirAll(filepath.Dir(definitionPath), 0o700); err != nil {
		t.Fatal(err)
	}
	definition := []byte(`{"schema_version":"1","id":"fixture-system","name":"Fixture","owner":{"level":"project","id":"fixture-project"},"tokens":[{"id":"label","type":"string","value":"Fixture"}],"components":[],"assets":[],"contrast_pairs":[]}`)
	if err := os.WriteFile(definitionPath, definition, 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, root, "add", ".")
	runFixtureGit(t, root, "commit", "--quiet", "-m", "valid design system")
	revision := fixtureGitOutput(t, root, "rev-parse", "HEAD")

	source, err := ac.NewGitTree(root, revision)
	if err != nil {
		t.Fatalf("construct exact Git source: %v", err)
	}
	binding := ac.SourceBinding{RepositoryID: "belluga/fixture", Revision: &revision, DefinitionPath: designSystemDefinitionPath}
	response := ac.EvaluateDesignSystem(source, "fixture-project", "belluga-solutions", "project", binding)
	if response.Outcome != "go" || response.DesignSystemValidation != "valid" || response.Revision == nil || *response.Revision != revision || len(response.Items) != 1 {
		t.Fatalf("shared evaluator did not accept the exact complete candidate: %+v", response)
	}

	invalidPath := filepath.Join(root, filepath.FromSlash(designSystemDefinitionPath))
	invalid := strings.Replace(string(definition), `"fixture-project"`, `"another-project"`, 1)
	if err := os.WriteFile(invalidPath, []byte(invalid), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, root, "add", ".")
	runFixtureGit(t, root, "commit", "--quiet", "-m", "wrong owner")
	invalidRevision := fixtureGitOutput(t, root, "rev-parse", "HEAD")
	invalidSource, err := ac.NewGitTree(root, invalidRevision)
	if err != nil {
		t.Fatalf("construct invalid exact Git source: %v", err)
	}
	binding.Revision = &invalidRevision
	response = ac.EvaluateDesignSystem(invalidSource, "fixture-project", "belluga-solutions", "project", binding)
	if response.Outcome != "no_go" || response.DesignSystemValidation == "valid" {
		t.Fatalf("shared evaluator accepted a candidate owned by another Project: %+v", response)
	}
}
