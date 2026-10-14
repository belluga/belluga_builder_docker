package knowledge_status

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func write(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func makeTestSymlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("Windows test account cannot create symlinks: %v", err)
		}
		t.Fatalf("required symlink fixture could not be created on %s: %v", runtime.GOOS, err)
	}
}

func foundationFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"project_mandate.md", "domain_entities.md", "project_constitution.md", "system_roadmap.md", "project_landing.md", "policies/one.md", "modules/one.md"} {
		write(t, root, name, "# "+name+"\n")
	}
	return root
}

func TestLandingContentAndPathInventoryChangeDigest(t *testing.T) {
	root := foundationFixture(t)
	before, err := CollectLanding(root)
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "modules/one.md", "# changed\n")
	changed, err := CollectLanding(root)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Digest == before.Digest {
		t.Fatal("content change did not change Landing digest")
	}
	write(t, root, "modules/two.md", "# added\n")
	added, err := CollectLanding(root)
	if err != nil {
		t.Fatal(err)
	}
	if added.Digest == changed.Digest {
		t.Fatal("module addition did not change Landing digest")
	}
	if len(added.Inputs) != 7 {
		t.Fatalf("expected seven sources, got %d", len(added.Inputs))
	}
}

func TestRoadmapTracksPathsButIgnoresBodies(t *testing.T) {
	root := t.TempDir()
	write(t, root, "todos/active/a.md", "first\n")
	first, err := CollectRoadmap(root, []string{"todos/active/a.md"})
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "todos/active/a.md", "second body\n")
	body, err := CollectRoadmap(root, []string{"todos/active/a.md"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != body.Digest {
		t.Fatal("body-only edit changed inventory digest")
	}
	write(t, root, "todos/active/b.md", "new TODO\n")
	write(t, root, "todos/active/.hidden.md", "hidden TODO\n")
	write(t, root, "todos/ephemeral/local.md", "ephemeral\n")
	write(t, root, "todos/generated/hidden.md", "generated\n")
	created, err := CollectRoadmap(root, []string{"todos/active/a.md", "todos/active/b.md", "todos/active/.hidden.md", "todos/ephemeral/local.md", "todos/generated/hidden.md"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Digest == body.Digest {
		t.Fatal("TODO creation did not change inventory digest")
	}
	if len(created.Inputs) != 3 {
		t.Fatalf("expected visible and hidden active TODOs with ephemeral/generated exclusions, got %v", created.Inputs)
	}
	renamed, err := CollectRoadmap(root, []string{"todos/active/b.md"})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Digest == created.Digest {
		t.Fatal("rename/deletion did not change inventory digest")
	}
	write(t, root, "todos/done/b.md", "moved TODO\n")
	moved, err := CollectRoadmap(root, []string{"todos/done/b.md"})
	if err != nil {
		t.Fatal(err)
	}
	if moved.Digest == renamed.Digest {
		t.Fatal("lane move did not change inventory digest")
	}
}

func TestStatusTransitionsAndTargetIsolation(t *testing.T) {
	root := foundationFixture(t)
	landing, err := CollectLanding(root)
	if err != nil {
		t.Fatal(err)
	}
	roadmap, err := CollectRoadmap(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	landingDoc, _ := os.ReadFile(filepath.Join(root, "project_landing.md"))
	roadmapDoc, _ := os.ReadFile(filepath.Join(root, "system_roadmap.md"))
	missing := Evaluate(landing, roadmap, landingDoc, roadmapDoc, Marker{}, false, nil, nil)
	if missing.Landing.Status != "unverifiable" || missing.Landing.ReasonCode != "review_missing" {
		t.Fatalf("missing marker result: %+v", missing.Landing)
	}
	marker := Marker{SchemaVersion: "1", ProjectID: "builder", Landing: &Review{ReviewedSourceDigest: landing.Digest, ReviewedDocumentDigest: DigestBytes(landingDoc), ReviewedPacketDigest: DigestBytes([]byte("packet")), ReviewedRevision: "abc", ReviewedBy: "reviewer", ReviewReference: "message-1"}, Roadmap: nil}
	current := Evaluate(landing, roadmap, landingDoc, roadmapDoc, marker, true, nil, nil)
	if current.Landing.Status != "current" || current.Landing.ReasonCode != "match" {
		t.Fatalf("expected matching Landing, got %+v", current.Landing)
	}
	if current.Roadmap.Status != "unverifiable" {
		t.Fatalf("one target review leaked: %+v", current.Roadmap)
	}
	write(t, root, "modules/one.md", "new source\n")
	changed, _ := CollectLanding(root)
	sourceChanged := Evaluate(changed, roadmap, landingDoc, roadmapDoc, marker, true, nil, nil)
	if sourceChanged.Landing.Status != "review_required" || sourceChanged.Landing.ReasonCode != "source_changed" {
		t.Fatalf("source status: %+v", sourceChanged.Landing)
	}
	newDoc := []byte("changed output")
	docChanged := Evaluate(landing, roadmap, newDoc, roadmapDoc, marker, true, nil, nil)
	if docChanged.Landing.ReasonCode != "document_changed" {
		t.Fatalf("document status: %+v", docChanged.Landing)
	}
	combined := Evaluate(changed, roadmap, newDoc, roadmapDoc, marker, true, nil, nil)
	if combined.Landing.Status != "review_required" || combined.Landing.ReasonCode != "source_and_document_changed" {
		t.Fatalf("combined status: %+v", combined.Landing)
	}
	bad := Evaluate(landing, roadmap, landingDoc, roadmapDoc, Marker{}, true, nil, nil)
	if bad.Landing.ReasonCode != "invalid_marker" {
		t.Fatalf("malformed marker status: %+v", bad.Landing)
	}
}

func TestHashV1AndPacketDigestAreDeterministic(t *testing.T) {
	pathOnly, err := HashRecords([]Input{{Path: "todos/active/a.md"}})
	if err != nil {
		t.Fatal(err)
	}
	if pathOnly != "sha256:577ee218f038d0f5fc09961018cfc26e9b6853ac8c830524ea05534effd1a4bd" {
		t.Fatalf("Roadmap v1 path encoding changed: %s", pathOnly)
	}
	withBody, err := HashRecords([]Input{{Path: "todos/active/a.md", ContentDigest: DigestBytes([]byte("body"))}})
	if err != nil {
		t.Fatal(err)
	}
	if pathOnly == withBody {
		t.Fatal("content unexpectedly affected the path-only digest")
	}
	packet := Packet{Target: "roadmap", SourceRevision: "revision", SourceDigest: pathOnly, OutputPath: "system_roadmap.md", DocumentDigest: DigestBytes([]byte("roadmap")), Inputs: []Input{{Path: "todos/active/a.md"}}}
	first, err := PacketDigest(packet)
	if err != nil {
		t.Fatal(err)
	}
	if first != "sha256:f9f8d8f7997a2754b31be409de2f4949ee8347d28406451e18224f27ce3f52be" {
		t.Fatalf("review packet v1 encoding changed: %s", first)
	}
	packet.PacketDigest = "sha256:ignored"
	second, err := PacketDigest(packet)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("packet digest included its own field")
	}
}

func TestPacketRejectsUnicodeControlRuneInSelectedInputPath(t *testing.T) {
	packet := Packet{
		Target:         "roadmap",
		SourceRevision: "revision",
		SourceDigest:   "sha256:" + strings.Repeat("0", 64),
		OutputPath:     "system_roadmap.md",
		DocumentDigest: DigestBytes([]byte("roadmap")),
		Inputs:         []Input{{Path: "todos/active/bad\u0085name.markdown"}},
	}
	if _, err := PacketDigest(packet); err == nil {
		t.Fatal("review packet accepted a Unicode control rune in an input path")
	}
}

func TestInvalidPathsSymlinksAndLimitsFailClosed(t *testing.T) {
	for _, path := range []string{"/absolute", "a/../b", "a//b", "bad\\name", "bad\nname", "bad\u0085name", "bad\u009fname", string([]byte{0xff})} {
		if err := ValidatePath(path); err == nil {
			t.Errorf("accepted invalid path %q", path)
		}
	}
	root := foundationFixture(t)
	makeTestSymlink(t, "modules/one.md", filepath.Join(root, "policies", "linked.md"))
	if _, err := CollectLanding(root); err == nil {
		t.Fatal("accepted symlink in source tree")
	}
	root = foundationFixture(t)
	write(t, root, "modules/large.md", strings.Repeat("x", MaxDocumentBytes+1))
	if _, err := CollectLanding(root); err == nil {
		t.Fatal("accepted oversized document")
	}
	tooMany := make([]Input, MaxInputs+1)
	for i := range tooMany {
		tooMany[i] = Input{Path: "a" + strings.Repeat("x", i) + ".md"}
	}
	if _, err := HashRecords(tooMany); err == nil {
		t.Fatal("input limit was not enforced")
	}
	root = foundationFixture(t)
	large := strings.Repeat("x", MaxDocumentBytes)
	for index := 0; index < 33; index++ {
		write(t, root, fmt.Sprintf("modules/large-%02d.md", index), large)
	}
	if _, err := CollectLanding(root); err == nil {
		t.Fatal("aggregate input limit was not enforced")
	}
}

func TestReadBoundedRejectsMissingAndNonRegularOutputs(t *testing.T) {
	root := t.TempDir()
	if _, err := ReadBounded(filepath.Join(root, "missing.md"), 10); err == nil {
		t.Fatal("missing output was accepted")
	}
	directory := filepath.Join(root, "directory")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBounded(directory, 10); err == nil {
		t.Fatal("non-regular output was accepted")
	}
}

func TestSerializedMarkerMustBeStrictAndUnambiguous(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "knowledge_review.manifest.json")
	valid := `{"schema_version":"1","project_id":"builder","landing":null,"roadmap":null}`
	for name, data := range map[string]string{
		"duplicate-root-key":   `{"schema_version":"1","schema_version":"1","project_id":"builder","landing":null,"roadmap":null}`,
		"duplicate-nested-key": `{"schema_version":"1","project_id":"builder","landing":{"reviewed_by":"a","reviewed_by":"b"},"roadmap":null}`,
		"case-alias-root":      `{"schema_version":"1","project_id":"builder","landing":null,"LANDING":null,"roadmap":null}`,
		"case-alias-nested":    `{"schema_version":"1","project_id":"builder","landing":{"reviewed_source_digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000","reviewed_document_digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000","reviewed_packet_digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000","reviewed_revision":"r","reviewed_by":"a","REVIEWED_BY":"b","review_reference":"ref"},"roadmap":null}`,
		"unknown-field":        `{"schema_version":"1","project_id":"builder","landing":null,"roadmap":null,"extra":true}`,
		"trailing-value":       valid + ` {}`,
		"malformed":            `{"schema_version":`,
		"invalid-utf8":         string([]byte{'{', 0xff, '}'}),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, exists, err := LoadMarker(path); err == nil || !exists {
				t.Fatalf("malformed serialized marker accepted: exists=%v err=%v", exists, err)
			}
		})
	}
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	marker, exists, err := LoadMarker(path)
	if err != nil || !exists || marker.SchemaVersion != SchemaVersion || marker.ProjectID != ProjectID {
		t.Fatalf("valid marker rejected: marker=%+v exists=%v err=%v", marker, exists, err)
	}
}

func TestLandingInventoryIncludesHiddenMarkdownFiles(t *testing.T) {
	root := foundationFixture(t)
	write(t, root, "modules/.hidden.md", "hidden module\n")
	write(t, root, "modules/UPPER.MD", "uppercase extension\n")
	snapshot, err := CollectLanding(root)
	if err != nil {
		t.Fatal(err)
	}
	foundHidden := false
	foundUppercase := false
	for _, input := range snapshot.Inputs {
		if input.Path == "modules/.hidden.md" {
			foundHidden = true
		}
		if input.Path == "modules/UPPER.MD" {
			foundUppercase = true
		}
	}
	if !foundHidden {
		t.Fatal("hidden Markdown file was omitted from Landing inputs")
	}
	if !foundUppercase {
		t.Fatal("uppercase .MD file was omitted from Landing inputs")
	}
}

func TestMarkdownExtensionPolicyIsCaseInsensitive(t *testing.T) {
	for _, path := range []string{"one.md", "two.MD", "three.markdown", "four.MARKDOWN"} {
		if !IsMarkdownPath(path) {
			t.Errorf("Markdown path was not recognized: %s", path)
		}
	}
	for _, path := range []string{"one.txt", "two.markdown.backup"} {
		if IsMarkdownPath(path) {
			t.Errorf("non-Markdown path was recognized: %s", path)
		}
	}
}

func TestSymlinkedBoundariesAndAncestorsFailClosed(t *testing.T) {
	outside := foundationFixture(t)
	for _, target := range []string{"root", "policies", "modules", "todo-ancestor", "output-ancestor"} {
		t.Run(target, func(t *testing.T) {
			base := t.TempDir()
			link := filepath.Join(base, "linked")
			makeTestSymlink(t, outside, link)
			switch target {
			case "root":
				if _, err := CollectLanding(link); err == nil {
					t.Fatal("symlinked root accepted")
				}
			case "policies", "modules":
				root := foundationFixture(t)
				if err := os.RemoveAll(filepath.Join(root, target)); err != nil {
					t.Fatal(err)
				}
				makeTestSymlink(t, filepath.Join(outside, target), filepath.Join(root, target))
				if _, err := CollectLanding(root); err == nil {
					t.Fatalf("symlinked %s root accepted", target)
				}
			case "todo-ancestor":
				makeTestSymlink(t, filepath.Join(outside, "todos"), filepath.Join(base, "todos"))
				if _, err := CollectRoadmap(base, []string{"todos/active/task.md"}); err == nil {
					// The not-yet-existing TODO below a symlinked ancestor still must not be accepted.
					t.Fatal("symlinked TODO ancestor accepted")
				}
			case "output-ancestor":
				if _, err := ReadBounded(filepath.Join(link, "project_landing.md"), MaxDocumentBytes); err == nil {
					t.Fatal("symlinked output ancestor accepted")
				}
			}
		})
	}
}
