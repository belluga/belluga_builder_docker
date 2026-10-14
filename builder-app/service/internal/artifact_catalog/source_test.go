package artifact_catalog

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitTreeRejectsOversizedBlobBeforeRead(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "payload.bin"), make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitFixture(t, root, "init", "-q")
	runGitFixture(t, root, "config", "user.email", "fixture@example.invalid")
	runGitFixture(t, root, "config", "user.name", "Fixture")
	runGitFixture(t, root, "add", "payload.bin")
	runGitFixture(t, root, "commit", "-qm", "fixture")

	source, err := NewGitTree(root, "")
	if err != nil {
		t.Fatal(err)
	}
	data, mode, err := source.Read("payload.bin", 10)
	if !errors.Is(err, ErrLimit) || mode != ModeRegular || len(data) != 0 {
		t.Fatalf("oversized committed blob was not rejected before read: bytes=%d mode=%d err=%v", len(data), mode, err)
	}
}

func TestGitTreeListsOnlyTheSelectedManagedSubtree(t *testing.T) {
	root := t.TempDir()
	for _, item := range []struct{ path, contents string }{{"prototypes/catalog.json", "{}"}, {"design/system/design-system.json", "{}"}, {"unrelated/large.bin", "unrelated"}} {
		path := filepath.Join(root, filepath.FromSlash(item.path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(item.contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGitFixture(t, root, "init", "-q")
	runGitFixture(t, root, "config", "user.email", "fixture@example.invalid")
	runGitFixture(t, root, "config", "user.name", "Fixture")
	runGitFixture(t, root, "add", ".")
	runGitFixture(t, root, "commit", "-qm", "fixture")
	source, err := NewGitTree(root, "")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := source.List("design/system")
	if err != nil {
		t.Fatalf("selected Design System subtree could not be listed: %v", err)
	}
	foundDefinition := false
	foundRoot := false
	for _, entry := range entries {
		if entry.Path == "design/system" && entry.Mode == ModeDirectory {
			foundRoot = true
		}
		if entry.Path == "design/system/design-system.json" && entry.Mode == ModeRegular {
			foundDefinition = true
		}
		if entry.Path != "design/system" && !strings.HasPrefix(entry.Path, "design/system/") {
			t.Fatalf("subtree listing included another authority: %#v", entries)
		}
	}
	if !foundRoot || !foundDefinition {
		t.Fatalf("subtree inventory omitted its root or definition: %#v", entries)
	}
}

func TestGitTreeDoesNotTreatNonDirectoryCollectionRootAsAbsent(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "target"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "target", "README.md"), []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(root, "prototypes")); err != nil {
		t.Fatal(err)
	}
	runGitFixture(t, root, "init", "-q")
	runGitFixture(t, root, "config", "user.email", "fixture@example.invalid")
	runGitFixture(t, root, "config", "user.name", "Fixture")
	runGitFixture(t, root, "add", ".")
	runGitFixture(t, root, "commit", "-qm", "fixture")
	source, err := NewGitTree(root, "")
	if err != nil {
		t.Fatal(err)
	}
	response := Evaluate(source, DefaultProjectID)
	if response.Outcome != "no_go" || !diagnosticCode(response, "unsupported_file") || diagnosticCode(response, "missing_catalog") {
		t.Fatalf("committed symlink collection root was conflated with an absent root: %#v", response)
	}
}

func TestWorkingTreeReadReappliesLimitToCachedBytes(t *testing.T) {
	root := t.TempDir()
	runGitFixture(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, "payload.bin"), []byte("payload larger than narrow limit"), 0o644); err != nil {
		t.Fatal(err)
	}
	source, err := NewWorkingTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := source.Read("payload.bin", 128); err != nil {
		t.Fatalf("wide first read failed: %v", err)
	}
	data, mode, err := source.Read("payload.bin", 4)
	if !errors.Is(err, ErrLimit) || mode != ModeRegular || len(data) != 0 {
		t.Fatalf("cached working-tree bytes bypassed current narrow limit: bytes=%d mode=%d err=%v", len(data), mode, err)
	}
}

func TestGitTreeReadReappliesLimitToCachedBytes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "payload.bin"), []byte("payload larger than narrow limit"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitFixture(t, root, "init", "-q")
	runGitFixture(t, root, "config", "user.email", "fixture@example.invalid")
	runGitFixture(t, root, "config", "user.name", "Fixture")
	runGitFixture(t, root, "add", "payload.bin")
	runGitFixture(t, root, "commit", "-qm", "fixture")
	source, err := NewGitTree(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := source.Read("payload.bin", 128); err != nil {
		t.Fatalf("wide first read failed: %v", err)
	}
	data, mode, err := source.Read("payload.bin", 4)
	if !errors.Is(err, ErrLimit) || mode != ModeRegular || len(data) != 0 {
		t.Fatalf("cached Git bytes bypassed current narrow limit: bytes=%d mode=%d err=%v", len(data), mode, err)
	}
}

func TestWorkingTreeCheckRegularDoesNotRetainRelatedPayload(t *testing.T) {
	root := t.TempDir()
	runGitFixture(t, root, "init", "-q")
	if err := os.MkdirAll(filepath.Join(root, "artifacts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "artifacts", "evidence.md"), []byte("evidence"), 0o644); err != nil {
		t.Fatal(err)
	}
	source, err := NewWorkingTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.CheckRegular("artifacts/evidence.md", 8); err != nil {
		t.Fatalf("regular evidence was rejected: %v", err)
	}
	if len(source.cache) != 0 {
		t.Fatalf("metadata check retained payload bytes: %d cached paths", len(source.cache))
	}
	if err := source.CheckRegular("artifacts/evidence.md", 7); !errors.Is(err, ErrLimit) {
		t.Fatalf("current metadata limit was not applied: %v", err)
	}
}

func TestGitTreeCheckRegularInspectsModeAndSizeWithoutCachingBlob(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "payload.bin"), []byte("regular evidence"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("payload.bin", filepath.Join(root, "linked.bin")); err != nil {
		t.Fatal(err)
	}
	runGitFixture(t, root, "init", "-q")
	runGitFixture(t, root, "config", "user.email", "fixture@example.invalid")
	runGitFixture(t, root, "config", "user.name", "Fixture")
	runGitFixture(t, root, "add", ".")
	runGitFixture(t, root, "commit", "-qm", "fixture")
	source, err := NewGitTree(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := source.CheckRegular("payload.bin", 16); err != nil {
		t.Fatalf("regular committed evidence was rejected: %v", err)
	}
	if err := source.CheckRegular("payload.bin", 4); !errors.Is(err, ErrLimit) {
		t.Fatalf("committed metadata limit was not applied: %v", err)
	}
	if err := source.CheckRegular("linked.bin", MaxFileBytes); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("committed symlink was accepted as regular evidence: %v", err)
	}
	if len(source.cache) != 0 {
		t.Fatalf("metadata check retained committed blob bytes: %d cached paths", len(source.cache))
	}
}

func TestGitTreeRequiresRepositoryRootAndFullRevision(t *testing.T) {
	root := t.TempDir()
	runGitFixture(t, root, "init", "-q")
	runGitFixture(t, root, "config", "user.email", "fixture@example.invalid")
	runGitFixture(t, root, "config", "user.name", "Fixture")
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitFixture(t, root, "add", "README")
	runGitFixture(t, root, "commit", "-qm", "fixture")
	short, err := exec.Command("git", "-C", root, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewGitTree(root, string(short[:len(short)-1])); err == nil {
		t.Fatal("short revision was accepted")
	}
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewGitTree(nested, ""); err == nil {
		t.Fatal("missing subdirectory was accepted as a repository root")
	}
}

func TestWorkingTreeFinalCheckDetectsInventoryAndReadChanges(t *testing.T) {
	root := t.TempDir()
	runGitFixture(t, root, "init", "-q")
	if err := os.Mkdir(filepath.Join(root, "prototypes"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "prototypes", "catalog.json")
	if err := os.WriteFile(path, []byte(`{"fixture":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	source, err := NewWorkingTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.List("prototypes"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := source.Read("prototypes/catalog.json", 256); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"fixture":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := source.VerifyStable(); err == nil {
		t.Fatal("final stability check accepted changed file bytes/statistics")
	}
}

func TestWorkingTreeFinalCheckDetectsNewInventoryEntry(t *testing.T) {
	root := t.TempDir()
	runGitFixture(t, root, "init", "-q")
	if err := os.Mkdir(filepath.Join(root, "prototypes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "prototypes", "catalog.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	source, err := NewWorkingTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.List("prototypes"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := source.Read("prototypes/catalog.json", 256); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "prototypes", "new-file"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := source.VerifyStable(); err == nil {
		t.Fatal("final stability check accepted a new inventory entry")
	}
}

func TestWorkingTreeStabilityUsesCapturedManagedPrefix(t *testing.T) {
	root := t.TempDir()
	runGitFixture(t, root, "init", "-q")
	if err := os.Mkdir(filepath.Join(root, "design-systems"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "design-systems", "catalog.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	source, err := NewWorkingTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.List("design-systems"); err != nil {
		t.Fatal(err)
	}
	if err := source.VerifyStable(); err != nil {
		t.Fatalf("generic source stability was hardcoded to a Prototype prefix: %v", err)
	}
}

func runGitFixture(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git fixture command %v failed: %v: %s", args, err, output)
	}
}
