package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
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
		if !verifiedCodeDeliveryIdentity(todo.VerifiedTransitionRef) || len(todo.ImplementationCommits) == 0 {
			return TODOProofReference{}, todoTeach("remote_proof", "require an exact verified historical local delivery or Reader-backed remote-code delivery", ErrConflict)
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
		repository, branch, identityErr := mvpPACEDRepository(todo, pacedCommits)
		if identityErr != nil {
			return TODOProofReference{}, identityErr
		}
		query.Set("paced_repository_id", repository)
		query.Set("paced_branch", branch)
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
	proof := todo.VerifiedTransitionRef
	if !verifiedCodeDeliveryIdentity(proof) || len(commits) == 0 {
		return "", "", todoTeach("local_proof", "production must use one verified local-delivery record for every PACED implementation commit", ErrConflict)
	}
	var scopedRepository string
	for _, scope := range todo.DiffExpectation {
		if scope.Repository != "PACED" && scope.Repository != "delphi-ai" {
			continue
		}
		if scope.RepositoryID == "" {
			continue
		}
		if !validGitRepositoryID(scope.RepositoryID) || scopedRepository != "" && scopedRepository != scope.RepositoryID {
			return "", "", todoTeach("diff_expectation.repository_id", "match one exact PACED repository identity when the TODO scope records it", ErrConflict)
		}
		scopedRepository = scope.RepositoryID
	}
	type identity struct{ repository, branch string }
	verified := make(map[string]identity, len(commits))
	requested := stringSliceSet(commits)
	items := strings.Split(proof.Reference, ";")
	if proof.Kind == "remote-local-delivery" {
		if len(items) < 2 || !strings.HasPrefix(items[0], "run=") || items[0] == "run=" {
			return "", "", todoTeach("local_proof", "preserve the exact historical run and repository tuples", ErrConflict)
		}
		items = items[1:]
	}
	if len(items) == 0 {
		return "", "", todoTeach("remote_proof", "include exact verified repository and commit tuples", ErrConflict)
	}
	for _, item := range items {
		if strings.HasPrefix(item, "repository_node_id=") || strings.HasPrefix(item, todoConstitutionPinPrefix) {
			continue
		}
		key, detail, ok := strings.Cut(item, ":")
		if !ok || key == "" || strings.HasPrefix(key, "run=") {
			return "", "", todoTeach("remote_proof", "use only exact repository/branch/commit tuples", ErrConflict)
		}
		repository, branch, ok := strings.Cut(key, "@")
		parts := strings.Split(detail, "/")
		if !ok || !validGitRepositoryID(repository) || !validDeliveryBranch(branch) || len(parts) != 3 || !validGitCommit(parts[0]) || !strings.HasPrefix(parts[1], "parent=") || !validGitCommit(strings.TrimPrefix(parts[1], "parent=")) || !strings.HasPrefix(parts[2], "head=") || !validGitCommit(strings.TrimPrefix(parts[2], "head=")) {
			return "", "", todoTeach("local_proof", "use only exact repository, branch and commit identities from the verified local-delivery record", ErrConflict)
		}
		commit := parts[0]
		if _, requested := requested[commit]; !requested {
			continue
		}
		candidate := identity{repository: repository, branch: branch}
		if _, exists := verified[commit]; exists {
			return "", "", todoTeach("local_proof", "match each PACED implementation commit to one unambiguous verified repository and branch", ErrConflict)
		}
		verified[commit] = candidate
	}
	var selected identity
	for _, commit := range commits {
		candidate, exists := verified[commit]
		if !exists || scopedRepository != "" && candidate.repository != scopedRepository {
			return "", "", todoTeach("local_proof", "match every PACED implementation commit to its previously verified remote delivery and any exact TODO repository identity", ErrConflict)
		}
		if selected.repository != "" && selected != candidate {
			return "", "", todoTeach("local_proof", "match all PACED implementation commits to the same verified repository and branch", ErrConflict)
		}
		selected = candidate
	}
	return selected.repository, selected.branch, nil
}

func verifiedCodeDeliveryIdentity(proof *TODOProofReference) bool {
	return proof != nil && proof.VerificationState == "verified" &&
		(proof.Kind == "remote-local-delivery" && proof.Source == "private-reader-and-paced-runner" ||
			proof.Kind == "remote-code-delivery" && proof.Source == "private-reader")
}

func stringSliceSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
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

type todoOriginalCommitSet struct {
	Repositories map[string][]string
	CommitOwner  map[string]string
}

func (m *MongoStore) verifyCompletePromotionArrival(ctx context.Context, companyID string, record TODORecord, current todoConstitutionPin, targets map[string]string, fixedBranch string) (TODOProofReference, []implementationRepositoryIdentity, error) {
	var err error
	if record.Source.Kind != "mongo-content-revision" || record.StageVerification != "verified" || record.VerifiedTransitionRef == nil || containsTODOString(record.Qualifiers, "Blocked") {
		return TODOProofReference{}, nil, todoTeach("local_proof", "require an unblocked verified current-v2 code proof", ErrConflict)
	}
	localProof := record.VerifiedTransitionRef
	var promotionPin todoConstitutionPin
	if record.Stage == "Promotion-Lane" {
		localProof, _, err = m.readCompleteLocalProof(ctx, companyID, record)
		if err != nil {
			return TODOProofReference{}, nil, err
		}
		if err := m.requireCompleteCurrentStageEvent(ctx, companyID, record, "Promotion-Lane"); err != nil {
			return TODOProofReference{}, nil, err
		}
		promotionPin, err = todoProofConstitutionPin(record.VerifiedTransitionRef)
		if err != nil {
			return TODOProofReference{}, nil, err
		}
		if err := requireTODOConstitutionPin(record.VerifiedTransitionRef, current); err != nil {
			return TODOProofReference{}, nil, err
		}
		if err := verifyCompleteArrivalReference(record.VerifiedTransitionRef, "promotion"); err != nil {
			return TODOProofReference{}, nil, err
		}
	} else if record.Stage != "Local-Implemented" || !verifiedCodeDeliveryIdentity(localProof) {
		return TODOProofReference{}, nil, todoTeach("local_proof", "require the exact verified Local-Implemented code proof", ErrConflict)
	} else if err := m.requireCompleteCurrentStageEvent(ctx, companyID, record, "Local-Implemented"); err != nil {
		return TODOProofReference{}, nil, err
	}
	localPin, err := m.resolveTODOConstitutionPin(ctx, companyID, record, localProof)
	if err != nil {
		return TODOProofReference{}, nil, err
	}
	if len(localPin.Repositories) == 0 && len(promotionPin.Repositories) > 0 {
		localPin.Repositories = promotionPin.Repositories
	}
	if localPin.Repositories == nil {
		localPin.Repositories = map[string]string{}
	}
	if !sameTODOConstitutionPin(localPin, current) {
		return TODOProofReference{}, nil, todoTeach("local_proof.constitution", "match the active constitution mode and immutable identity pinned at Local-Implemented", ErrConflict)
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return TODOProofReference{}, nil, err
	}
	database := m.client.Database(company.DatabaseName)
	content, contentErr := m.contentAtRevision(ctx, database, record.ProjectID, record.ID, record.Revision)
	if contentErr != nil {
		return TODOProofReference{}, nil, contentErr
	}
	if content.SchemaVersion != todoContentSchemaVersion {
		return TODOProofReference{}, nil, todoTeach("content_revision", "use the current typed Mongo v2 TODO content", ErrConflict)
	}
	record.Content = &content.TODOContent
	contentRevision, contentDigest, _, available, err := currentTODOContentSubject(ctx, database, record)
	if err != nil {
		return TODOProofReference{}, nil, err
	}
	if !available || !validSourceDigest(contentDigest) {
		return TODOProofReference{}, nil, todoTeach("content_revision", "resolve the current immutable TODO content digest", ErrConflict)
	}
	commits, err := todoOriginalCommits(record, contentDigest)
	if err != nil {
		return TODOProofReference{}, nil, err
	}
	if err := requireLocalProofCommitIdentity(localProof, commits); err != nil {
		return TODOProofReference{}, nil, err
	}
	if record.Stage == "Promotion-Lane" {
		if err := requireCompleteArrivalMatches(record.VerifiedTransitionRef, commits, localPin, "promotion", contentRevision, contentDigest); err != nil {
			return TODOProofReference{}, nil, err
		}
	}
	aliases := make([]string, 0, len(record.DiffExpectation))
	if len(localPin.Repositories) != 0 && len(localPin.Repositories) != len(record.DiffExpectation) {
		return TODOProofReference{}, nil, todoTeach("local_proof.repository_identity", "contain either no pre-pin identities or one immutable ID for every approved repository", ErrConflict)
	}
	for _, scope := range record.DiffExpectation {
		alias := scope.RepositoryID
		if len(localPin.Repositories) != 0 && localPin.Repositories[alias] == "" {
			return TODOProofReference{}, nil, todoTeach("local_proof.repository_identity", "contain an immutable ID for every approved repository alias", ErrConflict)
		}
		aliases = append(aliases, alias)
	}
	set, err := m.ReadImplementationRepositories(ctx, companyID, record.ProjectID)
	if err != nil {
		return TODOProofReference{}, nil, err
	}
	confirmed := stringSliceSet(set.Items)
	if m.proofReaderURL == "" || m.proofReaderToken == "" || m.proofHTTPClient == nil {
		return TODOProofReference{}, nil, todoTeach("remote_proof", "configure the private Reader before checking Complete-mode Git arrival", ErrUnavailable)
	}
	branch := fixedBranch
	if fixedBranch == "" {
		if err := validateCompletePromotionTargets(record.DiffExpectation, targets); err != nil {
			return TODOProofReference{}, nil, err
		}
	}
	sort.Strings(aliases)
	type evidence struct {
		Repository string   `json:"repository"`
		NodeID     string   `json:"repository_node_id"`
		Branch     string   `json:"branch"`
		HeadSHA    string   `json:"head_sha"`
		Commits    []string `json:"commits"`
	}
	verified := make([]evidence, 0, len(aliases))
	identityPins := make([]implementationRepositoryIdentity, 0, len(aliases))
	for _, alias := range aliases {
		if _, ok := confirmed[alias]; !ok || !validGitRepositoryID(alias) {
			return TODOProofReference{}, nil, todoTeach("diff_expectation.repository_id", "use the currently owner-confirmed Project repository alias", ErrConflict)
		}
		if pinnedID := set.GitHubNodeIDs[alias]; pinnedID != "" && localPin.Repositories[alias] != "" && pinnedID != localPin.Repositories[alias] {
			return TODOProofReference{}, nil, todoTeach("repository_identity.github_node_id", "match the immutable Project alias-to-GitHub-ID CAS binding", ErrConflict)
		}
		target := branch
		if fixedBranch == "" {
			target = targets[alias]
		}
		if fixedBranch == "" && target != "dev" && target != "stage" {
			return TODOProofReference{}, nil, todoTeach("promotion_targets."+alias, "select dev or stage for each original repository", ErrConflict)
		}
		query := url.Values{"company_id": {companyID}, "project_id": {record.ProjectID}, "repository": {alias}, "branch": {target}, "commit_shas": {strings.Join(commits.Repositories[alias], ",")}, "todo_authority": {"mongo-v2"}}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, m.proofReaderURL+"/internal/v1/proofs/arrival?"+query.Encode(), nil)
		if err != nil {
			return TODOProofReference{}, nil, ErrUnavailable
		}
		request.Header.Set("Authorization", "Bearer "+m.proofReaderToken)
		response, err := m.proofHTTPClient.Do(request)
		if err != nil {
			return TODOProofReference{}, nil, todoTeach("remote_proof", "retry when the private Reader is available", ErrUnavailable)
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		_ = response.Body.Close()
		if readErr != nil || len(data) == 0 || len(data) >= 64<<10 {
			return TODOProofReference{}, nil, ErrUnavailable
		}
		if response.StatusCode != http.StatusOK {
			var problem struct {
				Teach string `json:"teach"`
			}
			if json.Unmarshal(data, &problem) == nil && problem.Teach != "" {
				return TODOProofReference{}, nil, todoTeach("remote_proof", problem.Teach, ErrConflict)
			}
			return TODOProofReference{}, nil, todoTeach("remote_proof", "verify original commits on the target Git branch", ErrConflict)
		}
		var result struct {
			CompanyID        string `json:"company_id"`
			ProjectID        string `json:"project_id"`
			Repository       string `json:"repository"`
			RepositoryNodeID string `json:"repository_node_id"`
			Branch           string `json:"branch"`
			HeadSHA          string `json:"head_sha"`
			Commits          []struct {
				CommitSHA string `json:"commit_sha"`
				Reachable bool   `json:"reachable"`
			} `json:"commits"`
			Outcome string `json:"outcome"`
		}
		if json.Unmarshal(data, &result) != nil || result.CompanyID != companyID || result.ProjectID != record.ProjectID || result.Repository != alias || result.RepositoryNodeID == "" || len(result.RepositoryNodeID) > 128 || localPin.Repositories[alias] != "" && result.RepositoryNodeID != localPin.Repositories[alias] || set.GitHubNodeIDs[alias] != "" && result.RepositoryNodeID != set.GitHubNodeIDs[alias] || result.Branch != target || !validGitCommit(result.HeadSHA) || result.Outcome != "verified" || len(result.Commits) != len(commits.Repositories[alias]) {
			return TODOProofReference{}, nil, todoTeach("remote_proof.repository_identity", "match the original alias, pinned immutable GitHub ID, branch and bounded commit set", ErrConflict)
		}
		commitSHAs := make([]string, len(result.Commits))
		for i, item := range result.Commits {
			if !item.Reachable || item.CommitSHA != commits.Repositories[alias][i] {
				return TODOProofReference{}, nil, todoTeach("remote_proof.commits", "prove each original commit reachable from one target branch head", ErrConflict)
			}
			commitSHAs[i] = item.CommitSHA
		}
		identityPins = append(identityPins, implementationRepositoryIdentity{ProjectID: record.ProjectID, RepositoryID: alias, GitHubNodeID: result.RepositoryNodeID, ProofDigest: localProof.SourceDigest})
		localPin.Repositories[alias] = result.RepositoryNodeID
		verified = append(verified, evidence{Repository: alias, NodeID: result.RepositoryNodeID, Branch: target, HeadSHA: result.HeadSHA, Commits: commitSHAs})
	}
	arrivalStage := "promotion"
	if fixedBranch != "" {
		arrivalStage = "main"
	}
	payload, err := json.Marshal(struct {
		Stage           string              `json:"arrival_stage"`
		ContentRevision int64               `json:"content_revision"`
		ContentDigest   string              `json:"content_digest"`
		Constitution    todoConstitutionPin `json:"constitution"`
		Repositories    []evidence          `json:"repositories"`
	}{arrivalStage, contentRevision, contentDigest, localPin, verified})
	if err != nil {
		return TODOProofReference{}, nil, ErrInvalid
	}
	hash := sha256.Sum256(payload)
	refData, _ := json.Marshal(struct {
		Stage           string              `json:"arrival_stage"`
		ContentRevision int64               `json:"content_revision"`
		ContentDigest   string              `json:"content_digest"`
		Constitution    todoConstitutionPin `json:"constitution"`
		Repositories    []evidence          `json:"repositories"`
	}{arrivalStage, contentRevision, contentDigest, localPin, verified})
	proof := TODOProofReference{Kind: "complete-git-arrival", Source: "private-reader", Reference: string(refData), SourceDigest: "sha256:" + hex.EncodeToString(hash[:]), VerificationState: "verified"}
	proof, err = bindPromotionConstitutionPin(proof, localPin)
	return proof, identityPins, err
}

func validateCompletePromotionTargets(scopes []TODORepositoryScope, targets map[string]string) error {
	approvedAliases := map[string]bool{}
	for _, scope := range scopes {
		if !validGitRepositoryID(scope.RepositoryID) || approvedAliases[scope.RepositoryID] {
			return todoTeach("diff_expectation.repository_id", "identify each approved repository alias exactly once", ErrConflict)
		}
		approvedAliases[scope.RepositoryID] = true
	}
	if len(targets) != len(approvedAliases) {
		return todoTeach("promotion_targets", "provide exactly one dev or stage target for each approved repository", ErrConflict)
	}
	for alias, target := range targets {
		if !approvedAliases[alias] || target != "dev" && target != "stage" {
			return todoTeach("promotion_targets", "map only approved repository aliases to dev or stage", ErrConflict)
		}
	}
	return nil
}

func todoOriginalCommits(record TODORecord, contentDigest string) (todoOriginalCommitSet, error) {
	result := todoOriginalCommitSet{Repositories: map[string][]string{}, CommitOwner: map[string]string{}}
	for i, scope := range record.DiffExpectation {
		if !validGitRepositoryID(scope.RepositoryID) {
			return result, todoTeach(fmt.Sprintf("diff_expectation[%d].repository_id", i), "identify an exact implementation repository", ErrConflict)
		}
		if _, exists := result.Repositories[scope.RepositoryID]; exists {
			return result, todoTeach("diff_expectation.repository_id", "list each repository once", ErrConflict)
		}
		result.Repositories[scope.RepositoryID] = []string{}
	}
	for i, claim := range record.ImplementationCommits {
		if !validGitCommit(claim.Commit) || claim.SourceDigest != contentDigest || claim.VerificationState != "unverified" {
			return result, todoTeach(fmt.Sprintf("implementation_commits[%d]", i), "match a full original commit SHA and current content digest", ErrConflict)
		}
		alias := ""
		for _, scope := range record.DiffExpectation {
			if scope.Repository == claim.Repository {
				alias = scope.RepositoryID
				break
			}
		}
		if alias == "" {
			return result, todoTeach("implementation_commits.repository", "match each original commit to an approved repository", ErrConflict)
		}
		result.Repositories[alias] = append(result.Repositories[alias], claim.Commit)
		if prior := result.CommitOwner[claim.Commit]; prior != "" {
			return result, todoTeach("implementation_commits", "contain no duplicate original SHAs", ErrConflict)
		}
		result.CommitOwner[claim.Commit] = alias
	}
	if len(result.CommitOwner) == 0 || len(result.CommitOwner) > 32 {
		return result, todoTeach("implementation_commits", "contain one to 32 original commits across the approved repositories", ErrConflict)
	}
	for alias, shas := range result.Repositories {
		if len(shas) == 0 || len(shas) > 32 {
			return result, todoTeach("implementation_commits."+alias, "contain one to 32 original commits for every selected repository", ErrConflict)
		}
	}
	return result, nil
}

func bindPromotionConstitutionPin(proof TODOProofReference, pin todoConstitutionPin) (TODOProofReference, error) {
	data, err := json.Marshal(pin)
	if err != nil || strings.Contains(proof.Reference, ";"+todoConstitutionPinPrefix) {
		return TODOProofReference{}, ErrInvalid
	}
	proof.Reference += ";" + todoConstitutionPinPrefix + string(data)
	return proof, nil
}

func verifyCompleteArrivalReference(proof *TODOProofReference, expectedStage string) error {
	if proof == nil || proof.Kind != "complete-git-arrival" || proof.Source != "private-reader" || proof.VerificationState != "verified" {
		return todoTeach("promotion_history.proof", "resolve the immutable verified Git-arrival event", ErrConflict)
	}
	data, _, found := strings.Cut(proof.Reference, ";"+todoConstitutionPinPrefix)
	if !found || !validSourceDigest(proof.SourceDigest) {
		return todoTeach("promotion_history.proof", "contain the exact immutable arrival payload and constitution pin", ErrConflict)
	}
	hash := sha256.Sum256([]byte(data))
	if "sha256:"+hex.EncodeToString(hash[:]) != proof.SourceDigest {
		return todoTeach("promotion_history.proof.source_digest", "match its immutable arrival payload", ErrConflict)
	}
	var payload struct {
		Stage           string              `json:"arrival_stage"`
		ContentRevision int64               `json:"content_revision"`
		ContentDigest   string              `json:"content_digest"`
		Constitution    todoConstitutionPin `json:"constitution"`
		Repositories    []struct {
			Repository string   `json:"repository"`
			NodeID     string   `json:"repository_node_id"`
			Branch     string   `json:"branch"`
			HeadSHA    string   `json:"head_sha"`
			Commits    []string `json:"commits"`
		} `json:"repositories"`
	}
	if json.Unmarshal([]byte(data), &payload) != nil || payload.Stage != expectedStage || payload.ContentRevision < 1 || !validSourceDigest(payload.ContentDigest) || len(payload.Repositories) == 0 {
		return todoTeach("promotion_history.proof", "match a bounded verified arrival payload for the expected stage", ErrConflict)
	}
	for _, repository := range payload.Repositories {
		if !validGitRepositoryID(repository.Repository) || repository.NodeID == "" || len(repository.NodeID) > 128 || !validGitCommit(repository.HeadSHA) {
			return todoTeach("promotion_history.proof.repositories", "contain exact repository IDs, branch heads and immutable identity", ErrConflict)
		}
		for _, commit := range repository.Commits {
			if !validGitCommit(commit) {
				return todoTeach("promotion_history.proof.repositories.commits", "contain full original commit SHAs", ErrConflict)
			}
		}
	}
	pinned, err := todoProofConstitutionPin(proof)
	if err != nil || !sameTODOConstitutionPin(payload.Constitution, pinned) || len(payload.Constitution.Repositories) != len(pinned.Repositories) {
		return todoTeach("promotion_history.proof.constitution", "match the mode and identity pin embedded in the immutable arrival event", ErrConflict)
	}
	for alias, nodeID := range pinned.Repositories {
		if payload.Constitution.Repositories[alias] != nodeID {
			return todoTeach("promotion_history.proof.constitution.repositories", "match each pinned immutable GitHub ID", ErrConflict)
		}
	}
	return nil
}

func requireCompleteArrivalMatches(proof *TODOProofReference, expected todoOriginalCommitSet, pin todoConstitutionPin, stage string, contentRevision int64, contentDigest string) error {
	if err := verifyCompleteArrivalReference(proof, stage); err != nil {
		return err
	}
	data, _, _ := strings.Cut(proof.Reference, ";"+todoConstitutionPinPrefix)
	var payload struct {
		ContentRevision int64  `json:"content_revision"`
		ContentDigest   string `json:"content_digest"`
		Repositories    []struct {
			Repository string   `json:"repository"`
			NodeID     string   `json:"repository_node_id"`
			Branch     string   `json:"branch"`
			Commits    []string `json:"commits"`
		} `json:"repositories"`
	}
	if json.Unmarshal([]byte(data), &payload) != nil || len(payload.Repositories) != len(expected.Repositories) || payload.ContentRevision != contentRevision || payload.ContentDigest != contentDigest {
		return todoTeach("promotion_history.proof.repositories", "cover the exact current content revision and original implementation repository set", ErrConflict)
	}
	seen := map[string]bool{}
	for _, repository := range payload.Repositories {
		want, ok := expected.Repositories[repository.Repository]
		if !ok || seen[repository.Repository] || repository.NodeID != pin.Repositories[repository.Repository] || !equalCommitClaims(repository.Commits, want) {
			return todoTeach("promotion_history.proof.repositories", "match each original alias, immutable ID and exact commit set", ErrConflict)
		}
		if stage == "main" && repository.Branch != "main" || stage == "promotion" && repository.Branch != "dev" && repository.Branch != "stage" {
			return todoTeach("promotion_history.proof.repositories.branch", "use dev or stage for Promotion-Lane and main for Completed", ErrConflict)
		}
		seen[repository.Repository] = true
	}
	return nil
}

func (m *MongoStore) readCompleteLocalProof(ctx context.Context, companyID string, record TODORecord) (*TODOProofReference, TODOHistoryEvent, error) {
	database, err := m.todoTenantDatabase(ctx, companyID)
	if err != nil {
		return nil, TODOHistoryEvent{}, err
	}
	var event TODOHistoryEvent
	filter := bson.D{{Key: "project_id", Value: record.ProjectID}, {Key: "todo_id", Value: record.ID}, {Key: "kind", Value: "state_changed"}, {Key: "revision", Value: bson.D{{Key: "$lt", Value: record.Revision}}}, {Key: "changes.stage", Value: "Local-Implemented"}, {Key: "changes.stage_verification", Value: "verified"}, {Key: "changes.verified_transition_ref.kind", Value: bson.D{{Key: "$in", Value: bson.A{"remote-code-delivery", "remote-local-delivery"}}}}}
	err = database.Collection(todoEventsCollection).FindOne(ctx, filter, options.FindOne().SetSort(bson.D{{Key: "revision", Value: -1}})).Decode(&event)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, TODOHistoryEvent{}, todoTeach("local_history", "recover the immutable verified Local-Implemented event before Complete promotion", ErrConflict)
	}
	if err != nil {
		return nil, TODOHistoryEvent{}, ErrUnavailable
	}
	if event.Changes.Stage == nil || *event.Changes.Stage != "Local-Implemented" || event.Changes.StageVerification == nil || *event.Changes.StageVerification != "verified" || event.Changes.VerifiedTransitionRef == nil || !verifiedCodeDeliveryIdentity(event.Changes.VerifiedTransitionRef) {
		return nil, TODOHistoryEvent{}, todoTeach("local_history", "resolve the exact immutable verified Local-Implemented event and proof", ErrConflict)
	}
	copy := *event.Changes.VerifiedTransitionRef
	return &copy, event, nil
}

func (m *MongoStore) requireCompleteCurrentStageEvent(ctx context.Context, companyID string, record TODORecord, stage string) error {
	database, err := m.todoTenantDatabase(ctx, companyID)
	if err != nil {
		return err
	}
	var event TODOHistoryEvent
	filter := bson.D{{Key: "project_id", Value: record.ProjectID}, {Key: "todo_id", Value: record.ID}, {Key: "kind", Value: "state_changed"}, {Key: "revision", Value: bson.D{{Key: "$lte", Value: record.Revision}}}, {Key: "changes.stage", Value: stage}, {Key: "changes.stage_verification", Value: "verified"}}
	if err := database.Collection(todoEventsCollection).FindOne(ctx, filter, options.FindOne().SetSort(bson.D{{Key: "revision", Value: -1}})).Decode(&event); err != nil {
		return todoTeach("promotion_history.event", "resolve the exact immutable current-stage event", ErrConflict)
	}
	if event.Revision > record.Revision || event.OperationID == "" || event.Changes.Stage == nil || *event.Changes.Stage != stage || event.Changes.StageVerification == nil || *event.Changes.StageVerification != "verified" || event.Changes.VerifiedTransitionRef == nil || record.VerifiedTransitionRef == nil || *event.Changes.VerifiedTransitionRef != *record.VerifiedTransitionRef {
		return todoTeach("promotion_history.event", "match the current stage snapshot to its exact immutable state-change event and proof", ErrConflict)
	}
	return nil
}

func (m *MongoStore) historicalTODOConstitutionPin(ctx context.Context, companyID, projectID string, localEventAt time.Time) (todoConstitutionPin, error) {
	database, err := m.todoTenantDatabase(ctx, companyID)
	if err != nil {
		return todoConstitutionPin{}, err
	}
	sameTimeActivations, err := database.Collection(canonicalEvents).CountDocuments(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "family", Value: "project"}, {Key: "record_id", Value: projectID}, {Key: "kind", Value: "generation_activated"}, {Key: "occurred_at", Value: localEventAt}})
	if err != nil {
		return todoConstitutionPin{}, ErrUnavailable
	}
	if sameTimeActivations != 0 {
		return todoConstitutionPin{}, todoTeach("local_history.constitution", "resolve canonical activation strictly before the Local event timestamp; same-time activation is ambiguous", ErrConflict)
	}
	activationFilter := bson.D{{Key: "project_id", Value: projectID}, {Key: "family", Value: "project"}, {Key: "record_id", Value: projectID}, {Key: "kind", Value: "generation_activated"}, {Key: "occurred_at", Value: bson.D{{Key: "$lt", Value: localEventAt}}}}
	var activation CanonicalEvent
	if err := database.Collection(canonicalEvents).FindOne(ctx, activationFilter, options.FindOne().SetSort(bson.D{{Key: "occurred_at", Value: -1}, {Key: "_id", Value: -1}})).Decode(&activation); err != nil {
		return todoConstitutionPin{}, todoTeach("local_history.constitution", "resolve the uniquely active canonical generation before the immutable Local event", ErrConflict)
	}
	activationCollisions, err := database.Collection(canonicalEvents).CountDocuments(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "family", Value: "project"}, {Key: "record_id", Value: projectID}, {Key: "kind", Value: "generation_activated"}, {Key: "occurred_at", Value: activation.OccurredAt}})
	if err != nil || activationCollisions != 1 {
		return todoConstitutionPin{}, todoTeach("local_history.constitution", "resolve one unambiguous canonical generation activation before the Local event", ErrConflict)
	}
	filter := bson.D{{Key: "project_id", Value: projectID}, {Key: "generation_id", Value: activation.GenerationID}, {Key: "family", Value: FamilyConstitution}, {Key: "record_id", Value: projectID}, {Key: "occurred_at", Value: bson.D{{Key: "$lt", Value: localEventAt}}}}
	var collisions int64
	collisions, err = database.Collection(canonicalEvents).CountDocuments(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "generation_id", Value: activation.GenerationID}, {Key: "family", Value: FamilyConstitution}, {Key: "record_id", Value: projectID}, {Key: "occurred_at", Value: localEventAt}})
	if err != nil {
		return todoConstitutionPin{}, ErrUnavailable
	}
	if collisions > 0 {
		return todoConstitutionPin{}, todoTeach("local_history.constitution", "resolve constitution history strictly before the Local event timestamp; same-time updates are ambiguous", ErrConflict)
	}
	var event CanonicalEvent
	err = database.Collection(canonicalEvents).FindOne(ctx, filter, options.FindOne().SetSort(bson.D{{Key: "occurred_at", Value: -1}, {Key: "_id", Value: -1}})).Decode(&event)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return todoConstitutionPin{}, todoTeach("local_history.constitution", "find one active constitution revision before the immutable Local event", ErrConflict)
	}
	if err != nil {
		return todoConstitutionPin{}, ErrUnavailable
	}
	constitutionCollisions, err := database.Collection(canonicalEvents).CountDocuments(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "generation_id", Value: activation.GenerationID}, {Key: "family", Value: FamilyConstitution}, {Key: "record_id", Value: projectID}, {Key: "occurred_at", Value: event.OccurredAt}})
	if err != nil || constitutionCollisions != 1 {
		return todoConstitutionPin{}, todoTeach("local_history.constitution", "resolve one unambiguous constitution event at the historical timestamp", ErrConflict)
	}
	if event.Kind == "record_deleted" || event.GenerationID == "" || event.Revision < 1 {
		return todoConstitutionPin{}, todoTeach("local_history.constitution", "resolve a nondeleted historical constitution revision", ErrConflict)
	}
	var version struct {
		Record canonicalStored `bson:"record"`
	}
	err = database.Collection(canonicalVersions).FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "generation_id", Value: event.GenerationID}, {Key: "family", Value: FamilyConstitution}, {Key: "record_id", Value: projectID}, {Key: "revision", Value: event.Revision}}).Decode(&version)
	if err != nil {
		return todoConstitutionPin{}, ErrUnavailable
	}
	record := version.Record.toRecord()
	if record.ContentDigest != canonicalDigest(record.Payload) || record.Deleted {
		return todoConstitutionPin{}, todoTeach("local_history.constitution", "match the immutable canonical payload digest and nondeleted state", ErrConflict)
	}
	var payload ConstitutionPayload
	if json.Unmarshal(record.Payload, &payload) != nil || (payload.PromotionMode != "mvp" && payload.PromotionMode != "complete") {
		return todoConstitutionPin{}, todoTeach("local_history.constitution.promotion_mode", "resolve one valid historical typed mode", ErrConflict)
	}
	return todoConstitutionPin{Mode: payload.PromotionMode, GenerationID: record.GenerationID, RecordID: record.ID, Revision: record.Revision, ContentDigest: record.ContentDigest}, nil
}

func requireLocalProofCommitIdentity(proof *TODOProofReference, commits todoOriginalCommitSet) error {
	if proof == nil || !verifiedCodeDeliveryIdentity(proof) {
		return todoTeach("local_history.proof", "use an immutable verified local code-delivery event", ErrConflict)
	}
	seen := map[string]string{}
	for _, item := range strings.Split(proof.Reference, ";") {
		if item == "" || strings.HasPrefix(item, "run=") || strings.HasPrefix(item, "repository_node_id=") || strings.HasPrefix(item, todoConstitutionPinPrefix) {
			continue
		}
		key, detail, ok := strings.Cut(item, ":")
		if !ok {
			return todoTeach("local_history.proof.ref", "contain exact repository/branch/original-commit tuples", ErrConflict)
		}
		alias, branch, ok := strings.Cut(key, "@")
		parts := strings.Split(detail, "/")
		if !ok || !validGitRepositoryID(alias) || !validDeliveryBranch(branch) || len(parts) != 3 || !validGitCommit(parts[0]) || !strings.HasPrefix(parts[1], "parent=") || !validGitCommit(strings.TrimPrefix(parts[1], "parent=")) || !strings.HasPrefix(parts[2], "head=") || !validGitCommit(strings.TrimPrefix(parts[2], "head=")) {
			return todoTeach("local_history.proof.ref", "contain exact verified repository, branch and original commit identities", ErrConflict)
		}
		if seen[parts[0]] != "" && seen[parts[0]] != alias {
			return todoTeach("local_history.proof.ref", "map each original SHA to one immutable repository alias", ErrConflict)
		}
		seen[parts[0]] = alias
	}
	if len(seen) != len(commits.CommitOwner) {
		return todoTeach("local_history.proof.ref", "match the exact current-digest original commit set", ErrConflict)
	}
	for sha, alias := range commits.CommitOwner {
		if seen[sha] != alias {
			return todoTeach("local_history.proof.ref", "match each original commit to its approved repository alias", ErrConflict)
		}
	}
	return nil
}
