package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type mvpClaims struct {
	builder, paced, foundation                     []string
	dockerRepository, pacedRepository, pacedBranch string
}

// The same resolved repository identities drive the local fence and Reader request.
func resolveMVPClaims(todo TODORecord, boundDockerRepository string) (mvpClaims, error) {
	claims := mvpClaims{dockerRepository: mvpApprovedDockerRepository(todo)}
	if claims.dockerRepository == "" {
		claims.dockerRepository = boundDockerRepository
	}
	builder, paced, foundation, err := mvpImplementationClaims(todo, claims.dockerRepository)
	if err != nil {
		return mvpClaims{}, err
	}
	claims.builder, claims.paced, claims.foundation = builder, paced, foundation
	if len(paced) != 0 {
		claims.pacedRepository, claims.pacedBranch, err = mvpPACEDRepository(todo, paced)
	}
	return claims, err
}

func verifyMVPLocalDelivery(todo TODORecord, contentDigest string, localIDs map[string]string, claims mvpClaims) error {
	commits := todoOriginalCommitSet{CommitOwner: make(map[string]string, len(claims.builder)+len(claims.paced))}
	for _, sha := range claims.builder {
		if !validGitRepositoryID(claims.dockerRepository) {
			return todoTeach("project.docker_repository_id", "select the approved or bound Docker repository for the original production commits", ErrConflict)
		}
		commits.CommitOwner[sha] = claims.dockerRepository
	}
	for _, sha := range claims.paced {
		commits.CommitOwner[sha] = claims.pacedRepository
	}
	if len(commits.CommitOwner) == 0 || len(commits.CommitOwner) != len(todo.ImplementationCommits) {
		return todoTeach("implementation_commits", "match distinct original production commits and repository identities", ErrConflict)
	}
	for _, claim := range todo.ImplementationCommits {
		if claim.SourceDigest != contentDigest || claim.VerificationState != "unverified" {
			return todoTeach("implementation_commits", "bind the original implementation claims to the current immutable content", ErrConflict)
		}
	}
	if len(localIDs) != 0 {
		aliases := make(map[string]bool, len(localIDs))
		for _, alias := range commits.CommitOwner {
			aliases[alias] = true
			if localIDs[alias] == "" {
				return todoTeach("local_history.proof.repository_identity", "cover every exact implementation repository in the deployment", ErrConflict)
			}
		}
		if len(aliases) != len(localIDs) {
			return todoTeach("local_history.proof.repository_identity", "cover only the implementation repositories in the deployment", ErrConflict)
		}
	}
	return requireLocalProofCommitIdentity(todo.VerifiedTransitionRef, commits)
}

func (m *MongoStore) verifyMVPProduction(ctx context.Context, companyID string, todo TODORecord, request TODOActionRequest, claims mvpClaims) (TODOProofReference, error) {
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
	builderCommits, pacedCommits, foundationCommits := claims.builder, claims.paced, claims.foundation
	query := url.Values{
		"company_id": {companyID}, "project_id": {todo.ProjectID}, "todo_id": {todo.ID},
		"deployment_id": {parts[0]}, "root_sha": {parts[1]}, "module_path": {"project"}, "commit_sha": {parts[1]}, "project_mode": {"mvp"},
	}
	if err := claims.addReaderQuery(query, usesTypedMongoAuthority(todo), todo.Content != nil && todo.Content.SchemaVersion == todoContentSchemaVersion); err != nil {
		return TODOProofReference{}, err
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

func (claims mvpClaims) addReaderQuery(query url.Values, mongoAuthority, typedContent bool) error {
	if mongoAuthority {
		if validGitRepositoryID(claims.dockerRepository) {
			query.Set("docker_repository_id", claims.dockerRepository)
		} else if typedContent {
			return todoTeach("project.docker_repository_id", "register a Docker deployment repository or include an approved Builder implementation scope before production proof", ErrConflict)
		}
		if typedContent {
			query.Set("todo_authority", "mongo-v2")
		}
	}
	if len(claims.builder) > 0 {
		query.Set("project_commits", strings.Join(claims.builder, ","))
	}
	if len(claims.paced) > 0 {
		query.Set("paced_commits", strings.Join(claims.paced, ","))
		query.Set("paced_repository_id", claims.pacedRepository)
		query.Set("paced_branch", claims.pacedBranch)
	}
	if len(claims.foundation) > 0 {
		query.Set("foundation_commits", strings.Join(claims.foundation, ","))
	}
	return nil
}

// Older v2 contracts can lack repository metadata even though their local delivery
// already verified the exact remote PACED identity for every implementation commit.
// Reuse only that verified identity; never infer it from a label or a current branch.
func mvpPACEDRepository(todo TODORecord, commits []string) (string, string, error) {
	proof := todo.VerifiedTransitionRef
	if !verifiedCodeDeliveryIdentity(proof) || len(commits) == 0 {
		return "", "", todoTeach("local_proof", "production must use one verified local-delivery record for every PACED implementation commit", ErrConflict)
	}
	requested := stringSliceSet(commits)
	scopeAliases := map[string]struct{}{"PACED": {}, "delphi-ai": {}}
	if todo.Content != nil && todo.Content.SchemaVersion == todoContentSchemaVersion {
		scopeAliases = make(map[string]struct{})
		for _, claim := range todo.ImplementationCommits {
			if _, ok := requested[claim.Commit]; ok {
				scopeAliases[claim.Repository] = struct{}{}
			}
		}
	}
	var scopedRepository string
	for _, scope := range todo.DiffExpectation {
		if _, ok := scopeAliases[scope.Repository]; !ok || scope.RepositoryID == "" {
			continue
		}
		if !validGitRepositoryID(scope.RepositoryID) || scopedRepository != "" && scopedRepository != scope.RepositoryID {
			return "", "", todoTeach("diff_expectation.repository_id", "match one exact PACED repository identity when the TODO scope records it", ErrConflict)
		}
		scopedRepository = scope.RepositoryID
	}
	type identity struct{ repository, branch string }
	verified := make(map[string]identity, len(commits))
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

func mvpImplementationClaims(todo TODORecord, dockerRepository string) ([]string, []string, []string, error) {
	var builderCommits, pacedCommits, foundationCommits []string
	for _, claim := range todo.ImplementationCommits {
		if !validGitCommit(claim.Commit) {
			return nil, nil, nil, todoTeach("implementation_commits", "provide valid published implementation commit SHAs before MVP completion", ErrConflict)
		}
		source := strings.ToLower(strings.TrimSpace(claim.Repository))
		if todo.Content != nil && todo.Content.SchemaVersion == todoContentSchemaVersion && len(todo.DiffExpectation) > 0 {
			var match *TODORepositoryScope
			for i := range todo.DiffExpectation {
				scope := &todo.DiffExpectation[i]
				if scope.Repository != claim.Repository {
					continue
				}
				if match != nil {
					return nil, nil, nil, todoTeach("implementation_commits.repository", "select one approved repository for each implementation alias", ErrConflict)
				}
				match = scope
			}
			if match == nil {
				return nil, nil, nil, todoTeach("implementation_commits.repository", "match an exact alias from the approved TODO scope", ErrConflict)
			}
			if match.RepositoryID != "" {
				switch {
				case validGitRepositoryID(dockerRepository) && match.RepositoryID == dockerRepository:
					source = "builder"
				case match.RepositoryID == "belluga/delphi-ai" || validGitRepositoryID(match.RepositoryID) && (source == "paced" || source == "delphi-ai"):
					source = "paced"
				default:
					return nil, nil, nil, todoTeach("implementation_commits.repository", "match the approved Docker or PACED repository identity for this MVP release", ErrConflict)
				}
			}
		}
		switch source {
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

type completeArrivalRepository struct {
	Repository string   `json:"repository"`
	NodeID     string   `json:"repository_node_id"`
	Branch     string   `json:"branch"`
	HeadSHA    string   `json:"head_sha"`
	Commits    []string `json:"commits"`
}

type completeArrivalProof struct {
	Stage              string                      `json:"arrival_stage"`
	ContentRevision    int64                       `json:"content_revision"`
	ContentDigest      string                      `json:"content_digest"`
	Mode               string                      `json:"mode,omitempty"`
	LocalEventRevision int64                       `json:"local_event_revision,omitempty"`
	LocalProofDigest   string                      `json:"local_proof_digest,omitempty"`
	Constitution       *todoConstitutionPin        `json:"constitution,omitempty"`
	Repositories       []completeArrivalRepository `json:"repositories"`
}

func (m *MongoStore) verifyCompletePromotionArrival(ctx context.Context, companyID string, record TODORecord, localEvent TODOHistoryEvent, localIDs map[string]string, contentRevision int64, contentDigest string, targets map[string]string) (TODOProofReference, []implementationRepositoryIdentity, error) {
	localProof := localEvent.Changes.VerifiedTransitionRef
	commits, err := todoOriginalCommits(record, contentDigest)
	if err != nil {
		return TODOProofReference{}, nil, err
	}
	if err := requireLocalProofCommitIdentity(localProof, commits); err != nil {
		return TODOProofReference{}, nil, err
	}
	if record.Stage == "Promotion-Lane" {
		remoteIDs, err := requireCompleteArrivalMatches(record.VerifiedTransitionRef, commits, localEvent, contentRevision, contentDigest)
		if err != nil {
			return TODOProofReference{}, nil, err
		}
		for alias, nodeID := range remoteIDs {
			if localIDs[alias] != "" && localIDs[alias] != nodeID {
				return TODOProofReference{}, nil, todoTeach("promotion_history.proof.repositories", "match the immutable GitHub identity in the original Local delivery", ErrConflict)
			}
			localIDs[alias] = nodeID
		}
	}
	aliases := make([]string, 0, len(record.DiffExpectation))
	if len(localIDs) != 0 && len(localIDs) != len(record.DiffExpectation) {
		return TODOProofReference{}, nil, todoTeach("local_proof.repository_identity", "contain either no pre-pin identities or one immutable ID for every approved repository", ErrConflict)
	}
	for _, scope := range record.DiffExpectation {
		alias := scope.RepositoryID
		if len(localIDs) != 0 && localIDs[alias] == "" {
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
	arrivalStage := "main"
	if record.Stage == "Local-Implemented" {
		arrivalStage = "promotion"
		if err := validateCompletePromotionTargets(record.DiffExpectation, targets); err != nil {
			return TODOProofReference{}, nil, err
		}
	}
	sort.Strings(aliases)
	verified := make([]completeArrivalRepository, 0, len(aliases))
	identityPins := make([]implementationRepositoryIdentity, 0, len(aliases))
	for _, alias := range aliases {
		if _, ok := confirmed[alias]; !ok || !validGitRepositoryID(alias) {
			return TODOProofReference{}, nil, todoTeach("diff_expectation.repository_id", "use the currently owner-confirmed Project repository alias", ErrConflict)
		}
		if pinnedID := set.GitHubNodeIDs[alias]; pinnedID != "" && localIDs[alias] != "" && pinnedID != localIDs[alias] {
			return TODOProofReference{}, nil, todoTeach("repository_identity.github_node_id", "match the immutable Project alias-to-GitHub-ID CAS binding", ErrConflict)
		}
		target := "main"
		if arrivalStage == "promotion" {
			target = targets[alias]
		}
		query := url.Values{"company_id": {companyID}, "project_id": {record.ProjectID}, "repository": {alias}, "branch": {target}, "commit_shas": {strings.Join(commits.Repositories[alias], ",")}, "todo_authority": {"mongo-v2"}}
		response, lease, err := m.requestReaderProof(ctx, "/internal/v1/proofs/arrival", query, true)
		if err != nil {
			return TODOProofReference{}, nil, err
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
		if json.Unmarshal(data, &result) != nil || result.CompanyID != companyID || result.ProjectID != record.ProjectID || result.Repository != alias || result.RepositoryNodeID == "" || len(result.RepositoryNodeID) > 128 || localIDs[alias] != "" && result.RepositoryNodeID != localIDs[alias] || set.GitHubNodeIDs[alias] != "" && result.RepositoryNodeID != set.GitHubNodeIDs[alias] || result.Branch != target || !validGitCommit(result.HeadSHA) || result.Outcome != "verified" || len(result.Commits) != len(commits.Repositories[alias]) {
			return TODOProofReference{}, nil, todoTeach("remote_proof.repository_identity", "match the original alias, pinned immutable GitHub ID, branch and bounded commit set", ErrConflict)
		}
		commitSHAs := make([]string, len(result.Commits))
		for i, item := range result.Commits {
			if !item.Reachable || item.CommitSHA != commits.Repositories[alias][i] {
				return TODOProofReference{}, nil, todoTeach("remote_proof.commits", "prove each original commit reachable from one target branch head", ErrConflict)
			}
			commitSHAs[i] = item.CommitSHA
		}
		if err := m.fenceGitHubReaderProof(ctx, lease, result.RepositoryNodeID); err != nil {
			return TODOProofReference{}, nil, err
		}
		identityPins = append(identityPins, implementationRepositoryIdentity{ProjectID: record.ProjectID, RepositoryID: alias, GitHubNodeID: result.RepositoryNodeID, ProofDigest: localProof.SourceDigest, githubLease: lease})
		verified = append(verified, completeArrivalRepository{Repository: alias, NodeID: result.RepositoryNodeID, Branch: target, HeadSHA: result.HeadSHA, Commits: commitSHAs})
	}
	payload, err := json.Marshal(completeArrivalProof{
		Stage: arrivalStage, ContentRevision: contentRevision, ContentDigest: contentDigest, Mode: "complete",
		LocalEventRevision: localEvent.Revision, LocalProofDigest: localProof.SourceDigest, Repositories: verified,
	})
	if err != nil {
		return TODOProofReference{}, nil, ErrInvalid
	}
	return TODOProofReference{Kind: "complete-git-arrival", Source: "private-reader", Reference: string(payload), SourceDigest: sourceHash(payload), VerificationState: "verified"}, identityPins, nil
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

// The legacy proof embedded a constitution pin, including repository identity,
// in both the hashed arrival payload and a suffix. Preserve both formats when reading.
type todoConstitutionPin struct {
	Mode          string            `json:"mode"`
	GenerationID  string            `json:"generation_id"`
	RecordID      string            `json:"record_id"`
	Revision      int64             `json:"revision"`
	ContentDigest string            `json:"content_digest"`
	Repositories  map[string]string `json:"repositories"`
}

const todoConstitutionPinPrefix = "promotion_context="

func (pin todoConstitutionPin) valid() bool {
	if !validProjectPromotionMode(pin.Mode) || pin.GenerationID == "" || pin.RecordID == "" || pin.Revision < 1 || !validSourceDigest(pin.ContentDigest) || len(pin.Repositories) == 0 {
		return false
	}
	for alias, nodeID := range pin.Repositories {
		if !validGitRepositoryID(alias) || nodeID == "" || len(nodeID) > 128 {
			return false
		}
	}
	return true
}

func localProofRepositoryIdentities(proof TODOProofReference) (map[string]string, error) {
	identities := todoProofRepositoryNodeIDs(proof.Reference)
	if identities == nil {
		return nil, todoTeach("local_history.proof.repository_identity", "contain valid consistent immutable GitHub IDs", ErrConflict)
	}
	_, suffix, hasLegacyPin := strings.Cut(proof.Reference, ";"+todoConstitutionPinPrefix)
	if !hasLegacyPin {
		return identities, nil
	}
	var pin todoConstitutionPin
	if json.Unmarshal([]byte(suffix), &pin) != nil || !pin.valid() {
		return nil, todoTeach("local_history.proof.repository_identity", "contain one valid historical pin", ErrConflict)
	}
	if len(identities) == 0 {
		return pin.Repositories, nil
	}
	if !maps.Equal(identities, pin.Repositories) {
		return nil, todoTeach("local_history.proof.repository_identity", "match repository identities across the original tuples and historical pin", ErrConflict)
	}
	return identities, nil
}

func requireCompleteArrivalMatches(proof *TODOProofReference, commits todoOriginalCommitSet, localEvent TODOHistoryEvent, contentRevision int64, contentDigest string) (map[string]string, error) {
	if proof == nil || proof.Kind != "complete-git-arrival" || proof.Source != "private-reader" || proof.VerificationState != "verified" {
		return nil, todoTeach("promotion_history.proof", "resolve the immutable verified Git-arrival event", ErrConflict)
	}
	data, suffix, legacy := strings.Cut(proof.Reference, ";"+todoConstitutionPinPrefix)
	if sourceHash([]byte(data)) != proof.SourceDigest {
		return nil, todoTeach("promotion_history.proof.source_digest", "match the immutable arrival payload", ErrConflict)
	}
	var payload completeArrivalProof
	if json.Unmarshal([]byte(data), &payload) != nil || payload.Stage != "promotion" || payload.ContentRevision != contentRevision ||
		payload.ContentDigest != contentDigest || len(payload.Repositories) != len(commits.Repositories) {
		return nil, todoTeach("promotion_history.proof", "match the exact current content revision, Promotion-Lane stage, and approved repository set", ErrConflict)
	}
	var historicalIDs map[string]string
	if legacy {
		var pinned todoConstitutionPin
		if json.Unmarshal([]byte(suffix), &pinned) != nil || !pinned.valid() || pinned.Mode != "complete" || payload.Constitution == nil ||
			payload.Mode != "" || payload.LocalEventRevision != 0 || payload.LocalProofDigest != "" {
			return nil, todoTeach("promotion_history.proof", "contain an unambiguous legacy Complete-mode pin", ErrConflict)
		}
		embedded := payload.Constitution
		if embedded.Mode != pinned.Mode || embedded.GenerationID != pinned.GenerationID || embedded.RecordID != pinned.RecordID ||
			embedded.Revision != pinned.Revision || embedded.ContentDigest != pinned.ContentDigest || !maps.Equal(embedded.Repositories, pinned.Repositories) ||
			len(pinned.Repositories) != len(commits.Repositories) {
			return nil, todoTeach("promotion_history.proof", "match the mode and identities in the hashed payload and legacy pin", ErrConflict)
		}
		historicalIDs = pinned.Repositories
	} else if payload.Constitution != nil || payload.Mode != "complete" || payload.LocalEventRevision != localEvent.Revision ||
		payload.LocalProofDigest != localEvent.Changes.VerifiedTransitionRef.SourceDigest {
		return nil, todoTeach("promotion_history.proof", "match the preserved Complete mode and exact Local-Implemented event", ErrConflict)
	}

	identities := make(map[string]string, len(payload.Repositories))
	for _, repository := range payload.Repositories {
		want, found := commits.Repositories[repository.Repository]
		if !found || identities[repository.Repository] != "" || repository.NodeID == "" || len(repository.NodeID) > 128 ||
			historicalIDs != nil && historicalIDs[repository.Repository] != repository.NodeID || !validGitCommit(repository.HeadSHA) ||
			!equalCommitClaims(repository.Commits, want) || repository.Branch != "dev" && repository.Branch != "stage" {
			return nil, todoTeach("promotion_history.proof.repositories", "match each exact repository alias, immutable ID, promotion branch and original commit set", ErrConflict)
		}
		identities[repository.Repository] = repository.NodeID
	}
	return identities, nil
}

func readVerifiedTODOStageEvent(ctx context.Context, database *mongo.Database, record TODORecord, stage string, maxRevision int64) (TODOHistoryEvent, error) {
	var event TODOHistoryEvent
	filter := bson.M{"project_id": record.ProjectID, "todo_id": record.ID, "kind": "state_changed",
		"revision": bson.M{"$lte": maxRevision}, "changes.stage": stage}
	err := database.Collection(todoEventsCollection).FindOne(ctx, filter, options.FindOne().SetSort(bson.D{{Key: "revision", Value: -1}})).Decode(&event)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return TODOHistoryEvent{}, todoTeach("promotion_history.event", "resolve the exact immutable "+stage+" event", ErrConflict)
	}
	if err != nil {
		return TODOHistoryEvent{}, ErrUnavailable
	}
	if event.OperationID == "" || event.Changes.Stage == nil || *event.Changes.Stage != stage || event.Changes.StageVerification == nil ||
		*event.Changes.StageVerification != "verified" || event.Changes.VerifiedTransitionRef == nil {
		return TODOHistoryEvent{}, todoTeach("promotion_history.event", "resolve the exact verified state-change event and proof for "+stage, ErrConflict)
	}
	return event, nil
}

func readCurrentTODOPromotionEvent(ctx context.Context, database *mongo.Database, record TODORecord) (TODOHistoryEvent, error) {
	event, err := readVerifiedTODOStageEvent(ctx, database, record, record.Stage, record.Revision)
	if err != nil {
		return TODOHistoryEvent{}, err
	}
	if record.VerifiedTransitionRef == nil || *event.Changes.VerifiedTransitionRef != *record.VerifiedTransitionRef {
		return TODOHistoryEvent{}, todoTeach("promotion_history.event", "match the current stage snapshot to its exact immutable state-change event and proof", ErrConflict)
	}
	return event, nil
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
