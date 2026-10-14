package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	ac "belluga-builder-viewer/internal/artifact_catalog"
)

const defaultSourceBindings = "local-api/source-bindings.json"

func runDesignSystem(args []string, stdout, stderr io.Writer) int {
	flags := flagSet("design-system")
	workspaceDefault, rootErr := findWorkspaceRoot()
	if rootErr != nil {
		workspaceDefault = "."
	}
	project := flags.String("project-root", workspaceDefault, "Builder project root inside workspace")
	foundation := flags.String("foundation-root", filepath.Join(workspaceDefault, "belluga_builder_foundation_documentation"), "bound Project Foundation Git checkout")
	bindingsPath := flags.String("source-bindings", defaultSourceBindings, "trusted local source map inside workspace")
	mode := flags.String("mode", "", "working-tree or committed")
	stdoutOnly := flags.Bool("stdout-only", false, "write only the fresh response to stdout")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "artifact-status: invalid arguments")
		return 64
	}
	if *mode != "working-tree" && *mode != "committed" {
		fmt.Fprintln(stderr, "artifact-status: --mode must be working-tree or committed")
		return 64
	}
	workspaceRoot, err := filepath.Abs(workspaceDefault)
	if err != nil {
		return fail(stderr, err)
	}
	if err := ac.RejectSymlinkComponents(workspaceRoot); err != nil {
		return fail(stderr, errors.New("workspace root is unsafe"))
	}
	workspaceInfo, err := os.Stat(workspaceRoot)
	if err != nil || !workspaceInfo.IsDir() {
		return fail(stderr, errors.New("workspace root is unavailable"))
	}
	projectRoot, err := trustedRoot(workspaceRoot, *project)
	if err != nil {
		return fail(stderr, err)
	}
	foundationRoot, err := trustedRoot(workspaceRoot, *foundation)
	if err != nil {
		return fail(stderr, err)
	}
	protectedRoots := []string{foundationRoot}
	emit := func(response any) error {
		return emitResponseMode(response, projectRoot, stdout, protectedRoots, *stdoutOnly)
	}
	mapPath, err := trustedFilePath(workspaceRoot, *bindingsPath)
	if err != nil {
		response := designSystemNoGo(*mode, "read_failed", "local-api/source-bindings.json", "trusted local source map path is unsafe", "Use a readable workspace-relative source map without symlink components.")
		if outputErr := emit(response); outputErr != nil {
			return fail(stderr, outputErr)
		}
		return 2
	}
	mapBytes, readErr := readBoundedTrustedFile(mapPath, ac.MaxSourceBindingsBytes)
	if errors.Is(readErr, os.ErrNotExist) {
		response := designSystemNoGo(*mode, "source_binding_missing", "local-api/source-bindings.json", "trusted local source map is absent", "Create the explicitly bound local source map from source-bindings.example.json; do not invent a Design System.")
		if outputErr := emit(response); outputErr != nil {
			return fail(stderr, outputErr)
		}
		return 2
	}
	if readErr != nil {
		code := "read_failed"
		if errors.Is(readErr, ac.ErrLimit) {
			code = "limit_exceeded"
		}
		response := designSystemNoGo(*mode, code, "local-api/source-bindings.json", "trusted local source map could not be read safely", "Restore a regular readable source map no larger than 64 KiB.")
		if outputErr := emit(response); outputErr != nil {
			return fail(stderr, outputErr)
		}
		return 2
	}
	bindings, err := ac.DecodeSourceBindings(mapBytes)
	if err != nil {
		code := "invalid_schema"
		if errors.Is(err, ac.ErrLimit) {
			code = "limit_exceeded"
		}
		response := designSystemNoGo(*mode, code, "local-api/source-bindings.json", "trusted local source map does not match the strict contract", "Correct the exact schema_version 1 source map with no unknown or duplicate keys.")
		if outputErr := emit(response); outputErr != nil {
			return fail(stderr, outputErr)
		}
		return 2
	}
	protectedRoots = append(protectedRoots, safeDeclaredSourceRoots(workspaceRoot, bindings)...)
	mapDiagnostics := ac.ValidateSourceBindings(bindings, *mode)
	if len(mapDiagnostics) != 0 {
		response := designSystemNoGoWithDiagnostics(*mode, mapDiagnostics)
		if outputErr := emit(response); outputErr != nil {
			return fail(stderr, outputErr)
		}
		return 2
	}
	level, binding := ac.SelectSourceBinding(bindings)
	var checkoutRoot string
	if binding != nil {
		checkoutRoot = filepath.Join(workspaceRoot, filepath.FromSlash(binding.CheckoutRoot))
	}
	if binding == nil {
		response := designSystemNoGo(*mode, "source_binding_missing", "local-api/source-bindings.json", "no effective Design System source is bound", "Bind exactly the available project, company, or default source without merging their contents.")
		if outputErr := emit(response); outputErr != nil {
			return fail(stderr, outputErr)
		}
		return 2
	}
	if level == "project" {
		if filepath.Clean(checkoutRoot) != filepath.Clean(foundationRoot) {
			response := designSystemNoGoForBinding(*mode, level, *binding, "owner_mismatch", "project source does not resolve to the bound Project Foundation Repository", "Bind the Project source to the exact existing Project Foundation Git repository.")
			if outputErr := emit(response); outputErr != nil {
				return fail(stderr, outputErr)
			}
			return 2
		}
	}
	var source ac.Source
	if *mode == "working-tree" {
		source, err = ac.NewWorkingTree(checkoutRoot)
	} else {
		if binding.Revision == nil {
			response := designSystemNoGoForBinding(*mode, level, *binding, "revision_unavailable", "committed source has no explicit full SHA", "Set the selected binding revision to its exact full commit SHA; implicit HEAD is not supported.")
			if outputErr := emit(response); outputErr != nil {
				return fail(stderr, outputErr)
			}
			return 2
		}
		source, err = ac.NewGitTree(checkoutRoot, *binding.Revision)
	}
	if err != nil {
		code := "read_failed"
		if errors.Is(err, ac.ErrRevisionUnavailable) {
			code = "revision_unavailable"
		}
		response := designSystemNoGoForBinding(*mode, level, *binding, code, "selected source repository is unavailable or unsafe", "Use an existing workspace-contained Git repository at the exact bound root and a resolvable explicit revision.")
		if outputErr := emit(response); outputErr != nil {
			return fail(stderr, outputErr)
		}
		return 2
	}
	response := ac.EvaluateDesignSystem(source, bindings.ProjectID, bindings.CompanyID, level, *binding)
	if outputErr := emit(response); outputErr != nil {
		return fail(stderr, outputErr)
	}
	if response.Outcome == "go" {
		return 0
	}
	return 2
}

// safeDeclaredSourceRoots returns only unambiguous workspace-relative fixed-slot
// checkout paths. It inspects path metadata for symlink components, never source
// contents, and intentionally includes unselected slots for the output boundary.
func safeDeclaredSourceRoots(workspaceRoot string, bindings ac.SourceBindings) []string {
	var roots []string
	for _, binding := range []*ac.SourceBinding{bindings.Sources.Project, bindings.Sources.Company, bindings.Sources.Default} {
		if binding == nil || len([]byte(binding.CheckoutRoot)) > ac.MaxPathBytes || ac.ValidateRelativePath(binding.CheckoutRoot) != nil {
			continue
		}
		root := filepath.Join(workspaceRoot, filepath.FromSlash(binding.CheckoutRoot))
		absolute, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(workspaceRoot, absolute)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
			continue
		}
		if ac.RejectSymlinkComponents(absolute) != nil {
			continue
		}
		roots = append(roots, absolute)
	}
	return roots
}

func flagSet(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}

func trustedFilePath(workspaceRoot, requested string) (string, error) {
	path := requested
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspaceRoot, filepath.FromSlash(path))
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(workspaceRoot, absolute)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", errors.New("trusted file must remain inside the workspace")
	}
	if err := ac.RejectSymlinkComponents(absolute); err != nil {
		return "", err
	}
	return absolute, nil
}

func readBoundedTrustedFile(path string, limit int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, ac.ErrUnsafePath
	}
	if before.Size() > limit {
		return nil, ac.ErrLimit
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		file.Close()
		return nil, errors.New("trusted file changed before read")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ac.ErrLimit
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || int64(len(data)) != after.Size() {
		return nil, errors.New("trusted file changed during read")
	}
	return data, nil
}

func designSystemNoGo(mode, code, path, message, resolution string) ac.DesignSystemResponse {
	return designSystemNoGoWithDiagnostics(mode, []ac.Diagnostic{{Code: code, Path: stringPointer(path), Message: message, Resolution: resolution}})
}

func designSystemNoGoWithDiagnostics(mode string, diagnostics []ac.Diagnostic) ac.DesignSystemResponse {
	return ac.DesignSystemResponse{SchemaVersion: ac.SchemaVersion, ProjectID: ac.DefaultProjectID, CompanyID: "belluga-solutions", Target: ac.TargetDesignSystem, AuthorityScope: ac.AuthorityScope, Mode: mode, Outcome: "no_go", Items: []ac.DesignSystemDefinition{}, Diagnostics: diagnostics, DesignSystemValidation: "not_evaluated"}
}

func designSystemNoGoForBinding(mode, level string, binding ac.SourceBinding, code, message, resolution string) ac.DesignSystemResponse {
	ownerLevel, ownerID := "", ""
	switch level {
	case "project":
		ownerLevel, ownerID = "project", "builder"
	case "company":
		ownerLevel, ownerID = "company", "belluga-solutions"
	case "default":
		ownerLevel, ownerID = "default", "builder-platform"
	}
	response := designSystemNoGo(mode, code, binding.DefinitionPath, message, resolution)
	response.SelectedLevel = stringPointer(level)
	response.Source = &ac.DesignSystemSource{RepositoryID: binding.RepositoryID, OwnerLevel: ownerLevel, OwnerID: ownerID, DefinitionPath: binding.DefinitionPath, Revision: binding.Revision}
	return response
}
