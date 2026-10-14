package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	ac "belluga-builder-viewer/internal/artifact_catalog"
)

const snapshotRelativePath = "local-api/v1/projects/builder/artifact-status.json"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == ac.TargetDesignSystem {
		return runDesignSystem(args[1:], stdout, stderr)
	}
	if len(args) == 0 || args[0] != ac.TargetPrototypes {
		fmt.Fprintln(stderr, "usage: artifact-status prototypes --mode working-tree|committed [--revision SHA]")
		return 64
	}
	flags := flag.NewFlagSet("prototypes", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	workspaceDefault, rootErr := findWorkspaceRoot()
	if rootErr != nil {
		workspaceDefault = "."
	}
	project := flags.String("project-root", workspaceDefault, "Builder project root inside workspace")
	foundationDefault := filepath.Join(workspaceDefault, "belluga_builder_foundation_documentation")
	foundation := flags.String("foundation-root", foundationDefault, "dedicated Builder Foundation Git checkout")
	mode := flags.String("mode", "", "working-tree or committed")
	stdoutOnly := flags.Bool("stdout-only", false, "write only the fresh response to stdout")
	revision := flags.String("revision", "", "full committed Foundation SHA (committed mode only)")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "artifact-status: invalid arguments")
		return 64
	}
	if *mode != "working-tree" && *mode != "committed" {
		fmt.Fprintln(stderr, "artifact-status: --mode must be working-tree or committed")
		return 64
	}
	if *mode == "working-tree" && *revision != "" {
		fmt.Fprintln(stderr, "artifact-status: --revision is only valid in committed mode")
		return 64
	}
	if *mode == "committed" && *revision != "" && !isFullRevision(*revision) {
		fmt.Fprintln(stderr, "artifact-status: --revision must be a full hexadecimal commit SHA")
		return 64
	}
	workspaceRoot, err := filepath.Abs(workspaceDefault)
	if err != nil {
		return fail(stderr, err)
	}
	projectRoot, err := trustedRoot(workspaceRoot, *project)
	if err != nil {
		return fail(stderr, err)
	}
	foundationRoot, err := trustedRoot(workspaceRoot, *foundation)
	if err != nil {
		return fail(stderr, err)
	}
	var source ac.Source
	if *mode == "working-tree" {
		source, err = ac.NewWorkingTree(foundationRoot)
	} else {
		var gitSource *ac.GitTree
		gitSource, err = ac.NewGitTree(foundationRoot, *revision)
		source = gitSource
	}
	if err != nil {
		if errors.Is(err, ac.ErrRevisionUnavailable) {
			response := unavailableResponse(*mode, ac.DefaultProjectID, *revision)
			if outputErr := emitResponseMode(response, projectRoot, stdout, []string{foundationRoot}, *stdoutOnly); outputErr != nil {
				return fail(stderr, outputErr)
			}
			return 2
		}
		return fail(stderr, err)
	}
	response := ac.Evaluate(source, ac.DefaultProjectID)
	if err := emitResponseMode(response, projectRoot, stdout, []string{foundationRoot}, *stdoutOnly); err != nil {
		return fail(stderr, err)
	}
	if response.Outcome == "go" {
		return 0
	}
	return 2
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "artifact-status: %s\n", err.Error())
	return 70
}

func unavailableResponse(mode, projectID, revision string) ac.Response {
	var revisionPointer *string
	if isFullRevision(revision) {
		revisionPointer = &revision
	}
	return ac.Response{
		SchemaVersion: ac.SchemaVersion, ProjectID: projectID, Target: ac.TargetPrototypes,
		AuthorityScope: ac.AuthorityScope, Mode: mode, Revision: revisionPointer,
		Outcome: "no_go", Items: []ac.Item{},
		Diagnostics:            []ac.Diagnostic{{Code: "revision_unavailable", SourceID: stringPointer(projectID), Path: stringPointer("prototypes/catalog.json"), Message: "the requested Foundation commit is unavailable", Resolution: "Provide a full commit SHA present in the bound Foundation Git repository."}},
		DesignSystemValidation: ac.DesignSystemPending,
	}
}

func stringPointer(value string) *string { return &value }

func isFullRevision(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func emitResponse(response any, projectRoot string, stdout io.Writer, protectedRoots []string) error {
	return emitResponseMode(response, projectRoot, stdout, protectedRoots, false)
}

func emitResponseMode(response any, projectRoot string, stdout io.Writer, protectedRoots []string, stdoutOnly bool) error {
	if err := ensureSnapshotOutsideSources(projectRoot, protectedRoots...); err != nil {
		return err
	}
	data, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := stdout.Write(data); err != nil {
		return err
	}
	if stdoutOnly {
		return nil
	}
	return atomicSnapshot(projectRoot, data)
}

func ensureSnapshotOutsideSources(projectRoot string, sourceRoots ...string) error {
	snapshot, err := filepath.Abs(filepath.Join(projectRoot, filepath.FromSlash(snapshotRelativePath)))
	if err != nil {
		return errors.New("snapshot destination cannot be resolved")
	}
	snapshot = filepath.Clean(snapshot)
	for _, sourceRoot := range sourceRoots {
		if sourceRoot == "" {
			continue
		}
		absoluteSource, err := filepath.Abs(sourceRoot)
		if err != nil {
			return errors.New("read-only source root cannot be resolved")
		}
		relative, err := filepath.Rel(filepath.Clean(absoluteSource), snapshot)
		if err != nil || relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))) {
			return errors.New("snapshot destination overlaps a read-only source")
		}
	}
	return nil
}

func trustedRoot(workspaceRoot, requested string) (string, error) {
	absolute, err := filepath.Abs(requested)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(requested) {
		absolute = filepath.Join(workspaceRoot, requested)
	}
	abs, err := filepath.Abs(absolute)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(workspaceRoot, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", errors.New("trusted root must remain inside the mounted workspace")
	}
	if err := ac.RejectSymlinkComponents(abs); err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("trusted root is not a directory")
	}
	return abs, nil
}

func atomicSnapshot(projectRoot string, data []byte) error {
	path := filepath.Join(projectRoot, filepath.FromSlash(snapshotRelativePath))
	if err := ac.EnsureSafeDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("snapshot destination is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".artifact-status-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temporary.Name()
	defer os.Remove(tempPath)
	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	// Rename is the commit point. Do not report a later directory-sync error
	// after replacing the prior snapshot; callers treat any reported error as stale.
	return nil
}

func findWorkspaceRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for current := cwd; ; current = filepath.Dir(current) {
		if _, err := os.Stat(filepath.Join(current, "builder-app", "service", "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(current, "local-api")); err == nil {
				return current, nil
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return "", errors.New("Builder workspace root could not be located; run from the workspace or provide trusted project and Foundation roots")
}
