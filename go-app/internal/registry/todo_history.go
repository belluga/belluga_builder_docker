package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	todoContentVersionsCollection = "todo_content_versions"
	todoEventsCollection          = "todo_events"
)

func ValidCompanyID(value string) bool { return validID(value) }

// TODOAuthoredContent is the immutable authored portion of a TODO. Operational
// state and imported Git provenance intentionally remain outside this value.
type TODOAuthoredContent struct {
	TODOContent     `bson:",inline"`
	Title           string                `json:"title" bson:"title"`
	Objective       string                `json:"objective" bson:"objective"`
	Sections        []TODOSection         `json:"sections,omitempty" bson:"sections,omitempty"`
	DiffExpectation []TODORepositoryScope `json:"diff_expectation" bson:"diff_expectation"`
	AffectedFiles   []string              `json:"affected_files" bson:"affected_files"`
}

// MarshalBSON preserves the strict v1 authored-content shape while retaining
// explicitly empty arrays in complete v2 content. BSON's omitempty otherwise
// drops empty legacy sections and v2 decisions/risks/references.
func (content TODOAuthoredContent) MarshalBSON() ([]byte, error) {
	document := bson.D{}
	if content.SchemaVersion != 0 {
		encoded, err := bson.Marshal(content.TODOContent)
		if err != nil {
			return nil, err
		}
		elements, err := bson.Raw(encoded).Elements()
		if err != nil {
			return nil, err
		}
		for _, element := range elements {
			document = append(document, bson.E{Key: element.Key(), Value: element.Value()})
		}
		if content.Decisions != nil && len(content.Decisions) == 0 {
			document = append(document, bson.E{Key: "decisions", Value: []TODOContentItem{}})
		}
		if content.Risks != nil && len(content.Risks) == 0 {
			document = append(document, bson.E{Key: "risks", Value: []TODOContentRisk{}})
		}
		if content.References != nil && len(content.References) == 0 {
			document = append(document, bson.E{Key: "references", Value: []TODOContentReference{}})
		}
	}
	document = append(document,
		bson.E{Key: "title", Value: content.Title},
		bson.E{Key: "objective", Value: content.Objective},
	)
	if content.SchemaVersion == 0 {
		sections := content.Sections
		if sections == nil {
			sections = []TODOSection{}
		}
		document = append(document, bson.E{Key: "sections", Value: sections})
	}
	document = append(document,
		bson.E{Key: "diff_expectation", Value: content.DiffExpectation},
		bson.E{Key: "affected_files", Value: content.AffectedFiles},
	)
	return bson.Marshal(document)
}

type TODOContentVersion struct {
	ObjectID  bson.ObjectID       `json:"id" bson:"_id,omitempty"`
	ProjectID string              `json:"project_id" bson:"project_id"`
	TODOID    string              `json:"todo_id" bson:"todo_id"`
	Revision  int64               `json:"revision" bson:"revision"`
	Digest    string              `json:"digest,omitempty" bson:"digest,omitempty"`
	Content   TODOAuthoredContent `json:"content" bson:"content"`
}

type TODOHistoryChanges struct {
	Stage                      *string                `json:"stage,omitempty" bson:"stage,omitempty"`
	Qualifiers                 *[]string              `json:"qualifiers,omitempty" bson:"qualifiers,omitempty"`
	WorkState                  *string                `json:"work_state,omitempty" bson:"work_state,omitempty"`
	NextExactStep              *string                `json:"next_exact_step,omitempty" bson:"next_exact_step,omitempty"`
	StageVerification          *string                `json:"stage_verification,omitempty" bson:"stage_verification,omitempty"`
	ProofReferences            *[]TODOProofReference  `json:"proof_refs,omitempty" bson:"proof_refs,omitempty"`
	WaiverReferences           *[]TODOProofReference  `json:"waiver_refs,omitempty" bson:"waiver_refs,omitempty"`
	ImplementationCommits      *[]TODOCommitReference `json:"implementation_commits,omitempty" bson:"implementation_commits,omitempty"`
	VerifiedTransitionRef      *TODOProofReference    `json:"verified_transition_ref,omitempty" bson:"verified_transition_ref,omitempty"`
	ClearVerifiedTransitionRef bool                   `json:"clear_verified_transition_ref,omitempty" bson:"clear_verified_transition_ref,omitempty"`
	RecordSections             *[]TODOSection         `json:"record_sections,omitempty" bson:"record_sections,omitempty"`
	ReleaseID                  *string                `json:"release_id,omitempty" bson:"release_id,omitempty"`
	Position                   *int                   `json:"position,omitempty" bson:"position,omitempty"`
	ClearPlacement             bool                   `json:"clear_placement,omitempty" bson:"clear_placement,omitempty"`
}

type TODOHistoryEvent struct {
	ObjectID            bson.ObjectID       `json:"id" bson:"_id,omitempty"`
	ProjectID           string              `json:"project_id" bson:"project_id"`
	TODOID              string              `json:"todo_id" bson:"todo_id"`
	Revision            int64               `json:"revision" bson:"revision"`
	Kind                string              `json:"kind" bson:"kind"`
	OccurredAt          time.Time           `json:"occurred_at" bson:"occurred_at"`
	OperationID         string              `json:"operation_id" bson:"operation_id"`
	RequestDigest       string              `json:"request_digest" bson:"request_digest"`
	Changes             TODOHistoryChanges  `json:"changes" bson:"changes"`
	ContentRevision     *int64              `json:"content_revision,omitempty" bson:"content_revision,omitempty"`
	FromContentRevision *int64              `json:"from_content_revision,omitempty" bson:"from_content_revision,omitempty"`
	FromContentDigest   string              `json:"from_content_digest,omitempty" bson:"from_content_digest,omitempty"`
	ToContentDigest     string              `json:"to_content_digest,omitempty" bson:"to_content_digest,omitempty"`
	ReviewedAcceptance  string              `json:"reviewed_acceptance,omitempty" bson:"reviewed_acceptance,omitempty"`
	ActorClaim          *TODOActorClaim     `json:"actor_claim,omitempty" bson:"actor_claim,omitempty"`
	PermissionDecision  string              `json:"permission_decision,omitempty" bson:"permission_decision,omitempty"`
	Reason              string              `json:"reason,omitempty" bson:"reason,omitempty"`
	ProofReference      *TODOProofReference `json:"proof_reference,omitempty" bson:"proof_reference,omitempty"`
}

type TODOActorClaim struct {
	Value              string `json:"value" bson:"value"`
	VerificationStatus string `json:"verification_status" bson:"verification_status"`
}

type TODOHistoryPage struct {
	CompanyID string             `json:"company_id"`
	ProjectID string             `json:"project_id"`
	TODOID    string             `json:"todo_id"`
	Events    []TODOHistoryEvent `json:"events"`
	NextAfter *int64             `json:"next_after_revision,omitempty"`
}

type TODOAsOfState struct {
	Stage                 string                `json:"stage"`
	Qualifiers            []string              `json:"qualifiers"`
	WorkState             string                `json:"work_state"`
	NextExactStep         string                `json:"next_exact_step"`
	StageVerification     string                `json:"stage_verification"`
	ProofReferences       []TODOProofReference  `json:"proof_refs"`
	WaiverReferences      []TODOProofReference  `json:"waiver_refs"`
	ImplementationCommits []TODOCommitReference `json:"implementation_commits"`
	VerifiedTransitionRef *TODOProofReference   `json:"verified_transition_ref,omitempty"`
	ReleaseID             string                `json:"release_id"`
	Position              int                   `json:"position"`
	PlacementAssigned     bool                  `json:"placement_assigned"`
	RecordSections        []TODOSection         `json:"record_sections"`
}

type TODOAsOfResponse struct {
	CompanyID string              `json:"company_id"`
	ProjectID string              `json:"project_id"`
	TODOID    string              `json:"todo_id"`
	Revision  int64               `json:"revision"`
	Content   TODOAuthoredContent `json:"content"`
	State     TODOAsOfState       `json:"state"`
}

type TODOContentComparison struct {
	CompanyID string   `json:"company_id"`
	ProjectID string   `json:"project_id"`
	TODOID    string   `json:"todo_id"`
	From      int64    `json:"from_revision"`
	To        int64    `json:"to_revision"`
	Changed   []string `json:"changed_fields"`
}

type TODOContentEditRequest struct {
	ProjectID        string              `json:"project_id"`
	TODOID           string              `json:"todo_id"`
	ExpectedRevision *int64              `json:"expected_revision"`
	OperationID      string              `json:"operation_id"`
	Content          TODOAuthoredContent `json:"content"`
	Reason           string              `json:"reason"`
}

type TODOContentEditResponse struct {
	CompanyID       string `json:"company_id"`
	ProjectID       string `json:"project_id"`
	TODOID          string `json:"todo_id"`
	Revision        int64  `json:"todo_revision"`
	ContentRevision *int64 `json:"content_revision,omitempty"`
	ContentDigest   string `json:"content_digest,omitempty"`
	NoOp            bool   `json:"no_op"`
}

type TODOContentRestoreRequest struct {
	ProjectID        string `json:"project_id"`
	TODOID           string `json:"todo_id"`
	ExpectedRevision *int64 `json:"expected_revision"`
	OperationID      string `json:"operation_id"`
	ContentRevision  int64  `json:"content_revision"`
	Reason           string `json:"reason"`
}

type TODOHistoryProjectInventory struct {
	ProjectID string `json:"project_id"`
	TODOs     int64  `json:"todos"`
}

type TODOHistoryPreflightReport struct {
	CompanyID            string                        `json:"company_id"`
	Projects             []TODOHistoryProjectInventory `json:"projects"`
	TODOs                int64                         `json:"todos"`
	ContentVersionsState string                        `json:"content_versions_state"`
	ContentVersions      int64                         `json:"content_versions"`
	EventsState          string                        `json:"events_state"`
	Events               int64                         `json:"events"`
	Baselines            int64                         `json:"baselines"`
	CoverageComplete     bool                          `json:"coverage_complete"`
}

func contentFromTODO(record TODORecord) TODOAuthoredContent {
	if record.Content != nil {
		content := *record.Content
		return TODOAuthoredContent{
			TODOContent: content, Title: record.Title, Objective: record.Objective,
			DiffExpectation: append([]TODORepositoryScope{}, record.DiffExpectation...),
			AffectedFiles:   append([]string{}, record.AffectedFiles...),
		}
	}
	sections := make([]TODOSection, 0, len(record.Sections))
	for _, section := range record.Sections {
		if sectionOwnership(section) == "contract" && !historyRecordSection(section.Key, section.Title) && !historyRenderOnlySection(section.Key, section.Title) {
			section.Ownership = "contract"
			section.Position = len(sections)
			sections = append(sections, section)
		}
	}
	return TODOAuthoredContent{
		Title: record.Title, Objective: record.Objective, Sections: sections,
		DiffExpectation: append([]TODORepositoryScope{}, record.DiffExpectation...),
		AffectedFiles:   append([]string{}, record.AffectedFiles...),
	}
}

func withContractOwnership(content TODOAuthoredContent) TODOAuthoredContent {
	for i := range content.Sections {
		content.Sections[i].Ownership = "contract"
	}
	return content
}

func sectionOwnership(section TODOSection) string {
	if section.Ownership == "contract" || section.Ownership == "record" {
		return section.Ownership
	}
	if historyRenderOnlySection(section.Key, section.Title) || historyRecordSection(section.Key, section.Title) {
		return "record"
	}
	return ""
}

func validateExistingSectionOwnership(record TODORecord) error {
	for i, section := range record.Sections {
		if sectionOwnership(section) == "" {
			return todoTeach(fmt.Sprintf("sections[%d].ownership", i), "classify this legacy section explicitly before editing; its ownership is ambiguous", ErrConflict)
		}
	}
	return nil
}

func historyRecordSection(key, title string) bool {
	if strings.HasPrefix(key, "execution-checkpoint-") || strings.HasPrefix(title, "Execution checkpoint") {
		return true
	}
	switch key {
	case "approval", "implementation-evidence", "implementation-commits", "completion-evidence-matrix", "review-results", "guard-results":
		return true
	}
	switch title {
	case "Approval", "Implementation Evidence", "Implementation Commits", "Completion Evidence Matrix", "Review Results", "Guard Results":
		return true
	default:
		return false
	}
}

func historyRenderOnlySection(key, title string) bool {
	switch key {
	case "delivery-status-canon", "active-work-state":
		return true
	}
	switch title {
	case "Delivery Status Canon", "Active Work State":
		return true
	default:
		return false
	}
}

func observedTODOChanges(record TODORecord) TODOHistoryChanges {
	stage, work, step, verification := record.Stage, record.WorkState, record.NextExactStep, record.StageVerification
	qualifiers := append([]string{}, record.Qualifiers...)
	proofs := append([]TODOProofReference{}, record.ProofReferences...)
	waivers := append([]TODOProofReference{}, record.WaiverReferences...)
	commits := append([]TODOCommitReference{}, record.ImplementationCommits...)
	recordSections := make([]TODOSection, 0)
	for _, section := range record.Sections {
		if sectionOwnership(section) == "record" {
			section.Ownership = "record"
			recordSections = append(recordSections, section)
		}
	}
	releaseID := record.ReleaseID
	position := 0
	if record.Position != nil {
		position = *record.Position
	}
	changes := TODOHistoryChanges{Stage: &stage, Qualifiers: &qualifiers, WorkState: &work, NextExactStep: &step,
		StageVerification: &verification, ProofReferences: &proofs, WaiverReferences: &waivers, ImplementationCommits: &commits,
		RecordSections: &recordSections, ReleaseID: &releaseID, Position: &position, ClearPlacement: record.ReleaseID == ""}
	if record.VerifiedTransitionRef != nil {
		proof := *record.VerifiedTransitionRef
		changes.VerifiedTransitionRef = &proof
	} else {
		changes.ClearVerifiedTransitionRef = true
	}
	return changes
}

// ProvisionTODOHistory creates and verifies the immutable collections before
// history writes are enabled, then baselines every currently admitted TODO at
// its actual aggregate revision. It is safe to repeat at service startup.
func (m *MongoStore) ProvisionTODOHistory(ctx context.Context, companyID string) error {
	if !validID(companyID) {
		return todoTeach("company_id", "name an existing Company tenant", ErrInvalid)
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return err
	}
	if company.DatabaseName != m.tenantDatabase(companyID) {
		return todoTeach("company.database_name", "match the server-owned Company tenant database", ErrConflict)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	database := m.client.Database(company.DatabaseName)
	if err := upgradeTODOCollectionForHistory(ctx, database); err != nil {
		return err
	}
	if err := verifyTODOIndexes(ctx, database.Collection(todosCollection)); err != nil {
		return err
	}
	if err := provisionHistoryCollection(ctx, database, todoContentVersionsCollection, TODOContentVersionsValidator(), todoContentVersionIndexes()); err != nil {
		return err
	}
	if err := provisionHistoryCollection(ctx, database, todoEventsCollection, TODOEventsValidator(), todoEventIndexes()); err != nil {
		return err
	}
	if err := verifyTODOHistoryCollections(ctx, database); err != nil {
		return err
	}
	cursor, err := database.Collection(todosCollection).Find(ctx, bson.D{})
	if err != nil {
		return ErrUnavailable
	}
	defer cursor.Close(ctx)
	for cursor.Next(ctx) {
		var record TODORecord
		if cursor.Decode(&record) != nil {
			return ErrUnavailable
		}
		if err := validateStoredTODO(record); err != nil {
			return err
		}
		if err := m.ensureTODOHistoryBaseline(ctx, database, record); err != nil {
			return err
		}
	}
	if cursor.Err() != nil {
		return ErrUnavailable
	}
	return nil
}

// PreflightTODOHistory is read-only. It verifies the server-resolved tenant,
// current TODO schema and any existing history collections before migration.
func (m *MongoStore) PreflightTODOHistory(ctx context.Context, companyID string) (TODOHistoryPreflightReport, error) {
	database, err := m.todoTenantDatabase(ctx, companyID)
	if err != nil {
		return TODOHistoryPreflightReport{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := verifyTODOCollection(ctx, database); err != nil {
		return TODOHistoryPreflightReport{}, err
	}
	if err := verifyTODOIndexes(ctx, database.Collection(todosCollection)); err != nil {
		return TODOHistoryPreflightReport{}, err
	}
	todoCursor, err := database.Collection(todosCollection).Find(ctx, bson.D{})
	if err != nil {
		return TODOHistoryPreflightReport{}, ErrUnavailable
	}
	for todoCursor.Next(ctx) {
		var record TODORecord
		if todoCursor.Decode(&record) != nil {
			_ = todoCursor.Close(ctx)
			return TODOHistoryPreflightReport{}, ErrUnavailable
		}
		if err := validateStoredTODO(record); err != nil {
			_ = todoCursor.Close(ctx)
			return TODOHistoryPreflightReport{}, err
		}
		if record.ReleaseID != "" {
			if err := validateTODOReleaseMembership(ctx, database, companyID, record.ProjectID, record.ReleaseID); err != nil {
				_ = todoCursor.Close(ctx)
				return TODOHistoryPreflightReport{}, err
			}
		} else if record.Source.Kind != "mongo-content-revision" || validateTODOProjectMembership(ctx, database, companyID, record.ProjectID) != nil {
			_ = todoCursor.Close(ctx)
			return TODOHistoryPreflightReport{}, todoTeach("release_id/position", "be assigned or be jointly absent only for a Mongo-born Backlog TODO", ErrConflict)
		}
	}
	if todoCursor.Err() != nil {
		_ = todoCursor.Close(ctx)
		return TODOHistoryPreflightReport{}, ErrUnavailable
	}
	if err := todoCursor.Close(ctx); err != nil {
		return TODOHistoryPreflightReport{}, ErrUnavailable
	}
	distinctResult := database.Collection(todosCollection).Distinct(ctx, "project_id", bson.D{})
	var projectIDsRaw []any
	if err := distinctResult.Decode(&projectIDsRaw); err != nil {
		return TODOHistoryPreflightReport{}, ErrUnavailable
	}
	projectIDs := make([]string, 0, len(projectIDsRaw))
	for _, value := range projectIDsRaw {
		projectID, ok := value.(string)
		if !ok || !validID(projectID) {
			return TODOHistoryPreflightReport{}, todoTeach("todos.project_id", "contain valid Project identifiers before history migration", ErrConflict)
		}
		projectIDs = append(projectIDs, projectID)
	}
	sort.Strings(projectIDs)
	report := TODOHistoryPreflightReport{CompanyID: companyID, Projects: []TODOHistoryProjectInventory{}, ContentVersionsState: "absent", EventsState: "absent"}
	for _, projectID := range projectIDs {
		count, err := database.Collection(todosCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: projectID}})
		if err != nil {
			return TODOHistoryPreflightReport{}, ErrUnavailable
		}
		report.Projects = append(report.Projects, TODOHistoryProjectInventory{ProjectID: projectID, TODOs: count})
		report.TODOs += count
	}
	for _, item := range []struct {
		name      string
		validator bson.D
		indexes   []historyIndexSpec
	}{
		{todoContentVersionsCollection, TODOContentVersionsValidator(), todoContentVersionIndexes()},
		{todoEventsCollection, TODOEventsValidator(), todoEventIndexes()},
	} {
		collections, err := database.ListCollectionSpecifications(ctx, bson.D{{Key: "name", Value: item.name}})
		if err != nil {
			return TODOHistoryPreflightReport{}, ErrUnavailable
		}
		if len(collections) == 0 {
			continue
		}
		if err := verifyTODOHistoryCollections(ctx, database); err != nil {
			return TODOHistoryPreflightReport{}, err
		}
		count, err := database.Collection(item.name).CountDocuments(ctx, bson.D{})
		if err != nil {
			return TODOHistoryPreflightReport{}, ErrUnavailable
		}
		if item.name == todoContentVersionsCollection {
			report.ContentVersionsState, report.ContentVersions = "provisioned_and_verified", count
		} else {
			report.EventsState, report.Events = "provisioned_and_verified", count
		}
	}
	if report.EventsState == "provisioned_and_verified" {
		count, err := database.Collection(todoEventsCollection).CountDocuments(ctx, bson.D{{Key: "kind", Value: "baseline"}})
		if err != nil {
			return TODOHistoryPreflightReport{}, ErrUnavailable
		}
		report.Baselines = count
	}
	if report.ContentVersionsState == "provisioned_and_verified" && report.EventsState == "provisioned_and_verified" && report.Baselines == report.TODOs && report.ContentVersions >= report.Baselines {
		report.CoverageComplete, err = verifyTODOHistoryCoverage(ctx, database)
		if err != nil {
			return TODOHistoryPreflightReport{}, err
		}
	}
	return report, nil
}

func verifyTODOHistoryCoverage(ctx context.Context, database *mongo.Database) (bool, error) {
	cursor, err := database.Collection(todosCollection).Find(ctx, bson.D{})
	if err != nil {
		return false, ErrUnavailable
	}
	defer cursor.Close(ctx)
	for cursor.Next(ctx) {
		var todo TODORecord
		if cursor.Decode(&todo) != nil {
			return false, ErrUnavailable
		}
		baseline, err := readHistoryBaseline(ctx, database, todo.ProjectID, todo.ID)
		if errors.Is(err, mongo.ErrNoDocuments) {
			return false, nil
		}
		if err != nil {
			return false, ErrUnavailable
		}
		if baseline.Revision > todo.Revision || baseline.ContentRevision == nil {
			return false, nil
		}
		var version TODOContentVersion
		err = database.Collection(todoContentVersionsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: todo.ProjectID}, {Key: "todo_id", Value: todo.ID}, {Key: "revision", Value: *baseline.ContentRevision}}).Decode(&version)
		if errors.Is(err, mongo.ErrNoDocuments) {
			return false, nil
		}
		if err != nil {
			return false, ErrUnavailable
		}
	}
	if cursor.Err() != nil {
		return false, ErrUnavailable
	}
	return true, nil
}

func readHistoryBaseline(ctx context.Context, database *mongo.Database, projectID, todoID string) (TODOHistoryEvent, error) {
	var event TODOHistoryEvent
	err := database.Collection(todoEventsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "todo_id", Value: todoID}, {Key: "kind", Value: "baseline"}}).Decode(&event)
	return event, err
}

type historyIndexSpec struct {
	name   string
	keys   bson.D
	unique bool
}

func provisionHistoryCollection(ctx context.Context, database *mongo.Database, name string, validator bson.D, indexes []historyIndexSpec) error {
	collections, err := database.ListCollectionSpecifications(ctx, bson.D{{Key: "name", Value: name}})
	if err != nil {
		return ErrUnavailable
	}
	if len(collections) == 0 {
		opts := options.CreateCollection().SetValidator(validator).SetValidationLevel("strict").SetValidationAction("error")
		if err := database.CreateCollection(ctx, name, opts); err != nil && !namespaceExists(err) {
			return ErrUnavailable
		}
	}
	collections, err = database.ListCollectionSpecifications(ctx, bson.D{{Key: "name", Value: name}})
	if err != nil || len(collections) != 1 {
		return ErrUnavailable
	}
	actual := collections[0].Options
	actualValidator := actual.Lookup("validator")
	level, levelOK := actual.Lookup("validationLevel").StringValueOK()
	action, actionOK := actual.Lookup("validationAction").StringValueOK()
	if !levelOK || level != "strict" || !actionOK || action != "error" || actualValidator.Type != bson.TypeEmbeddedDocument {
		return todoTeach(name+".validator", "match the strict/error closed history schema", ErrConflict)
	}
	if !bsonValidatorEqual(actualValidator.Document(), validator) {
		prior := historyValidatorBeforeOwnership(validator)
		priorV2 := historyValidatorBeforeSchemaV2(validator)
		beforeRepositoryIdentity := name == todoContentVersionsCollection && bsonValidatorEqual(actualValidator.Document(), contentVersionsValidatorBeforePlanningAndRepositoryIdentity())
		beforeAuditTriggers := name == todoContentVersionsCollection && bsonValidatorEqual(actualValidator.Document(), contentVersionsValidatorBeforeAuditTriggers())
		beforeDigest := name == todoContentVersionsCollection && bsonValidatorEqual(actualValidator.Document(), historyValidatorBeforeContentDigest(validator))
		beforePlacement := name == todoEventsCollection && bsonValidatorEqual(actualValidator.Document(), historyValidatorBeforeClearPlacement(validator))
		beforeAdoption := name == todoEventsCollection && bsonValidatorEqual(actualValidator.Document(), historyValidatorBeforeHistoricalAdoption(validator))
		beforeContentMigration := name == todoEventsCollection && bsonValidatorEqual(actualValidator.Document(), historyValidatorBeforeContentMigration(validator))
		beforeReviewOutcome := name == todoEventsCollection && bsonValidatorEqual(actualValidator.Document(), historyValidatorBeforeReviewOutcome(validator))
		beforeReviewWaiver := name == todoEventsCollection && bsonValidatorEqual(actualValidator.Document(), historyValidatorBeforeReviewWaiver(validator))
		beforeAdoptionAndContentMigration := name == todoEventsCollection && bsonValidatorEqual(actualValidator.Document(), historyValidatorBeforeContentMigration(historyValidatorBeforeHistoricalAdoption(validator)))
		if !bsonValidatorEqual(actualValidator.Document(), prior) && !bsonValidatorEqual(actualValidator.Document(), priorV2) && !beforeRepositoryIdentity && !beforeAuditTriggers && !beforeDigest && !beforePlacement && !beforeAdoption && !beforeContentMigration && !beforeAdoptionAndContentMigration && !beforeReviewOutcome && !beforeReviewWaiver {
			return todoTeach(name+".validator", "match the canonical history schema or the single recognized predecessor without section ownership", ErrConflict)
		}
		if !beforeDigest && !beforePlacement {
			if err := scanLegacyHistoryOwnership(ctx, database, name); err != nil {
				return err
			}
		}
		command := bson.D{{Key: "collMod", Value: name}, {Key: "validator", Value: validator}, {Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"}}
		if err := database.RunCommand(ctx, command).Err(); err != nil {
			return ErrUnavailable
		}
	}
	collections, err = database.ListCollectionSpecifications(ctx, bson.D{{Key: "name", Value: name}})
	if err != nil || len(collections) != 1 {
		return ErrUnavailable
	}
	actualValidator = collections[0].Options.Lookup("validator")
	if actualValidator.Type != bson.TypeEmbeddedDocument || !bsonValidatorEqual(actualValidator.Document(), validator) {
		return todoTeach(name+".validator", "match the strict/error closed history schema", ErrConflict)
	}
	collection := database.Collection(name)
	models := make([]mongo.IndexModel, 0, len(indexes))
	for _, index := range indexes {
		models = append(models, mongo.IndexModel{Keys: index.keys, Options: options.Index().SetName(index.name).SetUnique(index.unique)})
	}
	if _, err := collection.Indexes().CreateMany(ctx, models); err != nil {
		return ErrUnavailable
	}
	return verifyHistoryIndexes(ctx, collection, indexes)
}

func historyValidatorBeforeSchemaV2(value bson.D) bson.D {
	raw, err := bson.Marshal(value)
	if err != nil {
		return nil
	}
	var clone bson.D
	if bson.Unmarshal(raw, &clone) != nil {
		return nil
	}
	for i := range clone {
		if clone[i].Key != "$jsonSchema" {
			continue
		}
		schema, ok := clone[i].Value.(bson.D)
		if !ok {
			return nil
		}
		for j := range schema {
			if schema[j].Key != "properties" {
				continue
			}
			properties, ok := schema[j].Value.(bson.D)
			if !ok {
				return nil
			}
			for k := range properties {
				if properties[k].Key == "content" {
					properties[k].Value = TODOAuthoredContentV1Validator()
				}
			}
			schema[j].Value = properties
		}
		clone[i].Value = schema
	}
	return clone
}

func historyValidatorBeforeContentDigest(value bson.D) bson.D {
	return validatorWithoutProperty(value, "$jsonSchema", "properties", "digest")
}

func historyValidatorBeforeClearPlacement(value bson.D) bson.D {
	return validatorWithoutProperty(value, "$jsonSchema", "properties", "changes", "properties", "clear_placement")
}

func historyValidatorBeforeHistoricalAdoption(value bson.D) bson.D {
	value = validatorWithoutProperty(value, "$jsonSchema", "properties", "permission_decision")
	raw, err := bson.Marshal(value)
	if err != nil {
		return nil
	}
	var clone bson.D
	if bson.Unmarshal(raw, &clone) != nil {
		return nil
	}
	for i := range clone {
		if clone[i].Key != "$jsonSchema" {
			continue
		}
		schema, ok := clone[i].Value.(bson.D)
		if !ok {
			return nil
		}
		for j := range schema {
			if schema[j].Key != "properties" {
				continue
			}
			properties, ok := schema[j].Value.(bson.D)
			if !ok {
				return nil
			}
			for k := range properties {
				if properties[k].Key == "kind" {
					kind, ok := properties[k].Value.(bson.D)
					if !ok {
						return nil
					}
					for n := range kind {
						if kind[n].Key == "enum" {
							kind[n].Value = bson.A{"baseline", "content_changed", "state_changed", "evidence_changed", "placement_changed"}
						}
					}
					properties[k].Value = kind
				}
			}
			schema[j].Value = properties
		}
		clone[i].Value = schema
	}
	return clone
}

func historyValidatorBeforeContentMigration(value bson.D) bson.D {
	for _, property := range []string{"from_content_revision", "from_content_digest", "to_content_digest", "reviewed_acceptance"} {
		value = validatorWithoutProperty(value, "$jsonSchema", "properties", property)
	}
	return value
}

func validatorWithoutProperty(value bson.D, path ...string) bson.D {
	raw, err := bson.Marshal(value)
	if err != nil {
		return nil
	}
	var copy bson.D
	if bson.Unmarshal(raw, &copy) != nil {
		return nil
	}
	return removeValidatorProperty(copy, path)
}

func removeValidatorProperty(value bson.D, path []string) bson.D {
	result := make(bson.D, 0, len(value))
	for _, field := range value {
		if field.Key == path[0] {
			if len(path) == 1 {
				continue
			}
			field.Value = removeValidatorProperty(field.Value.(bson.D), path[1:])
		}
		result = append(result, field)
	}
	return result
}

func historyValidatorBeforeOwnership(value bson.D) bson.D {
	clean := stripHistoryOwnershipProperty(value).(bson.D)
	return clean
}

func stripHistoryOwnershipProperty(value any) any {
	switch typed := value.(type) {
	case bson.D:
		result := make(bson.D, 0, len(typed))
		for _, element := range typed {
			if element.Key == "ownership" {
				continue
			}
			result = append(result, bson.E{Key: element.Key, Value: stripHistoryOwnershipProperty(element.Value)})
		}
		return result
	case bson.A:
		result := make(bson.A, len(typed))
		for i, item := range typed {
			result[i] = stripHistoryOwnershipProperty(item)
		}
		return result
	default:
		return value
	}
}

func scanLegacyHistoryOwnership(ctx context.Context, database *mongo.Database, name string) error {
	cursor, err := database.Collection(name).Find(ctx, bson.D{})
	if err != nil {
		return ErrUnavailable
	}
	defer cursor.Close(ctx)
	for cursor.Next(ctx) {
		if name == todoContentVersionsCollection {
			var version TODOContentVersion
			if cursor.Decode(&version) != nil || version.ObjectID.IsZero() || !validID(version.ProjectID) || !validID(version.TODOID) || version.Revision < 0 || validateAuthoredContent(version.Content) != nil {
				return todoTeach(name+".existing_records", "pass the typed immutable content contract before additive ownership validator upgrade", ErrConflict)
			}
		} else {
			var event TODOHistoryEvent
			if cursor.Decode(&event) != nil || event.ObjectID.IsZero() || !validID(event.ProjectID) || !validID(event.TODOID) || event.Revision < 0 || !containsTODOString([]string{"baseline", "content_changed", "state_changed", "evidence_changed", "placement_changed", "historical-adoption", todoContentMigrationEventKind, "review-waiver-granted"}, event.Kind) || !todoOperationIDPattern.MatchString(event.OperationID) || !validSourceDigest(event.RequestDigest) {
				return todoTeach(name+".existing_records", "pass the typed immutable event contract before additive ownership validator upgrade", ErrConflict)
			}
			if (event.Kind == "historical-adoption" || event.Kind == "review-waiver-granted") && (event.PermissionDecision != "pre-iam-private-api-allow-all" || event.ActorClaim != nil) {
				return todoTeach(name+".existing_records", "record the explicit pre-IAM permission decision without inventing an authenticated actor", ErrConflict)
			}
			for _, section := range derefSections(event.Changes.RecordSections) {
				if section.Ownership != "" && section.Ownership != "record" {
					return todoTeach(name+".existing_records", "contain only absent or record section ownership", ErrConflict)
				}
			}
		}
	}
	if cursor.Err() != nil {
		return ErrUnavailable
	}
	return nil
}

func derefSections(value *[]TODOSection) []TODOSection {
	if value == nil {
		return nil
	}
	return *value
}

func (m *MongoStore) ensureTODOHistoryBaseline(ctx context.Context, database *mongo.Database, record TODORecord) error {
	if record.Revision < 0 {
		return todoTeach("todo.revision", "be nonnegative before history baseline", ErrConflict)
	}
	events := database.Collection(todoEventsCollection)
	versions := database.Collection(todoContentVersionsCollection)
	filter := bson.D{{Key: "project_id", Value: record.ProjectID}, {Key: "todo_id", Value: record.ID}}
	var existing TODOHistoryEvent
	err := events.FindOne(ctx, append(append(bson.D{}, filter...), bson.E{Key: "kind", Value: "baseline"})).Decode(&existing)
	if err == nil {
		var version TODOContentVersion
		if existing.Revision != record.Revision && existing.Revision > record.Revision {
			return todoTeach("history.baseline_revision", "not be newer than the current TODO revision", ErrConflict)
		}
		if existing.ContentRevision == nil || versions.FindOne(ctx, bson.D{{Key: "project_id", Value: record.ProjectID}, {Key: "todo_id", Value: record.ID}, {Key: "revision", Value: *existing.ContentRevision}}).Decode(&version) != nil {
			return todoTeach("history.baseline", "include its immutable initial content version", ErrConflict)
		}
		return nil
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return ErrUnavailable
	}
	return m.writeTODOHistoryBaseline(ctx, database, record)
}

func verifyTODOHistoryCollections(ctx context.Context, database *mongo.Database) error {
	for _, expected := range []struct {
		name      string
		validator bson.D
		indexes   []historyIndexSpec
	}{{todoContentVersionsCollection, TODOContentVersionsValidator(), todoContentVersionIndexes()}, {todoEventsCollection, TODOEventsValidator(), todoEventIndexes()}} {
		collections, err := database.ListCollectionSpecifications(ctx, bson.D{{Key: "name", Value: expected.name}})
		if err != nil || len(collections) != 1 {
			return todoTeach(expected.name, "be provisioned before enabling TODO history operations", ErrConflict)
		}
		optionsDocument := collections[0].Options
		actualValidator := optionsDocument.Lookup("validator")
		level, levelOK := optionsDocument.Lookup("validationLevel").StringValueOK()
		action, actionOK := optionsDocument.Lookup("validationAction").StringValueOK()
		if actualValidator.Type != bson.TypeEmbeddedDocument || !bsonValidatorEqual(actualValidator.Document(), expected.validator) || !levelOK || level != "strict" || !actionOK || action != "error" {
			return todoTeach(expected.name+".validator", "match the canonical strict/error history schema", ErrConflict)
		}
		if err := verifyHistoryIndexes(ctx, database.Collection(expected.name), expected.indexes); err != nil {
			return err
		}
	}
	return nil
}

func (m *MongoStore) ReadTODOHistory(ctx context.Context, companyID, projectID, todoID string, afterRevision int64, limit int) (TODOHistoryPage, error) {
	if afterRevision < -1 || limit < 1 || limit > 100 {
		return TODOHistoryPage{}, todoTeach("after_revision/limit", "use after_revision >= -1 and limit between 1 and 100", ErrInvalid)
	}
	if _, err := m.ReadTODO(ctx, companyID, projectID, todoID, nil); err != nil {
		return TODOHistoryPage{}, err
	}
	database, err := m.todoTenantDatabase(ctx, companyID)
	if err != nil {
		return TODOHistoryPage{}, err
	}
	if _, err := m.readTODOHistoryBaseline(ctx, database, projectID, todoID); err != nil {
		return TODOHistoryPage{}, err
	}
	filter := bson.D{{Key: "project_id", Value: projectID}, {Key: "todo_id", Value: todoID}}
	if afterRevision >= 0 {
		filter = append(filter, bson.E{Key: "revision", Value: bson.D{{Key: "$gt", Value: afterRevision}}})
	}
	opts := options.Find().SetSort(bson.D{{Key: "revision", Value: 1}}).SetLimit(int64(limit + 1))
	cursor, err := database.Collection(todoEventsCollection).Find(ctx, filter, opts)
	if err != nil {
		return TODOHistoryPage{}, ErrUnavailable
	}
	defer cursor.Close(ctx)
	events := make([]TODOHistoryEvent, 0, limit+1)
	for cursor.Next(ctx) {
		var event TODOHistoryEvent
		if cursor.Decode(&event) != nil {
			return TODOHistoryPage{}, ErrUnavailable
		}
		events = append(events, event)
	}
	if cursor.Err() != nil {
		return TODOHistoryPage{}, ErrUnavailable
	}
	var next *int64
	if len(events) > limit {
		nextValue := events[limit-1].Revision
		next = &nextValue
		events = events[:limit]
	}
	return TODOHistoryPage{CompanyID: companyID, ProjectID: projectID, TODOID: todoID, Events: events, NextAfter: next}, nil
}

func (m *MongoStore) ReadTODOAsOf(ctx context.Context, companyID, projectID, todoID string, revision int64) (TODOAsOfResponse, error) {
	current, err := m.ReadTODO(ctx, companyID, projectID, todoID, nil)
	if err != nil {
		return TODOAsOfResponse{}, err
	}
	if revision < 0 || revision > current.TODORevision {
		return TODOAsOfResponse{}, todoTeach("history_unavailable", "request an aggregate revision between the observed baseline and current TODO", ErrNotFound)
	}
	database, err := m.todoTenantDatabase(ctx, companyID)
	if err != nil {
		return TODOAsOfResponse{}, err
	}
	baseline, err := m.readTODOHistoryBaseline(ctx, database, projectID, todoID)
	if err != nil {
		return TODOAsOfResponse{}, err
	}
	if revision < baseline.Revision {
		return TODOAsOfResponse{}, todoTeach("history_unavailable", fmt.Sprintf("Mongo history begins at observed revision %d; earlier history was not reconstructed", baseline.Revision), ErrNotFound)
	}
	content, err := m.contentAtRevision(ctx, database, projectID, todoID, revision)
	if err != nil {
		return TODOAsOfResponse{}, err
	}
	state := TODOAsOfState{Qualifiers: []string{}, ProofReferences: []TODOProofReference{}, WaiverReferences: []TODOProofReference{}, ImplementationCommits: []TODOCommitReference{}, RecordSections: []TODOSection{}}
	applyTODOHistoryChanges(&state, baseline.Changes)
	filter := bson.D{{Key: "project_id", Value: projectID}, {Key: "todo_id", Value: todoID}, {Key: "revision", Value: bson.D{{Key: "$gt", Value: baseline.Revision}, {Key: "$lte", Value: revision}}}}
	cursor, err := database.Collection(todoEventsCollection).Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "revision", Value: 1}}))
	if err != nil {
		return TODOAsOfResponse{}, ErrUnavailable
	}
	defer cursor.Close(ctx)
	expectedRevision := baseline.Revision + 1
	for cursor.Next(ctx) {
		var event TODOHistoryEvent
		if cursor.Decode(&event) != nil || event.Revision != expectedRevision || event.Kind == "baseline" {
			return TODOAsOfResponse{}, todoTeach("history_integrity", "contain one valid event for every aggregate revision after baseline", ErrConflict)
		}
		applyTODOHistoryChanges(&state, event.Changes)
		expectedRevision++
	}
	if cursor.Err() != nil {
		return TODOAsOfResponse{}, ErrUnavailable
	}
	if expectedRevision != revision+1 {
		return TODOAsOfResponse{}, todoTeach("history_unavailable", "contain the complete event sequence through the requested aggregate revision", ErrNotFound)
	}
	return TODOAsOfResponse{CompanyID: companyID, ProjectID: projectID, TODOID: todoID, Revision: revision, Content: content, State: state}, nil
}

func (m *MongoStore) CompareTODOHistory(ctx context.Context, companyID, projectID, todoID string, fromRevision, toRevision int64) (TODOContentComparison, error) {
	if fromRevision < 0 || toRevision < 0 || fromRevision == toRevision {
		return TODOContentComparison{}, todoTeach("from/to", "be distinct nonnegative aggregate revisions", ErrInvalid)
	}
	from, err := m.ReadTODOAsOf(ctx, companyID, projectID, todoID, fromRevision)
	if err != nil {
		return TODOContentComparison{}, err
	}
	to, err := m.ReadTODOAsOf(ctx, companyID, projectID, todoID, toRevision)
	if err != nil {
		return TODOContentComparison{}, err
	}
	changed := make([]string, 0)
	if from.Content.Title != to.Content.Title {
		changed = append(changed, "title")
	}
	if from.Content.Objective != to.Content.Objective {
		changed = append(changed, "objective")
	}
	if !reflect.DeepEqual(from.Content.Sections, to.Content.Sections) {
		changed = append(changed, "sections")
	}
	if !reflect.DeepEqual(from.Content.DiffExpectation, to.Content.DiffExpectation) {
		changed = append(changed, "diff_expectation")
	}
	if !reflect.DeepEqual(from.Content.AffectedFiles, to.Content.AffectedFiles) {
		changed = append(changed, "affected_files")
	}
	if from.Content.SchemaVersion != to.Content.SchemaVersion {
		changed = append(changed, "schema_version")
	}
	if from.Content.Context != to.Content.Context {
		changed = append(changed, "context")
	}
	if !reflect.DeepEqual(from.Content.Scope, to.Content.Scope) {
		changed = append(changed, "scope")
	}
	if !reflect.DeepEqual(from.Content.Criteria, to.Content.Criteria) {
		changed = append(changed, "criteria")
	}
	if !reflect.DeepEqual(from.Content.ValidationScenarios, to.Content.ValidationScenarios) {
		changed = append(changed, "validation_scenarios")
	}
	if !reflect.DeepEqual(from.Content.ImplementationPlan, to.Content.ImplementationPlan) {
		changed = append(changed, "implementation_plan")
	}
	if !reflect.DeepEqual(from.Content.Decisions, to.Content.Decisions) {
		changed = append(changed, "decisions")
	}
	if !reflect.DeepEqual(from.Content.Risks, to.Content.Risks) {
		changed = append(changed, "risks")
	}
	if !reflect.DeepEqual(from.Content.References, to.Content.References) {
		changed = append(changed, "references")
	}
	if !reflect.DeepEqual(from.Content.LegacyContext, to.Content.LegacyContext) {
		changed = append(changed, "legacy_context")
	}
	if !reflect.DeepEqual(from.Content.Architecture, to.Content.Architecture) {
		changed = append(changed, "architecture")
	}
	if !reflect.DeepEqual(from.Content.Planning, to.Content.Planning) {
		changed = append(changed, "planning")
	}
	if from.State.Stage != to.State.Stage {
		changed = append(changed, "stage")
	}
	if !reflect.DeepEqual(from.State.Qualifiers, to.State.Qualifiers) {
		changed = append(changed, "qualifiers")
	}
	if from.State.WorkState != to.State.WorkState {
		changed = append(changed, "work_state")
	}
	if from.State.NextExactStep != to.State.NextExactStep {
		changed = append(changed, "next_exact_step")
	}
	if from.State.StageVerification != to.State.StageVerification {
		changed = append(changed, "stage_verification")
	}
	if !reflect.DeepEqual(from.State.ProofReferences, to.State.ProofReferences) {
		changed = append(changed, "proof_refs")
	}
	if !reflect.DeepEqual(from.State.WaiverReferences, to.State.WaiverReferences) {
		changed = append(changed, "waiver_refs")
	}
	if !reflect.DeepEqual(from.State.ImplementationCommits, to.State.ImplementationCommits) {
		changed = append(changed, "implementation_commits")
	}
	if !reflect.DeepEqual(from.State.VerifiedTransitionRef, to.State.VerifiedTransitionRef) {
		changed = append(changed, "verified_transition_ref")
	}
	if from.State.ReleaseID != to.State.ReleaseID {
		changed = append(changed, "release_id")
	}
	if from.State.Position != to.State.Position {
		changed = append(changed, "position")
	}
	if !reflect.DeepEqual(from.State.RecordSections, to.State.RecordSections) {
		changed = append(changed, "record_sections")
	}
	sort.Strings(changed)
	return TODOContentComparison{CompanyID: companyID, ProjectID: projectID, TODOID: todoID, From: fromRevision, To: toRevision, Changed: changed}, nil
}

func (m *MongoStore) EditTODOContent(ctx context.Context, companyID string, request TODOContentEditRequest) (TODOContentEditResponse, error) {
	if !validID(request.ProjectID) || !validID(request.TODOID) || request.ExpectedRevision == nil || *request.ExpectedRevision < 0 || *request.ExpectedRevision == math.MaxInt64 || !todoOperationIDPattern.MatchString(request.OperationID) || !validSummaryField(request.Reason, 512) {
		return TODOContentEditResponse{}, todoTeach("project_id/todo_id/expected_revision/operation_id/reason", "provide valid stable identity, current aggregate revision, operation ID and reason", ErrInvalid)
	}
	if err := validateNewAuthoredContent(request.Content); err != nil {
		return TODOContentEditResponse{}, err
	}
	project, collection, err := m.selectedTODOCollection(ctx, companyID, request.ProjectID, request.TODOID)
	if err != nil {
		return TODOContentEditResponse{}, err
	}
	record, err := readSelectedTODO(ctx, collection, request.ProjectID, request.TODOID)
	if err != nil {
		return TODOContentEditResponse{}, err
	}
	if err := m.exactTODORelease(ctx, companyID, project, record); err != nil {
		return TODOContentEditResponse{}, err
	}
	digest, err := todoRequestDigest(request)
	if err != nil {
		return TODOContentEditResponse{}, ErrInvalid
	}
	database, err := m.todoTenantDatabase(ctx, companyID)
	if err != nil {
		return TODOContentEditResponse{}, err
	}
	return m.writeTODOContent(ctx, database, collection, companyID, record, request, digest, "content_changed", true)
}

func (m *MongoStore) RestoreTODOContent(ctx context.Context, companyID string, request TODOContentRestoreRequest) (TODOContentEditResponse, error) {
	if !validID(request.ProjectID) || !validID(request.TODOID) || request.ExpectedRevision == nil || *request.ExpectedRevision < 0 || *request.ExpectedRevision == math.MaxInt64 || !todoOperationIDPattern.MatchString(request.OperationID) || request.ContentRevision < 0 {
		return TODOContentEditResponse{}, todoTeach("project_id/todo_id/expected_revision/operation_id/content_revision", "provide valid identity, current revision, stable operation ID, and nonnegative saved content revision", ErrInvalid)
	}
	database, err := m.todoTenantDatabase(ctx, companyID)
	if err != nil {
		return TODOContentEditResponse{}, err
	}
	var version TODOContentVersion
	if err := database.Collection(todoContentVersionsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: request.ProjectID}, {Key: "todo_id", Value: request.TODOID}, {Key: "revision", Value: request.ContentRevision}}).Decode(&version); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return TODOContentEditResponse{}, todoTeach("content_revision", "name an existing immutable content version for this TODO", ErrNotFound)
		}
		return TODOContentEditResponse{}, ErrUnavailable
	}
	edit := TODOContentEditRequest{ProjectID: request.ProjectID, TODOID: request.TODOID, ExpectedRevision: request.ExpectedRevision,
		OperationID: request.OperationID, Content: version.Content, Reason: fmt.Sprintf("restore from content revision %d: %s", request.ContentRevision, request.Reason)}
	for i := range edit.Content.Sections {
		edit.Content.Sections[i].Ownership = "contract"
	}
	if !validSummaryField(request.Reason, 400) {
		return TODOContentEditResponse{}, todoTeach("reason", "provide a restoration reason up to 400 bytes", ErrInvalid)
	}
	if err := validateAuthoredContent(edit.Content); err != nil {
		return TODOContentEditResponse{}, err
	}
	project, collection, err := m.selectedTODOCollection(ctx, companyID, request.ProjectID, request.TODOID)
	if err != nil {
		return TODOContentEditResponse{}, err
	}
	record, err := readSelectedTODO(ctx, collection, request.ProjectID, request.TODOID)
	if err != nil {
		return TODOContentEditResponse{}, err
	}
	if err := m.exactTODORelease(ctx, companyID, project, record); err != nil {
		return TODOContentEditResponse{}, err
	}
	digest, err := todoRequestDigest(request)
	if err != nil {
		return TODOContentEditResponse{}, ErrInvalid
	}
	return m.writeTODOContent(ctx, database, collection, companyID, record, edit, digest, "content_changed", false)
}

func (m *MongoStore) writeTODOContent(ctx context.Context, database *mongo.Database, collection *mongo.Collection, companyID string, record TODORecord, request TODOContentEditRequest, digest, eventKind string, preserveArchived bool) (TODOContentEditResponse, error) {
	var prior TODOHistoryEvent
	findErr := database.Collection(todoEventsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: record.ProjectID}, {Key: "todo_id", Value: record.ID}, {Key: "operation_id", Value: request.OperationID}}).Decode(&prior)
	if findErr == nil {
		if prior.Kind != eventKind || prior.RequestDigest != digest {
			return TODOContentEditResponse{}, todoTeach("operation_id", "not be reused with a different content payload", ErrConflict)
		}
		response := TODOContentEditResponse{CompanyID: companyID, ProjectID: record.ProjectID, TODOID: record.ID, Revision: prior.Revision, NoOp: true}
		if record.Source.Kind == "mongo-content-revision" || usesImportedTODOContentStageProof(record) {
			contentRevision, contentDigest, _, hasCurrentContent, subjectErr := currentTODOContentSubject(ctx, database, record)
			if subjectErr != nil {
				return TODOContentEditResponse{}, subjectErr
			}
			if hasCurrentContent && validSourceDigest(contentDigest) {
				response.ContentRevision, response.ContentDigest = &contentRevision, contentDigest
			}
		}
		return response, nil
	}
	if !errors.Is(findErr, mongo.ErrNoDocuments) {
		return TODOContentEditResponse{}, ErrUnavailable
	}
	if request.ExpectedRevision == nil || *request.ExpectedRevision != record.Revision {
		return TODOContentEditResponse{}, &todoRevisionConflict{Revision: record.Revision}
	}
	if err := requireTODOHistoryBaseline(ctx, database, record); err != nil {
		return TODOContentEditResponse{}, err
	}
	if request.Content.SchemaVersion != todoContentSchemaVersion {
		if err := validateExistingSectionOwnership(record); err != nil {
			return TODOContentEditResponse{}, err
		}
	} else if record.Stage != "Planning" {
		if err := ValidateTODOContent(request.Content.TODOContent, false); err != nil {
			return TODOContentEditResponse{}, err
		}
	}
	current, err := m.contentAtRevision(ctx, database, record.ProjectID, record.ID, record.Revision)
	if err != nil {
		return TODOContentEditResponse{}, err
	}
	if request.Content.SchemaVersion == todoContentSchemaVersion && current.SchemaVersion != todoContentSchemaVersion {
		return TODOContentEditResponse{}, todoTeach("content.schema_version", "use the digest-bound historical content migration operation to convert an admitted v1 TODO", ErrConflict)
	}
	if err := requireNoTODOContentDowngrade(current.SchemaVersion, request.Content.SchemaVersion); err != nil {
		return TODOContentEditResponse{}, err
	}
	requestedContent := withContractOwnership(request.Content)
	if preserveArchived && requestedContent.SchemaVersion == todoContentSchemaVersion && current.SchemaVersion == todoContentSchemaVersion {
		requestedContent.LegacyContext = current.LegacyContext
	}
	if reflect.DeepEqual(current, requestedContent) {
		response := TODOContentEditResponse{CompanyID: companyID, ProjectID: record.ProjectID, TODOID: record.ID, Revision: record.Revision, NoOp: true}
		if record.Source.Kind == "mongo-content-revision" || usesImportedTODOContentStageProof(record) {
			contentRevision, contentDigest, _, hasCurrentContent, subjectErr := currentTODOContentSubject(ctx, database, record)
			if subjectErr != nil {
				return TODOContentEditResponse{}, subjectErr
			}
			if hasCurrentContent && validSourceDigest(contentDigest) {
				response.ContentRevision, response.ContentDigest = &contentRevision, contentDigest
			}
		}
		return response, nil
	}
	nextRevision := record.Revision + 1
	updatedSections := record.Sections
	if request.Content.SchemaVersion != todoContentSchemaVersion {
		updatedSections, err = mergeTODOAuthoredSections(record.Sections, request.Content.Sections)
		if err != nil {
			return TODOContentEditResponse{}, err
		}
	}
	storedContent := requestedContent
	contentDigest, err := todoContentRevisionDigest(record.ProjectID, record.ID, nextRevision, storedContent)
	if err != nil {
		return TODOContentEditResponse{}, ErrInvalid
	}
	reopensPlanning := record.Source.Kind == "mongo-content-revision" && (record.Stage == "Implementation-Approval-Pending" || record.Stage == "Ready-for-Implementation" || record.Stage == "In-Progress")
	var planningProof *TODOProofReference
	if reopensPlanning {
		planningProof = &TODOProofReference{
			Kind: "planning-stage", Source: "mongo-content-revision",
			Reference:    fmt.Sprintf("%s/%s/content/%d", record.ProjectID, record.ID, nextRevision),
			SourceDigest: contentDigest, VerificationState: "verified",
		}
	}
	operation := TODOOperationState{ID: request.OperationID, Kind: "content-edit", RequestDigest: digest, ResultRevision: nextRevision}
	setFields := bson.D{{Key: "title", Value: request.Content.Title}, {Key: "objective", Value: request.Content.Objective}, {Key: "diff_expectation", Value: request.Content.DiffExpectation}, {Key: "affected_files", Value: request.Content.AffectedFiles}, {Key: "last_operation", Value: operation}, {Key: "updated_at", Value: bson.DateTime(time.Now().UTC().UnixMilli())}}
	if request.Content.SchemaVersion != todoContentSchemaVersion {
		setFields = append(setFields, bson.E{Key: "sections", Value: updatedSections})
	}
	if reopensPlanning {
		setFields = append(setFields,
			bson.E{Key: "stage", Value: "Planning"},
			bson.E{Key: "stage_verification", Value: "verified"},
			bson.E{Key: "verified_transition_ref", Value: planningProof},
		)
	} else if invalidatesContentApproval(record) {
		setFields = append(setFields, bson.E{Key: "stage_verification", Value: "unverified"})
	}
	update := bson.D{{Key: "$set", Value: setFields}, {Key: "$inc", Value: bson.D{{Key: "revision", Value: 1}}}}
	if !reopensPlanning && invalidatesContentApproval(record) {
		update = append(update, bson.E{Key: "$unset", Value: bson.D{{Key: "verified_transition_ref", Value: ""}}})
	}
	session, err := m.client.StartSession()
	if err != nil {
		return TODOContentEditResponse{}, ErrUnavailable
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		result, err := collection.UpdateOne(txCtx, bson.D{{Key: "_id", Value: record.ObjectID}, {Key: "project_id", Value: record.ProjectID}, {Key: "id", Value: record.ID}, {Key: "revision", Value: record.Revision}}, update)
		if err != nil {
			return nil, normalizeMongoError(err)
		}
		if result.MatchedCount != 1 {
			return nil, &todoRevisionConflict{Revision: nextRevision}
		}
		version := TODOContentVersion{ObjectID: bson.NewObjectID(), ProjectID: record.ProjectID, TODOID: record.ID, Revision: nextRevision, Digest: contentDigest, Content: storedContent}
		if _, err := database.Collection(todoContentVersionsCollection).InsertOne(txCtx, version); err != nil {
			return nil, normalizeMongoError(err)
		}
		contentRevision := nextRevision
		changes := TODOHistoryChanges{}
		if reopensPlanning {
			planning, verified := "Planning", "verified"
			changes.Stage = &planning
			changes.StageVerification = &verified
			changes.VerifiedTransitionRef = planningProof
		} else if invalidatesContentApproval(record) {
			unverified := "unverified"
			changes.StageVerification = &unverified
			changes.ClearVerifiedTransitionRef = true
		}
		event := TODOHistoryEvent{ObjectID: bson.NewObjectID(), ProjectID: record.ProjectID, TODOID: record.ID, Revision: nextRevision, Kind: eventKind, OccurredAt: time.Now().UTC(), OperationID: request.OperationID, RequestDigest: digest, Changes: changes, ContentRevision: &contentRevision, Reason: request.Reason}
		if _, err := database.Collection(todoEventsCollection).InsertOne(txCtx, event); err != nil {
			return nil, normalizeMongoError(err)
		}
		return nil, nil
	})
	if err != nil {
		return TODOContentEditResponse{}, normalizeMongoError(err)
	}
	response := TODOContentEditResponse{CompanyID: companyID, ProjectID: record.ProjectID, TODOID: record.ID, Revision: nextRevision}
	if record.Source.Kind == "mongo-content-revision" || usesImportedTODOContentStageProof(record) {
		contentRevision := nextRevision
		response.ContentRevision, response.ContentDigest = &contentRevision, contentDigest
	}
	return response, nil
}

func requireNoTODOContentDowngrade(current, next int) error {
	if current == todoContentSchemaVersion && next != todoContentSchemaVersion {
		return todoTeach("content.schema_version", "retain the current v2 Mongo contract; historical v1 content is archive only", ErrConflict)
	}
	return nil
}

func validateAuthoredContent(content TODOAuthoredContent) error {
	if content.SchemaVersion == todoContentSchemaVersion {
		if content.Sections != nil || !validName(content.Title) || !validSummaryField(content.Objective, 1024) || content.DiffExpectation == nil || content.AffectedFiles == nil {
			return todoTeach("content", "provide schema-v2 typed fields and bounded envelope fields", ErrInvalid)
		}
		operationalContent := content.TODOContent
		operationalContent.LegacyContext = nil
		if err := validateTODOContent(operationalContent, false, false); err != nil {
			return err
		}
		return validateAuthoredEnvelope(content)
	}
	if content.SchemaVersion != 0 && content.SchemaVersion != 1 {
		return todoTeach("content.schema_version", "be 1 for readable legacy content or 2 for typed content", ErrInvalid)
	}
	if !validName(content.Title) || !validSummaryField(content.Objective, 1024) || content.Sections == nil || content.DiffExpectation == nil || content.AffectedFiles == nil || len(content.Sections) > maxTODOSections {
		return todoTeach("content", "provide bounded title/objective and non-null ordered content arrays", ErrInvalid)
	}
	seen := map[string]bool{}
	lastPosition := -1
	for i, section := range content.Sections {
		if section.Position <= lastPosition || strings.TrimSpace(section.Key) == "" || strings.TrimSpace(section.Title) == "" || !utf8.ValidString(section.BodyMarkdown) || len(section.BodyMarkdown) > 64<<10 || seen[section.Title] || (section.Ownership != "" && section.Ownership != "contract") {
			return todoTeach(fmt.Sprintf("content.sections[%d]", i), "contain unique contract-only sections in strictly increasing source order", ErrInvalid)
		}
		lastPosition = section.Position
		seen[section.Title] = true
	}
	for _, scope := range content.DiffExpectation {
		if !validName(scope.Repository) || strings.TrimSpace(scope.Baseline) == "" || scope.ExpectedPaths == nil || scope.NotExpectedPaths == nil {
			return todoTeach("content.diff_expectation", "contain valid repository scopes and explicit path arrays", ErrInvalid)
		}
		for _, p := range append(append([]string{}, scope.ExpectedPaths...), scope.NotExpectedPaths...) {
			if !validRepositoryPattern(p) {
				return todoTeach("content.diff_expectation.paths", "use repository-relative valid patterns", ErrInvalid)
			}
		}
	}
	for _, file := range content.AffectedFiles {
		if !validRepositoryPattern(file) {
			return todoTeach("content.affected_files", "contain valid repository-relative paths", ErrInvalid)
		}
	}
	return nil
}

func validateAuthoredEnvelope(content TODOAuthoredContent) error {
	for _, scope := range content.DiffExpectation {
		if !validName(scope.Repository) || strings.TrimSpace(scope.Baseline) == "" || scope.ExpectedPaths == nil || scope.NotExpectedPaths == nil {
			return todoTeach("content.diff_expectation", "contain valid repository scopes and explicit path arrays", ErrInvalid)
		}
		if (scope.RepositoryID == "") != (scope.DeliveryBranch == "") || scope.RepositoryID != "" && (!validGitRepositoryID(scope.RepositoryID) || !validDeliveryBranch(scope.DeliveryBranch)) {
			return todoTeach("content.diff_expectation.repository_id/delivery_branch", "provide both an exact owner/repository and safe destination branch, or omit both before proof", ErrInvalid)
		}
		for _, p := range append(append([]string{}, scope.ExpectedPaths...), scope.NotExpectedPaths...) {
			if !validRepositoryPattern(p) {
				return todoTeach("content.diff_expectation.paths", "use repository-relative valid patterns", ErrInvalid)
			}
		}
	}
	for _, file := range content.AffectedFiles {
		if !validRepositoryPattern(file) {
			return todoTeach("content.affected_files", "contain valid repository-relative paths", ErrInvalid)
		}
	}
	return nil
}

func validateNewAuthoredContent(content TODOAuthoredContent) error {
	if content.SchemaVersion == todoContentSchemaVersion {
		if content.LegacyContext != nil || !validName(content.Title) || !validSummaryField(content.Objective, 1024) || content.Sections != nil || content.DiffExpectation == nil || content.AffectedFiles == nil {
			return todoTeach("content", "provide new schema-v2 typed fields without legacy_context and bounded envelope fields", ErrInvalid)
		}
		if err := ValidateTODOContentDraft(content.TODOContent); err != nil {
			return err
		}
		return validateAuthoredEnvelope(content)
	}
	if err := validateAuthoredContent(content); err != nil {
		return err
	}
	for i, section := range content.Sections {
		if section.Ownership != "contract" {
			return todoTeach(fmt.Sprintf("content.sections[%d].ownership", i), "explicitly classify every authored edit section as contract", ErrInvalid)
		}
		if historyRecordSection(section.Key, section.Title) || historyRenderOnlySection(section.Key, section.Title) {
			return todoTeach(fmt.Sprintf("content.sections[%d]", i), "omit record-owned sections from authored content; they are retained separately", ErrInvalid)
		}
	}
	return nil
}

func mergeTODOAuthoredSections(current, replacement []TODOSection) ([]TODOSection, error) {
	result := make([]TODOSection, 0, len(current)+len(replacement))
	inserted := false
	for _, section := range current {
		if historyRecordSection(section.Key, section.Title) || historyRenderOnlySection(section.Key, section.Title) {
			result = append(result, section)
			continue
		}
		if !inserted {
			result = append(result, replacement...)
			inserted = true
		}
	}
	if !inserted {
		result = append(result, replacement...)
	}
	for i := range result {
		result[i].Position = i
	}
	if len(result) > maxTODOSections {
		return nil, todoTeach("content.sections", "keep the retained section list within its document bound", ErrInvalid)
	}
	return result, nil
}

func (m *MongoStore) todoTenantDatabase(ctx context.Context, companyID string) (*mongo.Database, error) {
	if !validID(companyID) {
		return nil, todoTeach("company_id", "name a registered Company", ErrNotFound)
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return nil, err
	}
	if company.DatabaseName != m.tenantDatabase(companyID) {
		return nil, todoTeach("company.database_name", "match the server-owned Company tenant database", ErrConflict)
	}
	return m.client.Database(company.DatabaseName), nil
}

func (m *MongoStore) readTODOHistoryBaseline(ctx context.Context, database *mongo.Database, projectID, todoID string) (TODOHistoryEvent, error) {
	baseline, err := readHistoryBaseline(ctx, database, projectID, todoID)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return TODOHistoryEvent{}, todoTeach("history.baseline", "be provisioned at the observed Mongo revision before history reads", ErrNotFound)
	}
	if err != nil {
		return TODOHistoryEvent{}, ErrUnavailable
	}
	if baseline.ContentRevision == nil || baseline.Revision < 0 {
		return TODOHistoryEvent{}, todoTeach("history.baseline", "include its actual observed content revision", ErrConflict)
	}
	return baseline, nil
}

func (m *MongoStore) contentAtRevision(ctx context.Context, database *mongo.Database, projectID, todoID string, revision int64) (TODOAuthoredContent, error) {
	opts := options.FindOne().SetSort(bson.D{{Key: "revision", Value: -1}})
	var version TODOContentVersion
	err := database.Collection(todoContentVersionsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "todo_id", Value: todoID}, {Key: "revision", Value: bson.D{{Key: "$lte", Value: revision}}}}, opts).Decode(&version)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return TODOAuthoredContent{}, todoTeach("history_unavailable", "have an immutable content version at or before the requested revision", ErrNotFound)
	}
	if err != nil {
		return TODOAuthoredContent{}, fmt.Errorf("%w: content-version read/decode: %v", ErrUnavailable, err)
	}
	if err := validateAuthoredContent(version.Content); err != nil {
		return TODOAuthoredContent{}, todoTeach("history.content_version", "contain a valid immutable authored-content snapshot", ErrConflict)
	}
	return version.Content, nil
}

func applyTODOHistoryChanges(state *TODOAsOfState, changes TODOHistoryChanges) {
	if changes.Stage != nil {
		state.Stage = *changes.Stage
	}
	if changes.Qualifiers != nil {
		state.Qualifiers = append([]string{}, (*changes.Qualifiers)...)
	}
	if changes.WorkState != nil {
		state.WorkState = *changes.WorkState
	}
	if changes.NextExactStep != nil {
		state.NextExactStep = *changes.NextExactStep
	}
	if changes.StageVerification != nil {
		state.StageVerification = *changes.StageVerification
	}
	if changes.ProofReferences != nil {
		state.ProofReferences = append([]TODOProofReference{}, (*changes.ProofReferences)...)
	}
	if changes.WaiverReferences != nil {
		state.WaiverReferences = append([]TODOProofReference{}, (*changes.WaiverReferences)...)
	}
	if changes.ImplementationCommits != nil {
		state.ImplementationCommits = append([]TODOCommitReference{}, (*changes.ImplementationCommits)...)
	}
	if changes.VerifiedTransitionRef != nil {
		proof := *changes.VerifiedTransitionRef
		state.VerifiedTransitionRef = &proof
	} else if changes.ClearVerifiedTransitionRef {
		state.VerifiedTransitionRef = nil
	}
	if changes.ReleaseID != nil {
		state.ReleaseID = *changes.ReleaseID
	}
	if changes.Position != nil {
		state.Position = *changes.Position
	}
	if changes.ClearPlacement {
		state.ReleaseID, state.Position, state.PlacementAssigned = "", 0, false
	} else if changes.ReleaseID != nil && changes.Position != nil {
		state.PlacementAssigned = true
	}
	if changes.RecordSections != nil {
		state.RecordSections = append([]TODOSection{}, (*changes.RecordSections)...)
	}
}

func marshalContent(content TODOAuthoredContent) ([]byte, error) { return json.Marshal(content) }

func requireTODOHistoryBaseline(ctx context.Context, database *mongo.Database, record TODORecord) error {
	var event TODOHistoryEvent
	err := database.Collection(todoEventsCollection).FindOne(ctx, bson.D{
		{Key: "project_id", Value: record.ProjectID}, {Key: "todo_id", Value: record.ID}, {Key: "kind", Value: "baseline"},
	}).Decode(&event)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return todoTeach("history.baseline", "be provisioned for this admitted TODO before enabling mutations", ErrUnavailable)
	}
	if err != nil {
		return ErrUnavailable
	}
	if event.Revision > record.Revision || event.ContentRevision == nil {
		return todoTeach("history.baseline", "reflect a truthful observed revision no newer than the TODO", ErrConflict)
	}
	var version TODOContentVersion
	if err := database.Collection(todoContentVersionsCollection).FindOne(ctx, bson.D{
		{Key: "project_id", Value: record.ProjectID}, {Key: "todo_id", Value: record.ID}, {Key: "revision", Value: *event.ContentRevision},
	}).Decode(&version); err != nil {
		return todoTeach("history.baseline.content_revision", "resolve to the immutable observed content version", ErrConflict)
	}
	return nil
}

func (m *MongoStore) writeTODOHistoryBaseline(ctx context.Context, database *mongo.Database, record TODORecord) error {
	session, err := m.client.StartSession()
	if err != nil {
		return ErrUnavailable
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		return nil, insertTODOHistoryBaseline(txCtx, database, record)
	})
	if err != nil {
		return normalizeMongoError(err)
	}
	return nil
}

func insertTODOHistoryBaseline(ctx context.Context, database *mongo.Database, record TODORecord) error {
	events := database.Collection(todoEventsCollection)
	versions := database.Collection(todoContentVersionsCollection)
	filter := bson.D{{Key: "project_id", Value: record.ProjectID}, {Key: "todo_id", Value: record.ID}}
	var existing TODOHistoryEvent
	findErr := events.FindOne(ctx, append(append(bson.D{}, filter...), bson.E{Key: "kind", Value: "baseline"})).Decode(&existing)
	if findErr == nil {
		return nil
	}
	if !errors.Is(findErr, mongo.ErrNoDocuments) {
		return ErrUnavailable
	}
	currentCollection := database.Collection(todosCollection)
	var current TODORecord
	if err := currentCollection.FindOne(ctx, bson.D{{Key: "project_id", Value: record.ProjectID}, {Key: "id", Value: record.ID}}).Decode(&current); err != nil {
		return ErrUnavailable
	}
	if current.Revision != record.Revision || current.ObjectID != record.ObjectID {
		return todoTeach("history.baseline_revision", "match the still-current observed TODO revision during the baseline transaction", ErrConflict)
	}
	content := contentFromTODO(record)
	contentDigest, digestErr := todoContentRevisionDigest(record.ProjectID, record.ID, record.Revision, content)
	if digestErr != nil {
		return ErrInvalid
	}
	version := TODOContentVersion{ObjectID: bson.NewObjectID(), ProjectID: record.ProjectID, TODOID: record.ID, Revision: record.Revision, Digest: contentDigest, Content: content}
	operationID := fmt.Sprintf("baseline:%s:%d", record.ID, record.Revision)
	changes := observedTODOChanges(record)
	digest, digestErr := todoRequestDigest(bson.D{{Key: "content", Value: content}, {Key: "changes", Value: changes}})
	if digestErr != nil {
		return ErrInvalid
	}
	if _, err := versions.InsertOne(ctx, version); err != nil {
		return normalizeMongoError(err)
	}
	revision := record.Revision
	event := TODOHistoryEvent{ObjectID: bson.NewObjectID(), ProjectID: record.ProjectID, TODOID: record.ID, Revision: revision,
		Kind: "baseline", OccurredAt: time.Now().UTC(), OperationID: operationID, RequestDigest: digest, Changes: changes, ContentRevision: &revision}
	if record.Source.Kind == "mongo-content-revision" {
		creation := TODOProofReference{Kind: "created", Source: "mongo-todo-api", Reference: record.Source.Ref, SourceDigest: contentDigest, VerificationState: "verified"}
		event.ProofReference = &creation
	}
	if _, err := events.InsertOne(ctx, event); err != nil {
		return normalizeMongoError(err)
	}
	return nil
}

func todoContentRevisionDigest(projectID, todoID string, revision int64, content TODOAuthoredContent) (string, error) {
	return todoRequestDigest(bson.D{{Key: "project_id", Value: projectID}, {Key: "todo_id", Value: todoID}, {Key: "content_revision", Value: revision}, {Key: "content", Value: content}})
}

func verifyHistoryIndexes(ctx context.Context, collection *mongo.Collection, expected []historyIndexSpec) error {
	specs, err := collection.Indexes().ListSpecifications(ctx)
	if err != nil {
		return ErrUnavailable
	}
	byName := make(map[string]mongo.IndexSpecification, len(specs))
	for _, spec := range specs {
		byName[spec.Name] = spec
	}
	for _, model := range expected {
		name := model.name
		actual, ok := byName[name]
		if !ok {
			return todoTeach("history.indexes."+name, "exist before history writes", ErrConflict)
		}
		keys, keyErr := bson.Marshal(model.keys)
		uniqueExpected := model.unique
		uniqueActual := actual.Unique != nil && *actual.Unique
		if keyErr != nil || !equalBSONDocument(actual.KeysDocument, keys) || uniqueExpected != uniqueActual {
			return todoTeach("history.indexes."+name, "match canonical keys and uniqueness", ErrConflict)
		}
	}
	return nil
}

func equalBSONDocument(actual []byte, expected []byte) bool {
	return string(actual) == string(expected)
}

func todoContentVersionIndexes() []historyIndexSpec {
	return []historyIndexSpec{{name: "todo_content_project_todo_revision_unique", keys: bson.D{{Key: "project_id", Value: 1}, {Key: "todo_id", Value: 1}, {Key: "revision", Value: 1}}, unique: true}}
}

func todoEventIndexes() []historyIndexSpec {
	return []historyIndexSpec{
		{name: "todo_events_project_todo_revision_unique", keys: bson.D{{Key: "project_id", Value: 1}, {Key: "todo_id", Value: 1}, {Key: "revision", Value: 1}}, unique: true},
		{name: "todo_events_project_todo_operation_unique", keys: bson.D{{Key: "project_id", Value: 1}, {Key: "todo_id", Value: 1}, {Key: "operation_id", Value: 1}}, unique: true},
	}
}

func TODOContentVersionsValidator() bson.D {
	return bson.D{{Key: "$jsonSchema", Value: bson.D{
		{Key: "bsonType", Value: "object"},
		{Key: "additionalProperties", Value: false},
		{Key: "required", Value: bson.A{"_id", "project_id", "todo_id", "revision", "content"}},
		{Key: "properties", Value: bson.D{
			{Key: "_id", Value: bson.D{{Key: "bsonType", Value: "objectId"}}},
			{Key: "project_id", Value: bson.D{{Key: "bsonType", Value: "string"}}},
			{Key: "todo_id", Value: bson.D{{Key: "bsonType", Value: "string"}}},
			{Key: "revision", Value: bson.D{{Key: "bsonType", Value: "long"}, {Key: "minimum", Value: 0}}},
			{Key: "digest", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "pattern", Value: "^sha256:[a-f0-9]{64}$"}}},
			{Key: "content", Value: TODOAuthoredContentValidator()},
		}},
	}}}
}

// This exact deployed predecessor preceded both planning metadata and the
// two approved repository identity fields. It is accepted only for a
// scan-before-collMod migration; old content bytes remain unchanged.
func contentVersionsValidatorBeforePlanningAndRepositoryIdentity() bson.D {
	v1 := withoutBSONFieldsAtPath(TODOAuthoredContentV1Validator(), []string{"properties", "diff_expectation", "items", "properties"}, "repository_id", "delivery_branch")
	v2 := withoutBSONFieldsAtPath(TODOAuthoredContentV2Validator(), []string{"properties", "diff_expectation", "items", "properties"}, "repository_id", "delivery_branch")
	v2 = withoutBSONFieldsAtPath(v2, []string{"properties"}, "planning")
	content := bson.D{{Key: "bsonType", Value: "object"}, {Key: "oneOf", Value: bson.A{v1, v2}}}
	return replaceBSONValueAtPath(TODOContentVersionsValidator(), []string{"$jsonSchema", "properties", "content"}, content)
}

func contentVersionsValidatorBeforeAuditTriggers() bson.D {
	v2 := withoutBSONFieldsAtPath(TODOAuthoredContentV2Validator(), []string{"properties", "planning", "properties"}, "audit_triggers")
	content := bson.D{{Key: "bsonType", Value: "object"}, {Key: "oneOf", Value: bson.A{TODOAuthoredContentV1Validator(), v2}}}
	return replaceBSONValueAtPath(TODOContentVersionsValidator(), []string{"$jsonSchema", "properties", "content"}, content)
}

func TODOAuthoredContentValidator() bson.D {
	v1Schema := TODOAuthoredContentV1Validator()
	v2Schema := TODOAuthoredContentV2Validator()
	return bson.D{{Key: "bsonType", Value: "object"}, {Key: "oneOf", Value: bson.A{v1Schema, v2Schema}}}
}

func TODOAuthoredContentV2Validator() bson.D {
	typed := TODOContentSchemaValidator()
	var properties bson.D
	var required bson.A
	for _, field := range typed {
		switch field.Key {
		case "properties":
			properties = append(properties, field.Value.(bson.D)...)
		case "required":
			required = append(required, field.Value.(bson.A)...)
		}
	}
	properties = append(properties,
		bson.E{Key: "title", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "maxLength", Value: 160}}},
		bson.E{Key: "objective", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "maxLength", Value: 1024}}},
		bson.E{Key: "diff_expectation", Value: historyScopeArraySchema()},
		bson.E{Key: "affected_files", Value: historyStringArraySchema()},
	)
	required = append(required, "title", "objective", "diff_expectation", "affected_files")
	return bson.D{{Key: "bsonType", Value: "object"}, {Key: "additionalProperties", Value: false}, {Key: "required", Value: required}, {Key: "properties", Value: properties}}
}

func TODOAuthoredContentV1Validator() bson.D {
	return bson.D{{Key: "bsonType", Value: "object"}, {Key: "additionalProperties", Value: false},
		{Key: "required", Value: bson.A{"title", "objective", "sections", "diff_expectation", "affected_files"}},
		{Key: "properties", Value: bson.D{
			{Key: "title", Value: bson.D{{Key: "bsonType", Value: "string"}}},
			{Key: "objective", Value: bson.D{{Key: "bsonType", Value: "string"}}},
			{Key: "sections", Value: bson.D{{Key: "bsonType", Value: "array"}, {Key: "items", Value: bson.D{
				{Key: "bsonType", Value: "object"}, {Key: "additionalProperties", Value: false},
				{Key: "required", Value: bson.A{"key", "title", "body_markdown", "position"}},
				{Key: "properties", Value: bson.D{
					{Key: "key", Value: bson.D{{Key: "bsonType", Value: "string"}}},
					{Key: "title", Value: bson.D{{Key: "bsonType", Value: "string"}}},
					{Key: "body_markdown", Value: bson.D{{Key: "bsonType", Value: "string"}}},
					{Key: "position", Value: bson.D{{Key: "bsonType", Value: "int"}, {Key: "minimum", Value: 0}}},
					{Key: "ownership", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "enum", Value: bson.A{"contract"}}}},
				}},
			}}}},
			{Key: "diff_expectation", Value: historyScopeArraySchema()},
			{Key: "affected_files", Value: historyStringArraySchema()},
		}},
	}
}

func TODOContentSchemaValidator() bson.D {
	text := func(max int) bson.D {
		return bson.D{{Key: "bsonType", Value: "string"}, {Key: "maxLength", Value: max}}
	}
	id := bson.D{{Key: "bsonType", Value: "string"}, {Key: "minLength", Value: 1}, {Key: "maxLength", Value: 64}, {Key: "pattern", Value: "^[a-z][a-z0-9-]{0,63}$"}}
	item := bson.D{{Key: "bsonType", Value: "object"}, {Key: "additionalProperties", Value: false}, {Key: "required", Value: bson.A{"id", "text"}},
		{Key: "properties", Value: bson.D{{Key: "id", Value: id}, {Key: "text", Value: text(2048)}}}}
	stringArray := func(maxItems, maxLength int) bson.D {
		return bson.D{{Key: "bsonType", Value: "array"}, {Key: "maxItems", Value: maxItems}, {Key: "items", Value: text(maxLength)}}
	}
	object := func(required bson.A, properties bson.D) bson.D {
		return bson.D{{Key: "bsonType", Value: "object"}, {Key: "additionalProperties", Value: false}, {Key: "required", Value: required}, {Key: "properties", Value: properties}}
	}
	array := func(max int, items bson.D) bson.D {
		return bson.D{{Key: "bsonType", Value: "array"}, {Key: "maxItems", Value: max}, {Key: "items", Value: items}}
	}
	boolean := bson.D{{Key: "bsonType", Value: "bool"}}
	impact := object(bson.A{"cross_module", "public_contract", "runtime_sensitive", "high_severity"}, bson.D{{Key: "cross_module", Value: boolean}, {Key: "public_contract", Value: boolean}, {Key: "runtime_sensitive", Value: boolean}, {Key: "high_severity", Value: boolean}})
	codeRef := object(bson.A{"repository_id", "path"}, bson.D{{Key: "repository_id", Value: text(2048)}, {Key: "path", Value: text(2048)}})
	assumption := object(bson.A{"id", "status", "statement", "code_refs"}, bson.D{{Key: "id", Value: id}, {Key: "status", Value: text(16)}, {Key: "statement", Value: text(2048)}, {Key: "code_refs", Value: array(32, codeRef)}})
	review := object(bson.A{"kind", "reference"}, bson.D{{Key: "kind", Value: text(32)}, {Key: "reference", Value: text(2048)}})
	auditProperties := bson.D{{Key: "blast_radius", Value: text(16)}, {Key: "architecture_applicability", Value: text(16)}}
	for _, name := range []string{"behavioral_change_or_bugfix", "changes_public_contract", "touches_auth_or_tenant", "touches_runtime_or_infra", "touches_tests", "critical_user_journey", "release_or_promotion_critical", "high_severity_plan_review_issue", "explicit_three_lane_request"} {
		auditProperties = append(auditProperties, bson.E{Key: name, Value: boolean})
	}
	audit := bson.D{{Key: "bsonType", Value: "object"}, {Key: "additionalProperties", Value: false}, {Key: "properties", Value: auditProperties}}
	planning := object(bson.A{"complexity", "impact", "assumptions", "reviews"}, bson.D{{Key: "complexity", Value: text(16)}, {Key: "audit_triggers", Value: audit}, {Key: "impact", Value: impact}, {Key: "assumptions", Value: array(64, assumption)}, {Key: "reviews", Value: array(32, review)}})
	return bson.D{{Key: "bsonType", Value: "object"}, {Key: "additionalProperties", Value: false},
		{Key: "required", Value: bson.A{"schema_version"}},
		{Key: "properties", Value: bson.D{
			{Key: "schema_version", Value: bson.D{{Key: "bsonType", Value: "int"}, {Key: "enum", Value: bson.A{todoContentSchemaVersion}}}},
			{Key: "context", Value: text(16 << 10)},
			{Key: "scope", Value: bson.D{{Key: "bsonType", Value: "object"}, {Key: "additionalProperties", Value: false}, {Key: "required", Value: bson.A{"included", "excluded"}}, {Key: "properties", Value: bson.D{{Key: "included", Value: stringArray(64, 2048)}, {Key: "excluded", Value: stringArray(64, 2048)}}}}},
			{Key: "criteria", Value: bson.D{{Key: "bsonType", Value: "array"}, {Key: "maxItems", Value: 64}, {Key: "items", Value: item}}},
			{Key: "validation_scenarios", Value: bson.D{{Key: "bsonType", Value: "array"}, {Key: "maxItems", Value: 64}, {Key: "items", Value: bson.D{{Key: "bsonType", Value: "object"}, {Key: "additionalProperties", Value: false}, {Key: "required", Value: bson.A{"id", "criterion_ids", "method", "expected"}}, {Key: "properties", Value: bson.D{{Key: "id", Value: id}, {Key: "criterion_ids", Value: bson.D{{Key: "bsonType", Value: "array"}, {Key: "maxItems", Value: 64}, {Key: "items", Value: id}}}, {Key: "method", Value: text(2048)}, {Key: "expected", Value: text(2048)}}}}}}},
			{Key: "implementation_plan", Value: bson.D{{Key: "bsonType", Value: "array"}, {Key: "maxItems", Value: 64}, {Key: "items", Value: item}}},
			{Key: "decisions", Value: bson.D{{Key: "bsonType", Value: "array"}, {Key: "maxItems", Value: 32}, {Key: "items", Value: item}}},
			{Key: "risks", Value: bson.D{{Key: "bsonType", Value: "array"}, {Key: "maxItems", Value: 32}, {Key: "items", Value: bson.D{{Key: "bsonType", Value: "object"}, {Key: "additionalProperties", Value: false}, {Key: "required", Value: bson.A{"id", "text", "mitigation"}}, {Key: "properties", Value: bson.D{{Key: "id", Value: id}, {Key: "text", Value: text(2048)}, {Key: "mitigation", Value: text(2048)}}}}}}},
			{Key: "references", Value: bson.D{{Key: "bsonType", Value: "array"}, {Key: "maxItems", Value: 64}, {Key: "items", Value: bson.D{{Key: "bsonType", Value: "object"}, {Key: "additionalProperties", Value: false}, {Key: "required", Value: bson.A{"kind", "ref"}}, {Key: "properties", Value: bson.D{{Key: "kind", Value: text(2048)}, {Key: "ref", Value: text(2048)}}}}}}},
			{Key: "legacy_context", Value: bson.D{{Key: "bsonType", Value: "array"}, {Key: "maxItems", Value: 128}, {Key: "items", Value: bson.D{{Key: "bsonType", Value: "object"}, {Key: "additionalProperties", Value: false}, {Key: "required", Value: bson.A{"source_revision", "section_position", "ownership", "title", "body", "digest"}}, {Key: "properties", Value: bson.D{{Key: "source_revision", Value: bson.D{{Key: "bsonType", Value: "string"}}}, {Key: "section_position", Value: bson.D{{Key: "bsonType", Value: "int"}, {Key: "minimum", Value: 0}}}, {Key: "ownership", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "enum", Value: bson.A{"contract"}}}}, {Key: "title", Value: text(2048)}, {Key: "body", Value: text(64 << 10)}, {Key: "digest", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "pattern", Value: "^sha256:[a-f0-9]{64}$"}}}}}}}}},
			{Key: "architecture", Value: bson.D{{Key: "bsonType", Value: "object"}, {Key: "additionalProperties", Value: false}, {Key: "required", Value: bson.A{"decision", "rationale", "protection"}}, {Key: "properties", Value: bson.D{{Key: "decision", Value: text(2048)}, {Key: "rationale", Value: text(2048)}, {Key: "protection", Value: text(2048)}}}}},
			{Key: "planning", Value: planning},
		}},
	}
}

func TODOEventsValidator() bson.D {
	return bson.D{{Key: "$jsonSchema", Value: bson.D{
		{Key: "bsonType", Value: "object"},
		{Key: "additionalProperties", Value: false},
		{Key: "required", Value: bson.A{"_id", "project_id", "todo_id", "revision", "kind", "occurred_at", "operation_id", "request_digest", "changes"}},
		{Key: "properties", Value: bson.D{
			{Key: "_id", Value: bson.D{{Key: "bsonType", Value: "objectId"}}},
			{Key: "project_id", Value: bson.D{{Key: "bsonType", Value: "string"}}},
			{Key: "todo_id", Value: bson.D{{Key: "bsonType", Value: "string"}}},
			{Key: "revision", Value: bson.D{{Key: "bsonType", Value: "long"}, {Key: "minimum", Value: 0}}},
			{Key: "kind", Value: bson.D{{Key: "enum", Value: bson.A{"baseline", "content_changed", "state_changed", "evidence_changed", "placement_changed", "historical-adoption", todoContentMigrationEventKind, "review-waiver-granted"}}}},
			{Key: "occurred_at", Value: bson.D{{Key: "bsonType", Value: "date"}}},
			{Key: "operation_id", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "minLength", Value: 1}, {Key: "maxLength", Value: 128}}},
			{Key: "request_digest", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "pattern", Value: "^sha256:[a-f0-9]{64}$"}}},
			{Key: "changes", Value: TODOHistoryChangesValidator()},
			{Key: "content_revision", Value: bson.D{{Key: "bsonType", Value: "long"}}},
			{Key: "from_content_revision", Value: bson.D{{Key: "bsonType", Value: "long"}}},
			{Key: "from_content_digest", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "pattern", Value: "^sha256:[a-f0-9]{64}$"}}},
			{Key: "to_content_digest", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "pattern", Value: "^sha256:[a-f0-9]{64}$"}}},
			{Key: "reviewed_acceptance", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "enum", Value: bson.A{todoContentMigrationAcceptance}}}},
			{Key: "reason", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "maxLength", Value: 512}}},
			{Key: "actor_claim", Value: TODOActorClaimValidator()},
			{Key: "permission_decision", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "enum", Value: bson.A{"pre-iam-private-api-allow-all"}}}},
			{Key: "proof_reference", Value: TODOProofReferenceValidator()},
		}},
	}}}
}

func historyStringArraySchema() bson.D {
	return bson.D{{Key: "bsonType", Value: "array"}, {Key: "items", Value: bson.D{{Key: "bsonType", Value: "string"}}}}
}

func historySectionArraySchema() bson.D {
	return bson.D{{Key: "bsonType", Value: "array"}, {Key: "items", Value: bson.D{
		{Key: "bsonType", Value: "object"}, {Key: "additionalProperties", Value: false},
		{Key: "required", Value: bson.A{"key", "title", "body_markdown", "position"}},
		{Key: "properties", Value: bson.D{
			{Key: "key", Value: bson.D{{Key: "bsonType", Value: "string"}}},
			{Key: "title", Value: bson.D{{Key: "bsonType", Value: "string"}}},
			{Key: "body_markdown", Value: bson.D{{Key: "bsonType", Value: "string"}}},
			{Key: "position", Value: bson.D{{Key: "bsonType", Value: "int"}, {Key: "minimum", Value: 0}}},
			{Key: "ownership", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "enum", Value: bson.A{"record"}}}},
		}},
	}}}
}

func historyScopeArraySchema() bson.D {
	return bson.D{{Key: "bsonType", Value: "array"}, {Key: "items", Value: bson.D{
		{Key: "bsonType", Value: "object"}, {Key: "additionalProperties", Value: false},
		{Key: "required", Value: bson.A{"repository", "baseline", "expected_paths", "not_expected_paths"}},
		{Key: "properties", Value: bson.D{
			{Key: "repository", Value: bson.D{{Key: "bsonType", Value: "string"}}}, {Key: "baseline", Value: bson.D{{Key: "bsonType", Value: "string"}}},
			{Key: "repository_id", Value: bson.D{{Key: "bsonType", Value: "string"}}}, {Key: "delivery_branch", Value: bson.D{{Key: "bsonType", Value: "string"}}},
			{Key: "expected_paths", Value: historyStringArraySchema()}, {Key: "not_expected_paths", Value: historyStringArraySchema()},
		}},
	}}}
}

func TODOProofReferenceValidator() bson.D {
	return bson.D{{Key: "bsonType", Value: "object"}, {Key: "additionalProperties", Value: false}, {Key: "required", Value: bson.A{"kind", "source", "ref", "source_digest", "verification_state"}}, {Key: "properties", Value: bson.D{
		{Key: "kind", Value: bson.D{{Key: "bsonType", Value: "string"}}}, {Key: "source", Value: bson.D{{Key: "bsonType", Value: "string"}}}, {Key: "ref", Value: bson.D{{Key: "bsonType", Value: "string"}}},
		{Key: "source_digest", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "pattern", Value: "^sha256:[a-f0-9]{64}$"}}}, {Key: "verification_state", Value: bson.D{{Key: "enum", Value: bson.A{"unverified", "verified"}}}},
		{Key: "outcome", Value: reviewOutcomeSchema()},
	}}}
}

func historyValidatorBeforeReviewOutcome(value bson.D) bson.D {
	raw, err := bson.Marshal(value)
	if err != nil {
		return nil
	}
	var clone bson.D
	if bson.Unmarshal(raw, &clone) != nil {
		return nil
	}
	for _, itemPath := range [][]string{
		{"$jsonSchema", "properties", "proof_reference", "properties"},
		{"$jsonSchema", "properties", "changes", "properties", "proof_refs", "items", "properties"},
		{"$jsonSchema", "properties", "changes", "properties", "waiver_refs", "items", "properties"},
		{"$jsonSchema", "properties", "changes", "properties", "verified_transition_ref", "properties"},
	} {
		clone = withoutBSONFieldsAtPath(clone, itemPath, "outcome")
	}
	return clone
}

func historyValidatorBeforeReviewWaiver(value bson.D) bson.D {
	raw, err := bson.Marshal(value)
	if err != nil {
		return nil
	}
	var clone bson.D
	if bson.Unmarshal(raw, &clone) != nil {
		return nil
	}
	for i := range clone {
		if clone[i].Key != "$jsonSchema" {
			continue
		}
		schema, ok := clone[i].Value.(bson.D)
		if !ok {
			return nil
		}
		for j := range schema {
			if schema[j].Key != "properties" {
				continue
			}
			properties, ok := schema[j].Value.(bson.D)
			if !ok {
				return nil
			}
			for k := range properties {
				if properties[k].Key != "kind" {
					continue
				}
				kind, ok := properties[k].Value.(bson.D)
				if !ok {
					return nil
				}
				for n := range kind {
					if kind[n].Key != "enum" {
						continue
					}
					enum, ok := kind[n].Value.(bson.A)
					if !ok {
						return nil
					}
					filtered := bson.A{}
					for _, item := range enum {
						if item != reviewWaiverEventKind {
							filtered = append(filtered, item)
						}
					}
					kind[n].Value = filtered
				}
				properties[k].Value = kind
			}
			schema[j].Value = properties
		}
		clone[i].Value = schema
	}
	return clone
}

func TODOActorClaimValidator() bson.D {
	return bson.D{{Key: "bsonType", Value: "object"}, {Key: "additionalProperties", Value: false}, {Key: "required", Value: bson.A{"value", "verification_status"}}, {Key: "properties", Value: bson.D{
		{Key: "value", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "minLength", Value: 1}, {Key: "maxLength", Value: 160}}},
		{Key: "verification_status", Value: bson.D{{Key: "enum", Value: bson.A{"unverified"}}}},
	}}}
}

func TODOHistoryChangesValidator() bson.D {
	return bson.D{{Key: "bsonType", Value: "object"}, {Key: "additionalProperties", Value: false}, {Key: "properties", Value: bson.D{
		{Key: "stage", Value: bson.D{{Key: "enum", Value: bson.A{"Planning", "Implementation-Approval-Pending", "Ready-for-Implementation", "In-Progress", "Local-Implemented", "Promotion-Lane", "Completed"}}}},
		{Key: "qualifiers", Value: historyStringArraySchema()}, {Key: "work_state", Value: bson.D{{Key: "bsonType", Value: "string"}}}, {Key: "next_exact_step", Value: bson.D{{Key: "bsonType", Value: "string"}}},
		{Key: "stage_verification", Value: bson.D{{Key: "enum", Value: bson.A{"unverified", "verified"}}}}, {Key: "proof_refs", Value: bson.D{{Key: "bsonType", Value: "array"}, {Key: "items", Value: TODOProofReferenceValidator()}}},
		{Key: "waiver_refs", Value: bson.D{{Key: "bsonType", Value: "array"}, {Key: "items", Value: TODOProofReferenceValidator()}}}, {Key: "implementation_commits", Value: bson.D{{Key: "bsonType", Value: "array"}}},
		{Key: "verified_transition_ref", Value: TODOProofReferenceValidator()}, {Key: "clear_verified_transition_ref", Value: bson.D{{Key: "bsonType", Value: "bool"}}},
		{Key: "record_sections", Value: historySectionArraySchema()}, {Key: "release_id", Value: bson.D{{Key: "bsonType", Value: "string"}}}, {Key: "position", Value: bson.D{{Key: "bsonType", Value: "int"}, {Key: "minimum", Value: 0}}}, {Key: "clear_placement", Value: bson.D{{Key: "bsonType", Value: "bool"}}},
	}}}
}
