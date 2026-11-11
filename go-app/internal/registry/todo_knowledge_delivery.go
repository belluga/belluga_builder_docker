package registry

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// validateTODODeliveryContract checks only caller-authored shape. A transition
// must still verify every target against immutable Knowledge versions/events.
func validateTODODeliveryContract(delivery *TODODeliveryContract) error {
	if delivery == nil {
		return nil
	}
	if delivery.Kind != "knowledge-only" || len(delivery.Targets) == 0 {
		return todoTeach("content.delivery", "name knowledge-only and at least one explicit Knowledge target", ErrInvalid)
	}
	seen := make(map[string]bool, len(delivery.Targets))
	for i, target := range delivery.Targets {
		field := fmt.Sprintf("content.delivery.targets[%d]", i)
		if _, err := canonicalCollection(target.Family); err != nil || !canonicalID.MatchString(target.RecordID) || !canonicalID.MatchString(target.GenerationID) {
			return todoTeach(field, "name a valid Knowledge family, record_id and generation_id", ErrInvalid)
		}
		key := string(target.Family) + "/" + target.RecordID
		if seen[key] {
			return todoTeach(field, "identify each family/record_id once", ErrInvalid)
		}
		seen[key] = true
		if target.BaselineRevision < 0 || target.BaselineRevision == 0 && target.BaselineDigest != "" || target.BaselineRevision > 0 && !validSourceDigest(target.BaselineDigest) {
			return todoTeach(field+".baseline_revision/baseline_digest", "use revision 0 without digest for creation, or a positive revision with exact SHA-256 digest", ErrInvalid)
		}
		if target.ChangeKind != "create" && target.ChangeKind != "edit" && target.ChangeKind != "restore" && target.ChangeKind != "delete" || target.ChangeKind == "create" && target.BaselineRevision != 0 || target.ChangeKind != "create" && target.BaselineRevision == 0 {
			return todoTeach(field+".change_kind", "use create with absent baseline, or edit/restore/delete with an existing baseline", ErrInvalid)
		}
	}
	// The Local and retrospective transitions send every target in one bounded
	// request. Check the fully serialized worst-case shape before approval.
	claim := TODOKnowledgeDeliveryClaim{ContentDigest: "sha256:" + sixtyFourHex(), OwnerAttributionReference: strings.Repeat("x", 2048), Items: make([]TODOKnowledgeDeliveryItem, len(delivery.Targets))}
	for i, target := range delivery.Targets {
		claim.Items[i] = TODOKnowledgeDeliveryItem{Family: target.Family, RecordID: target.RecordID, GenerationID: target.GenerationID, ToRevision: 9223372036854775807, ToDigest: "sha256:" + sixtyFourHex()}
	}
	encoded, err := json.Marshal(TODOActionRequest{ProjectID: strings.Repeat("x", 64), TODOID: strings.Repeat("x", 64), OperationID: strings.Repeat("x", 128), RequestedStage: "Local-Implemented", KnowledgeDelivery: &claim})
	if err != nil || len(encoded) > int(maxRequestBody) {
		return todoTeach("content.delivery.targets", "fit one fully serialized Knowledge claim within the existing 128 KiB TODO transition request limit; reduce the target set", ErrInvalid)
	}
	return nil
}

func validateKnowledgeClaimCapacity(content TODOContent) error {
	if content.Delivery == nil {
		return nil
	}
	claim := TODOKnowledgeDeliveryClaim{ContentDigest: "sha256:" + sixtyFourHex(), OwnerAttributionReference: strings.Repeat("x", 2048), Retrospective: true, Items: make([]TODOKnowledgeDeliveryItem, len(content.Delivery.Targets))}
	criteria, scenarios := make([]string, 0, len(content.Criteria)), make([]string, 0, len(content.ValidationScenarios))
	for _, item := range content.Criteria {
		criteria = append(criteria, item.ID)
	}
	for _, item := range content.ValidationScenarios {
		scenarios = append(scenarios, item.ID)
	}
	shortest := func(values []string) []string {
		if len(values) == 0 {
			return nil
		}
		best := values[0]
		bestJSON, _ := json.Marshal(best)
		for _, value := range values[1:] {
			encoded, _ := json.Marshal(value)
			if len(encoded) < len(bestJSON) {
				best, bestJSON = value, encoded
			}
		}
		return []string{best}
	}
	minimalCriteria, minimalScenarios := shortest(criteria), shortest(scenarios)
	for i, target := range content.Delivery.Targets {
		criterionIDs, scenarioIDs := minimalCriteria, minimalScenarios
		if i == 0 {
			criterionIDs, scenarioIDs = criteria, scenarios
		}
		claim.Items[i] = TODOKnowledgeDeliveryItem{Family: target.Family, RecordID: target.RecordID, GenerationID: target.GenerationID, ToRevision: 9223372036854775807, ToDigest: "sha256:" + sixtyFourHex(), CriterionIDs: criterionIDs, ScenarioIDs: scenarioIDs}
	}
	revision := int64(9223372036854775807)
	encoded, err := json.Marshal(TODOActionRequest{ProjectID: strings.Repeat("x", 64), TODOID: strings.Repeat("x", 64), ExpectedRevision: &revision, OperationID: strings.Repeat("x", 128), RequestedStage: "Local-Implemented", ProofReferences: []TODOProofReference{}, WaiverReferences: []TODOProofReference{}, KnowledgeDelivery: &claim})
	if err != nil || len(encoded) > int(maxRequestBody) {
		return todoTeach("content.delivery.targets", "fit the fully serialized single Knowledge claim, including required criterion/scenario IDs, within the existing 128 KiB transition bound before approval; reduce targets or scope", ErrInvalid)
	}
	return nil
}

func sixtyFourHex() string { return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }

type TODOKnowledgeDeliveryClaim struct {
	ContentDigest             string                      `json:"content_digest"`
	Items                     []TODOKnowledgeDeliveryItem `json:"items"`
	OwnerAttributionReference string                      `json:"owner_attribution_reference"`
	Retrospective             bool                        `json:"retrospective,omitempty"`
}

func todoKnowledgeContractSchema() bson.D {
	text := func(max int) bson.D {
		return bson.D{{Key: "bsonType", Value: "string"}, {Key: "maxLength", Value: max}}
	}
	digest := bson.D{{Key: "bsonType", Value: "string"}, {Key: "pattern", Value: "^sha256:[a-f0-9]{64}$"}}
	target := bson.D{{Key: "bsonType", Value: "object"}, {Key: "additionalProperties", Value: false}, {Key: "required", Value: bson.A{"family", "record_id", "generation_id", "baseline_revision", "change_kind"}}, {Key: "properties", Value: bson.D{
		{Key: "family", Value: text(32)}, {Key: "record_id", Value: text(128)}, {Key: "generation_id", Value: text(128)},
		{Key: "baseline_revision", Value: bson.D{{Key: "bsonType", Value: "long"}, {Key: "minimum", Value: 0}}},
		{Key: "baseline_digest", Value: digest}, {Key: "change_kind", Value: bson.D{{Key: "enum", Value: bson.A{"create", "edit", "restore", "delete"}}}},
	}}}
	return bson.D{{Key: "bsonType", Value: "object"}, {Key: "additionalProperties", Value: false}, {Key: "required", Value: bson.A{"kind", "targets"}}, {Key: "properties", Value: bson.D{
		{Key: "kind", Value: bson.D{{Key: "enum", Value: bson.A{"knowledge-only"}}}},
		{Key: "targets", Value: bson.D{{Key: "bsonType", Value: "array"}, {Key: "minItems", Value: 1}, {Key: "items", Value: target}}},
	}}}
}

func todoKnowledgeProofSchema() bson.D {
	// The Go verifier owns these values. The collection validator keeps the
	// persisted aggregate/history typed while Go checks cross-record facts.
	text := func() bson.D { return bson.D{{Key: "bsonType", Value: "string"}} }
	item := bson.D{{Key: "bsonType", Value: "object"}, {Key: "additionalProperties", Value: false}, {Key: "required", Value: bson.A{"family", "record_id", "generation_id", "baseline_revision", "change_kind", "to_revision", "to_digest", "to_deleted", "event_at", "criterion_ids", "scenario_ids"}}, {Key: "properties", Value: bson.D{
		{Key: "family", Value: text()}, {Key: "record_id", Value: text()}, {Key: "generation_id", Value: text()},
		{Key: "baseline_revision", Value: bson.D{{Key: "bsonType", Value: "long"}}}, {Key: "baseline_digest", Value: text()}, {Key: "change_kind", Value: text()},
		{Key: "to_revision", Value: bson.D{{Key: "bsonType", Value: "long"}}}, {Key: "to_digest", Value: text()}, {Key: "to_deleted", Value: bson.D{{Key: "bsonType", Value: "bool"}}},
		{Key: "event_at", Value: bson.D{{Key: "bsonType", Value: "date"}}}, {Key: "criterion_ids", Value: bson.D{{Key: "bsonType", Value: "array"}, {Key: "items", Value: text()}}},
		{Key: "scenario_ids", Value: bson.D{{Key: "bsonType", Value: "array"}, {Key: "items", Value: text()}}},
	}}}
	return bson.D{{Key: "bsonType", Value: "object"}, {Key: "additionalProperties", Value: false}, {Key: "required", Value: bson.A{"content_digest", "items", "set_digest", "owner_attribution_reference", "verified_at", "retrospective"}}, {Key: "properties", Value: bson.D{
		{Key: "content_digest", Value: text()}, {Key: "items", Value: bson.D{{Key: "bsonType", Value: "array"}, {Key: "items", Value: item}}},
		{Key: "set_digest", Value: text()}, {Key: "owner_attribution_reference", Value: text()},
		{Key: "verified_at", Value: bson.D{{Key: "bsonType", Value: "date"}}}, {Key: "retrospective", Value: bson.D{{Key: "bsonType", Value: "bool"}}},
	}}}
}

type TODOKnowledgeDeliveryItem struct {
	Family       CanonicalFamily `json:"family"`
	RecordID     string          `json:"record_id"`
	GenerationID string          `json:"generation_id"`
	ToRevision   int64           `json:"to_revision"`
	ToDigest     string          `json:"to_digest"`
	CriterionIDs []string        `json:"criterion_ids"`
	ScenarioIDs  []string        `json:"scenario_ids"`
}

type TODOKnowledgeFinalAcceptance struct {
	ContentDigest  string `json:"content_digest"`
	SetDigest      string `json:"set_digest"`
	OwnerReference string `json:"owner_reference"`
	Reason         string `json:"reason"`
}

type TODOKnowledgeDeliveryProof struct {
	ContentDigest             string                      `json:"content_digest" bson:"content_digest"`
	Items                     []TODOKnowledgeVerifiedItem `json:"items" bson:"items"`
	SetDigest                 string                      `json:"set_digest" bson:"set_digest"`
	OwnerAttributionReference string                      `json:"owner_attribution_reference" bson:"owner_attribution_reference"`
	VerifiedAt                time.Time                   `json:"verified_at" bson:"verified_at"`
	Retrospective             bool                        `json:"retrospective" bson:"retrospective"`
}

type TODOKnowledgeVerifiedItem struct {
	TODOKnowledgeTarget `bson:",inline"`
	ToRevision          int64     `json:"to_revision" bson:"to_revision"`
	ToDigest            string    `json:"to_digest" bson:"to_digest"`
	ToDeleted           bool      `json:"to_deleted" bson:"to_deleted"`
	EventAt             time.Time `json:"event_at" bson:"event_at"`
	CriterionIDs        []string  `json:"criterion_ids" bson:"criterion_ids"`
	ScenarioIDs         []string  `json:"scenario_ids" bson:"scenario_ids"`
}

func knowledgeClaimDigest(items []TODOKnowledgeVerifiedItem) string {
	h := sha256.New()
	_, _ = h.Write([]byte("builder-knowledge-delivery-v1\x00"))
	write := func(s string) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(s)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(s))
	}
	write(fmt.Sprint(len(items)))
	for _, item := range items {
		for _, value := range []string{string(item.Family), item.RecordID, item.GenerationID, fmt.Sprint(item.BaselineRevision), item.BaselineDigest, item.ChangeKind, fmt.Sprint(item.ToRevision), item.ToDigest, fmt.Sprint(item.ToDeleted), item.EventAt.UTC().Format(time.RFC3339Nano)} {
			write(value)
		}
		write("criterion_ids")
		write(fmt.Sprint(len(item.CriterionIDs)))
		for _, id := range item.CriterionIDs {
			write(id)
		}
		write("scenario_ids")
		write(fmt.Sprint(len(item.ScenarioIDs)))
		for _, id := range item.ScenarioIDs {
			write(id)
		}
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

type knowledgeProofFacts struct {
	versions map[string]canonicalStored
	events   map[string]CanonicalEvent
}

func knowledgeProofKey(family CanonicalFamily, recordID string, revision int64) string {
	return string(family) + "\x00" + recordID + "\x00" + fmt.Sprint(revision)
}

// Fetch only the exact immutable facts named by the bounded claim. Canonical
// writes advance a record by one revision and insert its version and event in
// the same Mongo transaction; proof does not replay an unbounded history.
func (m *MongoStore) knowledgeFactsForClaim(ctx context.Context, db *mongo.Database, projectID, generationID string, claim *TODOKnowledgeDeliveryClaim, targets map[string]TODOKnowledgeTarget) (knowledgeProofFacts, error) {
	facts := knowledgeProofFacts{versions: map[string]canonicalStored{}, events: map[string]CanonicalEvent{}}
	versionClauses, eventClauses := bson.A{}, bson.A{}
	versionSeen := map[string]bool{}
	for _, item := range claim.Items {
		target := targets[string(item.Family)+"/"+item.RecordID]
		for _, revision := range []int64{target.BaselineRevision, item.ToRevision - 1, item.ToRevision} {
			if revision < 1 {
				continue
			}
			key := knowledgeProofKey(item.Family, item.RecordID, revision)
			if !versionSeen[key] {
				versionSeen[key] = true
				versionClauses = append(versionClauses, bson.D{{Key: "family", Value: item.Family}, {Key: "record_id", Value: item.RecordID}, {Key: "revision", Value: revision}})
			}
		}
		eventClauses = append(eventClauses, bson.D{{Key: "family", Value: item.Family}, {Key: "record_id", Value: item.RecordID}, {Key: "revision", Value: item.ToRevision}})
	}
	base := bson.D{{Key: "project_id", Value: projectID}, {Key: "generation_id", Value: generationID}}
	versionFilter := append(append(bson.D{}, base...), bson.E{Key: "$or", Value: versionClauses})
	versionCursor, err := db.Collection(canonicalVersions).Find(ctx, versionFilter, options.Find().SetHint("canonical_versions_lookup").SetProjection(bson.D{{Key: "family", Value: 1}, {Key: "record_id", Value: 1}, {Key: "revision", Value: 1}, {Key: "record.content_digest", Value: 1}, {Key: "record.deleted", Value: 1}, {Key: "record.updated_at", Value: 1}}))
	if err != nil {
		return facts, ErrUnavailable
	}
	for versionCursor.Next(ctx) {
		var version struct {
			Family   CanonicalFamily `bson:"family"`
			RecordID string          `bson:"record_id"`
			Revision int64           `bson:"revision"`
			Record   canonicalStored `bson:"record"`
		}
		if versionCursor.Decode(&version) != nil {
			_ = versionCursor.Close(ctx)
			return facts, ErrUnavailable
		}
		facts.versions[knowledgeProofKey(version.Family, version.RecordID, version.Revision)] = version.Record
	}
	err = versionCursor.Err()
	_ = versionCursor.Close(ctx)
	if err != nil {
		return facts, ErrUnavailable
	}
	eventFilter := append(append(bson.D{}, base...), bson.E{Key: "$or", Value: eventClauses})
	eventCursor, err := db.Collection(canonicalEvents).Find(ctx, eventFilter, options.Find().SetHint("canonical_events_record_revision").SetProjection(bson.D{{Key: "family", Value: 1}, {Key: "record_id", Value: 1}, {Key: "revision", Value: 1}, {Key: "kind", Value: 1}, {Key: "occurred_at", Value: 1}}))
	if err != nil {
		return facts, ErrUnavailable
	}
	for eventCursor.Next(ctx) {
		var event CanonicalEvent
		if eventCursor.Decode(&event) != nil {
			_ = eventCursor.Close(ctx)
			return facts, ErrUnavailable
		}
		facts.events[knowledgeProofKey(event.Family, event.RecordID, event.Revision)] = event
	}
	err = eventCursor.Err()
	_ = eventCursor.Close(ctx)
	if err != nil {
		return facts, ErrUnavailable
	}
	return facts, nil
}

func knowledgeTemporalOffender(ctx context.Context, db *mongo.Database, projectID, generationID string, claim *TODOKnowledgeDeliveryClaim, targets map[string]TODOKnowledgeTarget, retrospective bool, approvalAt, workStartedAt time.Time) (bool, error) {
	clauses := make(bson.A, 0, len(claim.Items))
	for _, item := range claim.Items {
		target := targets[string(item.Family)+"/"+item.RecordID]
		boundary := approvalAt
		comparison := "$gte"
		if !retrospective {
			boundary = workStartedAt
			comparison = "$lte"
		}
		clauses = append(clauses, bson.D{{Key: "family", Value: item.Family}, {Key: "record_id", Value: item.RecordID}, {Key: "revision", Value: bson.D{{Key: "$gt", Value: target.BaselineRevision}, {Key: "$lte", Value: item.ToRevision}}}, {Key: "occurred_at", Value: bson.D{{Key: comparison, Value: boundary}}}})
	}
	filter := bson.D{{Key: "project_id", Value: projectID}, {Key: "generation_id", Value: generationID}, {Key: "$or", Value: clauses}}
	var event CanonicalEvent
	err := db.Collection(canonicalEvents).FindOne(ctx, filter, options.FindOne().SetHint("canonical_events_record_revision").SetProjection(bson.D{{Key: "_id", Value: 1}})).Decode(&event)
	if err == mongo.ErrNoDocuments {
		return false, nil
	}
	if err != nil {
		return false, ErrUnavailable
	}
	return true, nil
}

// Existing tenant databases may predate the record/revision index. Provision
// it once before any hinted Knowledge proof query; new tenants receive the
// same index through ensureCanonicalCollections.
func (m *MongoStore) ensureKnowledgeProofIndex(ctx context.Context, databaseName string) error {
	if _, ready := m.knowledgeProofIndex.Load(databaseName); ready {
		return nil
	}
	keys := bson.D{{Key: "project_id", Value: 1}, {Key: "generation_id", Value: 1}, {Key: "family", Value: 1}, {Key: "record_id", Value: 1}, {Key: "revision", Value: 1}}
	if _, err := m.client.Database(databaseName).Collection(canonicalEvents).Indexes().CreateOne(ctx, mongo.IndexModel{Keys: keys, Options: options.Index().SetName("canonical_events_record_revision")}); err != nil {
		return todoTeach("canonical_events index", "provision the record/revision Knowledge proof index for this Company before documentary transitions", ErrUnavailable)
	}
	m.knowledgeProofIndex.Store(databaseName, true)
	return nil
}

// A Ready approval must not freeze a nonexistent or foreign Knowledge baseline.
// A newer head is allowed here only for the explicit retrospective route at
// the following edge; prospective Local proof still requires head==baseline
// at the actual approval checkpoint.
func (m *MongoStore) validateKnowledgeApprovalTargets(ctx context.Context, companyID string, project Project, delivery *TODODeliveryContract) error {
	if delivery == nil || project.KnowledgeGenerationID == "" {
		return todoTeach("content.delivery", "use an explicit Knowledge-only contract with an active Project Knowledge generation", ErrConflict)
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return err
	}
	db := m.client.Database(company.DatabaseName)
	for i, target := range delivery.Targets {
		field := fmt.Sprintf("content.delivery.targets[%d]", i)
		if target.GenerationID != project.KnowledgeGenerationID {
			return todoTeach(field+".generation_id", "match the current active generation of this Project before owner approval", ErrConflict)
		}
		filter := bson.D{{Key: "project_id", Value: project.ID}, {Key: "generation_id", Value: target.GenerationID}, {Key: "family", Value: target.Family}, {Key: "record_id", Value: target.RecordID}}
		var head CanonicalEvent
		headErr := db.Collection(canonicalEvents).FindOne(ctx, filter, options.FindOne().SetSort(bson.D{{Key: "revision", Value: -1}})).Decode(&head)
		if headErr != nil && headErr != mongo.ErrNoDocuments {
			return ErrUnavailable
		}
		if target.BaselineRevision == 0 {
			continue
		}
		if headErr == mongo.ErrNoDocuments || head.Revision < target.BaselineRevision {
			return todoTeach(field+".baseline_revision", "identify an existing same-Project Knowledge baseline before owner approval", ErrConflict)
		}
		baseFilter := append(append(bson.D{}, filter...), bson.E{Key: "revision", Value: target.BaselineRevision})
		var version struct {
			Record canonicalStored `bson:"record"`
		}
		if err := db.Collection(canonicalVersions).FindOne(ctx, baseFilter).Decode(&version); err != nil || version.Record.ContentDigest != target.BaselineDigest {
			return todoTeach(field+".baseline_digest", "match the exact immutable same-Project Knowledge baseline before owner approval", ErrConflict)
		}
	}
	return nil
}

func (m *MongoStore) verifyKnowledgeDelivery(ctx context.Context, companyID string, project Project, record TODORecord, digest string, claim *TODOKnowledgeDeliveryClaim, retrospective bool, approvalAt, workStartedAt time.Time) (*TODOKnowledgeDeliveryProof, error) {
	if claim == nil || claim.ContentDigest != digest || claim.Retrospective != retrospective || !validSummaryField(claim.OwnerAttributionReference, 2048) || len(claim.Items) != len(record.Content.Delivery.Targets) {
		return nil, todoTeach("knowledge_delivery", "name the current content digest, every frozen target once, and present owner attribution in the correct temporal mode", ErrConflict)
	}
	if project.KnowledgeGenerationID == "" {
		return nil, todoTeach("knowledge_delivery.generation_id", "use an active Project Knowledge generation", ErrConflict)
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return nil, err
	}
	db := m.client.Database(company.DatabaseName)
	targets := make(map[string]TODOKnowledgeTarget, len(record.Content.Delivery.Targets))
	for _, target := range record.Content.Delivery.Targets {
		if target.GenerationID != project.KnowledgeGenerationID {
			return nil, todoTeach("content.delivery.targets[].generation_id", "match the active Project Knowledge generation", ErrConflict)
		}
		targets[string(target.Family)+"/"+target.RecordID] = target
	}
	claimSeen := map[string]bool{}
	for i, item := range claim.Items {
		key := string(item.Family) + "/" + item.RecordID
		target, ok := targets[key]
		if !ok || claimSeen[key] || item.GenerationID != target.GenerationID || item.ToRevision <= target.BaselineRevision || !validSourceDigest(item.ToDigest) {
			return nil, todoTeach(fmt.Sprintf("knowledge_delivery.items[%d]", i), "match one frozen target, generation and later exact Knowledge revision/digest", ErrConflict)
		}
		claimSeen[key] = true
	}
	facts, err := m.knowledgeFactsForClaim(ctx, db, record.ProjectID, project.KnowledgeGenerationID, claim, targets)
	if err != nil {
		return nil, err
	}
	if offender, err := knowledgeTemporalOffender(ctx, db, record.ProjectID, project.KnowledgeGenerationID, claim, targets, retrospective, approvalAt, workStartedAt); err != nil {
		return nil, err
	} else if offender {
		return nil, todoTeach("knowledge_delivery.items[].to_revision", "use only Knowledge mutation events on the correct side of the verified owner approval/work-start checkpoint", ErrConflict)
	}
	criteria, scenarios := make(map[string]bool), make(map[string]bool)
	for _, item := range record.Content.Criteria {
		criteria[item.ID] = false
	}
	for _, item := range record.Content.ValidationScenarios {
		scenarios[item.ID] = false
	}
	seen := map[string]bool{}
	verified := make([]TODOKnowledgeVerifiedItem, 0, len(claim.Items))
	for i, item := range claim.Items {
		field := fmt.Sprintf("knowledge_delivery.items[%d]", i)
		key := string(item.Family) + "/" + item.RecordID
		target, ok := targets[key]
		if !ok || seen[key] || item.GenerationID != target.GenerationID || item.ToRevision <= target.BaselineRevision || !validSourceDigest(item.ToDigest) {
			return nil, todoTeach(field, "match one frozen target, generation and later exact Knowledge revision/digest", ErrConflict)
		}
		seen[key] = true
		if len(item.CriterionIDs) == 0 || len(item.ScenarioIDs) == 0 {
			return nil, todoTeach(field+".criterion_ids/scenario_ids", "map this delivered revision to at least one existing criterion and scenario", ErrConflict)
		}
		for _, id := range item.CriterionIDs {
			if _, ok := criteria[id]; !ok {
				return nil, todoTeach(field+".criterion_ids", "reference only approved criterion IDs", ErrConflict)
			}
			criteria[id] = true
		}
		for _, id := range item.ScenarioIDs {
			if _, ok := scenarios[id]; !ok {
				return nil, todoTeach(field+".scenario_ids", "reference only approved scenario IDs", ErrConflict)
			}
			scenarios[id] = true
		}
		version, hasVersion := facts.versions[knowledgeProofKey(item.Family, item.RecordID, item.ToRevision)]
		if !hasVersion {
			return nil, todoTeach(field+".to_revision", "resolve to an immutable same-Project Knowledge version", ErrConflict)
		}
		if version.ContentDigest != item.ToDigest {
			return nil, todoTeach(field+".to_digest", "match the immutable delivered Knowledge version", ErrConflict)
		}
		event, hasEvent := facts.events[knowledgeProofKey(item.Family, item.RecordID, item.ToRevision)]
		if !hasEvent {
			return nil, todoTeach(field+".to_revision", "have an exact immutable mutation event", ErrConflict)
		}
		if !retrospective && (!event.OccurredAt.After(approvalAt) || !event.OccurredAt.After(workStartedAt)) {
			return nil, todoTeach(field+".to_revision", "postdate verified approval and work-started, or use the retrospective late-work-verification route", ErrConflict)
		}
		actualKind := map[string]string{"record_created": "create", "content_changed": "edit", "content_restored": "restore", "record_deleted": "delete"}[event.Kind]
		if target.BaselineRevision > 0 {
			baseline, hasBaseline := facts.versions[knowledgeProofKey(item.Family, item.RecordID, target.BaselineRevision)]
			if !hasBaseline || baseline.ContentDigest != target.BaselineDigest {
				return nil, todoTeach(field+".baseline_revision", "match the exact immutable approved pre-change version", ErrConflict)
			}
			if retrospective && !event.OccurredAt.After(baseline.UpdatedAt) {
				return nil, todoTeach(field+".to_revision", "follow the exact pre-change baseline event", ErrConflict)
			}
		}
		previous, hasPrevious := facts.versions[knowledgeProofKey(item.Family, item.RecordID, item.ToRevision-1)]
		if item.ToRevision > 1 && !hasPrevious || item.ToRevision == 1 && hasPrevious {
			return nil, todoTeach(field+".to_revision", "have a consistent immediately preceding as-of state", ErrConflict)
		}
		if item.ToRevision > 1 && previous.ContentDigest == version.ContentDigest && previous.Deleted == version.Deleted {
			return nil, todoTeach(field+".to_revision", "identify a material Knowledge mutation, not a no-op", ErrConflict)
		}
		if target.ChangeKind == "delete" && !version.Deleted || target.ChangeKind != "delete" && version.Deleted || actualKind != target.ChangeKind {
			return nil, todoTeach(field+".change_kind", "match the immutable mutation event and delivered deletion state", ErrConflict)
		}
		verified = append(verified, TODOKnowledgeVerifiedItem{TODOKnowledgeTarget: target, ToRevision: item.ToRevision, ToDigest: item.ToDigest, ToDeleted: version.Deleted, EventAt: event.OccurredAt, CriterionIDs: append([]string{}, item.CriterionIDs...), ScenarioIDs: append([]string{}, item.ScenarioIDs...)})
	}
	for id, ok := range criteria {
		if !ok {
			return nil, todoTeach("knowledge_delivery.items[].criterion_ids", "cover approved criterion "+id, ErrConflict)
		}
	}
	for id, ok := range scenarios {
		if !ok {
			return nil, todoTeach("knowledge_delivery.items[].scenario_ids", "cover approved scenario "+id, ErrConflict)
		}
	}
	sort.Slice(verified, func(i, j int) bool {
		a, b := verified[i], verified[j]
		return strings.Join([]string{string(a.Family), a.RecordID}, "/") < strings.Join([]string{string(b.Family), b.RecordID}, "/")
	})
	return &TODOKnowledgeDeliveryProof{ContentDigest: digest, Items: verified, SetDigest: knowledgeClaimDigest(verified), OwnerAttributionReference: claim.OwnerAttributionReference, VerifiedAt: time.Now().UTC(), Retrospective: retrospective}, nil
}

func (m *MongoStore) knowledgeWorkCheckpoint(ctx context.Context, db *mongo.Database, record TODORecord, kind string) (time.Time, error) {
	var event TODOHistoryEvent
	filter := bson.D{{Key: "project_id", Value: record.ProjectID}, {Key: "todo_id", Value: record.ID}, {Key: "kind", Value: "state_changed"}, {Key: "changes.verified_transition_ref.kind", Value: kind}, {Key: "revision", Value: bson.D{{Key: "$lte", Value: record.Revision}}}}
	if err := db.Collection(todoEventsCollection).FindOne(ctx, filter, options.FindOne().SetSort(bson.D{{Key: "revision", Value: -1}})).Decode(&event); err != nil {
		return time.Time{}, todoTeach("knowledge_delivery", "find the verified owner approval/work checkpoint before claiming Knowledge revisions", ErrConflict)
	}
	return event.OccurredAt, nil
}

func (m *MongoStore) transitionKnowledgeTODO(ctx context.Context, companyID string, project Project, collection *mongo.Collection, record TODORecord, request TODOActionRequest, requestDigest string) (TODOActionResponse, error) {
	company, err := m.company(ctx, companyID)
	if err != nil {
		return TODOActionResponse{}, err
	}
	db := m.client.Database(company.DatabaseName)
	if err := m.ensureKnowledgeProofIndex(ctx, company.DatabaseName); err != nil {
		return TODOActionResponse{}, err
	}
	_, digest, _, err := currentMongoContentSubject(ctx, db, record)
	if err != nil {
		return TODOActionResponse{}, err
	}
	if request.LocalProof != nil || len(request.WaiverReferences) != 0 {
		return TODOActionResponse{}, todoTeach("local_proof/waiver_refs", "omit code-run proof and waivers for explicit Knowledge-only delivery", ErrConflict)
	}
	if request.KnowledgeFinalAcceptance != nil && (record.Stage != "Local-Implemented" || request.RequestedStage != "Completed") || request.KnowledgeDelivery != nil && record.Stage == "Local-Implemented" && request.RequestedStage == "Completed" {
		return TODOActionResponse{}, todoTeach("knowledge_delivery/knowledge_final_acceptance", "send only the proof field required by this Knowledge-only edge", ErrConflict)
	}
	if record.Stage == "Implementation-Approval-Pending" && request.RequestedStage == "Ready-for-Implementation" {
		if err := m.validateKnowledgeApprovalTargets(ctx, companyID, project, record.Content.Delivery); err != nil {
			return TODOActionResponse{}, err
		}
	}
	if record.Stage == "Ready-for-Implementation" && request.RequestedStage == "In-Progress" && request.KnowledgeDelivery != nil {
		if len(request.ProofReferences) != 0 || record.VerifiedTransitionRef == nil || record.VerifiedTransitionRef.Kind != "owner-approval" || record.VerifiedTransitionRef.SourceDigest != digest {
			return TODOActionResponse{}, todoTeach("approval/knowledge_delivery", "start retrospective verification only from the exact approved Ready contract", ErrConflict)
		}
		approvalAt, err := m.knowledgeWorkCheckpoint(ctx, db, record, "owner-approval")
		if err != nil {
			return TODOActionResponse{}, err
		}
		proof, err := m.verifyKnowledgeDelivery(ctx, companyID, project, record, digest, request.KnowledgeDelivery, true, approvalAt, time.Time{})
		if err != nil {
			return TODOActionResponse{}, err
		}
		stageProof := TODOProofReference{Kind: "late-work-verification", Source: "mongo-knowledge-api", Reference: proof.SetDigest, SourceDigest: digest, VerificationState: "verified"}
		return writeVerifiedTODOWithKnowledge(ctx, db, collection, companyID, record, request.RequestedStage, stageProof, request.OperationID, "transition", requestDigest, proof, nil)
	}
	if record.Stage == "In-Progress" && request.RequestedStage == "Local-Implemented" {
		if err := requireApprovedCurrentContent(record, digest); err != nil {
			return TODOActionResponse{}, err
		}
		if err := validateTODOAuditDeliveryReferences(record, digest); err != nil {
			return TODOActionResponse{}, err
		}
		if len(request.ProofReferences) != 0 {
			return TODOActionResponse{}, todoTeach("proof_refs", "submit the typed Knowledge delivery claim, not code commits or deployment references", ErrConflict)
		}
		var proof *TODOKnowledgeDeliveryProof
		if record.VerifiedTransitionRef != nil && record.VerifiedTransitionRef.Kind == "late-work-verification" && record.KnowledgeDeliveryProof != nil {
			if request.KnowledgeDelivery != nil || record.KnowledgeDeliveryProof.ContentDigest != digest {
				return TODOActionResponse{}, todoTeach("knowledge_delivery", "reuse only the exact server-verified retrospective proof from Ready→In-Progress", ErrConflict)
			}
			proof = record.KnowledgeDeliveryProof
		} else {
			approvalAt, err := m.knowledgeWorkCheckpoint(ctx, db, record, "owner-approval")
			if err != nil {
				return TODOActionResponse{}, err
			}
			workStartedAt, err := m.knowledgeWorkCheckpoint(ctx, db, record, "work-started")
			if err != nil {
				return TODOActionResponse{}, err
			}
			proof, err = m.verifyKnowledgeDelivery(ctx, companyID, project, record, digest, request.KnowledgeDelivery, false, approvalAt, workStartedAt)
			if err != nil {
				return TODOActionResponse{}, err
			}
		}
		stageProof := TODOProofReference{Kind: "knowledge-delivery", Source: "mongo-knowledge-api", Reference: proof.SetDigest, SourceDigest: digest, VerificationState: "verified"}
		return writeVerifiedTODOWithKnowledge(ctx, db, collection, companyID, record, request.RequestedStage, stageProof, request.OperationID, "transition", requestDigest, proof, nil)
	}
	if record.Stage == "Local-Implemented" && request.RequestedStage == "Completed" {
		acceptance := request.KnowledgeFinalAcceptance
		proof := record.KnowledgeDeliveryProof
		if err := validateTODOAuditDeliveryReferences(record, digest); err != nil {
			return TODOActionResponse{}, err
		}
		if record.StageVerification != "verified" || record.VerifiedTransitionRef == nil || record.VerifiedTransitionRef.Kind != "knowledge-delivery" || proof == nil || proof.ContentDigest != digest || acceptance == nil || acceptance.ContentDigest != digest || acceptance.SetDigest != proof.SetDigest || !validSummaryField(acceptance.OwnerReference, 2048) || !validSummaryField(acceptance.Reason, 512) || len(request.ProofReferences) != 0 {
			return TODOActionResponse{}, todoTeach("knowledge_final_acceptance", "ratify the current verified Knowledge set and TODO digest with owner reference and reason; no deployment claim", ErrConflict)
		}
		var localEvent TODOHistoryEvent
		localFilter := bson.D{{Key: "project_id", Value: record.ProjectID}, {Key: "todo_id", Value: record.ID}, {Key: "revision", Value: bson.D{{Key: "$lte", Value: record.Revision}}}, {Key: "kind", Value: "state_changed"}, {Key: "changes.stage", Value: "Local-Implemented"}}
		if err := db.Collection(todoEventsCollection).FindOne(ctx, localFilter, options.FindOne().SetSort(bson.D{{Key: "revision", Value: -1}})).Decode(&localEvent); err != nil || localEvent.Changes.KnowledgeDeliveryProof == nil || !reflect.DeepEqual(*proof, *localEvent.Changes.KnowledgeDeliveryProof) {
			return TODOActionResponse{}, todoTeach("knowledge_final_acceptance.set_digest", "match the complete immutable Local-Implemented Knowledge proof event", ErrConflict)
		}
		stageProof := TODOProofReference{Kind: "knowledge-final-acceptance", Source: "owner-chat", Reference: acceptance.OwnerReference, SourceDigest: digest, VerificationState: "verified"}
		return writeVerifiedTODOWithKnowledge(ctx, db, collection, companyID, record, request.RequestedStage, stageProof, request.OperationID, "transition", requestDigest, proof, acceptance)
	}
	if request.KnowledgeDelivery != nil || request.KnowledgeFinalAcceptance != nil {
		return TODOActionResponse{}, todoTeach("knowledge_delivery/knowledge_final_acceptance", "supply these fields only on their governed Knowledge-only edges", ErrConflict)
	}
	stageProof, err := m.mongoBornTransitionProof(ctx, companyID, project, record, request)
	if err != nil {
		return TODOActionResponse{}, err
	}
	return writeVerifiedTODO(ctx, db, collection, companyID, record, request.RequestedStage, stageProof, request.OperationID, "transition", requestDigest)
}
