package registry

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	ac "belluga-builder-viewer/artifact_catalog"
)

const (
	designSystemHistoryFetchDepth = 256
	designSystemHistoryMaxDepth   = 4096
	designSystemHistoryMaxPage    = 100
)

type designSystemGitHistoryReader interface {
	ListDesignSystemHistory(context.Context, string, string, string, string, int) (DesignSystemHistoryResponse, error)
	ReadDesignSystemAsOf(context.Context, string, string, string, string, string) (ac.DesignSystemResponse, string, error)
}

func (v gitDesignSystemVerifier) ListDesignSystemHistory(ctx context.Context, projectID, companyID, repositoryID, branch string, limit int) (DesignSystemHistoryResponse, error) {
	if !validID(projectID) || !validID(companyID) || !validGitRepositoryIdentity(repositoryID) || !validGitBranch(branch) || limit < 1 || limit > designSystemHistoryMaxPage || v.repositoryURL == nil {
		return DesignSystemHistoryResponse{}, ErrInvalid
	}
	workspace, remote, ref, head, err := v.openHistory(ctx, repositoryID, branch)
	if err != nil {
		return DesignSystemHistoryResponse{}, err
	}
	defer removeGitWorkspace(workspace)
	output, err := v.git(ctx, workspace, false, "log", "--first-parent", "--format=%H%x00%ct", "--max-count="+strconv.Itoa(limit+1), ref, "--", designSystemRoot)
	if err != nil {
		return DesignSystemHistoryResponse{}, ErrUnavailable
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	revisions := make([]DesignSystemHistoryRevision, 0, min(limit, len(lines)))
	for index, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if index >= limit {
			break
		}
		fields := strings.Split(line, "\x00")
		if len(fields) != 2 || !validFullGitSHA(fields[0]) {
			return DesignSystemHistoryResponse{}, ErrUnavailable
		}
		unix, parseErr := strconv.ParseInt(fields[1], 10, 64)
		if parseErr != nil {
			return DesignSystemHistoryResponse{}, ErrUnavailable
		}
		revisions = append(revisions, DesignSystemHistoryRevision{SHA: fields[0], CommittedAt: time.Unix(unix, 0).UTC().Format(time.RFC3339)})
	}
	truncated := len(lines) > limit
	shallow, err := v.git(ctx, workspace, false, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return DesignSystemHistoryResponse{}, ErrUnavailable
	}
	// A bounded shallow scan cannot prove that older Design System changes do
	// not exist beyond its boundary, even when this page has few/no rows.
	truncated = truncated || strings.TrimSpace(string(shallow)) == "true"
	if err := v.confirmRemoteHead(ctx, remote, branch, head); err != nil {
		return DesignSystemHistoryResponse{}, err
	}
	return DesignSystemHistoryResponse{RepositoryID: repositoryID, Branch: branch, BranchHeadSHA: head, Revisions: revisions, Truncated: truncated}, nil
}

func (v gitDesignSystemVerifier) ReadDesignSystemAsOf(ctx context.Context, projectID, companyID, repositoryID, branch, revision string) (ac.DesignSystemResponse, string, error) {
	if !validID(projectID) || !validID(companyID) || !validGitRepositoryIdentity(repositoryID) || !validGitBranch(branch) || !validFullGitSHA(revision) || v.repositoryURL == nil {
		return ac.DesignSystemResponse{}, "", ErrInvalid
	}
	workspace, remote, ref, head, err := v.openHistory(ctx, repositoryID, branch)
	if err != nil {
		return ac.DesignSystemResponse{}, "", err
	}
	defer removeGitWorkspace(workspace)
	depth := designSystemHistoryFetchDepth
	for {
		if err := ctx.Err(); err != nil {
			return ac.DesignSystemResponse{}, "", ErrUnavailable
		}
		if _, err := v.git(ctx, workspace, false, "cat-file", "-e", revision+"^{commit}"); err == nil {
			if _, err := v.git(ctx, workspace, false, "merge-base", "--is-ancestor", revision, ref); err != nil {
				return ac.DesignSystemResponse{}, "", ErrNotFound
			}
			break
		}
		shallow, err := v.git(ctx, workspace, false, "rev-parse", "--is-shallow-repository")
		if err != nil {
			return ac.DesignSystemResponse{}, "", ErrUnavailable
		}
		if strings.TrimSpace(string(shallow)) == "false" {
			return ac.DesignSystemResponse{}, "", ErrNotFound
		}
		if depth >= designSystemHistoryMaxDepth {
			return ac.DesignSystemResponse{}, "", todoTeach("revision", "be reachable within the Registry's bounded branch-history scan", ErrUnavailable)
		}
		deepen := designSystemHistoryFetchDepth
		if depth+deepen > designSystemHistoryMaxDepth {
			deepen = designSystemHistoryMaxDepth - depth
		}
		if _, err := v.git(ctx, workspace, true, "fetch", "--quiet", "--no-tags", "--filter=blob:none", "--deepen="+strconv.Itoa(deepen), "origin", "+refs/heads/"+branch+":"+ref); err != nil {
			return ac.DesignSystemResponse{}, "", ErrUnavailable
		}
		depth += deepen
	}
	// History traversal intentionally carries trees/commits only. Read the exact
	// reachable commit through the full-blob credentialed reader so the shared
	// evaluator never triggers a credentialless promisor fetch.
	evaluation, _, err := v.ReadDesignSystem(ctx, projectID, companyID, repositoryID, revision, branch, "project", "")
	if err != nil {
		return ac.DesignSystemResponse{}, "", err
	}
	if err := v.confirmRemoteHead(ctx, remote, branch, head); err != nil {
		return ac.DesignSystemResponse{}, "", err
	}
	return evaluation, head, nil
}

func (v gitDesignSystemVerifier) openHistory(ctx context.Context, repositoryID, branch string) (workspace, remote, ref, head string, resultErr error) {
	workspace, err := newDesignSystemGitWorkspace(ctx, v)
	if err != nil {
		return "", "", "", "", ErrUnavailable
	}
	remote = v.repositoryURL(repositoryID)
	if remote == "" || strings.ContainsAny(remote, "\x00\r\n") {
		removeGitWorkspace(workspace)
		return "", "", "", "", ErrUnavailable
	}
	if _, err := v.git(ctx, workspace, false, "remote", "add", "origin", remote); err != nil {
		removeGitWorkspace(workspace)
		return "", "", "", "", ErrUnavailable
	}
	ref = "refs/remotes/origin/" + branch
	branchRef := "refs/heads/" + branch
	if _, err := v.git(ctx, workspace, true, "fetch", "--quiet", "--no-tags", "--filter=blob:none", "--depth="+strconv.Itoa(designSystemHistoryFetchDepth), "origin", "+"+branchRef+":"+ref); err != nil {
		removeGitWorkspace(workspace)
		return "", "", "", "", ErrUnavailable
	}
	value, err := v.git(ctx, workspace, false, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil || !validFullGitSHA(strings.TrimSpace(string(value))) {
		removeGitWorkspace(workspace)
		return "", "", "", "", ErrUnavailable
	}
	head = strings.TrimSpace(string(value))
	return workspace, remote, ref, head, nil
}

func newDesignSystemGitWorkspace(ctx context.Context, v gitDesignSystemVerifier) (string, error) {
	workspace, err := os.MkdirTemp("", ".builder-design-system-history-*")
	if err != nil {
		return "", err
	}
	if err := os.Chmod(workspace, 0o700); err != nil {
		_ = os.RemoveAll(workspace)
		return "", err
	}
	if _, err := v.git(ctx, workspace, false, "init", "--quiet"); err != nil {
		_ = os.RemoveAll(workspace)
		return "", ErrUnavailable
	}
	return workspace, nil
}

func removeGitWorkspace(path string) { _ = os.RemoveAll(path) }
