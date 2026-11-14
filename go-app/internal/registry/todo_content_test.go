package registry

import (
	"errors"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func validTODOContent() TODOContent {
	return TODOContent{
		SchemaVersion: todoContentSchemaVersion,
		Context:       "Provide a concise execution context.",
		Scope:         &TODOContentScope{Included: []string{"typed validation"}, Excluded: []string{}},
		Criteria:      []TODOContentItem{{ID: "criterion-one", Text: "Invalid payloads receive field-addressed TEACH."}},
		ValidationScenarios: []TODOValidationScenario{{
			ID: "scenario-one", CriterionIDs: []string{"criterion-one"}, Method: "Run the focused registry tests.", Expected: "Invalid values identify their field.",
		}},
		ImplementationPlan: []TODOContentItem{{ID: "plan-one", Text: "Define bounded typed content."}},
		Decisions:          []TODOContentItem{},
		Risks:              []TODOContentRisk{},
		References:         []TODOContentReference{},
	}
}

func TestValidateTODOContentAcceptsCanonicalTypedPayload(t *testing.T) {
	if err := ValidateTODOContent(validTODOContent(), false); err != nil {
		t.Fatalf("valid schema-v2 content rejected: %v", err)
	}
}

func TestValidateTODOLocalChecksAreTypedAndRepositoryScoped(t *testing.T) {
	content := validTODOContent()
	content.LocalChecks = []TODOLocalCheck{{
		ID: "G-TEST", RepositoryID: "belluga/belluga_builder_docker",
		Argv:             []string{"docker", "build", "--target", "test", "-f", "go-app/Dockerfile", "."},
		WorkingDirectory: ".", InputPaths: []string{"go-app/**", "builder-app/service/**"},
	}}
	if err := ValidateTODOContent(content, false); err != nil {
		t.Fatalf("valid explicit local check rejected: %v", err)
	}
	content.LocalChecks[0].ID = "lint_check_2"
	if err := ValidateTODOContent(content, false); err != nil {
		t.Fatalf("generic project check ID rejected: %v", err)
	}
	content.LocalChecks[0].ID = "G-TEST"
	for _, test := range []struct {
		name  string
		edit  func(*TODOLocalCheck)
		field string
	}{
		{"repository", func(c *TODOLocalCheck) { c.RepositoryID = "builder" }, "repository_id"},
		{"empty argv", func(c *TODOLocalCheck) { c.Argv = nil }, "argv"},
		{"absolute cwd", func(c *TODOLocalCheck) { c.WorkingDirectory = "/tmp" }, "working_directory"},
		{"traversal input", func(c *TODOLocalCheck) { c.InputPaths = []string{"../outside"} }, "input_paths[0]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := validTODOContent()
			check := content.LocalChecks[0]
			test.edit(&check)
			candidate.LocalChecks = []TODOLocalCheck{check}
			err := ValidateTODOContent(candidate, false)
			var teach *TODOValidationError
			if !errors.As(err, &teach) || !strings.Contains(teach.Field, test.field) {
				t.Fatalf("error=%#v, want TEACH field containing %q", err, test.field)
			}
		})
	}
}

func TestTODOContentMongoSchemaIncludesStrictLocalCheckShape(t *testing.T) {
	properties, ok := valueAt(TODOContentSchemaValidator(), "properties").(bson.D)
	if !ok {
		t.Fatal("content validator has no typed properties")
	}
	localChecks, ok := valueAt(properties, "local_checks").(bson.D)
	if !ok || valueAt(localChecks, "bsonType") != "array" || valueAt(localChecks, "maxItems") != 32 {
		t.Fatalf("local_checks schema is not bounded array: %#v", localChecks)
	}
	check, ok := valueAt(localChecks, "items").(bson.D)
	if !ok || valueAt(check, "additionalProperties") != false {
		t.Fatalf("local check schema does not reject extra fields: %#v", check)
	}
	checkProperties, ok := valueAt(check, "properties").(bson.D)
	if !ok || valueAt(checkProperties, "id") == nil || valueAt(checkProperties, "repository_id") == nil || valueAt(checkProperties, "argv") == nil || valueAt(checkProperties, "working_directory") == nil || valueAt(checkProperties, "input_paths") == nil {
		t.Fatalf("local check schema omits required typed fields: %#v", checkProperties)
	}
}

func TestTODOAuthoredContentV2FlattensTypedFieldsInBSON(t *testing.T) {
	content := TODOAuthoredContent{TODOContent: validTODOContent(), Title: "TODO: Typed contract", Objective: "Store the bounded typed fields.", DiffExpectation: []TODORepositoryScope{}, AffectedFiles: []string{}}
	data, err := bson.Marshal(content)
	if err != nil {
		t.Fatalf("marshal schema-v2 authored content: %v", err)
	}
	raw := bson.Raw(data)
	if raw.Lookup("schema_version").Int32() != int32(todoContentSchemaVersion) || raw.Lookup("context").StringValue() != content.Context || raw.Lookup("typed_content").Type != 0 {
		t.Fatalf("schema-v2 fields were not stored at the authored-content root: %s", raw)
	}
	var decoded TODOAuthoredContent
	if err := bson.Unmarshal(data, &decoded); err != nil || decoded.SchemaVersion != todoContentSchemaVersion || decoded.Context != content.Context || len(decoded.Criteria) != 1 {
		t.Fatalf("schema-v2 authored content did not round trip: %+v, %v", decoded, err)
	}
}

func TestTODOAuthoredContentBSONPreservesExplicitEmptyArrays(t *testing.T) {
	content := TODOAuthoredContent{TODOContent: validTODOContent(), Title: "TODO: Typed contract", Objective: "Preserve explicit empty arrays.", DiffExpectation: []TODORepositoryScope{}, AffectedFiles: []string{}}
	content.LocalChecks = []TODOLocalCheck{}
	encoded, err := bson.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	raw := bson.Raw(encoded)
	for _, field := range []string{"decisions", "risks", "references", "local_checks"} {
		if value := raw.Lookup(field); value.Type != bson.TypeArray || len(value.Array()) != 5 {
			t.Fatalf("explicit empty %s array was not retained in BSON: %v", field, value.Type)
		}
	}
	scope := raw.Lookup("scope").Document()
	if scope.Lookup("excluded").Type != bson.TypeArray || len(scope.Lookup("excluded").Array()) != 5 {
		t.Fatalf("explicit empty excluded scope was not retained in BSON: %v", scope.Lookup("excluded").Type)
	}
}

func TestValidateTODOContentDraftAllowsMissingApprovalFields(t *testing.T) {
	draft := TODOContent{SchemaVersion: todoContentSchemaVersion}
	if err := ValidateTODOContentDraft(draft); err != nil {
		t.Fatalf("incomplete Planning draft rejected: %v", err)
	}
	if err := ValidateTODOContent(draft, false); err == nil {
		t.Fatal("incomplete content passed the implementation-approval validator")
	}
}

func TestValidateTODOContentRejectsMissingAndInvalidFieldsWithTEACHPath(t *testing.T) {
	tests := []struct {
		name  string
		field string
		edit  func(*TODOContent)
	}{
		{"missing context", "content.context", func(content *TODOContent) { content.Context = " " }},
		{"duplicate criteria ID", "content.criteria[1].id", func(content *TODOContent) {
			content.Criteria = append(content.Criteria, TODOContentItem{ID: "criterion-one", Text: "Duplicate"})
		}},
		{"dangling criterion reference", "content.validation_scenarios[0].criterion_ids[0]", func(content *TODOContent) { content.ValidationScenarios[0].CriterionIDs = []string{"missing"} }},
		{"unbounded item text", "content.criteria[0].text", func(content *TODOContent) { content.Criteria[0].Text = strings.Repeat("x", 2049) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			content := validTODOContent()
			test.edit(&content)
			err := ValidateTODOContent(content, false)
			var teach *TODOValidationError
			if !errors.As(err, &teach) || teach.Field != test.field {
				t.Fatalf("error = %#v, want field-addressed TEACH for %q", err, test.field)
			}
		})
	}
}

func TestValidateTODOContentReservesLegacyContextForMigration(t *testing.T) {
	content := validTODOContent()
	content.LegacyContext = []TODOLegacyContext{{
		SourceRevision:  strings.Repeat("a", 40),
		SectionPosition: 0,
		Ownership:       "contract",
		Title:           "Original section",
		Body:            "Exact source text",
		Digest:          todoBodyDigest("Exact source text"),
	}}
	if err := ValidateTODOContent(content, false); err == nil {
		t.Fatal("new content accepted migration-only legacy_context")
	}
	if err := ValidateTODOContent(content, true); err != nil {
		t.Fatalf("migration content rejected: %v", err)
	}
	content.LegacyContext = append(content.LegacyContext, content.LegacyContext[0])
	if err := ValidateTODOContent(content, true); err == nil {
		t.Fatal("migration content accepted a duplicate source occurrence")
	}
	content.LegacyContext = content.LegacyContext[:1]
	content.LegacyContext[0].Ownership = "record"
	if err := ValidateTODOContent(content, true); err == nil {
		t.Fatal("migration content accepted a non-contract source occurrence")
	}
	content.LegacyContext[0].Ownership = "contract"
	content.LegacyContext[0].Digest = "sha256:" + strings.Repeat("a", 64)
	if err := ValidateTODOContent(content, true); err == nil {
		t.Fatal("migration content accepted a digest that does not match the preserved body")
	}
}

func TestMigrationLegacyContextUsesOriginalTODOSectionPositions(t *testing.T) {
	revision := strings.Repeat("a", 40)
	record := TODORecord{
		Source: TODOSource{Foundation: "boora", Revision: revision, Path: "todos/active/example.md", Digest: "sha256:" + strings.Repeat("b", 64)},
		Sections: []TODOSection{
			{Key: "contract", Title: "Current objective", BodyMarkdown: "Typed elsewhere", Position: 0, Ownership: "contract"},
			{Key: "historical-note", Title: "Reviewed contract note", BodyMarkdown: "Preserve these exact source bytes.", Position: 1, Ownership: "contract"},
		},
	}
	target := TODOAuthoredContent{TODOContent: validTODOContent(), Title: "TODO: Example", Objective: "Validate exact legacy locations.", DiffExpectation: []TODORepositoryScope{}, AffectedFiles: []string{}}
	target.TODOContent.LegacyContext = []TODOLegacyContext{{
		SourceRevision: revision, SectionPosition: 1, Ownership: "contract", Title: record.Sections[1].Title, Body: record.Sections[1].BodyMarkdown, Digest: todoBodyDigest(record.Sections[1].BodyMarkdown),
	}}
	if err := validateTODOContentMigrationTarget(record, target); err != nil {
		t.Fatalf("exact source occurrence was rejected: %v", err)
	}
	target.LegacyContext[0].SectionPosition = 0
	if err := validateTODOContentMigrationTarget(record, target); err == nil {
		t.Fatal("migration accepted a position/title/body mismatch")
	}
}

func TestPreserveLegacySectionsCopiesOnlyExactContractOccurrences(t *testing.T) {
	sections := []TODOSection{
		{Key: "not-a-field-name", Title: "Decision", BodyMarkdown: "keep this exact  \nbody", Position: 0, Ownership: "contract"},
		{Key: "record", Title: "Approval", BodyMarkdown: "approved", Position: 1, Ownership: "contract"},
	}
	legacy, err := preserveLegacySections(strings.Repeat("a", 40), sections)
	if err != nil || len(legacy) != 2 || legacy[0].Title != sections[0].Title || legacy[0].Body != sections[0].BodyMarkdown || legacy[0].Ownership != "contract" || legacy[0].Digest != todoBodyDigest(sections[0].BodyMarkdown) || legacy[1].SectionPosition != 1 {
		t.Fatalf("preserveLegacySections = %#v, %v", legacy, err)
	}
	sections[1].Ownership = "record"
	if _, err := preserveLegacySections(strings.Repeat("a", 40), sections); err == nil {
		t.Fatal("migration accepted a record occurrence in legacy_context")
	}
	sections[1].Ownership = "contract"
	sections[1].Position = 3
	if _, err := preserveLegacySections(strings.Repeat("a", 40), sections); err == nil {
		t.Fatal("migration accepted a non-unique source position")
	}
}
