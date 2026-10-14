package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	ac "belluga-builder-viewer/internal/artifact_catalog"
)

func TestAtomicSnapshotConcurrentWritersKeepWholeLastWrite(t *testing.T) {
	root := t.TempDir()
	for batch := 0; batch < 2; batch++ {
		start := make(chan struct{})
		var writers sync.WaitGroup
		errorsFound := make(chan error, 5)
		for writer := 0; writer < 5; writer++ {
			writers.Add(1)
			go func(writer int) {
				defer writers.Done()
				<-start
				stdout := &strings.Builder{}
				response := ac.Response{SchemaVersion: ac.SchemaVersion, ProjectID: ac.DefaultProjectID, Target: ac.TargetPrototypes, AuthorityScope: ac.AuthorityScope, Mode: fmt.Sprintf("writer-%d-batch-%d", writer, batch), Outcome: "go", Items: []ac.Item{}, Diagnostics: []ac.Diagnostic{}, DesignSystemValidation: ac.DesignSystemPending}
				if err := emitResponse(response, root, stdout, nil); err != nil {
					errorsFound <- err
					return
				}
				var echoed ac.Response
				if err := json.Unmarshal([]byte(stdout.String()), &echoed); err != nil || echoed.Mode != response.Mode || echoed.Target != response.Target || echoed.ProjectID != response.ProjectID {
					errorsFound <- fmt.Errorf("writer %d batch %d received another writer's response: %s", writer, batch, stdout.String())
				}
			}(writer)
		}
		close(start)
		writers.Wait()
		close(errorsFound)
		for err := range errorsFound {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(snapshotRelativePath)))
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			SchemaVersion string `json:"schema_version"`
			ProjectID     string `json:"project_id"`
			Target        string `json:"target"`
			Mode          string `json:"mode"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatalf("concurrent snapshot is partial JSON: %v; bytes=%q", err, data)
		}
		if result.SchemaVersion != "1" || result.ProjectID != "builder" || result.Target != "prototypes" || !strings.HasPrefix(result.Mode, "writer-") || !strings.HasSuffix(result.Mode, fmt.Sprintf("batch-%d", batch)) {
			t.Fatalf("snapshot is not one complete last-writer result: %#v", result)
		}
	}
}

func TestAtomicSnapshotObserverSeesCompleteResponsesDuringOverlappingWrites(t *testing.T) {
	root := t.TempDir()
	seed := ac.Response{SchemaVersion: ac.SchemaVersion, ProjectID: ac.DefaultProjectID, Target: ac.TargetPrototypes, AuthorityScope: ac.AuthorityScope, Mode: "seed", Outcome: "go", Items: []ac.Item{}, Diagnostics: []ac.Diagnostic{}, DesignSystemValidation: ac.DesignSystemPending}
	if err := emitResponse(seed, root, &strings.Builder{}, nil); err != nil {
		t.Fatal(err)
	}
	// Hold one actual emitter inside its caller-provided stdout writer. While it
	// remains active, other real emitters replace the snapshot and a completed
	// reader checks the exact full payload after each replacement.
	entered, release := make(chan struct{}), make(chan struct{})
	blockedStdout := blockingWriter{entered: entered, release: release}
	blocked := snapshotResponse("blocked-emitter", "b")
	blockedPayload := serializedResponse(t, blocked)
	blockedErr := make(chan error, 1)
	go func() { blockedErr <- emitResponse(blocked, root, blockedStdout, nil) }()
	<-entered

	path := filepath.Join(root, filepath.FromSlash(snapshotRelativePath))
	completedPayloads := make([][]byte, 0, 10)
	completedReads := 0
	for i := 0; i < 10; i++ {
		response := snapshotResponse(fmt.Sprintf("writer-%02d", i), string(rune('a'+i)))
		expected := serializedResponse(t, response)
		var stdout strings.Builder
		if err := emitResponse(response, root, &stdout, nil); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal([]byte(stdout.String()), expected) {
			t.Fatalf("writer %q stdout differed from its complete response", response.Mode)
		}
		completedPayloads = append(completedPayloads, expected)
		actual, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("completed read while another emitResponse remained active: %v", err)
		}
		matched := false
		for _, payload := range completedPayloads {
			if bytes.Equal(actual, payload) {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("completed read during active emitter was not the exact full payload of a completed writer: %q", actual[:min(len(actual), 96)])
		}
		completedReads++
	}
	if completedReads == 0 {
		t.Fatal("no successful snapshot read completed while another emitResponse call was active")
	}
	close(release)
	if err := <-blockedErr; err != nil {
		t.Fatalf("held emitResponse failed after release: %v", err)
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, blockedPayload) {
		t.Fatalf("released emitter did not commit its identified whole payload: err=%v", err)
	}
}

type blockingWriter struct {
	entered chan<- struct{}
	release <-chan struct{}
}

func (writer blockingWriter) Write(data []byte) (int, error) {
	close(writer.entered)
	<-writer.release
	return len(data), nil
}

func snapshotResponse(mode, payload string) ac.Response {
	return ac.Response{SchemaVersion: ac.SchemaVersion, ProjectID: ac.DefaultProjectID, Target: ac.TargetPrototypes, AuthorityScope: ac.AuthorityScope, Mode: mode, Outcome: "go", Items: []ac.Item{{ID: "payload", Name: strings.Repeat(payload, 256*1024)}}, Diagnostics: []ac.Diagnostic{}, DesignSystemValidation: ac.DesignSystemPending}
}

func serializedResponse(t *testing.T, response ac.Response) []byte {
	t.Helper()
	data, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func TestEmitResponseWriterFailureLeavesNoSnapshot(t *testing.T) {
	root := t.TempDir()
	err := emitResponse(ac.Response{SchemaVersion: ac.SchemaVersion, ProjectID: ac.DefaultProjectID, Target: ac.TargetPrototypes, AuthorityScope: ac.AuthorityScope, Mode: "working-tree", Outcome: "go", Items: []ac.Item{}, Diagnostics: []ac.Diagnostic{}, DesignSystemValidation: ac.DesignSystemPending}, root, failingWriter{}, nil)
	if err == nil {
		t.Fatal("expected injected stdout failure")
	}
	if _, statErr := os.Lstat(filepath.Join(root, filepath.FromSlash(snapshotRelativePath))); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed stdout write left a fresh snapshot: %v", statErr)
	}
}

func TestEmitResponseRejectsSnapshotInsideReadOnlySourceBeforeOutput(t *testing.T) {
	workspace := t.TempDir()
	foundation := filepath.Join(workspace, "foundation")
	if err := os.MkdirAll(foundation, 0o755); err != nil {
		t.Fatal(err)
	}
	response := ac.Response{SchemaVersion: ac.SchemaVersion, ProjectID: ac.DefaultProjectID, Target: ac.TargetPrototypes, AuthorityScope: ac.AuthorityScope, Mode: "working-tree", Outcome: "go", Items: []ac.Item{}, Diagnostics: []ac.Diagnostic{}, DesignSystemValidation: ac.DesignSystemPending}
	for _, projectRoot := range []string{foundation, filepath.Join(foundation, "nested-project")} {
		if err := os.MkdirAll(projectRoot, 0o755); err != nil {
			t.Fatal(err)
		}
		var stdout strings.Builder
		if err := emitResponse(response, projectRoot, &stdout, []string{foundation}); err == nil {
			t.Fatalf("snapshot destination inside read-only Foundation was accepted: project root %q", projectRoot)
		}
		if stdout.Len() != 0 {
			t.Fatalf("protected output emitted JSON before rejecting project root %q: %q", projectRoot, stdout.String())
		}
		snapshotPath := filepath.Join(projectRoot, filepath.FromSlash(snapshotRelativePath))
		if _, err := os.Lstat(snapshotPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("protected output created snapshot at %q: %v", snapshotPath, err)
		}
		if _, err := os.Lstat(filepath.Join(projectRoot, "local-api")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("protected output created directories under %q: %v", projectRoot, err)
		}
	}

	// A Project root above Foundation remains valid when its own fixed output is outside it.
	var stdout strings.Builder
	if err := emitResponse(response, workspace, &stdout, []string{foundation}); err != nil {
		t.Fatalf("allowed ancestor Project root was rejected: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("allowed ancestor Project root emitted no response")
	}
	if _, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(snapshotRelativePath))); err != nil {
		t.Fatalf("allowed ancestor Project snapshot was not written: %v", err)
	}
}

func TestAtomicSnapshotRejectsUnsafeDestinationBeforeReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, filepath.FromSlash(snapshotRelativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("prior snapshot"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere", path+".link"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".link", path); err != nil {
		t.Fatal(err)
	}
	if err := atomicSnapshot(root, []byte("fresh")); err == nil {
		t.Fatal("snapshot replacement accepted a symlink destination")
	}
	if target, err := os.Readlink(path); err != nil || target != "elsewhere" {
		t.Fatalf("pre-commit failure changed the prior symlink destination: target=%q err=%v", target, err)
	}
}

func TestInvalidCommandUsesUsageExitWithoutSnapshot(t *testing.T) {
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	status := run([]string{"unexpected"}, stdout, stderr)
	if status != 64 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "usage:") {
		t.Fatalf("usage failure contract mismatch: status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
}

func TestMalformedCommittedRevisionIsUsageAndNeverEchoed(t *testing.T) {
	for _, revision := range []string{"main", "deadbee", "DEADBEEF"} {
		stdout, stderr := &strings.Builder{}, &strings.Builder{}
		status := run([]string{"prototypes", "--mode", "committed", "--revision", revision}, stdout, stderr)
		if status != 64 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "full hexadecimal commit SHA") {
			t.Fatalf("malformed revision %q was not a usage error: status=%d stdout=%q stderr=%q", revision, status, stdout.String(), stderr.String())
		}
		if response := unavailableResponse("committed", ac.DefaultProjectID, revision); response.Revision != nil {
			t.Fatalf("unavailable response echoed invalid revision %q", *response.Revision)
		}
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("injected writer failure") }

func TestEmitResponseStdoutOnlyDoesNotWriteSnapshot(t *testing.T) {
	root := t.TempDir()
	response := snapshotResponse("fresh-read", "evidence")
	var stdout strings.Builder
	if err := emitResponseMode(response, root, &stdout, nil, true); err != nil {
		t.Fatal(err)
	}
	var got ac.Response
	if err := json.Unmarshal([]byte(stdout.String()), &got); err != nil || got.Mode != response.Mode {
		t.Fatalf("fresh stdout response invalid: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(snapshotRelativePath))); !os.IsNotExist(err) {
		t.Fatal("stdout-only evaluator wrote an observation snapshot")
	}
}
