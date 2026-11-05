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

func (m *MongoStore) verifyMVPProduction(ctx context.Context, companyID string, todo TODORecord, request TODOActionRequest, projectDockerRepository string) (TODOProofReference, error) {
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
	if usesTypedMongoAuthority(todo) || usesImportedTODOContentStageProof(todo) {
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
	if todo.Content != nil && todo.Content.SchemaVersion == todoContentSchemaVersion {
		if todo.VerifiedTransitionRef.Kind != "remote-local-delivery" || todo.VerifiedTransitionRef.Source != "private-reader-and-paced-runner" || len(todo.ImplementationCommits) == 0 {
			return TODOProofReference{}, todoTeach("local_proof", "require a current v2 local delivery verified by the private Reader and PACED runner", ErrConflict)
		}
		for _, implementation := range todo.ImplementationCommits {
			if implementation.SourceDigest != contentDigest {
				return TODOProofReference{}, todoTeach("implementation_commits.source_digest", "bind every locally verified implementation commit to the current Mongo content digest", ErrConflict)
			}
		}
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
	if usesTypedMongoAuthority(todo) {
		repositoryID := mvpApprovedDockerRepository(todo)
		if repositoryID == "" && validGitRepositoryID(projectDockerRepository) {
			repositoryID = projectDockerRepository
		}
		if repositoryID != "" {
			query.Set("docker_repository_id", repositoryID)
		} else if todo.Content != nil && todo.Content.SchemaVersion == todoContentSchemaVersion {
			return TODOProofReference{}, todoTeach("project.docker_repository_id", "register a Docker deployment repository or include an approved Builder implementation scope before production proof", ErrConflict)
		}
		if todo.Content != nil && todo.Content.SchemaVersion == todoContentSchemaVersion {
			query.Set("todo_authority", "mongo-v2")
		}
	}
	if len(builderCommits) > 0 {
		query.Set("project_commits", strings.Join(builderCommits, ","))
	}
	if len(pacedCommits) > 0 {
		query.Set("paced_commits", strings.Join(pacedCommits, ","))
		if todo.Content != nil && todo.Content.SchemaVersion == todoContentSchemaVersion {
			repository, branch, identityErr := mvpPACEDRepository(todo, pacedCommits)
			if identityErr != nil {
				return TODOProofReference{}, identityErr
			}
			query.Set("paced_repository_id", repository)
			query.Set("paced_branch", branch)
		}
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
	if json.Unmarshal(data, &verified) != nil || verified.CompanyID != companyID || verified.ProjectID != todo.ProjectID || verified.TODOID != todo.ID || verified.DeploymentID != deploymentID || verified.RootSHA != parts[1] || verified.Environment != "production" || verified.ModulePath != "project" || verified.CommitSHA != parts[1] || verified.Outcome != "verified" || verified.ProjectMode != "mvp" || !validGitCommit(verified.ReaderSHA) || !equalCommitClaims(verified.ProjectCommits, builderCommits) || !equalCommitClaims(verified.PACEDCommits, pacedCommits) || !equalCommitClaims(verified.FoundationCommits, foundationCommits) {
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

// Older v2 contracts can lack repository metadata even though their local delivery
// already verified the exact remote PACED identity for every implementation commit.
// Reuse only that verified identity; never infer it from a label or a current branch.
func mvpPACEDRepository(todo TODORecord, commits []string) (string, string, error) {
	for _, scope := range todo.DiffExpectation {
		if scope.Repository != "PACED" && scope.Repository != "delphi-ai" {
			continue
		}
		if validGitRepositoryID(scope.RepositoryID) && validDeliveryBranch(scope.DeliveryBranch) {
			return scope.RepositoryID, scope.DeliveryBranch, nil
		}
		if scope.RepositoryID != "" || scope.DeliveryBranch != "" {
			break
		}
		proof := todo.VerifiedTransitionRef
		if proof == nil || proof.Kind != "remote-local-delivery" || proof.Source != "private-reader-and-paced-runner" || proof.VerificationState != "verified" {
			break
		}
		var repository, branch string
		for _, commit := range commits {
			found := false
			for _, item := range strings.Split(proof.Reference, ";") {
				identity, detail, ok := strings.Cut(item, ":")
				if !ok || !strings.HasPrefix(detail, commit+"/parent=") {
					continue
				}
				candidateRepository, candidateBranch, ok := strings.Cut(identity, "@")
				if !ok || !validGitRepositoryID(candidateRepository) || !validDeliveryBranch(candidateBranch) || (found && (repository != candidateRepository || branch != candidateBranch)) {
					return "", "", todoTeach("local_proof", "use one unambiguous previously verified PACED repository and branch for every implementation commit", ErrConflict)
				}
				if repository != "" && (repository != candidateRepository || branch != candidateBranch) {
					return "", "", todoTeach("local_proof", "match all PACED implementation commits to the same verified repository and branch", ErrConflict)
				}
				repository, branch, found = candidateRepository, candidateBranch, true
			}
			if !found {
				return "", "", todoTeach("local_proof", "match every PACED implementation commit to its previously verified remote delivery", ErrConflict)
			}
		}
		if repository != "" {
			return repository, branch, nil
		}
		break
	}
	return "", "", todoTeach("diff_expectation.repository_id/delivery_branch", "provide the approved PACED repository and branch or retain exact previously verified remote delivery for every PACED commit", ErrConflict)
}

func mvpApprovedDockerRepository(todo TODORecord) string {
	if !usesTypedMongoAuthority(todo) {
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
	if todo.Content != nil && todo.Content.SchemaVersion == todoContentSchemaVersion && len(foundationCommits) > 0 {
		return nil, nil, nil, todoTeach("implementation_commits.repository", "omit Foundation commits from current v2 TODO evidence", ErrConflict)
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
	PACEDCommits      []string `json:"paced_commits,omitempty"`
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
