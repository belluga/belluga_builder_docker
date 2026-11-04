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
	"strings"
)

func (m *MongoStore) verifyMVPProduction(ctx context.Context, companyID string, todo TODORecord, request TODOActionRequest) (TODOProofReference, error) {
	if todo.ReleaseID == "" || todo.Stage != "Local-Implemented" || todo.StageVerification != "verified" || todo.VerifiedTransitionRef == nil || todo.VerifiedTransitionRef.VerificationState != "verified" || containsTODOString(todo.Qualifiers, "Blocked") {
		return TODOProofReference{}, todoTeach("stage/release_id", "complete only a verified, unblocked Local-Implemented TODO assigned to a Release", ErrConflict)
	}
	if request.LocalProof != nil || len(request.WaiverReferences) != 0 || len(request.ProofReferences) != 1 {
		return TODOProofReference{}, todoTeach("proof_refs", "provide one exact MVP deployment claim; local-run and waiver claims are not production proof", ErrConflict)
	}
	claim := request.ProofReferences[0]
	parts := strings.Split(claim.Reference, "@")
	if len(parts) != 2 || !validGitCommit(parts[1]) {
		return TODOProofReference{}, todoTeach("proof_refs.ref", "use deployment_id@published_mvp_sha from the successful production Deployment", ErrConflict)
	}
	deploymentID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || deploymentID <= 0 {
		return TODOProofReference{}, todoTeach("proof_refs.ref", "use a positive exact GitHub production Deployment ID", ErrConflict)
	}
	contentDigest := todo.Source.Digest
	if todo.Source.Kind == "mongo-content-revision" || usesImportedTODOContentStageProof(todo) {
		company, companyErr := m.company(ctx, companyID)
		if companyErr != nil {
			return TODOProofReference{}, companyErr
		}
		_, currentDigest, _, available, subjectErr := currentTODOContentSubject(ctx, m.client.Database(company.DatabaseName), todo)
		if subjectErr != nil {
			return TODOProofReference{}, subjectErr
		}
		if available && validSourceDigest(currentDigest) {
			contentDigest = currentDigest
		}
	}
	if claim.Kind != "mvp-deployment-claim" || claim.Source != "github-deployment" || claim.SourceDigest != contentDigest || claim.VerificationState != "unverified" {
		return TODOProofReference{}, todoTeach("proof_refs", "bind the MVP Deployment claim to this TODO's current immutable content digest", ErrConflict)
	}
	if m.proofReaderURL == "" || m.proofHTTPClient == nil || m.proofReaderToken == "" {
		return TODOProofReference{}, todoTeach("production_proof", "configure the private Reader proof route before Completed", ErrUnavailable)
	}
	builderCommits, pacedCommits, foundationCommits, err := mvpImplementationClaims(todo)
	if err != nil {
		return TODOProofReference{}, err
	}
	query := url.Values{
		"company_id": {companyID}, "project_id": {todo.ProjectID}, "todo_id": {todo.ID},
		"deployment_id": {parts[0]}, "root_sha": {parts[1]}, "module_path": {"project"}, "commit_sha": {parts[1]}, "project_mode": {"mvp"},
	}
	if todo.Source.Kind == "mongo-content-revision" {
		if repositoryID := mvpApprovedDockerRepository(todo); repositoryID != "" {
			query.Set("docker_repository_id", repositoryID)
		}
	}
	if len(builderCommits) > 0 {
		query.Set("project_commits", strings.Join(builderCommits, ","))
	}
	if len(pacedCommits) > 0 {
		query.Set("paced_commits", strings.Join(pacedCommits, ","))
	}
	if len(foundationCommits) > 0 {
		query.Set("foundation_commits", strings.Join(foundationCommits, ","))
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, m.proofReaderURL+"/internal/v1/proofs/production?"+query.Encode(), nil)
	if err != nil {
		return TODOProofReference{}, ErrUnavailable
	}
	httpRequest.Header.Set("Authorization", "Bearer "+m.proofReaderToken)
	response, err := m.proofHTTPClient.Do(httpRequest)
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
		return TODOProofReference{}, todoTeach("production_proof", "obtain verified MVP publication evidence from the private Reader", ErrConflict)
	}
	var verified remoteProductionResult
	if json.Unmarshal(data, &verified) != nil || verified.CompanyID != companyID || verified.ProjectID != todo.ProjectID || verified.TODOID != todo.ID || verified.DeploymentID != deploymentID || verified.RootSHA != parts[1] || verified.Environment != "production" || verified.ModulePath != "project" || verified.CommitSHA != parts[1] || verified.Outcome != "verified" || verified.ProjectMode != "mvp" || !validGitCommit(verified.ReaderSHA) || !equalCommitClaims(verified.ProjectCommits, builderCommits) || !equalCommitClaims(verified.FoundationCommits, foundationCommits) {
		return TODOProofReference{}, todoTeach("production_proof", "match every selected implementation commit and the exact served MVP Deployment", ErrConflict)
	}
	evidence, _ := json.Marshal(struct {
		Response      remoteProductionResult `json:"response"`
		ContentDigest string                 `json:"content_digest"`
		LocalDigest   string                 `json:"local_digest"`
		PACEDCommits  []string               `json:"paced_commits"`
	}{verified, contentDigest, todo.VerifiedTransitionRef.SourceDigest, pacedCommits})
	hash := sha256.Sum256(evidence)
	return TODOProofReference{Kind: "production-deployment", Source: "private-reader", Reference: claim.Reference, SourceDigest: "sha256:" + hex.EncodeToString(hash[:]), VerificationState: "verified"}, nil
}

func mvpApprovedDockerRepository(todo TODORecord) string {
	if todo.Source.Kind != "mongo-content-revision" {
		return ""
	}
	for _, scope := range todo.DiffExpectation {
		if scope.Repository == "Builder" && validGitRepositoryID(scope.RepositoryID) {
			return scope.RepositoryID
		}
	}
	return ""
}

func equalCommitClaims(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func mvpImplementationClaims(todo TODORecord) ([]string, []string, []string, error) {
	var builderCommits, pacedCommits, foundationCommits []string
	for _, claim := range todo.ImplementationCommits {
		if !validGitCommit(claim.Commit) {
			return nil, nil, nil, todoTeach("implementation_commits", "provide valid published implementation commit SHAs before MVP completion", ErrConflict)
		}
		switch strings.ToLower(strings.TrimSpace(claim.Repository)) {
		case "builder", "builder code", "belluga_builder_docker":
			builderCommits = append(builderCommits, claim.Commit)
		case "paced", "delphi-ai":
			pacedCommits = append(pacedCommits, claim.Commit)
		case "belluga_builder_foundation_documentation":
			foundationCommits = append(foundationCommits, claim.Commit)
		default:
			return nil, nil, nil, todoTeach("implementation_commits.repository", "use the registered Builder, PACED or Foundation source for this MVP release", ErrConflict)
		}
	}
	if len(builderCommits) == 0 && len(pacedCommits) == 0 && len(foundationCommits) == 0 && (todo.VerifiedTransitionRef == nil || todo.VerifiedTransitionRef.Kind != "local-run-accepted") {
		return nil, nil, nil, todoTeach("implementation_commits", "link attributable implementation commits, or retain a previously accepted local run for this historical TODO", ErrConflict)
	}
	return builderCommits, pacedCommits, foundationCommits, nil
}

const (
	booraV071DeploymentID = int64(7394797124)
	booraV071RootSHA      = "6f948189a57711c74cc03963bd5bcfe6faa4733e"
)

type remoteProductionResult struct {
	CompanyID         string   `json:"company_id"`
	ProjectID         string   `json:"project_id"`
	TODOID            string   `json:"todo_id"`
	DeploymentID      int64    `json:"deployment_id"`
	Environment       string   `json:"environment"`
	RootSHA           string   `json:"root_sha"`
	ModulePath        string   `json:"module_path"`
	CommitSHA         string   `json:"commit_sha"`
	Outcome           string   `json:"outcome"`
	ProjectMode       string   `json:"project_mode"`
	ProjectCommits    []string `json:"project_commits,omitempty"`
	FoundationCommits []string `json:"foundation_commits,omitempty"`
	ReaderSHA         string   `json:"reader_sha,omitempty"`
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
