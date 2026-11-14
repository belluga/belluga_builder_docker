package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

const todoContentSchemaVersion = 2

var localCheckIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

// TODOContent is the bounded, typed authored contract for schema-v2 TODOs.
// Operational state and historical verification remain on their existing APIs.
type TODOContent struct {
	SchemaVersion       int                       `json:"schema_version,omitempty" bson:"schema_version,omitempty"`
	Delivery            *TODODeliveryContract     `json:"delivery,omitempty" bson:"delivery,omitempty"`
	Context             string                    `json:"context,omitempty" bson:"context,omitempty"`
	Scope               *TODOContentScope         `json:"scope,omitempty" bson:"scope,omitempty"`
	Criteria            []TODOContentItem         `json:"criteria,omitempty" bson:"criteria,omitempty"`
	ValidationScenarios []TODOValidationScenario  `json:"validation_scenarios,omitempty" bson:"validation_scenarios,omitempty"`
	ImplementationPlan  []TODOContentItem         `json:"implementation_plan,omitempty" bson:"implementation_plan,omitempty"`
	Decisions           []TODOContentItem         `json:"decisions,omitempty" bson:"decisions,omitempty"`
	Risks               []TODOContentRisk         `json:"risks,omitempty" bson:"risks,omitempty"`
	References          []TODOContentReference    `json:"references,omitempty" bson:"references,omitempty"`
	LegacyContext       []TODOLegacyContext       `json:"legacy_context,omitempty" bson:"legacy_context,omitempty"`
	Architecture        *TODOArchitectureDecision `json:"architecture,omitempty" bson:"architecture,omitempty"`
	Planning            *TODOPlanning             `json:"planning,omitempty" bson:"planning,omitempty"`
	LocalChecks         []TODOLocalCheck          `json:"local_checks,omitempty" bson:"local_checks,omitempty"`
}

// TODOLocalCheck is a Project-authored, repository-scoped command contract.
// The registry validates its shape but never executes the command.
type TODOLocalCheck struct {
	ID               string   `json:"id" bson:"id"`
	RepositoryID     string   `json:"repository_id" bson:"repository_id"`
	Argv             []string `json:"argv" bson:"argv"`
	WorkingDirectory string   `json:"working_directory" bson:"working_directory"`
	InputPaths       []string `json:"input_paths" bson:"input_paths"`
}

// TODODeliveryContract identifies documentary work explicitly. An empty code
// diff never selects this mode implicitly.
type TODODeliveryContract struct {
	Kind    string                `json:"kind" bson:"kind"`
	Targets []TODOKnowledgeTarget `json:"targets" bson:"targets"`
}

type TODOKnowledgeTarget struct {
	Family           CanonicalFamily `json:"family" bson:"family"`
	RecordID         string          `json:"record_id" bson:"record_id"`
	GenerationID     string          `json:"generation_id" bson:"generation_id"`
	BaselineRevision int64           `json:"baseline_revision" bson:"baseline_revision"`
	BaselineDigest   string          `json:"baseline_digest,omitempty" bson:"baseline_digest,omitempty"`
	ChangeKind       string          `json:"change_kind" bson:"change_kind"`
}

// Planning records declared inputs and human review references; it does not
// assert that the referenced review is correct or independently verified.
type TODOPlanning struct {
	Complexity    string                   `json:"complexity" bson:"complexity"`
	AuditTriggers *TODOAuditTriggers       `json:"audit_triggers,omitempty" bson:"audit_triggers,omitempty"`
	Impact        TODOPlanningImpact       `json:"impact" bson:"impact"`
	Assumptions   []TODOPlanningAssumption `json:"assumptions" bson:"assumptions"`
	Reviews       []TODOPlanningReview     `json:"reviews" bson:"reviews"`
}

// Pointers distinguish an explicit negative declaration from an absent input.
type TODOAuditTriggers struct {
	BlastRadius                 string `json:"blast_radius" bson:"blast_radius"`
	ArchitectureApplicability   string `json:"architecture_applicability" bson:"architecture_applicability"`
	BehavioralChangeOrBugfix    *bool  `json:"behavioral_change_or_bugfix,omitempty" bson:"behavioral_change_or_bugfix,omitempty"`
	ChangesPublicContract       *bool  `json:"changes_public_contract,omitempty" bson:"changes_public_contract,omitempty"`
	TouchesAuthOrTenant         *bool  `json:"touches_auth_or_tenant,omitempty" bson:"touches_auth_or_tenant,omitempty"`
	TouchesRuntimeOrInfra       *bool  `json:"touches_runtime_or_infra,omitempty" bson:"touches_runtime_or_infra,omitempty"`
	TouchesTests                *bool  `json:"touches_tests,omitempty" bson:"touches_tests,omitempty"`
	CriticalUserJourney         *bool  `json:"critical_user_journey,omitempty" bson:"critical_user_journey,omitempty"`
	ReleaseOrPromotionCritical  *bool  `json:"release_or_promotion_critical,omitempty" bson:"release_or_promotion_critical,omitempty"`
	HighSeverityPlanReviewIssue *bool  `json:"high_severity_plan_review_issue,omitempty" bson:"high_severity_plan_review_issue,omitempty"`
	ExplicitThreeLaneRequest    *bool  `json:"explicit_three_lane_request,omitempty" bson:"explicit_three_lane_request,omitempty"`
}

type TODOPlanningImpact struct {
	CrossModule      bool `json:"cross_module" bson:"cross_module"`
	PublicContract   bool `json:"public_contract" bson:"public_contract"`
	RuntimeSensitive bool `json:"runtime_sensitive" bson:"runtime_sensitive"`
	HighSeverity     bool `json:"high_severity" bson:"high_severity"`
}

type TODOPlanningAssumption struct {
	ID        string                `json:"id" bson:"id"`
	Status    string                `json:"status" bson:"status"`
	Statement string                `json:"statement" bson:"statement"`
	CodeRefs  []TODOPlanningCodeRef `json:"code_refs" bson:"code_refs"`
}

type TODOPlanningCodeRef struct {
	RepositoryID string `json:"repository_id" bson:"repository_id"`
	Path         string `json:"path" bson:"path"`
}

type TODOPlanningReview struct {
	Kind      string `json:"kind" bson:"kind"`
	Reference string `json:"reference" bson:"reference"`
}

type TODOContentScope struct {
	Included []string `json:"included,omitempty" bson:"included"`
	Excluded []string `json:"excluded,omitempty" bson:"excluded"`
}

type TODOContentItem struct {
	ID   string `json:"id" bson:"id"`
	Text string `json:"text" bson:"text"`
}

type TODOValidationScenario struct {
	ID           string   `json:"id" bson:"id"`
	CriterionIDs []string `json:"criterion_ids" bson:"criterion_ids"`
	Method       string   `json:"method" bson:"method"`
	Expected     string   `json:"expected" bson:"expected"`
}

type TODOContentRisk struct {
	ID         string `json:"id" bson:"id"`
	Text       string `json:"text" bson:"text"`
	Mitigation string `json:"mitigation" bson:"mitigation"`
}

type TODOContentReference struct {
	Kind string `json:"kind" bson:"kind"`
	Ref  string `json:"ref" bson:"ref"`
}

type TODOLegacyContext struct {
	SourceRevision  string `json:"source_revision" bson:"source_revision"`
	SectionPosition int    `json:"section_position" bson:"section_position"`
	Ownership       string `json:"ownership" bson:"ownership"`
	Title           string `json:"title" bson:"title"`
	Body            string `json:"body" bson:"body"`
	Digest          string `json:"digest" bson:"digest"`
}

type TODOArchitectureDecision struct {
	Decision   string `json:"decision" bson:"decision"`
	Rationale  string `json:"rationale" bson:"rationale"`
	Protection string `json:"protection" bson:"protection"`
}

// ValidateTODOContent returns field-addressed TEACH errors without parsing
// Markdown or inferring contract fields from headings.
func ValidateTODOContent(content TODOContent, allowLegacyContext bool) error {
	return validateTODOContent(content, allowLegacyContext, true)
}

// ValidateTODOContentDraft validates supplied values while allowing a Planning
// draft to omit fields that become mandatory at implementation approval.
func ValidateTODOContentDraft(content TODOContent) error {
	return validateTODOContent(content, false, false)
}

func validateTODOContent(content TODOContent, allowLegacyContext, requireComplete bool) error {
	if content.SchemaVersion != todoContentSchemaVersion {
		return todoTeach("content.schema_version", "equal 2", ErrInvalid)
	}
	if err := validateTODODeliveryContract(content.Delivery); err != nil {
		return err
	}
	if err := validateTODOLocalChecks(content.LocalChecks); err != nil {
		return err
	}
	if (requireComplete && !validSummaryField(content.Context, 16<<10)) || (!requireComplete && content.Context != "" && !validSummaryField(content.Context, 16<<10)) {
		return todoTeach("content.context", "contain nonblank valid UTF-8 text up to 16384 bytes", ErrInvalid)
	}
	if content.Scope == nil && requireComplete || content.Scope != nil && (requireComplete && (content.Scope.Included == nil || content.Scope.Excluded == nil || len(content.Scope.Included) == 0) || len(content.Scope.Included) > 64 || len(content.Scope.Excluded) > 64) {
		return todoTeach("content.scope", "provide non-null included/excluded arrays, 1..64 included entries, and at most 64 excluded entries", ErrInvalid)
	}
	if content.Scope != nil {
		if err := validateTODOContentTextList("content.scope.included", content.Scope.Included); err != nil {
			return err
		}
		if err := validateTODOContentTextList("content.scope.excluded", content.Scope.Excluded); err != nil {
			return err
		}
	}
	if err := validateTODOContentItems("content.criteria", content.Criteria, boolMinimum(requireComplete), 64); err != nil {
		return err
	}
	criteria := make(map[string]bool, len(content.Criteria))
	for _, criterion := range content.Criteria {
		criteria[criterion.ID] = true
	}
	if (requireComplete && (content.ValidationScenarios == nil || len(content.ValidationScenarios) == 0)) || len(content.ValidationScenarios) > 64 {
		return todoTeach("content.validation_scenarios", "provide 1..64 scenarios", ErrInvalid)
	}
	seenScenarios := make(map[string]bool, len(content.ValidationScenarios))
	for i, scenario := range content.ValidationScenarios {
		field := fmt.Sprintf("content.validation_scenarios[%d]", i)
		if !validID(scenario.ID) || seenScenarios[scenario.ID] {
			return todoTeach(field+".id", "be a unique canonical ID within validation_scenarios", ErrInvalid)
		}
		seenScenarios[scenario.ID] = true
		if scenario.CriterionIDs == nil || len(scenario.CriterionIDs) == 0 {
			return todoTeach(field+".criterion_ids", "reference at least one criterion", ErrInvalid)
		}
		for j, id := range scenario.CriterionIDs {
			if !criteria[id] {
				return todoTeach(fmt.Sprintf("%s.criterion_ids[%d]", field, j), "reference an existing criterion ID", ErrInvalid)
			}
		}
		for _, item := range []struct{ name, value string }{{"method", scenario.Method}, {"expected", scenario.Expected}} {
			if !validSummaryField(item.value, 2048) {
				return todoTeach(field+"."+item.name, "contain nonblank valid UTF-8 text up to 2048 bytes", ErrInvalid)
			}
		}
	}
	if err := validateTODOContentItems("content.implementation_plan", content.ImplementationPlan, boolMinimum(requireComplete), 64); err != nil {
		return err
	}
	if requireComplete && content.Decisions == nil {
		return todoTeach("content.decisions", "be a non-null array with at most 32 entries", ErrInvalid)
	}
	if err := validateTODOContentItems("content.decisions", content.Decisions, 0, 32); err != nil {
		return err
	}
	if (requireComplete && content.Risks == nil) || len(content.Risks) > 32 {
		return todoTeach("content.risks", "be a non-null array with at most 32 entries", ErrInvalid)
	}
	seenRisks := make(map[string]bool, len(content.Risks))
	for i, risk := range content.Risks {
		field := fmt.Sprintf("content.risks[%d]", i)
		if !validID(risk.ID) || seenRisks[risk.ID] {
			return todoTeach(field+".id", "be a unique canonical ID within risks", ErrInvalid)
		}
		seenRisks[risk.ID] = true
		for _, item := range []struct{ name, value string }{{"text", risk.Text}, {"mitigation", risk.Mitigation}} {
			if !validSummaryField(item.value, 2048) {
				return todoTeach(field+"."+item.name, "contain nonblank valid UTF-8 text up to 2048 bytes", ErrInvalid)
			}
		}
	}
	if (requireComplete && content.References == nil) || len(content.References) > 64 {
		return todoTeach("content.references", "be a non-null array with at most 64 entries", ErrInvalid)
	}
	for i, ref := range content.References {
		if !validSummaryField(ref.Kind, 2048) || !validSummaryField(ref.Ref, 2048) {
			return todoTeach(fmt.Sprintf("content.references[%d]", i), "contain nonblank kind and ref text up to 2048 bytes", ErrInvalid)
		}
	}
	if content.LegacyContext != nil {
		if !allowLegacyContext {
			return todoTeach("content.legacy_context", "be omitted for new TODO creation", ErrInvalid)
		}
		if len(content.LegacyContext) > 128 {
			return todoTeach("content.legacy_context", "contain at most 128 ordered source occurrences", ErrInvalid)
		}
		seenOccurrences := make(map[string]bool, len(content.LegacyContext))
		lastPositionByRevision := make(map[string]int, len(content.LegacyContext))
		for i, entry := range content.LegacyContext {
			field := fmt.Sprintf("content.legacy_context[%d]", i)
			occurrence := fmt.Sprintf("%s/%d", entry.SourceRevision, entry.SectionPosition)
			previous, seenRevision := lastPositionByRevision[entry.SourceRevision]
			if !validSummaryField(entry.SourceRevision, 128) || entry.SectionPosition < 0 || seenOccurrences[occurrence] || seenRevision && entry.SectionPosition <= previous || entry.Ownership != "contract" || !validSummaryField(entry.Title, 2048) || !utf8.ValidString(entry.Body) || len(entry.Body) > 64<<10 || entry.Digest != todoBodyDigest(entry.Body) {
				return todoTeach(field, "identify an exact reviewed contract occurrence by source revision/position/title/body with a matching digest within bounds", ErrInvalid)
			}
			seenOccurrences[occurrence] = true
			lastPositionByRevision[entry.SourceRevision] = entry.SectionPosition
		}
	}
	if architecture := content.Architecture; architecture != nil {
		for _, item := range []struct{ name, value string }{{"decision", architecture.Decision}, {"rationale", architecture.Rationale}, {"protection", architecture.Protection}} {
			if !validSummaryField(item.value, 2048) {
				return todoTeach("content.architecture."+item.name, "contain nonblank valid UTF-8 text up to 2048 bytes when architecture is supplied", ErrInvalid)
			}
		}
	}
	if err := validateKnowledgeClaimCapacity(content); err != nil {
		return err
	}
	return nil
}

func validateTODOLocalChecks(checks []TODOLocalCheck) error {
	if len(checks) > 32 {
		return todoTeach("content.local_checks", "contain at most 32 repository-scoped checks", ErrInvalid)
	}
	seen := make(map[string]bool, len(checks))
	for i, check := range checks {
		field := fmt.Sprintf("content.local_checks[%d]", i)
		if !localCheckIDPattern.MatchString(check.ID) || seen[check.ID] {
			return todoTeach(field+".id", "be a unique bounded check ID using letters, digits, hyphens or underscores", ErrInvalid)
		}
		seen[check.ID] = true
		if !validGitRepositoryID(check.RepositoryID) {
			return todoTeach(field+".repository_id", "identify an exact owner/repository", ErrInvalid)
		}
		if len(check.Argv) == 0 || len(check.Argv) > 32 {
			return todoTeach(field+".argv", "contain 1..32 explicit argument strings", ErrInvalid)
		}
		for j, arg := range check.Argv {
			if !validSummaryField(arg, 2048) {
				return todoTeach(fmt.Sprintf("%s.argv[%d]", field, j), "contain nonblank valid UTF-8 arguments up to 2048 bytes", ErrInvalid)
			}
		}
		if !validRepositoryPattern(check.WorkingDirectory) || strings.ContainsAny(check.WorkingDirectory, "*?[]") || path.Clean(check.WorkingDirectory) != check.WorkingDirectory {
			return todoTeach(field+".working_directory", "be a canonical repository-relative working directory", ErrInvalid)
		}
		if len(check.InputPaths) == 0 || len(check.InputPaths) > 64 {
			return todoTeach(field+".input_paths", "contain 1..64 normalized repository-relative file paths or globs", ErrInvalid)
		}
		for j, input := range check.InputPaths {
			if !validRepositoryPattern(input) || path.Clean(input) != input {
				return todoTeach(fmt.Sprintf("%s.input_paths[%d]", field, j), "be a normalized repository-relative file path or glob", ErrInvalid)
			}
		}
	}
	return nil
}

func boolMinimum(required bool) int {
	if required {
		return 1
	}
	return 0
}

func todoBodyDigest(body string) string {
	digest := sha256.Sum256([]byte(body))
	return "sha256:" + hex.EncodeToString(digest[:])
}

// preserveLegacySections copies explicitly classified contract occurrences
// without interpreting their keys or titles. Record, render-only, ambiguous,
// or unsupported sections must remain on the v1 snapshot.
func preserveLegacySections(sourceRevision string, sections []TODOSection) ([]TODOLegacyContext, error) {
	if !validSummaryField(sourceRevision, 128) || len(sections) > 128 {
		return nil, todoTeach("content.legacy_context", "preserve a bounded source revision and at most 128 exact occurrences", ErrInvalid)
	}
	legacy := make([]TODOLegacyContext, 0, len(sections))
	for i, section := range sections {
		if section.Position < 0 || section.Position != i || section.Ownership != "contract" || !validSummaryField(section.Title, 2048) || !utf8.ValidString(section.BodyMarkdown) || len(section.BodyMarkdown) > 64<<10 {
			return nil, todoTeach(fmt.Sprintf("sections[%d]", i), "preserve only an exact, explicitly reviewed contract occurrence within legacy bounds", ErrConflict)
		}
		legacy = append(legacy, TODOLegacyContext{SourceRevision: sourceRevision, SectionPosition: section.Position, Ownership: section.Ownership, Title: section.Title, Body: section.BodyMarkdown, Digest: todoBodyDigest(section.BodyMarkdown)})
	}
	return legacy, nil
}

func validateTODOContentItems(field string, items []TODOContentItem, minimum, maximum int) error {
	if items == nil && minimum > 0 || len(items) < minimum || len(items) > maximum {
		return todoTeach(field, fmt.Sprintf("be a non-null array with %d..%d entries", minimum, maximum), ErrInvalid)
	}
	seen := make(map[string]bool, len(items))
	for i, item := range items {
		itemField := fmt.Sprintf("%s[%d]", field, i)
		if !validID(item.ID) || seen[item.ID] {
			return todoTeach(itemField+".id", "be a unique canonical ID within its array", ErrInvalid)
		}
		seen[item.ID] = true
		if !validSummaryField(item.Text, 2048) {
			return todoTeach(itemField+".text", "contain nonblank valid UTF-8 text up to 2048 bytes", ErrInvalid)
		}
	}
	return nil
}

func validateTODOContentTextList(field string, values []string) error {
	for i, value := range values {
		if !validSummaryField(value, 2048) {
			return todoTeach(fmt.Sprintf("%s[%d]", field, i), "contain nonblank valid UTF-8 text up to 2048 bytes", ErrInvalid)
		}
	}
	return nil
}
