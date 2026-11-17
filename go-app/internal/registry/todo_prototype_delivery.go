package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const prototypeDeliveryProofVersion = "prototype-delivery-v1"
const maxPrototypeDeliveryProofBytes = 8 << 10

type TODOPrototypeDeliveryClaim struct {
	ContentDigest          string `json:"content_digest"`
	Revision               string `json:"revision"`
	InventoryDigest        string `json:"inventory_digest"`
	BindingRevision        int64  `json:"binding_revision"`
	bindingRevisionPresent bool   `json:"-"`
}

func (claim *TODOPrototypeDeliveryClaim) UnmarshalJSON(data []byte) error {
	type claimAlias TODOPrototypeDeliveryClaim
	decoder := json.NewDecoder(bytes.NewReader(data))
	var decoded claimAlias
	if err := decoder.Decode(&decoded); err != nil {
		return todoTeach("prototype_delivery", "provide one typed observation tuple with only the approved fields", ErrInvalid)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return todoTeach("prototype_delivery", "contain exactly one observation tuple", ErrInvalid)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return todoTeach("prototype_delivery", "provide a typed observation tuple object", ErrInvalid)
	}
	for key := range fields {
		switch key {
		case "content_digest", "revision", "inventory_digest", "binding_revision":
		default:
			return todoTeach("prototype_delivery."+key, "omit unknown observation tuple fields", ErrInvalid)
		}
	}
	bindingRevisionRaw, present := fields["binding_revision"]
	if !present || bytes.Equal(bytes.TrimSpace(bindingRevisionRaw), []byte("null")) {
		return todoTeach("prototype_delivery.binding_revision", "provide an explicit nonnegative integer, including zero when that is the observed value", ErrInvalid)
	}
	var bindingRevision int64
	if err := json.Unmarshal(bindingRevisionRaw, &bindingRevision); err != nil || bindingRevision < 0 {
		return todoTeach("prototype_delivery.binding_revision", "provide an explicit nonnegative integer, including zero when that is the observed value", ErrInvalid)
	}
	*claim = TODOPrototypeDeliveryClaim(decoded)
	_, claim.bindingRevisionPresent = fields["binding_revision"]
	return nil
}

// TODOPrototypeDeliveryProof is the server observation persisted inside the
// existing verified_transition_ref. Field order is canonical JSON order.
type TODOPrototypeDeliveryProof struct {
	Version             string    `json:"version"`
	CompanyID           string    `json:"company_id"`
	ProjectID           string    `json:"project_id"`
	TODOID              string    `json:"todo_id"`
	ContentDigest       string    `json:"content_digest"`
	PrototypeID         string    `json:"prototype_id"`
	RepositoryID        string    `json:"repository_id"`
	BindingRevision     int64     `json:"binding_revision"`
	PublicationBranch   string    `json:"publication_branch"`
	Revision            string    `json:"revision"`
	InventoryDigest     string    `json:"inventory_digest"`
	EvaluatorVersion    string    `json:"evaluator_version"`
	KnowledgeGeneration string    `json:"knowledge_generation_id,omitempty"`
	KnowledgeDigest     string    `json:"knowledge_digest"`
	VerifiedAt          time.Time `json:"verified_at"`
}

func validatePrototypeProjectBinding(project Project) error {
	binding := repositoryBindingFromProject(project)
	if binding.State != "configured" || binding.PublicationBranch != "main" || !validGitRepositoryIdentity(binding.RepositoryID) || binding.Revision < 0 {
		return todoTeach("project.repository_binding", "configure this Project's artifact repository and main publication branch before approving prototype delivery", ErrConflict)
	}
	return nil
}

func validatePrototypeNoCodeFields(content *TODOContent, record TODORecord, commits []TODOCommitReference) error {
	if content == nil || content.Delivery == nil || content.Delivery.Kind != "prototype" {
		return nil
	}
	if len(record.DiffExpectation) != 0 || len(record.ImplementationCommits) != 0 || len(commits) != 0 || len(record.AffectedFiles) != 0 || len(content.LocalChecks) != 0 {
		return todoTeach("content.delivery", "prototype delivery requires empty diff_expectation, implementation_commits, local_checks, and affected_files", ErrConflict)
	}
	return nil
}

func (m *MongoStore) transitionPrototypeTODO(ctx context.Context, companyID string, project Project, collection *mongo.Collection, record TODORecord, request TODOActionRequest, requestDigest string, contentDigest string) (TODOActionResponse, error) {
	claim := request.PrototypeDelivery
	if record.Stage != "In-Progress" || request.RequestedStage != "Completed" || claim == nil {
		return TODOActionResponse{}, todoTeach("requested_stage/prototype_delivery", "prototype delivery supports only direct In-Progress to Completed with the expected published main revision tuple", ErrConflict)
	}
	if request.localProofPresent || request.knowledgeDeliveryPresent || request.knowledgeAcceptancePresent || request.LocalProof != nil || request.KnowledgeDelivery != nil || request.KnowledgeFinalAcceptance != nil || request.proofReferencesPresent || request.waiverReferencesPresent || request.promotionTargetsPresent || len(request.ProofReferences) != 0 || len(request.WaiverReferences) != 0 || len(request.PromotionTargets) != 0 {
		return TODOActionResponse{}, todoTeach("proof_refs/waiver_refs/local_proof/promotion_targets/knowledge_delivery", "omit caller-authored proof, Knowledge and promotion fields for prototype delivery", ErrConflict)
	}
	if record.Content == nil || record.Content.Delivery == nil || record.Content.Delivery.Kind != "prototype" || !validStableArtifactID(record.Content.Delivery.PrototypeID) {
		return TODOActionResponse{}, todoTeach("content.delivery", "use an explicitly approved prototype delivery contract", ErrConflict)
	}
	if claim.ContentDigest != contentDigest || !validFullGitSHA(claim.Revision) || !validSourceDigest(claim.InventoryDigest) || !claim.bindingRevisionPresent || claim.BindingRevision < 0 {
		return TODOActionResponse{}, todoTeach("prototype_delivery", "match the current TODO digest and provide a full Git SHA, inventory digest, and nonnegative binding revision", ErrConflict)
	}
	if err := requireApprovedCurrentContent(record, contentDigest); err != nil {
		return TODOActionResponse{}, err
	}
	if err := validateExternalRequirementReferences(record.Content, record.ProofReferences, contentDigest); err != nil {
		return TODOActionResponse{}, err
	}
	if err := validateTODOAuditDeliveryReferences(record, contentDigest); err != nil {
		return TODOActionResponse{}, err
	}
	if err := validatePrototypeProjectBinding(project); err != nil {
		return TODOActionResponse{}, err
	}
	binding := repositoryBindingFromProject(project)
	if binding.Revision != claim.BindingRevision {
		return TODOActionResponse{}, todoTeach("prototype_delivery.binding_revision", "match the configured Project artifact repository, main publication branch and binding revision", ErrConflict)
	}
	catalog, err := m.prototypeSnapshot(ctx, companyID, record.ProjectID, "")
	if err != nil {
		return TODOActionResponse{}, err
	}
	if catalog.RepositoryID != binding.RepositoryID || catalog.PublicationBranch != "main" || catalog.BindingRevision != binding.Revision || catalog.Revision != claim.Revision || catalog.InventoryDigest != claim.InventoryDigest {
		return TODOActionResponse{}, todoTeach("prototype_delivery", "match the active candidate and current bound main inventory observed by the Registry", ErrConflict)
	}
	found := false
	for _, candidate := range catalog.Items {
		if candidate.ID == record.Content.Delivery.PrototypeID && candidate.Status == "active" {
			found = true
			break
		}
	}
	if !found {
		return TODOActionResponse{}, todoTeach("prototype_delivery.prototype_id", "resolve the approved prototype ID in the active main branch inventory", ErrConflict)
	}
	if catalog.KnowledgeDigest == "" || catalog.EvaluatorVersion != prototypeEvaluatorVersion {
		return TODOActionResponse{}, todoTeach("prototype_delivery.knowledge/evaluator", "resolve the current canonical Knowledge identity and supported Prototype evaluator", ErrConflict)
	}
	// Re-read the aggregate/content subject after Git observation so concurrent
	// edits cannot be recorded against the stale approval snapshot.
	current, err := readSelectedTODO(ctx, collection, record.ProjectID, record.ID)
	if err != nil || current.Revision != record.Revision {
		return TODOActionResponse{}, todoTeach("expected_revision", "retry against the unchanged TODO aggregate after Prototype observation", ErrConflict)
	}
	_, currentDigest, _, _, err := currentTODOContentSubject(ctx, collection.Database(), current)
	if err != nil || currentDigest != contentDigest {
		return TODOActionResponse{}, todoTeach("content_digest", "retain the exact approved TODO content through Prototype observation", ErrConflict)
	}
	if err := m.fencePrototypeCurrent(ctx, companyID, record.ProjectID, catalog); err != nil {
		return TODOActionResponse{}, todoTeach("prototype_delivery.currentness", "retry after the bound repository, main head, or canonical Knowledge identity stabilizes", ErrConflict)
	}
	proofValue := TODOPrototypeDeliveryProof{
		Version: prototypeDeliveryProofVersion, CompanyID: companyID, ProjectID: record.ProjectID, TODOID: record.ID,
		ContentDigest: contentDigest, PrototypeID: record.Content.Delivery.PrototypeID,
		RepositoryID: catalog.RepositoryID, BindingRevision: catalog.BindingRevision,
		PublicationBranch: catalog.PublicationBranch, Revision: catalog.Revision,
		InventoryDigest: catalog.InventoryDigest, EvaluatorVersion: catalog.EvaluatorVersion,
		KnowledgeGeneration: catalog.KnowledgeGeneration, KnowledgeDigest: catalog.KnowledgeDigest,
		VerifiedAt: time.Now().UTC(),
	}
	reference, err := encodePrototypeDeliveryProof(proofValue)
	if err != nil {
		return TODOActionResponse{}, todoTeach("prototype_delivery.proof", "encode the typed observation within the existing proof-reference bound", ErrInvalid)
	}
	proof := TODOProofReference{Kind: "prototype-delivery", Source: "mongo-prototype-api", Reference: reference, SourceDigest: contentDigest, VerificationState: "verified"}
	if m.afterPrototypeObservation != nil {
		m.afterPrototypeObservation()
	}
	return writeVerifiedTODO(ctx, collection.Database(), collection, companyID, record, "Completed", proof, request.OperationID, "transition", requestDigest)
}

func encodePrototypeDeliveryProof(value TODOPrototypeDeliveryProof) (string, error) {
	data, err := json.Marshal(value)
	if err != nil || len(data) > maxPrototypeDeliveryProofBytes {
		return "", fmt.Errorf("prototype proof exceeds limit")
	}
	hash := sha256.Sum256(append([]byte(prototypeDeliveryProofVersion+"\x00"), data...))
	encoded := prototypeDeliveryProofVersion + ":" + base64.RawURLEncoding.EncodeToString(data) + ":" + hex.EncodeToString(hash[:])
	if len(encoded) > maxPrototypeDeliveryProofBytes {
		return "", fmt.Errorf("encoded prototype proof exceeds limit")
	}
	return encoded, nil
}

func decodePrototypeDeliveryProof(reference string) (TODOPrototypeDeliveryProof, error) {
	var value TODOPrototypeDeliveryProof
	prefix := prototypeDeliveryProofVersion + ":"
	if len(reference) > maxPrototypeDeliveryProofBytes || len(reference) <= len(prefix) || reference[:len(prefix)] != prefix {
		return value, ErrInvalid
	}
	rest := reference[len(prefix):]
	separator := bytes.LastIndexByte([]byte(rest), ':')
	if separator < 1 {
		return value, ErrInvalid
	}
	encoded, providedDigest := rest[:separator], rest[separator+1:]
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(data) > maxPrototypeDeliveryProofBytes {
		return value, ErrInvalid
	}
	hash := sha256.Sum256(append([]byte(prototypeDeliveryProofVersion+"\x00"), data...))
	if providedDigest != hex.EncodeToString(hash[:]) {
		return value, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, ErrInvalid
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return value, ErrInvalid
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(canonical, data) || value.Version != prototypeDeliveryProofVersion || !validID(value.CompanyID) || !validID(value.ProjectID) || !validID(value.TODOID) || !validSourceDigest(value.ContentDigest) || !validStableArtifactID(value.PrototypeID) || !validGitRepositoryIdentity(value.RepositoryID) || value.PublicationBranch != "main" || !validFullGitSHA(value.Revision) || !validSourceDigest(value.InventoryDigest) || value.BindingRevision < 0 || value.EvaluatorVersion != prototypeEvaluatorVersion || value.KnowledgeDigest == "" || value.VerifiedAt.IsZero() {
		return TODOPrototypeDeliveryProof{}, ErrInvalid
	}
	return value, nil
}

func validateStoredPrototypeProof(ctx context.Context, database *mongo.Database, companyID string, record TODORecord, contentDigest string, stage, verification string, reference *TODOProofReference, revision int64) error {
	if record.Content == nil || record.Content.Delivery == nil || record.Content.Delivery.Kind != "prototype" || stage != "Completed" || verification != "verified" {
		return nil
	}
	if reference == nil || reference.Kind != "prototype-delivery" || reference.Source != "mongo-prototype-api" || reference.SourceDigest != contentDigest || reference.VerificationState != "verified" {
		return todoTeach("verified_transition_ref", "contain the server-verified Prototype observation for this exact content revision", ErrConflict)
	}
	proof, err := decodePrototypeDeliveryProof(reference.Reference)
	if err != nil || proof.CompanyID != companyID || proof.ProjectID != record.ProjectID || proof.TODOID != record.ID || proof.ContentDigest != contentDigest || proof.PrototypeID != record.Content.Delivery.PrototypeID {
		return todoTeach("verified_transition_ref.ref", "decode as the canonical typed Prototype tuple bound to this Company, Project, TODO, content digest and prototype ID", ErrConflict)
	}
	var event TODOHistoryEvent
	filter := bson.D{{Key: "project_id", Value: record.ProjectID}, {Key: "todo_id", Value: record.ID}, {Key: "kind", Value: "state_changed"}, {Key: "changes.stage", Value: "Completed"}, {Key: "changes.stage_verification", Value: "verified"}, {Key: "changes.verified_transition_ref.kind", Value: "prototype-delivery"}, {Key: "changes.verified_transition_ref.ref", Value: reference.Reference}, {Key: "changes.verified_transition_ref.source_digest", Value: contentDigest}, {Key: "revision", Value: bson.D{{Key: "$lte", Value: revision}}}}
	if err := database.Collection(todoEventsCollection).FindOne(ctx, filter, options.FindOne().SetSort(bson.D{{Key: "revision", Value: -1}})).Decode(&event); err != nil || event.Changes.VerifiedTransitionRef == nil || *event.Changes.VerifiedTransitionRef != *reference {
		return todoTeach("history.prototype_delivery", "match the immutable Completed event that records this exact server-generated proof", ErrConflict)
	}
	return nil
}
