package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const (
	todosCollection     = "todos"
	todoSchemaVersion   = "builder-selected-todos-v1"
	maxTODORecordBytes  = 1 << 20
	maxTODOSections     = 128
	maxTODOProofs       = 64
	maxTODOPathPatterns = 512
)

var canonicalTODOStages = []string{
	"Planning", "Implementation-Approval-Pending", "Ready-for-Implementation",
	"In-Progress", "Local-Implemented", "Promotion-Lane", "Completed",
}

var canonicalTODOQualifiers = []string{"Blocked", "Provisional"}

type selectedTODOSourceContract struct {
	ProjectID     string
	ReleaseID     string
	EpicID        string
	Position      int
	Stage         string
	Qualifier     string
	Foundation    string
	FoundationSHA string
	Path          string
	SourceDigest  string
}

var selectedTODOSources = map[string]selectedTODOSourceContract{
	"t-9936500c9582e07e": {
		ProjectID: "boora", ReleaseID: "release-v0-7-1-participant-media", EpicID: "epic-pilot", Position: 0,
		Stage: "Planning", Qualifier: "Provisional", Foundation: "boora",
		FoundationSHA: "cc56f7f4da7682251a500228afda1201560d1fe9",
		Path:          "todos/active/v0.7.1/TODO-v0.7.1-release-package.md",
		SourceDigest:  "sha256:83c5056abd55159d0acbee8535f5535c347df59dc215f2a376731f7f8303c446",
	},
	"t-cf5c0ffd07c4f50d": {
		ProjectID: "boora", ReleaseID: "release-v0-7-1-participant-media", EpicID: "epic-pilot", Position: 1,
		Stage: "In-Progress", Foundation: "boora",
		FoundationSHA: "cc56f7f4da7682251a500228afda1201560d1fe9",
		Path:          "todos/active/v0.7.1/TODO-v0.7.1-event-participant-avatar-public-media-url.md",
		SourceDigest:  "sha256:9910ae4da6bdff9973d3b257459254c27612d7437bb93b4734de285936133aae",
	},
	"t-b9e3ea9e3a616b01": {
		ProjectID: "boora", ReleaseID: "release-v0-7-1-participant-media", EpicID: "epic-pilot", Position: 2,
		Stage: "Completed", Foundation: "boora",
		FoundationSHA: "cc56f7f4da7682251a500228afda1201560d1fe9",
		Path:          "todos/active/v0.7.1/TODO-v0.7.1-flutter-stage-domain-and-resubmission.md",
		SourceDigest:  "sha256:b46bade5bfeed2d04b7b9464625b9ec2970cb73d0ce318a640dffde89d389302",
	},
	"builder-company-tenant-mongo-registry": {
		ProjectID: "builder", ReleaseID: "r-08", EpicID: "e-06", Position: 0,
		Stage: "In-Progress", Foundation: "builder",
		FoundationSHA: "76d26a6ed63b0589839a0880c4ecd08da0944f50",
		Path:          "todos/active/TODO-builder-company-tenant-mongo-registry.md",
		SourceDigest:  "sha256:f220bc304e7d1f9e5ddfa611f7bf190200b09a81c3a6120e6e834e014de3c7a9",
	},
}

// additionalTODOSources is admitted after the original four-record cut. Keep
// the first import's exact cardinality and source mapping unchanged.
var additionalTODOSources = map[string]selectedTODOSourceContract{
	"builder-mongo-v0-7-1-todo-migration": {
		ProjectID: "builder", ReleaseID: "r-08", EpicID: "e-06", Position: 1,
		Stage: "In-Progress", Foundation: "builder",
		FoundationSHA: "e2430eff748f72fd7abb6dd6f903b12484ed5e61",
		Path:          "todos/active/TODO-builder-mongo-v0-7-1-todo-migration.md",
		SourceDigest:  "sha256:d2ee2fa7fcb027b5e7ee229989a86966750bd7cde2d6cfe3bcdae8883f63c2dc",
	},
	"builder-mongo-selected-todo-stage-verification": {
		ProjectID: "builder", ReleaseID: "r-08", EpicID: "e-06", Position: 2,
		Stage: "Planning", Foundation: "builder",
		FoundationSHA: "e2430eff748f72fd7abb6dd6f903b12484ed5e61",
		Path:          "todos/active/TODO-builder-mongo-selected-todo-stage-verification.md",
		SourceDigest:  "sha256:48b9a25580856a348f1db227b6f15ed0cd1ad79cfe504b711df2d570d8da3ad4",
	},
}

func selectedTODOContract(id string) (selectedTODOSourceContract, bool) {
	if source, ok := selectedTODOSources[id]; ok {
		return source, true
	}
	source, ok := additionalTODOSources[id]
	return source, ok
}

// TODOSection is authored Markdown preserved in source order. Typed fields,
// not headings or statements here, remain the API's governance authority.
type TODOSection struct {
	Key          string `json:"key" bson:"key"`
	Title        string `json:"title" bson:"title"`
	BodyMarkdown string `json:"body_markdown" bson:"body_markdown"`
	Position     int    `json:"position" bson:"position"`
	Ownership    string `json:"ownership,omitempty" bson:"ownership,omitempty"`
}

// TODOSource records the immutable source identity supplied by the trusted
// executor. ImportedAt is assigned by Go and never accepted from the request.
type TODOSource struct {
	Kind          string        `json:"kind,omitempty" bson:"kind,omitempty"`
	Ref           string        `json:"ref,omitempty" bson:"ref,omitempty"`
	Foundation    string        `json:"foundation,omitempty" bson:"foundation,omitempty"`
	Revision      string        `json:"revision,omitempty" bson:"revision,omitempty"`
	Path          string        `json:"path,omitempty" bson:"path,omitempty"`
	Digest        string        `json:"digest,omitempty" bson:"digest,omitempty"`
	MappingDigest string        `json:"-" bson:"mapping_digest,omitempty"`
	ImportedAt    bson.DateTime `json:"-" bson:"imported_at,omitempty"`
}

type TODOProofReference struct {
	Kind              string `json:"kind" bson:"kind"`
	Source            string `json:"source" bson:"source"`
	Reference         string `json:"ref" bson:"ref"`
	SourceDigest      string `json:"source_digest" bson:"source_digest"`
	VerificationState string `json:"verification_state" bson:"verification_state"`
}

type TODORepositoryScope struct {
	Repository       string   `json:"repository" bson:"repository"`
	Baseline         string   `json:"baseline" bson:"baseline"`
	ExpectedPaths    []string `json:"expected_paths" bson:"expected_paths"`
	NotExpectedPaths []string `json:"not_expected_paths" bson:"not_expected_paths"`
}

type TODOCommitReference struct {
	Repository        string `json:"repository" bson:"repository"`
	Commit            string `json:"commit" bson:"commit"`
	SourceDigest      string `json:"source_digest" bson:"source_digest"`
	VerificationState string `json:"verification_state" bson:"verification_state"`
}

// TODORecord is the closed typed Mongo document. The executor maps the pinned
// source sections into these fields; Go validates the envelope and persists it.
type TODORecord struct {
	ObjectID              bson.ObjectID         `json:"-" bson:"_id,omitempty"`
	ID                    string                `json:"id" bson:"id"`
	ProjectID             string                `json:"project_id" bson:"project_id"`
	ReleaseID             string                `json:"release_id,omitempty" bson:"release_id,omitempty"`
	Position              *int                  `json:"position,omitempty" bson:"position,omitempty"`
	Title                 string                `json:"title" bson:"title"`
	Objective             string                `json:"objective" bson:"objective"`
	NextExactStep         string                `json:"next_exact_step" bson:"next_exact_step"`
	ArtifactType          string                `json:"artifact_type" bson:"artifact_type"`
	Stage                 string                `json:"stage" bson:"stage"`
	Qualifiers            []string              `json:"qualifiers" bson:"qualifiers"`
	WorkState             string                `json:"work_state,omitempty" bson:"work_state,omitempty"`
	Sections              []TODOSection         `json:"sections" bson:"sections"`
	ProofReferences       []TODOProofReference  `json:"proof_refs" bson:"proof_refs,omitempty"`
	WaiverReferences      []TODOProofReference  `json:"waiver_refs" bson:"waiver_refs,omitempty"`
	DiffExpectation       []TODORepositoryScope `json:"diff_expectation" bson:"diff_expectation,omitempty"`
	ImplementationCommits []TODOCommitReference `json:"implementation_commits" bson:"implementation_commits,omitempty"`
	AffectedFiles         []string              `json:"affected_files" bson:"affected_files"`
	Content               *TODOContent          `json:"content,omitempty" bson:"-"`
	Source                TODOSource            `json:"source" bson:"source"`
	StageVerification     string                `json:"stage_verification" bson:"stage_verification"`
	VerifiedTransitionRef *TODOProofReference   `json:"verified_transition_ref,omitempty" bson:"verified_transition_ref,omitempty"`
	LastOperation         *TODOOperationState   `json:"-" bson:"last_operation,omitempty"`
	Revision              int64                 `json:"revision" bson:"revision"`
	CreatedAt             bson.DateTime         `json:"-" bson:"created_at"`
	UpdatedAt             bson.DateTime         `json:"-" bson:"updated_at"`
}

// TODOOperationState makes a retried last CAS operation idempotent without
// introducing a second event/history collection.
type TODOOperationState struct {
	ID             string `bson:"id"`
	Kind           string `bson:"kind"`
	RequestDigest  string `bson:"request_digest"`
	ResultRevision int64  `bson:"result_revision"`
}

type SelectedTODOImportRequest struct {
	SchemaVersion string       `json:"schema_version"`
	TODOs         []TODORecord `json:"todos"`
}

type SelectedTODOImportReport struct {
	Imported int    `json:"imported"`
	NoOp     bool   `json:"no_op"`
	Digest   string `json:"source_digest"`
}

type TODOValidationError struct {
	Field    string
	Expected string
	Cause    error
}

func (e *TODOValidationError) Error() string {
	return fmt.Sprintf("TEACH: %s must %s", e.Field, e.Expected)
}

func (e *TODOValidationError) Unwrap() error { return e.Cause }

func todoTeach(field, expected string, cause error) error {
	if cause == nil {
		cause = ErrInvalid
	}
	return &TODOValidationError{Field: field, Expected: expected, Cause: cause}
}

// ValidateSelectedTODOImport enforces the frozen four-ID import contract and
// returns a deterministic digest over its typed input. Source Markdown is not
// parsed here; its published source revision, path and byte digest are pinned.
func ValidateSelectedTODOImport(request SelectedTODOImportRequest) (SelectedTODOImportReport, error) {
	if request.SchemaVersion != todoSchemaVersion || len(request.TODOs) != len(selectedTODOSources) {
		return SelectedTODOImportReport{}, todoTeach("schema_version/todos", "use builder-selected-todos-v1 and provide exactly the four approved IDs", ErrInvalid)
	}
	seen := make(map[string]bool, len(request.TODOs))
	encoded := make([]TODORecord, 0, len(request.TODOs))
	for _, record := range request.TODOs {
		contract, ok := selectedTODOSources[record.ID]
		if !ok || seen[record.ID] || record.ProjectID != contract.ProjectID || record.ReleaseID != contract.ReleaseID || record.Position == nil || *record.Position != contract.Position {
			return SelectedTODOImportReport{}, todoTeach("todos[].identity", "use each approved TODO ID once with its pinned Project, Release, and position", ErrConflict)
		}
		seen[record.ID] = true
		if err := validateTODORecord(record, contract); err != nil {
			return SelectedTODOImportReport{}, err
		}
		encoded = append(encoded, record)
	}
	for id := range selectedTODOSources {
		if !seen[id] {
			return SelectedTODOImportReport{}, todoTeach("todos[].id", "include selected TODO "+id, ErrInvalid)
		}
	}
	sortSelectedTODOs(encoded)
	data, err := bson.Marshal(bson.D{{Key: "schema_version", Value: request.SchemaVersion}, {Key: "todos", Value: encoded}})
	if err != nil || len(data) > len(request.TODOs)*maxTODORecordBytes {
		return SelectedTODOImportReport{}, ErrInvalid
	}
	hash := sha256.Sum256(data)
	return SelectedTODOImportReport{Imported: len(request.TODOs), Digest: "sha256:" + hex.EncodeToString(hash[:])}, nil
}

func validateTODORecord(record TODORecord, source selectedTODOSourceContract) error {
	if !validName(record.Title) || !validSummaryField(record.Objective, 1024) || !validSummaryField(record.NextExactStep, 512) || record.ArtifactType != "tactical_execution_contract" || record.Stage != source.Stage {
		return todoTeach("todos["+record.ID+"].title/objective/next_exact_step/artifact_type/stage", "provide a valid title, objective up to 1024 bytes, next_exact_step up to 512 bytes, tactical_execution_contract, and stage "+source.Stage, ErrInvalid)
	}
	if !containsTODOString(canonicalTODOStages, record.Stage) || record.WorkState == "" {
		return todoTeach("todos["+record.ID+"].stage/work_state", "use a canonical stage and a nonempty authored work_state", ErrInvalid)
	}
	if (source.Qualifier == "") != (len(record.Qualifiers) == 0) {
		return todoTeach("todos["+record.ID+"].qualifiers", "match the pinned source qualifier set", ErrConflict)
	}
	seenQualifiers := map[string]bool{}
	for _, qualifier := range record.Qualifiers {
		if !containsTODOString(canonicalTODOQualifiers, qualifier) || seenQualifiers[qualifier] || qualifier != source.Qualifier {
			return todoTeach("todos["+record.ID+"].qualifiers", "contain unique values from Blocked and Provisional matching the pinned source", ErrInvalid)
		}
		seenQualifiers[qualifier] = true
	}
	if record.Source.Foundation != source.Foundation || record.Source.Revision != source.FoundationSHA || record.Source.Path != source.Path || record.Source.Digest != source.SourceDigest || record.Source.MappingDigest != "" || record.Source.ImportedAt != 0 || record.StageVerification != "" || record.VerifiedTransitionRef != nil || record.LastOperation != nil || record.Revision != 0 || record.CreatedAt != 0 || record.UpdatedAt != 0 || !record.ObjectID.IsZero() {
		return todoTeach("todos["+record.ID+"].source", "match Foundation "+source.Foundation+" revision "+source.FoundationSHA+", path "+source.Path+", and digest "+source.SourceDigest, ErrConflict)
	}
	if len(record.Sections) > maxTODOSections || len(record.ProofReferences)+len(record.WaiverReferences)+len(record.ImplementationCommits) > maxTODOProofs {
		return todoTeach("todos["+record.ID+"].sections/proof_refs/waiver_refs/implementation_commits", "stay within 128 ordered sections and 64 combined proof references", ErrInvalid)
	}
	if record.Qualifiers == nil || record.Sections == nil {
		return todoTeach("todos["+record.ID+"].qualifiers/sections", "encode empty collections as [] rather than null", ErrInvalid)
	}
	seenSections := map[string]bool{}
	for i, section := range record.Sections {
		if strings.TrimSpace(section.Key) == "" || strings.TrimSpace(section.Title) == "" || section.Position != i || seenSections[section.Title] || len(section.BodyMarkdown) > 64<<10 {
			return todoTeach("todos["+record.ID+"].sections["+fmt.Sprint(i)+"]", "preserve authored headings as uniquely titled, ordered narrative sections with bounded bodies", ErrInvalid)
		}
		seenSections[section.Title] = true
	}
	for _, proof := range append(append([]TODOProofReference{}, record.ProofReferences...), record.WaiverReferences...) {
		if strings.TrimSpace(proof.Kind) == "" || strings.TrimSpace(proof.Source) == "" || strings.TrimSpace(proof.Reference) == "" || !validSourceDigest(proof.SourceDigest) || proof.VerificationState != "unverified" {
			return todoTeach("todos["+record.ID+"].proof_refs/waiver_refs", "use bounded references with source digests and verification_state unverified at import", ErrInvalid)
		}
	}
	for _, commit := range record.ImplementationCommits {
		if strings.TrimSpace(commit.Repository) == "" || !validGitCommit(commit.Commit) || !validSourceDigest(commit.SourceDigest) || commit.VerificationState != "unverified" {
			return todoTeach("todos["+record.ID+"].implementation_commits", "use a full commit ID, source digest, and verification_state unverified at import", ErrInvalid)
		}
	}
	patternCount := 0
	seenRepositories := map[string]bool{}
	for _, scope := range record.DiffExpectation {
		if strings.TrimSpace(scope.Repository) == "" || strings.TrimSpace(scope.Baseline) == "" || seenRepositories[scope.Repository] || scope.ExpectedPaths == nil || scope.NotExpectedPaths == nil {
			return todoTeach("todos["+record.ID+"].diff_expectation", "provide one baseline and two explicit path arrays per repository", ErrInvalid)
		}
		seenRepositories[scope.Repository] = true
		for _, pattern := range append(append([]string{}, scope.ExpectedPaths...), scope.NotExpectedPaths...) {
			if !validRepositoryPattern(pattern) {
				return todoTeach("todos["+record.ID+"].diff_expectation[].expected_paths/not_expected_paths", "use repository-relative valid path or glob patterns", ErrInvalid)
			}
			patternCount++
		}
	}
	if patternCount > maxTODOPathPatterns || record.AffectedFiles != nil {
		return todoTeach("todos["+record.ID+"].diff_expectation/affected_files", "stay within 512 path patterns and leave affected_files null until attributable commits are verified", ErrInvalid)
	}
	record.Revision = 0
	record.CreatedAt = 0
	record.UpdatedAt = 0
	record.Source.ImportedAt = 0
	data, err := bson.Marshal(record)
	if err != nil || len(data) > maxTODORecordBytes {
		return todoTeach("todos["+record.ID+"]", "fit within the 1 MiB UTF-8/BSON record limit", ErrInvalid)
	}
	return nil
}

func containsTODOString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func validSourceDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(decoded) == sha256.Size
}

func validGitCommit(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validRepositoryPattern(value string) bool {
	if value == "" || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") || strings.Contains(value, "..") || strings.ContainsRune(value, 0) {
		return false
	}
	_, err := path.Match(value, "")
	return err == nil
}

// TODOCollectionValidator declares the closed MongoDB document contract.
func TODOCollectionValidator() bson.D {
	return todoCollectionValidator(true, true, true)
}

func TODOCollectionValidatorBeforeCreation() bson.D {
	return todoCollectionValidatorWithCreation(true, true, true, false)
}

func priorTODOCollectionValidator() bson.D {
	return todoCollectionValidator(false, false, false)
}

func TODOCollectionValidatorBeforeRecordEvents() bson.D {
	return todoCollectionValidator(true, false, true)
}
func TODOCollectionValidatorBeforeOwnership() bson.D {
	return todoCollectionValidator(true, false, false)
}

func todoCollectionValidator(allowContentEdit, allowRecordEvents, allowOwnership bool) bson.D {
	return todoCollectionValidatorWithCreation(allowContentEdit, allowRecordEvents, allowOwnership, true)
}

func todoCollectionValidatorWithCreation(allowContentEdit, allowRecordEvents, allowOwnership, allowCreation bool) bson.D {
	properties := bson.D{
		{Key: "_id", Value: bson.D{{Key: "bsonType", Value: "objectId"}}},
		{Key: "id", Value: nonemptyStringSchema()},
		{Key: "project_id", Value: nonemptyStringSchema()},
		{Key: "release_id", Value: nonemptyStringSchema()},
		{Key: "position", Value: nonnegativeIntSchema()},
		{Key: "title", Value: nonemptyStringSchema()},
		{Key: "objective", Value: nonemptyStringSchema()},
		{Key: "next_exact_step", Value: nonemptyStringSchema()},
		{Key: "artifact_type", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "enum", Value: bson.A{"tactical_execution_contract"}}}},
		{Key: "stage", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "enum", Value: stringTODOArray(canonicalTODOStages)}}},
		{Key: "qualifiers", Value: bson.D{{Key: "bsonType", Value: "array"}, {Key: "uniqueItems", Value: true}, {Key: "items", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "enum", Value: stringTODOArray(canonicalTODOQualifiers)}}}}},
		{Key: "work_state", Value: bson.D{{Key: "bsonType", Value: "string"}}},
		{Key: "sections", Value: sectionsSchema(allowOwnership)},
		{Key: "proof_refs", Value: proofArraySchema()},
		{Key: "waiver_refs", Value: proofArraySchema()},
		{Key: "diff_expectation", Value: diffArraySchema()},
		{Key: "implementation_commits", Value: commitArraySchema()},
		{Key: "affected_files", Value: bson.D{{Key: "bsonType", Value: bson.A{"array", "null"}}, {Key: "items", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "minLength", Value: 1}}}}},
		{Key: "source", Value: sourceSchemaForCreation(allowCreation)},
		{Key: "revision", Value: bson.D{{Key: "bsonType", Value: bson.A{"int", "long"}}, {Key: "minimum", Value: int64(0)}}},
		{Key: "created_at", Value: bson.D{{Key: "bsonType", Value: "date"}}},
		{Key: "updated_at", Value: bson.D{{Key: "bsonType", Value: "date"}}},
		{Key: "stage_verification", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "enum", Value: bson.A{"unverified", "verified"}}}},
		{Key: "verified_transition_ref", Value: verifiedTransitionSchema()},
		{Key: "last_operation", Value: lastTODOOperationSchema(allowContentEdit, allowRecordEvents)},
	}
	required := bson.A{"_id", "id", "project_id", "title", "objective", "next_exact_step", "artifact_type", "stage", "qualifiers", "sections", "affected_files", "source", "revision", "created_at", "updated_at", "stage_verification"}
	schema := bson.D{
		{Key: "bsonType", Value: "object"},
		{Key: "required", Value: required},
		{Key: "additionalProperties", Value: false},
		{Key: "properties", Value: properties},
	}
	if allowCreation {
		schema = append(schema, bson.E{Key: "oneOf", Value: bson.A{
			bson.D{{Key: "required", Value: bson.A{"release_id", "position"}}},
			bson.D{{Key: "not", Value: bson.D{{Key: "anyOf", Value: bson.A{bson.D{{Key: "required", Value: bson.A{"release_id"}}}, bson.D{{Key: "required", Value: bson.A{"position"}}}}}}}},
		}})
	}
	return bson.D{{Key: "$jsonSchema", Value: schema}}
}

func verifiedTransitionSchema() bson.D {
	return bson.D{{Key: "bsonType", Value: "object"}, {Key: "required", Value: bson.A{"kind", "source", "ref", "source_digest", "verification_state"}}, {Key: "additionalProperties", Value: false}, {Key: "properties", Value: bson.D{
		{Key: "kind", Value: nonemptyStringSchema()}, {Key: "source", Value: nonemptyStringSchema()}, {Key: "ref", Value: nonemptyStringSchema()},
		{Key: "source_digest", Value: nonemptyStringSchema()}, {Key: "verification_state", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "enum", Value: bson.A{"verified"}}}},
	}}}
}

func lastTODOOperationSchema(allowContentEdit, allowRecordEvents bool) bson.D {
	kinds := bson.A{"transition", "verify-current-stage"}
	if allowContentEdit {
		kinds = append(kinds, "content-edit")
	}
	if allowRecordEvents {
		kinds = append(kinds, "evidence-edit", "placement-edit", "historical-adoption", "content-migration")
	}
	return bson.D{{Key: "bsonType", Value: "object"}, {Key: "required", Value: bson.A{"id", "kind", "request_digest", "result_revision"}}, {Key: "additionalProperties", Value: false}, {Key: "properties", Value: bson.D{
		{Key: "id", Value: nonemptyStringSchema()}, {Key: "kind", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "enum", Value: kinds}}},
		{Key: "request_digest", Value: nonemptyStringSchema()}, {Key: "result_revision", Value: nonnegativeLongSchema()},
	}}}
}

func TODOCollectionOptions() bson.D {
	return bson.D{{Key: "validator", Value: TODOCollectionValidator()}, {Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"}}
}

func nonemptyStringSchema() bson.D {
	return bson.D{{Key: "bsonType", Value: "string"}, {Key: "minLength", Value: 1}}
}

func nonnegativeIntSchema() bson.D {
	return bson.D{{Key: "bsonType", Value: "int"}, {Key: "minimum", Value: 0}}
}

func nonnegativeLongSchema() bson.D {
	return bson.D{{Key: "bsonType", Value: "long"}, {Key: "minimum", Value: int64(0)}}
}

func stringTODOArray(values []string) bson.A {
	result := make(bson.A, len(values))
	for i, value := range values {
		result[i] = value
	}
	return result
}

func sectionsSchema(allowOwnership bool) bson.D {
	section := bson.D{
		{Key: "bsonType", Value: "object"},
		{Key: "required", Value: bson.A{"key", "title", "body_markdown", "position"}},
		{Key: "additionalProperties", Value: false},
		{Key: "properties", Value: bson.D{
			{Key: "key", Value: nonemptyStringSchema()},
			{Key: "title", Value: nonemptyStringSchema()},
			{Key: "body_markdown", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "maxLength", Value: 65536}}},
			{Key: "position", Value: nonnegativeIntSchema()},
		}},
	}
	if allowOwnership {
		section[3].Value = append(section[3].Value.(bson.D), bson.E{Key: "ownership", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "enum", Value: bson.A{"contract", "record"}}}})
	}
	return bson.D{{Key: "bsonType", Value: "array"}, {Key: "maxItems", Value: maxTODOSections}, {Key: "items", Value: section}}
}

func proofArraySchema() bson.D {
	proof := bson.D{
		{Key: "bsonType", Value: "object"},
		{Key: "required", Value: bson.A{"kind", "source", "ref", "source_digest", "verification_state"}},
		{Key: "additionalProperties", Value: false},
		{Key: "properties", Value: bson.D{
			{Key: "kind", Value: nonemptyStringSchema()}, {Key: "source", Value: nonemptyStringSchema()},
			{Key: "ref", Value: nonemptyStringSchema()}, {Key: "source_digest", Value: nonemptyStringSchema()},
			{Key: "verification_state", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "enum", Value: bson.A{"unverified", "verified", "rejected"}}}},
		}},
	}
	return bson.D{{Key: "bsonType", Value: "array"}, {Key: "maxItems", Value: maxTODOProofs}, {Key: "items", Value: proof}}
}

func diffArraySchema() bson.D {
	scope := bson.D{
		{Key: "bsonType", Value: "object"},
		{Key: "required", Value: bson.A{"repository", "baseline", "expected_paths", "not_expected_paths"}},
		{Key: "additionalProperties", Value: false},
		{Key: "properties", Value: bson.D{
			{Key: "repository", Value: nonemptyStringSchema()}, {Key: "baseline", Value: nonemptyStringSchema()},
			{Key: "expected_paths", Value: stringArraySchema()}, {Key: "not_expected_paths", Value: stringArraySchema()},
		}},
	}
	return bson.D{{Key: "bsonType", Value: "array"}, {Key: "items", Value: scope}}
}

func stringArraySchema() bson.D {
	return bson.D{{Key: "bsonType", Value: "array"}, {Key: "maxItems", Value: maxTODOPathPatterns}, {Key: "items", Value: nonemptyStringSchema()}}
}

func commitArraySchema() bson.D {
	commit := bson.D{
		{Key: "bsonType", Value: "object"},
		{Key: "required", Value: bson.A{"repository", "commit", "source_digest", "verification_state"}},
		{Key: "additionalProperties", Value: false},
		{Key: "properties", Value: bson.D{
			{Key: "repository", Value: nonemptyStringSchema()}, {Key: "commit", Value: nonemptyStringSchema()},
			{Key: "source_digest", Value: nonemptyStringSchema()},
			{Key: "verification_state", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "enum", Value: bson.A{"unverified", "verified", "rejected"}}}},
		}},
	}
	return bson.D{{Key: "bsonType", Value: "array"}, {Key: "maxItems", Value: maxTODOProofs}, {Key: "items", Value: commit}}
}

func sourceSchema() bson.D {
	return sourceSchemaForCreation(true)
}

func sourceSchemaForCreation(allowCreation bool) bson.D {
	if !allowCreation {
		return bson.D{{Key: "bsonType", Value: "object"}, {Key: "required", Value: bson.A{"foundation", "revision", "path", "digest", "mapping_digest", "imported_at"}}, {Key: "additionalProperties", Value: false}, {Key: "properties", Value: bson.D{
			{Key: "foundation", Value: nonemptyStringSchema()}, {Key: "revision", Value: nonemptyStringSchema()},
			{Key: "path", Value: nonemptyStringSchema()}, {Key: "digest", Value: nonemptyStringSchema()},
			{Key: "mapping_digest", Value: nonemptyStringSchema()},
			{Key: "imported_at", Value: bson.D{{Key: "bsonType", Value: "date"}}},
		}}}
	}
	source := bson.D{
		{Key: "bsonType", Value: "object"},
		{Key: "oneOf", Value: bson.A{
			bson.D{{Key: "required", Value: bson.A{"foundation", "revision", "path", "digest", "mapping_digest", "imported_at"}}},
			bson.D{{Key: "required", Value: bson.A{"kind", "ref", "digest"}}},
		}},
		{Key: "additionalProperties", Value: false},
		{Key: "properties", Value: bson.D{
			{Key: "kind", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "enum", Value: bson.A{"mongo-content-revision"}}}},
			{Key: "ref", Value: nonemptyStringSchema()},
			{Key: "foundation", Value: nonemptyStringSchema()}, {Key: "revision", Value: nonemptyStringSchema()},
			{Key: "path", Value: nonemptyStringSchema()}, {Key: "digest", Value: nonemptyStringSchema()},
			{Key: "mapping_digest", Value: nonemptyStringSchema()},
			{Key: "imported_at", Value: bson.D{{Key: "bsonType", Value: "date"}}},
		}},
	}
	return source
}
