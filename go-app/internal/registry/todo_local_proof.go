package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

var localRunIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

const maxLocalDeliveryCommits = 32

type localProofRepository struct {
	RepositoryID       string `json:"repository_id"`
	Branch             string `json:"branch"`
	HeadBefore         string `json:"head_before"`
	HeadAfter          string `json:"head_after"`
	InputsDigestBefore string `json:"inputs_digest_before"`
	InputsDigestAfter  string `json:"inputs_digest_after"`
}

type localProofCheck struct {
	ID           string `json:"id"`
	RepositoryID string `json:"repository_id"`
	ExitCode     int    `json:"exit_code"`
}

type LocalProofResult struct {
	SchemaVersion string                 `json:"schema_version"`
	TODOID        string                 `json:"todo_id"`
	CompanyID     string                 `json:"company_id,omitempty"`
	ProjectID     string                 `json:"project_id,omitempty"`
	TODORevision  *int64                 `json:"todo_revision,omitempty"`
	ContentDigest string                 `json:"content_digest,omitempty"`
	ReleaseID     string                 `json:"release_id,omitempty"`
	Position      *int                   `json:"position,omitempty"`
	RunID         string                 `json:"run_id"`
	StartedAt     string                 `json:"started_at"`
	FinishedAt    string                 `json:"finished_at"`
	Repositories  []localProofRepository `json:"repositories"`
	Checks        []localProofCheck      `json:"checks"`
	ResultDigest  string                 `json:"result_digest"`
}

func sourceHash(data []byte) string {
	hash := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(hash[:])
}

func (m *MongoStore) validateLocalProof(content TODOAuthoredContent, branchPlan map[string]string, result *LocalProofResult) (TODOProofReference, error) {
	if result == nil || result.SchemaVersion != "builder-local-proof-result-v3" || !localRunIDPattern.MatchString(result.RunID) {
		return TODOProofReference{}, todoTeach("local_proof", "provide the current Project-scoped PACED result", ErrConflict)
	}
	if len(result.Checks) != len(content.LocalChecks) || len(content.LocalChecks) == 0 {
		return TODOProofReference{}, todoTeach("local_proof.checks", "include every typed check selected by the exact TODO content", ErrConflict)
	}
	for i, check := range content.LocalChecks {
		actual := result.Checks[i]
		if actual.ID != check.ID || actual.RepositoryID != check.RepositoryID || actual.ExitCode != 0 {
			return TODOProofReference{}, todoTeach(fmt.Sprintf("local_proof.checks[%d]", i), "pass each exact TODO-selected repository check in content order", ErrConflict)
		}
	}
	if len(result.Repositories) != len(branchPlan) {
		return TODOProofReference{}, todoTeach("local_proof.repositories", "include only the exact repositories selected by this TODO and its Release plan", ErrConflict)
	}
	seenRepositories := make(map[string]bool, len(result.Repositories))
	for i, repository := range result.Repositories {
		field := fmt.Sprintf("local_proof.repositories[%d]", i)
		requiredBranch, selected := branchPlan[repository.RepositoryID]
		if !selected || seenRepositories[repository.RepositoryID] || repository.Branch != requiredBranch || repository.HeadBefore != repository.HeadAfter || !validGitCommit(repository.HeadBefore) || !validSourceDigest(repository.InputsDigestBefore) || repository.InputsDigestBefore != repository.InputsDigestAfter {
			return TODOProofReference{}, todoTeach(field, "match one selected immutable Release branch with unchanged checkout head and committed input digest", ErrConflict)
		}
		seenRepositories[repository.RepositoryID] = true
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
	if result.ResultDigest != sourceHash(canonical) {
		return TODOProofReference{}, todoTeach("local_proof.result_digest", "match the exact PACED local result digest", ErrConflict)
	}
	return TODOProofReference{Kind: "local-run-verified", Source: "paced-local-runner", Reference: result.RunID, SourceDigest: result.ResultDigest, VerificationState: "verified"}, nil
}

// verifyLocalRequirementReferences binds every typed v2 criterion and scenario
// to a command that actually completed successfully in this exact local run.
// The references declare coverage; the API verifies the command/result binding
// and does not treat the reference itself as a caller-authored pass claim.
func verifyLocalRequirementReferences(content *TODOContent, references []TODOProofReference, result *LocalProofResult, contentDigest string) ([]TODOProofReference, error) {
	if content == nil || content.SchemaVersion != todoContentSchemaVersion || result == nil {
		return nil, todoTeach("proof_refs", "provide typed content and the exact current PACED local result", ErrConflict)
	}
	if len(content.LocalChecks) == 0 || len(content.LocalChecks) != len(result.Checks) {
		return nil, todoTeach("local_proof.checks", "match every check selected by the exact TODO content", ErrConflict)
	}
	commands := make(map[string]bool, len(result.Checks))
	for i, command := range result.Checks {
		if command.ID != content.LocalChecks[i].ID || command.RepositoryID != content.LocalChecks[i].RepositoryID || commands[command.ID] {
			return nil, todoTeach("local_proof.checks", "contain unique check IDs selected by the exact TODO content", ErrConflict)
		}
		commands[command.ID] = command.ExitCode == 0
	}
	required := make(map[string]bool, len(content.Criteria)+len(content.ValidationScenarios))
	scenarioCommands := make(map[string]bool, len(result.Checks))
	for _, criterion := range content.Criteria {
		required["criterion:"+criterion.ID] = false
	}
	for _, scenario := range content.ValidationScenarios {
		required["scenario:"+scenario.ID] = false
	}
	verified := append([]TODOProofReference(nil), references...)
	seenScenarioReference := make(map[string]bool)
	for i := range verified {
		ref := &verified[i]
		if _, applies := required[ref.Kind]; !applies {
			continue
		}
		// Old content revisions remain immutable history. They neither satisfy
		// nor conflict with current-revision coverage for the same subject.
		if ref.SourceDigest != contentDigest {
			continue
		}
		field := "proof_refs." + ref.Kind
		if ref.Source != "paced-local-runner" {
			return nil, todoTeach(field+".source", "be exactly paced-local-runner for PACED command evidence", ErrConflict)
		}
		if ref.Reference == "" {
			return nil, todoTeach(field+".ref", "be the exact ID of a successful check in this local result, not a run UUID or argv", ErrConflict)
		}
		if ref.VerificationState != "unverified" && ref.VerificationState != "verified" {
			return nil, todoTeach(field+".verification_state", "be unverified for new evidence; preserve any existing verified reference exactly", ErrConflict)
		}
		scenario := strings.HasPrefix(ref.Kind, "scenario:")
		if required[ref.Kind] && !scenario {
			return nil, todoTeach(field, "provide exactly one command reference for each criterion", ErrConflict)
		}
		if scenario && seenScenarioReference[ref.Kind+"\x00"+ref.Reference] {
			return nil, todoTeach(field+".ref", "do not repeat the same check ID for a validation scenario", ErrConflict)
		}
		exitOK, present := commands[ref.Reference]
		if !present {
			return nil, todoTeach(field+".ref", "use the exact check ID of a successful command in this local result, not the run UUID or argv", ErrConflict)
		}
		if !exitOK {
			return nil, todoTeach(field+".ref", "use the exact check ID of a command that exited zero in this local result", ErrConflict)
		}
		required[ref.Kind] = true
		if scenario {
			seenScenarioReference[ref.Kind+"\x00"+ref.Reference] = true
			scenarioCommands[ref.Reference] = true
		}
		ref.VerificationState = "verified"
	}
	for kind, found := range required {
		if !found {
			return nil, todoTeach("proof_refs."+kind+".source_digest", "add a new proof reference for the exact current TODO content digest "+contentDigest+" with source paced-local-runner, ref equal to a successful check ID and verification_state unverified; retain historical references unchanged", ErrConflict)
		}
	}
	for _, command := range result.Checks {
		if !scenarioCommands[command.ID] {
			return nil, todoTeach("proof_refs.scenario.ref", "add a current-digest scenario reference using the exact successful check ID "+command.ID, ErrConflict)
		}
	}
	return verified, nil
}

// External citations are coverage claims, not attestations that a test ran.
// Only the Reader-backed commit proof can make the new stage verified.
func validateExternalRequirementReferences(content *TODOContent, references []TODOProofReference, contentDigest string) error {
	if content == nil || content.SchemaVersion != todoContentSchemaVersion {
		return todoTeach("proof_refs", "use the current typed TODO content for external evidence", ErrConflict)
	}
	required := make(map[string]bool, len(content.Criteria)+len(content.ValidationScenarios))
	for _, criterion := range content.Criteria {
		required["criterion:"+criterion.ID] = false
	}
	for _, scenario := range content.ValidationScenarios {
		required["scenario:"+scenario.ID] = false
	}
	for _, ref := range references {
		if ref.SourceDigest != contentDigest {
			continue // Immutable historical citations cannot satisfy this revision.
		}
		_, needed := required[ref.Kind]
		if !needed && !strings.HasPrefix(ref.Kind, "criterion:") && !strings.HasPrefix(ref.Kind, "scenario:") && !strings.HasPrefix(ref.Kind, "test:") {
			continue
		}
		field := "proof_refs." + ref.Kind
		if !needed && !strings.HasPrefix(ref.Kind, "test:") {
			return todoTeach(field, "cite only a criterion or scenario in the current TODO content", ErrConflict)
		}
		if ref.Source != "external-evidence" || strings.TrimSpace(ref.Reference) == "" || ref.VerificationState != "unverified" {
			return todoTeach(field, "record a concrete external-evidence reference for the current digest as unverified; do not claim verified test execution", ErrConflict)
		}
		if needed {
			if required[ref.Kind] {
				return todoTeach(field, "record exactly one current-digest external citation", ErrConflict)
			}
			required[ref.Kind] = true
		}
	}
	for kind, found := range required {
		if !found {
			return todoTeach("proof_refs."+kind, "record one external-evidence citation bound to the current TODO content digest", ErrConflict)
		}
	}
	return nil
}

// Only next-step edits may occur between preflight and the guarded transition.
func validateLocalProofSubject(ctx context.Context, database *mongo.Database, companyID string, record TODORecord, digest string, result *LocalProofResult) error {
	if result == nil || result.SchemaVersion != "builder-local-proof-result-v3" {
		return todoTeach("local_proof.schema_version", "run the current v3 PACED runner against the exact Mongo TODO preflight", ErrConflict)
	}
	if result.CompanyID != companyID || result.ProjectID != record.ProjectID || result.TODOID != record.ID || result.TODORevision == nil || *result.TODORevision < 0 || *result.TODORevision > record.Revision || result.ContentDigest != digest || result.ReleaseID != record.ReleaseID || result.Position == nil || record.Position == nil || *result.Position != *record.Position {
		return todoTeach("local_proof.subject", "match the preflight Company, Project, TODO, content digest and current Release placement", ErrConflict)
	}
	if record.Revision-*result.TODORevision > 256 {
		return todoTeach("local_proof.todo_revision", "rerun against the current TODO after too many intervening changes", ErrConflict)
	}
	if *result.TODORevision == record.Revision {
		return nil
	}
	filter := bson.D{{Key: "project_id", Value: record.ProjectID}, {Key: "todo_id", Value: record.ID}, {Key: "revision", Value: bson.D{{Key: "$gt", Value: *result.TODORevision}, {Key: "$lte", Value: record.Revision}}}}
	cursor, err := database.Collection(todoEventsCollection).Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "revision", Value: 1}}))
	if err != nil {
		return ErrUnavailable
	}
	defer cursor.Close(ctx)
	expected := *result.TODORevision + 1
	for cursor.Next(ctx) {
		var event TODOHistoryEvent
		if cursor.Decode(&event) != nil {
			return ErrUnavailable
		}
		changes := event.Changes
		nextStep := changes.NextExactStep
		changes.NextExactStep = nil
		if event.Revision != expected || event.Kind != "state_changed" || nextStep == nil || !reflect.DeepEqual(changes, TODOHistoryChanges{}) || event.ContentRevision != nil || event.ProofReference != nil {
			return todoTeach("local_proof.todo_revision", "rerun after any intervening change other than next_exact_step", ErrConflict)
		}
		expected++
	}
	if cursor.Err() != nil {
		return ErrUnavailable
	}
	if expected != record.Revision+1 {
		return todoTeach("local_proof.todo_revision", "retain a complete history of next-step-only changes or rerun", ErrConflict)
	}
	return nil
}

// verifyLocalProofHeads proves that the checked checkout heads were already
// reachable from their immutable Release-owned branches. It deliberately does
// not require those branches to remain at the same tip after the local run.
func (m *MongoStore) verifyLocalProofHeads(ctx context.Context, companyID string, todo TODORecord, branchPlan map[string]string, result *LocalProofResult) error {
	for i, repository := range result.Repositories {
		branch := branchPlan[repository.RepositoryID]
		if !validDeliveryBranch(branch) || repository.Branch != branch {
			return todoTeach(fmt.Sprintf("local_proof.repositories[%d].branch", i), "match the exact TODO-selected Release branch", ErrConflict)
		}
		if _, err := m.readRemoteCommit(ctx, companyID, todo.ProjectID, repository.RepositoryID, branch, repository.HeadBefore, true); err != nil {
			return todoTeach(fmt.Sprintf("local_proof.repositories[%d].head_before", i), "prove the tested checkout head is already published on its Release-owned branch", ErrConflict)
		}
	}
	return nil
}

type remoteCommitResult struct {
	CompanyID     string   `json:"company_id"`
	ProjectID     string   `json:"project_id"`
	Repository    string   `json:"repository"`
	Branch        string   `json:"branch"`
	CommitSHA     string   `json:"commit_sha"`
	TestedHeadSHA string   `json:"tested_head_sha,omitempty"`
	BaselineSHA   string   `json:"baseline_sha,omitempty"`
	RemoteHead    string   `json:"remote_head"`
	ParentSHA     string   `json:"parent_sha"`
	Paths         []string `json:"paths"`
	Outcome       string   `json:"outcome"`
}

// The private Reader admits up to eleven GitHub commit pages (100 files each).
// Their complete path list can exceed the former 32 KiB transport limit even
// when the commit is attributable. Keep the response bounded by that upstream
// page budget instead of rejecting a valid large commit as an outage.
const maxRemoteCommitProofBytes = 12 << 20

// The approved scope, not the current checkout diff, decides attribution.
func (m *MongoStore) verifyRemoteLocalDelivery(ctx context.Context, companyID, foundationRepositoryID string, todo TODORecord, contentDigest string, local TODOProofReference) (TODOProofReference, error) {
	branchPlan := map[string]string{}
	foundationOnly := todo.Source.Kind != "mongo-content-revision" && len(todo.DiffExpectation) > 0
	for _, scope := range todo.DiffExpectation {
		if !foundationScope(scope.Repository) {
			foundationOnly = false
			break
		}
	}
	if !foundationOnly {
		var err error
		branchPlan, err = confirmedTODOReleaseBranches(ctx, m, companyID, todo)
		if err != nil {
			return TODOProofReference{}, err
		}
	}
	return m.verifyRemoteLocalDeliveryWithBranchPlan(ctx, companyID, foundationRepositoryID, todo, contentDigest, local, branchPlan)
}

func (m *MongoStore) verifyRemoteLocalDeliveryWithBranchPlan(ctx context.Context, companyID, foundationRepositoryID string, todo TODORecord, contentDigest string, local TODOProofReference, branchPlan map[string]string, testedHeadPlans ...map[string]string) (TODOProofReference, error) {
	if len(todo.ImplementationCommits) > maxLocalDeliveryCommits {
		return TODOProofReference{}, todoTeach("implementation_commits", "limit one local delivery to 32 attributable commits before remote verification", ErrInvalid)
	}
	if m.proofReaderURL == "" || m.proofReaderToken == "" || m.proofHTTPClient == nil {
		return TODOProofReference{}, todoTeach("remote_proof", "configure the private Project Reader before Local-Implemented", ErrUnavailable)
	}
	parts := strings.SplitN(foundationRepositoryID, "/", 2)
	if todo.Source.Kind != "mongo-content-revision" && (len(parts) != 2 || parts[0] == "" || parts[1] == "") {
		return TODOProofReference{}, todoTeach("remote_proof", "use the registered Project Foundation repository owner", ErrConflict)
	}
	legacyFoundation := todo.Source.Kind != "mongo-content-revision"
	codeOnly := todo.Content != nil && todo.Content.SchemaVersion == todoContentSchemaVersion && local.Kind == "" && local.Source == ""
	foundationOnly := legacyFoundation && len(todo.DiffExpectation) > 0
	for _, scope := range todo.DiffExpectation {
		if !foundationScope(scope.Repository) {
			foundationOnly = false
			break
		}
	}
	if !foundationOnly && !codeOnly && (local.Kind != "local-run-verified" || local.VerificationState != "verified") {
		return TODOProofReference{}, todoTeach("local_proof", "pass the fixed PACED local run before remote code proof", ErrConflict)
	}
	var testedHeads map[string]string
	if len(testedHeadPlans) > 0 {
		testedHeads = testedHeadPlans[0]
	}
	typedMongoDelivery := todo.Content != nil && todo.Content.SchemaVersion == todoContentSchemaVersion
	var verified []remoteCommitResult
	for _, scope := range todo.DiffExpectation {
		if legacyFoundation && foundationScope(scope.Repository) && !foundationOnly {
			continue
		}
		branch, repository := "", scope.RepositoryID
		if legacyFoundation && foundationScope(scope.Repository) {
			branch, repository = "main", foundationRepositoryID
		} else {
			if !validGitRepositoryID(repository) {
				return TODOProofReference{}, todoTeach("diff_expectation.repository_id", "select the exact confirmed implementation repository for this Project", ErrConflict)
			}
			branch = branchPlan[repository]
			if !validDeliveryBranch(branch) {
				return TODOProofReference{}, todoTeach("release_id.delivery_branches."+repository, "confirm this repository's branch on the exact Release before local proof", ErrConflict)
			}
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
			var result remoteCommitResult
			var err error
			if typedMongoDelivery && !codeOnly {
				testedHead := testedHeads[repository]
				if !validGitCommit(testedHead) {
					return TODOProofReference{}, todoTeach("local_proof.repositories.head_before", "bind every implementation commit to that repository's tested checkout head", ErrConflict)
				}
				result, err = m.readRemoteCommit(ctx, companyID, todo.ProjectID, repository, branch, claim.Commit, true, testedHead)
			} else {
				if codeOnly {
					result, err = m.readRemoteCommit(ctx, companyID, todo.ProjectID, repository, branch, claim.Commit, true, "", scope.Baseline)
				} else {
					result, err = m.readRemoteCommit(ctx, companyID, todo.ProjectID, repository, branch, claim.Commit, false)
				}
			}
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
	var data []byte
	var err error
	if codeOnly {
		data, err = json.Marshal(struct {
			CompanyID     string               `json:"company_id"`
			ProjectID     string               `json:"project_id"`
			TODOID        string               `json:"todo_id"`
			ContentDigest string               `json:"content_digest"`
			Remote        []remoteCommitResult `json:"remote"`
		}{companyID, todo.ProjectID, todo.ID, contentDigest, verified})
	} else {
		data, err = json.Marshal(struct {
			Local  TODOProofReference   `json:"local"`
			Remote []remoteCommitResult `json:"remote"`
		}{local, verified})
	}
	if err != nil {
		return TODOProofReference{}, ErrInvalid
	}
	claims := make([]string, 0, len(verified)+1)
	if !foundationOnly && !codeOnly {
		claims = append(claims, "run="+local.Reference)
	}
	for _, result := range verified {
		claims = append(claims, result.Repository+"@"+result.Branch+":"+result.CommitSHA+"/parent="+result.ParentSHA+"/head="+result.RemoteHead)
	}
	kind, source := "remote-local-delivery", "private-reader-and-paced-runner"
	if foundationOnly {
		kind, source = "foundation-publication", "private-reader"
	} else if codeOnly {
		kind, source = "remote-code-delivery", "private-reader"
	}
	return TODOProofReference{Kind: kind, Source: source, Reference: strings.Join(claims, ";"), SourceDigest: sourceHash(data), VerificationState: "verified"}, nil
}

func validGitRepositoryID(value string) bool {
	parts := strings.Split(value, "/")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") || !branchPattern.MatchString(part) || strings.Contains(part, "/") || strings.Contains(part, "..") {
			return false
		}
	}
	return true
}

func validDeliveryBranch(value string) bool {
	return value != "" && !strings.HasPrefix(value, "/") && !strings.HasSuffix(value, "/") && !strings.Contains(value, "..") && branchPattern.MatchString(value)
}

func foundationScope(repository string) bool {
	return repository == "Foundation" || strings.HasSuffix(repository, "_foundation_documentation")
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
	patternParts := strings.Split(pattern, "/")
	changedParts := strings.Split(changed, "/")
	type position struct{ pattern, changed int }
	memo := make(map[position]bool)
	visited := make(map[position]bool)
	var match func(int, int) bool
	match = func(patternIndex, changedIndex int) bool {
		at := position{patternIndex, changedIndex}
		if visited[at] {
			return memo[at]
		}
		visited[at] = true
		if patternIndex == len(patternParts) {
			memo[at] = changedIndex == len(changedParts)
			return memo[at]
		}
		if patternParts[patternIndex] == "**" {
			memo[at] = match(patternIndex+1, changedIndex) || changedIndex < len(changedParts) && match(patternIndex, changedIndex+1)
			return memo[at]
		}
		if changedIndex == len(changedParts) {
			return false
		}
		matched, err := path.Match(patternParts[patternIndex], changedParts[changedIndex])
		memo[at] = err == nil && matched && match(patternIndex+1, changedIndex+1)
		return memo[at]
	}
	return match(0, 0)
}

func (m *MongoStore) readRemoteCommit(ctx context.Context, companyID, projectID, repository, branch, sha string, mongoV2 bool, proofBounds ...string) (remoteCommitResult, error) {
	q := url.Values{"company_id": {companyID}, "project_id": {projectID}, "repository": {repository}, "branch": {branch}, "commit_sha": {sha}}
	if mongoV2 {
		q.Set("todo_authority", "mongo-v2")
	}
	if len(proofBounds) > 2 {
		return remoteCommitResult{}, ErrInvalid
	}
	if len(proofBounds) == 1 && proofBounds[0] != "" {
		if !validGitCommit(proofBounds[0]) || !mongoV2 {
			return remoteCommitResult{}, ErrInvalid
		}
		q.Set("tested_head_sha", proofBounds[0])
	}
	if len(proofBounds) == 2 {
		if proofBounds[0] != "" || !validGitCommit(proofBounds[1]) || !mongoV2 {
			return remoteCommitResult{}, ErrInvalid
		}
		q.Set("baseline_sha", proofBounds[1])
	}
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
	data, err := io.ReadAll(io.LimitReader(response.Body, maxRemoteCommitProofBytes+1))
	if err != nil || len(data) == 0 {
		return remoteCommitResult{}, ErrUnavailable
	}
	if len(data) > maxRemoteCommitProofBytes {
		return remoteCommitResult{}, todoTeach("remote_proof", "the private Reader commit proof exceeds the bounded response; split the attributable delivery into smaller commits", ErrConflict)
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
	if json.Unmarshal(data, &result) != nil || result.CompanyID != companyID || result.ProjectID != projectID || result.Repository != repository || result.Branch != branch || result.CommitSHA != sha || len(proofBounds) == 1 && result.TestedHeadSHA != proofBounds[0] || len(proofBounds) == 2 && result.BaselineSHA != proofBounds[1] || !validGitCommit(result.RemoteHead) || !validGitCommit(result.ParentSHA) || len(result.Paths) == 0 || result.Outcome != "verified" {
		return remoteCommitResult{}, todoTeach("remote_proof", "match the exact repository, branch and attributable commit returned by the private Reader", ErrConflict)
	}
	return result, nil
}

func (m *MongoStore) verifyPlanningCodeAnchors(ctx context.Context, companyID, projectID string, content TODOAuthoredContent) error {
	for i, assumption := range content.Planning.Assumptions {
		if assumption.Status != "live" {
			continue
		}
		field := fmt.Sprintf("content.planning.assumptions[%d].code_refs", i)
		verified := false
		for _, ref := range assumption.CodeRefs {
			if !planningCodeLike(ref.Path) {
				continue
			}
			for _, scope := range content.DiffExpectation {
				if ref.RepositoryID != scope.RepositoryID || !validGitCommit(scope.Baseline) || !approvedCommitPath(ref.Path, scope) {
					continue
				}
				if m.proofReaderURL == "" || m.proofReaderToken == "" || m.proofHTTPClient == nil {
					return todoTeach(field, "configure the private Reader to verify a code/test file at the approved repository baseline", ErrUnavailable)
				}
				q := url.Values{"company_id": {companyID}, "project_id": {projectID}, "repository": {ref.RepositoryID}, "baseline": {scope.Baseline}, "path": {ref.Path}}
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.proofReaderURL+"/internal/v1/proofs/code-anchor?"+q.Encode(), nil)
				if err != nil {
					return ErrUnavailable
				}
				req.Header.Set("Authorization", "Bearer "+m.proofReaderToken)
				response, err := m.proofHTTPClient.Do(req)
				if err != nil {
					return todoTeach(field, "retry when the private Reader can verify the approved baseline", ErrUnavailable)
				}
				data, readErr := io.ReadAll(io.LimitReader(response.Body, 16<<10))
				_ = response.Body.Close()
				var result struct {
					CompanyID  string `json:"company_id"`
					ProjectID  string `json:"project_id"`
					Repository string `json:"repository"`
					Baseline   string `json:"baseline"`
					Path       string `json:"path"`
					Type       string `json:"type"`
				}
				if readErr == nil && len(data) < 16<<10 && response.StatusCode == http.StatusOK && json.Unmarshal(data, &result) == nil && result.CompanyID == companyID && result.ProjectID == projectID && result.Repository == ref.RepositoryID && result.Baseline == scope.Baseline && result.Path == ref.Path && result.Type == "file" {
					verified = true
					break
				}
			}
			if verified {
				break
			}
		}
		if !verified {
			return todoTeach(field, "verify at least one code/test file in an approved implementing repository at its exact baseline", ErrConflict)
		}
	}
	return nil
}

func planningCodeLike(value string) bool {
	if strings.Contains("/"+value, "/test/") {
		return true
	}
	switch strings.ToLower(path.Ext(value)) {
	case ".c", ".cc", ".cpp", ".cs", ".dart", ".go", ".h", ".hpp", ".java", ".js", ".jsx", ".kt", ".m", ".mm", ".php", ".py", ".rb", ".rs", ".sh", ".sql", ".swift", ".ts", ".tsx":
		return true
	default:
		return false
	}
}
