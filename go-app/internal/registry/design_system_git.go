package registry

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"

	ac "belluga-builder-viewer/artifact_catalog"
)

const (
	designSystemRoot           = "design/system"
	designSystemDefinitionPath = designSystemRoot + "/design-system.json"
)

// gitDesignSystemVerifier proves only Git custody, commit identity, manifest
// structure, and inventory bytes. Full Design System semantic evaluation stays
// with artifact_catalog and is deliberately not duplicated here.
type gitDesignSystemVerifier struct {
	repositoryURL func(string) string
	gitExecutable string
	readCache     *designSystemReadCache
}

func newGitDesignSystemVerifier() designSystemRemoteVerifier {
	return gitDesignSystemVerifier{repositoryURL: func(id string) string { return "https://github.com/" + id + ".git" }, readCache: newDesignSystemReadCache()}
}

func (v gitDesignSystemVerifier) VerifyDesignSystemMutation(ctx context.Context, companyID, projectID string, binding Binding, intent DesignSystemMutationIntent) (designSystemRemoteState, error) {
	if !validID(companyID) || !validID(projectID) || !validBinding(binding) || !validGitRepositoryIdentity(binding.RepositoryID) || !validGitBranch(binding.PublicationBranch) ||
		!validDesignSystemMutationIntent(intent) || intent.PreparedCommitSHA == intent.ExpectedHead || v.repositoryURL == nil {
		return designSystemRemoteState{}, ErrUnavailable
	}
	workspace, err := os.MkdirTemp("", ".builder-design-system-verify-*")
	if err != nil {
		return designSystemRemoteState{}, ErrUnavailable
	}
	defer os.RemoveAll(workspace)
	if err := os.Chmod(workspace, 0o700); err != nil {
		return designSystemRemoteState{}, ErrUnavailable
	}
	if _, err := v.git(ctx, workspace, false, "init", "--quiet"); err != nil {
		return designSystemRemoteState{}, err
	}
	remote := v.repositoryURL(binding.RepositoryID)
	if remote == "" || strings.ContainsAny(remote, "\x00\r\n") {
		return designSystemRemoteState{}, ErrUnavailable
	}
	if _, err := v.git(ctx, workspace, false, "remote", "add", "origin", remote); err != nil {
		return designSystemRemoteState{}, err
	}
	branchRef := "refs/heads/" + binding.PublicationBranch
	remoteRef := "refs/remotes/origin/" + binding.PublicationBranch
	if _, err := v.git(ctx, workspace, true, "fetch", "--quiet", "--no-tags", "origin", "+"+branchRef+":"+remoteRef); err != nil {
		return designSystemRemoteState{}, ErrUnavailable
	}
	headBytes, err := v.git(ctx, workspace, false, "rev-parse", "--verify", remoteRef+"^{commit}")
	if err != nil {
		return designSystemRemoteState{}, ErrUnavailable
	}
	remoteHead := strings.TrimSpace(string(headBytes))
	if !validFullGitSHA(remoteHead) {
		return designSystemRemoteState{}, ErrUnavailable
	}
	state := designSystemRemoteState{RemoteHead: remoteHead}
	if remoteHead == intent.ExpectedHead {
		if err := v.confirmRemoteHead(ctx, remote, binding.PublicationBranch, remoteHead); err != nil {
			return designSystemRemoteState{}, err
		}
		// The exact unchanged remote ref is conclusive no-push evidence.
		state.ReachabilityVerified = true
		return state, nil
	}
	if _, err := v.git(ctx, workspace, false, "merge-base", "--is-ancestor", intent.PreparedCommitSHA, remoteHead); err != nil {
		if confirmErr := v.confirmRemoteHead(ctx, remote, binding.PublicationBranch, remoteHead); confirmErr != nil {
			return designSystemRemoteState{}, confirmErr
		}
		state.ReachabilityVerified = true
		return state, nil
	}
	state.Reachable = true
	parents, err := v.git(ctx, workspace, false, "rev-list", "--parents", "-n", "1", intent.PreparedCommitSHA)
	if err != nil {
		return designSystemRemoteState{}, ErrUnavailable
	}
	parentFields := strings.Fields(string(parents))
	if len(parentFields) != 2 || parentFields[0] != intent.PreparedCommitSHA || parentFields[1] != intent.ExpectedHead {
		return designSystemRemoteState{}, ErrUnavailable
	}
	treeBytes, err := v.git(ctx, workspace, false, "rev-parse", "--verify", intent.PreparedCommitSHA+"^{tree}")
	if err != nil {
		return designSystemRemoteState{}, ErrUnavailable
	}
	state.CommitSHA = intent.PreparedCommitSHA
	state.ParentSHA = strings.TrimSpace(parentFields[1])
	state.TreeSHA = strings.TrimSpace(string(treeBytes))
	if !validFullGitSHA(state.TreeSHA) || state.TreeSHA != intent.CandidateTreeSHA {
		return designSystemRemoteState{}, ErrUnavailable
	}
	message, err := v.git(ctx, workspace, false, "show", "-s", "--format=%B", intent.PreparedCommitSHA)
	if err != nil || !hasExactGitTrailer(string(message), "Builder-Operation-ID: "+intent.OperationID) {
		return designSystemRemoteState{}, ErrUnavailable
	}
	if intent.Operation == "delete" {
		// A delete is valid only when the entire managed subtree is absent from
		// the exact published tree; an incomplete/partial deletion is not enough.
		managedTree, err := v.git(ctx, workspace, false, "ls-tree", "-z", "--full-tree", intent.PreparedCommitSHA, "--", ":(literal)"+designSystemRoot)
		if err != nil || len(managedTree) != 0 {
			return designSystemRemoteState{}, ErrUnavailable
		}
		state.InventoryDigest, err = ac.InventoryDigest(binding.RepositoryID, nil)
		if err != nil {
			return designSystemRemoteState{}, ErrUnavailable
		}
	} else {
		selectedRevision := intent.PreparedCommitSHA
		source, err := ac.NewGitTreeContext(ctx, workspace, selectedRevision)
		if err != nil {
			return designSystemRemoteState{}, ErrUnavailable
		}
		evaluation := ac.EvaluateDesignSystem(source, projectID, companyID, "project", ac.SourceBinding{
			RepositoryID: binding.RepositoryID, Revision: &selectedRevision, DefinitionPath: designSystemDefinitionPath,
		})
		if evaluation.Outcome != "go" || evaluation.DesignSystemValidation != "valid" || evaluation.InventoryDigest == nil || len(evaluation.Items) != 1 {
			return designSystemRemoteState{}, ErrUnavailable
		}
		state.InventoryDigest = *evaluation.InventoryDigest
	}
	if err := v.confirmRemoteHead(ctx, remote, binding.PublicationBranch, remoteHead); err != nil {
		return designSystemRemoteState{}, err
	}
	state.ReachabilityVerified = true
	return state, nil
}

// ReadDesignSystem evaluates one immutable remote commit. The revision must be
// supplied by Mongo selection or the verified private source map; this method
// never resolves a branch head or falls back to another source.
func (v gitDesignSystemVerifier) ReadDesignSystem(ctx context.Context, projectID, companyID, repositoryID, revision, branch, level, assetPath string) (ac.DesignSystemResponse, []byte, error) {
	if !validID(projectID) || !validID(companyID) || !validGitRepositoryIdentity(repositoryID) || !validFullGitSHA(revision) || v.repositoryURL == nil || level != "project" && level != "company" && level != "default" {
		return ac.DesignSystemResponse{}, nil, ErrUnavailable
	}
	cacheKey := designSystemReadCacheKey(projectID, companyID, repositoryID, revision, level)
	if cached, ok := v.readCache.get(cacheKey); ok {
		if assetPath == "" {
			return cached.evaluation, nil, nil
		}
		if !designSystemAssetRegistered(cached.evaluation, assetPath) {
			return cached.evaluation, nil, ErrNotFound
		}
		data, ok := cached.assets[assetPath]
		if !ok {
			return ac.DesignSystemResponse{}, nil, ErrUnavailable
		}
		return cached.evaluation, append([]byte(nil), data...), nil
	}
	workspace, err := os.MkdirTemp("", ".builder-design-system-read-*")
	if err != nil {
		return ac.DesignSystemResponse{}, nil, ErrUnavailable
	}
	defer os.RemoveAll(workspace)
	if err := os.Chmod(workspace, 0o700); err != nil {
		return ac.DesignSystemResponse{}, nil, ErrUnavailable
	}
	if _, err := v.git(ctx, workspace, false, "init", "--quiet"); err != nil {
		return ac.DesignSystemResponse{}, nil, err
	}
	remote := v.repositoryURL(repositoryID)
	if remote == "" || strings.ContainsAny(remote, "\x00\r\n") {
		return ac.DesignSystemResponse{}, nil, ErrUnavailable
	}
	if _, err := v.git(ctx, workspace, false, "remote", "add", "origin", remote); err != nil {
		return ac.DesignSystemResponse{}, nil, err
	}
	// Fetch the exact shallow commit with its blobs. The evaluator reads blobs
	// after fetch using sanitized local Git commands; a partial clone would
	// trigger a lazy remote fetch without the private credential header.
	if _, err := v.git(ctx, workspace, true, "fetch", "--quiet", "--no-tags", "--depth=1", "origin", revision); err != nil {
		return ac.DesignSystemResponse{}, nil, ErrUnavailable
	}
	actual, err := v.git(ctx, workspace, false, "rev-parse", "--verify", revision+"^{commit}")
	if err != nil || strings.TrimSpace(string(actual)) != revision {
		return ac.DesignSystemResponse{}, nil, ErrUnavailable
	}
	source, err := ac.NewGitTreeContext(ctx, workspace, revision)
	if err != nil {
		return ac.DesignSystemResponse{}, nil, ErrUnavailable
	}
	revisionRef := revision
	evaluation := ac.EvaluateDesignSystem(source, projectID, companyID, level, ac.SourceBinding{
		RepositoryID: repositoryID, Revision: &revisionRef, DefinitionPath: designSystemDefinitionPath,
	})
	if evaluation.Outcome != "go" || evaluation.DesignSystemValidation != "valid" || len(evaluation.Items) != 1 || evaluation.InventoryDigest == nil {
		return evaluation, nil, nil
	}
	assetData, cacheable := readAllDesignSystemAssets(source, evaluation)
	if cacheable {
		v.readCache.put(cacheKey, evaluation, assetData)
	}
	if assetPath == "" {
		return evaluation, nil, nil
	}
	if !designSystemAssetRegistered(evaluation, assetPath) {
		return evaluation, nil, ErrNotFound
	}
	if data, ok := assetData[assetPath]; ok {
		return evaluation, data, nil
	}
	data, mode, err := source.Read(designSystemRoot+"/"+assetPath, ac.MaxFileBytes)
	if err != nil || mode != ac.ModeRegular {
		return evaluation, nil, ErrUnavailable
	}
	return evaluation, data, nil
}

func designSystemAssetRegistered(evaluation ac.DesignSystemResponse, assetPath string) bool {
	for _, item := range evaluation.Items {
		for _, declared := range item.Assets {
			if declared == assetPath {
				return true
			}
		}
	}
	return false
}

func readAllDesignSystemAssets(source ac.Source, evaluation ac.DesignSystemResponse) (map[string][]byte, bool) {
	assets := make(map[string][]byte)
	var total int64
	for _, item := range evaluation.Items {
		for _, path := range item.Assets {
			data, mode, err := source.Read(designSystemRoot+"/"+path, ac.MaxFileBytes)
			if err != nil || mode != ac.ModeRegular || int64(len(data)) > ac.MaxTotalBytes-total {
				return nil, false
			}
			total += int64(len(data))
			assets[path] = data
		}
	}
	return assets, true
}

func (v gitDesignSystemVerifier) confirmRemoteHead(ctx context.Context, remote, branch, expected string) error {
	output, err := v.git(ctx, "", true, "ls-remote", "--exit-code", remote, "refs/heads/"+branch)
	if err != nil {
		return ErrUnavailable
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 || fields[1] != "refs/heads/"+branch || fields[0] != expected || !validFullGitSHA(fields[0]) {
		return ErrUnavailable
	}
	return nil
}

func hasExactGitTrailer(message, trailer string) bool {
	for _, line := range strings.Split(message, "\n") {
		if strings.TrimSuffix(line, "\r") == trailer {
			return true
		}
	}
	return false
}

func (v gitDesignSystemVerifier) git(ctx context.Context, dir string, credentialed bool, args ...string) ([]byte, error) {
	return v.gitLimited(ctx, dir, 8<<20, credentialed, args...)
}

func (v gitDesignSystemVerifier) gitLimited(ctx context.Context, dir string, limit int, credentialed bool, args ...string) ([]byte, error) {
	executable := v.gitExecutable
	if executable == "" {
		executable = "git"
	}
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = dir
	command.Env = designSystemGitEnvironment(credentialed)
	var output limitedGitBuffer
	output.limit = limit
	command.Stdout = &output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil || output.exceeded {
		return nil, ErrUnavailable
	}
	return output.buffer.Bytes(), nil
}

type limitedGitBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (b *limitedGitBuffer) Write(value []byte) (int, error) {
	if len(value) > b.limit-b.buffer.Len() {
		b.exceeded = true
		return 0, errors.New("Git output limit exceeded")
	}
	return b.buffer.Write(value)
}

func designSystemGitEnvironment(credentialed bool) []string {
	filtered := make([]string, 0, len(os.Environ())+7)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") || key == "SSH_ASKPASS" || key == "DELPHI_GITHUB_READ_TOKEN" {
			continue
		}
		filtered = append(filtered, entry)
	}
	configCount := 1
	filtered = append(filtered, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_ATTR_NOSYSTEM=1", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=/dev/null")
	if !credentialed {
		filtered = append(filtered, "GIT_CONFIG_COUNT=1")
		return filtered
	}
	token := os.Getenv("DELPHI_GITHUB_READ_TOKEN")
	if token == "" {
		filtered = append(filtered, "GIT_CONFIG_COUNT=1")
		return filtered
	}
	auth := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	configCount++
	filtered = append(filtered, "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_1=http.https://github.com/.extraheader", "GIT_CONFIG_VALUE_1=AUTHORIZATION: basic "+auth)
	return filtered
}
