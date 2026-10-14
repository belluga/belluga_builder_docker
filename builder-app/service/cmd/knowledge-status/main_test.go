package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	ks "belluga-builder-viewer/internal/knowledge_status"
)

func writeFixture(t *testing.T, root, name, content string) {
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

func newGitFixture(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	foundation := filepath.Join(base, "foundation")
	project := filepath.Join(base, "project")
	if err := os.MkdirAll(foundation, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"project_mandate.md", "domain_entities.md", "project_constitution.md", "system_roadmap.md", "project_landing.md", "policies/policy.md", "modules/viewer.md", "todos/active/todo.md"} {
		writeFixture(t, foundation, name, "# "+name+"\n")
	}
	gitTest(t, foundation, "init", "-q")
	gitTest(t, foundation, "config", "user.email", "test@example.invalid")
	gitTest(t, foundation, "config", "user.name", "Test Reviewer")
	gitTest(t, foundation, "add", "--all")
	gitTest(t, foundation, "commit", "-qm", "fixture")
	return project, foundation
}

func gitTest(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func captureStdout(t *testing.T, callback func() error) string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = file
	err = callback()
	os.Stdout = previous
	if closeErr := file.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func packetFor(t *testing.T, target, project, foundation string) ks.Packet {
	t.Helper()
	packetText := captureStdout(t, func() error {
		return runPacket([]string{"--target", target, "--project-root", project, "--foundation-root", foundation})
	})
	var packet ks.Packet
	if err := json.Unmarshal([]byte(packetText), &packet); err != nil {
		t.Fatal(err)
	}
	return packet
}

func TestStatusMaterializesFreshRepeatableSnapshots(t *testing.T) {
	project, foundation := newGitFixture(t)
	args := []string{"--project-root", project, "--foundation-root", foundation}
	first := captureStdout(t, func() error { return runStatus(args) })
	second := captureStdout(t, func() error { return runStatus(args) })
	if first != second {
		t.Fatal("unchanged status output was not byte-stable")
	}
	var before ks.Response
	if err := json.Unmarshal([]byte(first), &before); err != nil {
		t.Fatal(err)
	}
	if before.Landing.Status != "unverifiable" || before.Landing.ReasonCode != "review_missing" {
		t.Fatalf("initial Landing status: %+v", before.Landing)
	}
	if before.Roadmap.Status != "unverifiable" || before.Roadmap.ReasonCode != "review_missing" {
		t.Fatalf("initial Roadmap status: %+v", before.Roadmap)
	}
	if _, err := os.Stat(filepath.Join(foundation, markerName)); !os.IsNotExist(err) {
		t.Fatal("status invocation created review evidence")
	}
	snapshotPath := filepath.Join(project, "local-api/v1/projects/builder/knowledge-status.json")
	oldSnapshot, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, foundation, "project_mandate.md", "# changed after snapshot\n")
	third := captureStdout(t, func() error { return runStatus(args) })
	newSnapshot, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(oldSnapshot) == string(newSnapshot) || third == first {
		t.Fatal("fresh invocation retained a stale materialized status")
	}
	var after ks.Response
	if err := json.Unmarshal([]byte(third), &after); err != nil {
		t.Fatal(err)
	}
	if after.Landing.ObservedSourceDigest == nil || before.Landing.ObservedSourceDigest == nil || *after.Landing.ObservedSourceDigest == *before.Landing.ObservedSourceDigest {
		t.Fatal("source mutation did not change observed Landing digest")
	}
}

func TestStatusCanReturnFreshReadWithoutWritingSnapshot(t *testing.T) {
	project, foundation := newGitFixture(t)
	args := []string{"--project-root", project, "--foundation-root", foundation, "--stdout-only"}
	out := captureStdout(t, func() error { return runStatus(args) })
	var response ks.Response
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatal(err)
	}
	if response.Landing.Status != "unverifiable" || response.Roadmap.Status != "unverifiable" {
		t.Fatalf("unexpected status: %+v", response)
	}
	if _, err := os.Stat(filepath.Join(project, "local-api/v1/projects/builder/knowledge-status.json")); !os.IsNotExist(err) {
		t.Fatal("stdout-only status wrote a snapshot")
	}
}

func assertStatusJSONContract(t *testing.T, data []byte, reviewedLanding, reviewedRoadmap bool, expected map[string][2]string) {
	t.Helper()
	var response map[string]json.RawMessage
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"schema_version": `"1"`, "project_id": `"builder"`, "authority_scope": `"local_review_only"`} {
		value, exists := response[key]
		if !exists || string(value) != want {
			t.Fatalf("response %s = %s (present=%v), want %s", key, value, exists, want)
		}
	}
	for target, reviewed := range map[string]bool{"landing": reviewedLanding, "roadmap": reviewedRoadmap} {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(response[target], &fields); err != nil {
			t.Fatalf("decode %s fields: %v", target, err)
		}
		var status struct {
			Status     string `json:"status"`
			ReasonCode string `json:"reason_code"`
		}
		if err := json.Unmarshal(response[target], &status); err != nil {
			t.Fatalf("decode %s status: %v", target, err)
		}
		if want, ok := expected[target]; !ok || status.Status != want[0] || status.ReasonCode != want[1] {
			t.Fatalf("%s status/reason = %q/%q, want %q/%q", target, status.Status, status.ReasonCode, want[0], want[1])
		}
		for _, key := range []string{"observed_source_digest", "reviewed_source_digest", "observed_document_digest", "reviewed_document_digest"} {
			value, exists := fields[key]
			if !exists {
				t.Fatalf("%s omitted required JSON field %q", target, key)
			}
			null := string(value) == "null"
			wantNull := strings.HasPrefix(key, "reviewed_") && !reviewed
			if null != wantNull {
				t.Fatalf("%s.%s = %s, want null=%v", target, key, value, wantNull)
			}
			if !null {
				var digest string
				if err := json.Unmarshal(value, &digest); err != nil || !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(digest) {
					t.Fatalf("%s.%s is not a lowercase SHA-256 digest: %s (decode error: %v)", target, key, value, err)
				}
			}
		}
	}
}

func TestStatusJSONV1IdentityAuthorityAndDigestNullSemantics(t *testing.T) {
	for _, state := range []string{"missing-review", "current", "source-changed", "output-changed", "invalid-marker"} {
		t.Run(state, func(t *testing.T) {
			project, foundation := newGitFixture(t)
			reviewed := false
			if state != "missing-review" {
				for _, target := range []string{"landing", "roadmap"} {
					packet := packetFor(t, target, project, foundation)
					if err := runRecord([]string{"--target", target, "--expected-packet-digest", packet.PacketDigest, "--reviewed-by", "reviewer", "--review-reference", "approval", "--foundation-root", foundation}); err != nil {
						t.Fatal(err)
					}
				}
				reviewed = true
			}
			switch state {
			case "source-changed":
				writeFixture(t, foundation, "modules/viewer.md", "changed source\n")
			case "output-changed":
				writeFixture(t, foundation, "system_roadmap.md", "changed Roadmap output\n")
			case "invalid-marker":
				if err := os.WriteFile(filepath.Join(foundation, markerName), []byte(`{"schema_version":"2","project_id":"builder","landing":null,"roadmap":null}`), 0o600); err != nil {
					t.Fatal(err)
				}
				reviewed = false
			}
			data := captureStdout(t, func() error {
				return runStatus([]string{"--project-root", project, "--foundation-root", foundation})
			})
			expected := map[string][2]string{
				"landing": {"unverifiable", "review_missing"},
				"roadmap": {"unverifiable", "review_missing"},
			}
			switch state {
			case "current":
				expected["landing"], expected["roadmap"] = [2]string{"current", "match"}, [2]string{"current", "match"}
			case "source-changed":
				expected["landing"], expected["roadmap"] = [2]string{"review_required", "source_changed"}, [2]string{"current", "match"}
			case "output-changed":
				expected["landing"], expected["roadmap"] = [2]string{"review_required", "source_changed"}, [2]string{"review_required", "document_changed"}
			case "invalid-marker":
				expected["landing"], expected["roadmap"] = [2]string{"unverifiable", "invalid_marker"}, [2]string{"unverifiable", "invalid_marker"}
			}
			assertStatusJSONContract(t, []byte(data), reviewed, reviewed, expected)
		})
	}
}

func TestRoadmapTODOBodyOnlyEditsDoNotTriggerRealGitStatus(t *testing.T) {
	project, foundation := newGitFixture(t)
	baseline := packetFor(t, "roadmap", project, foundation)
	if err := runRecord([]string{"--target", "roadmap", "--expected-packet-digest", baseline.PacketDigest, "--reviewed-by", "reviewer", "--review-reference", "approval", "--foundation-root", foundation}); err != nil {
		t.Fatal(err)
	}
	assertCurrent := func(label string) {
		t.Helper()
		data := captureStdout(t, func() error {
			return runStatus([]string{"--project-root", project, "--foundation-root", foundation})
		})
		var response ks.Response
		if err := json.Unmarshal([]byte(data), &response); err != nil {
			t.Fatal(err)
		}
		if response.Roadmap.Status != "current" || response.Roadmap.ReasonCode != "match" || response.Roadmap.ObservedSourceDigest == nil || *response.Roadmap.ObservedSourceDigest != baseline.SourceDigest {
			t.Fatalf("%s body-only edit changed Roadmap status or digest: %+v (baseline=%s)", label, response.Roadmap, baseline.SourceDigest)
		}
	}

	writeFixture(t, foundation, "todos/active/todo.md", "# edited body while unstaged\n")
	assertCurrent("unstaged")
	unstagedPacket := packetFor(t, "roadmap", project, foundation)
	if unstagedPacket.SourceDigest != baseline.SourceDigest || unstagedPacket.PacketDigest != baseline.PacketDigest {
		t.Fatalf("unstaged body-only edit changed committed packet: before=%+v after=%+v", baseline, unstagedPacket)
	}

	gitTest(t, foundation, "add", "todos/active/todo.md")
	gitTest(t, foundation, "commit", "-qm", "edit TODO body")
	assertCurrent("committed")
	committedPacket := packetFor(t, "roadmap", project, foundation)
	if committedPacket.SourceDigest != baseline.SourceDigest {
		t.Fatalf("committed body-only edit changed Roadmap source digest: before=%s after=%s", baseline.SourceDigest, committedPacket.SourceDigest)
	}
	if committedPacket.PacketDigest == baseline.PacketDigest {
		t.Fatalf("committed packet digest did not bind the new revision: before=%s after=%s", baseline.PacketDigest, committedPacket.PacketDigest)
	}
}

func TestStatusOutputCannotOverwriteArbitraryFoundationFiles(t *testing.T) {
	project, foundation := newGitFixture(t)
	markerPath := filepath.Join(foundation, markerName)
	if err := os.WriteFile(markerPath, []byte("existing marker bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, destination := range []string{
		filepath.Join(foundation, "project_landing.md"),
		markerPath,
	} {
		original, err := os.ReadFile(destination)
		if err != nil {
			t.Fatal(err)
		}
		err = runStatus([]string{"--project-root", project, "--foundation-root", foundation, "--out", destination})
		if err == nil {
			t.Fatalf("arbitrary status output path was accepted: %s", destination)
		}
		after, readErr := os.ReadFile(destination)
		if readErr != nil || string(after) != string(original) {
			t.Fatalf("rejected output path changed %s: read=%v", destination, readErr)
		}
	}
}

func TestStatusSerializesCaseAliasMarkersAsInvalidMarker(t *testing.T) {
	project, foundation := newGitFixture(t)
	for name, marker := range map[string]string{
		"root-alias":   `{"schema_version":"1","project_id":"builder","landing":null,"LANDING":null,"roadmap":null}`,
		"nested-alias": `{"schema_version":"1","project_id":"builder","landing":{"reviewed_source_digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000","reviewed_document_digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000","reviewed_packet_digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000","reviewed_revision":"r","reviewed_by":"a","REVIEWED_BY":"b","review_reference":"ref"},"roadmap":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(foundation, markerName), []byte(marker), 0o600); err != nil {
				t.Fatal(err)
			}
			data := captureStdout(t, func() error {
				return runStatus([]string{"--project-root", project, "--foundation-root", foundation})
			})
			var response ks.Response
			if err := json.Unmarshal([]byte(data), &response); err != nil {
				t.Fatal(err)
			}
			if response.Landing.Status != "unverifiable" || response.Landing.ReasonCode != "invalid_marker" || response.Roadmap.ReasonCode != "invalid_marker" {
				t.Fatalf("case-alias marker did not fail closed: landing=%+v roadmap=%+v", response.Landing, response.Roadmap)
			}
		})
	}
}

func TestStatusAndRecordReviewRejectIncompleteOrInvalidOppositeTargetMarker(t *testing.T) {
	for _, scenario := range []string{"missing-roadmap-key", "invalid-roadmap-review"} {
		t.Run(scenario, func(t *testing.T) {
			project, foundation := newGitFixture(t)
			packet := packetFor(t, "landing", project, foundation)
			landingReview := ks.Review{
				ReviewedSourceDigest: packet.SourceDigest, ReviewedDocumentDigest: packet.DocumentDigest,
				ReviewedPacketDigest: packet.PacketDigest, ReviewedRevision: packet.SourceRevision,
				ReviewedBy: "fixture-reviewer", ReviewReference: "fixture-approval",
			}
			var markerBytes []byte
			var err error
			if scenario == "missing-roadmap-key" {
				reviewJSON, marshalErr := json.Marshal(landingReview)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				markerBytes = []byte(fmt.Sprintf(`{"schema_version":"1","project_id":"builder","landing":%s}`, reviewJSON))
			} else {
				markerBytes, err = json.Marshal(ks.Marker{
					SchemaVersion: "1", ProjectID: "builder", Landing: &landingReview,
					Roadmap: &ks.Review{ReviewedSourceDigest: "invalid", ReviewedDocumentDigest: "invalid", ReviewedPacketDigest: "invalid", ReviewedRevision: "r", ReviewedBy: "reviewer", ReviewReference: "ref"},
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			markerPath := filepath.Join(foundation, markerName)
			if err := os.WriteFile(markerPath, markerBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			statusText := captureStdout(t, func() error {
				return runStatus([]string{"--project-root", project, "--foundation-root", foundation})
			})
			var response ks.Response
			if err := json.Unmarshal([]byte(statusText), &response); err != nil {
				t.Fatal(err)
			}
			if response.Landing.Status != "unverifiable" || response.Landing.ReasonCode != "invalid_marker" || response.Roadmap.Status != "unverifiable" || response.Roadmap.ReasonCode != "invalid_marker" {
				t.Fatalf("invalid complete marker schema did not fail closed globally: landing=%+v roadmap=%+v", response.Landing, response.Roadmap)
			}
			before, err := os.ReadFile(markerPath)
			if err != nil {
				t.Fatal(err)
			}
			err = runRecord([]string{"--target", "landing", "--expected-packet-digest", packet.PacketDigest, "--reviewed-by", "reviewer", "--review-reference", "approval", "--foundation-root", foundation})
			if err == nil {
				t.Fatal("record-review accepted incomplete or semantically invalid complete marker")
			}
			after, readErr := os.ReadFile(markerPath)
			if readErr != nil || string(after) != string(before) {
				t.Fatalf("rejected marker was modified: read=%v", readErr)
			}
		})
	}
}

func TestLandingSourceRemovalRequiresReviewWithoutAffectingRoadmap(t *testing.T) {
	project, foundation := newGitFixture(t)
	landing := packetFor(t, "landing", project, foundation)
	roadmap := packetFor(t, "roadmap", project, foundation)
	for target, packet := range map[string]ks.Packet{"landing": landing, "roadmap": roadmap} {
		if err := runRecord([]string{"--target", target, "--expected-packet-digest", packet.PacketDigest, "--reviewed-by", "reviewer-" + target, "--review-reference", "approval:" + target, "--foundation-root", foundation}); err != nil {
			t.Fatalf("could not seed valid %s review: %v", target, err)
		}
	}
	gitTest(t, foundation, "rm", "modules/viewer.md")
	gitTest(t, foundation, "commit", "-qm", "remove Landing source")
	statusText := captureStdout(t, func() error {
		return runStatus([]string{"--project-root", project, "--foundation-root", foundation})
	})
	var response ks.Response
	if err := json.Unmarshal([]byte(statusText), &response); err != nil {
		t.Fatal(err)
	}
	if response.Landing.Status != "review_required" || response.Landing.ReasonCode != "source_changed" {
		t.Fatalf("Landing source removal did not require review: %+v", response.Landing)
	}
	if response.Roadmap.Status != "current" || response.Roadmap.ReasonCode != "match" {
		t.Fatalf("Landing source removal affected Roadmap status: %+v", response.Roadmap)
	}
}

func TestCommittedAndWorkingLandingSelectorsAgreeForHiddenAndUppercaseMarkdown(t *testing.T) {
	project, foundation := newGitFixture(t)
	writeFixture(t, foundation, "modules/.hidden.md", "hidden\n")
	writeFixture(t, foundation, "modules/UPPER.MD", "uppercase\n")
	writeFixture(t, foundation, "modules/summary.markdown", "Markdown extension\n")
	writeFixture(t, foundation, "policies/UPPER.MARKDOWN", "uppercase Markdown extension\n")
	gitTest(t, foundation, "add", "modules/.hidden.md", "modules/UPPER.MD", "modules/summary.markdown", "policies/UPPER.MARKDOWN")
	gitTest(t, foundation, "commit", "-qm", "selector edge cases")
	committed := packetFor(t, "landing", project, foundation)
	working, err := workingPacket(foundation, "landing", committed.SourceRevision)
	if err != nil {
		t.Fatal(err)
	}
	if working.SourceDigest != committed.SourceDigest || working.PacketDigest != committed.PacketDigest {
		t.Fatalf("clean committed and working packets differ: committed=%+v working=%+v", committed, working)
	}
	foundHidden, foundUppercase, foundMarkdown, foundUppercaseMarkdown := false, false, false, false
	for _, input := range committed.Inputs {
		if input.Path == "modules/.hidden.md" {
			foundHidden = true
		}
		if input.Path == "modules/UPPER.MD" {
			foundUppercase = true
		}
		if input.Path == "modules/summary.markdown" {
			foundMarkdown = true
		}
		if input.Path == "policies/UPPER.MARKDOWN" {
			foundUppercaseMarkdown = true
		}
	}
	if !foundHidden {
		t.Fatal("committed selector omitted hidden Markdown module")
	}
	if !foundUppercase {
		t.Fatal("committed selector omitted uppercase .MD module")
	}
	if !foundMarkdown || !foundUppercaseMarkdown {
		t.Fatalf("committed selector omitted conventional Markdown extensions: %v", committed.Inputs)
	}
	if err := runRecord([]string{"--target", "landing", "--expected-packet-digest", committed.PacketDigest, "--reviewed-by", "reviewer", "--review-reference", "approval", "--foundation-root", foundation}); err != nil {
		t.Fatalf("review for matching committed/working selector packet failed: %v", err)
	}
}

func TestRoadmapUppercaseMDInStatusAndReviewFixtures(t *testing.T) {
	for _, state := range []string{"tracked", "untracked"} {
		t.Run(state, func(t *testing.T) {
			project, foundation := newGitFixture(t)
			baseline := packetFor(t, "roadmap", project, foundation)
			if err := runRecord([]string{"--target", "roadmap", "--expected-packet-digest", baseline.PacketDigest, "--reviewed-by", "baseline", "--review-reference", "approval:baseline", "--foundation-root", foundation}); err != nil {
				t.Fatal(err)
			}
			writeFixture(t, foundation, "todos/active/UPPER.MD", "uppercase TODO extension\n")
			if state == "tracked" {
				gitTest(t, foundation, "add", "todos/active/UPPER.MD")
				gitTest(t, foundation, "commit", "-qm", "uppercase TODO")
			}
			statusText := captureStdout(t, func() error {
				return runStatus([]string{"--project-root", project, "--foundation-root", foundation})
			})
			var response ks.Response
			if err := json.Unmarshal([]byte(statusText), &response); err != nil {
				t.Fatal(err)
			}
			if response.Roadmap.Status != "review_required" || response.Roadmap.ReasonCode != "source_changed" {
				t.Fatalf("%s uppercase TODO was not detected by status: %+v", state, response.Roadmap)
			}
			packet := packetFor(t, "roadmap", project, foundation)
			err := runRecord([]string{"--target", "roadmap", "--expected-packet-digest", packet.PacketDigest, "--reviewed-by", "reviewer", "--review-reference", "approval:uppercase", "--foundation-root", foundation})
			if state == "tracked" {
				if err != nil {
					t.Fatalf("review of tracked uppercase TODO failed: %v", err)
				}
				marker, exists, loadErr := ks.LoadMarker(filepath.Join(foundation, markerName))
				if loadErr != nil || !exists || marker.Roadmap == nil || marker.Roadmap.ReviewedSourceDigest != packet.SourceDigest {
					t.Fatalf("tracked uppercase TODO review was not recorded: marker=%+v exists=%v err=%v", marker, exists, loadErr)
				}
			} else if err == nil {
				t.Fatal("review unexpectedly accepted a new untracked uppercase TODO")
			}
		})
	}
}

func TestRoadmapMarkdownExtensionsTriggerStatusAndPacketReview(t *testing.T) {
	for _, fixture := range []struct {
		name    string
		path    string
		tracked bool
	}{
		{name: "tracked-markdown", path: "todos/active/brief.markdown", tracked: true},
		{name: "untracked-uppercase-markdown", path: "todos/active/BRIEF.MARKDOWN", tracked: false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			project, foundation := newGitFixture(t)
			baseline := packetFor(t, "roadmap", project, foundation)
			if err := runRecord([]string{"--target", "roadmap", "--expected-packet-digest", baseline.PacketDigest, "--reviewed-by", "baseline", "--review-reference", "approval:baseline", "--foundation-root", foundation}); err != nil {
				t.Fatal(err)
			}
			writeFixture(t, foundation, fixture.path, "additional Markdown TODO\n")
			if fixture.tracked {
				gitTest(t, foundation, "add", fixture.path)
				gitTest(t, foundation, "commit", "-qm", "add Markdown TODO")
			}
			statusText := captureStdout(t, func() error {
				return runStatus([]string{"--project-root", project, "--foundation-root", foundation})
			})
			var response ks.Response
			if err := json.Unmarshal([]byte(statusText), &response); err != nil {
				t.Fatal(err)
			}
			if response.Roadmap.Status != "review_required" || response.Roadmap.ReasonCode != "source_changed" {
				t.Fatalf("%s addition did not trigger Roadmap review: %+v", fixture.name, response.Roadmap)
			}
			packet := packetFor(t, "roadmap", project, foundation)
			found := false
			for _, input := range packet.Inputs {
				if input.Path == fixture.path {
					found = true
				}
			}
			err := runRecord([]string{"--target", "roadmap", "--expected-packet-digest", packet.PacketDigest, "--reviewed-by", "reviewer", "--review-reference", "approval:markdown", "--foundation-root", foundation})
			if fixture.tracked {
				if !found {
					t.Fatalf("committed packet omitted %s: %v", fixture.path, packet.Inputs)
				}
				working, workingErr := workingPacket(foundation, "roadmap", packet.SourceRevision)
				if workingErr != nil || working.PacketDigest != packet.PacketDigest {
					t.Fatalf("committed and working Roadmap packets differ: committed=%+v working=%+v err=%v", packet, working, workingErr)
				}
				if err != nil {
					t.Fatalf("exact review packet was rejected: %v", err)
				}
			} else {
				if found {
					t.Fatalf("committed packet included untracked %s", fixture.path)
				}
				if err == nil {
					t.Fatal("review accepted untracked Markdown input")
				}
			}
		})
	}
}

func TestStatusRejectsSymlinkedTodosRootWithEmptyReviewedInventory(t *testing.T) {
	project, foundation := newGitFixture(t)
	gitTest(t, foundation, "rm", "-r", "todos")
	gitTest(t, foundation, "commit", "-qm", "empty roadmap inventory")
	packet := packetFor(t, "roadmap", project, foundation)
	if len(packet.Inputs) != 0 {
		t.Fatalf("expected empty committed Roadmap inventory, got %v", packet.Inputs)
	}
	if err := runRecord([]string{"--target", "roadmap", "--expected-packet-digest", packet.PacketDigest, "--reviewed-by", "reviewer", "--review-reference", "approval:empty", "--foundation-root", foundation}); err != nil {
		t.Fatalf("failed to record empty-inventory review: %v", err)
	}
	outside := t.TempDir()
	writeFixture(t, outside, "active/task.markdown", "outside TODO\n")
	makeTestSymlink(t, outside, filepath.Join(foundation, "todos"))
	statusText := captureStdout(t, func() error {
		return runStatus([]string{"--project-root", project, "--foundation-root", foundation})
	})
	var response ks.Response
	if err := json.Unmarshal([]byte(statusText), &response); err != nil {
		t.Fatal(err)
	}
	if response.Roadmap.Status != "unverifiable" || response.Roadmap.ReasonCode != "invalid_input" {
		t.Fatalf("symlinked empty-inventory TODO root did not fail closed: %+v", response.Roadmap)
	}
}

func TestHiddenRoadmapInputsTriggerStatusAndCanBeReviewedAfterCommit(t *testing.T) {
	project, foundation := newGitFixture(t)
	baseline := packetFor(t, "roadmap", project, foundation)
	if err := runRecord([]string{"--target", "roadmap", "--expected-packet-digest", baseline.PacketDigest, "--reviewed-by", "baseline", "--review-reference", "approval:baseline", "--foundation-root", foundation}); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, foundation, "todos/active/.hidden.md", "hidden TODO\n")
	writeFixture(t, foundation, "todos/.private/ancestor.md", "hidden directory TODO\n")
	statusText := captureStdout(t, func() error {
		return runStatus([]string{"--project-root", project, "--foundation-root", foundation})
	})
	var response ks.Response
	if err := json.Unmarshal([]byte(statusText), &response); err != nil {
		t.Fatal(err)
	}
	if response.Roadmap.Status != "review_required" || response.Roadmap.ReasonCode != "source_changed" {
		t.Fatalf("new hidden TODO inputs were not detected: %+v", response.Roadmap)
	}
	untrackedPacket := packetFor(t, "roadmap", project, foundation)
	for _, input := range untrackedPacket.Inputs {
		if input.Path == "todos/active/.hidden.md" || input.Path == "todos/.private/ancestor.md" {
			t.Fatal("committed packet included untracked hidden TODO")
		}
	}
	if err := runRecord([]string{"--target", "roadmap", "--expected-packet-digest", untrackedPacket.PacketDigest, "--reviewed-by", "reviewer", "--review-reference", "approval:hidden", "--foundation-root", foundation}); err == nil {
		t.Fatal("record-review accepted working tree with uncommitted hidden TODOs")
	}
	gitTest(t, foundation, "add", "todos/active/.hidden.md", "todos/.private/ancestor.md")
	gitTest(t, foundation, "commit", "-qm", "add hidden TODO inputs")
	committed := packetFor(t, "roadmap", project, foundation)
	foundHiddenFile, foundHiddenAncestor := false, false
	for _, input := range committed.Inputs {
		if input.Path == "todos/active/.hidden.md" {
			foundHiddenFile = true
		}
		if input.Path == "todos/.private/ancestor.md" {
			foundHiddenAncestor = true
		}
	}
	if !foundHiddenFile || !foundHiddenAncestor {
		t.Fatalf("committed packet omitted hidden TODO path(s): %+v", committed.Inputs)
	}
	if err := runRecord([]string{"--target", "roadmap", "--expected-packet-digest", committed.PacketDigest, "--reviewed-by", "reviewer", "--review-reference", "approval:hidden", "--foundation-root", foundation}); err != nil {
		t.Fatalf("exact review of committed hidden TODO packet failed: %v", err)
	}
	statusText = captureStdout(t, func() error {
		return runStatus([]string{"--project-root", project, "--foundation-root", foundation})
	})
	if err := json.Unmarshal([]byte(statusText), &response); err != nil {
		t.Fatal(err)
	}
	if response.Roadmap.Status != "current" || response.Roadmap.ReasonCode != "match" {
		t.Fatalf("review of exact committed hidden TODO packet did not restore current status: %+v", response.Roadmap)
	}
}

func TestStatusJSONFailsClosedForSymlinkedFoundationRootsAndAncestors(t *testing.T) {
	for _, boundary := range []string{"root", "modules", "todos"} {
		t.Run(boundary, func(t *testing.T) {
			project, foundation := newGitFixture(t)
			selectedFoundation := foundation
			if boundary == "root" {
				selectedFoundation = filepath.Join(filepath.Dir(foundation), "foundation-link")
				makeTestSymlink(t, foundation, selectedFoundation)
			} else {
				name := boundary
				target := filepath.Join(foundation, name)
				outside := t.TempDir()
				if err := os.RemoveAll(target); err != nil {
					t.Fatal(err)
				}
				if name == "modules" {
					writeFixture(t, outside, "viewer.md", "outside\n")
				}
				if name == "todos" {
					writeFixture(t, outside, "active/todo.md", "outside\n")
				}
				makeTestSymlink(t, outside, target)
			}
			data := captureStdout(t, func() error {
				return runStatus([]string{"--project-root", project, "--foundation-root", selectedFoundation})
			})
			var response ks.Response
			if err := json.Unmarshal([]byte(data), &response); err != nil {
				t.Fatal(err)
			}
			if boundary == "root" || boundary == "modules" {
				if response.Landing.Status != "unverifiable" || response.Landing.ReasonCode != "invalid_input" {
					t.Fatalf("symlinked %s did not invalidate Landing: %+v", boundary, response.Landing)
				}
			}
			if boundary == "root" || boundary == "todos" {
				if response.Roadmap.Status != "unverifiable" || response.Roadmap.ReasonCode != "invalid_input" {
					t.Fatalf("symlinked %s did not invalidate Roadmap: %+v", boundary, response.Roadmap)
				}
			}
		})
	}
}

func TestRecordReviewRequiresExactPacketAndPreservesOtherTarget(t *testing.T) {
	project, foundation := newGitFixture(t)
	packet := packetFor(t, "landing", project, foundation)
	badArgs := []string{"--target", "landing", "--expected-packet-digest", "sha256:" + strings.Repeat("0", 64), "--reviewed-by", "reviewer", "--review-reference", "approval-1", "--foundation-root", foundation, "--project-root", project}
	if err := runRecord(badArgs); err == nil {
		t.Fatal("mismatched packet approval was accepted")
	}
	if _, err := os.Stat(filepath.Join(foundation, markerName)); !os.IsNotExist(err) {
		t.Fatal("rejected approval changed marker")
	}
	args := []string{"--target", "landing", "--expected-packet-digest", packet.PacketDigest, "--reviewed-by", "reviewer", "--review-reference", "approval-1", "--foundation-root", foundation, "--project-root", project}
	if err := runRecord(args); err != nil {
		t.Fatal(err)
	}
	marker, exists, err := ks.LoadMarker(filepath.Join(foundation, markerName))
	if err != nil || !exists {
		t.Fatalf("load marker: exists=%v err=%v", exists, err)
	}
	if marker.Landing == nil || marker.Roadmap != nil {
		t.Fatalf("per-target review was not preserved: %+v", marker)
	}
	current := captureStdout(t, func() error { return runStatus([]string{"--project-root", project, "--foundation-root", foundation}) })
	var response ks.Response
	if err := json.Unmarshal([]byte(current), &response); err != nil {
		t.Fatal(err)
	}
	if response.Landing.Status != "current" || response.Roadmap.Status != "unverifiable" {
		t.Fatalf("target isolation status: Landing=%+v Roadmap=%+v", response.Landing, response.Roadmap)
	}
}

func TestRecordReviewRejectsSemanticallyInvalidExistingMarkersWithoutMutation(t *testing.T) {
	for name, data := range map[string]string{
		"empty-object":  ` { } `,
		"wrong-schema":  `{"schema_version":"2","project_id":"builder","landing":null,"roadmap":null}`,
		"wrong-project": `{"schema_version":"1","project_id":"other","landing":null,"roadmap":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			project, foundation := newGitFixture(t)
			packet := packetFor(t, "landing", project, foundation)
			markerPath := filepath.Join(foundation, markerName)
			if err := os.WriteFile(markerPath, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(markerPath)
			if err != nil {
				t.Fatal(err)
			}
			err = runRecord([]string{"--target", "landing", "--expected-packet-digest", packet.PacketDigest, "--reviewed-by", "reviewer", "--review-reference", "approval", "--foundation-root", foundation})
			if err == nil {
				t.Fatal("semantically invalid marker was accepted")
			}
			after, readErr := os.ReadFile(markerPath)
			if readErr != nil || string(after) != string(before) {
				t.Fatalf("rejected marker was modified: read=%v", readErr)
			}
		})
	}
}

func TestConcurrentTargetReviewsDoNotLoseUpdates(t *testing.T) {
	project, foundation := newGitFixture(t)
	landing := packetFor(t, "landing", project, foundation)
	roadmap := packetFor(t, "roadmap", project, foundation)
	makeArgs := func(target string, packet ks.Packet) []string {
		return []string{"--target", target, "--expected-packet-digest", packet.PacketDigest, "--reviewed-by", "reviewer-" + target, "--review-reference", "approval-" + target, "--foundation-root", foundation, "--project-root", project}
	}
	var wait sync.WaitGroup
	errors := make(chan error, 2)
	for _, args := range [][]string{makeArgs("landing", landing), makeArgs("roadmap", roadmap)} {
		wait.Add(1)
		go func(current []string) { defer wait.Done(); errors <- runRecord(current) }(args)
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	marker, exists, err := ks.LoadMarker(filepath.Join(foundation, markerName))
	if err != nil || !exists {
		t.Fatalf("load marker: exists=%v err=%v", exists, err)
	}
	if marker.Landing == nil || marker.Roadmap == nil {
		t.Fatalf("concurrent update lost a target: %+v", marker)
	}
}

func TestMarkerLockWaitsForCurrentWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marker.lock")
	first, err := acquireMarkerLock(path)
	if err != nil {
		t.Fatal(err)
	}
	waited := make(chan struct{}, 1)
	resume := make(chan struct{})
	second := make(chan *os.File, 1)
	failures := make(chan error, 1)
	go func() {
		lock, err := acquireMarkerLockWithWait(path, func(time.Duration) {
			select {
			case waited <- struct{}{}:
			default:
			}
			<-resume
		})
		if err != nil {
			failures <- err
			return
		}
		second <- lock
	}()
	select {
	case <-waited:
	case err := <-failures:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("contender never attempted the held lock")
	}
	select {
	case lock := <-second:
		lock.Close()
		t.Fatal("contender acquired lock while first writer held it")
	default:
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	close(resume)
	select {
	case err := <-failures:
		t.Fatal(err)
	case lock := <-second:
		if err := lock.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("contender did not acquire after release")
	}
}

func TestReviewPacketRejectsCommittedSymlinkInputs(t *testing.T) {
	_, foundation := newGitFixture(t)
	path := filepath.Join(foundation, "modules", "viewer.md")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	makeTestSymlink(t, "../project_mandate.md", path)
	gitTest(t, foundation, "add", "--all")
	gitTest(t, foundation, "commit", "-qm", "symlink fixture")
	if _, err := committedPacket(foundation, "landing", "HEAD"); err == nil {
		t.Fatal("committed symlink input was accepted")
	}
}

func TestAtomicSnapshotReplacesWholeFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "status.json")
	if err := atomicWrite(path, []byte("old snapshot"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(path, []byte("new snapshot"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new snapshot" {
		t.Fatalf("unexpected final snapshot: %q", data)
	}
	temporary, err := filepath.Glob(filepath.Join(root, ".knowledge-status-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporary) != 0 {
		t.Fatalf("temporary files remained after replacement: %v", temporary)
	}
}

func TestRecordReviewRejectsStalePacketsAndPreservesMarker(t *testing.T) {
	for _, mutation := range []string{"source-bytes", "output-bytes", "inventory", "head", "wrong-target"} {
		t.Run(mutation, func(t *testing.T) {
			project, foundation := newGitFixture(t)
			target := "landing"
			if mutation == "inventory" {
				target = "roadmap"
			}
			packet := packetFor(t, target, project, foundation)
			for _, otherTarget := range []string{"landing", "roadmap"} {
				p := packetFor(t, otherTarget, project, foundation)
				if err := runRecord([]string{"--target", otherTarget, "--expected-packet-digest", p.PacketDigest, "--reviewed-by", "seed", "--review-reference", "seed-approval", "--foundation-root", foundation}); err != nil {
					t.Fatal(err)
				}
			}
			markerPath := filepath.Join(foundation, markerName)
			original, err := os.ReadFile(markerPath)
			if err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "source-bytes":
				writeFixture(t, foundation, "project_mandate.md", "changed source\n")
			case "output-bytes":
				writeFixture(t, foundation, "project_landing.md", "changed output\n")
			case "inventory":
				writeFixture(t, foundation, "todos/active/new.md", "new inventory item\n")
			case "head":
				writeFixture(t, foundation, "todos/active/todo.md", "committed body change\n")
				gitTest(t, foundation, "add", "todos/active/todo.md")
				gitTest(t, foundation, "commit", "-qm", "advance HEAD")
			case "wrong-target":
				target = "roadmap"
			}
			err = runRecord([]string{"--target", target, "--expected-packet-digest", packet.PacketDigest, "--reviewed-by", "reviewer", "--review-reference", "approval", "--foundation-root", foundation, "--revision", packet.SourceRevision})
			if err == nil {
				t.Fatalf("%s mutation accepted stale/wrong packet", mutation)
			}
			after, readErr := os.ReadFile(markerPath)
			if readErr != nil || string(after) != string(original) {
				t.Fatalf("%s rejection changed marker: read=%v", mutation, readErr)
			}
		})
	}
}

func TestRoadmapRealGitInventoryTransitions(t *testing.T) {
	for _, change := range []string{"create", "unstaged-delete", "staged-delete", "rename", "staged-lane-move"} {
		t.Run(change, func(t *testing.T) {
			project, foundation := newGitFixture(t)
			packet := packetFor(t, "roadmap", project, foundation)
			if err := runRecord([]string{"--target", "roadmap", "--expected-packet-digest", packet.PacketDigest, "--reviewed-by", "reviewer", "--review-reference", "approval", "--foundation-root", foundation}); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "create":
				writeFixture(t, foundation, "todos/active/new.md", "new\n")
			case "unstaged-delete":
				if err := os.Remove(filepath.Join(foundation, "todos/active/todo.md")); err != nil {
					t.Fatal(err)
				}
			case "staged-delete":
				gitTest(t, foundation, "rm", "todos/active/todo.md")
			case "rename":
				if err := os.MkdirAll(filepath.Join(foundation, "todos/done"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(foundation, "todos/active/todo.md"), filepath.Join(foundation, "todos/done/todo.md")); err != nil {
					t.Fatal(err)
				}
			case "staged-lane-move":
				if err := os.MkdirAll(filepath.Join(foundation, "todos/done"), 0o755); err != nil {
					t.Fatal(err)
				}
				gitTest(t, foundation, "mv", "todos/active/todo.md", "todos/done/todo.md")
			}
			text := captureStdout(t, func() error { return runStatus([]string{"--foundation-root", foundation, "--project-root", project}) })
			var response ks.Response
			if err := json.Unmarshal([]byte(text), &response); err != nil {
				t.Fatal(err)
			}
			if response.Roadmap.Status != "review_required" || response.Roadmap.ReasonCode != "source_changed" {
				t.Fatalf("%s was not represented as inventory change: %+v", change, response.Roadmap)
			}
		})
	}
}

func TestMediumBCIOverlappingReviewsPreserveExactProvenance(t *testing.T) {
	for batch := 0; batch < 3; batch++ {
		t.Run(fmt.Sprintf("batch-%d", batch+1), func(t *testing.T) {
			project, foundation := newGitFixture(t)
			packets := map[string]ks.Packet{"landing": packetFor(t, "landing", project, foundation), "roadmap": packetFor(t, "roadmap", project, foundation)}
			start := make(chan struct{})
			errs := make(chan error, 10)
			submitted := map[string]map[string]string{"landing": {}, "roadmap": {}}
			var wg sync.WaitGroup
			for i := 0; i < 10; i++ {
				target := "landing"
				if i%2 == 1 {
					target = "roadmap"
				}
				id := fmt.Sprintf("batch-%d-%s-reviewer-%02d", batch+1, target, i)
				reference := "approval:" + id
				submitted[target][id] = reference
				args := []string{"--target", target, "--expected-packet-digest", packets[target].PacketDigest, "--reviewed-by", id, "--review-reference", reference, "--foundation-root", foundation}
				wg.Add(1)
				go func(args []string) { defer wg.Done(); <-start; errs <- runRecord(args) }(args)
			}
			close(start)
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			marker, exists, err := ks.LoadMarker(filepath.Join(foundation, markerName))
			if err != nil || !exists {
				t.Fatalf("marker load: exists=%v err=%v", exists, err)
			}
			for target, review := range map[string]*ks.Review{"landing": marker.Landing, "roadmap": marker.Roadmap} {
				if review == nil {
					t.Fatalf("missing %s provenance", target)
				}
				packet := packets[target]
				if review.ReviewedSourceDigest != packet.SourceDigest || review.ReviewedDocumentDigest != packet.DocumentDigest || review.ReviewedPacketDigest != packet.PacketDigest || review.ReviewedRevision != packet.SourceRevision {
					t.Fatalf("%s provenance does not match its packet: %+v packet=%+v", target, review, packet)
				}
				if submitted[target][review.ReviewedBy] != review.ReviewReference {
					t.Fatalf("%s provenance pair was not submitted for that target: %+v submitted=%v", target, review, submitted[target])
				}
			}
		})
	}
}

func TestAtomicSnapshotReadersNeverObservePartialFileAndFailurePreservesOld(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "snapshot.json")
	a := []byte(strings.Repeat("A", 1<<20))
	b := []byte(strings.Repeat("B", 1<<20))
	if err := atomicWrite(path, a, 0o644); err != nil {
		t.Fatal(err)
	}
	preRename := make(chan struct{})
	allowRename := make(chan struct{})
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- atomicWriteUsing(path, b, 0o644, func(temp, target string) error {
			close(preRename)
			<-allowRename
			return os.Rename(temp, target)
		})
	}()
	<-preRename
	pending, err := os.ReadFile(path)
	if err != nil || string(pending) != string(a) {
		t.Fatalf("reader during pending rename did not see complete prior snapshot: %v", err)
	}
	close(allowRename)
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		<-start
		for i := 0; i < 12; i++ {
			value := a
			if i%2 == 0 {
				value = b
			}
			if err := atomicWrite(path, value, 0o644); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	close(start)
	for i := 0; i < 40; i++ {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != string(a) && string(data) != string(b) {
			t.Fatal("reader observed a partial or mixed snapshot")
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	err = atomicWriteUsing(path, []byte("replacement"), 0o644, func(string, string) error { return errors.New("injected rename failure") })
	if err == nil {
		t.Fatal("injected atomic replacement failure was ignored")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(old) {
		t.Fatalf("failed replacement damaged old snapshot: err=%v", err)
	}
}
