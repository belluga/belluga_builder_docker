package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

const (
	booraV071DeploymentID = int64(7394797124)
	booraV071RootSHA      = "6f948189a57711c74cc03963bd5bcfe6faa4733e"
)

type remoteProductionResult struct {
	CompanyID    string `json:"company_id"`
	ProjectID    string `json:"project_id"`
	TODOID       string `json:"todo_id"`
	DeploymentID int64  `json:"deployment_id"`
	Environment  string `json:"environment"`
	RootSHA      string `json:"root_sha"`
	ModulePath   string `json:"module_path"`
	CommitSHA    string `json:"commit_sha"`
	Outcome      string `json:"outcome"`
	ProjectMode  string `json:"project_mode"`
}

func (m *MongoStore) verifyProductionCommit(ctx context.Context, companyID string, todo TODORecord, modulePath string, claim TODOCommitReference) (TODOProofReference, error) {
	if m.proofReaderURL == "" || m.proofHTTPClient == nil || m.proofReaderToken == "" {
		return TODOProofReference{}, todoTeach("production_proof", "configure the private Reader proof route before Completed", ErrUnavailable)
	}
	query := url.Values{
		"company_id": {companyID}, "project_id": {todo.ProjectID}, "todo_id": {todo.ID},
		"deployment_id": {strconv.FormatInt(booraV071DeploymentID, 10)}, "root_sha": {booraV071RootSHA},
		"module_path": {modulePath}, "commit_sha": {claim.Commit},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, m.proofReaderURL+"/internal/v1/proofs/production?"+query.Encode(), nil)
	if err != nil {
		return TODOProofReference{}, ErrUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+m.proofReaderToken)
	response, err := m.proofHTTPClient.Do(request)
	if err != nil {
		return TODOProofReference{}, todoTeach("production_proof", "retry when the private Reader is available", ErrUnavailable)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 16<<10))
	if err != nil || len(data) == 0 {
		return TODOProofReference{}, ErrUnavailable
	}
	if response.StatusCode != http.StatusOK {
		var problem struct {
			Teach string `json:"teach"`
		}
		if json.Unmarshal(data, &problem) == nil && problem.Teach != "" {
			return TODOProofReference{}, todoTeach("production_proof", problem.Teach, ErrConflict)
		}
		return TODOProofReference{}, todoTeach("production_proof", "obtain exact verified production Deployment evidence from the private Reader", ErrConflict)
	}
	var proof remoteProductionResult
	if json.Unmarshal(data, &proof) != nil || proof.CompanyID != companyID || proof.ProjectID != todo.ProjectID || proof.TODOID != todo.ID || proof.DeploymentID != booraV071DeploymentID || proof.Environment != "production" || proof.RootSHA != booraV071RootSHA || proof.ModulePath != modulePath || proof.CommitSHA != claim.Commit || proof.Outcome != "verified" || proof.ProjectMode != "complete" {
		return TODOProofReference{}, todoTeach("production_proof", "match the requested Project, TODO, commit and exact successful v0.7.1 Deployment", ErrConflict)
	}
	hash := sha256.Sum256(data)
	return TODOProofReference{Kind: "production-deployment", Source: "private-reader", Reference: strconv.FormatInt(proof.DeploymentID, 10) + "@" + proof.RootSHA, SourceDigest: "sha256:" + hex.EncodeToString(hash[:]), VerificationState: "verified"}, nil
}

func productionModulePath(todo TODORecord) (string, TODOCommitReference, error) {
	if len(todo.ImplementationCommits) != 1 {
		return "", TODOCommitReference{}, todoTeach("implementation_commits", "contain exactly one attributable implementation commit for this selected TODO", ErrConflict)
	}
	claim := todo.ImplementationCommits[0]
	if !validGitCommit(claim.Commit) || claim.SourceDigest != todo.Source.Digest || claim.VerificationState != "unverified" {
		return "", TODOCommitReference{}, todoTeach("implementation_commits", "bind the imported commit to this TODO's pinned source before production verification", ErrConflict)
	}
	switch claim.Repository {
	case "Flutter":
		return "flutter-app", claim, nil
	case "Laravel":
		return "laravel-app", claim, nil
	case "Docker":
		return "project", claim, nil
	default:
		return "", TODOCommitReference{}, errors.New("TEACH: implementation repository is not one of this selected Project's approved code sources")
	}
}
