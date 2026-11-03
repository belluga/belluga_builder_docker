package registry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// TODOAdmissionRequest carries one typed TODO into an already registered
// Project Release. It is intentionally one-at-a-time; bulk migration remains
// a separate governed operation.
type TODOAdmissionRequest struct {
	SchemaVersion string     `json:"schema_version,omitempty"`
	TODO          TODORecord `json:"todo"`
}

type TODOAdmissionReport struct {
	CompanyID       string `json:"company_id"`
	ProjectID       string `json:"project_id"`
	ReleaseID       string `json:"release_id"`
	TODOID          string `json:"todo_id"`
	Revision        int64  `json:"todo_revision"`
	ContentRevision int64  `json:"content_revision"`
	ContentDigest   string `json:"content_digest"`
	SourcePath      string `json:"source_path,omitempty"`
}

const todoCreationSchemaVersion = "builder-todo-creation-v1"

type TODOCreationTemplate struct {
	SchemaVersion string               `json:"schema_version"`
	Required      []string             `json:"required"`
	Optional      []string             `json:"optional"`
	InitialStage  string               `json:"initial_stage"`
	Stages        []string             `json:"stages"`
	Qualifiers    []string             `json:"qualifiers"`
	SectionShape  map[string]string    `json:"section_shape"`
	Limits        map[string]int       `json:"limits"`
	ReleaseRules  []string             `json:"release_rules"`
	Example       TODOAdmissionRequest `json:"example"`
}

func ProjectTODOCreationTemplate(projectID string) (TODOCreationTemplate, error) {
	if !validID(projectID) {
		return TODOCreationTemplate{}, todoTeach("project_id", "be a registered Project ID", ErrInvalid)
	}
	return TODOCreationTemplate{
		SchemaVersion: todoCreationSchemaVersion,
		Required:      []string{"todo.id", "todo.project_id", "todo.title", "todo.objective", "todo.next_exact_step", "todo.artifact_type", "todo.stage", "todo.qualifiers", "todo.work_state", "todo.sections", "todo.proof_refs", "todo.waiver_refs", "todo.diff_expectation", "todo.implementation_commits", "todo.affected_files"},
		Optional:      []string{"todo.release_id", "todo.position"}, InitialStage: "Planning", Stages: append([]string{}, canonicalTODOStages...),
		Qualifiers:   append([]string{}, canonicalTODOQualifiers...),
		SectionShape: map[string]string{"key": "stable snake_case key", "title": "nonempty unique heading", "body_markdown": "UTF-8 Markdown, at most 65536 bytes", "position": "zero-based source order", "ownership": "contract or record"},
		Limits:       map[string]int{"title_runes": 160, "objective_bytes": 1024, "next_exact_step_bytes": 512, "sections": maxTODOSections, "section_body_bytes": 64 << 10, "proof_references": maxTODOProofs, "record_bytes": maxTODORecordBytes},
		ReleaseRules: []string{"release_id and position are both absent for Backlog", "when assigned, both are required and the Release must belong to this Company and Project", "a Backlog TODO remains Planning until governed attachment"},
		Example:      TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, TODO: TODORecord{ID: "example-todo-id", ProjectID: projectID, Title: "TODO: Example", Objective: "Describe the intended outcome.", NextExactStep: "Perform the next governed action.", ArtifactType: "tactical_execution_contract", Stage: "Planning", Qualifiers: []string{}, WorkState: "planning", Sections: []TODOSection{{Key: "intent", Title: "Intent", BodyMarkdown: "Bounded authoring content.", Position: 0, Ownership: "contract"}}, ProofReferences: []TODOProofReference{}, WaiverReferences: []TODOProofReference{}, DiffExpectation: []TODORepositoryScope{}, ImplementationCommits: []TODOCommitReference{}, AffectedFiles: []string{}}},
	}, nil
}

func (m *MongoStore) TODOCreationTemplate(ctx context.Context, companyID, projectID string) (TODOCreationTemplate, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	company, err := m.company(ctx, companyID)
	if err != nil {
		return TODOCreationTemplate{}, err
	}
	if err := validateTODOProjectMembership(ctx, m.client.Database(company.DatabaseName), companyID, projectID); err != nil {
		return TODOCreationTemplate{}, err
	}
	return ProjectTODOCreationTemplate(projectID)
}

func (m *MongoStore) AdmitTODO(ctx context.Context, companyID, projectID string, request TODOAdmissionRequest) (TODOAdmissionReport, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	record := request.TODO
	if !validID(companyID) || !validID(projectID) || record.ProjectID != projectID {
		return TODOAdmissionReport{}, todoTeach("company_id/project_id/todo.project_id", "match the registered Company and Project in the request path", ErrConflict)
	}
	mongoBorn := request.SchemaVersion != ""
	if mongoBorn {
		if request.SchemaVersion != todoCreationSchemaVersion {
			return TODOAdmissionReport{}, todoTeach("schema_version", "match the current Project template version; GET /internal/v1/companies/{companyID}/projects/{projectID}/todos/template", ErrInvalid)
		}
		if record.Stage != "Planning" || (record.ReleaseID == "") != (record.Position == nil) || (record.Position != nil && *record.Position < 0) || record.Source != (TODOSource{}) {
			return TODOAdmissionReport{}, todoTeach("todo.stage/release_id/position/source", "start at Planning, provide both Release placement fields or neither, and omit Foundation/Mongo-owned provenance", ErrInvalid)
		}
	} else {
		if record.Source == (TODOSource{}) {
			return TODOAdmissionReport{}, todoTeach("schema_version/source", "use the current Project template for a new Mongo TODO, or provide complete Foundation provenance for legacy admission", ErrInvalid)
		}
		if err := validateGenericTODOAdmission(record); err != nil {
			return TODOAdmissionReport{}, err
		}
	}
	if mongoBorn {
		if err := validateMongoTODOCreation(record); err != nil {
			return TODOAdmissionReport{}, err
		}
	}
	if _, selected := selectedTODOContract(record.ID); selected {
		return TODOAdmissionReport{}, todoTeach("todo.id", "use the original bounded import/admission contract for this already selected identity", ErrConflict)
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return TODOAdmissionReport{}, err
	}
	if company.DatabaseName != m.tenantDatabase(companyID) {
		return TODOAdmissionReport{}, todoTeach("company.database_name", "match the server-owned Company tenant database", ErrConflict)
	}
	database := m.client.Database(company.DatabaseName)
	if mongoBorn {
		if err := upgradeTODOCollectionForHistory(ctx, database); err != nil {
			return TODOAdmissionReport{}, err
		}
		if err := provisionHistoryCollection(ctx, database, todoContentVersionsCollection, TODOContentVersionsValidator(), todoContentVersionIndexes()); err != nil {
			return TODOAdmissionReport{}, err
		}
		if err := provisionHistoryCollection(ctx, database, todoEventsCollection, TODOEventsValidator(), todoEventIndexes()); err != nil {
			return TODOAdmissionReport{}, err
		}
	}
	if err := verifyTODOCollection(ctx, database); err != nil {
		return TODOAdmissionReport{}, err
	}
	collection := database.Collection(todosCollection)
	if err := verifyTODOIndexes(ctx, collection); err != nil {
		return TODOAdmissionReport{}, err
	}
	if err := verifyTODOHistoryCollections(ctx, database); err != nil {
		return TODOAdmissionReport{}, err
	}
	if !mongoBorn {
		mappingDigest, err := todoMappingDigest(record)
		if err != nil {
			return TODOAdmissionReport{}, todoTeach("todo", "fit the canonical bounded BSON document", ErrInvalid)
		}
		record.Source.MappingDigest = mappingDigest
	}
	session, err := m.client.StartSession()
	if err != nil {
		return TODOAdmissionReport{}, ErrUnavailable
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		if record.ReleaseID != "" {
			if err := validateTODOReleaseMembership(txCtx, database, companyID, projectID, record.ReleaseID); err != nil {
				return nil, err
			}
		} else if err := validateTODOProjectMembership(txCtx, database, companyID, projectID); err != nil {
			return nil, err
		}
		var project projectDocument
		if err := database.Collection(projectsCollection).FindOne(txCtx, bson.D{{Key: "id", Value: projectID}, {Key: "company_id", Value: companyID}}).Decode(&project); err != nil {
			return nil, ErrUnavailable
		}
		if err := bumpRoadmapRevision(txCtx, database, companyID, projectID, project.RoadmapRevision); err != nil {
			return nil, err
		}
		var existing TODORecord
		findErr := collection.FindOne(txCtx, bson.D{{Key: "project_id", Value: projectID}, {Key: "id", Value: record.ID}}).Decode(&existing)
		if findErr == nil {
			return nil, todoTeach("todo.id", "be absent from this Project's Mongo TODO collection before individual admission", ErrConflict)
		}
		if !errors.Is(findErr, mongo.ErrNoDocuments) {
			return nil, ErrUnavailable
		}
		now := bson.DateTime(time.Now().UTC().UnixMilli())
		record.ObjectID = bson.NewObjectID()
		record.Revision = 0
		record.CreatedAt, record.UpdatedAt = now, now
		record.Source.ImportedAt = now
		record.StageVerification = "unverified"
		if mongoBorn {
			contentDigest, digestErr := todoContentRevisionDigest(projectID, record.ID, 0, contentFromTODO(record))
			if digestErr != nil {
				return nil, ErrInvalid
			}
			record.Source = TODOSource{Kind: "mongo-content-revision", Ref: fmt.Sprintf("%s/%s/content/0", projectID, record.ID), Digest: contentDigest}
			record.StageVerification = "verified"
			record.VerifiedTransitionRef = &TODOProofReference{Kind: "created", Source: "mongo-todo-api", Reference: record.Source.Ref, SourceDigest: contentDigest, VerificationState: "verified"}
		}
		if _, err := collection.InsertOne(txCtx, record); err != nil {
			return nil, normalizeMongoError(err)
		}
		if err := insertTODOHistoryBaseline(txCtx, database, record); err != nil {
			return nil, err
		}
		return nil, nil
	})
	if err != nil {
		if errors.Is(err, ErrInvalid) || errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
			return TODOAdmissionReport{}, err
		}
		return TODOAdmissionReport{}, normalizeMongoError(err)
	}
	return TODOAdmissionReport{CompanyID: companyID, ProjectID: projectID, ReleaseID: record.ReleaseID, TODOID: record.ID, Revision: 0, ContentRevision: 0, ContentDigest: record.Source.Digest, SourcePath: record.Source.Path}, nil
}

func validateGenericTODOAdmission(record TODORecord) error {
	if !validID(record.ID) || !validID(record.ProjectID) || (record.ReleaseID == "") != (record.Position == nil) || (record.ReleaseID != "" && (!validID(record.ReleaseID) || *record.Position < 0)) {
		return todoTeach("todo.id/project_id/release_id/position", "provide stable identities and either both valid Release placement fields or neither", ErrInvalid)
	}
	if !validName(record.Title) || !validSummaryField(record.Objective, 1024) || !validSummaryField(record.NextExactStep, 512) || record.ArtifactType != "tactical_execution_contract" || !containsTODOString(canonicalTODOStages, record.Stage) || !validSummaryField(record.WorkState, 128) {
		return todoTeach("todo.title/objective/next_exact_step/artifact_type/stage/work_state", "provide typed fields within the canonical TODO limits", ErrInvalid)
	}
	if record.Qualifiers == nil || record.Sections == nil || len(record.Sections) > maxTODOSections || len(record.ProofReferences)+len(record.WaiverReferences)+len(record.ImplementationCommits) > maxTODOProofs {
		return todoTeach("todo.qualifiers/sections/proof_refs", "use non-null bounded arrays and at most 128 sections and 64 evidence references", ErrInvalid)
	}
	seenQualifiers := map[string]bool{}
	for _, qualifier := range record.Qualifiers {
		if !containsTODOString(canonicalTODOQualifiers, qualifier) || seenQualifiers[qualifier] {
			return todoTeach("todo.qualifiers", "contain unique canonical qualifiers", ErrInvalid)
		}
		seenQualifiers[qualifier] = true
	}
	seenSections := map[string]bool{}
	for i, section := range record.Sections {
		if strings.TrimSpace(section.Key) == "" || strings.TrimSpace(section.Title) == "" || !utf8.ValidString(section.BodyMarkdown) || section.Position != i || seenSections[section.Title] || len(section.BodyMarkdown) > 64<<10 || (section.Ownership != "contract" && section.Ownership != "record") {
			return todoTeach(fmt.Sprintf("todo.sections[%d]", i), "preserve unique titled Markdown sections in source order, explicitly classified contract or record, with bodies no larger than 64 KiB", ErrInvalid)
		}
		seenSections[section.Title] = true
	}
	for _, proof := range append(append([]TODOProofReference{}, record.ProofReferences...), record.WaiverReferences...) {
		if !validProofReference(proof) || proof.VerificationState != "unverified" {
			return todoTeach("todo.proof_refs/waiver_refs", "contain valid source-bound references marked unverified", ErrInvalid)
		}
	}
	for _, commit := range record.ImplementationCommits {
		if strings.TrimSpace(commit.Repository) == "" || !validGitCommit(commit.Commit) || !validSourceDigest(commit.SourceDigest) || commit.VerificationState != "unverified" {
			return todoTeach("todo.implementation_commits", "contain full commit IDs and source digests marked unverified", ErrInvalid)
		}
	}
	for _, scope := range record.DiffExpectation {
		if !validName(scope.Repository) || strings.TrimSpace(scope.Baseline) == "" || scope.ExpectedPaths == nil || scope.NotExpectedPaths == nil {
			return todoTeach("todo.diff_expectation", "provide a repository, baseline, and explicit expected/not-expected path arrays", ErrInvalid)
		}
		for _, pattern := range append(append([]string{}, scope.ExpectedPaths...), scope.NotExpectedPaths...) {
			if !validRepositoryPattern(pattern) {
				return todoTeach("todo.diff_expectation.paths", "use repository-relative valid path patterns", ErrInvalid)
			}
		}
	}
	for _, file := range record.AffectedFiles {
		if !validRepositoryPattern(file) {
			return todoTeach("todo.affected_files", "contain repository-relative valid paths", ErrInvalid)
		}
	}
	foundationSource := validID(record.Source.Foundation) && validGitCommit(record.Source.Revision) && validRepositoryPattern(record.Source.Path) && validSourceDigest(record.Source.Digest) && record.Source.Kind == "" && record.Source.Ref == ""
	mongoSourceAbsent := record.Source == (TODOSource{})
	if (!foundationSource && !mongoSourceAbsent) || record.Source.MappingDigest != "" || record.Source.ImportedAt != 0 || record.ObjectID != bson.NilObjectID || record.Revision != 0 || record.CreatedAt != 0 || record.UpdatedAt != 0 || record.StageVerification != "" || record.VerifiedTransitionRef != nil || record.LastOperation != nil {
		return todoTeach("todo.source/operational_metadata", "provide valid Foundation provenance for legacy admission or omit provenance for Mongo creation, and leave Mongo-owned metadata unset", ErrInvalid)
	}
	data, err := bson.Marshal(record)
	if err != nil || len(data) > maxTODORecordBytes {
		return todoTeach("todo", "fit within the 1 MiB UTF-8/BSON document limit", ErrInvalid)
	}
	return nil
}

func validateMongoTODOCreation(record TODORecord) error {
	if record.Stage != "Planning" || record.Source != (TODOSource{}) {
		return todoTeach("todo.stage/source", "start at Planning and omit all caller-authored provenance", ErrInvalid)
	}
	return validateGenericTODOAdmission(record)
}

func validateTODOProjectMembership(ctx context.Context, database *mongo.Database, companyID, projectID string) error {
	var project projectDocument
	if err := database.Collection(projectsCollection).FindOne(ctx, bson.D{{Key: "id", Value: projectID}}).Decode(&project); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return todoTeach("project_id", "name an existing Project in the resolved Company tenant", ErrNotFound)
		}
		return ErrUnavailable
	}
	if project.ObjectID.IsZero() || project.ID != projectID || project.CompanyID != companyID {
		return todoTeach("company_id/project_id", "match the registered same-Company Project", ErrConflict)
	}
	return nil
}

func validateTODOReleaseMembership(ctx context.Context, database *mongo.Database, companyID, projectID, releaseID string) error {
	var project projectDocument
	if err := database.Collection(projectsCollection).FindOne(ctx, bson.D{{Key: "id", Value: projectID}}).Decode(&project); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return todoTeach("project_id", "name an existing Project in the resolved Company tenant", ErrNotFound)
		}
		return ErrUnavailable
	}
	if project.ObjectID.IsZero() || project.ID != projectID || project.CompanyID != companyID {
		return todoTeach("company_id/project_id", "match the registered same-Company Project", ErrConflict)
	}
	var release roadmapRelease
	if err := database.Collection(releasesCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "id", Value: releaseID}}).Decode(&release); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return todoTeach("release_id", "name an existing Release in the same Project", ErrNotFound)
		}
		return ErrUnavailable
	}
	if release.StorageID.IsZero() || release.ProjectID != projectID || release.ID != releaseID || !validID(release.EpicID) {
		return todoTeach("release_id", "match a valid Release in the same Project", ErrConflict)
	}
	var epic roadmapEpic
	if err := database.Collection(epicsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "id", Value: release.EpicID}}).Decode(&epic); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return todoTeach("release_id/epic_id", "belong to an existing Epic in the same Project", ErrConflict)
		}
		return ErrUnavailable
	}
	if epic.StorageID.IsZero() || epic.ProjectID != projectID || epic.ID != release.EpicID {
		return todoTeach("release_id/epic_id", "belong to an existing Epic in the same Project", ErrConflict)
	}
	return nil
}
