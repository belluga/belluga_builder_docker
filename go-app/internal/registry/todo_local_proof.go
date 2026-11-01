package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"reflect"
	"regexp"
	"strings"
	"time"
)

var localRunIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type localProofMatrix struct {
	SchemaVersion   string   `json:"schema_version"`
	DefaultRequired []string `json:"default_required"`
	Commands        []struct {
		ID   string   `json:"id"`
		Argv []string `json:"argv"`
	} `json:"commands"`
	RequiredBy map[string][]string `json:"required_by"`
	digest     string
}

type localProofSnapshot struct {
	BuilderHead         string `json:"builder_head"`
	PACEDHead           string `json:"paced_head"`
	BuilderInputsDigest string `json:"builder_inputs_digest"`
	PACEDInputsDigest   string `json:"paced_inputs_digest"`
}

type localProofCommand struct {
	ID       string `json:"id"`
	ExitCode int    `json:"exit_code"`
}

type LocalProofResult struct {
	SchemaVersion          string                       `json:"schema_version"`
	TODOID                 string                       `json:"todo_id"`
	RunID                  string                       `json:"run_id"`
	MatrixDigest           string                       `json:"matrix_digest"`
	StartedAt              string                       `json:"started_at"`
	FinishedAt             string                       `json:"finished_at"`
	Before                 localProofSnapshot           `json:"before"`
	After                  localProofSnapshot           `json:"after"`
	OutsideUntrackedBefore map[string]map[string]string `json:"outside_untracked_before"`
	OutsideUntrackedAfter  map[string]map[string]string `json:"outside_untracked_after"`
	Commands               []localProofCommand          `json:"commands"`
	ResultDigest           string                       `json:"result_digest"`
}

type OwnerAcceptance struct {
	RunID        string `json:"run_id"`
	ResultDigest string `json:"result_digest"`
	AcceptedBy   string `json:"accepted_by"`
}

func sourceHash(data []byte) string {
	hash := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(hash[:])
}

func (m *MongoStore) ConfigureLocalMatrix(path string) error {
	if path == "" {
		return errors.New("missing matrix path")
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 || len(data) > 32<<10 {
		return errors.New("local proof matrix unavailable")
	}
	var matrix localProofMatrix
	if json.Unmarshal(data, &matrix) != nil || matrix.SchemaVersion != "builder-local-proof-v1" || len(matrix.Commands) == 0 || len(matrix.DefaultRequired) == 0 || len(matrix.RequiredBy) != 3 {
		return errors.New("local proof matrix invalid")
	}
	commandsByID := make(map[string]bool, len(matrix.Commands))
	for _, command := range matrix.Commands {
		if command.ID == "" || len(command.Argv) == 0 || commandsByID[command.ID] {
			return errors.New("local proof matrix has invalid command")
		}
		commandsByID[command.ID] = true
	}
	for _, id := range matrix.DefaultRequired {
		if !commandsByID[id] {
			return errors.New("local proof matrix has unknown default command")
		}
	}
	for id, commands := range matrix.RequiredBy {
		if id != "builder-company-tenant-mongo-registry" && id != "builder-mongo-v0-7-1-todo-migration" && id != "builder-mongo-selected-todo-stage-verification" {
			return errors.New("local proof matrix has unapproved TODO")
		}
		if len(commands) == 0 {
			return errors.New("local proof matrix has empty requirement")
		}
		for _, command := range commands {
			if !commandsByID[command] {
				return errors.New("local proof matrix has unknown required command")
			}
		}
	}
	matrix.digest = sourceHash(data)
	m.localMatrix = &matrix
	return nil
}

func (m *MongoStore) validateLocalProof(todoID string, result *LocalProofResult, acceptance *OwnerAcceptance) (TODOProofReference, error) {
	if m.localMatrix == nil || result == nil || acceptance == nil {
		return TODOProofReference{}, todoTeach("local_proof/owner_acceptance", "provide the fixed PACED local result and separate owner acceptance for this TODO", ErrConflict)
	}
	required := m.localMatrix.RequiredBy[todoID]
	if len(required) == 0 {
		required = m.localMatrix.DefaultRequired
	}
	if result.SchemaVersion != "builder-local-proof-result-v1" || result.TODOID != todoID || !localRunIDPattern.MatchString(result.RunID) || result.MatrixDigest != m.localMatrix.digest || len(result.Commands) != len(required) {
		return TODOProofReference{}, todoTeach("local_proof", "match this TODO and the current fixed command matrix", ErrConflict)
	}
	for i, id := range required {
		if result.Commands[i].ID != id || result.Commands[i].ExitCode != 0 {
			return TODOProofReference{}, todoTeach("local_proof.commands", "pass every fixed command for this TODO in matrix order", ErrConflict)
		}
	}
	if result.Before != result.After || !validSourceDigest(result.Before.BuilderInputsDigest) || !validSourceDigest(result.Before.PACEDInputsDigest) || len(result.Before.BuilderHead) != 40 || len(result.Before.PACEDHead) != 40 {
		return TODOProofReference{}, todoTeach("local_proof.inputs", "show unchanged Builder/PACED heads and approved-path content during the run", ErrConflict)
	}
	if len(result.OutsideUntrackedBefore) != 2 || result.OutsideUntrackedBefore["builder"] == nil || result.OutsideUntrackedBefore["paced"] == nil || !reflect.DeepEqual(result.OutsideUntrackedBefore, result.OutsideUntrackedAfter) {
		return TODOProofReference{}, todoTeach("local_proof.outside_untracked", "leave outside-scope untracked files unchanged during the run", ErrConflict)
	}
	started, startErr := time.Parse(time.RFC3339Nano, result.StartedAt)
	finished, finishErr := time.Parse(time.RFC3339Nano, result.FinishedAt)
	if startErr != nil || finishErr != nil || finished.Before(started) || finished.Sub(started) > 2*time.Hour || time.Since(finished) > 24*time.Hour || finished.After(time.Now().Add(time.Minute)) {
		return TODOProofReference{}, todoTeach("local_proof.time", "provide a recent bounded real run time", ErrConflict)
	}
	data, err := json.Marshal(result)
	if err != nil {
		return TODOProofReference{}, ErrInvalid
	}
	var values map[string]any
	if json.Unmarshal(data, &values) != nil {
		return TODOProofReference{}, ErrInvalid
	}
	delete(values, "result_digest")
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(values) != nil {
		return TODOProofReference{}, ErrInvalid
	}
	canonical := bytes.TrimSpace(encoded.Bytes())
	if result.ResultDigest != sourceHash(canonical) || acceptance.RunID != result.RunID || acceptance.ResultDigest != result.ResultDigest || acceptance.AcceptedBy == "" {
		return TODOProofReference{}, todoTeach("owner_acceptance", "bind explicit owner acceptance to this exact run ID and result digest", ErrConflict)
	}
	return TODOProofReference{Kind: "local-run-accepted", Source: "paced-local-runner", Reference: result.RunID, SourceDigest: result.ResultDigest, VerificationState: "verified"}, nil
}

type remoteCommitResult struct {
	CompanyID  string   `json:"company_id"`
	ProjectID  string   `json:"project_id"`
	Repository string   `json:"repository"`
	Branch     string   `json:"branch"`
	CommitSHA  string   `json:"commit_sha"`
	RemoteHead string   `json:"remote_head"`
	ParentSHA  string   `json:"parent_sha"`
	Paths      []string `json:"paths"`
	Outcome    string   `json:"outcome"`
}

// The approved scope, not the current checkout diff, decides attribution.
// Foundation-only Markdown is not an implementation repository.
func (m *MongoStore) verifyRemoteLocalDelivery(ctx context.Context, companyID, foundationRepositoryID string, todo TODORecord, contentDigest string, local TODOProofReference) (TODOProofReference, error) {
	if m.proofReaderURL == "" || m.proofReaderToken == "" || m.proofHTTPClient == nil {
		return TODOProofReference{}, todoTeach("remote_proof", "configure the private Project Reader before Local-Implemented", ErrUnavailable)
	}
	parts := strings.SplitN(foundationRepositoryID, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return TODOProofReference{}, todoTeach("remote_proof", "use the registered Project Foundation repository owner", ErrConflict)
	}
	var verified []remoteCommitResult
	for _, scope := range todo.DiffExpectation {
		if strings.HasSuffix(scope.Repository, "_foundation_documentation") {
			continue
		}
		branch, ok := implementationBranch(todo.ProjectID, todo.ReleaseID, scope.Repository)
		if !ok {
			return TODOProofReference{}, todoTeach("diff_expectation.repository", "configure the remote release branch for this implementing repository", ErrConflict)
		}
		claims := 0
		for _, claim := range todo.ImplementationCommits {
			if claim.Repository != scope.Repository {
				continue
			}
			claims++
			if !validGitCommit(claim.Commit) || claim.SourceDigest != contentDigest || claim.VerificationState != "unverified" {
				return TODOProofReference{}, todoTeach("implementation_commits", "bind each full commit SHA to the approved current TODO content revision", ErrConflict)
			}
			repository := parts[0] + "/" + scope.Repository
			result, err := m.readRemoteCommit(ctx, companyID, todo.ProjectID, repository, branch, claim.Commit)
			if err != nil {
				return TODOProofReference{}, err
			}
			for _, changed := range result.Paths {
				if !approvedCommitPath(changed, scope) {
					return TODOProofReference{}, todoTeach("implementation_commits.paths", "identify attributable commits whose changed paths fit this TODO's approved repository scope", ErrConflict)
				}
			}
			verified = append(verified, result)
		}
		if claims == 0 {
			return TODOProofReference{}, todoTeach("implementation_commits", "record at least one pushed attributable commit for each implementing repository", ErrConflict)
		}
	}
	if len(verified) == 0 || len(verified) != len(todo.ImplementationCommits) {
		return TODOProofReference{}, todoTeach("implementation_commits", "record only commits from the approved implementing repositories", ErrConflict)
	}
	data, err := json.Marshal(struct {
		Local  TODOProofReference   `json:"local"`
		Remote []remoteCommitResult `json:"remote"`
	}{local, verified})
	if err != nil {
		return TODOProofReference{}, ErrInvalid
	}
	claims := make([]string, 0, len(verified)+1)
	claims = append(claims, "run="+local.Reference)
	for _, result := range verified {
		claims = append(claims, result.Repository+"@"+result.Branch+":"+result.CommitSHA+"/parent="+result.ParentSHA+"/head="+result.RemoteHead)
	}
	return TODOProofReference{Kind: "remote-local-delivery", Source: "private-reader-and-paced-runner", Reference: strings.Join(claims, ";"), SourceDigest: sourceHash(data), VerificationState: "verified"}, nil
}

func implementationBranch(projectID, releaseID, repository string) (string, bool) {
	if projectID == "builder" {
		if repository == "delphi-ai" {
			return "builder", true
		}
		if repository == "belluga_builder_docker" && strings.HasPrefix(releaseID, "r-") {
			return releaseID + "-rc", true
		}
	}
	return "", false
}

func approvedCommitPath(changed string, scope TODORepositoryScope) bool {
	if changed == "" || strings.HasPrefix(changed, "/") || strings.Contains(changed, "..") || strings.Contains(changed, "\\") {
		return false
	}
	for _, forbidden := range scope.NotExpectedPaths {
		if matchesCommitPath(forbidden, changed) {
			return false
		}
	}
	for _, allowed := range scope.ExpectedPaths {
		if matchesCommitPath(allowed, changed) {
			return true
		}
	}
	return false
}

func matchesCommitPath(pattern, changed string) bool {
	if strings.HasSuffix(pattern, "/**") {
		return strings.HasPrefix(changed, strings.TrimSuffix(pattern, "/**")+"/")
	}
	matched, err := path.Match(pattern, changed)
	return err == nil && matched
}

func (m *MongoStore) readRemoteCommit(ctx context.Context, companyID, projectID, repository, branch, sha string) (remoteCommitResult, error) {
	q := url.Values{"company_id": {companyID}, "project_id": {projectID}, "repository": {repository}, "branch": {branch}, "commit_sha": {sha}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.proofReaderURL+"/internal/v1/proofs/commit?"+q.Encode(), nil)
	if err != nil {
		return remoteCommitResult{}, ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+m.proofReaderToken)
	response, err := m.proofHTTPClient.Do(req)
	if err != nil {
		return remoteCommitResult{}, todoTeach("remote_proof", "retry when the private Reader can verify the remote release branch", ErrUnavailable)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 32<<10))
	if err != nil || len(data) == 0 || len(data) >= 32<<10 {
		return remoteCommitResult{}, ErrUnavailable
	}
	if response.StatusCode != http.StatusOK {
		var problem struct {
			Teach string `json:"teach"`
		}
		if json.Unmarshal(data, &problem) == nil && problem.Teach != "" {
			return remoteCommitResult{}, todoTeach("remote_proof", problem.Teach, ErrConflict)
		}
		return remoteCommitResult{}, todoTeach("remote_proof", "verify the commit on the remote release branch", ErrConflict)
	}
	var result remoteCommitResult
	if json.Unmarshal(data, &result) != nil || result.CompanyID != companyID || result.ProjectID != projectID || result.Repository != repository || result.Branch != branch || result.CommitSHA != sha || !validGitCommit(result.RemoteHead) || !validGitCommit(result.ParentSHA) || len(result.Paths) == 0 || result.Outcome != "verified" {
		return remoteCommitResult{}, todoTeach("remote_proof", "match the exact repository, branch and attributable commit returned by the private Reader", ErrConflict)
	}
	return result, nil
}
