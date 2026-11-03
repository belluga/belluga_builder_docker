package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"unicode/utf8"
)

const todoContentSchemaVersion = 2

// TODOContent is the bounded, typed authored contract for schema-v2 TODOs.
// Operational state and historical verification remain on their existing APIs.
type TODOContent struct {
	SchemaVersion       int                       `json:"schema_version,omitempty" bson:"schema_version,omitempty"`
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
